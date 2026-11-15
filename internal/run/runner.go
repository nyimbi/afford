// Package run is the crawl/probe loop.
//
// BFS in waves: every page is captured once, its links feed the next wave, and
// its controls are probed from fresh loads. Findings are written as they are
// produced, so a crash leaves a usable partial run.
package run

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/auth"
	"afford/internal/browser"
	"afford/internal/config"
	"afford/internal/discover"
	"afford/internal/gate"
	"afford/internal/model"
	"afford/internal/oracles"
	"afford/internal/pass"
	"afford/internal/payloads"
	"afford/internal/probe"
	"afford/internal/sanitize"
	"afford/internal/store"
	"afford/internal/visual"
)

type Item struct {
	URL    string
	Depth  int
	Parent string
}

// found is a URL discovered while visiting a page, with the depth it should be
// enqueued at. Carrying the depth matters: hardcoding zero made every page
// depth 0 and disarmed both the depth budget and the deep-link check.
type found struct {
	URL   string
	Depth int
}

type stats struct {
	pagesVisited    int
	pagesFailed     int
	controlsTotal   int
	controlsProbed  int
	formsTotal      int
	formsProbed     int
	inputsSwept     int
	payloadsSent    int
	authRedirects   int
	controlsSkipped int
}

type Runner struct {
	cfg     config.Config
	store   *store.Store
	runID   string
	baseDir string
	ctx     context.Context

	mu             sync.Mutex
	seen           map[string]bool
	templateCounts map[string]int
	stats          stats
	notTested      map[string]int
	skipped        map[string]int
	started        time.Time
	visitPage      func(playwright.BrowserContext, Item) []found

	host       *browser.Host
	session    auth.Result
	sessionGen int
	refreshes  int
	sessionMu  sync.Mutex
	reauthFlag map[string]bool // page URL → a re-login was requested for it
	reauthDone map[string]bool // page URL → it already had its one retry
}

func New(cfg config.Config, st *store.Store, runID, baseDir string, ctx context.Context) *Runner {
	runner := &Runner{
		cfg: cfg, store: st, runID: runID, baseDir: baseDir, ctx: ctx,
		seen: map[string]bool{}, templateCounts: map[string]int{},
		notTested: map[string]int{}, skipped: map[string]int{},
		reauthFlag: map[string]bool{}, reauthDone: map[string]bool{},
	}
	runner.visitPage = runner.visit
	return runner
}

func (r *Runner) bump(bucket map[string]int, reason string, n int) {
	r.mu.Lock()
	bucket[reason] += n
	r.mu.Unlock()
}

func (r *Runner) enqueue(next *[]Item, raw string, depth int, parent, how string) {
	url := discover.Normalize(raw)
	if !discover.InScope(url, r.cfg) {
		r.bump(r.notTested, "outside scope or off-origin", 1)
		return
	}
	if depth > r.cfg.Limits.MaxDepth {
		r.bump(r.notTested, "beyond max depth", 1)
		return
	}
	template := discover.Template(url)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[url] {
		return
	}
	if len(r.seen) >= r.cfg.Limits.MaxPages {
		r.notTested["page budget exhausted"]++
		return
	}
	if r.templateCounts[template] >= r.cfg.Limits.MaxInstancesPerTemplate {
		r.notTested["template instance cap"]++
		return
	}
	r.seen[url] = true
	r.templateCounts[template]++
	*next = append(*next, Item{URL: url, Depth: depth, Parent: parent})
}

