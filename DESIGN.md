# afford: design

Crawl a running web app, exercise every interactive affordance, and report what breaks.
Unit tests prove functions; this proves the wiring a user actually touches.

**Implementation:** Go, standard library plus `github.com/mxschmitt/playwright-go`. One
static binary, no runtime to install beyond the browser (`afford install`). The run log is
append-only JSONL rather than SQLite: it needs no dependency, survives a crash mid-write,
and the report is built from the same events the log holds. The tool must be runnable in a
CI image by copying one file.

## The problem, stated precisely

"Test every input and every button" is underdetermined. Nothing about a click tells you
whether the result was *correct*: only whether something happened, whether it crashed, and
whether the app told the user. A tool that guesses at semantics produces reports nobody
trusts, and an untrusted report is worse than none because it burns the attention that would
have found the real bug.

So the contract is narrower and falsifiable:

> **Every reachable page was rendered; every enabled affordance on it was exercised; and
> nothing crashed, returned 5xx, failed to respond, or silently did nothing. Here is the
> evidence for each claim.**

That is the whole product. Semantic correctness stays with unit and contract tests.

## What makes a page

A page is not a URL. It is `(route, query, session role, data state)`. Enumerating all of it
is undecidable, so the tool enumerates *routes* and samples *states*, and then reports its
own coverage explicitly. A run that says "41 of 47 discovered routes exercised" is useful; a
run that implies totality is lying.

- **Identity**: normalized URL: fragment dropped, tracking params stripped, query sorted.
- **Template**: path segments matching UUID / integer / long hex become `{id}`, so
  `/orders/1042` and `/orders/881` are one page with two instances.
- **Instances**: at most `max_instances_per_template` (default 3), discovered by the crawl.
- **Reachability**: recorded as an edge with the mechanism (`href`, `form`, `click`), because
  a page reachable only by clicking is a page with no deep link: itself a finding.

## Discovery: the union of three graphs

| source | finds | cost |
|---|---|---|
| `a[href]`, `form[action]`, `sitemap.xml` | static pages, orphan routes | cheap |
| clicking navigational controls | SPA routes that never appear in HTML | expensive |
| framework route manifest (`_buildManifest.js`, route tables) | routes nobody links to | cheap, adapter needed |

v0.1 does the first two; the third is an adapter slot. Click-derived URLs are fed back into
the frontier, which is the only way to walk a React/Vue app whose links are all `<button>`.

## What "test an input" means

Inputs are classified by declared type, then by name/label/placeholder semantics. Each class
gets a payload battery: empty, whitespace, boundary (below min, above max), overflow length,
malformed-by-type (`not-an-email`, `2024-02-30`, `abc` for `number`), unicode, and: in
`deep` mode only: an XSS sentinel whose *reflection is detected by the dialog it fires*,
not assumed.

The sweep is navigation-free: fill, blur, read `validity`, `validationMessage`, and any
inline error text, then restore. That is where exceptions, console errors and layout
breakage from long input are caught cheaply.

## What "test a button" means

Each control gets a fresh page load, then one action, then an honest classification of the
effect:

`navigated` · `dialog_opened` · `dom_changed` · `network_only` · **`no_effect`**

`no_effect` on an enabled button is the highest-value finding the tool produces and the one
no unit test can see: the handler is not wired. Occlusion by an overlay is checked with
`elementFromPoint`, because a control a real user cannot hit is not a control.

## Safety is a feature, not a flag

Against a real app, clicks create records, send mail, and charge cards. Therefore:

- **Mutation is granted by class, not by a single switch.** Submitting a form (creation)
  is `submit_forms` / `submit: true`. Deleting or erasing data is `allow_delete`. Paying,
  sending, publishing, approving and logging out is `allow_side_effects`.
  `allow_destructive` is the umbrella. Every skipped control is reported with the flag
  that would have allowed it, so coverage stays honest.
- Matching is on word boundaries: a substring rule quarantined a nav link called
  "dropdown" because it contains "drop".
- **`--no-submit`** fills and validates without ever submitting.
- **Network egress** stays on `allow_hosts`; off-origin links are recorded, not followed.
- Credentials come from a TTY prompt, `AFFORD_USERNAME`/`AFFORD_PASSWORD`, or a storage-state
  file. They are never written to the run database, the report, or argv.

## Oracles

A finding is a defect with a code, a severity, and an evidence bundle (console lines,
response status and URL, DOM excerpt, screenshot). Only deterministic predicates are used:

- uncaught exception / unhandled rejection
- `console.error`
- document or subresource 4xx/5xx, failed request
- blank render (no text, no controls, status < 400)
- framework error-boundary text (React/Next/Vue)
- horizontal overflow at the configured viewport
- dead control, occluded control, unlabeled control, empty `href`
- dead link: 4xx/5xx target, or a soft 404 (200 with "page not found" text)
- internal identifiers shown in a dropdown: UUID/ULID labels, UUID option values
- a visible, enabled select with nothing to choose
- 5xx or navigation-to-error *caused by* a click
- empty-required submit that proceeds anyway
- direct navigation that does not reproduce a page reached by clicking
- missing screenshot, recorded rather than swallowed

