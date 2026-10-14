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
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
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

// Template names: dotted words, an optional @locale, a leading "_" for a
// shared piece.
var namePattern = regexp.MustCompile(`^_?[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*(?:@[a-z]{2}(?:-[A-Za-z]{2,4})?)?$`)

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

	for _, r := range rows {
		summary := &templatesconsolev1.TemplateSummary{
			Name: r.Name, Description: r.Description, Versions: r.Versions, UpdatedAt: timestamppb.New(r.UpdatedAt),
		}

		if r.ActiveVersion != nil {
			summary.ActiveVersion = *r.ActiveVersion
		}

		if len(r.ActiveParts) > 0 {
			if parts, err := templates.DecodeParts(r.ActiveParts); err == nil {
				for _, p := range parts {
					summary.Parts = append(summary.Parts, p.Name)
				}
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

	out := &templatesconsolev1.GetTemplateResponse{Name: t.Name, Description: t.Description}
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

	row, err := st.Q.GetVersion(ctx, db.GetVersionParams{Name: t.Name, Version: want})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "template %q has no version %d", req.GetName(), want)
	}

	if err != nil {
		return nil, a.failed("get version", err)
	}

	parts, err := templates.DecodeParts(row.Parts)
	if err != nil {
		return nil, a.failed("decode parts", err)
	}

	out.Content = &templatesconsolev1.Version{
		Version: row.Version, Parts: toProto(parts), Sample: string(row.Sample), Author: row.Author, Comment: row.Comment,
		CreatedAt: timestamppb.New(row.CreatedAt),
	}

	return out, nil
}

// SaveTemplate implements AdminService.
func (a *Admin) SaveTemplate(
	ctx context.Context, req *templatesconsolev1.SaveTemplateRequest,
) (*templatesconsolev1.SaveTemplateResponse, error) {
	parts, sample, err := checkSave(req)
	if err != nil {
		return nil, err
	}

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	partials, err := templates.Partials(ctx, st)
	if err != nil {
		return nil, a.failed("partials", err)
	}

	compiled, err := engine.Compile(parts, partials)
	if err != nil {
		return nil, invalid(err)
	}

	problems := render(compiled, sample)

	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return nil, a.failed("encode parts", err)
	}

	sampleJSON, err := json.Marshal(sample)
	if err != nil {
		return nil, a.failed("encode sample", err)
	}

	out := &templatesconsolev1.SaveTemplateResponse{Problems: problems}

	err = st.InTx(ctx, func(ctx context.Context) error {
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
			Name: req.GetName(), Version: next.Version, Parts: partsJSON, Sample: sampleJSON,
			Author: "console", Comment: req.GetComment(),
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
		return nil, a.failed("save", err)
	}

	a.audit(ctx, "template.saved", req.GetName(), xlog.Int64("version", out.GetVersion()),
		xlog.Bool("activated", out.GetActivated()), xlog.Int("problems", len(problems)))

	return out, nil
}

// checkSave validates a save's name, description and sample.
func checkSave(req *templatesconsolev1.SaveTemplateRequest) ([]engine.Part, map[string]any, error) {
	if !namePattern.MatchString(req.GetName()) {
		return nil, nil, status.Errorf(codes.InvalidArgument, "%s",
			"name must be dotted lower-case words, optionally @locale; a shared piece starts with _")
	}

	if len(req.GetDescription()) > maxDescription {
		return nil, nil, status.Errorf(codes.InvalidArgument, "%s", "description is too long")
	}

	sample, err := sampleOf(req.GetSample())
	if err != nil {
		return nil, nil, err
	}

	return fromProto(req.GetParts()), sample, nil
}

