package run

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mxschmitt/playwright-go"

	"github.com/nyimbi/afford/internal/config"
	"github.com/nyimbi/afford/internal/store"
)

func TestVisitSafelyRecoversAndRecordsFinding(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	runner := New(config.Default(), st, "run-test", t.TempDir(), context.Background())
	runner.visitPage = func(playwright.BrowserContext, Item) []found {
		panic("simulated probe failure")
	}

	urls := runner.visitSafely(nil, Item{URL: "http://app.test/orders"})
	if urls != nil {
		t.Fatalf("panicking visit returned %v", urls)
	}
	_, findings := st.Snapshot()
	if len(findings) != 1 {
		t.Fatalf("expected one harness finding, got %d", len(findings))
	}
	got := findings[0]
	if got.Code != "E_HARNESS_TIMEOUT" {
		t.Errorf("code = %s", got.Code)
	}
	if got.PageURL != "http://app.test/orders" {
		t.Errorf("page = %s", got.PageURL)
	}
	if len(got.Evidence) == 0 || got.Detail["error"] == nil {
		t.Errorf("harness finding has no evidence: %+v", got)
	}
}

func TestVisitSafelyReturnsLinksOnSuccess(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	runner := New(config.Default(), st, "run-test", t.TempDir(), context.Background())
	runner.visitPage = func(playwright.BrowserContext, Item) []found {
		return []found{{URL: "http://app.test/next", Depth: 1}}
	}

	urls := runner.visitSafely(nil, Item{URL: "http://app.test/"})
	if len(urls) != 1 || urls[0].URL != "http://app.test/next" {
		t.Fatalf("links not returned: %+v", urls)
	}
	if _, findings := st.Snapshot(); len(findings) != 0 {
		t.Fatalf("successful visit produced findings: %+v", findings)
	}
}
