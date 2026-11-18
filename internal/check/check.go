// Package check probes one page on demand, for the MCP server.
//
// It shares the oracles, payloads and probe drivers with the crawler but returns
// results in memory instead of writing a run. The browser is serialized: two
// concurrent probes on the same app are a source of flaky evidence, not speed.
package check

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nyimbi/afford/internal/auth"
	"github.com/nyimbi/afford/internal/browser"
	"github.com/nyimbi/afford/internal/config"
	"github.com/nyimbi/afford/internal/discover"
	"github.com/nyimbi/afford/internal/gate"
	"github.com/nyimbi/afford/internal/model"
	"github.com/nyimbi/afford/internal/oracles"
	"github.com/nyimbi/afford/internal/pass"
	"github.com/nyimbi/afford/internal/payloads"
	"github.com/nyimbi/afford/internal/probe"
	"github.com/nyimbi/afford/internal/visual"
)

type Options struct {
	ReadOnly    bool
	Submit      bool
	Depth       string
	MaxControls int
	Screenshot  bool
}

type Result struct {
	URL            string          `json:"url"`
	FinalURL       string          `json:"final_url"`
	Title          string          `json:"title"`
	Status         int             `json:"status"`
	LoadMS         int64           `json:"load_ms"`
	SettledMS      int64           `json:"settled_ms"`
	AuthRedirect   bool            `json:"auth_redirect,omitempty"`
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

func (r Result) Counts() map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		if !f.Gates() {
			continue
		}
		out[string(f.Severity)]++
	}
	return out
}

// OK reports whether no finding at major severity or worse survived.
func (r Result) OK() bool {
	for _, f := range r.Findings {
		if f.Gates() && f.Rank() <= model.SeverityOrder[model.Major] {
			return false
		}
	}
	return true
}

type Checker struct {
	cfg     config.Config
	host    *browser.Host
	session auth.Result
	mu      sync.Mutex
}

func New(cfg config.Config, host *browser.Host, session auth.Result) *Checker {
	return &Checker{cfg: cfg, host: host, session: session}
}

// allowed is the SSRF gate. An agent can name any URL; the server will only
// open the configured origin or an explicitly allowed host.
func (c *Checker) allowed(raw string) bool {
	if c.cfg.MCP.AllowAnyHost {
		return true
	}
	target, err := url.Parse(raw)
	if err != nil {
		return false
	}
	base, err := url.Parse(c.cfg.Run.BaseURL)
	if err != nil {
		return false
	}
	if strings.EqualFold(target.Host, base.Host) && target.Scheme == base.Scheme {
		return true
	}
	for _, host := range c.cfg.Scope.AllowHosts {
		if strings.EqualFold(host, target.Host) {
			return true
		}
	}
	return false
}

// Check probes one page and applies the same policy, suppression and baseline
// the crawler does, so an agent and CI see one verdict.
func (c *Checker) Check(ctx context.Context, rawURL string, opts Options) (Result, error) {
	result, err := c.check(ctx, rawURL, opts)
	if err != nil {
		return result, err
	}
	cfg := c.cfg
	cfg.Baseline.Accept = false
	gated, gerr := gate.Apply(result.Findings, gate.Options{
		Cfg: cfg, Now: time.Now().UTC(), Partial: true,
		Pages: []model.Page{{URL: rawURL, Template: discover.Template(rawURL)}},
	})
	if gerr != nil {
		return result, gerr
	}
	result.Findings = gated.Findings
	return result, nil
}

