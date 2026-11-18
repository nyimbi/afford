// afford crawls a running web app, exercises every affordance, and reports what
// breaks. Exit codes: 0 clean, 1 findings at or above --fail-on, 2 usage or
// configuration, 3 the run could not complete.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nyimbi/afford/internal/auth"
	"github.com/nyimbi/afford/internal/browser"
	"github.com/nyimbi/afford/internal/config"
	"github.com/nyimbi/afford/internal/fixtures"
	"github.com/nyimbi/afford/internal/gate"
	"github.com/nyimbi/afford/internal/history"
	"github.com/nyimbi/afford/internal/mcpserver"
	"github.com/nyimbi/afford/internal/model"
	"github.com/nyimbi/afford/internal/report"
	"github.com/nyimbi/afford/internal/run"
	"github.com/nyimbi/afford/internal/store"
)

const version = "0.1.0"

var levels = []model.Severity{model.Blocker, model.Critical, model.Major, model.Minor, model.Info}

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

func dispatch(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "install":
		return cmdInstall()
	case "history":
		return cmdHistory(os.Args[2:])
	case "checks":
		return cmdChecks(args[1:])
	case "version", "--version", "-v":
		fmt.Println("afford " + version)
		return 0
	case "help", "--help", "-h", "usage":
		usage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "afford: unknown command %q\n\n", args[0])
	usage()
	return 2
}

func usage() {
	fmt.Fprint(os.Stderr, `afford — crawl a web app, exercise every affordance, report what breaks.

  afford run --base-url http://localhost:3000 [options]
  afford mcp --config afford.toml     # MCP server on stdio, for agents
  afford init [--base-url URL] [--path afford.toml]
  afford install          # download the Playwright browser once
  afford checks [--json]  # every check afford performs, worst first
  afford history [--out DIR]  recent runs and how each moved
  afford version

Run options:
  --config PATH          TOML configuration (defaults to none)
  --out DIR              artifacts root (default .afford)
  --depth MODE           smoke | standard | deep
  --max-pages N          page budget (default 200)
  --max-depth N          link depth (default 4)
  --concurrency N        parallel browser contexts (default 3)
  --auth MODE            none | form | basic | bearer | storage
  --login-url PATH       login page path (default /login)
  --storage-state PATH   reuse a saved session
  --username NAME        username (or AFFORD_USERNAME)
  --allow-delete         click controls that delete or erase data
  --allow-side-effects   click pay/send/publish/logout/approve controls
  --allow-destructive    both of the above (umbrella)
  --no-submit            never submit a form
  --no-prompt            fail instead of asking for credentials
  --headful              show the browser
  --embed-images         inline screenshots in the HTML report
  --visual               compare screenshots against the baseline store
  --update-baseline      write current screenshots as the new baseline
  --accept               write the current findings as the findings baseline (exit 0)
  --baseline PATH        findings baseline file (default .afford/findings-baseline.json)
  --no-baseline          ignore the findings baseline and suppression file
  --suppress PATH        suppression file (default afford.suppress.toml)
  --timeout SECONDS      hard wall for the whole run (0 disables)
  --fail-on LEVEL        blocker | critical | major | minor | info | never
  --quiet                no console summary; artifacts only

MCP options:
  --config PATH          same configuration as run
  --base-url URL         the origin the server is allowed to open
  --allow-delete         allow the agent to delete or erase data
  --allow-side-effects   allow pay/send/publish/logout controls
  --allow-destructive    both of the above (umbrella)
  --allow-any-host       drop the SSRF host allowlist (dangerous)
  Credentials are read from AFFORD_USERNAME / AFFORD_PASSWORD; the server
  never prompts.

Exit codes: 0 clean, 1 findings at or above --fail-on, 2 usage/config, 3 run failed.
`)
}

func cmdInstall() int {
	if err := playwright.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "afford: install browsers: %v\n", err)
		return 3
	}
	fmt.Println("browsers installed")
	return 0
}

func cmdMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var baseURL, configPath, out, authMode, loginURL, storageState string
	var allowDestructive, allowDelete, allowSideEffects, allowAnyHost bool

	fs.StringVar(&baseURL, "base-url", "", "origin the server may open")
	fs.StringVar(&baseURL, "u", "", "alias for --base-url")
	fs.StringVar(&configPath, "config", "", "TOML configuration file")
	fs.StringVar(&configPath, "c", "", "alias for --config")
	fs.StringVar(&out, "out", "", "artifact directory")
	fs.StringVar(&out, "o", "", "alias for --out")
	fs.StringVar(&authMode, "auth", "", "none | form | basic | bearer | storage")
	fs.StringVar(&loginURL, "login-url", "", "login page path")
	fs.StringVar(&storageState, "storage-state", "", "reuse a saved session")
	fs.BoolVar(&allowDestructive, "allow-destructive", false, "let the agent click destructive controls (both grants below)")
	fs.BoolVar(&allowDelete, "allow-delete", false, "let the agent click controls that delete or erase data")
	fs.BoolVar(&allowSideEffects, "allow-side-effects", false, "let the agent click pay/send/publish/logout controls")
	fs.BoolVar(&allowAnyHost, "allow-any-host", false, "drop the SSRF host allowlist")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}
	if baseURL != "" {
		cfg.Run.BaseURL = baseURL
	}
	if out != "" {
		cfg.Run.Out = out
	}
	if authMode != "" {
		cfg.Auth.Mode = authMode
	}
	if loginURL != "" {
		cfg.Auth.LoginURL = loginURL
	}
	if storageState != "" {
		cfg.Auth.StorageState = storageState
	}
	if allowDestructive {
		cfg.Safety.AllowDestructive = true
	}
	if allowDelete {
		cfg.Safety.AllowDelete = true
	}
	if allowSideEffects {
		cfg.Safety.AllowSideEffects = true
	}
	if allowAnyHost {
		cfg.MCP.AllowAnyHost = true
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}
	server := mcpserver.New(cfg, version)
	if err := server.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "afford: mcp server: %v\n", err)
		return 3
	}
	return 0
}

