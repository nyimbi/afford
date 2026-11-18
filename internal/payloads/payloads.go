// Package payloads classifies fields and produces values that are either valid
// or invalid by the field's own declaration, so failures are attributable.
package payloads

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nyimbi/afford/internal/model"
)

type Payload struct {
	Label string
	Value string
}

type semantic struct {
	Kind    string
	Pattern *regexp.Regexp
}

var semantics = []semantic{
	{"password", regexp.MustCompile(`(?i)pass(word|wd)?|pwd|secret`)},
	{"email", regexp.MustCompile(`(?i)e[-_ ]?mail`)},
	{"tel", regexp.MustCompile(`(?i)phone|mobile|tel|cell|contact[-_ ]?number`)},
	{"url", regexp.MustCompile(`(?i)url|website|homepage|link|uri`)},
	{"number", regexp.MustCompile(`(?i)amount|price|qty|quantity|count|age|number|num|total|sum|weight|height|width|year`)},
	{"date", regexp.MustCompile(`(?i)date|dob|birth|deadline|due|start|end|from|to[_ ]?date`)},
	{"search", regexp.MustCompile(`(?i)search|query|^q$|filter`)},
	{"username", regexp.MustCompile(`(?i)user(name)?|login|handle|account`)},
	{"text", regexp.MustCompile(`.`)},
}

// Deleting or erasing data is its own permission: --allow-delete.
var deletionWords = []string{
	"delete", "remove", "destroy", "drop", "truncate", "purge", "wipe", "erase",
	"reset", "clear all", "deactivate", "revoke", "terminate", "close account",
	"cancel order", "cancel subscription", "cancel account",
}

// Irreversible effects that are not deletion: --allow-side-effects.
var sideEffectWords = []string{
	"pay", "payment", "purchase", "buy", "checkout", "place order", "send",
	"publish", "deploy", "release", "logout", "log out", "sign out", "signout",
	"unsubscribe", "archive", "refund", "transfer", "withdraw", "approve", "reject",
}

var numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// InferKind prefers the declared type, then the field's own vocabulary.
func InferKind(el model.Element) string {
	declared := strings.ToLower(el.Type)
	switch el.Tag {
	case "select":
		return "select"
	case "textarea":
		return "textarea"
	}
	switch declared {
	case "checkbox", "radio", "range", "date", "time", "datetime-local", "month",
		"week", "color", "file", "password", "email", "url", "tel", "number",
		"search", "submit", "button", "reset", "image":
		return declared
	}
	blob := strings.ToLower(strings.Join([]string{
		el.NameAttr, el.Name, el.Placeholder, el.Attrs["autocomplete"],
	}, " "))
	for _, s := range semantics {
		if s.Pattern.MatchString(blob) {
			return s.Kind
		}
	}
	return "text"
}

// ValidValue returns a value the field advertises as acceptable. Empty means
// "this control is toggled or chosen, not filled".
func ValidValue(el model.Element) string {
	kind := InferKind(el)
	switch kind {
	case "checkbox", "radio", "submit", "button", "reset", "image", "file":
		return ""
	case "select":
		for _, opt := range el.Options {
			if opt.Value != "" && !opt.Disabled {
				return opt.Value
			}
		}
		return ""
	case "range", "number":
		return numeric(el, kind == "range")
	case "email":
		return "qa.afford@example.com"
	case "url":
		return "https://example.com/path"
	case "tel":
		return "+15550100"
	case "password":
		return "Afford-Test-42!"
	case "date":
		return "2026-01-15"
	case "time":
		return "12:30"
	case "datetime-local":
		return "2026-01-15T12:30"
	case "month":
		return "2026-01"
	case "week":
		return "2026-W03"
	case "color":
		return "#3366cc"
	case "search":
		return "afford"
	}
	return "Afford test 42"
}

func numeric(el model.Element, mid bool) string {
	lo, hasLo := num(el.Attrs["min"])
	hi, hasHi := num(el.Attrs["max"])
	if mid && hasLo && hasHi {
		return format((lo + hi) / 2)
	}
	if hasLo {
		if lo > 0 {
			return format(lo)
		}
		return "1"
	}
	if hasHi {
		if hi > 0 {
			return format(hi)
		}
		return "0"
	}
	return "1"
}

