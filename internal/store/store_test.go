package store

import (
	"path/filepath"
	"testing"

	"afford/internal/model"
)

func navFinding(page string) model.Finding {
	f := model.NewFinding("E_NAV_TO_NONOK", page)
	f.Element = "a “missing”"
	f.Detail["status"] = 404
	f.Detail["url"] = "http://x.test/missing"
	return f
}

func TestDedupeMergesSharedChrome(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.AddFinding(navFinding("http://x.test/"))
	s.AddFinding(navFinding("http://x.test/orders"))
	s.AddFinding(navFinding("http://x.test/orders"))

	_, findings := s.Snapshot()
	if len(findings) != 1 {
		t.Fatalf("expected one merged finding, got %d", len(findings))
	}
	if findings[0].PageURL != "http://x.test/" {
		t.Errorf("merged finding should keep the first page, got %s", findings[0].PageURL)
	}
	if findings[0].Occurrences != 2 || len(findings[0].AlsoOn) != 1 {
		t.Errorf("occurrences=%d also_on=%v", findings[0].Occurrences, findings[0].AlsoOn)
	}
}

func TestDistinctPagesWithDistinctDetailStaySeparate(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "run.jsonl"))
	defer s.Close()

	first := navFinding("http://x.test/")
	first.Detail["url"] = "http://x.test/a"
	second := navFinding("http://x.test/")
	second.Detail["url"] = "http://x.test/b"
	s.AddFinding(first)
	s.AddFinding(second)

	if _, findings := s.Snapshot(); len(findings) != 2 {
		t.Fatalf("expected two findings, got %d", len(findings))
	}
}

func TestReplayRestoresFindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	s, _ := Open(path)
	s.AddPage(model.Page{URL: "http://x.test/", Template: "/"})
	s.AddFinding(navFinding("http://x.test/"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	pages, findings := again.Snapshot()
	if len(pages) != 1 || len(findings) != 1 {
		t.Fatalf("replay restored %d pages and %d findings", len(pages), len(findings))
	}
}
