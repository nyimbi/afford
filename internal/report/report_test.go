package report

import (
	"strings"
	"testing"
	"time"

	"afford/internal/model"
)

func finding(code, page string, severity model.Severity) model.Finding {
	f := model.NewFinding(code, page)
	f.Severity = severity
	return f
}

func TestJUnitCountsTestsNotFindings(t *testing.T) {
	payload := Payload{
		Summary: model.Summary{StartedAt: time.Now()},
		Pages: []model.Page{
			{URL: "http://x.test/a"},
			{URL: "http://x.test/b"},
		},
		Findings: []model.Finding{
			finding("E_DEAD_LINK", "http://x.test/a", model.Major),
			finding("E_DEAD_CONTROL", "http://x.test/a", model.Major),
			finding("E_CONSOLE_ERROR", "http://x.test/b", model.Minor),
			finding("E_AUTH_WALL", "http://x.test/seed", model.Blocker),
		},
	}
	out := junit(payload)
	for _, want := range []string{`tests="3"`, `failures="2"`, `afford.run`, `E_DEAD_LINK, E_DEAD_CONTROL`} {
		if !strings.Contains(out, want) {
			t.Errorf("junit output missing %q:\n%s", want, out)
		}
	}
	// A page's hard findings are aggregated into one failure, not overwritten.
	if strings.Count(out, "<failure") != 2 {
		t.Errorf("expected 2 failures, got %d", strings.Count(out, "<failure"))
	}
}

func TestJUnitMergedFindingAppearsOnEveryPage(t *testing.T) {
	shared := finding("E_OUT_OF_SCOPE", "http://x.test/a", model.Major)
	shared.AlsoOn = []string{"http://x.test/b"}
	payload := Payload{
		Summary:  model.Summary{StartedAt: time.Now()},
		Pages:    []model.Page{{URL: "http://x.test/a"}, {URL: "http://x.test/b"}},
		Findings: []model.Finding{shared},
	}
	out := junit(payload)
	if strings.Count(out, "<failure") != 2 {
		t.Errorf("shared finding should fail both pages:\n%s", out)
	}
}

func TestFormatDetailIsReadable(t *testing.T) {
	got := formatDetail(map[string]any{"b": 2, "a": []string{"x", "y"}})
	if got != "a: [x y]\nb: 2" {
		t.Errorf("formatDetail = %q", got)
	}
}

func TestVisualLabelSaysOff(t *testing.T) {
	if visualLabel(false, 0) != "off" {
		t.Error("disabled visual mode must not read as zero diffs")
	}
	if visualLabel(true, 0) != "0" {
		t.Error("enabled visual mode should show the count")
	}
}