func (r *Runner) Run(host *browser.Host, authResult auth.Result) (model.Summary, error) {
	r.started = time.Now().UTC()
	r.host = host
	r.session = authResult
	// A fresh browser context per page: cookies and the session come from the
	// storage state each time, but nothing one page wrote to localStorage can
	// leak into the next. The semaphore bounds concurrency.
	sem := make(chan struct{}, r.cfg.Limits.Concurrency)

	var frontier []Item
	r.enqueue(&frontier, r.cfg.Run.BaseURL, 0, "", "seed")

	for len(frontier) > 0 {
		if err := r.ctx.Err(); err != nil {
			r.notTested["run deadline reached"] += len(frontier)
			break
		}
		var (
			wg         sync.WaitGroup
			discovered = make(chan []found, len(frontier))
		)
		for _, item := range frontier {
			wg.Add(1)
			go func(it Item) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				discovered <- r.visitIsolated(it)
			}(item)
		}
		wg.Wait()
		close(discovered)

		var next []Item
		for items := range discovered {
			for _, f := range items {
				r.enqueue(&next, f.URL, f.Depth, "", "discovered")
			}
		}
		frontier = next
	}

	r.finalChecks()
	return r.finish()
}

// finish gates the findings (policy, suppression, baseline) and builds the
// summary from the gated view, so the exit code and the report agree.
func (r *Runner) finish() (model.Summary, error) {
	pages, findings := r.store.Snapshot()
	res, err := gate.Apply(findings, gate.Options{
		Cfg: r.cfg, Pages: pages, Now: time.Now().UTC(),
		Partial: r.notTested["run deadline reached"] > 0,
	})
	if err != nil {
		return model.Summary{}, err
	}
	r.store.SetFindings(res.Findings)
	summary := r.summary()
	summary.Counts, summary.Totals = res.Counts, res.Totals
	summary.Baseline, summary.Unconfirmed, summary.Suppressed = res.Baseline, res.Unconfirmed, res.Suppressed
	return summary, nil
}

// newContext opens a context from the current session.
func (r *Runner) newContext() (playwright.BrowserContext, int, error) {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()
	headers := map[string]string{}
	for k, v := range r.session.Headers {
		headers[k] = v
	}
	headers["X-Afford-Run"] = r.runID
	ctx, err := r.host.NewContext(r.session.StatePath, r.session.Creds, headers)
	return ctx, r.sessionGen, err
}

// refreshSession logs in again unless another worker already did since gen.
func (r *Runner) refreshSession(gen int) bool {
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()
	if gen != r.sessionGen {
		return true
	}
	// A bounded number of re-logins: an app that keeps ending the session, or a
	// role that cannot reach half the routes, must not turn into a login storm.
	if r.session.Refresh == nil || r.refreshes >= 3 {
		return false
	}
	r.refreshes++
	fresh, err := r.session.Refresh()
	if err != nil {
		return false
	}
	fresh.Refresh = r.session.Refresh
	r.session = fresh
	r.sessionGen++
	return true
}

// wantReauth reports whether a login redirect on this page should trigger one
// re-login and one retry. After the retry the redirect is a finding.
func (r *Runner) wantReauth(url string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Only a session that worked earlier in the run can have been lost; a first
	// redirect is the auth wall, and it is reported as such.
	if r.session.Refresh == nil || r.reauthDone[url] || r.stats.pagesVisited <= r.stats.authRedirects {
		return false
	}
	r.reauthDone[url] = true
	r.reauthFlag[url] = true
	return true
}

func (r *Runner) takeReauth(url string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	flagged := r.reauthFlag[url]
	delete(r.reauthFlag, url)
	return flagged
}

// visitIsolated runs one page visit in its own context, and retries once with a
// fresh session when the visit found itself logged out.
func (r *Runner) visitIsolated(item Item) []found {
	for attempt := 0; attempt < 2; attempt++ {
		bctx, gen, err := r.newContext()
		if err != nil {
			r.bump(r.notTested, "could not open browser context", 1)
			return nil
		}
		urls := r.visitSafely(bctx, item)
		_ = bctx.Close()
		if r.takeReauth(item.URL) {
			r.bump(r.notTested, "session lost mid-run (re-authenticated)", 1)
			if r.refreshSession(gen) {
				continue
			}
		}
		return urls
	}
	return nil
}