// ActivateVersion implements AdminService.
func (a *Admin) ActivateVersion(
	ctx context.Context, req *templatesconsolev1.ActivateVersionRequest,
) (*templatesconsolev1.ActivateVersionResponse, error) {
	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	n, err := st.Q.ActivateVersion(ctx, db.ActivateVersionParams{Name: req.GetName(), Version: req.GetVersion()})
	if err != nil {
		return nil, a.failed("activate", err)
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
	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	n, err := st.Q.DeleteTemplate(ctx, req.GetName())
	if err != nil {
		return nil, a.failed("delete", err)
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

	st, err := a.ready()
	if err != nil {
		return nil, err
	}

	partials, err := templates.Partials(ctx, st)
	if err != nil {
		return nil, a.failed("partials", err)
	}

	parts := fromProto(req.GetParts())
	if err := engine.Check(parts); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	out := &templatesconsolev1.PreviewResponse{Parts: map[string]string{}}

	// Every part on its own, so one broken part does not hide the rest.
	for _, p := range parts {
		compiled, err := engine.Compile([]engine.Part{p}, partials)
		if err != nil {
			out.Problems = append(out.Problems, problemsOf(err)...)

			continue
		}

		rendered, err := compiled.Render(data)
		if err != nil {
			out.Problems = append(out.Problems, problemsOf(err)...)

			continue
		}

		out.Parts[p.Name] = rendered[p.Name]
	}

	return out, nil
}

// render reports the sample not rendering, by part.
func render(c *engine.Compiled, sample map[string]any) []*templatesconsolev1.Problem {
	if _, err := c.Render(sample); err != nil {
		return problemsOf(err)
	}

	return nil
}

func problemsOf(err error) []*templatesconsolev1.Problem {
	problems := engine.Problems(err)
	out := make([]*templatesconsolev1.Problem, 0, len(problems))

	for _, p := range problems {
		out = append(out, &templatesconsolev1.Problem{Part: p.Part, Message: p.Message})
	}

	return out
}

// invalid is a parse failure as the caller's mistake, with every part's
// problem in the message.
func invalid(err error) error {
	problems := engine.Problems(err)
	if len(problems) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	lines := make([]string, 0, len(problems)+1)
	lines = append(lines, "parts do not parse")

	for _, p := range problems {
		lines = append(lines, p.Error())
	}

	return status.Errorf(codes.InvalidArgument, "%s", strings.Join(lines, "; "))
}

// sampleOf parses a JSON object, "" being empty.
func sampleOf(raw string) (map[string]any, error) {
	data, err := engine.DecodeData([]byte(raw))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	return data, nil
}

func toProto(parts []engine.Part) []*templatesconsolev1.Part {
	out := make([]*templatesconsolev1.Part, 0, len(parts))
	for _, p := range parts {
		out = append(out, &templatesconsolev1.Part{Name: p.Name, Kind: p.Kind, Source: p.Source})
	}

	return out
}

func fromProto(parts []*templatesconsolev1.Part) []engine.Part {
	out := make([]engine.Part, 0, len(parts))
	for _, p := range parts {
		out = append(out, engine.Part{Name: p.GetName(), Kind: p.GetKind(), Source: p.GetSource()})
	}

	return out
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

// failed is an Internal status; the cause goes to the log, not the console.
func (a *Admin) failed(what string, err error) error {
	a.Log().Warn(what+" failed", xlog.Err(err))

	return status.Errorf(codes.Internal, "%s failed; see the service log", what)
}

// audit records an operator action: actor console.
func (a *Admin) audit(ctx context.Context, name, template string, fields ...xlog.Field) {
	fields = append([]xlog.Field{
		backplane.Audit(), xlog.String("event.name", name), xlog.String(backplane.AuditSubject, "template/"+template),
		xlog.String(backplane.AuditActor, "console"), xlog.String(backplane.AuditOutcome, "succeeded"),
		xlog.String("template", template), xlog.Time("at", time.Now()),
	}, fields...)

	a.Log().Ctx().Info(ctx, name, fields...)
}

var _ templatesconsolev1.AdminServiceServer = (*Admin)(nil)
