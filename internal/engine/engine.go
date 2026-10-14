// Package engine renders a template's parts in one language with Go
// templates: text/template for text parts, html/template for html parts
// (contextual escaping), a closed function set with language-aware
// helpers, strict about missing data, and bounded so a template cannot
// exhaust the process. Shared pieces are other templates (named "_x")
// whose part of the same name a part includes with {{template "_x" .}}.
package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"runtime"
	"sort"
	"strings"
	texttemplate "text/template"
	"text/template/parse"
	"time"

	"golang.org/x/text/language"
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

// Errors of Compile and Render; every one is final but ErrBusy.
var (
	ErrNoParts    = errors.New("template: no parts")
	ErrPartName   = errors.New("template: part name must be [a-z][a-z0-9_]* and unique")
	ErrPartKind   = errors.New("template: part kind must be text or html")
	ErrPartTooBig = errors.New("template: part exceeds 256 KiB")
	ErrTooMany    = errors.New("template: too many parts")
	ErrParse      = errors.New("template: parse")
	ErrRender     = errors.New("template: render")
	ErrDataTooBig = errors.New("template: data exceeds 1 MiB")
	ErrTooLong    = errors.New("output exceeds 1 MiB")
	ErrTooSlow    = errors.New("template: render exceeded 2s")
	ErrLanguage   = errors.New("template: not a language tag")
	// ErrBusy: renders that ran past their time still hold every slot;
	// retrying later can succeed.
	ErrBusy = errors.New("template: too many renders running")
)

// Part is one output of a template in one language: its name, how it is
// escaped and its source.
type Part struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
}

// Partial is a shared piece in the language being compiled: a template
// whose parts other templates include by the template's name.
type Partial struct {
	Name  string
	Parts []Part
}

// Compiled is a template ready to render in one language.
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

// ParseLanguage is the canonical BCP 47 form of a tag ("ru", "pt-BR").
func ParseLanguage(s string) (string, error) {
	tag, err := language.Parse(s)
	if err != nil || s == "" {
		return "", fmt.Errorf("%w: %q", ErrLanguage, s)
	}

	return tag.String(), nil
}

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

// Compile parses every part of the template in lang with the partials it
// may include and checks what it may do (see safety.go). Problems come
// back one per failing part, joined.
func Compile(parts []Part, partials []Partial, lang string) (*Compiled, error) {
	if err := Check(parts); err != nil {
		return nil, err
	}

	tag, err := language.Parse(lang)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrLanguage, lang)
	}

	fns := funcs(tag)
	c := &Compiled{}

	var problems []error

	for _, p := range parts {
		part := compiledPart{name: p.Name}

		if p.Kind == KindHTML {
			part.html, err = compileHTML(p, partials, fns)
		} else {
			part.text, err = compileText(p, partials, fns)
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

// sources are the partials' sources for part p, then p's own.
func sources(p Part, partials []Partial) [][2]string {
	out := make([][2]string, 0, len(partials)+1)

	for _, partial := range partials {
		if src, ok := partialSource(partial, p.Name); ok {
			out = append(out, [2]string{partial.Name, src.Source})
		}
	}

	return append(out, [2]string{p.Name, p.Source})
}

// set is what text/template and html/template templates share.
type set[T any] interface {
	comparable
	New(name string) T
	Parse(text string) (T, error)
	Lookup(name string) T
	Templates() []T
}

// build parses the sources into root (the part's own into root, the
// partials as named templates) and checks what the set may do.
func build[T set[T]](root T, p Part, partials []Partial, tree func(T) *parse.Tree) error {
	var zero T

	srcs := sources(p, partials)
	for _, src := range srcs {
		target := root
		if src[0] != p.Name {
			target = root.New(src[0])
		}

		if _, err := target.Parse(src[1]); err != nil {
			return fmt.Errorf("%s: %w", src[0], err)
		}
	}

	defined := len(root.Templates())
	lookup := func(name string) *treeOf {
		if t := root.Lookup(name); t != zero && tree(t) != nil {
			return &treeOf{root: tree(t).Root, defined: defined}
		}

		return nil
	}

	return checkSet(p.Name, len(srcs), lookup)
}

func compileText(p Part, partials []Partial, fns map[string]any) (*texttemplate.Template, error) {
	t := texttemplate.New(p.Name).Funcs(fns).Option("missingkey=error")
	if err := build(t, p, partials, func(t *texttemplate.Template) *parse.Tree { return t.Tree }); err != nil {
		return nil, err
	}

	return t, nil
}

func compileHTML(p Part, partials []Partial, fns map[string]any) (*htmltemplate.Template, error) {
	t := htmltemplate.New(p.Name).Funcs(fns).Option("missingkey=error")
	if err := build(t, p, partials, func(t *htmltemplate.Template) *parse.Tree { return t.Tree }); err != nil {
		return nil, err
	}

	return t, nil
}

// limited is a buffer that refuses to grow past MaxOutputBytes.
type limited struct{ bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if l.Len()+len(p) > MaxOutputBytes {
		return 0, ErrTooLong
	}

	return l.Buffer.Write(p) //nolint:wrapcheck // a buffer does not fail
}

// slots bounds renders running at once, including those abandoned at
// RenderTimeout (a Go template cannot be stopped): a runaway template
// costs at most these goroutines, never an unbounded pile of them.
//
//nolint:gochecknoglobals // one bound for the process
var slots = make(chan struct{}, max(minSlots, slotsPerCPU*runtime.GOMAXPROCS(0)))

const (
	minSlots    = 4
	slotsPerCPU = 2
)

// Render executes every part with data; a failing part is a ProblemError.
// A part's output is bounded by MaxOutputBytes and the whole render by
// RenderTimeout.
func (c *Compiled) Render(data map[string]any) (map[string]string, error) {
	type result struct {
		out map[string]string
		err error
	}

	select {
	case slots <- struct{}{}:
	default:
		return nil, ErrBusy
	}

	done := make(chan result, 1)

	go func() {
		defer func() { <-slots }()

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

// DecodeData parses the JSON object data of a render and bounds what a
// template can iterate over (see safety.go).
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

	if err := CheckData(data); err != nil {
		return nil, err
	}

	return data, nil
}