func (c *Checker) check(ctx context.Context, rawURL string, opts Options) (Result, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return Result{}, fmt.Errorf("url must be absolute http(s), got %q", rawURL)
	}
	if !c.allowed(rawURL) {
		return Result{}, fmt.Errorf(
			"refusing to open %s: it is neither the configured origin (%s) nor an allowed host. "+
				"Widen [scope] allow_hosts or [mcp] allow_any_host deliberately",
			rawURL, c.cfg.Run.BaseURL)
	}
	if opts.MaxControls <= 0 {
		opts.MaxControls = c.cfg.MCP.MaxControls
	}
	cfg := c.cfg
	if opts.Depth != "" {
		switch opts.Depth {
		case "smoke", "standard", "deep":
			cfg.Run.Depth = opts.Depth
		default:
			return Result{}, fmt.Errorf("depth must be smoke, standard or deep, got %q", opts.Depth)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	started := time.Now()

	bctx, err := c.host.NewContext(c.session.StatePath, c.session.Creds, c.session.Headers)
	if err != nil {
		return Result{}, fmt.Errorf("open browser context: %w", err)
	}
	defer func() { _ = bctx.Close() }()
	page, collector, err := browser.NewProbePage(bctx, cfg)
	if err != nil {
		return Result{}, fmt.Errorf("open page: %w", err)
	}
	defer func() { _ = page.Close() }()

	load := probe.Reset(page, cfg, rawURL)
	if !load.OK {
		return Result{}, fmt.Errorf("could not open %s: %w", rawURL, load.Err)
	}
	scanResult, err := discover.Scan(page)
	if err != nil {
		return Result{}, fmt.Errorf("scan %s: %w", rawURL, err)
	}

	result := Result{
		URL: rawURL, FinalURL: page.URL(), Title: scanResult.Title,
		Status: load.Status, LoadMS: load.LoadMS, SettledMS: load.SettledMS,
	}
	result.AuthRedirect = discover.IsLoginURL(result.FinalURL, cfg.Auth.LoginPathPatterns) &&
		!discover.IsLoginURL(rawURL, cfg.Auth.LoginPathPatterns)

	seen := map[string]bool{}
	add := func(findings ...model.Finding) {
		for _, f := range findings {
			if seen[f.Key()] {
				continue
			}
			seen[f.Key()] = true
			result.Findings = append(result.Findings, f)
		}
	}

	visible := pass.Visible(scanResult.Controls)
	_, captured := pass.Capture(page, collector, cfg, result.FinalURL, result.FinalURL, result.AuthRedirect, load, scanResult)
	add(captured...)
	if result.AuthRedirect {
		f := model.NewFinding("E_AUTH_REDIRECT", rawURL)
		f.Detail["redirected_to"] = result.FinalURL
		add(f)
	} else if discover.Template(result.FinalURL) != discover.Template(rawURL) {
		f := model.NewFinding("E_NOT_DEEPLINKABLE", rawURL)
		f.Detail["requested"] = rawURL
		f.Detail["landed"] = result.FinalURL
		add(f)
	}

	result.Controls = len(visible)
	if opts.Screenshot {
		shot, err := c.shoot(page, rawURL)
		if err != nil {
			f := model.NewFinding("E_SCREENSHOT_FAILED", rawURL)
			f.Detail["error"] = err.Error()
			add(f)
		}
		result.Screenshot = shot
		if c.cfg.Visual.Enabled && shot != "" && !c.visualIgnored(rawURL) {
			if current, readErr := os.ReadFile(shot); readErr == nil {
				outcome := visual.Consider(
					visual.Store{Dir: c.cfg.Visual.Dir},
					c.cfg.Visual, visual.Key(discover.Template(rawURL), c.cfg.Run.Viewport),
					current, filepath.Join(c.cfg.Run.Out, "mcp", "visual"),
				)
				switch outcome.Action {
				case "seeded", "updated":
					f := model.NewFinding("E_VISUAL_BASELINE_WRITTEN", rawURL)
					f.Detail["action"] = outcome.Action
					f.Detail["key"] = outcome.Key
					f.Detail["baseline"] = outcome.Baseline
					add(f)
				case "diff":
					f := model.NewFinding("E_VISUAL_DIFF", rawURL)
					f.Detail["key"] = outcome.Key
					f.Detail["pct"] = outcome.Pct
					f.Detail["changed_pixels"] = outcome.Changed
					f.Detail["total_pixels"] = outcome.Total
					f.Detail["size_changed"] = outcome.SizeChanged
					f.Detail["baseline"] = outcome.Baseline
					f.Detail["current"] = outcome.Current
					f.Detail["diff"] = outcome.Diff
					add(f)
				}
			}
		}
	}
	if opts.ReadOnly || result.AuthRedirect {
		result.Notes = append(result.Notes, readOnlyNote(opts, result.AuthRedirect))
		model.SortFindings(result.Findings)
		result.DurationMS = time.Since(started).Milliseconds()
		return result, nil
	}

	c.probeControls(bctx, page, collector, cfg, rawURL, scanResult, opts, &result, add)
	c.sweep(page, collector, cfg, rawURL, scanResult, &result, add)
	c.probeForms(page, collector, cfg, rawURL, scanResult, opts, &result, add)

	model.SortFindings(result.Findings)
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

func readOnlyNote(opts Options, authRedirect bool) string {
	if authRedirect {
		return "read-only: navigation landed on a login path, so the app was not seen"
	}
	if opts.ReadOnly {
		return "read-only: page captured, no controls clicked and no forms filled"
	}
	return ""
}

func (c *Checker) shoot(page playwright.Page, rawURL string) (string, error) {
	sum := sha1.Sum([]byte(rawURL))
	name := fmt.Sprintf("%d-%s.png", time.Now().UnixMilli(), hex.EncodeToString(sum[:])[:10])
	path := filepath.Join(c.cfg.Run.Out, "mcp", "shots", name)
	full, err := filepath.Abs(path)
	if err != nil {
		full = path
	}
	masks := c.cfg.ScreenshotMasks()
	if c.cfg.Visual.Enabled {
		masks = append(masks, c.cfg.Visual.Mask...)
	}
	if _, err := browser.Screenshot(page, full, masks); err != nil {
		return "", err
	}
	return full, nil
}

func (c *Checker) probeControls(
	bctx playwright.BrowserContext,
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	rawURL string,
	scanResult model.ScanResult,
	opts Options,
	result *Result,
	add func(...model.Finding),
) {
	probed, offScope := 0, 0
	destructive := map[string]int{}
	queue := append([]model.Element(nil), scanResult.Controls...)
	for i := 0; i < len(queue); i++ {
		el := queue[i]
		if probed >= opts.MaxControls {
			result.Notes = append(result.Notes, fmt.Sprintf("control budget reached at %d", probed))
			break
		}
		if !el.Visible || el.Disabled || el.IsSubmit() {
			continue
		}
		if el.Src == "input" && payloads.IsTextLike(el) {
			continue
		}
		if el.Tag == "a" && strings.TrimSpace(el.Href) == "" {
			continue
		}
		if el.Tag == "a" {
			if target := discover.Absolute(rawURL, el.Href); target != "" && !discover.InScope(target, cfg) {
				offScope++
				continue
			}
		}
		if kind := payloads.DestructiveKind(el, cfg.Safety.ExtraDestructive); kind != "" &&
			!cfg.Safety.Permits(kind) {
			destructive[kind]++
			continue
		}
		probed++
		obs, controlFindings := pass.Control(bctx, page, collector, cfg, rawURL, el,
			filepath.Join(cfg.Run.Out, "mcp", "popups"))
		add(controlFindings...)
		if obs.Popups > 0 {
			f := model.NewFinding("E_NEW_TAB", rawURL)
			f.Element = fmt.Sprintf("%s “%s”", el.Tag, el.Name)
			f.Detail["popups"] = obs.Extra["popups"]
			add(f)
		}
		add(c.popupFindings(rawURL, obs)...)
		queue = append(queue, pass.RevealedOf(obs)...)
	}
	result.ControlsProbed = probed
	for kind, n := range destructive {
		result.Notes = append(result.Notes, fmt.Sprintf(
			"skipped %d %s control(s); start the server with %s to include them",
			n, kind, payloads.OptInFlag(kind)))
	}
	if offScope > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("skipped %d link(s) outside the crawl scope", offScope))
	}
}

