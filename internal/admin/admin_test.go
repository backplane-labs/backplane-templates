package admin_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/backplane-labs/backplane-templates/internal/admin"
	"github.com/backplane-labs/backplane-templates/internal/store"
	"github.com/backplane-labs/backplane-templates/internal/templates"
	templatesconsolev1 "github.com/backplane-labs/backplane-templates/proto/templates/console/v1"
)

// isolatedDatabase creates a database of its own on BACKPLANE_TEST_PG.
func isolatedDatabase(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	ctx := t.Context()

	root, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	name := "templates_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		root.Close(ctx)
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, _ = root.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		root.Close(cleanup)
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}

	parsed.Path = "/" + name

	return parsed.String()
}

type liveConfig struct {
	Languages       config.Live[[]string] `json:"languages"`
	DefaultLanguage config.Live[string]   `json:"default_language" schemapb:"default=en"`
}

type fixture struct {
	h     *backplanetest.Harness
	admin *admin.Admin
}

func start(t *testing.T, also ...func(root deps.Scope, a *admin.Admin)) *fixture {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("templates"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(isolatedDatabase(t))}))
	live := backplanetest.Config[liveConfig](t, map[string]string{"LANGUAGES": `["ru","de","bogus tag"]`})

	tp, err := templates.New(h.Root(), st, templates.Settings{Languages: live.Languages, DefaultLanguage: live.DefaultLanguage})
	if err != nil {
		t.Fatal(err)
	}

	a := admin.New(h.Root(), st, tp)

	for _, fn := range also {
		fn(h.Root(), a)
	}

	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return &fixture{h: h, admin: a}
}

func (f *fixture) render(t *testing.T, in templates.RenderIn) (templates.RenderOut, error) {
	t.Helper()

	return backplanetest.Activity[templates.RenderIn, templates.RenderOut](t.Context(), f.h, "Render", in)
}

var mail = []*templatesconsolev1.Part{{Name: "subject", Kind: "text"}, {Name: "html", Kind: "html"}}

func tr(lang, subject, html string) *templatesconsolev1.Translation {
	return &templatesconsolev1.Translation{Language: lang, Sources: map[string]string{"subject": subject, "html": html}}
}