func num(raw string) (float64, bool) {
	if !numberRe.MatchString(raw) {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	return v, err == nil
}

func format(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Sweep returns the labelled battery for one text-like field.
func Sweep(el model.Element, depth string) []Payload {
	kind := InferKind(el)
	switch kind {
	case "checkbox", "radio", "select", "file", "submit", "button", "reset", "image":
		return nil
	}
	out := []Payload{{"empty", ""}, {"whitespace", " "}}
	switch kind {
	case "email":
		out = append(out, Payload{"malformed", "not-an-email"})
	case "url":
		out = append(out, Payload{"malformed", "not a url"})
	case "tel":
		out = append(out, Payload{"malformed", "abc-def"})
	case "number", "range":
		out = append(out, Payload{"malformed", "abc"})
		if lo, ok := num(el.Attrs["min"]); ok {
			out = append(out, Payload{"below-min", format(lo - 1)})
		}
		if hi, ok := num(el.Attrs["max"]); ok {
			out = append(out, Payload{"above-max", format(hi + 1)})
		}
	case "date", "datetime-local":
		out = append(out, Payload{"impossible", "2024-02-30"})
	default:
		out = append(out, Payload{"long", strings.Repeat("A", 300)})
	}
	out = append(out, Payload{"numeric-edge", "0"})
	if depth == "standard" || depth == "deep" {
		out = append(out,
			Payload{"unicode", "测试 ñ é 日本語"},
			Payload{"quotes", `'"<>&`},
			Payload{"very-long", strings.Repeat("B", 1200)},
		)
	}
	if depth == "deep" {
		out = append(out,
			Payload{"sentinel-html", "<b>afford</b>"},
			Payload{"sentinel-sql", "' OR 1=1 -- "},
			Payload{"sentinel-path", "../../etc/passwd"},
			Payload{"whitespace-tabs", "\t\t"},
		)
	}
	return out
}

// The vocabulary is compiled on word boundaries. Substring matching quarantined
// a nav link called "dropdown" because it contains "drop".
var (
	deletionRes   = compilePhrases(deletionWords)
	sideEffectRes = compilePhrases(sideEffectWords)
)

func compilePhrases(words []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(words))
	for _, word := range words {
		if re := phraseRegexp(word); re != nil {
			out = append(out, re)
		}
	}
	return out
}

func phraseRegexp(word string) *regexp.Regexp {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(word)))
	if len(parts) == 0 {
		return nil
	}
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = regexp.QuoteMeta(part)
	}
	re, err := regexp.Compile(`\b` + strings.Join(quoted, `[\s\-_]*`) + `\b`)
	if err != nil {
		return nil
	}
	return re
}

// DestructiveKind classifies a control's permission class: "delete",
// "side_effect", or "" for an ordinary control. Extra patterns from config are
// treated as deletion, the stricter grant.
func DestructiveKind(el model.Element, extra []string) string {
	blob := strings.ToLower(strings.Join([]string{el.Name, el.NameAttr, el.Placeholder}, " "))
	for _, re := range deletionRes {
		if re.MatchString(blob) {
			return "delete"
		}
	}
	for _, re := range sideEffectRes {
		if re.MatchString(blob) {
			return "side_effect"
		}
	}
	for _, word := range extra {
		if re := phraseRegexp(word); re != nil && re.MatchString(blob) {
			return "delete"
		}
	}
	if strings.TrimSpace(el.Attrs["data-confirm"]) != "" {
		return "delete"
	}
	return ""
}

func IsDestructive(el model.Element, extra []string) bool {
	return DestructiveKind(el, extra) != ""
}

// OptInFlag names the flag an operator would pass to allow this class.
func OptInFlag(kind string) string {
	if kind == "delete" {
		return "--allow-delete"
	}
	return "--allow-side-effects"
}

func EmptyRequired(el model.Element) bool {
	return el.Required && el.Visible && !el.Disabled && !el.ReadOnly
}

// IsTextLike reports whether the payload sweep can meaningfully fill this
// control. Selects, toggles, ranges and file inputs are activated instead.
func IsTextLike(el model.Element) bool {
	if !el.IsTextInput() {
		return false
	}
	switch InferKind(el) {
	case "text", "textarea", "email", "url", "tel", "number", "search", "password", "username":
		return true
	}
	return false
}

func Describe(el model.Element) string {
	name := el.Name
	if name == "" {
		name = el.NameAttr
	}
	return fmt.Sprintf("%s %q", el.Tag, name)
}
