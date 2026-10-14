// Package engine renders a template's parts with Go templates: text/template
// for text parts, html/template for html parts (contextual escaping), a
// small safe function set, strict about missing data. Shared pieces are
// other templates (named "_x") whose part of the same name is defined into
// the set, so a part says {{template "_x" .}}.
package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"
)

// Kinds of a part.
const (
	KindText = "text"
	KindHTML = "html"
)

// Limits of what the engine accepts.
const (
	MaxPartBytes   = 256 << 10
	MaxParts       = 16
	MaxDataBytes   = 1 << 20
	MaxOutputBytes = 1 << 20
	// RenderTimeout bounds one render of every part.
	RenderTimeout = 2 * time.Second
)

// Errors of Compile and Render; every one is final.
var (
	ErrNoParts     = errors.New("template: no parts")
	ErrPartName    = errors.New("template: part name must be [a-z][a-z0-9_]* and unique")
	ErrPartKind    = errors.New("template: part kind must be text or html")
	ErrPartTooBig  = errors.New("template: part exceeds 256 KiB")
	ErrTooMany     = errors.New("template: too many parts")
	ErrParse       = errors.New("template: parse")
	ErrRender      = errors.New("template: render")
	ErrDataTooBig  = errors.New("template: data exceeds 1 MiB")
	ErrPartialKind = errors.New("template: a shared piece needs a part of the same name")
	ErrTooLong     = errors.New("output exceeds 1 MiB")
	ErrTooSlow     = errors.New("template: render exceeded 2s")
)

// limited is a buffer that refuses to grow past MaxOutputBytes.
type limited struct{ bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if l.Len()+len(p) > MaxOutputBytes {
		return 0, ErrTooLong
	}

	return l.Buffer.Write(p) //nolint:wrapcheck // a buffer does not fail
}

// Part is one output of a template: its name, how it is escaped and its
// source.
type Part struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

// Partial is a shared piece: a template whose parts other templates
// include by the template's name.
type Partial struct {
	Name  string
	Parts []Part
}

// Compiled is a template ready to render.
type Compiled struct {
	parts []compiledPart
}

type compiledPart struct {
	name string
	text *texttemplate.Template
	html *htmltemplate.Template
}

// ProblemError is what a part is wrong about: for the console's editor.
type ProblemError struct {
	Part    string `json:"part"`
	Message string `json:"message"`
}

func (p ProblemError) Error() string { return p.Part + ": " + p.Message }

// Check validates the parts' shape without compiling.
func Check(parts []Part) error {
	if len(parts) == 0 {
		return ErrNoParts
	}

	if len(parts) > MaxParts {
		return ErrTooMany
	}

	seen := map[string]bool{}

	for _, p := range parts {
		if !validName(p.Name) || seen[p.Name] {
			return fmt.Errorf("%w: %q", ErrPartName, p.Name)
		}

		seen[p.Name] = true

		if p.Kind != KindText && p.Kind != KindHTML {
			return fmt.Errorf("%w: %s", ErrPartKind, p.Name)
		}

		if len(p.Source) > MaxPartBytes {
			return fmt.Errorf("%w: %s", ErrPartTooBig, p.Name)
		}
	}

	return nil
}

func validName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}

	for _, r := range s[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}

	return true
}

// Compile parses every part of the template with the partials it may
// include. Parse errors come back as ProblemErrors (one per failing part),
// joined.
func Compile(parts []Part, partials []Partial) (*Compiled, error) {
	if err := Check(parts); err != nil {
		return nil, err
	}

	c := &Compiled{}

	var problems []error

	for _, p := range parts {
		part := compiledPart{name: p.Name}

		var err error

		if p.Kind == KindHTML {
			part.html, err = compileHTML(p, partials)
		} else {
			part.text, err = compileText(p, partials)
		}

		if err != nil {
			problems = append(problems, ProblemError{Part: p.Name, Message: strings.TrimPrefix(err.Error(), "template: ")})

			continue
		}

		c.parts = append(c.parts, part)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrParse, errors.Join(problems...))
	}

	return c, nil
}

// partialSource is the partial's part of name, else its only part.
func partialSource(partial Partial, name string) (Part, bool) {
	for _, p := range partial.Parts {
		if p.Name == name {
			return p, true
		}
	}

	if len(partial.Parts) == 1 {
		return partial.Parts[0], true
	}

	return Part{}, false
}

func compileText(p Part, partials []Partial) (*texttemplate.Template, error) {
	t := texttemplate.New(p.Name).Funcs(textFuncs()).Option("missingkey=error")

	for _, partial := range partials {
		src, ok := partialSource(partial, p.Name)
		if !ok {
			continue
		}

		if _, err := t.New(partial.Name).Parse(src.Source); err != nil {
			return nil, fmt.Errorf("%s: %w", partial.Name, err)
		}
	}

	if _, err := t.Parse(p.Source); err != nil {
		return nil, err //nolint:wrapcheck // the parser names the part
	}

	return t, nil
}

