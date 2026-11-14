// Package probe drives one element or one form and returns an observation.
// It never decides severity.
package probe

import (
	"fmt"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/browser"
	"afford/internal/config"
	"afford/internal/discover"
	"afford/internal/model"
	"afford/internal/payloads"
)

type Load struct {
	OK       bool
	Status   int
	FinalURL string
	Err      error
}

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
	response, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(float64(cfg.Limits.NavigationTimeoutMS)),
	})
	if err != nil {
		return Load{OK: false, FinalURL: page.URL(), Err: err}
	}
	browser.Settle(page, cfg)
	status := 0
	if response != nil {
		status = response.Status()
	}
	return Load{OK: true, Status: status, FinalURL: page.URL()}
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
	const hit = document.elementFromPoint(x, y);
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
		if candidate.Fingerprint == el.Fingerprint && candidate.Nth == el.Nth {
			selector := fmt.Sprintf(`[data-afford-idx="%d"]`, candidate.Idx)
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
) model.Observation {
	collector.Reset()
	load := Reset(page, cfg, url)
	if !load.OK {
		obs := model.Observation{URLBefore: url, URLAfter: page.URL()}
		obs.ActionError = load.Err.Error()
		obs.Extra = map[string]any{"load_failed": true}
		return obs
	}
	// Load-time events are the page's, not the action's: they are reported by the
	// capture pass, so the action observation starts from this baseline.
	baseline := collector.SnapshotCopy()
	beforeURL := page.URL()
	beforeSig := Signature(page)
	started := time.Now()
	occluded := false
	actionError := ""

	locator, _, ok := Locate(page, el)
	if !ok {
		actionError = "element not present after reload"
	} else {
		occluded = IsOccluded(locator)
		kind := payloads.InferKind(el)
		switch {
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
		browser.Settle(page, cfg)
	}

	afterSig := Signature(page)
	full := collector.SnapshotCopy()
	snapshot := since(baseline, full)
	popups := 0
	var popupURLs []string
	for _, other := range bctx.Pages() {
		if other == page {
			continue
		}
		popups++
		popupURLs = append(popupURLs, other.URL())
		_ = other.Close()
	}
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
	return obs
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