func cmdChecks(args []string) int {
	fs := flag.NewFlagSet("checks", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "print the catalogue as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	specs := model.Specs()
	if asJSON {
		blob, err := json.MarshalIndent(specs, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "afford: %v\n", err)
			return 3
		}
		fmt.Println(string(blob))
		return 0
	}
	for _, spec := range specs {
		fmt.Printf("%-8s %-30s %s\n", spec.Severity, spec.Code, spec.Title)
	}
	fmt.Printf("\n%d checks\n", len(specs))
	return 0
}

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var path, baseURL string
	var force bool
	fs.StringVar(&path, "path", "afford.toml", "where to write the config")
	fs.StringVar(&path, "p", "afford.toml", "alias for --path")
	fs.StringVar(&baseURL, "base-url", "http://localhost:3000", "target application")
	fs.StringVar(&baseURL, "u", "http://localhost:3000", "alias for --base-url")
	fs.BoolVar(&force, "force", false, "overwrite an existing file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := os.Stat(path); err == nil && !force {
		fmt.Fprintf(os.Stderr, "afford: %s exists; pass --force to overwrite\n", path)
		return 1
	}
	body := strings.Replace(config.Example, "http://localhost:3000", baseURL, 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}
	fmt.Println("wrote " + path)
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		baseURL, configPath, out, depth, authMode, loginURL, storageState, username string
		failOn                                                                      string
		quiet                                                                       bool
	)
	var (
		maxPages, maxDepth, concurrency, timeoutSecs int
	)
	var (
		allowDestructive, allowDelete, allowSideEffects, noSubmit, headful, embedImages, noPrompt bool
		visualEnabled, updateBaseline, accept, noBaseline                                         bool
	)
	var baselinePath, suppressPath string

	fs.StringVar(&baseURL, "base-url", "", "target application")
	fs.StringVar(&baseURL, "u", "", "alias for --base-url")
	fs.StringVar(&configPath, "config", "", "TOML configuration file")
	fs.StringVar(&configPath, "c", "", "alias for --config")
	fs.StringVar(&out, "out", "", "artifact directory")
	fs.StringVar(&out, "o", "", "alias for --out")
	fs.StringVar(&depth, "depth", "", "smoke | standard | deep")
	fs.IntVar(&maxPages, "max-pages", -1, "page budget")
	fs.IntVar(&maxDepth, "max-depth", -1, "link depth")
	fs.IntVar(&concurrency, "concurrency", -1, "parallel contexts")
	fs.StringVar(&authMode, "auth", "", "none | form | basic | bearer | storage")
	fs.StringVar(&loginURL, "login-url", "", "login page path")
	fs.StringVar(&storageState, "storage-state", "", "reuse a saved session")
	fs.StringVar(&username, "username", "", "username (or AFFORD_USERNAME)")
	fs.BoolVar(&allowDestructive, "allow-destructive", false, "click destructive controls (both grants below)")
	fs.BoolVar(&allowDelete, "allow-delete", false, "click controls that delete or erase data")
	fs.BoolVar(&allowSideEffects, "allow-side-effects", false, "click pay/send/publish/logout/approve controls")
	fs.BoolVar(&noSubmit, "no-submit", false, "never submit a form")
	fs.BoolVar(&headful, "headful", false, "show the browser")
	fs.BoolVar(&visualEnabled, "visual", false, "compare screenshots against the baseline store")
	fs.BoolVar(&updateBaseline, "update-baseline", false, "write current screenshots as the new baseline")
	fs.BoolVar(&accept, "accept", false, "write the current findings as the findings baseline")
	fs.BoolVar(&noBaseline, "no-baseline", false, "ignore the findings baseline and suppression file")
	fs.StringVar(&baselinePath, "baseline", "", "findings baseline file")
	fs.StringVar(&suppressPath, "suppress", "", "suppression file")
	fs.BoolVar(&embedImages, "embed-images", false, "inline screenshots")
	fs.BoolVar(&noPrompt, "no-prompt", false, "never prompt for credentials")
	fs.IntVar(&timeoutSecs, "timeout", -1, "hard wall in seconds")
	fs.StringVar(&failOn, "fail-on", "critical", "blocker|critical|major|minor|info|never")
	fs.BoolVar(&quiet, "quiet", false, "no console summary")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}
	if baseURL != "" {
		cfg.Run.BaseURL = baseURL
	}
	if out != "" {
		cfg.Run.Out = out
	}
	if depth != "" {
		cfg.Run.Depth = depth
	}
	if maxPages >= 0 {
		cfg.Limits.MaxPages = maxPages
	}
	if maxDepth >= 0 {
		cfg.Limits.MaxDepth = maxDepth
	}
	if concurrency > 0 {
		cfg.Limits.Concurrency = concurrency
	}
	if authMode != "" {
		cfg.Auth.Mode = authMode
	}
	if loginURL != "" {
		cfg.Auth.LoginURL = loginURL
	}
	if storageState != "" {
		cfg.Auth.StorageState = storageState
	}
	if allowDestructive {
		cfg.Safety.AllowDestructive = true
	}
	if allowDelete {
		cfg.Safety.AllowDelete = true
	}
	if allowSideEffects {
		cfg.Safety.AllowSideEffects = true
	}
	if noSubmit {
		cfg.Safety.SubmitForms = false
	}
	if headful {
		cfg.Run.Headful = true
	}
	if embedImages {
		cfg.Run.EmbedImages = true
	}
	if visualEnabled || updateBaseline {
		cfg.Visual.Enabled = true
	}
	if updateBaseline {
		cfg.Visual.UpdateBaseline = true
	}
	if timeoutSecs >= 0 {
		cfg.Run.TimeoutSecs = timeoutSecs
	}
	if baselinePath != "" {
		cfg.Baseline.File = baselinePath
	}
	if suppressPath != "" {
		cfg.Baseline.SuppressFile = suppressPath
	}
	if noBaseline {
		cfg.Baseline.Disabled = true
		cfg.Baseline.SuppressFile = ""
	}
	if accept {
		if cfg.Baseline.Disabled {
			fmt.Fprintln(os.Stderr, "afford: --accept and --no-baseline contradict each other")
			return 2
		}
		cfg.Baseline.Accept = true
	}
	if username != "" {
		_ = os.Setenv("AFFORD_USERNAME", username)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}
	if !validFailOn(failOn) {
		fmt.Fprintf(os.Stderr, "afford: --fail-on must be one of blocker, critical, major, minor, info, never\n")
		return 2
	}
	if err := cfg.CheckMutationGuard(); err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}

	// A malformed suppression file is a configuration error, found before a
	// browser starts rather than after the crawl.
	if _, err := gate.LoadRules(cfg.Baseline.SuppressFile, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}
	if _, err := gate.Load(cfg.Baseline.File); err != nil && !cfg.Baseline.Disabled {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 2
	}

	runID := time.Now().UTC().Format("20060102T150405Z") + "-" + randomHex(3)
	baseDir := filepath.Join(cfg.Run.Out, "runs", runID)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 3
	}
	st, err := store.Open(filepath.Join(baseDir, "run.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 3
	}
	defer st.Close()

	ctx := context.Background()
	cancel := func() {}
	if cfg.Run.TimeoutSecs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Run.TimeoutSecs)*time.Second)
	}
	defer cancel()

	if !quiet {
		fmt.Printf("afford → %s\n", cfg.Run.BaseURL)
		fmt.Printf("        pages≤%d · depth≤%d · %s payloads · concurrency %d\n",
			cfg.Limits.MaxPages, cfg.Limits.MaxDepth, cfg.Run.Depth, cfg.Limits.Concurrency)
	}

	host, err := browser.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 3
	}
	defer host.Close()

	hooks := fixtures.New(cfg.Fixtures, runID, cfg.Run.BaseURL)
	if err := hooks.Setup(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "afford: fixtures setup: %v\n", err)
		_ = hooks.Teardown()
		return 3
	}
	defer func() {
		if err := hooks.Teardown(); err != nil {
			fmt.Fprintf(os.Stderr, "afford: fixtures teardown: %v\n", err)
		}
	}()

	authResult, err := auth.Authenticate(host, cfg, !noPrompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: authentication: %v\n", err)
		return 3
	}

	runner := run.New(cfg, st, runID, baseDir, ctx)
	summary, err := runner.Run(host, authResult)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: run: %v\n", err)
		return 3
	}

	reportPath, err := report.Write(st, runID, baseDir, summary, cfg.Run.EmbedImages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: write report: %v\n", err)
		return 3
	}

	histPath := filepath.Join(cfg.Run.Out, "history.jsonl")
	pagesNow, _ := st.Snapshot()
	entry := history.Build(summary, pagesNow)
	prior, _ := history.Load(histPath)
	if err := history.Append(histPath, entry); err != nil {
		fmt.Fprintf(os.Stderr, "afford: history: %v\n", err)
	}

	if summary.PagesVisited == 0 {
		fmt.Fprintln(os.Stderr, "afford: no page could be captured; treating the run as failed")
		return 3
	}

	if !quiet {
		fmt.Println()
		for _, level := range levels {
			fmt.Printf("%-9s %d\n", level, summary.Counts[level])
		}
		if line := baselineLine(summary); line != "" {
			fmt.Println(line)
		}
		fmt.Printf("\npages %d · controls %d/%d · forms %d/%d · payloads %d\n",
			summary.PagesVisited, summary.ControlsProbed, summary.ControlsTotal,
			summary.FormsProbed, summary.FormsTotal, summary.PayloadsSent)
		if len(summary.NotTested) > 0 {
			fmt.Printf("not tested: %s\n", strings.Join(summary.NotTested, "; "))
		}
		fmt.Println("history: " + history.Delta(entry, history.Previous(prior, entry.BaseURL, entry.RunID)))
		fmt.Printf("report %s\n", reportPath)
	}

	if cfg.Baseline.Accept {
		fmt.Printf("baseline written to %s\n", cfg.Baseline.File)
		return 0
	}
	if tripped := trippedLevels(summary.Counts, failOn); len(tripped) > 0 {
		if !quiet {
			fmt.Printf("failing on %v\n", tripped)
		}
		return 1
	}
	return 0
}