func (f *fixture) save(t *testing.T, req *templatesconsolev1.SaveTemplateRequest) *templatesconsolev1.SaveTemplateResponse {
	t.Helper()

	out, err := f.admin.SaveTemplate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// Templates are saved with their languages as one version, previewed per
// language, activated, and rendered in the language asked with fallback.
func TestTemplates(t *testing.T) {
	t.Parallel()

	f := start(t)
	ctx := t.Context()

	settings, err := f.admin.GetSettings(ctx, &templatesconsolev1.GetSettingsRequest{})
	if err != nil || strings.Join(settings.GetLanguages(), ",") != "en,ru,de" || settings.GetDefaultLanguage() != "en" {
		t.Fatalf("settings: %v %v", settings, err)
	}

	// A shared piece in two languages, then a template using it.
	f.save(t, &templatesconsolev1.SaveTemplateRequest{
		Name: "_footer", DefaultLanguage: "en",
		Parts: []*templatesconsolev1.Part{{Name: "html", Kind: "html"}},
		Translations: []*templatesconsolev1.Translation{
			{Language: "en", Sources: map[string]string{"html": "<p>{{.shop}}</p>"}},
			{Language: "ru", Sources: map[string]string{"html": "<p>Ваш {{.shop}}</p>"}},
		},
	})

	saved := f.save(t, &templatesconsolev1.SaveTemplateRequest{
		Name: "order.placed", Description: "Order confirmation", DefaultLanguage: "en", Parts: mail,
		Translations: []*templatesconsolev1.Translation{
			tr("en", `{{.n}} {{plural .n "item" "items"}}`, `<b>{{.name}}</b>{{template "_footer" .}}`),
			tr("ru", `{{.n}} {{plural .n "товар" "товара" "товаров"}}`, `<b>{{.name}}</b>{{template "_footer" .}}`),
		},
		Sample: `{"n": 2, "name": "Ann", "shop": "Shop"}`, Comment: "initial",
	})
	if saved.GetVersion() != 1 || !saved.GetActivated() || len(saved.GetProblems()) != 0 {
		t.Fatalf("save: %v", saved)
	}

	data := map[string]any{"n": 5, "name": "<b>", "shop": "S&P"}

	for asked, want := range map[string][2]string{
		"ru":    {"ru", "5 товаров"},
		"ru-RU": {"ru", "5 товаров"},
		"de":    {"en", "5 items"},
		"":      {"en", "5 items"},
		"x!":    {"en", "5 items"},
	} {
		out, err := f.render(t, templates.RenderIn{Template: "order.placed", Language: asked, Data: data})
		if err != nil || out.Language != want[0] || out.Parts["subject"] != want[1] || out.Version != 1 {
			t.Fatalf("render %q: %+v %v", asked, out, err)
		}
	}

	ru, _ := f.render(t, templates.RenderIn{Template: "order.placed", Language: "ru", Data: data})
	if ru.Parts["html"] != "<b>&lt;b&gt;</b><p>Ваш S&amp;P</p>" {
		t.Fatalf("ru html: %q", ru.Parts["html"])
	}

	// A second version: not activated, a sample problem in one language.
	saved = f.save(t, &templatesconsolev1.SaveTemplateRequest{
		Name: "order.placed", Description: "Order confirmation", DefaultLanguage: "en", Parts: mail,
		Translations: []*templatesconsolev1.Translation{tr("en", "{{.total}}", "x"), tr("ru", "{{.n}}", "x")},
		Sample:       `{"n": 1}`,
	})
	if saved.GetVersion() != 2 || saved.GetActivated() || len(saved.GetProblems()) != 1 ||
		saved.GetProblems()[0].GetLanguage() != "en" || saved.GetProblems()[0].GetPart() != "subject" {
		t.Fatalf("save v2: %v", saved)
	}

	if out, err := f.render(t, templates.RenderIn{Template: "order.placed", Data: data}); err != nil || out.Version != 1 {
		t.Fatalf("still v1: %+v %v", out, err)
	}

	if _, err := f.admin.ActivateVersion(ctx, &templatesconsolev1.ActivateVersionRequest{Name: "order.placed", Version: 2}); err != nil {
		t.Fatal(err)
	}

	if out, err := f.render(t, templates.RenderIn{Template: "order.placed", Language: "ru", Data: data}); err != nil || out.Version != 2 ||
		out.Parts["subject"] != "5" {
		t.Fatalf("v2: %+v %v", out, err)
	}

	// Missing data is a final failure naming the part.
	if _, err := f.render(t, templates.RenderIn{Template: "order.placed", Data: data}); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("missing: %v", err)
	}

	// Refused saves: bad name, shape, languages, sources that do not compile.
	for what, req := range map[string]*templatesconsolev1.SaveTemplateRequest{
		"name":    {Name: "Bad Name", DefaultLanguage: "en", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "a", "b")}},
		"no lang": {Name: "x", DefaultLanguage: "en", Parts: mail},
		"default": {Name: "x", DefaultLanguage: "de", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "a", "b")}},
		"tag":     {Name: "x", DefaultLanguage: "EN", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("EN", "a", "b")}},
		"missing": {Name: "x", DefaultLanguage: "en", Parts: mail, Translations: []*templatesconsolev1.Translation{{Language: "en", Sources: map[string]string{"subject": "a"}}}},
		"compile": {Name: "x", DefaultLanguage: "en", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "{{.x", "b")}},
		"range":   {Name: "x", DefaultLanguage: "en", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "{{range 9}}{{end}}", "b")}},
		"twice":   {Name: "x", DefaultLanguage: "en", Parts: mail, Translations: []*templatesconsolev1.Translation{tr("en", "a", "b"), tr("en", "a", "b")}},
	} {
		if _, err := f.admin.SaveTemplate(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: %v", what, err)
		}
	}

	// Preview renders one language and reports the rest.
	preview, err := f.admin.Preview(ctx, &templatesconsolev1.PreviewRequest{
		Parts: mail, Translation: tr("ru", `{{plural .n "товар" "товара" "товаров"}}`, "{{.missing}}"), Data: `{"n": 3}`,
	})
	if err != nil || preview.GetParts()["subject"] != "товара" || len(preview.GetProblems()) != 1 || preview.GetProblems()[0].GetPart() != "html" {
		t.Fatalf("preview: %v %v", preview, err)
	}

	list, err := f.admin.ListTemplates(ctx, &templatesconsolev1.ListTemplatesRequest{})
	if err != nil || len(list.GetTemplates()) != 2 || list.GetTemplates()[1].GetName() != "order.placed" ||
		strings.Join(list.GetTemplates()[1].GetLanguages(), ",") != "en,ru" {
		t.Fatalf("list: %v %v", list, err)
	}

	got, err := f.admin.GetTemplate(ctx, &templatesconsolev1.GetTemplateRequest{Name: "order.placed", Version: 1})
	if err != nil || got.GetActiveVersion() != 2 || len(got.GetVersions()) != 2 || got.GetContent().GetComment() != "initial" ||
		len(got.GetContent().GetTranslations()) != 2 || got.GetContent().GetTranslations()[1].GetLanguage() != "ru" {
		t.Fatalf("get v1: %v %v", got, err)
	}

	if del, err := f.admin.DeleteTemplate(ctx, &templatesconsolev1.DeleteTemplateRequest{Name: "order.placed"}); err != nil || !del.GetDeleted() {
		t.Fatalf("delete: %v %v", del, err)
	}

	if _, err := f.admin.GetTemplate(ctx, &templatesconsolev1.GetTemplateRequest{Name: "order.placed"}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted: %v", err)
	}
}

// The compiled cache follows activation of the template and of a shared
// piece, and ignores a shared piece that was never activated.
func TestCacheFollowsActivation(t *testing.T) {
	t.Parallel()

	f := start(t)
	ctx := t.Context()

	text := []*templatesconsolev1.Part{{Name: "text", Kind: "text"}}
	save := func(name, src string, activate bool) {
		t.Helper()
		f.save(t, &templatesconsolev1.SaveTemplateRequest{
			Name: name, DefaultLanguage: "en", Parts: text, Activate: activate,
			Translations: []*templatesconsolev1.Translation{{Language: "en", Sources: map[string]string{"text": src}}},
		})
	}

	save("_sig", "v1", true)
	save("_draft", "never", true)
	save("_draft", "x", false)
	save("note", `{{template "_sig" .}}`, true)

	render := func() string {
		t.Helper()

		out, err := f.render(t, templates.RenderIn{Template: "note"})
		if err != nil {
			t.Fatal(err)
		}

		return out.Parts["text"]
	}

	if got := render(); got != "v1" {
		t.Fatalf("first: %q", got)
	}

	save("_sig", "v2", true)

	if got := render(); got != "v2" {
		t.Fatalf("after partial activation: %q", got)
	}

	save("note", `[{{template "_sig" .}}]`, false)

	if got := render(); got != "v2" {
		t.Fatalf("inactive template version used: %q", got)
	}

	if _, err := f.admin.ActivateVersion(ctx, &templatesconsolev1.ActivateVersionRequest{Name: "note", Version: 2}); err != nil {
		t.Fatal(err)
	}

	if got := render(); got != "[v2]" {
		t.Fatalf("after template activation: %q", got)
	}
}
