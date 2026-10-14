package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane-templates/internal/engine"
)

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

	c, err := engine.Compile(parts, []engine.Partial{footer})
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
	_, err = engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: "{{.x"}, {Name: "b", Kind: "text", Source: "{{env \"HOME\"}}"}}, nil)

	problems := engine.Problems(err)
	if !errors.Is(err, engine.ErrParse) || len(problems) != 2 || problems[0].Part != "a" || !strings.Contains(problems[1].Message, "env") {
		t.Fatalf("parse: %v %v", err, problems)
	}

	for _, bad := range [][]engine.Part{{}, {{Name: "A", Kind: "text"}}, {{Name: "a", Kind: "md"}}, {{Name: "a", Kind: "text"}, {Name: "a", Kind: "text"}}} {
		if _, err := engine.Compile(bad, nil); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}

	// A template cannot exhaust the process: output is bounded, a range
	// over a number literal does not compile.
	big, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: "{{range .items}}{{$.pad}}{{end}}"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	items := make([]any, 200)
	if _, err := big.Render(map[string]any{"items": items, "pad": strings.Repeat("x", 8192)}); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("output bound: %v", err)
	}

	for _, src := range []string{"{{range 100000000000}}{{end}}", "{{if .x}}{{range $i := 1000000000}}{{end}}{{end}}"} {
		if _, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: src}}, nil); !errors.Is(err, engine.ErrParse) ||
			!strings.Contains(err.Error(), "number literal") {
			t.Fatalf("range literal %q: %v", src, err)
		}
	}

	// JSON numbers are float64: ranging over data cannot loop unbounded.
	loop, err := engine.Compile([]engine.Part{{Name: "a", Kind: "text", Source: "{{range .n}}x{{end}}"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := loop.Render(map[string]any{"n": float64(1e12)}); err == nil {
		t.Fatal("ranged over a float")
	}
}
