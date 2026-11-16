package history

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendLoadPreviousDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	a := Entry{RunID: "a", BaseURL: "http://x", Pages: 10, Counts: map[string]int{"critical": 1}, LoadP95MS: 100}
	b := Entry{RunID: "b", BaseURL: "http://x", Pages: 12, Counts: map[string]int{"critical": 3}, LoadP95MS: 200}
	other := Entry{RunID: "c", BaseURL: "http://y", Counts: map[string]int{}}
	for _, e := range []Entry{a, other, b} {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("load: %v %v", got, err)
	}
	prev := Previous(got, "http://x", "b")
	if prev == nil || prev.RunID != "a" {
		t.Fatalf("previous must skip other base URLs and the current run: %+v", prev)
	}
	d := Delta(b, prev)
	for _, want := range []string{"+2 critical", "load p95 100→200", "+2 pages", "against run a"} {
		if !strings.Contains(d, want) {
			t.Errorf("delta %q lacks %q", d, want)
		}
	}
	if !strings.Contains(Delta(a, nil), "first recorded") {
		t.Error("no previous run must say so")
	}
	if !strings.Contains(Delta(a, &a), "no change") {
		t.Error("identical runs must report no change")
	}
}
