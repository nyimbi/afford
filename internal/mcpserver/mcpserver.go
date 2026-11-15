// Package mcpserver exposes afford to an agent over MCP.
//
// The safety posture is the server's, not the caller's: destructive controls stay
// quarantined, forms are not submitted unless asked, and the URL host allowlist
// stops a prompt-injected agent from pointing the browser at arbitrary hosts.
package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"afford/internal/auth"
	"afford/internal/browser"
	"afford/internal/check"
	"afford/internal/config"
	"afford/internal/model"
	"afford/internal/report"
	"afford/internal/run"
	"afford/internal/store"
)

type Server struct {
	cfg     config.Config
	version string

	once    sync.Once
	initErr error
	host    *browser.Host
	session auth.Result
	checker *check.Checker

	mu sync.Mutex
}

func New(cfg config.Config, version string) *Server {
	return &Server{cfg: cfg, version: version}
}

// ensure starts the browser and establishes the session on first use. Failing
// here leaves the server running so the agent gets an actionable error instead
// of a dead MCP endpoint.
func (s *Server) ensure() error {
	s.once.Do(func() {
		host, err := browser.New(s.cfg)
		if err != nil {
			s.initErr = err
			return
		}
		session, err := auth.Authenticate(host, s.cfg, false)
		if err != nil {
			host.Close()
			s.initErr = fmt.Errorf(
				"afford is not authenticated: %w (set the credentials in the MCP server environment)", err)
			return
		}
		s.host = host
		s.session = session
		s.checker = check.New(s.cfg, host, session)
	})
	return s.initErr
}

func (s *Server) close() {
	if s.host != nil {
		s.host.Close()
	}
}

func (s *Server) callContext(parent context.Context) (context.Context, context.CancelFunc) {
	if s.cfg.MCP.CallTimeoutSecs <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(s.cfg.MCP.CallTimeoutSecs)*time.Second)
}

// ---- tools ---------------------------------------------------------------

type checkPageIn struct {
	URL          string `json:"url" jsonschema:"absolute http(s) URL of the page to check; must be the configured origin or an allowed host"`
	ReadOnly     bool   `json:"read_only,omitempty" jsonschema:"capture only: no clicks, no fills, no submits"`
	Submit       bool   `json:"submit,omitempty" jsonschema:"submit each form with valid data; mutates server state, default false"`
	Depth        string `json:"depth,omitempty" jsonschema:"payload battery: smoke, standard or deep; default standard"`
	NoScreenshot bool   `json:"no_screenshot,omitempty" jsonschema:"skip the full-page PNG"`
	MaxControls  int    `json:"max_controls,omitempty" jsonschema:"cap on controls activated; default 40"`
}

type pageOut struct {
	URL            string          `json:"url"`
	FinalURL       string          `json:"final_url"`
	Status         int             `json:"status"`
	Title          string          `json:"title"`
	LoadMS         int64           `json:"load_ms"`
	SettledMS      int64           `json:"settled_ms"`
	OK             bool            `json:"ok"`
	Counts         map[string]int  `json:"counts"`
	Findings       []model.Finding `json:"findings"`
	Controls       int             `json:"controls"`
	ControlsProbed int             `json:"controls_probed"`
	Forms          int             `json:"forms"`
	FormsProbed    int             `json:"forms_probed"`
	InputsSwept    int             `json:"inputs_swept"`
	PayloadsSent   int             `json:"payloads_sent"`
	Screenshot     string          `json:"screenshot,omitempty"`
	Notes          []string        `json:"notes,omitempty"`
	DurationMS     int64           `json:"duration_ms"`
}

type inspectPageIn struct {
	URL          string `json:"url" jsonschema:"absolute http(s) URL of the page to inspect; must be the configured origin or an allowed host"`
	NoScreenshot bool   `json:"no_screenshot,omitempty" jsonschema:"skip the full-page PNG"`
}

type crawlIn struct {
	MaxPages int    `json:"max_pages,omitempty" jsonschema:"page budget; default 25"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"link depth; default 2"`
	Depth    string `json:"depth,omitempty" jsonschema:"payload battery: smoke, standard or deep; default the server configuration"`
	Submit   bool   `json:"submit,omitempty" jsonschema:"submit forms during the crawl; mutates server state, default false"`
}

type crawlOut struct {
	RunID      string          `json:"run_id"`
	Report     string          `json:"report"`
	Summary    model.Summary   `json:"summary"`
	Findings   []model.Finding `json:"findings"`
	Pages      []string        `json:"pages"`
	LinkCounts map[string]int  `json:"link_counts"`
	OK         bool            `json:"ok"`
}

