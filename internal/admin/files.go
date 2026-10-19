package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"go.yaml.in/yaml/v3"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane-templates/internal/engine"
	"github.com/backplane-labs/backplane-templates/internal/store"
	"github.com/backplane-labs/backplane-templates/internal/store/db"
	"github.com/backplane-labs/backplane-templates/internal/templates"
)

// FilesAuthor is the author of the versions the files source writes.
const FilesAuthor = "files"

// defaultFilesPoll is how often the directory is read again.
const defaultFilesPoll = 5 * time.Second

// ErrFiles: the files source is not a valid set of templates.
var ErrFiles = errors.New("templates: files source")

// Files keeps the templates of a directory in force: the directory is the
// desired state, and the console only reads (and previews) what it holds.
//
// Every subdirectory is one template, named by it:
//
//	kratos.recovery_code_valid/
//	  template.yaml        # optional: description, default_language, sample
//	  en/subject.txt       # <language>/<part>.<ext>: .txt a text part, .html an html one
//	  en/html.html
//	  ru/subject.txt
//	  ru/html.html
//	_footer/
//	  en/html.html         # a shared piece: its part named as the including part (or its only one)
//
// Names starting with a dot are skipped (a Kubernetes ConfigMap's ..data).
// A pass saves what differs from a template's active version as a new
// version and activates it; a template a directory no longer holds is
// deleted. A template that does not compile keeps the version in force; a
// directory that does not read whole changes nothing. Replicas run it side
// by side and converge.
type Files struct {
	deps.Component

	admin *Admin
	dir   string
	poll  time.Duration

	mu       sync.Mutex
	reported map[string]string // entry -> the problem last logged
	settled  string            // fingerprint of the last pass that applied everything
}

// NewFiles creates the component under parent: the directory dir, read
// every poll (0: 5s).
func NewFiles(parent deps.Scope, a *Admin, dir string, poll time.Duration) *Files {
	if poll <= 0 {
		poll = defaultFilesPoll
	}

	f := &Files{
		Component: deps.NewComponent(parent, "files"), admin: a, dir: dir, poll: poll,
		reported: map[string]string{},
	}
	f.Go(f.loop)

	return f
}

func (f *Files) loop(ctx context.Context) error {
	tick := time.NewTicker(f.poll)
	defer tick.Stop()

	for {
		f.pass(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// FileTemplate is a template as its directory says.
type FileTemplate struct {
	Description string
	Content     templates.Content
	Sample      map[string]any
}

// FileSet is what a directory holds: templates by name, and its digest.
type FileSet struct {
	Templates map[string]FileTemplate
	Digest    string
}

func (f *Files) pass(ctx context.Context) {
	set := f.admin.templates.Settings()

	want, err := ReadFiles(f.dir, set.DefaultLanguage.Get())
	if err != nil {
		f.report("directory", "warn", fmt.Sprintf("templates directory not applied: %v", err))

		return
	}

	f.report("directory", "", "")

	st, err := f.admin.ready()
	if err != nil {
		return
	}

	stamp, err := st.Q.TemplatesStamp(ctx)
	if err != nil {
		if ctx.Err() == nil {
			f.Log().Debug("templates stamp", xlog.Err(err))
		}

		return
	}

	fingerprint := fmt.Sprint(want.Digest, "/", stamp.Templates, "/", stamp.UpdatedAt.UnixNano())

	f.mu.Lock()
	settled := f.settled == fingerprint
	f.mu.Unlock()

	if settled {
		return
	}

	clean, err := f.apply(ctx, st, want)
	if err != nil {
		if ctx.Err() == nil {
			f.Log().Warn("templates files: pass failed", xlog.Err(err))
		}

		return
	}

	if clean {
		f.mu.Lock()
		f.settled = fingerprint
		f.mu.Unlock()
	}
}

// apply saves the differences; clean is false when a template waits or a
// write failed (the next pass tries again).
func (f *Files) apply(ctx context.Context, st *store.Store, want FileSet) (bool, error) {
	rows, err := st.Q.ListTemplates(ctx)
	if err != nil {
		return false, fmt.Errorf("list: %w", err)
	}

	current := make(map[string]*db.ListTemplatesRow, len(rows))
	for i := range rows {
		current[rows[i].Name] = &rows[i]
	}

	clean := true

	// Shared pieces first: the templates of the same pass compile with them.
	names := make([]string, 0, len(want.Templates))
	for name := range want.Templates {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		si, sj := strings.HasPrefix(names[i], "_"), strings.HasPrefix(names[j], "_")
		if si != sj {
			return si
		}

		return names[i] < names[j]
	})

	for _, name := range names {
		t := want.Templates[name]

		same, err := f.same(ctx, st, current[name], name, t)
		if err != nil {
			return false, err
		}

		if same {
			f.report(name, "", "")

			continue
		}

		clean = f.save(ctx, st, name, t) && clean
	}

	for i := range rows {
		r := &rows[i]
		if _, held := want.Templates[r.Name]; held || deref(r.ManagedBy) == "" {
			continue
		}

		if _, err := st.Q.DeleteTemplate(ctx, r.Name); err != nil {
			f.report(r.Name, "warn", fmt.Sprintf("template %s not removed: %v", r.Name, err))

			clean = false

			continue
		}

		f.report(r.Name, "", "")
		f.admin.auditAs(ctx, FilesAuthor, "template.deleted", r.Name)
		f.Log().Info("template removed: its directory is gone", xlog.String("template", r.Name))
	}

	return clean, nil
}

// same reports whether the template's active version already is t, kept
// by its directory.
func (f *Files) same(
	ctx context.Context, st *store.Store, cur *db.ListTemplatesRow, name string, t FileTemplate,
) (bool, error) {
	if cur == nil || cur.ActiveVersion == nil || deref(cur.ManagedBy) != name || cur.Description != t.Description {
		return false, nil
	}

	row, err := st.Q.GetVersion(ctx, db.GetVersionParams{Name: name, Version: *cur.ActiveVersion})
	if err != nil {
		return false, fmt.Errorf("version %s/%d: %w", name, *cur.ActiveVersion, err)
	}

	content, err := templates.DecodeContent(row.Parts, row.Translations, row.DefaultLanguage)
	if err != nil {
		return false, nil //nolint:nilerr // an undecodable version is replaced
	}

	var sample map[string]any
	if err := json.Unmarshal(row.Sample, &sample); err != nil {
		return false, nil //nolint:nilerr // as above
	}

	return reflect.DeepEqual(content, t.Content) && sameData(sample, t.Sample), nil
}

// sameData compares two JSON objects as JSON.
func sameData(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}

	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)

	return errA == nil && errB == nil && bytes.Equal(left, right)
}

