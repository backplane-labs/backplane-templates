// Package admin is templates' internal API: the console module's editor,
// served as gRPC on the platform port behind the internal secret and
// relayed by backplane under the operator's session. Every change is an
// audit record.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane-templates/internal/engine"
	"github.com/backplane-labs/backplane-templates/internal/store"
	"github.com/backplane-labs/backplane-templates/internal/store/db"
	"github.com/backplane-labs/backplane-templates/internal/templates"
	templatesconsolev1 "github.com/backplane-labs/backplane-templates/proto/templates/console/v1"
)

// Template names: dotted words, a leading "_" for a shared piece.
var namePattern = regexp.MustCompile(`^_?[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

const maxDescription = 1024

// Admin serves AdminService.
type Admin struct {
	templatesconsolev1.UnimplementedAdminServiceServer
	deps.Component

	store     deps.Dependency[*store.Store]
	templates *templates.Templates
}

// New creates the admin component under parent.
func New(parent deps.Scope, st deps.Dependency[*store.Store], t *templates.Templates) *Admin {
	return &Admin{Component: deps.NewComponent(parent, "admin"), store: st, templates: t}
}

// GetSettings implements AdminService.
func (a *Admin) GetSettings(
	_ context.Context, _ *templatesconsolev1.GetSettingsRequest,
) (*templatesconsolev1.GetSettingsResponse, error) {
	set := a.templates.Settings()
	def := set.DefaultLanguage.Get()

	langs := []string{def}

	for _, l := range set.Languages.Get() {
		if canonical, err := engine.ParseLanguage(l); err == nil && canonical != def && !contains(langs, canonical) {
			langs = append(langs, canonical)
		}
	}

	return &templatesconsolev1.GetSettingsResponse{Languages: langs, DefaultLanguage: def}, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}

	return false
}

// ListTemplates implements AdminService.
func (a *Admin) ListTemplates(
	ctx context.Context, _ *templatesconsolev1.ListTemplatesRequest,
) (*templatesconsolev1.ListTemplatesResponse, error) {
	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	rows, err := st.Q.ListTemplates(ctx)
	if err != nil {
		return nil, a.failed("list", err)
	}

	out := &templatesconsolev1.ListTemplatesResponse{}

	for i := range rows {
		r := &rows[i]
		summary := &templatesconsolev1.TemplateSummary{
			Name: r.Name, Description: r.Description, Versions: r.Versions, UpdatedAt: timestamppb.New(r.UpdatedAt),
			ManagedBy: deref(r.ManagedBy),
		}

		if r.ActiveVersion != nil {
			summary.ActiveVersion = *r.ActiveVersion
		}

		if r.ActiveDefault != nil {
			if content, err := templates.DecodeContent(r.ActiveParts, r.ActiveTranslations, *r.ActiveDefault); err == nil {
				for _, p := range content.Parts {
					summary.Parts = append(summary.Parts, p.Name)
				}

				summary.Languages, summary.DefaultLanguage = content.Languages(), content.Default
				summary.Incomplete = content.Incomplete()
			}
		}

		out.Templates = append(out.Templates, summary)
	}

	return out, nil
}

// GetTemplate implements AdminService.
func (a *Admin) GetTemplate(
	ctx context.Context, req *templatesconsolev1.GetTemplateRequest,
) (*templatesconsolev1.GetTemplateResponse, error) {
	if err := checkName(req.GetName()); err != nil {
		return nil, err
	}

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	t, err := st.Q.GetTemplate(ctx, req.GetName())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "template %q does not exist", req.GetName())
	}

	if err != nil {
		return nil, a.failed("get", err)
	}

	out := &templatesconsolev1.GetTemplateResponse{Name: t.Name, Description: t.Description, ManagedBy: deref(t.ManagedBy)}
	if t.ActiveVersion != nil {
		out.ActiveVersion = *t.ActiveVersion
	}

	versions, err := st.Q.ListVersions(ctx, t.Name)
	if err != nil {
		return nil, a.failed("list versions", err)
	}

	for _, v := range versions {
		out.Versions = append(out.Versions, &templatesconsolev1.VersionSummary{
			Version: v.Version, Author: v.Author, Comment: v.Comment, CreatedAt: timestamppb.New(v.CreatedAt),
		})
	}

	want := req.GetVersion()
	if want == 0 {
		want = out.GetActiveVersion()
	}

	if want == 0 && len(versions) > 0 {
		want = versions[0].Version
	}

	if want == 0 {
		return out, nil
	}

	if out.Content, err = a.version(ctx, st, t.Name, want); err != nil {
		return nil, err
	}

	return out, nil
}

func (a *Admin) version(
	ctx context.Context, st *store.Store, name string, want int64,
) (*templatesconsolev1.Version, error) {
	row, err := st.Q.GetVersion(ctx, db.GetVersionParams{Name: name, Version: want})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "template %q has no version %d", name, want)
	}

	if err != nil {
		return nil, a.failed("get version", err)
	}

	content, err := templates.DecodeContent(row.Parts, row.Translations, row.DefaultLanguage)
	if err != nil {
		return nil, a.failed("decode version", err)
	}

	v := &templatesconsolev1.Version{
		Version: row.Version, DefaultLanguage: content.Default, Sample: string(row.Sample), Author: row.Author,
		Comment: row.Comment, CreatedAt: timestamppb.New(row.CreatedAt),
	}

	for _, p := range content.Parts {
		v.Parts = append(v.Parts, &templatesconsolev1.Part{Name: p.Name, Kind: p.Kind})
	}

	for _, lang := range content.Languages() {
		v.Translations = append(v.Translations,
			&templatesconsolev1.Translation{Language: lang, Sources: content.Translations[lang]})
	}

	return v, nil
}

// SaveTemplate implements AdminService.
func (a *Admin) SaveTemplate(
	ctx context.Context, req *templatesconsolev1.SaveTemplateRequest,
) (*templatesconsolev1.SaveTemplateResponse, error) {
	content, sample, err := checkSave(req)
	if err != nil {
		return nil, err
	}

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	problems, err := a.compileAll(ctx, st, content, sample)
	if err != nil {
		return nil, err
	}

	partsJSON, translationsJSON, err := content.Encode()
	if err != nil {
		return nil, a.failed("encode", err)
	}

	sampleJSON, err := json.Marshal(sample)
	if err != nil {
		return nil, a.failed("encode sample", err)
	}

	out := &templatesconsolev1.SaveTemplateResponse{Problems: problems}

	err = st.InTx(ctx, func(ctx context.Context) error {
		if err := consoleOwns(ctx, st, req.GetName()); err != nil {
			return err
		}

		err := st.Q.UpsertTemplate(ctx, db.UpsertTemplateParams{Name: req.GetName(), Description: req.GetDescription()})
		if err != nil {
			return fmt.Errorf("upsert: %w", err)
		}

		next, err := st.Q.NextVersion(ctx, req.GetName())
		if err != nil {
			return fmt.Errorf("next version: %w", err)
		}

		out.Version = next.Version

		if err := st.Q.InsertVersion(ctx, db.InsertVersionParams{
			Name: req.GetName(), Version: next.Version, Parts: partsJSON, Translations: translationsJSON,
			DefaultLanguage: content.Default, Sample: sampleJSON, Author: "console", Comment: req.GetComment(),
		}); err != nil {
			return fmt.Errorf("insert version: %w", err)
		}

		if req.GetActivate() || next.Version == 1 {
			_, err := st.Q.ActivateVersion(ctx, db.ActivateVersionParams{Name: req.GetName(), Version: next.Version})
			if err != nil {
				return fmt.Errorf("activate: %w", err)
			}

			out.Activated = true
		}

		return nil
	})
	if err != nil {
		return nil, a.changeFailed("save", err)
	}

	a.audit(ctx, "template.saved", req.GetName(), xlog.Int64("version", out.GetVersion()),
		xlog.Bool("activated", out.GetActivated()), xlog.Int("problems", len(problems)),
		xlog.String("languages", strings.Join(content.Languages(), ",")))

	return out, nil
}

// compileAll compiles every language (a failure refuses the save) and
// renders the sample in each (a failure is a problem reported).
func (a *Admin) compileAll(
	ctx context.Context, st *store.Store, content templates.Content, sample map[string]any,
) ([]*templatesconsolev1.Problem, error) {
	var (
		problems []*templatesconsolev1.Problem
		refused  []string
	)

	for _, lang := range content.Languages() {
		partials, _, err := templates.ActivePartials(ctx, st, lang)
		if err != nil {
			return nil, a.failed("partials", err)
		}

		compiled, err := engine.Compile(content.In(lang), partials, lang)
		if err != nil {
			for _, p := range problemsOf(lang, err) {
				refused = append(refused, lang+"/"+p.GetPart()+": "+p.GetMessage())
			}

			if len(engine.Problems(err)) == 0 {
				refused = append(refused, lang+": "+err.Error())
			}

			continue
		}

		if _, err := compiled.Render(sample); err != nil {
			problems = append(problems, problemsOf(lang, err)...)
		}
	}

	if len(refused) > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s", "sources do not compile; "+strings.Join(refused, "; "))
	}

	return problems, nil
}

// errManaged: the template is kept by the files source, which alone
// changes it.
var errManaged = errors.New("the template is kept by the files source")

// consoleOwns is errManaged when the files source keeps the template
// (run it in the transaction of the change).
func consoleOwns(ctx context.Context, st *store.Store, name string) error {
	t, err := st.Q.GetTemplate(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	if dir := deref(t.ManagedBy); dir != "" {
		return fmt.Errorf("%w (%s); change its files", errManaged, dir)
	}

	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// checkName refuses a name that is not a template name.
func checkName(name string) error {
	if !namePattern.MatchString(name) {
		return status.Errorf(codes.InvalidArgument, "%s",
			"name must be dotted lower-case words; a shared piece starts with _")
	}

	return nil
}

// checkSave validates a save's name, description, content and sample.
func checkSave(req *templatesconsolev1.SaveTemplateRequest) (templates.Content, map[string]any, error) {
	if err := checkName(req.GetName()); err != nil {
		return templates.Content{}, nil, err
	}

	if len(req.GetDescription()) > maxDescription {
		return templates.Content{}, nil, status.Errorf(codes.InvalidArgument, "%s", "description is too long")
	}

	sample, err := sampleOf(req.GetSample())
	if err != nil {
		return templates.Content{}, nil, err
	}

	content := templates.Content{Default: req.GetDefaultLanguage(), Translations: map[string]map[string]string{}}

	for _, p := range req.GetParts() {
		content.Parts = append(content.Parts, engine.Part{Name: p.GetName(), Kind: p.GetKind()})
	}

	for _, tr := range req.GetTranslations() {
		if _, dup := content.Translations[tr.GetLanguage()]; dup {
			return templates.Content{}, nil, status.Errorf(codes.InvalidArgument, "language %q twice", tr.GetLanguage())
		}

		content.Translations[tr.GetLanguage()] = tr.GetSources()
	}

	if err := content.Check(); err != nil {
		return templates.Content{}, nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	return content, sample, nil
}

// ActivateVersion implements AdminService.
func (a *Admin) ActivateVersion(
	ctx context.Context, req *templatesconsolev1.ActivateVersionRequest,
) (*templatesconsolev1.ActivateVersionResponse, error) {
	if err := checkName(req.GetName()); err != nil {
		return nil, err
	}

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	var n int64

	err = st.InTx(ctx, func(ctx context.Context) error {
		if err := consoleOwns(ctx, st, req.GetName()); err != nil {
			return err
		}

		var err error

		n, err = st.Q.ActivateVersion(ctx, db.ActivateVersionParams{Name: req.GetName(), Version: req.GetVersion()})

		return err //nolint:wrapcheck // named below
	})
	if err != nil {
		return nil, a.changeFailed("activate", err)
	}

	if n == 0 {
		return nil, status.Errorf(codes.NotFound, "template %q has no version %d", req.GetName(), req.GetVersion())
	}

	a.audit(ctx, "template.activated", req.GetName(), xlog.Int64("version", req.GetVersion()))

	return &templatesconsolev1.ActivateVersionResponse{}, nil
}

// DeleteTemplate implements AdminService.
func (a *Admin) DeleteTemplate(
	ctx context.Context, req *templatesconsolev1.DeleteTemplateRequest,
) (*templatesconsolev1.DeleteTemplateResponse, error) {
	if err := checkName(req.GetName()); err != nil {
		return nil, err
	}

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	var n int64

	err = st.InTx(ctx, func(ctx context.Context) error {
		if err := consoleOwns(ctx, st, req.GetName()); err != nil {
			return err
		}

		var err error

		n, err = st.Q.DeleteTemplate(ctx, req.GetName())

		return err //nolint:wrapcheck // named below
	})
	if err != nil {
		return nil, a.changeFailed("delete", err)
	}

	if n > 0 {
		a.audit(ctx, "template.deleted", req.GetName())
	}

	return &templatesconsolev1.DeleteTemplateResponse{Deleted: n > 0}, nil
}

// Preview implements AdminService.
func (a *Admin) Preview(
	ctx context.Context, req *templatesconsolev1.PreviewRequest,
) (*templatesconsolev1.PreviewResponse, error) {
	data, err := sampleOf(req.GetData())
	if err != nil {
		return nil, err
	}

	lang, err := engine.ParseLanguage(req.GetTranslation().GetLanguage())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	sources := req.GetTranslation().GetSources()
	parts := make([]engine.Part, 0, len(req.GetParts()))

	for _, p := range req.GetParts() {
		parts = append(parts, engine.Part{Name: p.GetName(), Kind: p.GetKind(), Source: sources[p.GetName()]})
	}

	if err := engine.Check(parts); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	partials, _, err := templates.ActivePartials(ctx, st, lang)
	if err != nil {
		return nil, a.failed("partials", err)
	}

	out := &templatesconsolev1.PreviewResponse{Parts: map[string]string{}}

	// Every part on its own, so one broken part does not hide the rest.
	for _, p := range parts {
		compiled, err := engine.Compile([]engine.Part{p}, partials, lang)
		if err != nil {
			out.Problems = append(out.Problems, problemsOf(lang, err)...)

			continue
		}

		rendered, err := compiled.Render(data)
		if err != nil {
			out.Problems = append(out.Problems, problemsOf(lang, err)...)

			continue
		}

		out.Parts[p.Name] = rendered[p.Name]
	}

	return out, nil
}

func problemsOf(lang string, err error) []*templatesconsolev1.Problem {
	problems := engine.Problems(err)
	out := make([]*templatesconsolev1.Problem, 0, len(problems))

	for _, p := range problems {
		out = append(out, &templatesconsolev1.Problem{Language: lang, Part: p.Part, Message: p.Message})
	}

	if len(problems) == 0 && err != nil {
		out = append(out, &templatesconsolev1.Problem{Language: lang, Message: err.Error()})
	}

	return out
}

// sampleOf parses a JSON object, "" being empty.
func sampleOf(raw string) (map[string]any, error) {
	data, err := engine.DecodeData([]byte(raw))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	return data, nil
}

// ready is the store, or Unavailable.
func (a *Admin) ready() (*store.Store, error) {
	if !a.store.Ready() {
		err := a.store.Err()
		if err == nil {
			err = errors.New("store not provided") //nolint:err113 // one message
		}

		return nil, status.Errorf(codes.Unavailable, "store: %v", err)
	}

	return a.store.Get(), nil
}

// changeFailed is FailedPrecondition for a template the files source
// keeps, else failed.
func (a *Admin) changeFailed(what string, err error) error {
	if errors.Is(err, errManaged) {
		return status.Errorf(codes.FailedPrecondition, "%s", err.Error())
	}

	return a.failed(what, err)
}

// failed is an Internal status; the cause goes to the log, not the console.
func (a *Admin) failed(what string, err error) error {
	a.Log().Warn(what+" failed", xlog.Err(err))

	return status.Errorf(codes.Internal, "%s failed; see the service log", what)
}

// audit records an operator action: actor console (the relay carries the
// operator's session, not an identity the service can name), one record
// per change (AuditID), the template name already validated.
func (a *Admin) audit(ctx context.Context, name, template string, fields ...xlog.Field) {
	a.auditAs(ctx, "console", name, template, fields...)
}

// auditAs is audit with another actor (the files source).
func (a *Admin) auditAs(ctx context.Context, actor, name, template string, fields ...xlog.Field) {
	fields = append([]xlog.Field{
		backplane.Audit(), xlog.String("event.name", name), xlog.String(backplane.AuditID, uuid.NewString()),
		xlog.String(backplane.AuditSubject, "template/"+template), xlog.String(backplane.AuditActor, actor),
		xlog.String(backplane.AuditOutcome, "succeeded"), xlog.String("template", template),
	}, fields...)

	a.Log().Ctx().Info(ctx, name, fields...)
}

var _ templatesconsolev1.AdminServiceServer = (*Admin)(nil)