func baselineLine(s model.Summary) string {
	var parts []string
	if b := s.Baseline; b != nil && !b.Accepted {
		parts = append(parts, fmt.Sprintf("baseline: %d new · %d known · %d fixed", b.New, b.Known, len(b.Fixed)))
	}
	if s.Suppressed > 0 {
		parts = append(parts, fmt.Sprintf("%d suppressed", s.Suppressed))
	}
	if s.Unconfirmed > 0 {
		parts = append(parts, fmt.Sprintf("%d unconfirmed (reported, not gating)", s.Unconfirmed))
	}
	return strings.Join(parts, " · ")
}

func validFailOn(level string) bool {
	if level == "never" {
		return true
	}
	for _, l := range levels {
		if string(l) == level {
			return true
		}
	}
	return false
}

// trippedLevels returns the severities at or above the threshold that fired.
func trippedLevels(counts map[model.Severity]int, threshold string) []string {
	if threshold == "never" {
		return nil
	}
	var out []string
	for _, level := range levels {
		if counts[level] > 0 {
			out = append(out, string(level))
		}
		if string(level) == threshold {
			break
		}
	}
	return out
}

func randomHex(n int) string {
	blob := make([]byte, n)
	if _, err := rand.Read(blob); err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1_000_000)
	}
	return hex.EncodeToString(blob)
}

func cmdHistory(args []string) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", ".afford", "output directory the runs were written to")
	n := fs.Int("n", 10, "how many recent runs to show")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	entries, err := history.Load(filepath.Join(*out, "history.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "afford: %v\n", err)
		return 3
	}
	if len(entries) == 0 {
		fmt.Println("no runs recorded in " + *out)
		return 0
	}
	if len(entries) > *n {
		entries = entries[len(entries)-*n:]
	}
	for i, e := range entries {
		var prev *history.Entry
		if i > 0 {
			prev = &entries[i-1]
		}
		fmt.Printf("%s  %s  pages %d  blocker %d critical %d major %d  load p95 %dms\n    %s\n",
			e.At.Format("2006-01-02 15:04"), e.RunID, e.Pages,
			e.Counts["blocker"], e.Counts["critical"], e.Counts["major"], e.LoadP95MS,
			history.Delta(e, prev))
	}
	return 0
}