// visit captures one page and returns the URLs it discovered, at the depth they
// should be crawled from.
func (r *Runner) visit(bctx playwright.BrowserContext, item Item) []found {
	page, collector, err := browser.NewProbePage(bctx, r.cfg)
	if err != nil {
		r.bump(r.notTested, "could not open page", 1)
		return nil
	}
	defer func() { _ = page.Close() }()

	url := item.URL
	load := probe.Reset(page, r.cfg, url)
	final := page.URL()
	authRedirect := discover.IsLoginURL(final, r.cfg.Auth.LoginPathPatterns) &&
		!discover.IsLoginURL(url, r.cfg.Auth.LoginPathPatterns)
	if authRedirect && r.wantReauth(item.URL) {
		return nil
	}
	if authRedirect {
		r.mu.Lock()
		r.stats.authRedirects++
		r.mu.Unlock()
	}

	scanResult, err := discover.Scan(page)
	if err != nil {
		r.bump(r.notTested, "DOM scan failed", 1)
		return nil
	}
	if load.ScrollTruncated {
		r.bump(r.notTested, "scroll-to-load stopped at its step bound; the page may have more content", 1)
	}
	for _, skipped := range scanResult.SkippedFrames {
		r.bump(r.skipped, "cross-origin frame: "+discover.Template(skipped), 1)
	}
	screenshotRel, shotErr := r.shoot(page, url)
	status := 0
	if load.OK {
		status = load.Status
	} else {
		r.mu.Lock()
		r.stats.pagesFailed++
		r.mu.Unlock()
	}

	r.auditLinks(scanResult.Controls, final)
	visible := make([]model.Element, 0, len(scanResult.Controls))
	for _, c := range scanResult.Controls {
		if c.Visible {
			visible = append(visible, c)
		}
	}
	r.store.AddPage(model.Page{
		URL: url, Template: discover.Template(url), Depth: item.Depth,
		Parent: item.Parent, Status: status, Title: scanResult.Title,
		LoadMS: load.LoadMS, SettledMS: load.SettledMS, Controls: len(visible),
		Screenshot: screenshotRel, AuthRedirect: authRedirect,
	})
	r.mu.Lock()
	r.stats.pagesVisited++
	r.mu.Unlock()
	r.compareVisual(url, discover.Template(url), screenshotRel)

	obs, captureFindings := pass.Capture(page, collector, r.cfg, url, final, authRedirect, load, scanResult)
	_ = obs
	r.store.AddProbe(url)
	for _, f := range captureFindings {
		r.store.AddFinding(f)
	}
	if shotErr != nil {
		f := model.NewFinding("E_SCREENSHOT_FAILED", url)
		f.Detail["error"] = shotErr.Error()
		r.store.AddFinding(f)
	}

	if authRedirect {
		f := model.NewFinding("E_AUTH_REDIRECT", url)
		f.Detail["redirected_to"] = final
		f.Detail["status"] = status
		r.store.AddFinding(f)
		return nil
	}
	// A discovered link that lands on a different route is not the page it
	// promised. The seed is exempt: a root that redirects to a dashboard is a
	// routing decision, not a broken link.
	if item.Depth > 0 && discover.Template(final) != discover.Template(url) {
		f := model.NewFinding("E_NOT_DEEPLINKABLE", url)
		f.Detail["requested"] = url
		f.Detail["landed"] = final
		r.store.AddFinding(f)
	}

	var discovered []found
	for _, el := range scanResult.Controls {
		if el.Tag != "a" || !el.Visible {
			continue
		}
		if target := discover.Absolute(final, el.Href); target != "" {
			discovered = append(discovered, found{URL: target, Depth: item.Depth + 1})
		}
	}

	r.probeControls(bctx, page, collector, url, item.Depth, scanResult, &discovered)
	r.sweep(page, collector, url, scanResult.Controls)
	r.probeForms(page, collector, url, scanResult)
	return discovered
}

