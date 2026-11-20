// Package probe drives one element or one form and returns an observation.
// It never decides severity.
package probe

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nyimbi/afford/internal/browser"
	"github.com/nyimbi/afford/internal/config"
	"github.com/nyimbi/afford/internal/discover"
	"github.com/nyimbi/afford/internal/model"
	"github.com/nyimbi/afford/internal/payloads"
	"github.com/nyimbi/afford/internal/sanitize"
	"github.com/nyimbi/afford/internal/uploads"
)

type Load struct {
	OK        bool
	Status    int
	FinalURL  string
	LoadMS    int64    // browser loadEventEnd
	SettledMS int64    // wall clock from navigation to settled
	NotReady  []string // readiness conditions that never held
	// Scrolled counts scroll steps that grew the page; ScrollTruncated means
	// the step bound ended it while the page was still growing.
	Scrolled        int
	ScrollTruncated bool
	// RateLimited means the navigation got a 429 and was retried after the
	// server's Retry-After (bounded); StillLimited means the retry got 429 too.
	RateLimited  bool
	StillLimited bool
	Err          error
}

const navTimingJS = `() => {
	const entries = performance.getEntriesByType ? performance.getEntriesByType('navigation') : [];
	const n = entries && entries[0];
	if (!n) return 0;
	return Math.round(n.loadEventEnd || n.domContentLoadedEventEnd || 0);
}`

type Anomaly struct {
	Element model.Element
	Label   string
	Obs     model.Observation
}

type SweepResult struct {
	Anomalies []Anomaly
	Payloads  int
	Inputs    int
}

type FormMeta struct {
	Mode            string
	Accepted        bool
	RequiredPresent bool
	Filled          int
	Action          string
	Method          string
}

func Reset(page playwright.Page, cfg config.Config, url string) Load {
	return reset(page, cfg, url, true)
}

// ResetForProbe loads the page for one action. It skips scroll-to-load: the
// capture pass already scrolled this page and scanned what appeared, and a probe
// only needs its target located. A control that is genuinely behind lazy loading
// is found by the fallback in ProbeControl, which pays for the scroll only when
// the first locate misses.
func ResetForProbe(page playwright.Page, cfg config.Config, url string) Load {
	return reset(page, cfg, url, false)
}

func reset(page playwright.Page, cfg config.Config, url string, scroll bool) Load {
	started := time.Now()
	response, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(float64(cfg.Limits.NavigationTimeoutMS)),
	})
	if err != nil {
		return Load{
			OK: false, FinalURL: page.URL(), Err: err,
			SettledMS: time.Since(started).Milliseconds(),
		}
	}
	limited, still := false, false
	for attempt := 0; attempt < 2 && response != nil && response.Status() == 429; attempt++ {
		limited = true
		wait := 2 * time.Second
		if v := response.Headers()["retry-after"]; v != "" {
			if secs, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil && secs >= 0 {
				wait = time.Duration(secs) * time.Second
			}
		}
		if wait > 8*time.Second {
			wait = 8 * time.Second
		}
		time.Sleep(wait)
		again, gotoErr := page.Goto(url, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(float64(cfg.Limits.NavigationTimeoutMS)),
		})
		if gotoErr != nil {
			break
		}
		response = again
	}
	if limited && response != nil && response.Status() == 429 {
		still = true
	}
	notReady := browser.SettleReady(page, cfg)
	settled := time.Since(started).Milliseconds()
	scrolled, truncated := 0, false
	if scroll {
		scrolled, truncated = browser.ScrollToLoad(page, cfg)
	}
	status := 0
	if response != nil {
		status = response.Status()
	}
	loadMS := int64(0)
	if raw, err := page.Evaluate(navTimingJS); err == nil {
		loadMS = asInt64(raw)
	}
	return Load{
		OK: true, Status: status, FinalURL: page.URL(),
		LoadMS: loadMS, SettledMS: settled, NotReady: notReady,
		Scrolled: scrolled, ScrollTruncated: truncated,
		RateLimited: limited, StillLimited: still,
	}
}

