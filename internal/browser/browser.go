// Package browser owns the Playwright process and per-probe event capture.
package browser

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/config"
	"afford/internal/discover"
	"afford/internal/model"
	"afford/internal/sanitize"
)

type Host struct {
	cfg     config.Config
	pw      *playwright.Playwright
	Browser playwright.Browser
}

func New(cfg config.Config) (*Host, error) {
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("start playwright driver: %w (run `afford install` once)", err)
	}
	headless := !cfg.Run.Headful
	browserType := pw.Chromium
	switch strings.ToLower(cfg.Run.Browser) {
	case "", "chromium":
	case "firefox":
		browserType = pw.Firefox
	case "webkit":
		browserType = pw.WebKit
	default:
		_ = pw.Stop()
		return nil, fmt.Errorf("unsupported browser %q (chromium, firefox or webkit)", cfg.Run.Browser)
	}
	browser, err := browserType.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
	})
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("launch %s: %w", cfg.Run.Browser, err)
	}
	return &Host{cfg: cfg, pw: pw, Browser: browser}, nil
}

func (h *Host) Close() {
	if h.Browser != nil {
		_ = h.Browser.Close()
	}
	if h.pw != nil {
		_ = h.pw.Stop()
	}
}

func (h *Host) NewContext(statePath string, creds *playwright.HttpCredentials, headers map[string]string) (playwright.BrowserContext, error) {
	opts := playwright.BrowserNewContextOptions{
		Viewport: &playwright.Size{
			Width:  h.cfg.Run.Viewport[0],
			Height: h.cfg.Run.Viewport[1],
		},
		IgnoreHttpsErrors: playwright.Bool(true),
		UserAgent: playwright.String(
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/126.0 Safari/537.36 Afford/0.1"),
	}
	if statePath != "" {
		opts.StorageStatePath = playwright.String(statePath)
	}
	if creds != nil {
		opts.HttpCredentials = creds
	}
	if len(headers) > 0 {
		opts.ExtraHttpHeaders = headers
	}
	return h.Browser.NewContext(opts)
}

// Snapshot is a lock-free copy of everything one probe heard.
type Snapshot struct {
	ConsoleErrors   []string
	ConsoleWarnings []string
	PageErrors      []string
	Requests        []model.RequestRecord
	Dialogs         []string
	RequestCount    int
}

// Collector hears one probe. One collector per probe, never reused: event
// attribution depends on it.
type Collector struct {
	mu sync.Mutex
	Snapshot
	ignoreConsole  []*regexp.Regexp
	ignoreRequests []*regexp.Regexp
}

func compileAll(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			out = append(out, re)
		}
	}
	return out
}

func matchesAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func (c *Collector) Bind(page playwright.Page) {
	page.On("console", func(msg playwright.ConsoleMessage) {
		text := msg.Text()
		if loc := msg.Location(); loc != nil && loc.URL != "" {
			text = fmt.Sprintf("%s  [%s]", text, loc.URL)
		}
		if matchesAny(c.ignoreConsole, text) {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch msg.Type() {
		case "error":
			c.ConsoleErrors = append(c.ConsoleErrors, text)
		case "warning":
			c.ConsoleWarnings = append(c.ConsoleWarnings, text)
		}
	})
	page.On("pageerror", func(err error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.PageErrors = append(c.PageErrors, err.Error())
	})
	page.On("request", func(req playwright.Request) {
		if isHTTP(req.URL()) {
			c.mu.Lock()
			c.RequestCount++
			c.mu.Unlock()
		}
	})
	page.On("response", func(resp playwright.Response) {
		req := resp.Request()
		if matchesAny(c.ignoreRequests, req.URL()) {
			return
		}
		c.mu.Lock()
		c.Requests = append(c.Requests, model.RequestRecord{
			URL:          sanitize.URL(req.URL()),
			Method:       req.Method(),
			ResourceType: req.ResourceType(),
			Kind:         kindOf(req.ResourceType()),
			Status:       resp.Status(),
		})
		c.mu.Unlock()
	})
	page.On("requestfailed", func(req playwright.Request) {
		if matchesAny(c.ignoreRequests, req.URL()) {
			return
		}
		failure := "failed"
		if err := req.Failure(); err != nil {
			failure = err.Error()
		}
		c.mu.Lock()
		c.Requests = append(c.Requests, model.RequestRecord{
			URL:          sanitize.URL(req.URL()),
			Method:       req.Method(),
			ResourceType: req.ResourceType(),
			Kind:         kindOf(req.ResourceType()),
			Failed:       failure,
		})
		c.mu.Unlock()
	})
	page.On("dialog", func(dialog playwright.Dialog) {
		c.mu.Lock()
		c.Dialogs = append(c.Dialogs, fmt.Sprintf("%s: %s", dialog.Type(), dialog.Message()))
		c.mu.Unlock()
		go func() { _ = dialog.Dismiss() }()
	})
}

func (c *Collector) SnapshotCopy() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{
		ConsoleErrors:   append([]string(nil), c.ConsoleErrors...),
		ConsoleWarnings: append([]string(nil), c.ConsoleWarnings...),
		PageErrors:      append([]string(nil), c.PageErrors...),
		Requests:        append([]model.RequestRecord(nil), c.Requests...),
		Dialogs:         append([]string(nil), c.Dialogs...),
		RequestCount:    c.RequestCount,
	}
}