func (r *Runner) shoot(page playwright.Page, url string) (string, error) {
	sum := sha1.Sum([]byte(url))
	relative := "screenshots/" + hex.EncodeToString(sum[:])[:12] + ".png"
	path := filepath.Join(r.baseDir, relative)
	masks := []string(nil)
	if r.cfg.Visual.Enabled {
		masks = r.cfg.Visual.Mask
	}
	if _, err := browser.Screenshot(page, path, masks); err != nil {
		return "", err
	}
	return relative, nil
}

func (r *Runner) debugOn() bool { return os.Getenv("AFFORD_DEBUG") != "" }

func (r *Runner) debug(pageURL, contextKind string, obs model.Observation, el *model.Element) {
	element := ""
	if el != nil {
		element = el.Tag + " " + el.Name
	}
	fmt.Fprintf(os.Stderr, "[debug] %-9s %-30s effect=%-9s err=%q pageerrors=%d requests=%d %s\n",
		contextKind, pageURL, obs.Effect, obs.ActionError, len(obs.PageErrors), len(obs.Requests), element)
}

func (r *Runner) emit(pageURL, kind, payload string, obs model.Observation, contextKind string, el *model.Element) {
	if r.debugOn() {
		r.debug(pageURL, contextKind, obs, el)
	}
	r.store.AddProbe(pageURL)
	for _, f := range oracles.Evaluate(obs, pageURL, contextKind, el) {
		if len(f.Evidence) == 0 {
			f.Evidence = oracles.FallbackEvidence(obs)
		}
		if f.Payload == "" {
			f.Payload = payload
		}
		r.store.AddFinding(f)
	}
}

// visitSafely contains a panic to one page. A panic in the probe path is a bug
// in the tool, not in the application, so it is recorded as a harness finding and
// the crawl continues.
func (r *Runner) visitSafely(ctx playwright.BrowserContext, item Item) (urls []found) {
	defer func() {
		if rec := recover(); rec != nil {
			f := model.NewFinding("E_HARNESS_TIMEOUT", item.URL)
			f.Detail["error"] = fmt.Sprint(rec)
			f.Evidence = []string{"panic during page visit: " + fmt.Sprint(rec)}
			r.store.AddFinding(f)
		}
	}()
	return r.visitPage(ctx, item)
}

// auditLinks records every link the crawler will not follow, with the reason.
// The findings are emitted once per (href, disposition) and merged across pages
// by the store; the inventory keeps the per-page provenance.
func (r *Runner) auditLinks(controls []model.Element, base string) {
	seen := map[string]bool{}
	for _, el := range controls {
		if el.Tag != "a" || !el.Visible {
			continue
		}
		resolved, disposition, reason := discover.LinkDisposition(base, el.Href, r.cfg)
		if disposition == "followed" {
			continue
		}
		key := strings.TrimSpace(el.Href) + "|" + disposition
		if seen[key] {
			continue
		}
		seen[key] = true
		r.store.AddLink(model.LinkRecord{
			Source: base, Href: el.Href, Resolved: sanitize.URL(resolved),
			Disposition: disposition, Reason: reason,
		})
		if disposition == "excluded" || disposition == "off_origin" {
			f := model.NewFinding("E_OUT_OF_SCOPE", base)
			f.Element = fmt.Sprintf("a “%s”", el.Name)
			f.Detail["href"] = el.Href
			f.Detail["resolved"] = resolved
			f.Detail["disposition"] = disposition
			f.Detail["reason"] = reason
			r.store.AddFinding(f)
		}
	}
}

