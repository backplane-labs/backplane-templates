// Package templates is the service's domain component: the Render
// activity over the installation's templates, with a cache of compiled
// versions that follows the active pointers.
package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/backplane-labs/backplane-templates/internal/engine"
	"github.com/backplane-labs/backplane-templates/internal/store"
	"github.com/backplane-labs/backplane-templates/internal/store/db"
)

const renderTimeout = 10 * time.Second

// Errors of Render; final ones are wrapped NonRetryable.
var (
	ErrNotFound    = errors.New("templates: no such template")
	ErrNotActive   = errors.New("templates: no version activated")
	ErrUnavailable = errors.New("templates: store unavailable")
)

// Settings of the component: what the service configuration decides.
type Settings struct {
	// Live: the locale a render falls back to (name@locale → name@default → name).
	DefaultLocale config.Live[string]
}

// Payloads of the activity.
type (
	// RenderIn names a template and gives it data.
	RenderIn struct {
		Template string `json:"template" schemapb:"description=Template name"`
		// Locale picks a variant: name@locale, then name@default, then name.
		Locale string         `json:"locale,omitempty"`
		Data   map[string]any `json:"data"             schemapb:"description=What the parts render: any JSON object"`
	}
	// RenderOut is every part rendered.
	RenderOut struct {
		Parts    map[string]string `json:"parts"    schemapb:"description=Rendered parts by name: subject, text, html, ..."`
		Template string            `json:"template" schemapb:"description=The variant that rendered"`
		Version  int64             `json:"version"`
	}
)

// Templates is the component.
type Templates struct {
	deps.Component

	set   Settings
	store deps.Dependency[*store.Store]

	mu    sync.Mutex
	cache map[string]cached

	renders  metric.Int64Counter
	duration metric.Float64Histogram
}

type cached struct {
	key      string
	version  int64
	compiled *engine.Compiled
}

// New declares the Render activity under parent.
func New(parent deps.Scope, st deps.Dependency[*store.Store], set Settings) (*Templates, error) {
	t := &Templates{Component: deps.NewComponent(parent, "templates"), set: set, store: st, cache: map[string]cached{}}

	activity.Handle(t, "Render", t.Render, activity.StartToClose(renderTimeout),
		activity.Describe("renders a template of the installation with data: every part, escaped by its kind"))

	var err error

	t.renders, err = t.Meter().Int64Counter("templates.renders", metric.WithDescription("Renders by template and outcome"))
	if err != nil {
		return nil, fmt.Errorf("templates metrics: %w", err)
	}

	t.duration, err = t.Meter().Float64Histogram("templates.render.duration", metric.WithUnit("s"),
		metric.WithDescription("Time a render took"))
	if err != nil {
		return nil, fmt.Errorf("templates metrics: %w", err)
	}

	return t, nil
}

// Settings as the component runs with them.
func (t *Templates) Settings() Settings { return t.set }

// Render is the templates.Render activity.
func (t *Templates) Render(ctx context.Context, in RenderIn) (RenderOut, error) {
	started := time.Now()

	out, err := t.render(ctx, in)

	outcome := "rendered"
	if err != nil {
		outcome = "failed"
	}

	attrs := metric.WithAttributes(attribute.String("template", in.Template), attribute.String("outcome", outcome))
	t.renders.Add(ctx, 1, attrs)
	t.duration.Record(ctx, time.Since(started).Seconds(), attrs)

	return out, err
}

func (t *Templates) render(ctx context.Context, in RenderIn) (RenderOut, error) {
	if in.Template == "" {
		return RenderOut{}, activity.NonRetryable(fmt.Errorf("%w: empty name", ErrNotFound)) //nolint:wrapcheck // final
	}

	if !t.store.Ready() {
		return RenderOut{}, fmt.Errorf("%w: %w", ErrUnavailable, t.store.Err())
	}

	for _, name := range t.candidates(in.Template, in.Locale) {
		c, err := t.compiled(ctx, name)
		if errors.Is(err, ErrNotFound) {
			continue
		}

		if err != nil {
			return RenderOut{}, err
		}

		data := in.Data
		if data == nil {
			data = map[string]any{}
		}

		parts, err := c.compiled.Render(data)
		if err != nil {
			return RenderOut{}, activity.NonRetryable(fmt.Errorf("%s: %w", name, err)) //nolint:wrapcheck // final
		}

		return RenderOut{Parts: parts, Template: name, Version: c.version}, nil
	}

	return RenderOut{}, activity.NonRetryable(fmt.Errorf("%w: %s", ErrNotFound, in.Template)) //nolint:wrapcheck // final
}