func compileHTML(p Part, partials []Partial) (*htmltemplate.Template, error) {
	t := htmltemplate.New(p.Name).Funcs(htmlFuncs()).Option("missingkey=error")

	for _, partial := range partials {
		src, ok := partialSource(partial, p.Name)
		if !ok {
			continue
		}

		if _, err := t.New(partial.Name).Parse(src.Source); err != nil {
			return nil, fmt.Errorf("%s: %w", partial.Name, err)
		}
	}

	if _, err := t.Parse(p.Source); err != nil {
		return nil, err //nolint:wrapcheck // the parser names the part
	}

	return t, nil
}

// Render executes every part with data; a failing part is a ProblemError.
// A part's output is bounded by MaxOutputBytes and the whole render by
// RenderTimeout: a template cannot exhaust the process with its loops.
func (c *Compiled) Render(data map[string]any) (map[string]string, error) {
	type result struct {
		out map[string]string
		err error
	}

	done := make(chan result, 1)

	go func() {
		out, err := c.render(data)
		done <- result{out, err}
	}()

	timer := time.NewTimer(RenderTimeout)
	defer timer.Stop()

	select {
	case r := <-done:
		return r.out, r.err
	case <-timer.C:
		return nil, ErrTooSlow
	}
}

func (c *Compiled) render(data map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(c.parts))

	var problems []error

	for _, p := range c.parts {
		var (
			buf limited
			err error
		)

		if p.html != nil {
			err = p.html.ExecuteTemplate(&buf, p.name, data)
		} else {
			err = p.text.ExecuteTemplate(&buf, p.name, data)
		}

		if err != nil {
			problems = append(problems, ProblemError{Part: p.name, Message: strings.TrimPrefix(err.Error(), "template: ")})

			continue
		}

		out[p.name] = buf.String()
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrRender, errors.Join(problems...))
	}

	return out, nil
}

// Problems lists the problems an error carries (parse or render), in
// part order.
func Problems(err error) []ProblemError {
	var out []ProblemError

	var collect func(err error)

	collect = func(err error) {
		var p ProblemError
		if errors.As(err, &p) && !isJoin(err) {
			out = append(out, p)

			return
		}

		if j, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range j.Unwrap() {
				collect(e)
			}

			return
		}

		if u := errors.Unwrap(err); u != nil {
			collect(u)
		}
	}

	collect(err)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Part < out[j].Part })

	return out
}

func isJoin(err error) bool {
	_, ok := err.(interface{ Unwrap() []error })

	return ok
}

// DecodeData parses the JSON object data of a render.
func DecodeData(raw []byte) (map[string]any, error) {
	if len(raw) > MaxDataBytes {
		return nil, ErrDataTooBig
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("template: data: %w", err)
	}

	if data == nil {
		data = map[string]any{}
	}

	return data, nil
}

// The function set: formatting only, nothing that reaches the process.
func funcs() map[string]any {
	return map[string]any{
		"upper":   strings.ToUpper,
		"lower":   strings.ToLower,
		"title":   title,
		"trim":    strings.TrimSpace,
		"replace": strings.ReplaceAll,
		"join":    join,
		"default": defaultOf,
		"date":    date,
		"json":    toJSON,
		"add":     func(a, b float64) float64 { return a + b },
		"mul":     func(a, b float64) float64 { return a * b },
		"round":   func(v float64, digits int) string { return fmt.Sprintf("%.*f", digits, v) },
	}
}

func textFuncs() texttemplate.FuncMap { return funcs() }
func htmlFuncs() htmltemplate.FuncMap { return funcs() }

func title(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}

	return strings.Join(words, " ")
}

func join(sep string, items any) string {
	switch v := items.(type) {
	case []string:
		return strings.Join(v, sep)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = fmt.Sprint(item)
		}

		return strings.Join(parts, sep)
	default:
		return fmt.Sprint(items)
	}
}

func defaultOf(fallback, value any) any {
	switch v := value.(type) {
	case nil:
		return fallback
	case string:
		if v == "" {
			return fallback
		}
	case float64:
		if v == 0 {
			return fallback
		}
	case bool:
		if !v {
			return fallback
		}
	}

	return value
}

// date formats a time.Time or an RFC 3339 string with a Go layout.
func date(layout string, value any) (string, error) {
	switch v := value.(type) {
	case time.Time:
		return v.Format(layout), nil
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return "", fmt.Errorf("date: %w", err)
		}

		return t.Format(layout), nil
	default:
		return "", fmt.Errorf("date: %T is not a time", value) //nolint:err113 // formatted
	}
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("json: %w", err)
	}

	return string(b), nil
}