func (r *Runner) probeControls(
	bctx playwright.BrowserContext,
	page playwright.Page,
	collector *browser.Collector,
	url string,
	depth int,
	scanResult model.ScanResult,
	discovered *[]found,
) {
	controls := map[int]model.Element{}
	for _, c := range scanResult.Controls {
		controls[c.Idx] = c
	}
	attempts := 0
	// Controls that match the destructive vocabulary and are permitted go last:
	// a logout or delete can end the session or remove the page's own subject,
	// and every probe after it would measure the wreckage.
	ordered := make([]model.Element, 0, len(scanResult.Controls))
	var guarded []model.Element
	for _, el := range scanResult.Controls {
		if payloads.DestructiveKind(el, r.cfg.Safety.ExtraDestructive) != "" {
			guarded = append(guarded, el)
		} else {
			ordered = append(ordered, el)
		}
	}
	ordered = append(ordered, guarded...)
	for i := 0; i < len(ordered); i++ {
		el := ordered[i]
		if attempts >= r.cfg.Limits.MaxControlsPerPage {
			r.bump(r.notTested, "control budget per page", 1)
			break
		}
		r.mu.Lock()
		r.stats.controlsTotal++
		r.mu.Unlock()
		if !el.Visible || el.Disabled {
			r.bump(r.notTested, "disabled or hidden control", 1)
			continue
		}
		if el.IsSubmit() {
			continue
		}
		if el.Src == "input" && payloads.IsTextLike(el) {
			continue
		}
		if el.Tag == "a" && strings.TrimSpace(el.Href) == "" {
			continue
		}
		if el.Tag == "a" {
			if target := discover.Absolute(url, el.Href); target != "" && !discover.InScope(target, r.cfg) {
				r.bump(r.skipped, "off-scope link", 1)
				continue
			}
		}
		if kind := payloads.DestructiveKind(el, r.cfg.Safety.ExtraDestructive); kind != "" &&
			!r.cfg.Safety.Permits(kind) {
			f := model.NewFinding("E_DESTRUCTIVE_SKIPPED", url)
			f.Element = fmt.Sprintf("%s “%s”", el.Tag, el.Name)
			f.Detail["kind"] = kind
			f.Detail["required_flag"] = payloads.OptInFlag(kind)
			r.store.AddFinding(f)
			r.bump(r.skipped, kind+" control", 1)
			continue
		}
		attempts++
		r.mu.Lock()
		r.stats.controlsProbed++
		r.mu.Unlock()
		obs, controlFindings := pass.Control(bctx, page, collector, r.cfg, url, el, filepath.Join(r.baseDir, "popups"))
		r.store.AddProbe(url)
		if r.debugOn() {
			r.debug(url, "action", obs, &el)
		}
		for _, f := range controlFindings {
			r.store.AddFinding(f)
		}
		if obs.Popups > 0 {
			f := model.NewFinding("E_NEW_TAB", url)
			f.Element = fmt.Sprintf("%s “%s”", el.Tag, el.Name)
			f.Detail["popups"] = obs.Extra["popups"]
			r.store.AddFinding(f)
		}
		r.popupFindings(url, obs)
		if revealed := pass.RevealedOf(obs); len(revealed) > 0 {
			ordered = append(ordered, revealed...)
			r.bump(r.notTested, "controls revealed by a click (probed at depth 1)", len(revealed))
		}
		if obs.Navigated && obs.URLAfter != "" {
			landed := discover.Normalize(obs.URLAfter)
			target := discover.Absolute(url, el.Href)
			if discover.InScope(landed, r.cfg) && (target == "" || landed != discover.Normalize(target)) {
				*discovered = append(*discovered, found{URL: landed, Depth: depth + 1})
			}
		}
	}
}

