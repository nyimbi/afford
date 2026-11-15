// Package audit holds the page-level checks that need a live DOM but no
// interaction: rendered-text defects, broken images, unhandled promise
// rejections, keyboard reachability and the axe-core accessibility rules.
//
// Each check returns findings through model.NewFinding; severity and prose live
// in the catalogue, not here.
package audit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/model"
	"afford/sources"
)

// InitScript records promise rejections nothing handled. It must be installed
// before any page script runs.
const InitScript = `
(() => {
	if (window.__affordRejections) return;
	window.__affordRejections = [];
	window.addEventListener('unhandledrejection', (e) => {
		let r = e.reason, msg;
		try { msg = r && (r.stack || r.message) ? String(r.stack || r.message) : JSON.stringify(r); } catch (_) { msg = String(r); }
		window.__affordRejections.push(String(msg).slice(0, 400));
	});
})();
`

const textJS = `
() => {
	const skip = new Set(['CODE','PRE','TEXTAREA','SCRIPT','STYLE','NOSCRIPT','KBD','SAMP','TEMPLATE']);
	const checks = [
		['undefined', /(^|[^\w.])undefined([^\w]|$)/],
		['NaN', /(^|[^\w])NaN([^\w]|$)/],
		['object-object', /\[object Object\]/],
		['invalid-date', /Invalid Date/],
		['unrendered-template', /\{\{\s*[\w.$]+[^}]*\}\}|\$\{[\w.]+\}|<%=?\s*[\w.]+/],
		['stack-trace', /(Traceback \(most recent call last\)|\n\s+at\s+[\w$.<>]+\s*\(.+:\d+:\d+\)|\bat\s+\S+\s+\(\S+\.(js|ts|py):\d+)/],
	];
	const hits = {};
	const walker = document.createTreeWalker(document.body || document.documentElement, NodeFilter.SHOW_TEXT);
	let node;
	while ((node = walker.nextNode())) {
		const p = node.parentElement;
		if (!p || p.closest && p.closest('code,pre,textarea,script,style,noscript,kbd,samp,template')) continue;
		if (skip.has(p.tagName)) continue;
		const s = getComputedStyle(p);
		if (s.display === 'none' || s.visibility === 'hidden') continue;
		const text = node.nodeValue || '';
		if (!text.trim()) continue;
		for (const [kind, re] of checks) {
			if (!hits[kind] && re.test(text)) hits[kind] = text.trim().replace(/\s+/g, ' ').slice(0, 140);
		}
	}
	return Object.entries(hits).map(([kind, sample]) => ({kind, sample}));
}
`

const imagesJS = `
() => Array.from(document.images).filter((img) => {
	const s = getComputedStyle(img);
	if (s.display === 'none' || s.visibility === 'hidden') return false;
	if (!img.getAttribute('src') && !img.currentSrc) return false;
	if (img.loading === 'lazy' && !img.complete) return false;
	return img.complete && img.naturalWidth === 0;
}).slice(0, 20).map((img) => ({src: (img.currentSrc || img.getAttribute('src') || '').slice(0, 200), alt: (img.alt || '').slice(0, 80)}))
`