func (c *Checker) visualIgnored(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, pattern := range c.cfg.Visual.Ignore {
		if discover.MatchPath(pattern, parsed.Path) {
			return true
		}
	}
	return false
}

func (c *Checker) popupFindings(opener string, obs model.Observation) []model.Finding {
	var out []model.Finding
	for _, popup := range obs.PopupResults {
		if !discover.InScope(popup.URL, c.cfg) {
			f := model.NewFinding("E_OUT_OF_SCOPE", opener)
			f.Element = "popup"
			f.Detail["popup_url"] = popup.URL
			f.Detail["reason"] = "popup opened off-origin"
			out = append(out, f)
			continue
		}
		capture := popup.Observation
		status := capture.Status
		capture.Status = 0 // the popup's own dead-link finding carries the status
		for _, f := range oracles.Evaluate(capture, opener, "capture", nil) {
			if f.Detail == nil {
				f.Detail = map[string]any{}
			}
			f.Detail["popup_url"] = popup.URL
			if popup.Screenshot != "" {
				f.Detail["screenshot"] = popup.Screenshot
			}
			f.Evidence = append(f.Evidence, "popup opened: "+popup.URL)
			out = append(out, f)
		}
		if status >= 400 {
			f := model.NewFinding("E_DEAD_LINK", opener)
			f.Element = "popup"
			f.Detail["popup_url"] = popup.URL
			f.Detail["status"] = popup.Status
			out = append(out, f)
		}
	}
	return out
}

