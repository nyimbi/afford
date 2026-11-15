// Package report writes the four deliverables: HTML, JSON, JUnit, Markdown.
package report

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"afford/internal/model"
	"afford/internal/store"
)

type Payload struct {
	Summary  model.Summary      `json:"summary"`
	Pages    []model.Page       `json:"pages"`
	Findings []model.Finding    `json:"findings"`
	Links    []model.LinkRecord `json:"links"`
}

func Write(st *store.Store, runID, baseDir string, summary model.Summary, embed bool) (string, error) {
	pages, findings := st.Snapshot()
	model.SortFindings(findings)
	payload := Payload{Summary: summary, Pages: pages, Findings: findings, Links: st.LinkRecords()}

	blob, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(baseDir, "report.json"), blob, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(baseDir, "junit.xml"), []byte(junit(payload)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(baseDir, "summary.md"), []byte(markdown(payload)), 0o644); err != nil {
		return "", err
	}
	page := filepath.Join(baseDir, "report.html")
	if err := os.WriteFile(page, []byte(buildHTML(payload, baseDir, embed)), 0o644); err != nil {
		return "", err
	}
	return page, nil
}

func esc(s string) string { return html.EscapeString(s) }

const css = `
:root{--bg:#fbfaf8;--panel:#fff;--ink:#171a1f;--muted:#5d6572;--line:#e4e0da;
	--accent:#2f5d8a;--chip:#f1eee9;--blocker:#8f1d1d;--critical:#b23a2e;
	--major:#a8620f;--minor:#68707c;--info:#4b6b52}
@media (prefers-color-scheme: dark){
	:root{--bg:#121417;--panel:#181b1f;--ink:#e9e7e2;--muted:#9aa3ad;--line:#2a2f36;
	--accent:#7aa8d0;--chip:#22262c;--blocker:#e07a7a;--critical:#e89a86;
	--major:#e3b169;--minor:#9aa3ad;--info:#8fb79a}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 ui-sans-serif,-apple-system,"Segoe UI",Inter,sans-serif}
main{max-width:1180px;margin:0 auto;padding:32px 20px 80px}
h1{font-size:22px;margin:0 0 4px;letter-spacing:-.01em}
h2{font-size:15px;text-transform:uppercase;letter-spacing:.08em;color:var(--muted);margin:36px 0 12px;font-weight:600}
h3{font-size:14px;margin:0 0 2px;word-break:break-all}
a{color:var(--accent);text-decoration:none}
.meta{color:var(--muted);font-size:13px}
.counts{display:flex;gap:8px;flex-wrap:wrap;margin:18px 0 8px}
.pill{background:var(--chip);border-radius:999px;padding:5px 12px;font-size:13px;font-variant-numeric:tabular-nums}
.pill b{font-weight:650}
.pill.blocker b{color:var(--blocker)} .pill.critical b{color:var(--critical)}
.pill.major b{color:var(--major)} .pill.minor b{color:var(--minor)} .pill.info b{color:var(--info)}
.cover{display:flex;flex-wrap:wrap;gap:10px;margin:14px 0}
.cover div{background:var(--panel);border:1px solid var(--line);border-radius:10px;
	padding:12px 14px;flex:1 1 150px;min-width:140px}
.cover .k{font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--muted)}
.cover .v{font-size:19px;font-variant-numeric:tabular-nums;margin-top:2px}
.toolbar{display:flex;gap:10px;margin:0 0 10px;flex-wrap:wrap}
input,select{background:var(--panel);color:var(--ink);border:1px solid var(--line);border-radius:8px;padding:8px 10px;font:inherit}
input{min-width:min(360px,70vw)}
table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden}
th{text-align:left;font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--muted);font-weight:600;padding:9px 12px;border-bottom:1px solid var(--line)}
td{padding:9px 12px;border-bottom:1px solid var(--line);vertical-align:top;font-size:13.5px}
tr:last-child td{border-bottom:none}
td.sev{white-space:nowrap;font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:.04em}
td.sev.blocker{color:var(--blocker)} td.sev.critical{color:var(--critical)}
td.sev.major{color:var(--major)} td.sev.minor{color:var(--minor)} td.sev.info{color:var(--info)}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px}
pre{white-space:pre-wrap;word-break:break-word;background:var(--chip);border-radius:8px;padding:10px;margin:8px 0 0;font-size:12px;color:var(--muted)}
details summary{cursor:pointer;color:var(--muted);font-size:12px}
img.thumb{width:96px;height:62px;object-fit:cover;object-position:top;border-radius:6px;
	border:1px solid var(--line);display:block;background:var(--chip)}
td.thumb{padding:6px 8px;width:104px}
table.pages td{vertical-align:middle}
table.pages tbody tr:hover td{background:var(--chip)}
td.bad{color:var(--critical);font-weight:650}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--muted);margin-right:7px}
.dot.blocker{background:var(--blocker)} .dot.critical{background:var(--critical)}
.dot.major{background:var(--major)} .dot.minor{background:var(--minor)} .dot.info{background:var(--info)}
.empty{color:var(--muted);font-size:14px;padding:18px 0}
.visual{display:flex;flex-direction:column;gap:16px}
.vcard{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px 16px}
.trio{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;margin-top:10px}
.trio figure{margin:0}
.trio img{width:100%;border:1px solid var(--line);border-radius:6px;background:var(--chip)}
.badge{display:inline-block;font-size:10.5px;text-transform:uppercase;letter-spacing:.05em;border-radius:99px;padding:1px 7px;margin-left:6px;background:var(--chip);color:var(--muted);vertical-align:middle}
.badge.new{background:#b3261e;color:#fff}.badge.unconfirmed{background:#7a5c00;color:#fff}.badge.confirmed{background:#1d6b3a;color:#fff}
.trio figcaption{font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--muted);margin-top:4px}
@media (max-width:720px){.trio{grid-template-columns:1fr}}
a.shot img{width:120px;border:1px solid var(--line);border-radius:6px;margin-top:8px;display:block}
.start{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:6px 18px 14px}
.start ol{margin:6px 0 0;padding-left:20px}
.start li{margin:6px 0;font-size:13.5px}
.limits{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px 18px}
.limits li{color:var(--muted);font-size:13.5px;margin:3px 0}
`

