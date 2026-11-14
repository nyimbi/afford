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
	Summary  model.Summary   `json:"summary"`
	Pages    []model.Page    `json:"pages"`
	Findings []model.Finding `json:"findings"`
}

func Write(st *store.Store, runID, baseDir string, summary model.Summary, embed bool) (string, error) {
	pages, findings := st.Snapshot()
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Rank() != findings[j].Rank() {
			return findings[i].Rank() < findings[j].Rank()
		}
		if findings[i].PageURL != findings[j].PageURL {
			return findings[i].PageURL < findings[j].PageURL
		}
		return findings[i].Code < findings[j].Code
	})
	payload := Payload{Summary: summary, Pages: pages, Findings: findings}

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
.cover{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:1px;background:var(--line);border:1px solid var(--line);border-radius:10px;overflow:hidden;margin:14px 0}
.cover div{background:var(--panel);padding:12px 14px}
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
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:14px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden;display:flex;flex-direction:column}
.card img{width:100%;height:150px;object-fit:cover;object-position:top;border-bottom:1px solid var(--line);background:var(--chip)}
.card .body{padding:11px 13px 13px}
.card ul{margin:8px 0 0;padding:0;list-style:none}
.card li{font-size:12.5px;color:var(--muted);padding:2px 0}
.card li b{color:var(--ink);font-weight:600;font-family:ui-monospace,monospace;font-size:11.5px}
.empty{color:var(--muted);font-size:14px;padding:18px 0}
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

	b.WriteString("<div class='cover'>")
	cover := [][2]string{
		{"Pages visited", fmt.Sprint(p.Summary.PagesVisited)},
		{"Controls probed", fmt.Sprintf("%d/%d", p.Summary.ControlsProbed, p.Summary.ControlsTotal)},
		{"Forms", fmt.Sprintf("%d/%d", p.Summary.FormsProbed, p.Summary.FormsTotal)},
		{"Inputs swept", fmt.Sprint(p.Summary.InputsSwept)},
		{"Payloads sent", fmt.Sprint(p.Summary.PayloadsSent)},
	}
	for _, kv := range cover {
		fmt.Fprintf(&b, "<div><div class='k'>%s</div><div class='v'>%s</div></div>", esc(kv[0]), esc(kv[1]))
	}
	b.WriteString("</div>")

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
		search := strings.ToLower(f.Code + " " + f.Title + " " + f.PageURL + " " + f.Element)
		fmt.Fprintf(&b, "<tr data-sev='%s' data-text='%s'>", f.Severity, esc(search))
		fmt.Fprintf(&b, "<td class='sev %s'>%s</td>", f.Severity, f.Severity)
		fmt.Fprintf(&b, "<td><code>%s</code><div class='meta'>%s</div>", esc(f.Code), esc(f.Title))
		if f.Occurrences > 1 {
			fmt.Fprintf(&b, "<div class='meta'>also on %d other pages</div>", f.Occurrences-1)
		}
		b.WriteString("</td>")
		fmt.Fprintf(&b, "<td><a href='%s'>%s</a></td>", esc(f.PageURL), esc(f.PageURL))
		fmt.Fprintf(&b, "<td>%s</td>", esc(f.Element))
		fmt.Fprintf(&b, "<td><code>%s</code>", esc(f.Payload))
		if len(f.Evidence) > 0 || len(f.Detail) > 0 {
			blob, _ := json.MarshalIndent(f.Detail, "", "  ")
			b.WriteString("<details><summary>evidence</summary><pre>")
			b.WriteString(esc(strings.Join(f.Evidence, "\n")))
			if len(blob) > 2 {
				b.WriteString("\n\n")
				b.WriteString(esc(string(blob)))
			}
			b.WriteString("</pre></details>")
		}
		b.WriteString("</td></tr>")
	}
	b.WriteString("</tbody></table>")

	b.WriteString("<h2>Pages</h2><div class='grid'>")
	if len(p.Pages) == 0 {
		b.WriteString("<div class='empty'>no pages captured</div>")
	}
	for _, page := range p.Pages {
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
		b.WriteString("<article class='card'>")
		if src != "" {
			fmt.Fprintf(&b, "<img loading='lazy' src='%s' alt='screenshot of %s'>", esc(src), esc(page.URL))
		}
		b.WriteString("<div class='body'>")
		fmt.Fprintf(&b, "<h3><a href='%s'>%s</a></h3>", esc(page.URL), esc(page.URL))
		fmt.Fprintf(&b, "<div class='meta'>%s · HTTP %d · %d probes</div>",
			esc(page.Template), page.Status, page.Probes)
		b.WriteString("<ul>")
		if items := byPage[page.URL]; len(items) > 0 {
			for _, f := range items {
				fmt.Fprintf(&b, "<li><b>%s</b> %s</li>", esc(f.Code), esc(f.Title))
			}
		} else {
			b.WriteString("<li>no findings</li>")
		}
		b.WriteString("</ul></div></article>")
	}
	b.WriteString("</div>")

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
	}
	suite := junitSuite{
		Name: "afford", Timestamp: p.Summary.StartedAt.Format(time.RFC3339),
		Tests: p.Summary.PagesVisited,
	}
	for _, page := range p.Pages {
		c := junitCase{Name: page.URL, Classname: "afford.pages"}
		for _, f := range byPage[page.URL] {
			switch f.Severity {
			case model.Blocker, model.Critical, model.Major:
				suite.Failures++
				c.Failure = &junitFailure{
					Message: f.Code + ": " + f.Title,
					Body:    strings.Join(f.Evidence, "\n"),
				}
			default:
				c.SystemOut = append(c.SystemOut, f.Code+": "+f.Title)
			}
		}
		suite.Cases = append(suite.Cases, c)
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
	fmt.Fprintf(&b, "\nPages visited: %d · controls probed: %d/%d · forms: %d/%d · payloads: %d\n\n",
		p.Summary.PagesVisited, p.Summary.ControlsProbed, p.Summary.ControlsTotal,
		p.Summary.FormsProbed, p.Summary.FormsTotal, p.Summary.PayloadsSent)
	b.WriteString("## Findings\n\n")
	if len(p.Findings) == 0 {
		b.WriteString("_none_\n")
	}
	for _, f := range p.Findings {
		fmt.Fprintf(&b, "- **%s** `%s` %s — %s", f.Severity, f.Code, f.Title, f.PageURL)
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