func (c *Checker) sweep(
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	rawURL string,
	scanResult model.ScanResult,
	result *Result,
	add func(...model.Finding),
) {
	swept := probe.SweepInputs(page, collector, cfg, rawURL, scanResult.Controls)
	result.InputsSwept = swept.Inputs
	result.PayloadsSent = swept.Payloads
	for _, anomaly := range swept.Anomalies {
		el := anomaly.Element
		value, _ := anomaly.Obs.Extra["payload"].(string)
		findings := oracles.Evaluate(anomaly.Obs, rawURL, "validate", &el)
		for i := range findings {
			findings[i].Payload = fmt.Sprintf("%s=%q", anomaly.Label, value)
			if len(findings[i].Evidence) == 0 {
				findings[i].Evidence = oracles.FallbackEvidence(anomaly.Obs)
			}
		}
		add(findings...)
	}
}

func (c *Checker) probeForms(
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	rawURL string,
	scanResult model.ScanResult,
	opts Options,
	result *Result,
	add func(...model.Finding),
) {
	controls := map[int]model.Element{}
	for _, el := range scanResult.Controls {
		controls[el.Idx] = el
	}
	limit := len(scanResult.Forms)
	if limit > cfg.Limits.MaxFormsPerPage {
		limit = cfg.Limits.MaxFormsPerPage
	}
	for _, form := range scanResult.Forms[:limit] {
		result.Forms++
		if len(form.SubmitIdx) == 0 {
			f := model.NewFinding("E_FORM_NO_SUBMIT", rawURL)
			f.Detail["form"] = form.Name
			add(f)
			continue
		}
		if !opts.Submit {
			result.Notes = append(result.Notes,
				"forms were filled and validated but not submitted; pass submit=true to submit them")
			continue
		}
		if submitEl, ok := controls[form.SubmitIdx[0]]; ok {
			if kind := payloads.DestructiveKind(submitEl, cfg.Safety.ExtraDestructive); kind != "" &&
				!cfg.Safety.Permits(kind) {
				result.Notes = append(result.Notes, fmt.Sprintf(
					"skipped a %s submit control (%q); start the server with %s to include it",
					kind, submitEl.Name, payloads.OptInFlag(kind)))
				continue
			}
		}
		requiredPresent := false
		for _, idx := range form.ControlIdx {
			if el, ok := controls[idx]; ok && payloads.EmptyRequired(el) {
				requiredPresent = true
			}
		}
		modes := []string{"valid"}
		if requiredPresent {
			modes = []string{"invalid", "valid"}
		}
		result.FormsProbed++
		for _, mode := range modes {
			obs, meta := probe.ProbeForm(page, collector, cfg, rawURL, form, controls, mode)
			formFindings := oracles.Evaluate(obs, rawURL, "form", nil)
			formFindings = append(formFindings, probe.FormFindings(obs, meta, rawURL)...)
			for i := range formFindings {
				formFindings[i].Payload = "submit=" + mode
				if len(formFindings[i].Evidence) == 0 {
					formFindings[i].Evidence = oracles.FallbackEvidence(obs)
				}
			}
			add(formFindings...)
		}
	}
}
