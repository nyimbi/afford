package mcpserver

import (
	"strings"
	"testing"

	"afford/internal/model"
)

func TestFirstEvidencePrefersObservationEvidence(t *testing.T) {
	f := model.NewFinding("E_DEAD_CONTROL", "http://x.test/")
	f.Evidence = []string{"effect=none", "status=200"}
	if got := firstEvidence(f); got != "effect=none" {
		t.Errorf("got %q", got)
	}
}

func TestFirstEvidenceExplainsSkippedControl(t *testing.T) {
	f := model.NewFinding("E_DESTRUCTIVE_SKIPPED", "http://x.test/")
	f.Detail["kind"] = "delete"
	f.Detail["required_flag"] = "--allow-delete"
	got := firstEvidence(f)
	if !strings.Contains(got, "delete") || !strings.Contains(got, "--allow-delete") {
		t.Errorf("got %q", got)
	}
}

func TestFirstEvidenceFallsBackToDetail(t *testing.T) {
	cases := []struct {
		code   string
		detail map[string]any
		want   string
	}{
		{"E_OUT_OF_SCOPE", map[string]any{"reason": "matches exclude /api/**"}, "matches exclude"},
		{"E_NEW_TAB", map[string]any{"popups": []string{"http://x.test/a"}}, "popups"},
		{"E_VISUAL_DIFF", map[string]any{"pct": 2.5}, "pct"},
		{"E_UNLABELED_CONTROL", map[string]any{"name": "nickname"}, "nickname"},
	}
	for _, c := range cases {
		f := model.NewFinding(c.code, "http://x.test/")
		for k, v := range c.detail {
			f.Detail[k] = v
		}
		if got := firstEvidence(f); !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.code, got, c.want)
		}
	}
}

func TestFirstEvidenceEmptyWhenNothingToSay(t *testing.T) {
	f := model.NewFinding("E_NEW_TAB", "http://x.test/")
	if got := firstEvidence(f); got != "" {
		t.Errorf("got %q", got)
	}
}
