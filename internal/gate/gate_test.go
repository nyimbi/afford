package gate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyimbi/afford/internal/config"
	"github.com/nyimbi/afford/internal/discover"
	"github.com/nyimbi/afford/internal/model"
)

var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func finding(code, page, element string, detail map[string]any) model.Finding {
	f := model.NewFinding(code, page)
	f.Element = element
	for k, v := range detail {
		f.Detail[k] = v
	}
	return f
}

func cfgIn(t *testing.T) config.Config {
	cfg := config.Default()
	cfg.Run.BaseURL = "http://app.test"
	dir := t.TempDir()
	cfg.Baseline.File = filepath.Join(dir, "baseline.json")
	cfg.Baseline.SuppressFile = filepath.Join(dir, "suppress.toml")
	return cfg
}

func pages(urls ...string) []model.Page {
	var out []model.Page
	for _, u := range urls {
		out = append(out, model.Page{URL: u, Template: discover.Template(u)})
	}
	return out
}

func TestFingerprintIgnoresVolatileDetail(t *testing.T) {
	a := finding("E_CONSOLE_ERROR", "http://app.test/a", "", map[string]any{"error": "boom after 123 ms for 550e8400-e29b-41d4-a716-446655440000"})
	b := finding("E_CONSOLE_ERROR", "http://app.test/b", "", map[string]any{"error": "boom after 9876 ms for 11111111-2222-3333-4444-555555555555"})
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("volatile numbers and ids changed the fingerprint")
	}
	c := finding("E_CONSOLE_ERROR", "http://app.test/a", "", map[string]any{"error": "a different failure"})
	if Fingerprint(a) == Fingerprint(c) {
		t.Fatal("different errors share a fingerprint")
	}
	d := finding("E_HTTP_4XX", "http://app.test/a", "", map[string]any{"status": 404, "url": "http://app.test/x"})
	e := finding("E_HTTP_4XX", "http://app.test/a", "", map[string]any{"status": 403, "url": "http://app.test/x"})
	if Fingerprint(d) == Fingerprint(e) {
		t.Fatal("status is part of identity")
	}
}

