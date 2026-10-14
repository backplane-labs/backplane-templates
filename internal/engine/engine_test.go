package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane-templates/internal/engine"
)

func render(t *testing.T, lang, src string, data map[string]any) string {
	t.Helper()

	c, err := engine.Compile([]engine.Part{{Name: "a", Kind: engine.KindText, Source: src}}, nil, lang)
	if err != nil {
		t.Fatal(err)
	}

	out, err := c.Render(data)
	if err != nil {
		t.Fatal(err)
	}

	return out["a"]
}

func TestRender(t *testing.T) {
	t.Parallel()

	footer := engine.Partial{Name: "_footer", Parts: []engine.Part{
		{Name: "text", Kind: engine.KindText, Source: "-- {{.shop}}"},
		{Name: "html", Kind: engine.KindHTML, Source: "<p>{{.shop}}</p>"},
	}}
	parts := []engine.Part{
		{Name: "subject", Kind: engine.KindText, Source: "Order {{.order.id}} for {{.name | upper}}"},
		{Name: "text", Kind: engine.KindText, Source: "Hi {{.name}}, {{join \", \" .items}}\n{{template \"_footer\" .}}"},
		{Name: "html", Kind: engine.KindHTML, Source: "<b>{{.name}}</b> {{date \"2006-01-02\" .when}}{{template \"_footer\" .}}"},
	}

	c, err := engine.Compile(parts, []engine.Partial{footer}, "en")
	if err != nil {
		t.Fatal(err)
	}

	data, err := engine.DecodeData([]byte(`{"name":"<ann>","order":{"id":42},"items":["a","b"],"when":"2026-10-15T10:00:00Z","shop":"Shop & Co"}`))
	if err != nil {
		t.Fatal(err)
	}

	out, err := c.Render(data)
	if err != nil {
		t.Fatal(err)
	}

	if out["subject"] != "Order 42 for <ANN>" || out["text"] != "Hi <ann>, a, b\n-- Shop & Co" ||
		out["html"] != "<b>&lt;ann&gt;</b> 2026-10-15<p>Shop &amp; Co</p>" {
		t.Fatalf("rendered %q", out)
	}

	// Missing data is an error, named by part.
	_, err = c.Render(map[string]any{"name": "x", "order": map[string]any{}, "items": []any{}, "when": "2026-10-15T10:00:00Z", "shop": "s"})
	if !errors.Is(err, engine.ErrRender) || len(engine.Problems(err)) != 1 || engine.Problems(err)[0].Part != "subject" {
		t.Fatalf("missing key: %v %v", err, engine.Problems(err))
	}

	// A parse error names its part; the function set is closed.
	_, err = engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: "{{.x"}, {Name: "b", Kind: "text", Source: "{{env \"HOME\"}}"}}, nil, "en")

	problems := engine.Problems(err)
	if !errors.Is(err, engine.ErrParse) || len(problems) != 2 || problems[0].Part != "a" || !strings.Contains(problems[1].Message, "env") {
		t.Fatalf("parse: %v %v", err, problems)
	}

	for _, bad := range [][]engine.Part{{}, {{Name: "A", Kind: "text"}}, {{Name: "a", Kind: "md"}}, {{Name: "a", Kind: "text"}, {Name: "a", Kind: "text"}}} {
		if _, err := engine.Compile(bad, nil, "en"); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}

	if _, err := engine.Compile(parts[:1], nil, "not a tag!"); !errors.Is(err, engine.ErrLanguage) {
		t.Fatalf("language: %v", err)
	}
}

// Plural forms, numbers and dates follow the language.
func TestLanguage(t *testing.T) {
	t.Parallel()

	ru := `{{range .ns}}{{.}} {{plural . "товар" "товара" "товаров"}};{{end}}`
	if got := render(t, "ru", ru, map[string]any{"ns": []any{1.0, 2.0, 5.0, 11.0, 21.0, 22.0, 1.5}}); got != "1 товар;2 товара;5 товаров;11 товаров;21 товар;22 товара;1.5 товаров;" {
		t.Fatalf("ru plural: %q", got)
	}

	en := `{{range .ns}}{{plural . "item" "items"}};{{end}}`
	if got := render(t, "en", en, map[string]any{"ns": []any{1.0, 2.0, 0.0}}); got != "item;items;items;" {
		t.Fatalf("en plural: %q", got)
	}

	nums := `{{number .n}} {{number .n 2}}`
	for lang, want := range map[string]string{"en": "1,234,567 1,234,567.00", "ru": "1\u00a0234\u00a0567 1\u00a0234\u00a0567,00", "de": "1.234.567 1.234.567,00"} {
		if got := render(t, lang, nums, map[string]any{"n": 1234567.0}); got != want {
			t.Fatalf("%s number: %q want %q", lang, got, want)
		}
	}

	dates := `{{longdate .d}}|{{monthname .d}}|{{weekday .d}}`
	for lang, want := range map[string]string{
		"en": "October 15, 2026|October|Thursday", "ru": "15 октября 2026|октябрь|четверг", "de": "15. Oktober 2026|Oktober|Donnerstag",
		"ja": "October 15, 2026|October|Thursday",
	} {
		if got := render(t, lang, dates, map[string]any{"d": "2026-10-15T10:00:00Z"}); got != want {
			t.Fatalf("%s dates: %q want %q", lang, got, want)
		}
	}
}

