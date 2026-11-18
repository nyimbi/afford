// Package gate decides which findings can fail a run.
//
// Raw findings are facts about the app. The gate adds three judgements: policy
// (per-code severity), suppression (a named, dated exception) and the baseline
// (findings already accepted). Only what survives all three gates the exit code.
package gate

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/nyimbi/afford/internal/config"
	"github.com/nyimbi/afford/internal/discover"
	"github.com/nyimbi/afford/internal/model"
)

// Rule is one suppression. Reason, owner and expiry are mandatory: a
// suppression nobody owns is a permanent blind spot.
type Rule struct {
	Code    string `toml:"code"`
	Page    string `toml:"page"`
	Element string `toml:"element"`
	Match   string `toml:"match"`
	Reason  string `toml:"reason"`
	Owner   string `toml:"owner"`
	Expires string `toml:"expires"`

	re      *regexp.Regexp
	expires time.Time
}

type ruleFile struct {
	Suppress []Rule `toml:"suppress"`
}

// File is the on-disk baseline.
type File struct {
	Version    int                   `json:"version"`
	AcceptedAt time.Time             `json:"accepted_at"`
	BaseURL    string                `json:"base_url"`
	Entries    []model.BaselineEntry `json:"entries"`
}

// Result is the gated view of a run.
type Result struct {
	Findings []model.Finding
	Counts   map[model.Severity]int
	Totals   map[model.Severity]int
	Baseline *model.BaselineSummary
	// Unconfirmed and Suppressed count findings reported but not gating.
	Unconfirmed int
	Suppressed  int
}

