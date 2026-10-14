// Package templates is the service's domain component: the Render
// activity over the installation's templates in their languages, with a
// cache of compiled versions that follows the active pointers.
package templates

import (
	"context"
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

const (
	renderTimeout = 10 * time.Second
	// maxCached bounds compiled entries (template × language).
	maxCached = 4096
)

// Errors of Render; final ones are wrapped NonRetryable.
var (
	ErrNotFound    = errors.New("templates: no such template")
	ErrNotActive   = errors.New("templates: no version activated")
	ErrNoLanguage  = errors.New("templates: no translation for the language")
	ErrUnavailable = errors.New("templates: store unavailable")
)

// Settings of the component: what the service configuration decides.
type Settings struct {
	// Live: the languages the editor offers, in order.
	Languages config.Live[[]string]
	// Live: the language renders fall back to after the template's own.
	DefaultLanguage config.Live[string]
}

// Payloads of the activity.
type (
	// RenderIn names a template, a language and data.
	RenderIn struct {
		Template string `json:"template" schemapb:"description=Template name"`
		// Language: BCP 47 (ru, pt-BR); falls back to its base language,
		// then the template's default, then the installation's.
		Language string         `json:"language,omitempty"`
		Data     map[string]any `json:"data"               schemapb:"description=What the parts render: any JSON object"`
	}
	// RenderOut is every part rendered.
	RenderOut struct {
		Parts    map[string]string `json:"parts"    schemapb:"description=Rendered parts by name: subject, text, html, ..."`
		Language string            `json:"language" schemapb:"description=The language that rendered"`
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
	language string
	compiled *engine.Compiled
}

// New declares the Render activity under parent.
func New(parent deps.Scope, st deps.Dependency[*store.Store], set Settings) (*Templates, error) {
	t := &Templates{Component: deps.NewComponent(parent, "templates"), set: set, store: st, cache: map[string]cached{}}

	activity.Handle(t, "Render", t.Render, activity.StartToClose(renderTimeout),
		activity.Describe("renders a template of the installation in a language with data: every part, escaped by its kind"))

	var err error

	t.renders, err = t.Meter().Int64Counter("templates.renders",
		metric.WithDescription("Renders by template, language and outcome"))
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

	attrs := metric.WithAttributes(attribute.String("template", in.Template), attribute.String("language", out.Language),
		attribute.String("outcome", outcome))
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

	c, err := t.compiled(ctx, in.Template, in.Language)
	if err != nil {
		return RenderOut{}, err
	}

	data := in.Data
	if data == nil {
		data = map[string]any{}
	}

	if err := engine.CheckData(data); err != nil {
		return RenderOut{}, activity.NonRetryable(err) //nolint:wrapcheck // final
	}

	parts, err := c.compiled.Render(data)
	if errors.Is(err, engine.ErrBusy) {
		return RenderOut{Language: c.language}, fmt.Errorf("%s: %w", in.Template, err)
	}

	if err != nil {
		return RenderOut{Language: c.language}, activity.NonRetryable(fmt.Errorf("%s@%s: %w", in.Template, c.language, err)) //nolint:wrapcheck,lll // final
	}

	return RenderOut{Parts: parts, Language: c.language, Version: c.version}, nil
}

// compiled is the template's active version in the language picked for
// asked, compiled with the active shared pieces in that language; an
// entry is reused while none of their versions changed.
func (t *Templates) compiled(ctx context.Context, name, asked string) (cached, error) {
	st := t.store.Get()

	active, err := st.Q.ActiveVersions(ctx, name)
	if err != nil {
		return cached{}, fmt.Errorf("templates: active versions: %w", err)
	}

	key, version, found := cacheKey(sortedByName(active), name)
	if !found {
		return cached{}, activity.NonRetryable(fmt.Errorf("%w: %s", ErrNotFound, name)) //nolint:wrapcheck // final
	}

	if version == 0 {
		return cached{}, activity.NonRetryable(fmt.Errorf("%w: %s", ErrNotActive, name)) //nolint:wrapcheck // final
	}

	// The slot is per canonical language tag, and the cache is bounded:
	// callers' strings cannot grow it without limit.
	if canonical, err := engine.ParseLanguage(asked); err == nil {
		asked = canonical
	} else {
		asked = ""
	}

	slot := name + "|" + asked

	t.mu.Lock()
	entry, ok := t.cache[slot]
	t.mu.Unlock()

	if ok && entry.key == key {
		return entry, nil
	}

	entry, err = t.load(ctx, st, name, asked)
	if err != nil {
		return cached{}, err
	}

	t.mu.Lock()
	if len(t.cache) >= maxCached {
		clear(t.cache)
	}

	t.cache[slot] = entry
	t.mu.Unlock()

	return entry, nil
}

// load reads and compiles; the entry is keyed by what was loaded, not by
// what was asked: an activation between the reads gives a key that
// differs from the next lookup's, so the entry is rebuilt instead of
// serving a mix.
func (t *Templates) load(ctx context.Context, st *store.Store, name, asked string) (cached, error) {
	row, err := st.Q.GetActiveVersion(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return cached{}, activity.NonRetryable(fmt.Errorf("%w: %s", ErrNotFound, name)) //nolint:wrapcheck // final
	}

	if err != nil {
		return cached{}, fmt.Errorf("templates: load %s: %w", name, err)
	}

	content, err := DecodeContent(row.Parts, row.Translations, row.DefaultLanguage)
	if err != nil {
		return cached{}, activity.NonRetryable(err) //nolint:wrapcheck // final
	}

	lang := content.Pick(asked, content.Default, t.set.DefaultLanguage.Get())
	if lang == "" {
		err := fmt.Errorf("%w: %s %q", ErrNoLanguage, name, asked)

		return cached{}, activity.NonRetryable(err) //nolint:wrapcheck // final
	}

	partials, loaded, err := ActivePartials(ctx, st, lang)
	if err != nil {
		return cached{}, err
	}

	c, err := engine.Compile(content.In(lang), partials, lang)
	if err != nil {
		err = fmt.Errorf("templates: %s@%s: %w", name, lang, err)

		return cached{}, activity.NonRetryable(err) //nolint:wrapcheck // final
	}

	loaded = append(loaded, db.ActiveVersionsRow{Name: name, ActiveVersion: &row.Version})
	key, _, _ := cacheKey(sortedByName(loaded), name)

	return cached{key: key, version: row.Version, language: lang, compiled: c}, nil
}

// cacheKey is the active version of the template and of every activated
// shared piece, as one string: a compiled entry is valid while it is
// unchanged.
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

// ActivePartials are the active shared pieces (templates named "_x") in
// lang (or their closest language), with the versions they were read at.
func ActivePartials(
	ctx context.Context, st *store.Store, lang string,
) ([]engine.Partial, []db.ActiveVersionsRow, error) {
	rows, err := st.Q.ListActivePartials(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("templates: partials: %w", err)
	}

	out := make([]engine.Partial, 0, len(rows))
	versions := make([]db.ActiveVersionsRow, 0, len(rows)+1)

	for _, r := range rows {
		content, err := DecodeContent(r.Parts, r.Translations, r.DefaultLanguage)
		if err != nil {
			return nil, nil, fmt.Errorf("templates: partial %s: %w", r.Name, err)
		}

		out = append(out, engine.Partial{Name: r.Name, Parts: content.In(content.Pick(lang, content.Default))})
		versions = append(versions, db.ActiveVersionsRow{Name: r.Name, ActiveVersion: &r.Version})
	}

	return out, versions, nil
}

// sortedByName orders rows as the store lists them, so keys compare.
func sortedByName(rows []db.ActiveVersionsRow) []db.ActiveVersionsRow {
	slices.SortFunc(rows, func(a, b db.ActiveVersionsRow) int { return strings.Compare(a.Name, b.Name) })

	return rows
}