// Reset clears the collector so the next probe cannot inherit this one's events.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ConsoleErrors = nil
	c.ConsoleWarnings = nil
	c.PageErrors = nil
	c.Requests = nil
	c.Dialogs = nil
	c.RequestCount = 0
}

func isHTTP(raw string) bool {
	return len(raw) >= 7 && (raw[:7] == "http://" || (len(raw) >= 8 && raw[:8] == "https://"))
}

func kindOf(resourceType string) string {
	switch resourceType {
	case "document":
		return "document"
	case "xhr", "fetch":
		return "api"
	}
	return "static"
}

func NewProbePage(ctx playwright.BrowserContext, cfg config.Config) (playwright.Page, *Collector, error) {
	page, err := ctx.NewPage()
	if err != nil {
		return nil, nil, err
	}
	page.SetDefaultTimeout(8000)
	collector := &Collector{
		ignoreConsole:  compileAll(cfg.Policy.IgnoreConsole),
		ignoreRequests: compileAll(cfg.Policy.IgnoreRequests),
	}
	collector.Bind(page)
	return page, collector, nil
}

// Settle waits for load, then quiet network, then a stable node count, then the
// app's own readiness condition if one is configured.
func Settle(page playwright.Page, cfg config.Config) {
	_ = SettleReady(page, cfg)
}

// readinessSelector picks the selector for the page's current path: a route
// override wins over the global one.
func readinessSelector(rawURL string, cfg config.Config) string {
	path := rawURL
	if i := strings.Index(path, "://"); i >= 0 {
		path = path[i+3:]
		if j := strings.Index(path, "/"); j >= 0 {
			path = path[j:]
		} else {
			path = "/"
		}
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	patterns := make([]string, 0, len(cfg.Readiness.Routes))
	for pattern := range cfg.Readiness.Routes {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	for _, pattern := range patterns {
		if discover.MatchPath(pattern, path) {
			return cfg.Readiness.Routes[pattern]
		}
	}
	return cfg.Readiness.Selector
}

// SettleReady is Settle that reports a readiness condition that never held. The
// caller decides whether that is a finding; probes ignore it.
func SettleReady(page playwright.Page, cfg config.Config) []string {
	settleHeuristic(page, cfg)
	var problems []string
	timeout := float64(cfg.Readiness.TimeoutMS)
	if selector := readinessSelector(page.URL(), cfg); selector != "" {
		err := page.Locator(selector).First().WaitFor(playwright.LocatorWaitForOptions{
			State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(timeout),
		})
		if err != nil {
			problems = append(problems, "readiness selector never became visible: "+selector)
		}
	}
	for _, loading := range cfg.Readiness.Loading {
		if err := page.Locator(loading).First().WaitFor(playwright.LocatorWaitForOptions{
			State: playwright.WaitForSelectorStateHidden, Timeout: playwright.Float(timeout),
		}); err != nil {
			problems = append(problems, "loading indicator never went away: "+loading)
		}
	}
	return problems
}

func settleHeuristic(page playwright.Page, cfg config.Config) {
	errLoad := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateLoad,
		Timeout: playwright.Float(float64(cfg.Limits.NavigationTimeoutMS)),
	})
	_ = errLoad
	_ = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(math.Min(2500, float64(cfg.Limits.SettleMS))),
	})
	previous := -1
	for i := 0; i < 10; i++ {
		nodes, err := page.Evaluate("() => document.querySelectorAll('*').length")
		if err != nil {
			return
		}
		current := toInt(nodes)
		if current == previous {
			return
		}
		previous = current
		page.WaitForTimeout(float64(cfg.Limits.QuietMS))
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func Screenshot(page playwright.Page, path string, masks []string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	options := playwright.PageScreenshotOptions{
		Path:       playwright.String(path),
		FullPage:   playwright.Bool(true),
		Animations: playwright.ScreenshotAnimationsDisabled,
	}
	for _, selector := range masks {
		if selector = strings.TrimSpace(selector); selector != "" {
			options.Mask = append(options.Mask, page.Locator(selector))
		}
	}
	_, err := page.Screenshot(options)
	if err != nil {
		return "", err
	}
	return path, nil
}

// ScrollToLoad scrolls to the bottom in bounded steps so lazy-loaded and
// infinite-scroll content exists before the scan, then returns to the top. It
// stops as soon as the page stops growing. It reports how many steps grew the
// page and whether the bound cut it off (an unbounded feed never settles).
func ScrollToLoad(page playwright.Page, cfg config.Config) (grew int, truncated bool) {
	steps := cfg.Run.ScrollSteps
	if steps <= 0 {
		return 0, false
	}
	height := func() float64 {
		raw, err := page.Evaluate("() => Math.max(document.documentElement.scrollHeight, document.body ? document.body.scrollHeight : 0)")
		if err != nil {
			return 0
		}
		switch v := raw.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
		return 0
	}
	prev := height()
	for i := 0; i < steps; i++ {
		if _, err := page.Evaluate("() => window.scrollTo(0, document.documentElement.scrollHeight)"); err != nil {
			return grew, false
		}
		page.WaitForTimeout(250)
		_ = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State:   playwright.LoadStateNetworkidle,
			Timeout: playwright.Float(math.Min(1500, float64(cfg.Limits.SettleMS))),
		})
		h := height()
		if h <= prev+1 {
			break
		}
		grew++
		prev = h
		if i == steps-1 {
			truncated = true
		}
	}
	_, _ = page.Evaluate("() => window.scrollTo(0, 0)")
	return grew, truncated
}