// save compiles t and saves it as the template's next, active version kept
// by its directory; ok when saved.
func (f *Files) save(ctx context.Context, st *store.Store, name string, t FileTemplate) bool {
	problems, err := f.admin.compileAll(ctx, st, t.Content, t.Sample)
	if err != nil {
		f.report(name, "warn", fmt.Sprintf("template %s from its files does not compile: %v", name, err))

		return false
	}

	partsJSON, translationsJSON, err := t.Content.Encode()
	if err != nil {
		f.report(name, "warn", fmt.Sprintf("template %s: encode: %v", name, err))

		return false
	}

	sampleJSON, err := json.Marshal(orEmpty(t.Sample))
	if err != nil {
		f.report(name, "warn", fmt.Sprintf("template %s: encode sample: %v", name, err))

		return false
	}

	var version int64

	err = st.InTx(ctx, func(ctx context.Context) error {
		if err := st.Q.UpsertTemplate(ctx, db.UpsertTemplateParams{Name: name, Description: t.Description}); err != nil {
			return fmt.Errorf("upsert: %w", err)
		}

		if err := st.Q.SetManagedBy(ctx, db.SetManagedByParams{Name: name, ManagedBy: &name}); err != nil {
			return fmt.Errorf("managed by: %w", err)
		}

		next, err := st.Q.NextVersion(ctx, name)
		if err != nil {
			return fmt.Errorf("next version: %w", err)
		}

		version = next.Version

		if err := st.Q.InsertVersion(ctx, db.InsertVersionParams{
			Name: name, Version: version, Parts: partsJSON, Translations: translationsJSON,
			DefaultLanguage: t.Content.Default, Sample: sampleJSON, Author: FilesAuthor, Comment: "from " + name + "/",
		}); err != nil {
			return fmt.Errorf("insert version: %w", err)
		}

		if _, err := st.Q.ActivateVersion(ctx, db.ActivateVersionParams{Name: name, Version: version}); err != nil {
			return fmt.Errorf("activate: %w", err)
		}

		return nil
	})
	if err != nil {
		f.report(name, "warn", fmt.Sprintf("template %s not saved: %v", name, err))

		return false
	}

	f.report(name, "", "")
	f.admin.auditAs(ctx, FilesAuthor, "template.saved", name, xlog.Int64("version", version),
		xlog.Bool("activated", true), xlog.Int("problems", len(problems)),
		xlog.String("languages", strings.Join(t.Content.Languages(), ",")))

	if len(problems) > 0 {
		f.Log().Info("template from its files saved; its sample does not render", xlog.String("template", name),
			xlog.String("first", problems[0].GetLanguage()+"/"+problems[0].GetPart()+": "+problems[0].GetMessage()))
	}

	return true
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}

	return m
}

