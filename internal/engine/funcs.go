package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// funcs is the function set of a language: formatting only, nothing that
// reaches the process or the network.
//
//	upper lower title trim replace join default json add mul round
//	plural N one [few many other]  CLDR plural form of N in the language
//	number N [digits]              N with the language's grouping and decimal mark
//	date LAYOUT WHEN               a time or RFC 3339 string in a Go layout
//	longdate T                     "15 October 2026" in the language
//	monthname T, weekday T         month and weekday names in the language
func funcs(tag language.Tag) map[string]any {
	printer := message.NewPrinter(tag)
	names := namesOf(tag)

	return map[string]any{
		"upper":     strings.ToUpper,
		"lower":     strings.ToLower,
		"title":     title,
		"trim":      strings.TrimSpace,
		"replace":   replace,
		"join":      join,
		"default":   defaultOf,
		"json":      toJSON,
		"add":       func(a, b float64) float64 { return a + b },
		"mul":       func(a, b float64) float64 { return a * b },
		"round":     round,
		"len":       lenOf,
		"index":     indexOf,
		"slice":     sliceOf,
		"date":      date,
		"plural":    func(n any, forms ...string) (string, error) { return pluralOf(tag, n, forms) },
		"number":    func(n any, digits ...any) (string, error) { return number(printer, n, digits) },
		"longdate":  func(v any) (string, error) { return longdate(names, v) },
		"monthname": func(v any) (string, error) { return field(v, names.month) },
		"weekday":   func(v any) (string, error) { return field(v, names.weekday) },
	}
}

var (
	errNoForms = errors.New("plural: no forms")
	errDigits  = errors.New("digits must be 0 to 12")
	errLarge   = errors.New("result exceeds 1 MiB")
)

const maxDigits = 12

func digitsOf(d any) (int, error) {
	n, err := intOf(d)
	if err != nil || n < 0 || n > maxDigits {
		return 0, errDigits
	}

	return n, nil
}

func round(v float64, d any) (string, error) {
	digits, err := digitsOf(d)
	if err != nil {
		return "", fmt.Errorf("round: %w", err)
	}

	return strconv.FormatFloat(v, 'f', digits, 64), nil
}

// replace is strings.ReplaceAll refusing a result past MaxOutputBytes
// before building it.
func replace(s, old, replacement string) (string, error) {
	n := strings.Count(s, old)
	if size := len(s) + n*(len(replacement)-len(old)); size > MaxOutputBytes {
		return "", fmt.Errorf("replace: %w", errLarge)
	}

	return strings.ReplaceAll(s, old, replacement), nil
}

func title(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}

	return strings.Join(words, " ")
}

// join joins items with sep, refusing a result past MaxOutputBytes.
func join(sep string, items any) (string, error) {
	var parts []string

	switch v := items.(type) {
	case []string:
		parts = v
	case []any:
		parts = make([]string, len(v))
		for i, item := range v {
			parts[i] = fmt.Sprint(item)
		}
	default:
		return fmt.Sprint(items), nil
	}

	size := len(sep) * max(len(parts)-1, 0)
	for _, p := range parts {
		size += len(p)
	}

	if size > MaxOutputBytes {
		return "", fmt.Errorf("join: %w", errLarge)
	}

	return strings.Join(parts, sep), nil
}

func defaultOf(fallback, value any) any {
	switch v := value.(type) {
	case nil:
		return fallback
	case string:
		if v == "" {
			return fallback
		}
	case float64:
		if v == 0 {
			return fallback
		}
	case bool:
		if !v {
			return fallback
		}
	}

	return value
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("json: %w", err)
	}

	return string(b), nil
}

func toTime(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v, nil
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return time.Time{}, fmt.Errorf("not an RFC 3339 time: %q", v) //nolint:err113 // formatted
		}

		return t, nil
	default:
		return time.Time{}, fmt.Errorf("%T is not a time", value) //nolint:err113 // formatted
	}
}

// date formats a time.Time or an RFC 3339 string with a Go layout.
func date(layout string, value any) (string, error) {
	t, err := toTime(value)
	if err != nil {
		return "", fmt.Errorf("date: %w", err)
	}

	return t.Format(layout), nil
}

func toFloat(n any) (float64, error) {
	switch v := n.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("not a number: %q", v) //nolint:err113 // formatted
		}

		return f, nil
	default:
		return 0, fmt.Errorf("%T is not a number", n) //nolint:err113 // formatted
	}
}

// pluralOf picks a form by the language's CLDR cardinal rules. Forms are
// positional: 2 = one, other; 3 = one, few, many; 4 = one, few, many,
// other; 6 = zero, one, two, few, many, other. A form the language uses
// but the call does not give falls back to the last one.
//
//	{{plural .n "item" "items"}}
//	{{plural .n "товар" "товара" "товаров"}}
func pluralOf(tag language.Tag, n any, forms []string) (string, error) {
	if len(forms) == 0 {
		return "", errNoForms
	}

	v, err := toFloat(n)
	if err != nil {
		return "", fmt.Errorf("plural: %w", err)
	}

	form := cardinal(tag, v)

	order := map[int][]plural.Form{
		2: {plural.One, plural.Other},
		3: {plural.One, plural.Few, plural.Many},
		4: {plural.One, plural.Few, plural.Many, plural.Other},
		6: {plural.Zero, plural.One, plural.Two, plural.Few, plural.Many, plural.Other},
	}[len(forms)]

	for i, f := range order {
		if f == form {
			return forms[i], nil
		}
	}

	return forms[len(forms)-1], nil
}

// cardinal is the CLDR plural form of v in the language, from its decimal
// digits as written (1.50 is "1.5": visible fraction digits count).
func cardinal(tag language.Tag, v float64) plural.Form {
	text := strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	intPart, frac, _ := strings.Cut(text, ".")

	digits := make([]byte, 0, len(intPart)+len(frac))
	for i := range len(intPart) + len(frac) {
		c := (intPart + frac)[i]
		digits = append(digits, c-'0')
	}

	return plural.Cardinal.MatchDigits(tag, digits, len(intPart), len(frac))
}

// number formats n with the language's grouping and decimal mark;
// digits defaults to the number's own (at most 6).
func number(printer *message.Printer, n any, digits []any) (string, error) {
	v, err := toFloat(n)
	if err != nil {
		return "", fmt.Errorf("number: %w", err)
	}

	if len(digits) > 0 {
		d, err := digitsOf(digits[0])
		if err != nil {
			return "", fmt.Errorf("number: %w", err)
		}

		return printer.Sprintf("%.*f", d, v), nil
	}

	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return printer.Sprintf("%d", int64(v)), nil
	}

	return printer.Sprintf("%v", v), nil
}

func field(v any, pick func(time.Time) string) (string, error) {
	t, err := toTime(v)
	if err != nil {
		return "", err
	}

	return pick(t), nil
}

func longdate(names calendar, v any) (string, error) {
	t, err := toTime(v)
	if err != nil {
		return "", fmt.Errorf("longdate: %w", err)
	}

	return names.long(t), nil
}