// candidates are the variants tried in order: name@locale, name@default, name.
func (t *Templates) candidates(name, locale string) []string {
	var out []string

	if locale != "" {
		out = append(out, name+"@"+locale)
	}

	if def := t.set.DefaultLocale.Get(); def != "" && def != locale {
		out = append(out, name+"@"+def)
	}

	return append(out, name)
}

// compiled is the template's active version, compiled with the active
// shared pieces; a cache entry is reused while none of them changed.
func (t *Templates) compiled(ctx context.Context, name string) (cached, error) {
	st := t.store.Get()

	active, err := st.Q.ActiveVersions(ctx, name)
	if err != nil {
		return cached{}, fmt.Errorf("templates: active versions: %w", err)
	}

	key, version, found := cacheKey(sortedByName(active), name)

	if !found {
		return cached{}, ErrNotFound
	}

	if version == 0 {
		return cached{}, activity.NonRetryable(fmt.Errorf("%w: %s", ErrNotActive, name)) //nolint:wrapcheck // final
	}

	t.mu.Lock()
	entry, ok := t.cache[name]
	t.mu.Unlock()

	if ok && entry.key == key {
		return entry, nil
	}

	row, err := st.Q.GetActiveVersion(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return cached{}, ErrNotFound
	}

	if err != nil {
		return cached{}, fmt.Errorf("templates: load %s: %w", name, err)
	}

	partials, loaded, err := activePartials(ctx, st)
	if err != nil {
		return cached{}, err
	}

	var parts []engine.Part
	if err := json.Unmarshal(row.Parts, &parts); err != nil {
		return cached{}, activity.NonRetryable(fmt.Errorf("templates: %s: parts: %w", name, err)) //nolint:wrapcheck // final
	}

	c, err := engine.Compile(parts, partials)
	if err != nil {
		return cached{}, activity.NonRetryable(fmt.Errorf("templates: %s: %w", name, err)) //nolint:wrapcheck // final
	}

	// The entry is keyed by what was loaded, not by what was asked: an
	// activation between the reads gives a key that differs from the next
	// lookup's, so the entry is rebuilt instead of serving a mix.
	loaded = append(loaded, db.ActiveVersionsRow{Name: name, ActiveVersion: &row.Version})
	key, _, _ = cacheKey(sortedByName(loaded), name)

	entry = cached{key: key, version: row.Version, compiled: c}

	t.mu.Lock()
	t.cache[name] = entry
	t.mu.Unlock()

	return entry, nil
}

// cacheKey is the active version of the template and of every shared
// piece, as one string: a compiled entry is valid while it is unchanged.
func cacheKey(active []db.ActiveVersionsRow, name string) (string, int64, bool) {
	var (
		key     strings.Builder
		version int64
		found   bool
	)

	for _, a := range active {
		v := int64(0)
		if a.ActiveVersion != nil {
			v = *a.ActiveVersion
		}

		if a.Name == name {
			found, version = true, v
		} else if v == 0 {
			continue // a shared piece not activated is not part of any render
		}

		fmt.Fprintf(&key, "%q=%d;", a.Name, v)
	}

	return key.String(), version, found
}

// Partials are the active shared pieces (templates named "_x").
func Partials(ctx context.Context, st *store.Store) ([]engine.Partial, error) {
	out, _, err := activePartials(ctx, st)

	return out, err
}

// activePartials are the shared pieces with the versions they were read at.
func activePartials(ctx context.Context, st *store.Store) ([]engine.Partial, []db.ActiveVersionsRow, error) {
	rows, err := st.Q.ListActivePartials(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("templates: partials: %w", err)
	}

	out := make([]engine.Partial, 0, len(rows))
	versions := make([]db.ActiveVersionsRow, 0, len(rows)+1)

	for _, r := range rows {
		var parts []engine.Part
		if err := json.Unmarshal(r.Parts, &parts); err != nil {
			return nil, nil, fmt.Errorf("templates: partial %s: %w", r.Name, err)
		}

		out = append(out, engine.Partial{Name: r.Name, Parts: parts})
		versions = append(versions, db.ActiveVersionsRow{Name: r.Name, ActiveVersion: &r.Version})
	}

	return out, versions, nil
}

// sortedByName orders rows as the store lists them, so keys compare.
func sortedByName(rows []db.ActiveVersionsRow) []db.ActiveVersionsRow {
	slices.SortFunc(rows, func(a, b db.ActiveVersionsRow) int { return strings.Compare(a.Name, b.Name) })

	return rows
}

// DecodeParts reads a version's parts.
func DecodeParts(raw []byte) ([]engine.Part, error) {
	var parts []engine.Part
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("templates: parts: %w", err)
	}

	return parts, nil
}
