// Package history keeps one line per run so trends are visible: finding counts,
// pages, load-time percentiles. It is an index, not an archive; the artifacts
// stay under runs/.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nyimbi/afford/internal/model"
)

// Entry is one run.
type Entry struct {
	RunID      string           `json:"run_id"`
	BaseURL    string           `json:"base_url"`
	At         time.Time        `json:"at"`
	Pages      int              `json:"pages"`
	Counts     map[string]int   `json:"counts"`
	Totals     map[string]int   `json:"totals,omitempty"`
	New        int              `json:"new,omitempty"`
	Fixed      int              `json:"fixed,omitempty"`
	LoadP50MS  int64            `json:"load_p50_ms"`
	LoadP95MS  int64            `json:"load_p95_ms"`
	Settled50  int64            `json:"settled_p50_ms"`
	Settled95  int64            `json:"settled_p95_ms"`
	DurationMS int64            `json:"duration_ms"`
	Extra      map[string]int64 `json:"extra,omitempty"`
}

func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted)*p + 99) / 100
	if idx > 0 {
		idx--
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func counts(in map[model.Severity]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		out[string(k)] = v
	}
	return out
}

// Build derives an entry from a finished run.
func Build(summary model.Summary, pages []model.Page) Entry {
	var loads, settled []int64
	for _, p := range pages {
		if p.LoadMS > 0 {
			loads = append(loads, p.LoadMS)
		}
		if p.SettledMS > 0 {
			settled = append(settled, p.SettledMS)
		}
	}
	sort.Slice(loads, func(i, j int) bool { return loads[i] < loads[j] })
	sort.Slice(settled, func(i, j int) bool { return settled[i] < settled[j] })
	e := Entry{
		RunID: summary.RunID, BaseURL: summary.BaseURL, At: summary.FinishedAt,
		Pages: summary.PagesVisited, Counts: counts(summary.Counts), Totals: counts(summary.Totals),
		LoadP50MS: percentile(loads, 50), LoadP95MS: percentile(loads, 95),
		Settled50: percentile(settled, 50), Settled95: percentile(settled, 95),
		DurationMS: summary.FinishedAt.Sub(summary.StartedAt).Milliseconds(),
	}
	if summary.Baseline != nil {
		e.New, e.Fixed = summary.Baseline.New, len(summary.Baseline.Fixed)
	}
	return e
}

// Append writes an entry to the history file.
func Append(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	blob, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(blob, '\n'))
	return err
}

// Load reads the history, oldest first, skipping lines that do not parse.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.RunID != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Previous returns the latest entry for the same base URL before the run.
func Previous(entries []Entry, baseURL, runID string) *Entry {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].BaseURL == baseURL && entries[i].RunID != runID {
			return &entries[i]
		}
	}
	return nil
}

// Delta describes how a run moved against the previous one, in words a person
// can scan: what got worse first.
func Delta(cur Entry, prev *Entry) string {
	if prev == nil {
		return "first recorded run for this base URL"
	}
	var parts []string
	for _, level := range []string{"blocker", "critical", "major", "minor"} {
		if d := cur.Counts[level] - prev.Counts[level]; d != 0 {
			parts = append(parts, fmt.Sprintf("%+d %s", d, level))
		}
	}
	if prev.LoadP95MS > 0 {
		change := float64(cur.LoadP95MS-prev.LoadP95MS) / float64(prev.LoadP95MS) * 100
		if change >= 20 || change <= -20 {
			parts = append(parts, fmt.Sprintf("load p95 %d→%d ms (%+.0f%%)", prev.LoadP95MS, cur.LoadP95MS, change))
		}
	}
	if d := cur.Pages - prev.Pages; d != 0 {
		parts = append(parts, fmt.Sprintf("%+d pages", d))
	}
	if len(parts) == 0 {
		return "no change against run " + prev.RunID
	}
	return strings.Join(parts, ", ") + " against run " + prev.RunID
}
