// Package browser owns the Playwright process and per-probe event capture.
package browser

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/config"
	"afford/internal/model"
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
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
	})
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("launch chromium: %w", err)
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
}

func (c *Collector) Bind(page playwright.Page) {
	page.On("console", func(msg playwright.ConsoleMessage) {
		text := msg.Text()
		if loc := msg.Location(); loc != nil && loc.URL != "" {
			text = fmt.Sprintf("%s  [%s]", text, loc.URL)
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
		c.mu.Lock()
		c.Requests = append(c.Requests, model.RequestRecord{
			URL:          req.URL(),
			Method:       req.Method(),
			ResourceType: req.ResourceType(),
			Kind:         kindOf(req.ResourceType()),
			Status:       resp.Status(),
		})
		c.mu.Unlock()
	})
	page.On("requestfailed", func(req playwright.Request) {
		failure := "failed"
		if err := req.Failure(); err != nil {
			failure = err.Error()
		}
		c.mu.Lock()
		c.Requests = append(c.Requests, model.RequestRecord{
			URL:          req.URL(),
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

func NewProbePage(ctx playwright.BrowserContext) (playwright.Page, *Collector, error) {
	page, err := ctx.NewPage()
	if err != nil {
		return nil, nil, err
	}
	page.SetDefaultTimeout(8000)
	collector := &Collector{}
	collector.Bind(page)
	return page, collector, nil
}

// Settle waits for load, then quiet network, then a stable node count.
func Settle(page playwright.Page, cfg config.Config) {
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
