// Package pass holds the page-level passes both entry points share: the crawler
// (internal/run) and the on-demand checker (internal/check). One definition of
// "what we check on a loaded page" keeps the two from drifting apart.
package pass

import (
	"fmt"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/audit"
	"afford/internal/browser"
	"afford/internal/config"
	"afford/internal/model"
	"afford/internal/oracles"
	"afford/internal/probe"
)

// Visible filters a scan to controls a user could see.
func Visible(controls []model.Element) []model.Element {
	out := make([]model.Element, 0, len(controls))
	for _, c := range controls {
		if c.Visible {
			out = append(out, c)
		}
	}
	return out
}

// Capture evaluates a freshly loaded page: transport status, console and
// exceptions, render health, and the inventory oracles over its controls. The
// returned observation is what the capture-time findings were computed from.
func Capture(
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	pageURL, finalURL string,
	authRedirect bool,
	load probe.Load,
	scan model.ScanResult,
) (model.Observation, []model.Finding) {
	visible := Visible(scan.Controls)
	textSample, _ := page.Evaluate("() => (document.body ? document.body.innerText : '').slice(0, 1500)")
	sample, _ := textSample.(string)
	status := 0
	if load.OK {
		status = load.Status
	}
	snap := collector.SnapshotCopy()
	obs := model.Observation{
		URLBefore:       pageURL,
		URLAfter:        finalURL,
		Title:           scan.Title,
		Status:          status,
		BodyLen:         scan.BodyLen,
		Requests:        snap.Requests,
		ConsoleErrors:   snap.ConsoleErrors,
		ConsoleWarnings: snap.ConsoleWarnings,
		PageErrors:      snap.PageErrors,
		Dialogs:         snap.Dialogs,
		Extra: map[string]any{
			"overflow_x":        scan.OverflowX,
			"scroll_width":      scan.ScrollWidth,
			"viewport_width":    scan.ViewportWidth,
			"text_sample":       sample,
			"interactive_count": len(visible),
			"auth_redirect":     authRedirect,
		},
	}
	findings := oracles.Evaluate(obs, pageURL, "capture", nil)
	for i := range findings {
		if len(findings[i].Evidence) == 0 {
			findings[i].Evidence = oracles.FallbackEvidence(obs)
		}
	}
	findings = append(findings, oracles.Unlabeled(visible, pageURL)...)
	findings = append(findings, oracles.EmptyHref(visible, pageURL)...)
	findings = append(findings, oracles.OptionsAudit(visible, pageURL)...)
	if load.RateLimited {
		f := model.NewFinding("E_RATE_LIMITED", pageURL)
		f.Detail["still_limited"] = load.StillLimited
		findings = append(findings, f)
	}
	findings = append(findings, audit.RenderedText(page, pageURL)...)
	findings = append(findings, audit.BrokenImages(page, pageURL)...)
	findings = append(findings, audit.Rejections(page, pageURL)...)
	if cfg.Run.A11y {
		if axe, err := audit.Axe(page, pageURL); err == nil {
			findings = append(findings, axe...)
		}
	}
	if cfg.Run.Keyboard {
		findings = append(findings, audit.Keyboard(page, pageURL, tabbable(visible))...)
		_, _ = page.Evaluate("() => { if (document.activeElement) document.activeElement.blur(); window.scrollTo(0, 0); }")
	}
	if len(load.NotReady) > 0 {
		f := model.NewFinding("E_NOT_READY", pageURL)
		f.Detail["conditions"] = load.NotReady
		f.Evidence = append([]string(nil), load.NotReady...)
		findings = append(findings, f)
	}
	return obs, findings
}

// confirmable are the findings a single unlucky load can produce: a handler
// that had not bound yet, a request that timed out once, an element that
// shifted under the pointer. Deterministic facts (an alert text, an exposed
// UUID) are not re-probed.
var confirmable = map[string]bool{
	"E_DEAD_CONTROL": true, "E_OCCLUDED": true, "E_CONTROL_ERROR": true,
	"E_CLIENT_ERROR_ON_ACTION": true, "E_500_ON_ACTION": true,
	"E_FAILED_REQUEST_ON_ACTION": true, "E_NAV_TO_NONOK": true,
	"E_DEAD_LINK": true, "E_UPLOAD_FAILED": true,
}

func identity(f model.Finding) string { return f.Code + "|" + f.Element }

func controlFindings(obs model.Observation, url string, el model.Element) []model.Finding {
	pointedTo := el
	fs := oracles.Evaluate(obs, url, "action", &pointedTo)
	for i := range fs {
		fs[i].Payload = "effect=" + obs.Effect
		if el.Via != nil {
			fs[i].Detail["revealed_by"] = fmt.Sprintf("%s “%s”", el.Via.Tag, el.Via.Name)
		}
		if len(fs[i].Evidence) == 0 {
			fs[i].Evidence = oracles.FallbackEvidence(obs)
		}
	}
	return fs
}

// Control probes one control and, when the result is the kind a single bad
// load can produce, probes it once more from a fresh load. A finding that does
// not reproduce is kept but marked unconfirmed so it cannot fail a gate.
func Control(
	bctx playwright.BrowserContext,
	page playwright.Page,
	collector *browser.Collector,
	cfg config.Config,
	url string,
	el model.Element,
	popupDir string,
) (model.Observation, []model.Finding) {
	obs := probe.ProbeControl(bctx, page, collector, cfg, url, el, popupDir)
	fs := controlFindings(obs, url, el)
	if !cfg.Run.Confirm {
		return obs, fs
	}
	suspect := false
	for _, f := range fs {
		if confirmable[f.Code] {
			suspect = true
		}
	}
	if !suspect {
		return obs, fs
	}
	again := controlFindings(probe.ProbeControl(bctx, page, collector, cfg, url, el, popupDir), url, el)
	second := map[string]bool{}
	for _, f := range again {
		second[identity(f)] = true
	}
	first := map[string]bool{}
	for i := range fs {
		first[identity(fs[i])] = true
		if !confirmable[fs[i].Code] {
			continue
		}
		if second[identity(fs[i])] {
			fs[i].Confidence = "confirmed"
		} else {
			fs[i].Confidence = "unconfirmed"
			fs[i].Evidence = append(fs[i].Evidence, "did not reproduce on a second load")
		}
	}
	// Seen only on the second load: equally unreliable.
	for _, f := range again {
		if confirmable[f.Code] && !first[identity(f)] {
			f.Confidence = "unconfirmed"
			f.Evidence = append(f.Evidence, fmt.Sprintf("appeared on the second load only"))
			fs = append(fs, f)
		}
	}
	return obs, fs
}

// RevealedOf returns the controls a probe found uncovered by its click.
func RevealedOf(obs model.Observation) []model.Element {
	list, _ := obs.Extra["revealed"].([]model.Element)
	return list
}

// tabbable are the controls a keyboard user is expected to reach: visible,
// enabled, not hidden inputs, not inside a frame (Tab order crosses frames
// differently and is not judged here).
func tabbable(controls []model.Element) []model.Element {
	var out []model.Element
	for _, c := range controls {
		if c.Disabled || c.Frame != 0 || c.Type == "hidden" || c.Via != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}