// A template cannot loop without the data bounding it, recurse, or grow
// its output past the limit.
func TestSafety(t *testing.T) {
	t.Parallel()

	refused := map[string]error{
		"{{range 100000000000}}{{end}}":                                engine.ErrRangeSource,
		"{{$n := 1000000000}}{{range $n}}{{end}}":                      engine.ErrRangeSource,
		"{{with $n := 1000000000}}{{range $n}}{{end}}{{end}}":          engine.ErrRangeSource,
		"{{range $i, $e := .items}}{{range $i}}{{end}}{{end}}":         engine.ErrRangeSource,
		"{{range len .items}}{{end}}":                                  engine.ErrRangeSource,
		"{{range .a}}{{range .b}}{{range .c}}{{end}}{{end}}{{end}}":    engine.ErrRangeDepth,
		`{{define "x"}}{{template "x" .}}{{end}}{{template "x" .}}`:    engine.ErrDefine,
		`{{block "x" .}}{{end}}`:                                       engine.ErrDefine,
		`{{range .a}}{{range .b}}{{template "_loop" .}}{{end}}{{end}}`: engine.ErrRangeDepth,
	}

	loop := engine.Partial{Name: "_loop", Parts: []engine.Part{{Name: "a", Kind: "text", Source: "{{range .c}}{{end}}"}}}

	for src, want := range refused {
		_, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: src}}, []engine.Partial{loop}, "en")
		if !errors.Is(err, engine.ErrParse) || !strings.Contains(err.Error(), want.Error()) {
			t.Fatalf("%s: %v", src, err)
		}
	}

	// Shared pieces including each other in a cycle.
	a := engine.Partial{Name: "_a", Parts: []engine.Part{{Name: "a", Kind: "text", Source: `{{template "_b" .}}`}}}
	b := engine.Partial{Name: "_b", Parts: []engine.Part{{Name: "a", Kind: "text", Source: `{{template "_a" .}}`}}}

	if _, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: `{{template "_a" .}}`}}, []engine.Partial{a, b}, "en"); err == nil ||
		!strings.Contains(err.Error(), engine.ErrCycle.Error()) {
		t.Fatalf("cycle: %v", err)
	}

	// Allowed: ranges over data and data variables, two deep.
	ok := `{{$xs := .items}}{{range $i, $e := $xs}}{{$i}}{{range $e.tags}}{{.}}{{end}}{{end}}{{with .order}}{{range .lines}}{{.}}{{end}}{{end}}`
	if got := render(t, "en", ok, map[string]any{"items": []any{map[string]any{"tags": []any{"x"}}}, "order": map[string]any{"lines": []any{"l"}}}); got != "0xl" {
		t.Fatalf("allowed: %q", got)
	}

	// Helpers refuse to build what would exceed the output limit.
	for src, data := range map[string]map[string]any{
		`{{replace .s "" .r}}`:  {"s": strings.Repeat("x", 4096), "r": strings.Repeat("y", 4096)},
		`{{join .sep .items}}`:  {"sep": strings.Repeat("z", 8192), "items": make([]any, 1000)},
		`{{round 1.5 100000}}`:  {},
		`{{number 1.5 100000}}`: {},
	} {
		c, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: src}}, nil, "en")
		if err != nil {
			t.Fatal(err)
		}

		if _, err := c.Render(data); err == nil {
			t.Fatalf("%s rendered", src)
		}
	}

	// Output is bounded; data elements are bounded; JSON numbers cannot be ranged over.
	big, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: "{{range .items}}{{$.pad}}{{end}}"}}, nil, "en")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := big.Render(map[string]any{"items": make([]any, 200), "pad": strings.Repeat("x", 8192)}); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("output bound: %v", err)
	}

	if _, err := engine.DecodeData([]byte(`{"xs": [` + strings.Repeat("0,", engine.MaxDataItems) + `0]}`)); !errors.Is(err, engine.ErrDataItems) {
		t.Fatalf("items bound: %v", err)
	}

	loopy, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: "{{range .n}}x{{end}}"}}, nil, "en")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := loopy.Render(map[string]any{"n": float64(1e12)}); err == nil {
		t.Fatal("ranged over a float")
	}
}