## Failure modes the tool must avoid

1. **False clean.** An auth wall or a JS-only shell yields "1 page, 0 findings". Guard: if
   the crawl visits ≤1 page, or the login path accounts for every navigation, emit
   `E_AUTH_WALL` at blocker severity and exit non-zero.
2. **Harness noise reported as product bugs.** Every finding carries its evidence, and
   timeouts land under `E_HARNESS_TIMEOUT`, never as a defect.
3. **Nondeterminism.** Payload choice is seeded; no LLM in the loop by default. A report you
   cannot reproduce is a rumour.
4. **State bleed.** Each probe starts from a fresh page load in a fresh page; sessions are
   cloned per worker, never shared across concurrent mutations.

## Extensions

**Link inventory.** A link is classified once, at capture: followed, excluded by a scope
glob, off-origin, empty, `javascript:`, mail or tel. Only `followed` enters the frontier;
the rest is stored with its reason and rendered as *Links not crawled*. Findings
(`E_OUT_OF_SCOPE`) are emitted for the two classes the operator can actually change.

**Uploads.** A file input receives a real file: an operator fixture from `[uploads]`
(by name, then by `accept`), else a synthesized valid PNG/PDF/CSV/JSON/text chosen from
`accept`. The browser never receives a path the operator did not choose. A missing
fixture is `E_UPLOAD_FAILED`, not silence.

**Popups.** After an action, extra pages in the context are settled, scanned,
screenshotted and closed; their observations are attached to the opener's finding with
`popup_url` and `screenshot`. The collector is bound after the popup exists, so console
output emitted before it is missed: an accepted limitation of polling the context rather
than subscribing per probe.

**Visual regression.** Baselines are keyed by route template and viewport, so detail-page
instances share a reference. Comparison is a pixel diff with a per-channel tolerance,
because an explainable gate is one people keep. A size change is always a finding.
Artifacts (baseline, current, diff) are written into the run and shown side by side.
Volatile regions are masked through Playwright's screenshot mask; baselines are
platform-specific and must be generated in the environment that compares them.

## Report

One HTML file plus a `screenshots/` directory, backed by `report.json` and `junit.xml`.
Per page: template, instances, screenshot, control inventory, probe ledger, findings.
Top: severity counts, coverage statement, and the explicit list of what was *not* tested.

Findings are merged across pages by `(code, element, detail)`. A nav link that 404s is one
defect present on ten pages, not ten defects; the report says "also on 9 other pages" so the
list stays actionable without hiding the blast radius. Dedupe never merges across different
targets or different element names.

## CI contract

`afford run` is a gate, so the exit code is part of the interface: `0` clean, `1` findings
at or above `--fail-on`, `2` usage/config, `3` the run could not complete. Artifacts are
written before the code is decided, including on partial runs. A crawl that captured zero
pages exits 3: the false-clean case is the one this tool must never ship. `--timeout` is a
wall clock; `--no-prompt` turns missing credentials into a failure instead of a hang.

## Rejected options

- **Selenium**: no auto-wait, no tracing, no storage-state; the flake budget is worse.
- **Cypress**: JS-only, cross-origin and multi-tab limitations, weak crawl ergonomics.
- **Puppeteer**: no Python, no test-runner or trace artifacts.
- **Raw CDP**: reimplements what Playwright already stabilised.
- **LLM-driven agent exploration as the default loop**: non-reproducible, costly per page,
  and it cannot tell you *why* a click did nothing. Kept as an optional later adapter for
  naming findings, never for the verdict.
- **Record-and-replay (Playwright codegen)**: tests the happy path you already know; this
  tool is for the paths you do not.

## Roadmap

- v0.2: route-manifest adapters (Next.js/React Router/Nuxt), OpenAPI-driven valid payloads,
  multi-role crawl (same run as admin/anon/user, diff the affordance sets).
- v0.3: run-to-run diff of findings (new/fixed, not just visual), axe-core a11y pass,
  trace zip per failure.
- v0.4: click-path replay for pages that cannot be deep-linked; interaction inside popups.

## Added after the first release

- **Page audits** run from one shared pass (`internal/pass`) so `run` and `check` cannot diverge: rendered-text leakage, broken images, unhandled rejections, a bounded axe-core set, a Tab pass.
- **Extra passes** (`internal/run/extras.go`) never click: extra viewports, role replay against declared `deny` globs, and routes the sitemap or manifest names that no link reaches.
- **Run lifecycle**: fixture hooks bracket the run, every request carries `X-Afford-Run`, and each run appends to `history.jsonl`.
- **Privacy**: password inputs are masked in every screenshot, unconditionally.
- **Untested**: the Dockerfile and the composite GitHub Action have never run here.