const signatureJS = `
() => {
	const body = document.body;
	const text = body ? (body.innerText || '') : '';
	let h = 2166136261 >>> 0;
	const mix = (s) => { for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619) >>> 0; } };
	mix(text);
	const els = document.querySelectorAll('*');
	mix('|' + els.length);
	for (const e of els) {
		mix(',' + (e.className || '') + ':' + (e.getAttribute('aria-expanded') || '')
			+ ':' + (e.getAttribute('hidden') || '') + ':' + (e.getAttribute('open') || '')
			+ ':' + (e.getAttribute('aria-selected') || ''));
	}
	return h.toString(16);
}
`

func Signature(page playwright.Page) string {
	raw, err := page.Evaluate(signatureJS)
	if err != nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return fmt.Sprint(raw)
}

const occludedJS = `
(el) => {
	const r = el.getBoundingClientRect();
	if (r.width < 1 || r.height < 1) return true;
	const x = r.left + r.width / 2;
	const y = r.top + r.height / 2;
	if (x < 0 || y < 0 || x > window.innerWidth || y > window.innerHeight) return false;
	const root = el.getRootNode();
	const hit = (root.elementFromPoint ? root : document).elementFromPoint(x, y);
	return !(hit && (hit === el || el.contains(hit) || hit.contains(el)));
}
`

// Locate re-scans after the reload and finds the same element by fingerprint and
// occurrence. Scanning stamps data-afford-idx, so the locator is exact.
func Locate(page playwright.Page, el model.Element) (playwright.Locator, model.ScanResult, bool) {
	fresh, err := discover.Scan(page)
	if err != nil {
		return nil, model.ScanResult{}, false
	}
	for _, candidate := range fresh.Controls {
		if candidate.Fingerprint == el.Fingerprint && candidate.Nth == el.Nth && candidate.Frame == el.Frame {
			selector := fmt.Sprintf(`[data-afford-idx="%d"]`, candidate.Idx)
			if candidate.Frame > 0 {
				frames := page.Frames()
				if candidate.Frame >= len(frames) {
					return nil, fresh, false
				}
				return frames[candidate.Frame].Locator(selector), fresh, true
			}
			return page.Locator(selector), fresh, true
		}
	}
	return nil, fresh, false
}

func IsOccluded(locator playwright.Locator) bool {
	_ = locator.ScrollIntoViewIfNeeded(playwright.LocatorScrollIntoViewIfNeededOptions{
		Timeout: playwright.Float(1500),
	})
	raw, err := locator.Evaluate(occludedJS, nil)
	if err != nil {
		return false
	}
	flag, _ := raw.(bool)
	return flag
}

func effect(beforeURL, afterURL string, collector browser.Snapshot, changed bool, popups int) string {
	if discover.Normalize(beforeURL) != discover.Normalize(afterURL) {
		return "navigated"
	}
	switch {
	case len(collector.Dialogs) > 0:
		return "dialog"
	case popups > 0:
		return "popup"
	case changed:
		return "dom"
	case len(collector.Requests) > 0:
		return "network"
	}
	return "none"
}

func documentStatus(collector browser.Snapshot) int {
	status := 0
	for _, r := range collector.Requests {
		if r.Kind == "document" && r.Status != 0 {
			status = r.Status
		}
	}
	return status
}