const filterJS = `
const q = document.getElementById('q');
const sev = document.getElementById('sev');
function apply() {
	const text = q.value.toLowerCase();
	const level = sev.value;
	document.querySelectorAll('tbody tr[data-sev]').forEach(function (tr) {
		const ok = (!level || tr.dataset.sev === level) && (!text || tr.dataset.text.indexOf(text) >= 0);
		tr.style.display = ok ? '' : 'none';
	});
}
q.addEventListener('input', apply);
sev.addEventListener('change', apply);
const pq = document.getElementById('pq');
function applyPages() {
	const text = pq.value.toLowerCase();
	document.querySelectorAll('tbody tr[data-page]').forEach(function (tr) {
		tr.style.display = (!text || tr.dataset.page.indexOf(text) >= 0) ? '' : 'none';
	});
}
pq.addEventListener('input', applyPages);
document.querySelectorAll('a.filter-page').forEach(function (link) {
	link.addEventListener('click', function (event) {
		event.preventDefault();
		q.value = link.dataset.url;
		q.dispatchEvent(new Event('input'));
		q.scrollIntoView({behavior: 'smooth', block: 'center'});
	});
});
`

func buildHTML(p Payload, baseDir string, embed bool) string {
	var b strings.Builder
	byPage := map[string][]model.Finding{}
	for _, f := range p.Findings {
		byPage[f.PageURL] = append(byPage[f.PageURL], f)
		// A merged finding is still present on every page it was seen on; a card
		// that says "no findings" while carrying the shared nav bug lies.
		for _, also := range f.AlsoOn {
			byPage[also] = append(byPage[also], f)
		}
	}

	b.WriteString("<!doctype html><html lang='en'><head><meta charset='utf-8'>")
	b.WriteString("<meta name='viewport' content='width=device-width,initial-scale=1'>")
	fmt.Fprintf(&b, "<title>afford — %s</title><style>%s</style></head><body><main>", esc(p.Summary.BaseURL), css)
	fmt.Fprintf(&b, "<h1>afford — %s</h1>", esc(p.Summary.BaseURL))
	fmt.Fprintf(&b, "<div class='meta'>run <code>%s</code> · started %s · finished %s</div>",
		esc(p.Summary.RunID),
		esc(p.Summary.StartedAt.Format(time.RFC3339)),
		esc(p.Summary.FinishedAt.Format(time.RFC3339)))

	b.WriteString("<div class='counts'>")
	for _, level := range []model.Severity{model.Blocker, model.Critical, model.Major, model.Minor, model.Info} {
		fmt.Fprintf(&b, "<span class='pill %s'><b>%d</b> %s</span>",
			level, p.Summary.Counts[level], level)
	}
	b.WriteString("</div>")
	if line := gateLine(p.Summary); line != "" {
		fmt.Fprintf(&b, "<div class='meta gate'>%s</div>", esc(line))
	}
	b.WriteString(baselineHTML(p.Summary))

	var visualDiffs []model.Finding
	for _, f := range p.Findings {
		if f.Code == "E_VISUAL_DIFF" {
			visualDiffs = append(visualDiffs, f)
		}
	}
	shotCount := 0
	loads := make([]int64, 0, len(p.Pages))
	settled := make([]int64, 0, len(p.Pages))
	for _, page := range p.Pages {
		if page.Screenshot != "" {
			shotCount++
		}
		if page.LoadMS > 0 {
			loads = append(loads, page.LoadMS)
		}
		if page.SettledMS > 0 {
			settled = append(settled, page.SettledMS)
		}
	}
	sort.Slice(loads, func(i, j int) bool { return loads[i] < loads[j] })
	sort.Slice(settled, func(i, j int) bool { return settled[i] < settled[j] })

	b.WriteString("<div class='cover'>")
	cover := [][2]string{
		{"Pages visited", fmt.Sprint(p.Summary.PagesVisited)},
		{"Screenshots", fmt.Sprintf("%d/%d", shotCount, len(p.Pages))},
		{"Controls", fmt.Sprintf("%d/%d", p.Summary.ControlsProbed, p.Summary.ControlsTotal)},
		{"Forms", fmt.Sprintf("%d/%d", p.Summary.FormsProbed, p.Summary.FormsTotal)},
		{"Inputs", fmt.Sprint(p.Summary.InputsSwept)},
		{"Payloads", fmt.Sprint(p.Summary.PayloadsSent)},
		{"Links skipped", fmt.Sprint(len(p.Links))},
		{"Visual diffs", visualLabel(p.Summary.VisualEnabled, len(visualDiffs))},
		{"Load p50 / p95", fmt.Sprintf("%d / %d ms", percentile(loads, 50), percentile(loads, 95))},
		{"Settled p50 / p95", fmt.Sprintf("%d / %d ms", percentile(settled, 50), percentile(settled, 95))},
	}
	for _, kv := range cover {
		fmt.Fprintf(&b, "<div><div class='k'>%s</div><div class='v'>%s</div></div>", esc(kv[0]), esc(kv[1]))
	}
	b.WriteString("</div>")

	var gating []model.Finding
	for _, f := range p.Findings {
		if f.Gates() {
			gating = append(gating, f)
		}
	}
	if len(gating) > 8 {
		b.WriteString("<div class='start'><h2>Start here</h2><ol>")
		for _, f := range gating[:3] {
			fmt.Fprintf(&b, "<li><code>%s</code> %s — <a href='%s'>%s</a>",
				esc(f.Code), esc(f.Title), esc(f.PageURL), esc(f.PageURL))
			if first := firstEvidence(f); first != "" {
				fmt.Fprintf(&b, "<div class='meta'>%s</div>", esc(first))
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ol></div>")
	}
	b.WriteString("<h2>Findings</h2><div class='toolbar'>")
	b.WriteString("<input id='q' type='search' placeholder='filter by page, code, control…'>")
	b.WriteString("<select id='sev'><option value=''>all severities</option>")
	for _, level := range []string{"blocker", "critical", "major", "minor", "info"} {
		fmt.Fprintf(&b, "<option>%s</option>", level)
	}
	b.WriteString("</select></div>")

	b.WriteString("<table><thead><tr><th>Severity</th><th>Finding</th><th>Page</th>")
	b.WriteString("<th>Control</th><th>Payload / evidence</th></tr></thead><tbody>")
	if len(p.Findings) == 0 {
		b.WriteString("<tr><td colspan='5'>no findings</td></tr>")
	}
	for _, f := range p.Findings {
		search := strings.ToLower(f.Code + " " + f.Title + " " + f.PageURL + " " + f.Element + " " + f.Status + " " + f.Confidence)
		fmt.Fprintf(&b, "<tr data-sev='%s' data-text='%s'>", f.Severity, esc(search))
		fmt.Fprintf(&b, "<td class='sev %s'>%s</td>", f.Severity, f.Severity)
		fmt.Fprintf(&b, "<td><code>%s</code>%s<div class='meta'>%s</div>", esc(f.Code), badges(f), esc(f.Title))
		if f.Occurrences > 1 {
			fmt.Fprintf(&b, "<div class='meta'>also on %d other pages</div>", f.Occurrences-1)
		}
		b.WriteString("</td>")
		fmt.Fprintf(&b, "<td><a href='%s'>%s</a></td>", esc(f.PageURL), esc(f.PageURL))
		fmt.Fprintf(&b, "<td>%s</td>", esc(f.Element))
		fmt.Fprintf(&b, "<td><code>%s</code>", esc(f.Payload))
		if f.Why != "" || len(f.Evidence) > 0 || len(f.Detail) > 0 || len(f.AlsoOn) > 0 {
			b.WriteString("<details><summary>evidence</summary><pre>")
			if f.Why != "" {
				b.WriteString("why: " + esc(f.Why))
			}
			if len(f.Evidence) > 0 {
				b.WriteString("\n\n" + esc(strings.Join(f.Evidence, "\n")))
			}
			if formatted := formatDetail(f.Detail); formatted != "" {
				b.WriteString("\n\n" + esc(formatted))
			}
			if len(f.AlsoOn) > 0 {
				b.WriteString("\n\nalso on:\n" + esc(strings.Join(f.AlsoOn, "\n")))
			}
			b.WriteString("</pre></details>")
			if shot, _ := f.Detail["screenshot"].(string); shot != "" {
				fmt.Fprintf(&b, "<a class='shot' href='%s'><img loading='lazy' src='%s' alt='popup screenshot'></a>",
					esc(shot), esc(shot))
			}
		}
		b.WriteString("</td></tr>")
	}
	b.WriteString("</tbody></table>")

	b.WriteString("<h2>Pages</h2>")
	b.WriteString("<div class='toolbar'>")
	b.WriteString("<input id='pq' type='search' placeholder='filter pages by URL, title or template…'>")
	b.WriteString("</div>")
	b.WriteString("<table class='pages'><thead><tr><th class='thumb'></th><th>Page</th>")
	b.WriteString("<th>Status</th><th>Load</th><th>Settled</th><th>Controls</th><th>Probes</th><th>Findings</th>")
	b.WriteString("</tr></thead><tbody>")
	if len(p.Pages) == 0 {
		b.WriteString("<tr><td colspan='8'>no pages captured</td></tr>")
	}
	ordered := append([]model.Page(nil), p.Pages...)
	sort.SliceStable(ordered, func(i, j int) bool {
		si, sj := byPage[ordered[i].URL], byPage[ordered[j].URL]
		if wi, wj := worstRank(si), worstRank(sj); wi != wj {
			return wi < wj
		}
		if len(si) != len(sj) {
			return len(si) > len(sj)
		}
		if ordered[i].LoadMS != ordered[j].LoadMS {
			return ordered[i].LoadMS > ordered[j].LoadMS
		}
		return ordered[i].URL < ordered[j].URL
	})
	for _, page := range ordered {
		src := ""
		if page.Screenshot != "" {
			if embed {
				blob, err := os.ReadFile(filepath.Join(baseDir, page.Screenshot))
				if err == nil {
					src = "data:image/png;base64," + base64.StdEncoding.EncodeToString(blob)
				}
			} else {
				src = page.Screenshot
			}
		}
		items := byPage[page.URL]
		fmt.Fprintf(&b, "<tr data-page='%s'>", esc(strings.ToLower(
			page.URL+" "+page.Title+" "+page.Template)))
		if src != "" {
			fmt.Fprintf(&b, "<td class='thumb'><a href='%s'><img class='thumb' loading='lazy' src='%s' alt='screenshot of %s'></a></td>",
				esc(src), esc(src), esc(page.URL))
		} else {
			b.WriteString("<td class='thumb'><span class='meta'>no image</span></td>")
		}
		fmt.Fprintf(&b, "<td><a href='%s'>%s</a><div class='meta'>%s · depth %d%s</div></td>",
			esc(page.URL), esc(page.URL), esc(page.Template), page.Depth, parentNote(page.Parent))
		statusClass := ""
		if page.Status >= 400 {
			statusClass = " class='bad'"
		}
		fmt.Fprintf(&b, "<td%s>%d</td>", statusClass, page.Status)
		fmt.Fprintf(&b, "<td>%s</td>", loadLabel(page.LoadMS))
		fmt.Fprintf(&b, "<td>%s</td>", loadLabel(page.SettledMS))
		fmt.Fprintf(&b, "<td>%d</td>", page.Controls)
		fmt.Fprintf(&b, "<td>%d</td>", page.Probes)
		worst := worstSeverity(items)
		fmt.Fprintf(&b, "<td><span class='dot %s' title='%d finding(s), worst %s'></span>",
			worst, len(items), dotLabel(worst))
		fmt.Fprintf(&b, "<a class='filter-page' href='#' data-url='%s' title='show these findings'>%d</a></td>",
			esc(page.URL), len(items))
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")

	if len(visualDiffs) > 0 {
		b.WriteString("<h2>Visual changes</h2><div class='visual'>")
		for _, f := range visualDiffs {
			fmt.Fprintf(&b, "<article class='vcard'><h3><a href='%s'>%s</a></h3>", esc(f.PageURL), esc(f.PageURL))
			fmt.Fprintf(&b, "<div class='meta'>%.3f%% of pixels changed", detailFloat(f.Detail, "pct"))
			if changed, ok := f.Detail["changed_pixels"]; ok {
				fmt.Fprintf(&b, " · %v pixels", changed)
			}
			if flag, _ := f.Detail["size_changed"].(bool); flag {
				b.WriteString(" · page size changed")
			}
			b.WriteString("</div><div class='trio'>")
			for _, label := range []string{"baseline", "current", "diff"} {
				path, _ := f.Detail[label].(string)
				if path == "" {
					continue
				}
				src := path
				if embed {
					if blob, err := os.ReadFile(filepath.Join(baseDir, path)); err == nil {
						src = "data:image/png;base64," + base64.StdEncoding.EncodeToString(blob)
					}
				}
				fmt.Fprintf(&b, "<figure><img loading='lazy' src='%s' alt='%s'>",
					esc(src), esc(label))
				fmt.Fprintf(&b, "<figcaption>%s</figcaption></figure>", esc(label))
			}
			b.WriteString("</div></article>")
		}
		b.WriteString("</div>")
	}

	b.WriteString("<h2>Links not crawled</h2>")
	if len(p.Links) == 0 {
		b.WriteString("<p class='meta'>every link the crawler saw was followed</p>")
	} else {
		counts := map[string]int{}
		for _, link := range p.Links {
			counts[link.Disposition]++
		}
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
		}
		fmt.Fprintf(&b, "<p class='meta'>%d link(s) seen and deliberately not followed: %s.</p>",
			len(p.Links), strings.Join(parts, ", "))
		b.WriteString("<table><thead><tr><th>Disposition</th><th>Link</th><th>Found on</th><th>Reason</th></tr></thead><tbody>")
		shown := 0
		for _, link := range p.Links {
			if shown >= 200 {
				fmt.Fprintf(&b, "<tr><td colspan='4' class='meta'>…and %d more</td></tr>", len(p.Links)-shown)
				break
			}
			shown++
			target := link.Href
			if link.Resolved != "" {
				target = link.Resolved
			}
			fmt.Fprintf(&b, "<tr><td><code>%s</code></td><td>%s</td><td class='meta'>%s</td><td class='meta'>%s</td></tr>",
				esc(link.Disposition), esc(target), esc(link.Source), esc(link.Reason))
		}
		b.WriteString("</tbody></table>")
	}

	b.WriteString("<h2>Coverage limits</h2><div class='limits'>")
	b.WriteString("<p class='meta'>What this run did not test:</p><ul>")
	if len(p.Summary.NotTested) == 0 {
		b.WriteString("<li>nothing recorded as skipped</li>")
	}
	for _, item := range p.Summary.NotTested {
		fmt.Fprintf(&b, "<li>%s</li>", esc(item))
	}
	b.WriteString("</ul><p class='meta'>What no run of this tool proves:</p><ul>")
	b.WriteString("<li>This run proves the affordances it could reach did not crash, fail, or go inert.</li>")
	b.WriteString("<li>It does not prove business rules are correct, that data was saved properly, or that the app is usable by a human.</li>")
	b.WriteString("<li>Destructive controls and form submissions disabled by configuration were not exercised.</li>")
	b.WriteString("</ul></div>")
	fmt.Fprintf(&b, "<script>%s</script></main></body></html>", filterJS)
	return b.String()
}

// badges renders the gate verdict next to a finding: what kind of finding this
// is matters as much as its severity once a baseline exists.
func badges(f model.Finding) string {
	var out string
	add := func(class, text, title string) {
		out += fmt.Sprintf(" <span class='badge %s' title='%s'>%s</span>", class, esc(title), esc(text))
	}
	switch f.Status {
	case "new":
		text := "new"
		if f.NewPages > 0 {
			text = fmt.Sprintf("new on %d page(s)", f.NewPages)
		}
		add("new", text, "not in the accepted baseline")
	case "known":
		add("known", "known", "accepted in the baseline; does not fail the run")
	case "suppressed":
		add("known", "suppressed", f.Suppression)
	}
	switch f.Confidence {
	case "unconfirmed":
		add("unconfirmed", "unconfirmed", "did not reproduce on a second load; does not fail the run")
	case "confirmed":
		add("confirmed", "confirmed", "reproduced on a second load")
	}
	return out
}

func gateLine(s model.Summary) string {
	var parts []string
	if s.Baseline != nil && !s.Baseline.Accepted {
		parts = append(parts, fmt.Sprintf("%d new", s.Baseline.New), fmt.Sprintf("%d known", s.Baseline.Known))
		if n := len(s.Baseline.Fixed); n > 0 {
			parts = append(parts, fmt.Sprintf("%d fixed", n))
		}
	}
	if s.Baseline != nil && s.Baseline.Accepted {
		parts = append(parts, fmt.Sprintf("baseline written: %d finding(s) accepted", s.Baseline.Known))
	}
	if s.Suppressed > 0 {
		parts = append(parts, fmt.Sprintf("%d suppressed", s.Suppressed))
	}
	if s.Unconfirmed > 0 {
		parts = append(parts, fmt.Sprintf("%d unconfirmed", s.Unconfirmed))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Counts above are findings that can fail the run. " + strings.Join(parts, " · ")
}

func baselineHTML(s model.Summary) string {
	if s.Baseline == nil || (len(s.Baseline.Fixed) == 0 && len(s.Baseline.NotReached) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("<div class='start'><h2>Against the baseline</h2>")
	if len(s.Baseline.Fixed) > 0 {
		fmt.Fprintf(&b, "<div class='meta'>%d accepted finding(s) are gone:</div><ul>", len(s.Baseline.Fixed))
		for _, e := range s.Baseline.Fixed {
			fmt.Fprintf(&b, "<li><code>%s</code> %s %s</li>", esc(e.Code), esc(e.Element), esc(strings.Join(e.Templates, ", ")))
		}
		b.WriteString("</ul>")
	}
	if len(s.Baseline.NotReached) > 0 {
		fmt.Fprintf(&b, "<div class='meta'>%d accepted finding(s) are not shown as fixed because this run did not reach their pages:</div><ul>", len(s.Baseline.NotReached))
		for _, e := range s.Baseline.NotReached {
			fmt.Fprintf(&b, "<li><code>%s</code> %s %s</li>", esc(e.Code), esc(e.Element), esc(strings.Join(e.Templates, ", ")))
		}
		b.WriteString("</ul>")
	}
	b.WriteString("</div>")
	return b.String()
}

func countCode(findings []model.Finding, code string) int {
	n := 0
	for _, f := range findings {
		if f.Code == code {
			n++
		}
	}
	return n
}

func visualLabel(enabled bool, diffs int) string {
	if !enabled {
		return "off"
	}
	return fmt.Sprint(diffs)
}

func dotLabel(severity string) string {
	if severity == "" {
		return "none"
	}
	return severity
}

func firstEvidence(f model.Finding) string {
	if len(f.Evidence) > 0 {
		return truncateText(f.Evidence[0], 160)
	}
	return ""
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// formatDetail renders the finding's structured detail as readable lines instead
// of a JSON blob; a human adjudicating a finding should not parse braces.
func formatDetail(detail map[string]any) string {
	if len(detail) == 0 {
		return ""
	}
	keys := make([]string, 0, len(detail))
	for key := range detail {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(key + ": " + fmt.Sprint(detail[key]))
	}
	return b.String()
}

func detailFloat(detail map[string]any, key string) float64 {
	switch v := detail[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	}
	return 0
}

func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p*len(sorted)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func loadLabel(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d ms", ms)
}

func worstSeverity(items []model.Finding) string {
	worst, rank := "", 99
	for _, f := range items {
		if r := f.Rank(); r < rank {
			rank, worst = r, string(f.Severity)
		}
	}
	return worst
}

func worstRank(items []model.Finding) int {
	rank := 99
	for _, f := range items {
		if r := f.Rank(); r < rank {
			rank = r
		}
	}
	return rank
}

func parentNote(parent string) string {
	if parent == "" {
		return ""
	}
	if len(parent) > 70 {
		parent = parent[:67] + "…"
	}
	return " · linked from " + parent
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	SystemOut []string      `xml:"system-out,omitempty"`
}

type junitSuite struct {
	XMLName   xml.Name    `xml:"testsuite"`
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Timestamp string      `xml:"timestamp,attr"`
	Cases     []junitCase `xml:"testcase"`
}

func junit(p Payload) string {
	byPage := map[string][]model.Finding{}
	for _, f := range p.Findings {
		byPage[f.PageURL] = append(byPage[f.PageURL], f)
		for _, also := range f.AlsoOn {
			byPage[also] = append(byPage[also], f)
		}
	}
	cases := make([]junitCase, 0, len(p.Pages)+1)
	failures := 0

	addCase := func(name, classname string, findings []model.Finding) {
		c := junitCase{Name: name, Classname: classname}
		var codes []string
		var bodies []string
		for _, f := range findings {
			if !f.Gates() {
				c.SystemOut = append(c.SystemOut, "not gating ("+f.Status+f.Confidence+") "+f.Code+": "+f.Title)
				continue
			}
			switch f.Severity {
			case model.Blocker, model.Critical, model.Major:
				codes = append(codes, f.Code)
				body := f.Code + ": " + f.Title
				if len(f.Evidence) > 0 {
					body += "\n" + strings.Join(f.Evidence, "\n")
				}
				bodies = append(bodies, body)
			default:
				c.SystemOut = append(c.SystemOut, f.Code+": "+f.Title)
			}
		}
		if len(codes) > 0 {
			failures++
			c.Failure = &junitFailure{
				Message: strings.Join(codes, ", "),
				Body:    strings.Join(bodies, "\n\n"),
			}
		}
		cases = append(cases, c)
	}

	covered := map[string]bool{}
	for _, page := range p.Pages {
		covered[page.URL] = true
		addCase(page.URL, "afford.pages", byPage[page.URL])
	}
	// A run-level finding (auth wall, harness failure) belongs to no page. It
	// still has to fail CI, so it is its own testcase rather than disappearing.
	var runLevel []model.Finding
	for _, f := range p.Findings {
		if !covered[f.PageURL] {
			runLevel = append(runLevel, f)
		}
	}
	if len(runLevel) > 0 {
		addCase("afford.run", "afford", runLevel)
	}

	suite := junitSuite{
		Name:      "afford",
		Tests:     len(cases),
		Failures:  failures,
		Timestamp: p.Summary.StartedAt.Format(time.RFC3339),
		Cases:     cases,
	}
	blob, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return ""
	}
	return xml.Header + string(blob) + "\n"
}

func markdown(p Payload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# afford — %s\n\n", p.Summary.BaseURL)
	fmt.Fprintf(&b, "Run `%s` · %s → %s\n\n",
		p.Summary.RunID,
		p.Summary.StartedAt.Format(time.RFC3339),
		p.Summary.FinishedAt.Format(time.RFC3339))
	b.WriteString("| severity | count |\n|---|---|\n")
	for _, level := range []model.Severity{model.Blocker, model.Critical, model.Major, model.Minor, model.Info} {
		fmt.Fprintf(&b, "| %s | %d |\n", level, p.Summary.Counts[level])
	}
	if line := gateLine(p.Summary); line != "" {
		fmt.Fprintf(&b, "\n%s\n", line)
	}
	visual := "off"
	if p.Summary.VisualEnabled {
		visual = fmt.Sprintf("%d diff(s)", countCode(p.Findings, "E_VISUAL_DIFF"))
	}
	fmt.Fprintf(&b, "\nPages visited: %d · controls probed: %d/%d · forms: %d/%d · payloads: %d · links skipped: %d · visual: %s\n\n",
		p.Summary.PagesVisited, p.Summary.ControlsProbed, p.Summary.ControlsTotal,
		p.Summary.FormsProbed, p.Summary.FormsTotal, p.Summary.PayloadsSent,
		len(p.Links), visual)
	b.WriteString("## Findings\n\n")
	if len(p.Findings) == 0 {
		b.WriteString("_none_\n")
	}
	for _, f := range p.Findings {
		tag := ""
		if f.Status != "" {
			tag += " [" + f.Status + "]"
		}
		if f.Confidence == "unconfirmed" {
			tag += " [unconfirmed]"
		}
		fmt.Fprintf(&b, "- **%s** `%s`%s %s — %s", f.Severity, f.Code, tag, f.Title, f.PageURL)
		if f.Element != "" {
			fmt.Fprintf(&b, " — %s", f.Element)
		}
		b.WriteString("\n")
	}
	if len(p.Summary.NotTested) > 0 {
		b.WriteString("\n## Not tested\n\n")
		for _, item := range p.Summary.NotTested {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}
	return b.String()
}