func eval[T any](page playwright.Page, expr string) (T, error) {
	var zero T
	raw, err := page.Evaluate(expr)
	if err != nil {
		return zero, err
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(blob, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// RenderedText finds values that leaked from code into prose.
func RenderedText(page playwright.Page, pageURL string) []model.Finding {
	hits, err := eval[[]struct{ Kind, Sample string }](page, textJS)
	if err != nil {
		return nil
	}
	var out []model.Finding
	for _, h := range hits {
		f := model.NewFinding("E_RENDERED_GARBAGE", pageURL)
		f.Element = h.Kind
		f.Detail["kind"] = h.Kind
		f.Detail["sample"] = h.Sample
		f.Evidence = []string{fmt.Sprintf("visible text contains %s: %q", h.Kind, h.Sample)}
		out = append(out, f)
	}
	return out
}

// BrokenImages reports images that finished loading with no pixels.
func BrokenImages(page playwright.Page, pageURL string) []model.Finding {
	hits, err := eval[[]struct{ Src, Alt string }](page, imagesJS)
	if err != nil {
		return nil
	}
	var out []model.Finding
	for _, h := range hits {
		f := model.NewFinding("E_BROKEN_IMAGE", pageURL)
		f.Element = "img " + h.Src
		f.Detail["src"] = h.Src
		f.Detail["alt"] = h.Alt
		f.Evidence = []string{"image completed with zero width: " + h.Src}
		out = append(out, f)
	}
	return out
}

// Rejections reports promise rejections no handler caught.
func Rejections(page playwright.Page, pageURL string) []model.Finding {
	list, err := eval[[]string](page, "() => window.__affordRejections || []")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []model.Finding
	for _, msg := range list {
		first := strings.SplitN(msg, "\n", 2)[0]
		if seen[first] {
			continue
		}
		seen[first] = true
		f := model.NewFinding("E_UNHANDLED_REJECTION", pageURL)
		f.Detail["error"] = first
		f.Evidence = []string{"unhandledrejection: " + first}
		out = append(out, f)
	}
	return out
}

// axeRules is the bounded subset afford runs. Rules another check already owns
// (label, document-title) are left out so one defect is one finding.
var axeRules = []string{
	"image-alt", "button-name", "link-name", "input-image-alt", "select-name",
	"color-contrast", "aria-valid-attr", "aria-valid-attr-value", "aria-required-attr",
	"aria-allowed-attr", "aria-hidden-focus", "html-has-lang", "html-lang-valid",
	"meta-viewport", "frame-title", "scrollable-region-focusable", "duplicate-id-aria",
	"form-field-multiple-labels", "tabindex", "role-img-alt", "nested-interactive",
}

type axeNode struct {
	Target []any  `json:"target"`
	HTML   string `json:"html"`
}

type axeViolation struct {
	ID      string    `json:"id"`
	Impact  string    `json:"impact"`
	Help    string    `json:"help"`
	HelpURL string    `json:"helpUrl"`
	Nodes   []axeNode `json:"nodes"`
}

// Axe injects axe-core and runs the bounded rule subset on the main document.
func Axe(page playwright.Page, pageURL string) ([]model.Finding, error) {
	if _, err := page.Evaluate(sources.AxeJS); err != nil {
		return nil, fmt.Errorf("inject axe: %w", err)
	}
	values, _ := json.Marshal(axeRules)
	raw, err := page.Evaluate(fmt.Sprintf(`async () => {
		const r = await axe.run(document, {runOnly: {type: 'rule', values: %s}, resultTypes: ['violations']});
		return r.violations.map(v => ({id: v.id, impact: v.impact, help: v.help, helpUrl: v.helpUrl,
			nodes: v.nodes.slice(0, 5).map(n => ({target: n.target, html: (n.html || '').slice(0, 200)}))}));
	}`, values))
	if err != nil {
		return nil, fmt.Errorf("axe.run: %w", err)
	}
	blob, _ := json.Marshal(raw)
	var violations []axeViolation
	if err := json.Unmarshal(blob, &violations); err != nil {
		return nil, err
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].ID < violations[j].ID })
	var out []model.Finding
	for _, v := range violations {
		code := "E_A11Y_MINOR"
		if v.Impact == "serious" || v.Impact == "critical" {
			code = "E_A11Y_SERIOUS"
		}
		f := model.NewFinding(code, pageURL)
		f.Element = v.ID
		f.Detail["rule"] = v.ID
		f.Detail["impact"] = v.Impact
		f.Detail["help"] = v.Help
		f.Detail["help_url"] = v.HelpURL
		f.Detail["nodes"] = len(v.Nodes)
		for _, n := range v.Nodes {
			f.Evidence = append(f.Evidence, fmt.Sprintf("%v: %s", n.Target, n.HTML))
		}
		out = append(out, f)
	}
	return out, nil
}
