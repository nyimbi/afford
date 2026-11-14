// Package model holds the wire types and the finding catalogue.
//
// Every finding code the tool can emit is declared here exactly once. Oracles
// return codes; the catalogue supplies severity and the explanation that lands
// in the report.
package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Severity string

const (
	Blocker  Severity = "blocker"
	Critical Severity = "critical"
	Major    Severity = "major"
	Minor    Severity = "minor"
	Info     Severity = "info"
)

// SeverityOrder ranks findings for sorting; lower is worse.
var SeverityOrder = map[Severity]int{
	Blocker: 0, Critical: 1, Major: 2, Minor: 3, Info: 4,
}

type FindingSpec struct {
	Code     string
	Severity Severity
	Title    string
	Why      string
}

var specs = []FindingSpec{
	{"E_AUTH_WALL", Blocker, "Crawl never got past authentication",
		"Every navigation landed on a login page. Nothing observed about the app can be trusted, and a clean report here would be a false negative."},
	{"E_HARNESS_TIMEOUT", Info, "Probe exceeded its time budget",
		"The tool gave up waiting. This is a tool limitation, not necessarily a product defect; the evidence shows what it was waiting for."},
	{"E_NOT_DEEPLINKABLE", Major, "Page cannot be reproduced by direct navigation",
		"The page was reached by clicking but a direct load shows something else. Shared links and refreshes to this route will not show the intended page."},
	{"E_UNCAUGHT", Major, "Uncaught JavaScript exception",
		"The page threw and nothing caught it. The UI after this point may be frozen or half-rendered."},
	{"E_CONSOLE_ERROR", Minor, "console.error emitted",
		"A deliberate error report from the app. Often a swallowed fetch failure; always worth a line in the report."},
	{"E_HTTP_5XX", Critical, "Server error (5xx)",
		"A request returned 5xx. For a document this is a broken page; for a subresource it is a broken region."},
	{"E_HTTP_4XX", Major, "Client error (4xx)",
		"A document or API call returned 4xx where the app expected success."},
	{"E_REQUEST_FAILED", Major, "Request failed below HTTP",
		"DNS, TLS, connection reset, or a blocked request. The app had no chance to handle it."},
	{"E_BLANK_PAGE", Blocker, "Page rendered nothing",
		"A 2xx document with no meaningful text and fewer than three controls. Usually a shell that failed to hydrate."},
	{"E_ERROR_BOUNDARY", Critical, "Framework error screen is visible",
		"The app caught the render error and replaced its own UI with an error page. The route is down."},
	{"E_OVERFLOW_X", Minor, "Horizontal overflow at the configured viewport",
		"Content is wider than the window, so the page scrolls sideways. On mobile this is a usability defect."},
	{"E_NO_TITLE", Info, "Missing document title",
		"Tabs, history entries and assistive technology all lose their label."},
	{"E_AUTH_REDIRECT", Info, "Navigation redirected to login",
		"The session may have expired, or the route is role-gated. Either way the page was not tested as intended."},
	{"E_DEAD_CONTROL", Major, "Enabled control did nothing",
		"No navigation, no dialog, no DOM change, no request. The handler is missing or bound to something inert."},
	{"E_OCCLUDED", Major, "Control cannot be clicked",
		"Another element sits on top of it, or it has no hit area. A real user cannot reach it."},
	{"E_CONTROL_ERROR", Major, "Control failed to activate",
		"The click or fill raised in the harness: the element vanished, stayed disabled, or was detached mid-action."},
	{"E_UNLABELED_CONTROL", Minor, "Form control has no accessible name",
		"No label, aria-label or aria-labelledby. Screen readers announce it as an unnamed edit field."},
	{"E_UUID_EXPOSED", Minor, "Internal identifier shown in a dropdown",
		"A select option's visible label is a UUID or ULID. The user is shown a database key where a human-readable name belongs, and the key leaks into screenshots, exports and support tickets."},
	{"E_UUID_IN_OPTION_VALUE", Info, "Dropdown option values are internal identifiers",
		"Option labels are readable but their values are UUIDs or ULIDs. Often intentional in a form, but it puts internal keys in the DOM, logs and any client-side state."},
	{"E_EMPTY_SELECT", Minor, "Dropdown has nothing to choose",
		"A visible, enabled select has no selectable option: every option is a placeholder, disabled, or absent. The user is shown a control that cannot be used. A select that is meant to stay empty until another field changes should be disabled until then."},
	{"E_SCREENSHOT_FAILED", Info, "Screenshot could not be captured",
		"The page was visited but no image was written, so the visual record for this route is missing. Usually a browser crash or a page that never stabilised."},
	{"E_DEAD_LINK", Major, "Dead link",
		"Clicking this anchor navigated to an error status, or to a page that says it could not find what the link promised. The target is missing, moved, or the route was removed without the link."},
	{"E_EMPTY_HREF", Minor, "Link points nowhere",
		"href is '#', empty, or javascript:void(0). It looks navigable and is not."},
	{"E_ALERT", Minor, "Native alert/confirm/prompt used",
		"Blocking browser dialogs break automation and are hard to style or dismiss deliberately."},
	{"E_UNSUPPORTED_INPUT", Info, "Input type not exercised",
		"File uploads and other non-synthetic inputs need a fixture. Recorded so coverage stays honest."},
	{"E_500_ON_ACTION", Critical, "Action triggered a server error",
		"Clicking or submitting caused a 5xx that the user would see as a failed operation."},
	{"E_CLIENT_ERROR_ON_ACTION", Major, "Action triggered an uncaught exception",
		"The handler threw. The user-visible result is a frozen or partially updated screen."},
	{"E_FAILED_REQUEST_ON_ACTION", Major, "Action triggered a failed request",
		"The request never produced a response. Check the evidence bundle for the target."},
	{"E_NAV_TO_NONOK", Critical, "Action navigated to an error page",
		"The control promised a destination and delivered 4xx/5xx."},
	{"E_NEW_TAB", Info, "Control opens a new tab",
		"Recorded for coverage; popups are closed immediately."},
	{"E_VALIDATION_NOT_ENFORCED", Major, "Empty required submit was accepted",
		"Submitting the form with required fields empty navigated or succeeded. Either the attributes are decorative or the handler does not read them."},
	{"E_500_ON_INVALID", Critical, "Invalid submit reached the server and failed",
		"Client validation let a known-invalid payload through and the server returned 5xx instead of a 4xx validation response."},
	{"E_500_ON_VALID", Critical, "Valid submit returned a server error",
		"The tool filled the form with values the fields advertise as valid, and the server rejected the request with 5xx."},
	{"E_FORM_SUCCESS_UNVERIFIED", Major, "Submit produced no user-visible feedback",
		"No navigation, no dialog, no DOM change, no error. Either the operation silently failed or nothing tells the user it worked."},
	{"E_FORM_NO_SUBMIT", Info, "Form has no submit control",
		"Enter may still submit it. Recorded for coverage."},
	{"E_DESTRUCTIVE_SKIPPED", Info, "Destructive control skipped",
		"The control's name matched the destructive vocabulary, so it was not clicked. Deletion needs --allow-delete; paying, sending, publishing and logging out need --allow-side-effects. --allow-destructive grants both. Only do that on an environment whose data can be thrown away."},
	{"E_OUT_OF_SCOPE", Info, "Link outside scope not followed",
		"Off-origin or excluded by the include/exclude rules."},
}

