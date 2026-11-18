// Package oracles turns an observation into findings. No browser access, no
// guessing: every predicate is decidable from the observation alone.
package oracles

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nyimbi/afford/internal/model"
)

var errorBoundary = regexp.MustCompile(`(?i)application error|client-side exception|something went wrong|` +
	`unhandled runtime error|hydration failed|this page could not be found|internal server error|` +
	`unexpected error|something broke|white screen`)

var (
	uuidRe = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// ULIDs start with a 48-bit timestamp, so the first character is 0-7 until
	// well past this century. Requiring that cuts most 26-character false hits.
	ulidRe = regexp.MustCompile(`(?i)\b[0-7][0-9a-hjkmnp-tv-z]{25}\b`)
)

func internalID(text string) string {
	switch {
	case uuidRe.MatchString(text):
		return "uuid"
	case ulidRe.MatchString(text):
		return "ulid"
	}
	return ""
}

// OptionsAudit reports identifiers shown in dropdowns and dropdowns with
// nothing to choose. It reads only the inventory the scan already captured.
func OptionsAudit(controls []model.Element, pageURL string) []model.Finding {
	var out []model.Finding
	for _, el := range controls {
		if el.Tag != "select" || !el.Visible || el.Disabled {
			continue
		}
		selectable := 0
		var labelHits, valueHits []string
		kinds := map[string]bool{}
		for _, opt := range el.Options {
			value := strings.TrimSpace(opt.Value)
			label := strings.TrimSpace(opt.Label)
			if value != "" && !opt.Disabled {
				selectable++
			}
			if kind := internalID(label); kind != "" {
				kinds[kind] = true
				if len(labelHits) < 5 {
					labelHits = append(labelHits, truncate(label, 80))
				}
				continue
			}
			if kind := internalID(value); kind != "" {
				kinds[kind] = true
				if len(valueHits) < 5 {
					valueHits = append(valueHits, truncate(value, 80))
				}
			}
		}
		name := el.Name
		if name == "" {
			name = el.NameAttr
		}
		if len(labelHits) > 0 {
			f := model.NewFinding("E_UUID_EXPOSED", pageURL)
			f.Element = fmt.Sprintf("select “%s”", name)
			f.Detail["exposed_in"] = "label"
			f.Detail["kind"] = firstKind(kinds)
			f.Detail["options"] = labelHits
			f.Detail["options_total"] = len(el.Options)
			out = append(out, f)
		}
		if len(valueHits) > 0 {
			f := model.NewFinding("E_UUID_IN_OPTION_VALUE", pageURL)
			f.Element = fmt.Sprintf("select “%s”", name)
			f.Detail["exposed_in"] = "value"
			f.Detail["kind"] = firstKind(kinds)
			f.Detail["options"] = valueHits
			f.Detail["options_total"] = len(el.Options)
			out = append(out, f)
		}
		if selectable == 0 {
			f := model.NewFinding("E_EMPTY_SELECT", pageURL)
			f.Element = fmt.Sprintf("select “%s”", name)
			f.Detail["options_total"] = len(el.Options)
			if len(el.Options) > 0 {
				f.Detail["first_option"] = truncate(strings.TrimSpace(el.Options[0].Label), 80)
			}
			out = append(out, f)
		}
	}
	return out
}

func firstKind(kinds map[string]bool) string {
	if kinds["uuid"] {
		return "uuid"
	}
	return "ulid"
}

// notFound matches the language a page uses when it could not find its subject.
// A 200 response with this text is the SPA soft-404: the router rendered an
// error view and the server never saw a missing path. The phrases stay narrow
// on purpose: bare "404" and "missing" appear in ordinary site chrome.
var notFound = regexp.MustCompile(
	`(?i)\b(page not found|404 page|404 error|404 not found|not found page|` +
		`could(n't| not) be found|does(n't| not) exist|no longer (exists|available)|` +
		`could(n't| not) find|unknown route|nothing here)\b`)

func observationText(obs model.Observation) string {
	sample, _ := obs.Extra["text_sample"].(string)
	return sample
}

func softNotFound(obs model.Observation) bool {
	sample := observationText(obs)
	if sample == "" {
		return false
	}
	if len(sample) > 600 {
		sample = sample[:600]
	}
	return notFound.MatchString(sample)
}