// LoadRules reads a suppression file. A missing file is no rules; a malformed
// rule is an error, because a rule that silently fails to load un-suppresses.
func LoadRules(path string, now time.Time) ([]Rule, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	var rf ruleFile
	if _, err := toml.DecodeFile(path, &rf); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var problems []string
	for i := range rf.Suppress {
		r := &rf.Suppress[i]
		where := fmt.Sprintf("%s: rule %d", path, i+1)
		if _, ok := model.Catalogue[r.Code]; !ok {
			problems = append(problems, where+": unknown code "+r.Code)
		}
		if strings.TrimSpace(r.Reason) == "" || strings.TrimSpace(r.Owner) == "" {
			problems = append(problems, where+": reason and owner are required")
		}
		exp, err := time.Parse("2006-01-02", r.Expires)
		if err != nil {
			problems = append(problems, where+": expires must be YYYY-MM-DD")
		}
		r.expires = exp.Add(24*time.Hour - time.Second)
		if r.Match != "" {
			re, err := regexp.Compile(r.Match)
			if err != nil {
				problems = append(problems, where+": match: "+err.Error())
			}
			r.re = re
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return rf.Suppress, nil
}

func (r Rule) expired(now time.Time) bool { return now.After(r.expires) }

var (
	digits   = regexp.MustCompile(`\d+`)
	uuidish  = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	longhex  = regexp.MustCompile(`(?i)\b[0-9a-f]{16,}\b`)
	spacerun = regexp.MustCompile(`\s+`)
)

// normalize strips the parts of a message that change between runs: counts,
// identifiers, timings. It is the difference between a baseline and a lottery.
func normalize(s string, limit int) string {
	s = uuidish.ReplaceAllString(s, "<id>")
	s = longhex.ReplaceAllString(s, "<hex>")
	s = digits.ReplaceAllString(s, "#")
	s = spacerun.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = s[:limit]
	}
	return s
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// discriminator picks the one detail that tells two findings of the same code
// and element apart, and nothing volatile.
func discriminator(f model.Finding) string {
	d := f.Detail
	switch f.Code {
	case "E_HTTP_4XX", "E_HTTP_5XX", "E_NAV_TO_NONOK", "E_DEAD_LINK", "E_500_ON_ACTION":
		parts := []string{str(d["status"])}
		if u := str(d["url"]); u != "" {
			parts = append(parts, discover.Template(u))
		}
		if rs, ok := d["requests"].([]string); ok && len(rs) > 0 {
			parts = append(parts, discover.Template(rs[0]))
		}
		if u := str(d["popup_url"]); u != "" {
			parts = append(parts, discover.Template(u))
		}
		return strings.Join(parts, "|")
	case "E_REQUEST_FAILED", "E_FAILED_REQUEST_ON_ACTION":
		if rs, ok := d["requests"].([]string); ok && len(rs) > 0 {
			return discover.Template(rs[0])
		}
	case "E_UNCAUGHT", "E_CONSOLE_ERROR", "E_CLIENT_ERROR_ON_ACTION":
		return normalize(str(d["error"]), 140)
	case "E_VISUAL_DIFF", "E_VISUAL_BASELINE_WRITTEN":
		return str(d["key"])
	case "E_UUID_EXPOSED", "E_UUID_IN_OPTION_VALUE":
		return str(d["exposed_in"])
	case "E_ALERT":
		return normalize(fmt.Sprint(d["dialogs"]), 100)
	}
	return ""
}

// Fingerprint identifies a finding across runs. The page is not part of it:
// the store already merges one defect seen on many pages, and the pages are
// compared separately so a spread to a new page is visible.
func Fingerprint(f model.Finding) string {
	sum := sha1.Sum([]byte(strings.Join([]string{
		f.Code, normalize(f.Element, 120), discriminator(f),
	}, "\x1f")))
	return hex.EncodeToString(sum[:])[:16]
}

func templatesOf(f model.Finding) []string {
	set := map[string]bool{discover.Template(f.PageURL): true}
	for _, u := range f.AlsoOn {
		set[discover.Template(u)] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func glob(pattern, s string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	ok, _ := regexp.MatchString(re, s)
	return ok
}

func (r Rule) matches(f model.Finding) bool {
	if r.Code != f.Code {
		return false
	}
	if r.Element != "" && !glob(r.Element, f.Element) {
		return false
	}
	if r.Page != "" {
		hit := false
		for _, u := range append([]string{f.PageURL}, f.AlsoOn...) {
			if p := pathOf(u); discover.MatchPath(r.Page, p) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if r.re != nil {
		blob := strings.Join(f.Evidence, "\n") + "\n" + fmt.Sprint(f.Detail)
		if !r.re.MatchString(blob) {
			return false
		}
	}
	return true
}

func pathOf(raw string) string {
	t := discover.Template(raw)
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
		if j := strings.Index(t, "/"); j >= 0 {
			return t[j:]
		}
		return "/"
	}
	return t
}

// Options carries everything Apply needs beyond the findings.
type Options struct {
	Cfg     config.Config
	Pages   []model.Page
	Now     time.Time
	Partial bool // the crawl stopped early; absence proves nothing
}

// Apply runs policy, suppression and the baseline over the findings.
func Apply(in []model.Finding, opt Options) (Result, error) {
	cfg := opt.Cfg
	rules, err := LoadRules(cfg.Baseline.SuppressFile, opt.Now)
	if err != nil {
		return Result{}, err
	}
	findings := make([]model.Finding, 0, len(in)+2)
	for _, f := range in {
		if level, ok := cfg.Policy.Severity[f.Code]; ok {
			if level == "off" {
				continue
			}
			f.Severity = model.Severity(level)
		}
		findings = append(findings, f)
	}
	for _, r := range rules {
		if r.expired(opt.Now) {
			f := model.NewFinding("E_SUPPRESSION_EXPIRED", cfg.Run.BaseURL)
			f.Element = r.Code
			f.Detail["rule"] = r.Code
			f.Detail["owner"] = r.Owner
			f.Detail["reason"] = r.Reason
			f.Detail["expired"] = r.Expires
			f.Evidence = []string{fmt.Sprintf("suppression for %s owned by %s expired on %s: %s", r.Code, r.Owner, r.Expires, r.Reason)}
			findings = append(findings, f)
		}
	}

	res := Result{Counts: map[model.Severity]int{}, Totals: map[model.Severity]int{}}
	for i := range findings {
		f := &findings[i]
		f.Fingerprint = Fingerprint(*f)
		for _, r := range rules {
			if !r.expired(opt.Now) && r.matches(*f) {
				f.Status = "suppressed"
				f.Suppression = fmt.Sprintf("%s (%s, until %s)", r.Reason, r.Owner, r.Expires)
				break
			}
		}
	}

	var base *File
	if !cfg.Baseline.Disabled {
		base, err = Load(cfg.Baseline.File)
		if err != nil {
			return Result{}, err
		}
	}
	if base != nil && !cfg.Baseline.Accept {
		res.Baseline = classify(findings, base, cfg.Baseline.File, opt)
	}
	if cfg.Baseline.Accept {
		entries := entriesOf(findings)
		if err := Save(cfg.Baseline.File, File{
			Version: 1, AcceptedAt: opt.Now, BaseURL: cfg.Run.BaseURL, Entries: entries,
		}); err != nil {
			return Result{}, err
		}
		res.Baseline = &model.BaselineSummary{File: cfg.Baseline.File, Accepted: true, Known: len(entries)}
		for i := range findings {
			if findings[i].Status == "" && findings[i].Confidence != "unconfirmed" {
				findings[i].Status = "known"
			}
		}
	}

	for _, f := range findings {
		res.Totals[f.Severity]++
		switch {
		case f.Status == "suppressed":
			res.Suppressed++
		case f.Confidence == "unconfirmed":
			res.Unconfirmed++
		}
		if f.Gates() {
			res.Counts[f.Severity]++
		}
	}
	model.SortFindings(findings)
	res.Findings = findings
	return res, nil
}

func entriesOf(findings []model.Finding) []model.BaselineEntry {
	index := map[string]int{}
	var out []model.BaselineEntry
	for _, f := range findings {
		if f.Confidence == "unconfirmed" || f.Status == "suppressed" {
			continue
		}
		// Findings with different merge keys can share a fingerprint (a popup's
		// overflow and a page's overflow), so the entry owns every page of all.
		if i, ok := index[f.Fingerprint]; ok {
			out[i].Templates = unionSorted(out[i].Templates, templatesOf(f))
			continue
		}
		index[f.Fingerprint] = len(out)
		out = append(out, model.BaselineEntry{
			Fingerprint: f.Fingerprint, Code: f.Code, Severity: f.Severity,
			Element: f.Element, Title: f.Title, Templates: templatesOf(f),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

func unionSorted(a, b []string) []string {
	set := map[string]bool{}
	for _, t := range a {
		set[t] = true
	}
	for _, t := range b {
		set[t] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// classify marks each finding new or known and lists baseline entries that are
// gone. An entry is only "fixed" if the run visited every page it lived on;
// otherwise the crawl simply did not look.
func classify(findings []model.Finding, base *File, path string, opt Options) *model.BaselineSummary {
	known := map[string]model.BaselineEntry{}
	for _, e := range base.Entries {
		known[e.Fingerprint] = e
	}
	visited := map[string]bool{}
	for _, p := range opt.Pages {
		visited[p.Template] = true
	}
	summary := &model.BaselineSummary{File: path}
	present := map[string]bool{}
	for i := range findings {
		f := &findings[i]
		if f.Status == "suppressed" || f.Confidence == "unconfirmed" {
			continue
		}
		entry, ok := known[f.Fingerprint]
		if !ok {
			f.Status = "new"
			summary.New++
			continue
		}
		present[f.Fingerprint] = true
		old := map[string]bool{}
		for _, t := range entry.Templates {
			old[t] = true
		}
		spread := 0
		for _, t := range templatesOf(*f) {
			if !old[t] {
				spread++
			}
		}
		if spread > 0 {
			f.Status = "new"
			f.NewPages = spread
			summary.New++
			continue
		}
		f.Status = "known"
		summary.Known++
	}
	for _, e := range base.Entries {
		if present[e.Fingerprint] {
			continue
		}
		reached := !opt.Partial
		for _, t := range e.Templates {
			if !visited[t] {
				reached = false
			}
		}
		if reached {
			summary.Fixed = append(summary.Fixed, e)
		} else {
			summary.NotReached = append(summary.NotReached, e)
		}
	}
	return summary
}

// Load reads a baseline file; a missing file is no baseline.
func Load(path string) (*File, error) {
	if path == "" {
		return nil, nil
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f File
	if err := json.Unmarshal(blob, &f); err != nil {
		return nil, fmt.Errorf("baseline %s is not valid JSON: %w", path, err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("baseline %s has unsupported version %d", path, f.Version)
	}
	return &f, nil
}

// Save writes the baseline atomically.
func Save(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(blob, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