func ProbeControl(
	bctx playwright.BrowserContext,
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	url string,
	el model.Element,
	popupShotDir string,
) model.Observation {
	collector.Reset()
	load := ResetForProbe(page, cfg, url)
	if !load.OK {
		obs := model.Observation{URLBefore: url, URLAfter: page.URL()}
		obs.ActionError = load.Err.Error()
		obs.Extra = map[string]any{"load_failed": true}
		return obs
	}
	// Load-time events are the page's, not the action's: they are reported by the
	// capture pass, so the action observation starts from this baseline.
	if el.Via != nil {
		// A revealed control exists only after its trigger ran; replay it.
		if vl, _, ok := Locate(page, *el.Via); ok {
			_, _ = perform(vl, *el.Via, cfg)
			browser.SettleAction(page, collector, Signature, cfg)
		}
	}
	locator, pre, ok := Locate(page, el)
	if !ok {
		// Not present without scrolling: lazy content. Pay for the scroll, replay
		// the trigger if the control is revealed, and try once more.
		browser.ScrollToLoad(page, cfg)
		browser.Settle(page, cfg)
		if el.Via != nil {
			if vl, _, viaOK := Locate(page, *el.Via); viaOK {
				_, _ = perform(vl, *el.Via, cfg)
				browser.SettleAction(page, collector, Signature, cfg)
			}
		}
		locator, pre, ok = Locate(page, el)
	}
	baseline := collector.SnapshotCopy()
	beforeURL := page.URL()
	beforeSig := Signature(page)
	started := time.Now()
	occluded := false
	actionError := ""
	obsUploadError := ""

	if !ok {
		actionError = "element not present after reload"
	} else {
		occluded = IsOccluded(locator)
		actionError, obsUploadError = perform(locator, el, cfg)
		browser.SettleAction(page, collector, Signature, cfg)
	}

	afterSig := Signature(page)
	full := collector.SnapshotCopy()
	snapshot := since(baseline, full)
	extras := make([]playwright.Page, 0, 2)
	popupURLs := make([]string, 0, 2)
	for _, other := range bctx.Pages() {
		if other == page {
			continue
		}
		extras = append(extras, other)
		popupURLs = append(popupURLs, other.URL())
	}
	popups := len(extras)
	obs := model.Observation{
		URLBefore:       url,
		URLAfter:        page.URL(),
		Status:          documentStatus(full),
		Navigated:       discover.Normalize(beforeURL) != discover.Normalize(page.URL()),
		Dialogs:         snapshot.Dialogs,
		ConsoleErrors:   snapshot.ConsoleErrors,
		ConsoleWarnings: snapshot.ConsoleWarnings,
		PageErrors:      snapshot.PageErrors,
		Requests:        snapshot.Requests,
		Effect:          effect(beforeURL, page.URL(), snapshot, beforeSig != afterSig, popups),
		ActionError:     actionError,
		Occluded:        occluded,
		Popups:          popups,
		DurationMS:      int(time.Since(started).Milliseconds()),
		Extra:           map[string]any{"popups": popupURLs},
	}
	if obsUploadError != "" {
		obs.Extra["upload_error"] = obsUploadError
	}
	if el.Via == nil && ok && cfg.Run.Reveal > 0 && !obs.Navigated && popups == 0 {
		if revealed := Revealed(page, pre, el, cfg.Run.Reveal); len(revealed) > 0 {
			obs.Extra["revealed"] = revealed
		}
	}
	if cfg.Popups.Mode == "capture" && len(extras) > 0 {
		obs.PopupResults = capturePopups(extras, cfg, popupShotDir)
	}
	for _, extra := range extras {
		_ = extra.Close()
	}
	// A link's destination can be a soft 404: HTTP 200 with "page not found"
	// text. Sample the main content, not the whole body: site chrome routinely
	// contains labels like "404" or "missing" that say nothing about this page.
	if obs.Navigated {
		const mainText = `() => {
			const main = document.querySelector('main, [role=main], #main, #content');
			const el = main || document.body;
			return el ? (el.innerText || '').slice(0, 600) : '';
		}`
		if raw, err := page.Evaluate(mainText); err == nil {
			if sample, ok := raw.(string); ok {
				obs.Extra["text_sample"] = sample
			}
		}
	}
	return obs
}

// asInt64 accepts the numeric shapes the Playwright decoder can hand back.
// perform applies the action a control's kind calls for: attach a file, choose
// an option, tick a box, or click. It returns the action error and the upload
// error separately because they mean different things to the oracles.
func perform(locator playwright.Locator, el model.Element, cfg config.Config) (actionError, obsUploadError string) {
	kind := payloads.InferKind(el)
	switch {
	case el.Type == "file":
		file, ferr := uploads.Resolve(el, cfg.Uploads)
		if ferr != nil {
			obsUploadError = ferr.Error()
			break
		}
		if err := locator.SetInputFiles([]playwright.InputFile{{
			Name: file.Name, MimeType: file.MimeType, Buffer: file.Buffer,
		}}); err != nil {
			obsUploadError = err.Error()
		}
	case el.Tag == "select":
		if value := payloads.ValidValue(el); value != "" {
			_, err := locator.SelectOption(playwright.SelectOptionValues{Values: &[]string{value}},
				playwright.LocatorSelectOptionOptions{Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS))})
			if err != nil {
				actionError = err.Error()
			}
		}
	case kind == "checkbox" || kind == "radio":
		if err := locator.Check(playwright.LocatorCheckOptions{
			Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
		}); err != nil {
			actionError = err.Error()
		}
	case kind == "range":
		value := payloads.ValidValue(el)
		if value == "" {
			value = "1"
		}
		if err := locator.Fill(value, playwright.LocatorFillOptions{
			Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
		}); err != nil {
			actionError = err.Error()
		}
	default:
		if err := locator.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
		}); err != nil {
			actionError = err.Error()
		}
	}
	return actionError, obsUploadError
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