func evidence(obs model.Observation) []string {
	var lines []string
	for _, e := range obs.PageErrors {
		lines = append(lines, "pageerror: "+e)
	}
	for _, e := range obs.ConsoleErrors {
		lines = append(lines, "console: "+e)
	}
	for _, r := range obs.ErrorRequests() {
		if r.Failed != "" {
			lines = append(lines, fmt.Sprintf("request failed: %s %s — %s", r.Method, r.URL, r.Failed))
		} else {
			lines = append(lines, fmt.Sprintf("HTTP %d: %s %s", r.Status, r.Method, r.URL))
		}
	}
	for _, d := range obs.Dialogs {
		lines = append(lines, "dialog: "+d)
	}
	return lines
}

func withFloor(obs model.Observation, floor int) []model.RequestRecord {
	out := make([]model.RequestRecord, 0, 4)
	for _, r := range obs.Requests {
		if r.Status >= floor {
			out = append(out, r)
		}
	}
	return out
}

func urls(records []model.RequestRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.URL)
	}
	return out
}

// Evaluate applies the checks for one observation context.
func Evaluate(obs model.Observation, pageURL, kind string, element *model.Element) []model.Finding {
	var out []model.Finding
	add := func(code string) model.Finding {
		f := model.NewFinding(code, pageURL)
		f.ProbeKind = kind
		f.Evidence = evidence(obs)
		if element != nil {
			f.Element = fmt.Sprintf("%s “%s”", element.Tag, element.Name)
		}
		return f
	}

	if kind == "capture" {
		switch {
		case obs.Status >= 500:
			f := add("E_HTTP_5XX")
			f.Detail["status"] = obs.Status
			f.Detail["url"] = obs.URLAfter
			out = append(out, f)
		case obs.Status >= 400:
			f := add("E_HTTP_4XX")
			f.Detail["status"] = obs.Status
			f.Detail["url"] = obs.URLAfter
			out = append(out, f)
		case obs.BodyLen < 20 && obs.Extra != nil && toInt(obs.Extra["interactive_count"]) < 3:
			f := add("E_BLANK_PAGE")
			f.Detail["body_len"] = obs.BodyLen
			out = append(out, f)
		}
		if sample, _ := obs.Extra["text_sample"].(string); errorBoundary.MatchString(sample) {
			f := add("E_ERROR_BOUNDARY")
			f.Detail["text"] = truncate(sample, 220)
			out = append(out, f)
		}
		if overflow, _ := obs.Extra["overflow_x"].(bool); overflow {
			f := add("E_OVERFLOW_X")
			f.Detail["scroll_width"] = obs.Extra["scroll_width"]
			f.Detail["viewport_width"] = obs.Extra["viewport_width"]
			out = append(out, f)
		}
		if strings.TrimSpace(obs.Title) == "" && obs.Status < 400 {
			out = append(out, add("E_NO_TITLE"))
		}
		for _, err := range obs.PageErrors {
			f := add("E_UNCAUGHT")
			f.Detail["error"] = err
			out = append(out, f)
		}
		for _, err := range obs.ConsoleErrors {
			// The browser logs its own resource failures; the request oracles
			// already carry those with a status, so this would be a duplicate.
			if strings.HasPrefix(err, "Failed to load resource") {
				continue
			}
			f := add("E_CONSOLE_ERROR")
			f.Detail["error"] = err
			out = append(out, f)
		}
	}

	if kind == "action" || kind == "form" || kind == "validate" {
		for _, err := range obs.PageErrors {
			code := "E_CLIENT_ERROR_ON_ACTION"
			if kind == "validate" {
				code = "E_UNCAUGHT"
			}
			f := add(code)
			f.Detail["error"] = err
			out = append(out, f)
		}

		five := withFloor(obs, 500)
		if len(five) > 0 && kind != "form" {
			code := "E_500_ON_ACTION"
			if kind == "validate" {
				code = "E_HTTP_5XX"
			}
			f := add(code)
			f.Detail["requests"] = urls(five)
			out = append(out, f)
		}

		if kind == "validate" {
			if four := withFloor(obs, 400); len(four) > 0 {
				f := add("E_HTTP_4XX")
				f.Detail["requests"] = urls(four)
				out = append(out, f)
			}
		}

		var failed []model.RequestRecord
		for _, r := range obs.Requests {
			if r.Failed != "" {
				failed = append(failed, r)
			}
		}
		if len(failed) > 0 {
			code := "E_FAILED_REQUEST_ON_ACTION"
			if kind == "validate" {
				code = "E_REQUEST_FAILED"
			}
			f := add(code)
			f.Detail["requests"] = urls(failed)
			out = append(out, f)
		}

		if len(obs.Dialogs) > 0 {
			f := add("E_ALERT")
			f.Detail["dialogs"] = obs.Dialogs
			out = append(out, f)
		}
	}

	if kind == "action" && element != nil {
		switch {
		case obs.Extra["upload_error"] != nil:
			f := add("E_UPLOAD_FAILED")
			f.Detail["error"] = obs.Extra["upload_error"]
			out = append(out, f)
		case obs.ActionError != "":
			f := add("E_CONTROL_ERROR")
			f.Detail["error"] = obs.ActionError
			out = append(out, f)
		case obs.Occluded:
			f := add("E_OCCLUDED")
			f.Detail["rect"] = element.Rect
			out = append(out, f)
		case obs.Effect == "none" && element.Src == "button":
			f := add("E_DEAD_CONTROL")
			f.Detail["effect"] = obs.Effect
			out = append(out, f)
		}
		if obs.Navigated && obs.Status >= 400 {
			code := "E_NAV_TO_NONOK"
			if element.Tag == "a" {
				code = "E_DEAD_LINK"
			}
			f := add(code)
			f.Detail["status"] = obs.Status
			f.Detail["url"] = obs.URLAfter
			out = append(out, f)
		} else if obs.Navigated && element.Tag == "a" && softNotFound(obs) {
			f := add("E_DEAD_LINK")
			f.Detail["status"] = obs.Status
			f.Detail["url"] = obs.URLAfter
			f.Detail["soft_404"] = true
			f.Detail["text"] = truncate(observationText(obs), 180)
			out = append(out, f)
		}
	}
	return out
}