func (s *Server) checkPage(ctx context.Context, _ *mcp.CallToolRequest, in checkPageIn) (*mcp.CallToolResult, pageOut, error) {
	if err := s.ensure(); err != nil {
		return nil, pageOut{}, err
	}
	callCtx, cancel := s.callContext(ctx)
	defer cancel()
	res, err := s.checker.Check(callCtx, in.URL, check.Options{
		ReadOnly:    in.ReadOnly || s.cfg.MCP.DefaultReadOnly,
		Submit:      in.Submit,
		Depth:       in.Depth,
		MaxControls: in.MaxControls,
		Screenshot:  !in.NoScreenshot,
	})
	if err != nil {
		return nil, pageOut{}, err
	}
	out := toPageOut(res)
	return textResult(digestPage(res)), out, nil
}

func (s *Server) inspectPage(ctx context.Context, _ *mcp.CallToolRequest, in inspectPageIn) (*mcp.CallToolResult, pageOut, error) {
	if err := s.ensure(); err != nil {
		return nil, pageOut{}, err
	}
	callCtx, cancel := s.callContext(ctx)
	defer cancel()
	res, err := s.checker.Check(callCtx, in.URL, check.Options{
		ReadOnly:   true,
		Screenshot: !in.NoScreenshot,
	})
	if err != nil {
		return nil, pageOut{}, err
	}
	out := toPageOut(res)
	return textResult(digestPage(res)), out, nil
}