const popupStatusJS = `() => {
	const entries = performance.getEntriesByType ? performance.getEntriesByType('navigation') : [];
	const n = entries && entries[0];
	return n && n.responseStatus ? n.responseStatus : 0;
}`

// capturePopups settles and captures each popup before closing it. The event
// collector is attached after the page exists, so very early console output is
// missed; everything after load is attributed to the opener's finding.
func capturePopups(pages []playwright.Page, cfg config.Config, shotDir string) []model.PopupResult {
	limit := cfg.Popups.Max
	if limit <= 0 {
		limit = 5
	}
	out := make([]model.PopupResult, 0, len(pages))
	for i, popup := range pages {
		if i >= limit {
			break
		}
		rawURL := popup.URL()
		result := model.PopupResult{URL: sanitize.URL(rawURL)}
		if !discover.InScope(rawURL, cfg) {
			out = append(out, result)
			continue
		}
		collector := &browser.Collector{}
		collector.Bind(popup)
		browser.Settle(popup, cfg)
		result.URL = sanitize.URL(rawURL)
		scanResult, err := discover.Scan(popup)
		if err == nil {
			result.Title = scanResult.Title
			if raw, evalErr := popup.Evaluate(popupStatusJS); evalErr == nil {
				result.Status = int(asInt64(raw))
			}
			snapshot := collector.SnapshotCopy()
			visible := 0
			for _, el := range scanResult.Controls {
				if el.Visible {
					visible++
				}
			}
			result.Observation = model.Observation{
				URLBefore:       result.URL,
				URLAfter:        popup.URL(),
				Title:           scanResult.Title,
				Status:          result.Status,
				BodyLen:         scanResult.BodyLen,
				Requests:        snapshot.Requests,
				ConsoleErrors:   snapshot.ConsoleErrors,
				ConsoleWarnings: snapshot.ConsoleWarnings,
				PageErrors:      snapshot.PageErrors,
				Dialogs:         snapshot.Dialogs,
				Extra: map[string]any{
					"overflow_x":        scanResult.OverflowX,
					"scroll_width":      scanResult.ScrollWidth,
					"viewport_width":    scanResult.ViewportWidth,
					"interactive_count": visible,
					"text_sample":       "",
				},
			}
			if shotDir != "" {
				name := fmt.Sprintf("popup-%s.png", shortHash(result.URL))
				if path, shotErr := browser.Screenshot(popup, filepath.Join(shotDir, name), cfg.ScreenshotMasks()); shotErr == nil {
					result.Screenshot = path
				}
			}
		}
		out = append(out, result)
	}
	return out
}

func shortHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// since returns the events that appeared after the baseline was taken.
func since(baseline, after browser.Snapshot) browser.Snapshot {
	var out browser.Snapshot
	if len(after.PageErrors) > len(baseline.PageErrors) {
		out.PageErrors = after.PageErrors[len(baseline.PageErrors):]
	}
	if len(after.ConsoleErrors) > len(baseline.ConsoleErrors) {
		out.ConsoleErrors = after.ConsoleErrors[len(baseline.ConsoleErrors):]
	}
	if len(after.ConsoleWarnings) > len(baseline.ConsoleWarnings) {
		out.ConsoleWarnings = after.ConsoleWarnings[len(baseline.ConsoleWarnings):]
	}
	if len(after.Requests) > len(baseline.Requests) {
		out.Requests = after.Requests[len(baseline.Requests):]
	}
	if len(after.Dialogs) > len(baseline.Dialogs) {
		out.Dialogs = after.Dialogs[len(baseline.Dialogs):]
	}
	return out
}

