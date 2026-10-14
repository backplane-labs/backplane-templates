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
	DefaultLocale config.Live[string] `json:"default_locale" schemapb:"default=en"`
}

type fixture struct {
	h     *backplanetest.Harness
	admin *admin.Admin
}

func start(t *testing.T) *fixture {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("templates"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(isolatedDatabase(t))}))
	live := backplanetest.Config[liveConfig](t, nil)

	tp, err := templates.New(h.Root(), st, templates.Settings{DefaultLocale: live.DefaultLocale})
	if err != nil {
		t.Fatal(err)
	}

	a := admin.New(h.Root(), st, tp)

	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return &fixture{h: h, admin: a}
}

func parts(subject, text, html string) []*templatesconsolev1.Part {
	out := []*templatesconsolev1.Part{{Name: "subject", Kind: "text", Source: subject}, {Name: "text", Kind: "text", Source: text}}
	if html != "" {
		out = append(out, &templatesconsolev1.Part{Name: "html", Kind: "html", Source: html})
	}

	return out
}

func (f *fixture) render(t *testing.T, in templates.RenderIn) (templates.RenderOut, error) {
	t.Helper()

	return backplanetest.Activity[templates.RenderIn, templates.RenderOut](t.Context(), f.h, "Render", in)
}

// Templates are saved as versions, previewed, activated and rendered with
// shared pieces and locale variants; the cache follows activation.
func TestTemplates(t *testing.T) {
	t.Parallel()

	f := start(t)
	ctx := t.Context()

	// A shared piece first, then a template using it; the first version activates.
	if _, err := f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{
		Name:  "_footer",
		Parts: []*templatesconsolev1.Part{{Name: "text", Kind: "text", Source: "-- {{.shop}}"}, {Name: "html", Kind: "html", Source: "<p>{{.shop}}</p>"}},
	}); err != nil {
		t.Fatal(err)
	}

	saved, err := f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{
		Name: "order.placed", Description: "Order confirmation",
		Parts:  parts("Order {{.id}}", "Hi {{.name}}\n{{template \"_footer\" .}}", "<b>{{.name}}</b>{{template \"_footer\" .}}"),
		Sample: `{"id": 42, "name": "Ann", "shop": "Shop"}`, Comment: "initial",
	})
	if err != nil || saved.GetVersion() != 1 || !saved.GetActivated() || len(saved.GetProblems()) != 0 {
		t.Fatalf("save: %v %v", saved, err)
	}

	out, err := f.render(t, templates.RenderIn{Template: "order.placed", Data: map[string]any{"id": 7, "name": "<b>", "shop": "S&P"}})
	if err != nil || out.Version != 1 || out.Template != "order.placed" || out.Parts["subject"] != "Order 7" ||
		out.Parts["text"] != "Hi <b>\n-- S&P" || out.Parts["html"] != "<b>&lt;b&gt;</b><p>S&amp;P</p>" {
		t.Fatalf("render: %+v %v", out, err)
	}

	// A second version with a sample problem is saved, not activated; the
	// render still uses v1 until activation, then the cache follows.
	saved, err = f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{
		Name: "order.placed", Description: "Order confirmation", Parts: parts("Order {{.id}} ({{.total}})", "Hi", ""),
		Sample: `{"id": 1}`,
	})
	if err != nil || saved.GetVersion() != 2 || saved.GetActivated() || len(saved.GetProblems()) != 1 || saved.GetProblems()[0].GetPart() != "subject" {
		t.Fatalf("save v2: %v %v", saved, err)
	}

	if still, err := f.render(t, templates.RenderIn{Template: "order.placed", Data: map[string]any{"id": 1, "name": "x", "shop": "s"}}); err != nil || still.Version != 1 {
		t.Fatalf("still v1: %+v %v", still, err)
	}

	if _, err := f.admin.ActivateVersion(ctx, &templatesconsolev1.ActivateVersionRequest{Name: "order.placed", Version: 2}); err != nil {
		t.Fatal(err)
	}

	out, err = f.render(t, templates.RenderIn{Template: "order.placed", Data: map[string]any{"id": 1, "total": "9.99"}})
	if err != nil || out.Version != 2 || out.Parts["subject"] != "Order 1 (9.99)" {
		t.Fatalf("v2: %+v %v", out, err)
	}

	// Missing data is a final failure naming the part.
	if _, err := f.render(t, templates.RenderIn{Template: "order.placed", Data: map[string]any{"id": 1}}); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("missing: %v", err)
	}

	// Locale variants: name@ru, then name@default (en), then name.
	for _, name := range []string{"order.placed@ru", "order.placed@en"} {
		if _, err := f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{Name: name, Parts: parts(name, "x", "")}); err != nil {
			t.Fatal(err)
		}
	}

	for locale, want := range map[string]string{"ru": "order.placed@ru", "de": "order.placed@en", "": "order.placed@en"} {
		if variant, err := f.render(t, templates.RenderIn{Template: "order.placed", Locale: locale}); err != nil || variant.Template != want {
			t.Fatalf("locale %q: %+v %v", locale, variant, err)
		}
	}

	// Unknown template, bad name, a part that does not parse.
	if _, err := f.render(t, templates.RenderIn{Template: "nope"}); err == nil {
		t.Fatal("unknown rendered")
	}

	if _, err := f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{Name: "Bad Name", Parts: parts("x", "y", "")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad name: %v", err)
	}

	if _, err := f.admin.SaveTemplate(ctx, &templatesconsolev1.SaveTemplateRequest{Name: "broken", Parts: parts("{{.x", "y", "")}); status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("parse: %v", err)
	}

	// Preview renders what it can and reports the rest; the list and get show the state.
	preview, err := f.admin.Preview(ctx, &templatesconsolev1.PreviewRequest{Parts: parts("A {{.a}}", "B {{.b}}", ""), Data: `{"a": 1}`})
	if err != nil || preview.GetParts()["subject"] != "A 1" || len(preview.GetProblems()) != 1 || preview.GetProblems()[0].GetPart() != "text" {
		t.Fatalf("preview: %v %v", preview, err)
	}

	list, err := f.admin.ListTemplates(ctx, &templatesconsolev1.ListTemplatesRequest{})
	if err != nil || len(list.GetTemplates()) != 4 || list.GetTemplates()[0].GetName() != "_footer" {
		t.Fatalf("list: %v %v", list, err)
	}

	got, err := f.admin.GetTemplate(ctx, &templatesconsolev1.GetTemplateRequest{Name: "order.placed"})
	if err != nil || got.GetActiveVersion() != 2 || len(got.GetVersions()) != 2 || got.GetContent().GetVersion() != 2 ||
		got.GetDescription() != "Order confirmation" || got.GetContent().GetSample() != `{"id": 1}` {
		t.Fatalf("get: %v %v", got, err)
	}

	if old, err := f.admin.GetTemplate(ctx, &templatesconsolev1.GetTemplateRequest{Name: "order.placed", Version: 1}); err != nil || old.GetContent().GetComment() != "initial" {
		t.Fatalf("get v1: %v %v", old, err)
	}

	if del, err := f.admin.DeleteTemplate(ctx, &templatesconsolev1.DeleteTemplateRequest{Name: "order.placed@ru"}); err != nil || !del.GetDeleted() {
		t.Fatalf("delete: %v %v", del, err)
	}

	if _, err := f.admin.GetTemplate(ctx, &templatesconsolev1.GetTemplateRequest{Name: "order.placed@ru"}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted: %v", err)
	}
}
