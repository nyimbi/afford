package sarif

import (
	"encoding/json"
	"testing"

	"github.com/nyimbi/afford/internal/model"
)

func TestRenderMapsLevelsAndState(t *testing.T) {
	a := model.NewFinding("E_500_ON_VALID", "http://x/orders")
	a.Status, a.Fingerprint = "new", "abc123"
	b := model.NewFinding("E_OVERFLOW_X", "http://x/")
	b.Status = "suppressed"
	b.Suppression = "vendor widget"
	c := model.NewFinding("E_DEAD_CONTROL", "http://x/")
	c.Confidence = "unconfirmed"
	blob, err := Render([]model.Finding{a, b, c}, "t")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Rules []map[string]any `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID        string                `json:"ruleId"`
				Level         string                `json:"level"`
				BaselineState string                `json:"baselineState"`
				Suppressions  []map[string]any      `json:"suppressions"`
				Fingerprints  map[string]string     `json:"partialFingerprints"`
				Message       struct{ Text string } `json:"message"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatal(err)
	}
	r := doc.Runs[0].Results
	if doc.Version != "2.1.0" || len(r) != 3 {
		t.Fatalf("bad document: %s", blob)
	}
	if r[0].Level != "error" || r[0].BaselineState != "new" || r[0].Fingerprints["github.com/nyimbi/afford/v1"] != "abc123" {
		t.Errorf("new critical finding: %+v", r[0])
	}
	if r[1].Level != "note" || len(r[1].Suppressions) != 1 {
		t.Errorf("suppressed finding must carry a suppression: %+v", r[1])
	}
	if want := "unconfirmed"; !contains(r[2].Message.Text, want) {
		t.Errorf("unconfirmed finding must say so: %q", r[2].Message.Text)
	}
	if len(doc.Runs[0].Tool.Driver.Rules) != len(model.Specs()) {
		t.Errorf("every catalogued code must be a rule")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