// Catalogue indexes specs by code.
var Catalogue = func() map[string]FindingSpec {
	out := make(map[string]FindingSpec, len(specs))
	for _, s := range specs {
		if _, dup := out[s.Code]; dup {
			panic("duplicate finding code: " + s.Code)
		}
		out[s.Code] = s
	}
	if len(out) != len(specs) {
		panic("duplicate finding code in catalogue")
	}
	return out
}()

type Finding struct {
	Code        string         `json:"code"`
	Severity    Severity       `json:"severity"`
	Title       string         `json:"title"`
	Why         string         `json:"why"`
	PageURL     string         `json:"page_url"`
	Element     string         `json:"element,omitempty"`
	ProbeKind   string         `json:"probe_kind,omitempty"`
	Payload     string         `json:"payload,omitempty"`
	Evidence    []string       `json:"evidence,omitempty"`
	Detail      map[string]any `json:"detail,omitempty"`
	AlsoOn      []string       `json:"also_on,omitempty"`
	Occurrences int            `json:"occurrences,omitempty"`
}

// NewFinding fills severity and prose from the catalogue. It panics on an
// unregistered code: a finding nobody catalogued is a bug in the tool.
func NewFinding(code, pageURL string) Finding {
	spec, ok := Catalogue[code]
	if !ok {
		panic("unregistered finding code: " + code)
	}
	return Finding{
		Code: code, Severity: spec.Severity, Title: spec.Title,
		Why: spec.Why, PageURL: pageURL, Detail: map[string]any{},
	}
}