// compareVisual runs the baseline comparison for one page and records what it
// decided. Artifacts are written under the run directory so the report can show
// the three images side by side.
func (r *Runner) compareVisual(rawURL, template, shotRel string) {
	if !r.cfg.Visual.Enabled || shotRel == "" || r.visualIgnored(rawURL) {
		return
	}
	current, err := os.ReadFile(filepath.Join(r.baseDir, shotRel))
	if err != nil {
		r.bump(r.notTested, "visual: screenshot unreadable", 1)
		return
	}
	key := visual.Key(template, r.cfg.Run.Viewport)
	outcome := visual.Consider(
		visual.Store{Dir: r.cfg.Visual.Dir},
		r.cfg.Visual, key, current, filepath.Join(r.baseDir, "visual"),
	)
	switch outcome.Action {
	case "seeded", "updated":
		f := model.NewFinding("E_VISUAL_BASELINE_WRITTEN", rawURL)
		f.Detail["action"] = outcome.Action
		f.Detail["key"] = outcome.Key
		f.Detail["baseline"] = relPath(r.baseDir, outcome.Baseline)
		r.store.AddFinding(f)
	case "diff":
		f := model.NewFinding("E_VISUAL_DIFF", rawURL)
		f.Detail["key"] = outcome.Key
		f.Detail["pct"] = outcome.Pct
		f.Detail["changed_pixels"] = outcome.Changed
		f.Detail["total_pixels"] = outcome.Total
		f.Detail["size_changed"] = outcome.SizeChanged
		f.Detail["baseline"] = relPath(r.baseDir, outcome.Baseline)
		f.Detail["current"] = relPath(r.baseDir, outcome.Current)
		f.Detail["diff"] = relPath(r.baseDir, outcome.Diff)
		r.store.AddFinding(f)
	case "error":
		r.bump(r.notTested, "visual: compare failed", 1)
	}
}

func (r *Runner) visualIgnored(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, pattern := range r.cfg.Visual.Ignore {
		if discover.MatchPath(pattern, parsed.Path) {
			return true
		}
	}
	return false
}

