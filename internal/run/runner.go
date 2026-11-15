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
	"afford/internal/model"
	"afford/internal/oracles"
	"afford/internal/payloads"
	"afford/internal/probe"
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
}

func New(cfg config.Config, st *store.Store, runID, baseDir string, ctx context.Context) *Runner {
	return &Runner{
		cfg: cfg, store: st, runID: runID, baseDir: baseDir, ctx: ctx,
		seen: map[string]bool{}, templateCounts: map[string]int{},
		notTested: map[string]int{}, skipped: map[string]int{},
	}
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
	contexts := make([]playwright.BrowserContext, 0, r.cfg.Limits.Concurrency)
	for i := 0; i < r.cfg.Limits.Concurrency; i++ {
		ctx, err := host.NewContext(authResult.StatePath, authResult.Creds, authResult.Headers)
		if err != nil {
			for _, c := range contexts {
				_ = c.Close()
			}
			return model.Summary{}, fmt.Errorf("open browser context: %w", err)
		}
		contexts = append(contexts, ctx)
	}
	defer func() {
		for _, c := range contexts {
			_ = c.Close()
		}
	}()

	pool := make(chan playwright.BrowserContext, len(contexts))
	for _, c := range contexts {
		pool <- c
	}

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
				ctx := <-pool
				defer func() { pool <- ctx }()
				urls := r.visit(ctx, it)
				discovered <- urls
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
	return r.summary(), nil
}

// visit captures one page and returns the URLs it discovered, at the depth they
// should be crawled from.
func (r *Runner) visit(bctx playwright.BrowserContext, item Item) []found {
	page, collector, err := browser.NewProbePage(bctx)
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

	textSample, _ := page.Evaluate("() => (document.body ? document.body.innerText : '').slice(0, 1500)")
	sample, _ := textSample.(string)
	obs := model.Observation{
		URLBefore:       url,
		URLAfter:        final,
		Title:           scanResult.Title,
		Status:          status,
		BodyLen:         scanResult.BodyLen,
		Requests:        collector.SnapshotCopy().Requests,
		ConsoleErrors:   collector.SnapshotCopy().ConsoleErrors,
		ConsoleWarnings: collector.SnapshotCopy().ConsoleWarnings,
		PageErrors:      collector.SnapshotCopy().PageErrors,
		Dialogs:         collector.SnapshotCopy().Dialogs,
		Extra: map[string]any{
			"overflow_x":        scanResult.OverflowX,
			"scroll_width":      scanResult.ScrollWidth,
			"viewport_width":    scanResult.ViewportWidth,
			"text_sample":       sample,
			"interactive_count": len(visible),
			"auth_redirect":     authRedirect,
		},
	}
	r.emit(url, "capture", "", obs, "capture", nil)
	for _, f := range append(oracles.Unlabeled(visible, url), oracles.EmptyHref(visible, url)...) {
		r.store.AddFinding(f)
	}
	for _, f := range oracles.OptionsAudit(visible, url) {
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

func (r *Runner) emit(pageURL, kind, payload string, obs model.Observation, contextKind string, el *model.Element) {
	if os.Getenv("AFFORD_DEBUG") != "" {
		element := ""
		if el != nil {
			element = el.Tag + " " + el.Name
		}
		fmt.Fprintf(os.Stderr, "[debug] %-9s %-30s effect=%-9s err=%q pageerrors=%d requests=%d %s\n",
			contextKind, pageURL, obs.Effect, obs.ActionError, len(obs.PageErrors), len(obs.Requests), element)
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
			Source: base, Href: el.Href, Resolved: resolved,
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
	for _, el := range scanResult.Controls {
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
		obs := probe.ProbeControl(bctx, page, collector, r.cfg, url, el, filepath.Join(r.baseDir, "popups"))
		pointedTo := el
		r.emit(url, "control", "effect="+obs.Effect, obs, "action", &pointedTo)
		if obs.Popups > 0 {
			f := model.NewFinding("E_NEW_TAB", url)
			f.Element = fmt.Sprintf("%s “%s”", el.Tag, el.Name)
			f.Detail["popups"] = obs.Extra["popups"]
			r.store.AddFinding(f)
		}
		r.popupFindings(url, obs)
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