func (s *Server) crawlSite(ctx context.Context, _ *mcp.CallToolRequest, in crawlIn) (*mcp.CallToolResult, crawlOut, error) {
	if err := s.ensure(); err != nil {
		return nil, crawlOut{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.cfg
	if in.MaxPages > 0 {
		cfg.Limits.MaxPages = in.MaxPages
	} else {
		cfg.Limits.MaxPages = 25
	}
	if in.MaxDepth > 0 {
		cfg.Limits.MaxDepth = in.MaxDepth
	} else {
		cfg.Limits.MaxDepth = 2
	}
	if in.Depth != "" {
		switch in.Depth {
		case "smoke", "standard", "deep":
			cfg.Run.Depth = in.Depth
		default:
			return nil, crawlOut{}, fmt.Errorf("depth must be smoke, standard or deep, got %q", in.Depth)
		}
	}
	cfg.Safety.SubmitForms = in.Submit

	callCtx, cancel := s.callContext(ctx)
	defer cancel()
	runID := time.Now().UTC().Format("20060102T150405Z") + "-mcp"
	baseDir := filepath.Join(cfg.Run.Out, "mcp", "runs", runID)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, crawlOut{}, err
	}
	st, err := store.Open(filepath.Join(baseDir, "run.jsonl"))
	if err != nil {
		return nil, crawlOut{}, err
	}
	defer st.Close()

	runner := run.New(cfg, st, runID, baseDir, callCtx)
	summary, err := runner.Run(s.host, s.session)
	if err != nil {
		return nil, crawlOut{}, err
	}
	reportPath, err := report.Write(st, runID, baseDir, summary, false)
	if err != nil {
		return nil, crawlOut{}, err
	}
	if abs, absErr := filepath.Abs(reportPath); absErr == nil {
		reportPath = abs
	}
	pages, findings := st.Snapshot()
	urls := make([]string, 0, len(pages))
	for _, p := range pages {
		urls = append(urls, p.URL)
	}
	linkCounts := map[string]int{}
	for _, link := range st.LinkRecords() {
		linkCounts[link.Disposition]++
	}
	out := crawlOut{
		RunID: runID, Report: reportPath, Summary: summary, Findings: findings,
		Pages: urls, LinkCounts: linkCounts, OK: !hasSerious(summary.Counts),
	}
	return textResult(digestCrawl(out)), out, nil
}

// ---- output helpers ------------------------------------------------------

func toPageOut(r check.Result) pageOut {
	return pageOut{
		URL: r.URL, FinalURL: r.FinalURL, Status: r.Status, Title: r.Title,
		LoadMS: r.LoadMS, SettledMS: r.SettledMS,
		OK: r.OK(), Counts: r.Counts(), Findings: r.Findings,
		Controls: r.Controls, ControlsProbed: r.ControlsProbed,
		Forms: r.Forms, FormsProbed: r.FormsProbed,
		InputsSwept: r.InputsSwept, PayloadsSent: r.PayloadsSent,
		Screenshot: r.Screenshot, Notes: r.Notes, DurationMS: r.DurationMS,
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func hasSerious(counts map[model.Severity]int) bool {
	return counts[model.Blocker]+counts[model.Critical]+counts[model.Major] > 0
}

func digestPage(r check.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s → HTTP %d", r.URL, r.Status)
	if r.Title != "" {
		fmt.Fprintf(&b, " · %q", r.Title)
	}
	if r.FinalURL != r.URL {
		fmt.Fprintf(&b, "\nfinal URL %s", r.FinalURL)
	}
	counts := r.Counts()
	fmt.Fprintf(&b, "\n%d finding(s): %d blocker, %d critical, %d major, %d minor, %d info",
		len(r.Findings), counts["blocker"], counts["critical"], counts["major"], counts["minor"], counts["info"])
	fmt.Fprintf(&b, "\nload %d ms · settled %d ms · controls probed %d/%d · forms %d/%d · inputs swept %d · payloads %d",
		r.LoadMS, r.SettledMS,
		r.ControlsProbed, r.Controls, r.FormsProbed, r.Forms, r.InputsSwept, r.PayloadsSent)
	if len(r.Findings) == 0 {
		b.WriteString("\nno findings")
	}
	for i, f := range r.Findings {
		if i >= 20 {
			fmt.Fprintf(&b, "\n- …and %d more (see structuredContent)", len(r.Findings)-i)
			break
		}
		fmt.Fprintf(&b, "\n- %s %s — %s", strings.ToUpper(string(f.Severity)), f.Code, f.Title)
		if f.Element != "" {
			fmt.Fprintf(&b, " [%s]", f.Element)
		}
	}
	if r.Screenshot != "" {
		fmt.Fprintf(&b, "\nscreenshot %s", r.Screenshot)
	}
	for _, note := range r.Notes {
		fmt.Fprintf(&b, "\nnote: %s", note)
	}
	return b.String()
}

func digestCrawl(out crawlOut) string {
	s := out.Summary
	var b strings.Builder
	fmt.Fprintf(&b, "crawled %s: %d page(s), %d finding(s)",
		s.BaseURL, s.PagesVisited, len(out.Findings))
	fmt.Fprintf(&b, "\nblocker %d · critical %d · major %d · minor %d · info %d",
		s.Counts[model.Blocker], s.Counts[model.Critical], s.Counts[model.Major],
		s.Counts[model.Minor], s.Counts[model.Info])
	for i, f := range out.Findings {
		if i >= 20 {
			fmt.Fprintf(&b, "\n- …and %d more", len(out.Findings)-i)
			break
		}
		fmt.Fprintf(&b, "\n- %s %s — %s (%s)",
			strings.ToUpper(string(f.Severity)), f.Code, f.Title, f.PageURL)
	}
	if len(out.LinkCounts) > 0 {
		parts := make([]string, 0, len(out.LinkCounts))
		for disposition, n := range out.LinkCounts {
			parts = append(parts, fmt.Sprintf("%s %d", disposition, n))
		}
		sort.Strings(parts)
		fmt.Fprintf(&b, "\nlinks not crawled: %s", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "\nreport %s", out.Report)
	return b.String()
}

// ---- server --------------------------------------------------------------

func (s *Server) Run(ctx context.Context) error {
	defer s.close()
	server := mcp.NewServer(&mcp.Implementation{Name: "afford", Version: s.version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "check_page",
		Description: "Open one page in a real browser, capture console errors, uncaught exceptions, " +
			"failed requests, blank/error renders and horizontal overflow, then exercise its affordances: " +
			"click every enabled control from a fresh load, fill every text input with a battery derived " +
			"from the field's declared type, and (only with submit=true) submit each form. " +
			"Controls that delete data are never clicked unless the server was started with --allow-delete; " +
			"paying, sending, publishing, approving and logging out need --allow-side-effects. " +
			"Returns findings with evidence, a severity summary, load timings and a screenshot path.",
	}, s.checkPage)

	mcp.AddTool(server, &mcp.Tool{
		Name: "inspect_page",
		Description: "Capture one page without interacting: status, title, console errors, uncaught " +
			"exceptions, failed requests, blank and error renders, overflow, control inventory and a " +
			"screenshot. Never clicks, fills or submits. Use this first when you must not mutate anything.",
	}, s.inspectPage)

	mcp.AddTool(server, &mcp.Tool{
		Name: "crawl_site",
		Description: "Crawl the configured origin from its base URL and probe every reachable page, then " +
			"write an HTML/JSON/JUnit report. Slower than check_page; use it for whole-site sweeps. " +
			"Forms are not submitted unless submit=true; deletion and other irreversible controls remain " +
			"quarantined unless the server was started with the matching grant.",
	}, s.crawlSite)

	return server.Run(ctx, &mcp.StdioTransport{})
}