func relPath(base, path string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// popupFindings attributes what a popup renders to the control that opened it.
func (r *Runner) popupFindings(opener string, obs model.Observation) {
	for _, popup := range obs.PopupResults {
		if !discover.InScope(popup.URL, r.cfg) {
			f := model.NewFinding("E_OUT_OF_SCOPE", opener)
			f.Element = "popup"
			f.Detail["popup_url"] = popup.URL
			f.Detail["reason"] = "popup opened off-origin"
			r.store.AddFinding(f)
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
			r.store.AddFinding(f)
		}
		if status >= 400 {
			f := model.NewFinding("E_DEAD_LINK", opener)
			f.Element = "popup"
			f.Detail["popup_url"] = popup.URL
			f.Detail["status"] = popup.Status
			if popup.Screenshot != "" {
				f.Detail["screenshot"] = popup.Screenshot
			}
			r.store.AddFinding(f)
		}
	}
}

func (r *Runner) sweep(page playwright.Page, collector *browser.Collector, url string, controls []model.Element) {
	result := probe.SweepInputs(page, collector, r.cfg, url, controls)
	r.mu.Lock()
	r.stats.inputsSwept += result.Inputs
	r.stats.payloadsSent += result.Payloads
	r.mu.Unlock()
	for _, anomaly := range result.Anomalies {
		el := anomaly.Element
		payload := fmt.Sprintf("%s ← %s", el.Name, anomaly.Label)
		value, _ := anomaly.Obs.Extra["payload"].(string)
		r.emit(url, "validate", fmt.Sprintf("%s=%q", anomaly.Label, value), anomaly.Obs, "validate", &el)
		_ = payload
	}
}

func (r *Runner) probeForms(page playwright.Page, collector *browser.Collector, url string, scanResult model.ScanResult) {
	controls := map[int]model.Element{}
	for _, c := range scanResult.Controls {
		controls[c.Idx] = c
	}
	limit := len(scanResult.Forms)
	if limit > r.cfg.Limits.MaxFormsPerPage {
		limit = r.cfg.Limits.MaxFormsPerPage
	}
	for _, form := range scanResult.Forms[:limit] {
		r.mu.Lock()
		r.stats.formsTotal++
		r.mu.Unlock()
		if len(form.SubmitIdx) == 0 {
			f := model.NewFinding("E_FORM_NO_SUBMIT", url)
			f.Detail["form"] = form.Name
			r.store.AddFinding(f)
			continue
		}
		if !r.cfg.Safety.SubmitForms {
			r.bump(r.skipped, "form submission disabled", 1)
			continue
		}
		if submitEl, ok := controls[form.SubmitIdx[0]]; ok {
			if kind := payloads.DestructiveKind(submitEl, r.cfg.Safety.ExtraDestructive); kind != "" &&
				!r.cfg.Safety.Permits(kind) {
				f := model.NewFinding("E_DESTRUCTIVE_SKIPPED", url)
				f.Element = fmt.Sprintf("%s “%s”", submitEl.Tag, submitEl.Name)
				f.Detail["kind"] = kind
				f.Detail["required_flag"] = payloads.OptInFlag(kind)
				r.store.AddFinding(f)
				r.bump(r.skipped, kind+" submit control", 1)
				continue
			}
		}
		requiredPresent := false
		for _, idx := range form.ControlIdx {
			if el, ok := controls[idx]; ok && payloads.EmptyRequired(el) {
				requiredPresent = true
			}
		}
		r.mu.Lock()
		r.stats.formsProbed++
		r.mu.Unlock()
		modes := []string{"valid"}
		if requiredPresent {
			modes = []string{"invalid", "valid"}
		}
		for _, mode := range modes {
			obs, meta := probe.ProbeForm(page, collector, r.cfg, url, form, controls, mode)
			r.emit(url, "form", "submit="+mode, obs, "form", nil)
			for _, f := range probe.FormFindings(obs, meta, url) {
				if len(f.Evidence) == 0 {
					f.Evidence = oracles.FallbackEvidence(obs)
				}
				f.Payload = "submit=" + mode
				r.store.AddFinding(f)
			}
		}
	}
}

func (r *Runner) finalChecks() {
	if r.cfg.Auth.Mode == "none" || r.stats.authRedirects == 0 {
		return
	}
	if r.stats.pagesVisited <= 1 || r.stats.authRedirects >= r.stats.pagesVisited {
		f := model.NewFinding("E_AUTH_WALL", r.cfg.Run.BaseURL)
		f.Detail["pages_visited"] = r.stats.pagesVisited
		f.Detail["auth_redirects"] = r.stats.authRedirects
		f.Evidence = []string{
			"every navigation landed on a login path; the application was never seen",
		}
		r.store.AddFinding(f)
	}
}

func (r *Runner) summary() model.Summary {
	_, findings := r.store.Snapshot()
	counts := map[model.Severity]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	notTested := make([]string, 0, len(r.notTested)+len(r.skipped))
	for reason, n := range r.notTested {
		notTested = append(notTested, fmt.Sprintf("%s (%d)", reason, n))
	}
	for reason, n := range r.skipped {
		notTested = append(notTested, fmt.Sprintf("%s (%d)", reason, n))
	}
	sort.Strings(notTested)
	return model.Summary{
		RunID: r.runID, BaseURL: r.cfg.Run.BaseURL,
		StartedAt: r.started, FinishedAt: time.Now().UTC(),
		PagesDiscovered: len(r.seen), PagesVisited: r.stats.pagesVisited,
		PagesFailed:   r.stats.pagesFailed,
		ControlsTotal: r.stats.controlsTotal, ControlsProbed: r.stats.controlsProbed,
		ControlsSkipped: r.stats.controlsSkipped,
		FormsTotal:      r.stats.formsTotal, FormsProbed: r.stats.formsProbed,
		InputsSwept: r.stats.inputsSwept, PayloadsSent: r.stats.payloadsSent,
		Counts: counts, NotTested: notTested, VisualEnabled: r.cfg.Visual.Enabled,
	}
}
