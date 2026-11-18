package oracles

import (
	"testing"

	"github.com/nyimbi/afford/internal/model"
)

func selectWith(name string, options ...model.Option) model.Element {
	return model.Element{
		Tag: "select", Name: name, NameAttr: name, Visible: true, Src: "input",
		Options: options,
	}
}

func codes(findings []model.Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range findings {
		out[f.Code] = true
	}
	return out
}

func TestOptionsAuditExposedUUID(t *testing.T) {
	visible := selectWith("owner",
		model.Option{Value: "1", Label: "Alice"},
		model.Option{Value: "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			Label: "3f2504e0-4f89-11d3-9a0c-0305e82c3301"},
	)
	got := codes(OptionsAudit([]model.Element{visible}, "http://x.test/p"))
	if !got["E_UUID_EXPOSED"] {
		t.Fatalf("visible UUID label not reported: %v", got)
	}
	if got["E_UUID_IN_OPTION_VALUE"] {
		t.Error("a visible UUID label should not also be reported as a hidden value")
	}
}

func TestOptionsAuditUUIDValuesOnly(t *testing.T) {
	hidden := selectWith("team",
		model.Option{Value: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Label: "Support"},
		model.Option{Value: "9b2f1c4a-8e3d-4b6a-9f12-5d7c8e9a0b1c", Label: "Billing"},
	)
	got := codes(OptionsAudit([]model.Element{hidden}, "http://x.test/p"))
	if !got["E_UUID_IN_OPTION_VALUE"] {
		t.Fatalf("UUID option values not reported: %v", got)
	}
	if got["E_UUID_EXPOSED"] {
		t.Error("readable labels should not be reported as visible UUIDs")
	}
}

func TestOptionsAuditEmptyAndClean(t *testing.T) {
	empty := selectWith("region", model.Option{Value: "", Label: "Choose a region"})
	clean := selectWith("country",
		model.Option{Value: "ke", Label: "Kenya"},
		model.Option{Value: "tz", Label: "Tanzania"},
	)
	got := codes(OptionsAudit([]model.Element{empty, clean}, "http://x.test/p"))
	if !got["E_EMPTY_SELECT"] {
		t.Fatalf("placeholder-only select not reported: %v", got)
	}
	if len(OptionsAudit([]model.Element{clean}, "http://x.test/p")) != 0 {
		t.Error("a populated select should produce no finding")
	}
}

func TestOptionsAuditULID(t *testing.T) {
	el := selectWith("order",
		model.Option{Value: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Label: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
	)
	findings := OptionsAudit([]model.Element{el}, "http://x.test/p")
	if len(findings) != 1 || findings[0].Detail["kind"] != "ulid" {
		t.Fatalf("ULID not identified: %+v", findings)
	}
}

func TestDeadLinkOnErrorStatus(t *testing.T) {
	link := model.Element{Tag: "a", Name: "missing", Src: "link", Visible: true}
	obs := model.Observation{
		URLBefore: "http://x.test/", URLAfter: "http://x.test/missing",
		Navigated: true, Status: 404,
	}
	got := codes(Evaluate(obs, "http://x.test/", "action", &link))
	if !got["E_DEAD_LINK"] {
		t.Fatalf("404 link not reported as dead: %v", got)
	}
	if got["E_NAV_TO_NONOK"] {
		t.Error("an anchor should report E_DEAD_LINK, not the generic navigation code")
	}
}

func TestDeadLinkOnSoft404(t *testing.T) {
	link := model.Element{Tag: "a", Name: "orders", Src: "link", Visible: true}
	obs := model.Observation{
		URLBefore: "http://x.test/", URLAfter: "http://x.test/orders/9",
		Navigated: true, Status: 200,
		Extra: map[string]any{"text_sample": "Page not found. The order does not exist."},
	}
	findings := Evaluate(obs, "http://x.test/", "action", &link)
	found := false
	for _, f := range findings {
		if f.Code == "E_DEAD_LINK" && f.Detail["soft_404"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("soft 404 not reported: %+v", findings)
	}
}

func TestSoft404IgnoresSiteChrome(t *testing.T) {
	link := model.Element{Tag: "a", Name: "dashboard", Src: "link", Visible: true}
	obs := model.Observation{
		URLBefore: "http://x.test/", URLAfter: "http://x.test/dashboard",
		Navigated: true, Status: 200,
		Extra: map[string]any{"text_sample": "home dashboard dead crash missing soft 404 dropdown"},
	}
	if got := codes(Evaluate(obs, "http://x.test/", "action", &link)); got["E_DEAD_LINK"] {
		t.Fatalf("nav labels must not make every link a dead link: %v", got)
	}
}

func TestNavigationToErrorIsNotADeadLinkForForms(t *testing.T) {
	button := model.Element{Tag: "button", Name: "Save", Src: "button", Visible: true}
	obs := model.Observation{
		URLBefore: "http://x.test/", URLAfter: "http://x.test/error",
		Navigated: true, Status: 500,
	}
	got := codes(Evaluate(obs, "http://x.test/", "action", &button))
	if !got["E_NAV_TO_NONOK"] || got["E_DEAD_LINK"] {
		t.Fatalf("non-anchor navigation codes wrong: %v", got)
	}
}