func (f Finding) Rank() int {
	if r, ok := SeverityOrder[f.Severity]; ok {
		return r
	}
	return 9
}

// Specs returns the catalogue sorted worst-first, for `afford checks` and docs.
func Specs() []FindingSpec {
	out := make([]FindingSpec, 0, len(specs))
	out = append(out, specs...)
	sort.Slice(out, func(i, j int) bool {
		ri, rj := SeverityOrder[out[i].Severity], SeverityOrder[out[j].Severity]
		if ri != rj {
			return ri < rj
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// Key identifies a defect independently of the page it was seen on, so shared
// chrome (a nav link that 404s) is one finding with N locations, not N findings.
func (f Finding) Key() string {
	parts := []string{f.Code, f.Element}
	for _, name := range []string{"url", "status", "error", "requests"} {
		if v, ok := f.Detail[name]; ok {
			parts = append(parts, name+"="+fmt.Sprint(v))
		}
	}
	return strings.Join(parts, "|")
}

type Option struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

type Element struct {
	Idx         int               `json:"idx"`
	Tag         string            `json:"tag"`
	Role        string            `json:"role"`
	Name        string            `json:"name"`
	Fingerprint string            `json:"fingerprint"`
	Nth         int               `json:"nth"`
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	NameAttr    string            `json:"name_attr"`
	Placeholder string            `json:"placeholder"`
	Href        string            `json:"href"`
	Target      string            `json:"target"`
	Disabled    bool              `json:"disabled"`
	Required    bool              `json:"required"`
	ReadOnly    bool              `json:"readonly"`
	Visible     bool              `json:"visible"`
	Rect        []float64         `json:"rect"`
	FormIndex   *int              `json:"form_index"`
	Options     []Option          `json:"options"`
	LabelCount  int               `json:"label_count"`
	InForm      bool              `json:"in_form"`
	Src         string            `json:"src"`
	Attrs       map[string]string `json:"attrs"`
}

func (e Element) IsTextInput() bool {
	if e.Tag != "input" && e.Tag != "textarea" {
		return false
	}
	switch e.Type {
	case "submit", "button", "reset", "image", "checkbox", "radio", "file", "hidden":
		return false
	}
	return true
}

// IsSubmit reports whether activating this control submits its form. A button
// outside a form has the default submit type but nothing to submit, so it is
// still an ordinary control and must be clicked.
func (e Element) IsSubmit() bool {
	return e.InForm && (e.Type == "submit" || e.Type == "image")
}

type FormSpec struct {
	Index      int    `json:"index"`
	Action     string `json:"action"`
	Method     string `json:"method"`
	Name       string `json:"name"`
	ControlIdx []int  `json:"control_idxs"`
	SubmitIdx  []int  `json:"submit_idxs"`
}

type ScanResult struct {
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Controls      []Element  `json:"controls"`
	Forms         []FormSpec `json:"forms"`
	BodyLen       int        `json:"body_len"`
	NodeCount     int        `json:"node_count"`
	OverflowX     bool       `json:"overflow_x"`
	ScrollWidth   int        `json:"scroll_width"`
	ViewportWidth int        `json:"viewport_width"`
}

func (s ScanResult) ByIdx(idx int) (Element, bool) {
	for _, e := range s.Controls {
		if e.Idx == idx {
			return e, true
		}
	}
	return Element{}, false
}

type RequestRecord struct {
	URL          string `json:"url"`
	Method       string `json:"method"`
	ResourceType string `json:"resource_type"`
	Kind         string `json:"kind"`
	Status       int    `json:"status,omitempty"`
	Failed       string `json:"failed,omitempty"`
}

type Observation struct {
	URLBefore       string          `json:"url_before"`
	URLAfter        string          `json:"url_after"`
	Title           string          `json:"title"`
	Status          int             `json:"status,omitempty"`
	Navigated       bool            `json:"navigated"`
	Dialogs         []string        `json:"dialogs,omitempty"`
	ConsoleErrors   []string        `json:"console_errors,omitempty"`
	ConsoleWarnings []string        `json:"console_warnings,omitempty"`
	PageErrors      []string        `json:"page_errors,omitempty"`
	Requests        []RequestRecord `json:"requests,omitempty"`
	BodyLen         int             `json:"body_len"`
	Effect          string          `json:"effect"`
	ActionError     string          `json:"action_error,omitempty"`
	Occluded        bool            `json:"occluded,omitempty"`
	Popups          int             `json:"popups,omitempty"`
	DurationMS      int             `json:"duration_ms"`
	Extra           map[string]any  `json:"extra,omitempty"`
}

// ErrorRequests returns requests that failed or returned 4xx/5xx.
func (o Observation) ErrorRequests() []RequestRecord {
	out := make([]RequestRecord, 0, 4)
	for _, r := range o.Requests {
		if r.Status >= 400 || r.Failed != "" {
			out = append(out, r)
		}
	}
	return out
}

type Page struct {
	URL          string    `json:"url"`
	Template     string    `json:"template"`
	Depth        int       `json:"depth"`
	Parent       string    `json:"parent,omitempty"`
	Status       int       `json:"status,omitempty"`
	Title        string    `json:"title"`
	LoadMS       int64     `json:"load_ms"`
	SettledMS    int64     `json:"settled_ms"`
	Controls     int       `json:"controls"`
	Screenshot   string    `json:"screenshot,omitempty"`
	VisitedAt    time.Time `json:"visited_at"`
	Probes       int       `json:"probes"`
	AuthRedirect bool      `json:"auth_redirect,omitempty"`
}

type Summary struct {
	RunID           string           `json:"run_id"`
	BaseURL         string           `json:"base_url"`
	StartedAt       time.Time        `json:"started_at"`
	FinishedAt      time.Time        `json:"finished_at"`
	PagesDiscovered int              `json:"pages_discovered"`
	PagesVisited    int              `json:"pages_visited"`
	PagesFailed     int              `json:"pages_failed"`
	ControlsTotal   int              `json:"controls_total"`
	ControlsProbed  int              `json:"controls_probed"`
	ControlsSkipped int              `json:"controls_skipped"`
	FormsTotal      int              `json:"forms_total"`
	FormsProbed     int              `json:"forms_probed"`
	InputsSwept     int              `json:"inputs_swept"`
	PayloadsSent    int              `json:"payloads_sent"`
	Counts          map[Severity]int `json:"counts"`
	NotTested       []string         `json:"not_tested"`
}
