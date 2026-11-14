// Package store keeps the run in memory and mirrors it to an append-only JSONL
// file, so a crash costs the rest of the crawl, not the pages already recorded.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"afford/internal/model"
)

// key identifies a defect independently of the page it was seen on, so shared
// chrome (a nav link that 404s) is one finding with N locations, not N findings.
func key(f model.Finding) string {
	parts := []string{f.Code, f.Element}
	for _, k := range []string{"url", "status", "error", "requests", "error"} {
		if v, ok := f.Detail[k]; ok {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	return strings.Join(parts, "|")
}

type event struct {
	Kind    string         `json:"kind"`
	Page    *model.Page    `json:"page,omitempty"`
	Finding *model.Finding `json:"finding,omitempty"`
	Probe   string         `json:"probe,omitempty"` // page URL
}

type Store struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	Pages    []model.Page
	Findings []model.Finding
	probes   map[string]int
	index    map[string]int
}

func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, f: f, probes: map[string]int{}, index: map[string]int{}}
	if err := s.replay(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// replay loads events written by an earlier process. A truncated final line is
// ignored: that is what a crash mid-write looks like.
func (s *Store) replay() error {
	rf, err := os.Open(s.path)
	if err != nil {
		return nil
	}
	defer rf.Close()
	sc := bufio.NewScanner(rf)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		switch e.Kind {
		case "page":
			if e.Page != nil {
				s.Pages = append(s.Pages, *e.Page)
			}
		case "finding":
			if e.Finding != nil {
				s.merge(*e.Finding)
			}
		case "probe":
			s.probes[e.Probe]++
		}
	}
	return nil
}

func (s *Store) write(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = s.f.Write(append(blob, '\n'))
}

func (s *Store) AddPage(p model.Page) {
	s.mu.Lock()
	s.Pages = append(s.Pages, p)
	s.mu.Unlock()
	s.write(event{Kind: "page", Page: &p})
}

func (s *Store) AddProbe(pageURL string) {
	s.mu.Lock()
	s.probes[pageURL]++
	s.mu.Unlock()
	s.write(event{Kind: "probe", Probe: pageURL})
}

func (s *Store) AddFinding(f model.Finding) {
	s.mu.Lock()
	s.merge(f)
	merged := s.Findings[s.index[key(f)]]
	s.mu.Unlock()
	s.write(event{Kind: "finding", Finding: &merged})
}

// merge is called with the lock held.
func (s *Store) merge(f model.Finding) {
	k := key(f)
	if idx, ok := s.index[k]; ok {
		existing := &s.Findings[idx]
		if existing.PageURL != f.PageURL {
			seen := false
			for _, u := range existing.AlsoOn {
				if u == f.PageURL {
					seen = true
					break
				}
			}
			if !seen && len(existing.AlsoOn) < 50 {
				existing.AlsoOn = append(existing.AlsoOn, f.PageURL)
			}
			existing.Occurrences = len(existing.AlsoOn) + 1
		}
		return
	}
	s.index[k] = len(s.Findings)
	s.Findings = append(s.Findings, f)
}

func (s *Store) Snapshot() ([]model.Page, []model.Finding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pages := make([]model.Page, len(s.Pages))
	copy(pages, s.Pages)
	findings := make([]model.Finding, len(s.Findings))
	copy(findings, s.Findings)
	for i := range pages {
		pages[i].Probes = s.probes[pages[i].URL]
	}
	return pages, findings
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.f.Sync()
	return s.f.Close()
}