func SweepInputs(
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	url string,
	controls []model.Element,
) SweepResult {
	var result SweepResult
	collector.Reset()
	if load := Reset(page, cfg, url); !load.OK {
		return result
	}
	for _, el := range controls {
		if !el.Visible || el.Disabled || el.ReadOnly || !el.IsTextInput() {
			continue
		}
		switch payloads.InferKind(el) {
		case "text", "textarea", "email", "url", "tel", "number", "search", "password", "username":
		default:
			continue
		}
		locator, _, ok := Locate(page, el)
		if !ok {
			continue
		}
		result.Inputs++
		original, _ := locator.InputValue()
		for _, p := range payloads.Sweep(el, cfg.Run.Depth) {
			before := collector.SnapshotCopy()
			overflowBefore := overflow(page)
			if err := locator.Fill(p.Value, playwright.LocatorFillOptions{
				Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
			}); err != nil {
				continue
			}
			_, _ = locator.Evaluate("el => el.blur()", nil)
			page.WaitForTimeout(90)
			after := collector.SnapshotCopy()
			result.Payloads++

			var newErrors []string
			if len(after.PageErrors) > len(before.PageErrors) {
				newErrors = after.PageErrors[len(before.PageErrors):]
			}
			var newConsole []string
			if len(after.ConsoleErrors) > len(before.ConsoleErrors) {
				newConsole = after.ConsoleErrors[len(before.ConsoleErrors):]
			}
			var newRequests []model.RequestRecord
			if len(after.Requests) > len(before.Requests) {
				newRequests = after.Requests[len(before.Requests):]
			}
			var newDialogs []string
			if len(after.Dialogs) > len(before.Dialogs) {
				newDialogs = after.Dialogs[len(before.Dialogs):]
			}
			overflowNow := overflow(page) && !overflowBefore
			if len(newErrors) == 0 && len(newConsole) == 0 && len(newRequests) == 0 &&
				len(newDialogs) == 0 && !overflowNow {
				continue
			}
			result.Anomalies = append(result.Anomalies, Anomaly{
				Element: el,
				Label:   p.Label,
				Obs: model.Observation{
					URLBefore:     url,
					URLAfter:      page.URL(),
					PageErrors:    newErrors,
					ConsoleErrors: newConsole,
					Requests:      newRequests,
					Dialogs:       newDialogs,
					Effect:        "none",
					Extra: map[string]any{
						"payload_label": p.Label,
						"payload":       p.Value,
						"element":       el.Name,
						"overflow_x":    overflowNow,
					},
				},
			})
			_ = locator.Fill(original, playwright.LocatorFillOptions{
				Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
			})
		}
	}
	return result
}

func overflow(page playwright.Page) bool {
	raw, err := page.Evaluate("() => document.documentElement.scrollWidth > window.innerWidth + 2")
	if err != nil {
		return false
	}
	flag, _ := raw.(bool)
	return flag
}

// FormFindings applies the form-specific rules. It lives here because this is
// the only place that knows which mode was run and whether the submit reached
// the server.
func FormFindings(obs model.Observation, meta FormMeta, url string) []model.Finding {
	var out []model.Finding
	var five []int
	for _, rec := range obs.Requests {
		if rec.Status >= 500 {
			five = append(five, rec.Status)
		}
	}
	detail := map[string]any{
		"mode": meta.Mode, "accepted": meta.Accepted, "action": meta.Action,
	}
	if meta.Mode == "invalid" {
		if len(five) > 0 {
			f := model.NewFinding("E_500_ON_INVALID", url)
			f.Detail = detail
			f.Detail["statuses"] = five
			out = append(out, f)
		}
		if meta.RequiredPresent && meta.Accepted {
			f := model.NewFinding("E_VALIDATION_NOT_ENFORCED", url)
			f.Detail = detail
			out = append(out, f)
		}
	}
	if meta.Mode == "valid" {
		if len(five) > 0 {
			f := model.NewFinding("E_500_ON_VALID", url)
			f.Detail = detail
			f.Detail["statuses"] = five
			out = append(out, f)
		}
		if obs.Effect == "none" && len(obs.ErrorRequests()) == 0 && obs.ActionError == "" {
			f := model.NewFinding("E_FORM_SUCCESS_UNVERIFIED", url)
			f.Detail = detail
			out = append(out, f)
		}
	}
	return out
}

