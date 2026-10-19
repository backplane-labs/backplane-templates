package admin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/backplane-labs/backplane-templates/internal/admin"
	"github.com/backplane-labs/backplane-templates/internal/templates"
	templatesconsolev1 "github.com/backplane-labs/backplane-templates/proto/templates/console/v1"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for path, text := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadFiles(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		files map[string]string
		want  string // error substring; "" reads
	}{
		"templates": {files: map[string]string{
			"welcome/template.yaml":  "description: Hi\ndefault_language: en\nsample: {name: Ada}\n",
			"welcome/en/subject.txt": "Hi {{.name}}",
			"welcome/en/html.html":   "<p>{{.name}}</p>",
			"welcome/ru/subject.txt": "Привет {{.name}}",
			"welcome/ru/html.html":   "<p>{{.name}}</p>",
			"_footer/en/html.html":   "<small>bye</small>",
			"..data/ignored.txt":     "a ConfigMap's own",
		}},
		"no default":    {files: map[string]string{"a/de/s.txt": "x", "a/fr/s.txt": "y"}, want: "default language"},
		"missing part":  {files: map[string]string{"a/en/s.txt": "x", "a/ru/t.txt": "y"}, want: "every language"},
		"bad extension": {files: map[string]string{"a/en/s.md": "x"}, want: ".txt (text) or .html"},
		"bad name":      {files: map[string]string{"Bad/en/s.txt": "x"}, want: "dotted lower-case"},
		"stray file":    {files: map[string]string{"a/en/s.txt": "x", "a/notes.txt": "y"}, want: "template.yaml and <language>"},
		"unknown key":   {files: map[string]string{"a/en/s.txt": "x", "a/template.yaml": "descriptio: typo\n"}, want: "descriptio"},
		"kind clash":    {files: map[string]string{"a/en/s.txt": "x", "a/ru/s.html": "y"}, want: "text in one language"},
		"file at root":  {files: map[string]string{"readme.txt": "x"}, want: "only template directories"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			write(t, dir, c.files)

			set, err := admin.ReadFiles(dir, "en")

			switch {
			case c.want == "" && err != nil:
				t.Fatal(err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want %q", err, c.want)
			case c.want == "":
				w := set.Templates["welcome"]
				if len(set.Templates) != 2 || w.Description != "Hi" || w.Content.Default != "en" ||
					len(w.Content.Parts) != 2 || w.Content.Parts[0].Kind != "html" || w.Sample["name"] != "Ada" ||
					set.Templates["_footer"].Content.Default != "en" || set.Digest == "" {
					t.Fatalf("read: %+v", set)
				}
			}
		})
	}
}

// eventually polls cond until it holds or 10s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// The files source keeps its templates in force; the console only reads
// them.
func TestFilesSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, dir, map[string]string{
		"_footer/en/html.html":   "<small>bye</small>",
		"welcome/template.yaml":  "description: Greets\nsample: {name: Ada}\n",
		"welcome/en/subject.txt": "Hi {{.name}}",
		"welcome/en/html.html":   `<p>{{.name}}</p>{{template "_footer" .}}`,
		"broken/en/subject.txt":  "{{.name",
	})

	f := start(t, func(root deps.Scope, a *admin.Admin) { admin.NewFiles(root, a, dir, 20*time.Millisecond) })
	ctx := t.Context()

	get := func(name string) *templatesconsolev1.GetTemplateResponse {
		out, err := f.admin.GetTemplate(ctx, &templatesconsolev1.GetTemplateRequest{Name: name})
		if status.Code(err) == codes.NotFound {
			return nil
		}

		if err != nil {
			t.Fatal(err)
		}

		return out
	}

	eventually(t, "the templates from the files", func() bool { return get("welcome") != nil })

	w := get("welcome")
	if w.GetManagedBy() != "welcome" || w.GetActiveVersion() != 1 || w.GetDescription() != "Greets" ||
		w.GetContent().GetAuthor() != admin.FilesAuthor || w.GetContent().GetComment() != "from welcome/" {
		t.Fatalf("welcome: %v", w)
	}

	if get("broken") != nil {
		t.Fatal("a template that does not compile was saved")
	}

	out, err := f.render(t, templates.RenderIn{Template: "welcome", Data: map[string]any{"name": "Ada"}})
	if err != nil || out.Parts["html"] != "<p>Ada</p><small>bye</small>" {
		t.Fatalf("render: %v %v", out, err)
	}

	// The console reads, never writes, what the files keep.
	_, err = f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{
		Name: "welcome", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "x", "y")}, DefaultLanguage: "en",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("console save: %v", err)
	}

	if _, err := f.admin.DeleteTemplate(ctx, &templatesconsolev1.DeleteTemplateRequest{Name: "welcome"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("console delete: %v", err)
	}

	if _, err := f.admin.ActivateVersion(ctx, &templatesconsolev1.ActivateVersionRequest{Name: "welcome", Version: 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("console activate: %v", err)
	}

	// Unchanged files write nothing; a changed file is the next, active version.
	time.Sleep(200 * time.Millisecond)

	if v := get("welcome").GetActiveVersion(); v != 1 {
		t.Fatalf("unchanged files wrote v%d", v)
	}

	write(t, dir, map[string]string{"welcome/en/subject.txt": "Hello {{.name}}"})
	eventually(t, "the changed template", func() bool { return get("welcome").GetActiveVersion() == 2 })

	if v2, err := f.render(t, templates.RenderIn{Template: "welcome", Data: map[string]any{"name": "Ada"}}); err != nil ||
		v2.Parts["subject"] != "Hello Ada" {
		t.Fatalf("render v2: %v %v", v2, err)
	}

	// A directory that does not read whole changes nothing.
	write(t, dir, map[string]string{"stray.txt": "x"})
	time.Sleep(200 * time.Millisecond)

	if get("welcome").GetActiveVersion() != 2 || get("_footer") == nil {
		t.Fatal("a broken directory changed the templates")
	}

	// A removed directory deletes its template, and the console owns the name again.
	if err := os.Remove(filepath.Join(dir, "stray.txt")); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(dir, "welcome")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the removal", func() bool { return get("welcome") == nil })

	f.save(t, &templatesconsolev1.SaveTemplateRequest{
		Name: "welcome", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "x", "y")}, DefaultLanguage: "en",
	})
}
