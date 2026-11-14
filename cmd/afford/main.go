// afford crawls a running web app, exercises every affordance, and reports what
// breaks. Exit codes: 0 clean, 1 findings at or above --fail-on, 2 usage or
// configuration, 3 the run could not complete.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/auth"
	"afford/internal/browser"
	"afford/internal/config"
	"afford/internal/model"
	"afford/internal/report"
	"afford/internal/run"
	"afford/internal/store"
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
	case "init":
		return cmdInit(args[1:])
	case "install":
		return cmdInstall()
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
  afford init [--base-url URL] [--path afford.toml]
  afford install          # download the Playwright browser once
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
  --allow-destructive    click controls whose names look destructive
  --no-submit            never submit a form
  --no-prompt            fail instead of asking for credentials
  --headful              show the browser
  --embed-images         inline screenshots in the HTML report
  --timeout SECONDS      hard wall for the whole run (0 disables)
  --fail-on LEVEL        blocker | critical | major | minor | info | never
  --quiet                no console summary; artifacts only

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
		allowDestructive, noSubmit, headful, embedImages, noPrompt bool
	)

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
	fs.BoolVar(&allowDestructive, "allow-destructive", false, "click destructive controls")
	fs.BoolVar(&noSubmit, "no-submit", false, "never submit a form")
	fs.BoolVar(&headful, "headful", false, "show the browser")
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
	if noSubmit {
		cfg.Safety.SubmitForms = false
	}
	if headful {
		cfg.Run.Headful = true
	}
	if embedImages {
		cfg.Run.EmbedImages = true
	}
	if timeoutSecs >= 0 {
		cfg.Run.TimeoutSecs = timeoutSecs
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

	if summary.PagesVisited == 0 {
		fmt.Fprintln(os.Stderr, "afford: no page could be captured; treating the run as failed")
		return 3
	}

	if !quiet {
		fmt.Println()
		for _, level := range levels {
			fmt.Printf("%-9s %d\n", level, summary.Counts[level])
		}
		fmt.Printf("\npages %d · controls %d/%d · forms %d/%d · payloads %d\n",
			summary.PagesVisited, summary.ControlsProbed, summary.ControlsTotal,
			summary.FormsProbed, summary.FormsTotal, summary.PayloadsSent)
		if len(summary.NotTested) > 0 {
			fmt.Printf("not tested: %s\n", strings.Join(summary.NotTested, "; "))
		}
		fmt.Printf("report %s\n", reportPath)
	}

	if tripped := trippedLevels(summary.Counts, failOn); len(tripped) > 0 {
		if !quiet {
			fmt.Printf("failing on %v\n", tripped)
		}
		return 1
	}
	return 0
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