func TestAcceptThenDiffReportsOneNewOneFixed(t *testing.T) {
	cfg := cfgIn(t)
	keep := finding("E_DEAD_CONTROL", "http://app.test/a", "button “Save”", nil)
	gone := finding("E_OCCLUDED", "http://app.test/a", "button “Help”", nil)
	cfg.Baseline.Accept = true
	res, err := Apply([]model.Finding{keep, gone}, Options{Cfg: cfg, Pages: pages("http://app.test/a"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.Baseline == nil || !res.Baseline.Accepted || res.Counts[model.Major] != 0 {
		t.Fatalf("accept run must not gate: %+v counts=%v", res.Baseline, res.Counts)
	}

	cfg.Baseline.Accept = false
	fresh := finding("E_CONSOLE_ERROR", "http://app.test/a", "", map[string]any{"error": "new failure"})
	res, err = Apply([]model.Finding{keep, fresh}, Options{Cfg: cfg, Pages: pages("http://app.test/a"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	b := res.Baseline
	if b.New != 1 || b.Known != 1 || len(b.Fixed) != 1 || b.Fixed[0].Code != "E_OCCLUDED" {
		t.Fatalf("diff wrong: %+v", b)
	}
	if res.Counts[model.Minor] != 1 || res.Counts[model.Major] != 0 {
		t.Fatalf("only the new finding gates: %v", res.Counts)
	}
}

func TestUnvisitedPageIsNotReachedNotFixed(t *testing.T) {
	cfg := cfgIn(t)
	f := finding("E_DEAD_CONTROL", "http://app.test/deep", "button “Go”", nil)
	cfg.Baseline.Accept = true
	if _, err := Apply([]model.Finding{f}, Options{Cfg: cfg, Pages: pages("http://app.test/deep"), Now: now}); err != nil {
		t.Fatal(err)
	}
	cfg.Baseline.Accept = false
	res, _ := Apply(nil, Options{Cfg: cfg, Pages: pages("http://app.test/"), Now: now})
	if len(res.Baseline.Fixed) != 0 || len(res.Baseline.NotReached) != 1 {
		t.Fatalf("a page the crawl never saw cannot be fixed: %+v", res.Baseline)
	}
}

func TestSpreadToNewPageIsNew(t *testing.T) {
	cfg := cfgIn(t)
	f := finding("E_DEAD_CONTROL", "http://app.test/a", "button “Save”", nil)
	cfg.Baseline.Accept = true
	_, _ = Apply([]model.Finding{f}, Options{Cfg: cfg, Pages: pages("http://app.test/a"), Now: now})
	cfg.Baseline.Accept = false
	f.AlsoOn = []string{"http://app.test/b"}
	res, _ := Apply([]model.Finding{f}, Options{Cfg: cfg, Pages: pages("http://app.test/a", "http://app.test/b"), Now: now})
	if res.Findings[0].Status != "new" || res.Findings[0].NewPages != 1 {
		t.Fatalf("a known defect on a new page must gate: %+v", res.Findings[0])
	}
}

func TestSuppressionRequiresOwnerAndExpiry(t *testing.T) {
	cfg := cfgIn(t)
	os.WriteFile(cfg.Baseline.SuppressFile, []byte("[[suppress]]\ncode = \"E_CONSOLE_ERROR\"\nreason = \"vendor\"\n"), 0o644)
	if _, err := Apply(nil, Options{Cfg: cfg, Now: now}); err == nil {
		t.Fatal("a suppression without owner and expiry must be rejected")
	}
}

func TestSuppressionHidesThenExpires(t *testing.T) {
	cfg := cfgIn(t)
	rule := "[[suppress]]\ncode = \"E_CONSOLE_ERROR\"\npage = \"/admin/**\"\nmatch = \"vendor-sdk\"\nreason = \"third-party SDK\"\nowner = \"web-team\"\nexpires = \"2026-12-01\"\n"
	os.WriteFile(cfg.Baseline.SuppressFile, []byte(rule), 0o644)
	hit := finding("E_CONSOLE_ERROR", "http://app.test/admin/users", "", map[string]any{"error": "vendor-sdk exploded"})
	miss := finding("E_CONSOLE_ERROR", "http://app.test/orders", "", map[string]any{"error": "vendor-sdk exploded"})
	other := finding("E_CONSOLE_ERROR", "http://app.test/admin/users", "", map[string]any{"error": "our own bug"})
	res, err := Apply([]model.Finding{hit, miss, other}, Options{Cfg: cfg, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, f := range res.Findings {
		by[f.PageURL+"|"+str(f.Detail["error"])] = f.Status
	}
	if by["http://app.test/admin/users|vendor-sdk exploded"] != "suppressed" {
		t.Fatalf("matching finding not suppressed: %v", by)
	}
	if by["http://app.test/orders|vendor-sdk exploded"] == "suppressed" || by["http://app.test/admin/users|our own bug"] == "suppressed" {
		t.Fatalf("suppression leaked past its page/match scope: %v", by)
	}
	if res.Suppressed != 1 {
		t.Fatalf("suppressed count = %d", res.Suppressed)
	}

	late := now.AddDate(0, 0, 30)
	res, _ = Apply([]model.Finding{hit}, Options{Cfg: cfg, Now: late})
	var expired, active bool
	for _, f := range res.Findings {
		expired = expired || f.Code == "E_SUPPRESSION_EXPIRED"
		active = active || (f.Code == "E_CONSOLE_ERROR" && f.Status != "suppressed")
	}
	if !expired || !active {
		t.Fatalf("expired rule must nag and stop hiding: expired=%v active=%v", expired, active)
	}
}

func TestPolicySeverityOverrideAndOff(t *testing.T) {
	cfg := cfgIn(t)
	cfg.Policy.Severity = map[string]string{"E_UUID_EXPOSED": "major", "E_NO_TITLE": "off"}
	res, _ := Apply([]model.Finding{
		finding("E_UUID_EXPOSED", "http://app.test/a", "select “x”", nil),
		finding("E_NO_TITLE", "http://app.test/a", "", nil),
	}, Options{Cfg: cfg, Now: now})
	if len(res.Findings) != 1 || res.Findings[0].Severity != model.Major || res.Counts[model.Major] != 1 {
		t.Fatalf("policy not applied: %+v", res.Findings)
	}
}

func TestUnconfirmedDoesNotGate(t *testing.T) {
	cfg := cfgIn(t)
	f := finding("E_DEAD_CONTROL", "http://app.test/a", "button “Flaky”", nil)
	f.Confidence = "unconfirmed"
	res, _ := Apply([]model.Finding{f}, Options{Cfg: cfg, Now: now})
	if res.Counts[model.Major] != 0 || res.Totals[model.Major] != 1 || res.Unconfirmed != 1 {
		t.Fatalf("unconfirmed finding gated: counts=%v totals=%v", res.Counts, res.Totals)
	}
}

func TestEntriesUnionTemplatesAcrossSharedFingerprint(t *testing.T) {
	a := model.Finding{Code: "E_OVERFLOW_X", PageURL: "http://x/", Detail: map[string]any{"popup_url": "http://x/overflow"}}
	b := model.Finding{Code: "E_OVERFLOW_X", PageURL: "http://x/overflow"}
	a.Fingerprint, b.Fingerprint = Fingerprint(a), Fingerprint(b)
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("test premise: fingerprints differ")
	}
	got := entriesOf([]model.Finding{a, b})
	if len(got) != 1 || len(got[0].Templates) != 2 {
		t.Fatalf("want one entry owning both pages, got %+v", got)
	}
}