func ProbeForm(
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	url string,
	form model.FormSpec,
	controls map[int]model.Element,
	mode string,
) (model.Observation, FormMeta) {
	meta := FormMeta{Mode: mode, Action: form.Action, Method: form.Method}
	collector.Reset()
	if load := Reset(page, cfg, url); !load.OK {
		obs := model.Observation{URLBefore: url}
		obs.ActionError = load.Err.Error()
		return obs, meta
	}
	baseline := collector.SnapshotCopy()
	beforeURL := page.URL()
	beforeSig := Signature(page)
	started := time.Now()
	actionError := ""

	for _, idx := range form.ControlIdx {
		el, ok := controls[idx]
		if !ok || !el.Visible || el.Disabled {
			continue
		}
		if el.Required && el.IsTextInput() {
			meta.RequiredPresent = true
		}
		locator, _, found := Locate(page, el)
		if !found {
			continue
		}
		if mode == "invalid" {
			if payloads.EmptyRequired(el) && el.IsTextInput() {
				if err := locator.Fill("", playwright.LocatorFillOptions{
					Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
				}); err == nil {
					meta.Filled++
				}
			}
			continue
		}
		if el.Type == "file" {
			if file, ferr := uploads.Resolve(el, cfg.Uploads); ferr == nil {
				if err := locator.SetInputFiles([]playwright.InputFile{{
					Name: file.Name, MimeType: file.MimeType, Buffer: file.Buffer,
				}}); err == nil {
					meta.Filled++
				}
			}
			continue
		}
		kind := payloads.InferKind(el)
		value := payloads.ValidValue(el)
		switch {
		case kind == "checkbox" || kind == "radio":
			_ = locator.Check(playwright.LocatorCheckOptions{
				Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
			})
		case el.Tag == "select":
			if value != "" {
				_, _ = locator.SelectOption(playwright.SelectOptionValues{Values: &[]string{value}},
					playwright.LocatorSelectOptionOptions{Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS))})
			}
		case el.IsTextInput() && value != "":
			if err := locator.Fill(value, playwright.LocatorFillOptions{
				Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
			}); err == nil {
				meta.Filled++
			}
		}
	}

	if len(form.SubmitIdx) > 0 {
		submitEl, ok := controls[form.SubmitIdx[0]]
		if !ok {
			actionError = "submit control missing from inventory"
		} else if locator, _, found := Locate(page, submitEl); !found {
			actionError = "submit control vanished after reload"
		} else if err := locator.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(float64(cfg.Limits.ProbeTimeoutMS)),
		}); err != nil {
			actionError = err.Error()
		}
	} else if err := page.Keyboard().Press("Enter"); err != nil {
		actionError = err.Error()
	}
	browser.Settle(page, cfg)

	afterSig := Signature(page)
	full := collector.SnapshotCopy()
	snapshot := since(baseline, full)
	obs := model.Observation{
		URLBefore:       url,
		URLAfter:        page.URL(),
		Status:          documentStatus(full),
		Navigated:       discover.Normalize(beforeURL) != discover.Normalize(page.URL()),
		Dialogs:         snapshot.Dialogs,
		ConsoleErrors:   snapshot.ConsoleErrors,
		ConsoleWarnings: snapshot.ConsoleWarnings,
		PageErrors:      snapshot.PageErrors,
		Requests:        snapshot.Requests,
		Effect:          effect(beforeURL, page.URL(), snapshot, beforeSig != afterSig, 0),
		ActionError:     actionError,
		DurationMS:      int(time.Since(started).Milliseconds()),
	}
	meta.Accepted = obs.Navigated
	for _, r := range obs.Requests {
		switch strings.ToUpper(r.Method) {
		case "POST", "PUT", "PATCH", "DELETE":
			if r.Status > 0 && r.Status < 400 {
				meta.Accepted = true
			}
		}
	}
	return obs, meta
}

// Revealed rescans after an action and returns the visible, enabled controls
// that were not there before: the items of a menu, the buttons of a dialog.
// Each is stamped with its trigger so a later probe can replay the reveal.
func Revealed(page playwright.Page, before model.ScanResult, trigger model.Element, limit int) []model.Element {
	after, err := discover.Scan(page)
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, c := range before.Controls {
		// A control that existed but was hidden is the usual case: a menu that
		// toggles `hidden` is revealed, not added.
		if c.Visible {
			known[revealKey(c)] = true
		}
	}
	via := trigger
	via.Via = nil
	var out []model.Element
	for _, c := range after.Controls {
		if known[revealKey(c)] || !c.Visible || c.Disabled || c.IsSubmit() ||
			(c.ID != "" && c.ID == trigger.ID) {
			continue
		}
		c.Via = &via
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func revealKey(c model.Element) string {
	return fmt.Sprintf("%s|%d|%d", c.Fingerprint, c.Nth, c.Frame)
}
