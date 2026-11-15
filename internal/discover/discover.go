// Package discover turns URLs into identities and the DOM into an inventory.
package discover

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/mxschmitt/playwright-go"

	"afford/internal/config"
	"afford/internal/model"
)

var tracking = map[string]bool{
	"utm_source": true, "utm_medium": true, "utm_campaign": true, "utm_term": true,
	"utm_content": true, "gclid": true, "fbclid": true, "msclkid": true,
	"mc_cid": true, "mc_eid": true, "ref": true, "referrer": true,
	"_rsc": true, "_next": true, "cachebust": true, "cb": true, "timestamp": true,
}

var (
	uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	intRe  = regexp.MustCompile(`^\d{1,18}$`)
	hexRe  = regexp.MustCompile(`(?i)^[0-9a-f]{8,64}$`)
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$|^\d{8}$`)
	slash  = regexp.MustCompile(`/{2,}`)
)

// ScanJS returns the controls, forms and page facts in one round trip.
const ScanJS = `
() => {
	const SEL = 'a[href],button,input,select,textarea,summary,[role=button],[role=link],[role=tab],[role=menuitem],[role=checkbox],[role=radio],[role=switch],[contenteditable=true],[onclick]';
	const txt = (el) => (el && (el.innerText || el.textContent) || '').replace(/\s+/g, ' ').trim();
	const vis = (el) => {
		const s = getComputedStyle(el);
		if (s.display === 'none' || s.visibility === 'hidden' || s.visibility === 'collapse') return false;
		if (Number(s.opacity) === 0) return false;
		const r = el.getBoundingClientRect();
		return r.width > 1 && r.height > 1;
	};
	const acc = (el) => {
		const aria = (el.getAttribute('aria-label') || '').trim();
		if (aria) return aria;
		const ref = el.getAttribute('aria-labelledby');
		if (ref) {
			const t = ref.split(/\s+/).map(id => txt(document.getElementById(id))).filter(Boolean).join(' ').trim();
			if (t) return t;
		}
		if (el.labels && el.labels.length) {
			const t = Array.from(el.labels).map(l => txt(l)).filter(Boolean).join(' ').trim();
			if (t) return t;
		}
		const t = txt(el) || (el.value || '') || (el.getAttribute('placeholder') || '') || (el.getAttribute('title') || '');
		return String(t).trim().slice(0, 160);
	};
	const forms = Array.from(document.forms);
	const out = [];
	const seen = {};
	for (const el of Array.from(document.querySelectorAll(SEL))) {
		const declared = (el.getAttribute('type') || '').toLowerCase();
		if (el.tagName === 'INPUT' && declared === 'hidden') continue;
		const tag = el.tagName.toLowerCase();
		const role = (el.getAttribute('role') || '').toLowerCase() || null;
		const name = acc(el);
		const fp = tag + '|' + (role || '') + '|' + name;
		seen[fp] = seen[fp] || 0;
		const nth = seen[fp]++;
		const r = el.getBoundingClientRect();
		const type = declared || (tag === 'input' ? 'text' : (tag === 'button' ? 'submit' : null));
		el.setAttribute('data-afford-idx', String(out.length));
		const attrs = {};
		for (const a of ['min','max','step','minlength','maxlength','pattern','autocomplete','inputmode']) {
			const v = el.getAttribute(a);
			if (v !== null) attrs[a] = v;
		}
		out.push({
			idx: out.length, tag, role, name, fingerprint: fp, nth,
			type: type ? String(type).toLowerCase() : '',
			id: el.id || '', name_attr: el.getAttribute('name') || '',
			placeholder: el.getAttribute('placeholder') || '',
			href: el.getAttribute('href') || '', target: el.getAttribute('target') || '',
			disabled: !!el.disabled || el.getAttribute('aria-disabled') === 'true',
			required: !!el.required || el.getAttribute('aria-required') === 'true',
			readonly: !!el.readOnly,
			visible: vis(el),
			rect: [r.x, r.y, r.width, r.height],
			form_index: el.form ? forms.indexOf(el.form) : null,
			options: tag === 'select' ? Array.from(el.options).map(o => ({value: o.value, label: txt(o), disabled: !!o.disabled})) : [],
			label_count: el.labels ? el.labels.length : 0,
			in_form: !!el.form,
			src: tag === 'a' ? 'link' : (['input','select','textarea'].includes(tag) ? 'input' : 'button'),
			attrs
		});
	}
	return {
		url: location.href, title: document.title || '',
		controls: out,
		forms: forms.map((f, i) => ({
			index: i, action: f.getAttribute('action') || '',
			method: (f.getAttribute('method') || 'get').toLowerCase(),
			name: f.getAttribute('name') || f.id || '',
			control_idxs: out.filter(c => c.form_index === i).map(c => c.idx),
			submit_idxs: out.filter(c => c.form_index === i && ['submit','image'].includes(c.type || '')).map(c => c.idx)
		})),
		body_len: document.body ? (document.body.innerText || '').trim().length : 0,
		node_count: document.querySelectorAll('*').length,
		overflow_x: document.documentElement.scrollWidth > window.innerWidth + 2,
		scroll_width: document.documentElement.scrollWidth,
		viewport_width: window.innerWidth
	};
}
`

func Scan(page playwright.Page) (model.ScanResult, error) {
	raw, err := page.Evaluate(ScanJS)
	if err != nil {
		return model.ScanResult{}, err
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		return model.ScanResult{}, err
	}
	var out model.ScanResult
	if err := json.Unmarshal(blob, &out); err != nil {
		return model.ScanResult{}, err
	}
	return out, nil
}

// Absolute resolves a link target against the current page, dropping schemes
// that cannot be crawled.
func Absolute(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	low := strings.ToLower(href)
	for _, skip := range []string{"#", "javascript:", "mailto:", "tel:", "data:"} {
		if strings.HasPrefix(low, skip) {
			return ""
		}
	}
	baseURL, err := parseURL(base)
	if err != nil {
		return ""
	}
	ref, err := parseURL(href)
	if err != nil {
		return ""
	}
	resolved := baseURL.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	return resolved.String()
}

// Normalize makes two spellings of the same page compare equal.
func Normalize(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return raw
	}
	path := slash.ReplaceAllString(u.Path, "/")
	if path == "" {
		path = "/"
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	q := u.Query()
	for key := range q {
		if tracking[strings.ToLower(key)] {
			q.Del(key)
		}
	}
	u.Path = path
	u.RawQuery = q.Encode()
	u.Fragment = ""
	u.RawFragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

// Template collapses identifier-shaped path segments so /orders/1 and
// /orders/2 are one page with two instances.
func Template(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return raw
	}
	parts := strings.Split(u.Path, "/")
	for i, seg := range parts {
		if uuidRe.MatchString(seg) || intRe.MatchString(seg) || hexRe.MatchString(seg) || dateRe.MatchString(seg) {
			parts[i] = "{id}"
		}
	}
	out := slash.ReplaceAllString(strings.Join(parts, "/"), "/")
	if out == "" {
		return "/"
	}
	return out
}

func IsLoginURL(raw string, patterns []string) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	segments := strings.Split(strings.ToLower(u.Path), "/")
	for _, pattern := range patterns {
		p := strings.Trim(strings.ToLower(pattern), "/")
		for _, seg := range segments {
			if seg == "" {
				continue
			}
			if seg == p || strings.HasPrefix(seg, p+"-") || strings.HasSuffix(seg, "-"+p) {
				return true
			}
		}
	}
	return false
}

func InScope(raw string, cfg config.Config) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	base, err := parseURL(cfg.Run.BaseURL)
	if err != nil {
		return false
	}
	if host != strings.ToLower(base.Host) {
		allowed := false
		for _, h := range cfg.Scope.AllowHosts {
			if strings.EqualFold(h, host) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	} else if u.Scheme != base.Scheme {
		// The same host over a different scheme is a different origin and would
		// yield a duplicate crawl of every page.
		return false
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	included := false
	for _, pattern := range cfg.Scope.Include {
		if globMatch(pattern, path) {
			included = true
			break
		}
	}
	if !included {
		return false
	}
	for _, pattern := range cfg.Scope.Exclude {
		if globMatch(pattern, path) {
			return false
		}
	}
	return true
}

// globMatch supports ** across segments and * within one.
func globMatch(pattern, path string) bool {
	expr := globToRegexp(pattern)
	re, err := regexp.Compile(expr)
	if err != nil {
		return false
	}
	return re.MatchString(path)
}

func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return b.String()
}

var secretQuery = regexp.MustCompile(`(?i)token|key|secret|password|auth|session|code`)

// RedactURL masks query values that look like credentials before they reach
// the report.
func RedactURL(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	changed := false
	for key := range q {
		if secretQuery.MatchString(key) {
			q.Set(key, "***")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MatchPath exposes the scope glob so callers can reuse the same rule for
// unrelated path lists such as visual baselines to ignore.
func MatchPath(pattern, path string) bool {
	return globMatch(pattern, path)
}

// LinkDisposition says what the crawler will do with a link, and why. Only
// "followed" links enter the frontier; everything else is recorded so the
// report can show what was excluded rather than leave it implicit.
func LinkDisposition(base, href string, cfg config.Config) (resolved, disposition, reason string) {
	trimmed := strings.TrimSpace(href)
	lower := strings.ToLower(trimmed)
	switch {
	case trimmed == "" || trimmed == "#":
		return "", "empty", "no destination"
	case strings.HasPrefix(lower, "javascript:"):
		return "", "javascript", "javascript: pseudo-link"
	case strings.HasPrefix(lower, "mailto:"):
		return trimmed, "mail", "email address, not a page"
	case strings.HasPrefix(lower, "tel:"):
		return trimmed, "tel", "phone number, not a page"
	}
	resolved = Absolute(base, href)
	if resolved == "" {
		return "", "invalid", "could not resolve against the page URL"
	}
	target, err := parseURL(resolved)
	if err != nil {
		return "", "invalid", "unparseable URL"
	}
	root, err := parseURL(cfg.Run.BaseURL)
	if err != nil {
		return resolved, "invalid", "unparseable base URL"
	}
	if !strings.EqualFold(target.Host, root.Host) {
		for _, host := range cfg.Scope.AllowHosts {
			if strings.EqualFold(host, target.Host) {
				if InScope(resolved, cfg) {
					return resolved, "followed", ""
				}
				break
			}
		}
		return resolved, "off_origin", "host " + target.Host + " is not the configured origin"
	}
	if target.Scheme != root.Scheme {
		return resolved, "off_origin", "different scheme (" + target.Scheme + ")"
	}
	path := target.Path
	if path == "" {
		path = "/"
	}
	for _, pattern := range cfg.Scope.Exclude {
		if globMatch(pattern, path) {
			return resolved, "excluded", "matches exclude " + pattern
		}
	}
	included := false
	for _, pattern := range cfg.Scope.Include {
		if globMatch(pattern, path) {
			included = true
			break
		}
	}
	if !included {
		return resolved, "excluded", "outside include patterns"
	}
	return resolved, "followed", ""
}