// FallbackEvidence orders the observation so the actionable line comes first:
// an exception or an HTTP status, not the effect classification. A digest that
// leads with "effect=navigated" tells a reader nothing they can act on.
func FallbackEvidence(obs model.Observation) []string {
	var lines []string
	for i, e := range obs.PageErrors {
		if i >= 3 {
			break
		}
		lines = append(lines, "pageerror: "+e)
	}
	for i, r := range obs.ErrorRequests() {
		if i >= 3 {
			break
		}
		if r.Failed != "" {
			lines = append(lines, fmt.Sprintf("request failed: %s %s — %s", r.Method, r.URL, r.Failed))
		} else {
			lines = append(lines, fmt.Sprintf("HTTP %d: %s %s", r.Status, r.Method, r.URL))
		}
	}
	for i, e := range obs.ConsoleErrors {
		if i >= 3 {
			break
		}
		lines = append(lines, "console: "+e)
	}
	lines = append(lines, "effect="+obs.Effect)
	if obs.Status > 0 {
		lines = append(lines, fmt.Sprintf("status=%d", obs.Status))
	}
	lines = append(lines, "url="+obs.URLAfter)
	return lines
}

func Unlabeled(controls []model.Element, pageURL string) []model.Finding {
	var out []model.Finding
	for _, el := range controls {
		if el.Src != "input" || !el.Visible {
			continue
		}
		if el.Tag != "input" && el.Tag != "select" && el.Tag != "textarea" {
			continue
		}
		switch el.Type {
		case "submit", "button", "reset", "image", "hidden", "checkbox", "radio":
			continue
		}
		if strings.TrimSpace(el.Name) == "" && el.LabelCount == 0 {
			f := model.NewFinding("E_UNLABELED_CONTROL", pageURL)
			f.Element = fmt.Sprintf("%s “%s”", el.Tag, el.NameAttr)
			f.Detail["name"] = el.NameAttr
			f.Detail["id"] = el.ID
			f.Detail["type"] = el.Type
			out = append(out, f)
		}
	}
	return out
}

func EmptyHref(controls []model.Element, pageURL string) []model.Finding {
	var out []model.Finding
	for _, el := range controls {
		if el.Tag != "a" || !el.Visible {
			continue
		}
		href := strings.TrimSpace(el.Href)
		if href == "" || href == "#" || strings.HasPrefix(strings.ToLower(href), "javascript:") {
			f := model.NewFinding("E_EMPTY_HREF", pageURL)
			f.Element = fmt.Sprintf("a “%s”", el.Name)
			f.Detail["href"] = href
			out = append(out, f)
		}
	}
	return out
}

func toInt(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
