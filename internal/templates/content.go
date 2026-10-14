package templates

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/text/language"

	"github.com/backplane-labs/backplane-templates/internal/engine"
)

// Errors of a content's shape.
var (
	ErrNoTranslation = errors.New("templates: at least one language is required")
	ErrDefault       = errors.New("templates: the default language must be one of the translations")
	ErrSources       = errors.New("templates: every language gives a source for every part, and only those")
	ErrTooManyLangs  = errors.New("templates: at most 32 languages")
)

const maxLanguages = 32

// Content is a version's text: the parts every language shares and a
// source per part per language.
type Content struct {
	Parts        []engine.Part                `json:"parts"`
	Translations map[string]map[string]string `json:"translations"`
	Default      string                       `json:"default_language"`
}

// Check validates the shape: parts, canonical language tags, every part
// in every language, the default among them.
func (c Content) Check() error {
	if err := engine.Check(c.Parts); err != nil {
		return err //nolint:wrapcheck // the engine names the problem
	}

	if len(c.Translations) == 0 {
		return ErrNoTranslation
	}

	if len(c.Translations) > maxLanguages {
		return ErrTooManyLangs
	}

	for lang, sources := range c.Translations {
		canonical, err := engine.ParseLanguage(lang)
		if err != nil || canonical != lang {
			return fmt.Errorf("%w: %q (canonical %q)", engine.ErrLanguage, lang, canonical)
		}

		if len(sources) != len(c.Parts) {
			return fmt.Errorf("%w: %s", ErrSources, lang)
		}

		for _, p := range c.Parts {
			src, ok := sources[p.Name]
			if !ok {
				return fmt.Errorf("%w: %s lacks %s", ErrSources, lang, p.Name)
			}

			if len(src) > engine.MaxPartBytes {
				return fmt.Errorf("%w: %s/%s", engine.ErrPartTooBig, lang, p.Name)
			}
		}
	}

	if _, ok := c.Translations[c.Default]; !ok {
		return ErrDefault
	}

	return nil
}

// Languages in a stable order: the default first, then the rest sorted.
func (c Content) Languages() []string {
	rest := make([]string, 0, len(c.Translations))
	for lang := range c.Translations {
		if lang != c.Default {
			rest = append(rest, lang)
		}
	}

	slices.Sort(rest)

	return append([]string{c.Default}, rest...)
}

// Incomplete are the languages with an empty part.
func (c Content) Incomplete() []string {
	var out []string

	for _, lang := range c.Languages() {
		for _, p := range c.Parts {
			if strings.TrimSpace(c.Translations[lang][p.Name]) == "" {
				out = append(out, lang)

				break
			}
		}
	}

	return out
}

// In is the parts with their sources in lang.
func (c Content) In(lang string) []engine.Part {
	out := make([]engine.Part, 0, len(c.Parts))
	for _, p := range c.Parts {
		out = append(out, engine.Part{Name: p.Name, Kind: p.Kind, Source: c.Translations[lang][p.Name]})
	}

	return out
}

// Pick is the language a render uses: the asked one, its base language,
// then each fallback in order; "" when none of them is translated.
func (c Content) Pick(asked string, fallbacks ...string) string {
	for _, lang := range candidates(asked, fallbacks...) {
		if _, ok := c.Translations[lang]; ok {
			return lang
		}
	}

	return ""
}

func candidates(asked string, fallbacks ...string) []string {
	var out []string

	if tag, err := language.Parse(asked); err == nil && asked != "" {
		out = append(out, tag.String())

		if base, conf := tag.Base(); conf != language.No {
			out = append(out, base.String())
		}
	}

	return append(out, fallbacks...)
}

// DecodeContent reads a version's columns.
func DecodeContent(parts, translations []byte, def string) (Content, error) {
	c := Content{Default: def}

	if err := json.Unmarshal(parts, &c.Parts); err != nil {
		return Content{}, fmt.Errorf("templates: parts: %w", err)
	}

	if err := json.Unmarshal(translations, &c.Translations); err != nil {
		return Content{}, fmt.Errorf("templates: translations: %w", err)
	}

	return c, nil
}

// Encode is the version's parts and translations columns; parts carry
// no source (they live in translations).
func (c Content) Encode() ([]byte, []byte, error) {
	parts := make([]engine.Part, len(c.Parts))
	for i, p := range c.Parts {
		parts[i] = engine.Part{Name: p.Name, Kind: p.Kind}
	}

	p, err := json.Marshal(parts)
	if err != nil {
		return nil, nil, fmt.Errorf("templates: parts: %w", err)
	}

	t, err := json.Marshal(c.Translations)
	if err != nil {
		return nil, nil, fmt.Errorf("templates: translations: %w", err)
	}

	return p, t, nil
}