// report logs msg at level when it differs from what was logged for entry
// last; level "" clears the entry.
func (f *Files) report(entry, level, msg string) {
	f.mu.Lock()
	last := f.reported[entry]

	if level == "" {
		delete(f.reported, entry)
	} else {
		f.reported[entry] = msg
	}
	f.mu.Unlock()

	if level != "" && last != msg {
		f.Log().Warn(msg)
	}
}

// templateFile is a template directory's template.yaml.
type templateFile struct {
	Description     string         `yaml:"description"`
	DefaultLanguage string         `yaml:"default_language"` //nolint:tagliatelle // snake_case as the configuration
	Sample          map[string]any `yaml:"sample"`
}

// partKinds are the part files' extensions.
//
//nolint:gochecknoglobals // a constant table
var partKinds = map[string]string{".txt": engine.KindText, ".html": engine.KindHTML}

// ReadFiles reads the directory whole (any error fails it all); fallback
// is the default language of a template that names none and has several.
func ReadFiles(dir, fallback string) (FileSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return FileSet{}, fmt.Errorf("read %s: %w", dir, err)
	}

	set := FileSet{Templates: map[string]FileTemplate{}}
	sum := sha256.New()

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		if !isDir(filepath.Join(dir, name)) {
			return FileSet{}, fmt.Errorf("%w: %s: only template directories belong here", ErrFiles, name)
		}

		if !namePattern.MatchString(name) {
			return FileSet{}, fmt.Errorf("%w: %s: a template name is dotted lower-case words (a shared piece starts with _)",
				ErrFiles, name)
		}

		t, err := readTemplate(filepath.Join(dir, name), fallback, sum)
		if err != nil {
			return FileSet{}, fmt.Errorf("%s: %w", name, err)
		}

		set.Templates[name] = t
	}

	set.Digest = hex.EncodeToString(sum.Sum(nil))

	return set, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path) // follows a ConfigMap's links

	return err == nil && info.IsDir()
}

// readTemplate reads one template's directory, hashing what it reads.
func readTemplate(dir, fallback string, sum hash.Hash) (FileTemplate, error) {
	var meta templateFile

	content := templates.Content{Translations: map[string]map[string]string{}}
	kinds := map[string]string{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}

		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if d.IsDir() {
			if strings.Contains(rel, string(filepath.Separator)) {
				return fmt.Errorf("%w: %s: languages hold part files only", ErrFiles, rel)
			}

			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // the operator's own directory, links followed on purpose
		if err != nil {
			return err //nolint:wrapcheck // the path is in it
		}

		_, _ = fmt.Fprintf(sum, "%s/%s\x00%d\x00", filepath.Base(dir), rel, len(data))
		_, _ = sum.Write(data)

		return addFile(&content, kinds, &meta, rel, data)
	})
	if err != nil {
		return FileTemplate{}, err //nolint:wrapcheck // already says where
	}

	for part, kind := range kinds {
		content.Parts = append(content.Parts, engine.Part{Name: part, Kind: kind})
	}

	slices.SortFunc(content.Parts, func(a, b engine.Part) int { return strings.Compare(a.Name, b.Name) })

	content.Default = meta.DefaultLanguage
	if content.Default == "" {
		content.Default = defaultOf(content, fallback)
	}

	if err := content.Check(); err != nil {
		return FileTemplate{}, err //nolint:wrapcheck // the content names the problem
	}

	return FileTemplate{Description: meta.Description, Content: content, Sample: meta.Sample}, nil
}

// addFile adds one file of a template's directory.
func addFile(content *templates.Content, kinds map[string]string, meta *templateFile, rel string, data []byte) error {
	if rel == "template.yaml" {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)

		if err := dec.Decode(meta); err != nil {
			return fmt.Errorf("%w: template.yaml: %w", ErrFiles, err)
		}

		return nil
	}

	lang, file, ok := strings.Cut(rel, string(filepath.Separator))
	if !ok {
		return fmt.Errorf("%w: %s: a template holds template.yaml and <language>/<part>.<ext>", ErrFiles, rel)
	}

	ext := filepath.Ext(file)

	kind, known := partKinds[ext]
	if !known {
		return fmt.Errorf("%w: %s: a part file is .txt (text) or .html (html)", ErrFiles, rel)
	}

	part := strings.TrimSuffix(file, ext)
	if prev, seen := kinds[part]; seen && prev != kind {
		return fmt.Errorf("%w: part %s is text in one language and html in another", ErrFiles, part)
	}

	kinds[part] = kind

	if content.Translations[lang] == nil {
		content.Translations[lang] = map[string]string{}
	}

	content.Translations[lang][part] = string(data)

	return nil
}

// defaultOf is the default language of a template that names none: the
// only one, else the installation's when present.
func defaultOf(content templates.Content, fallback string) string {
	if len(content.Translations) == 1 {
		for lang := range content.Translations {
			return lang
		}
	}

	if _, ok := content.Translations[fallback]; ok {
		return fallback
	}

	return ""
}
