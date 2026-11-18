# afford

Crawl a running web app, use every control it offers, and report what breaks.

Unit tests prove functions work. afford proves the wiring a person touches: the
button that does nothing, the link that 404s, the form whose submit throws, the
image that never loads, the menu the keyboard cannot reach.

```sh
go install github.com/nyimbi/afford/cmd/afford@latest
afford install                              # once: fetch the Playwright browsers
afford init --base-url http://localhost:3000
afford run --config afford.toml
```

## Why

End-to-end suites test the paths somebody remembered to write. afford walks the app
instead: it follows links, clicks every enabled control from a fresh load, fills
every input with values derived from the field's own declared type, submits forms,
and records what happened to each action. An action that completes with no
navigation, no dialog, no DOM change and no request is a finding, and on most
codebases it is the highest-value one the tool produces.

## Requirements

- Go 1.27.4 or newer. Standard library plus Playwright-Go, no cgo.
- Playwright browsers, about 300 MB, fetched once by `afford install`.
- A running instance of the app under test. afford drives a real browser; it does
  not start your server.

## What it does

For each reachable page it captures console errors, uncaught exceptions, 4xx/5xx
responses, failed requests, blank renders, framework error screens, horizontal
overflow, leaked `undefined`/`NaN` text, broken images, unhandled promise
rejections, and accessibility violations from a bounded axe-core rule set. A Tab
pass reports keyboard traps, visible controls the keyboard cannot reach, and
controls with no focus indicator.

Then, from a fresh load each time, it clicks every enabled control and classifies
what happened: `navigated`, `dialog`, `dom`, `network`, `popup`, or **`none`**.
Inputs are filled with a battery derived from the field's declared type and
constraints; forms are submitted once empty-required and once with valid values.

Dropdowns are audited: an option whose visible label is a UUID or ULID is reported,
as are UUID option values and a select with nothing to choose. Links are checked by
clicking them, so both 4xx/5xx targets and SPA "soft 404s" (HTTP 200 with "page not
found" text) are caught, and every link the crawler refuses is listed with its
reason under **Links not crawled**.

File inputs are exercised with a real file: a fixture from `[uploads]`, or a small
valid file synthesized to match the input's `accept` attribute. Change handlers,
client validation and the submit path all run. Pages opened in a new tab are
captured too: status, console errors, overflow, screenshot, and attributed to the
control that opened them. Every page records two timings: the browser load event,
and the settled time when the quiet-network and quiet-DOM criteria hold.

It does not prove business rules are correct. It proves the affordances it could
reach did not crash, fail, or go inert, and it prints what it did not test.

`afford checks` prints every check the tool performs, with its code, severity and
reason.

## What a run writes

Under `.afford/runs/<run-id>/`:

- `report.html`: findings, evidence, and a screenshot of every page
- `report.json`: the same data for machines
- `summary.md`: a short Markdown digest
- `junit.xml`: for CI test reporters
- `results.sarif`: SARIF 2.1.0 for code scanning uploads
- `run.jsonl`: the append-only event log the report is built from
- `screenshots/`: full-page PNG per page, with password fields masked

`report.html` leads with severity counts and, past eight findings, a **Start here**
list of the three worst with their first piece of evidence. Then the findings table:
severity, code, what it means, the page, the control, and the payload or effect that
produced it. Expanding a row shows why the check matters, the console/HTTP
evidence, the structured detail, every other page carrying the same defect, and a
popup screenshot where one exists. Below that: a filterable page table (thumbnail,
status, load, settled, controls, probes, findings, where clicking a count filters
the findings), visual diffs side by side, links not crawled with reasons, and an
explicit list of what was not tested.

Each run also appends one line to `.afford/history.jsonl`: counts, load and settle
percentiles, duration, and how the run moved against the previous one for the same
base URL. `afford history` lists them.

## Safety

Clicks mutate state, and that is the first thing to understand about the tool.

- Point it at staging, a preview deployment, or a disposable instance. Not at
  production with mutation grants on.
- Form submission is a grant (`[safety] submit_forms`). Deletion and other
  irreversible actions are separate, explicit grants, and every skipped control is
  reported with the flag that would allow it.
- `--allow-delete`: click controls that delete or erase data
- `--allow-side-effects`: click pay/send/publish/approve/logout controls
- `--allow-destructive`: both grants at once
- `--no-submit`: fill and validate without ever submitting a form
- `--max-pages`, `--max-depth`, `--timeout`: hard budgets
- `[safety] mutation_hosts`: a production guard. A mutating run against a host that
  is not allowlisted is a configuration error (exit 2) before the browser starts.
  Read-only runs are not restricted.
- Links off-origin or excluded by `[scope]` are recorded, never followed
- Password inputs are masked in every screenshot, popups included

## Reports that survive a second run

`afford run --accept` records today's findings as the baseline; later runs fail only
on **new** ones. Control findings are re-probed and labelled `confirmed` or
`unconfirmed` (unconfirmed never gates). A suppression file mutes a finding with an
owner, a reason and an expiry, and raises `E_SUPPRESSION_EXPIRED` when the rule
lapses. `[readiness]` declares what "rendered" means per route, and a mid-run logout
is recovered by logging in again.

## Authentication

```toml
[auth]
mode = "form"            # none | form | basic | bearer | storage
login_url = "/login"
# username_field = "input[name=email]"
# password_field = "input[type=password]"
```

Credentials come from a TTY prompt, from `AFFORD_USERNAME` / `AFFORD_PASSWORD`, or
from a saved `--storage-state` file. They never enter argv, the report, or the run
log. If every navigation lands back on a login path, the run emits `E_AUTH_WALL`
at blocker severity rather than reporting a clean one-page crawl.

Role coverage takes `[[auth.profiles]]`: each profile logs in, reloads every page
the primary role found, and reports a route that loads despite being listed under
`deny` (`E_AUTHZ_REACHABLE`), plus an informational diff of which controls each
role sees.

## Visual regression

```sh
afford run --visual              # compare against .afford/baselines
afford run --update-baseline     # accept the current rendering as the reference
```

The first `--visual` run seeds a baseline per route template and viewport and
reports `E_VISUAL_BASELINE_WRITTEN`. Later runs report `E_VISUAL_DIFF` with the
percentage of changed pixels, a bounding box, and baseline/current/diff images side
by side in the report.

```toml
[visual]
enabled = false
threshold_pct = 0.1        # percent of pixels that may differ before it is a finding
tolerance = 12             # per-channel difference treated as identical
max_diff_pixels = 0        # absolute floor, 0 disables
mask = [".timestamp", ".live-indicator"]   # hidden from the screenshot
ignore = ["/admin/**"]
dir = ".afford/baselines"
```

A baseline is an artefact of one browser on one platform. Font rendering and
antialiasing differ across machines, so generate and compare baselines on the same
machine and OS. Pages that repaint on a timer are caught: each page is captured
twice, and a page whose two captures disagree is reported as not compared rather
than as a diff. Mask anything that legitimately changes between runs.

## More than clicking

Per page: leaked `undefined`/`NaN` text, broken images, unhandled rejections, the
axe-core subset and the Tab pass. Per run: extra viewports (`[run] viewports`),
role profiles over declared `deny` routes (`[[auth.profiles]]`), routes named by a
sitemap or framework manifest that no link reaches, fixture hooks (`[fixtures]`
setup and teardown with `AFFORD_RUN_ID`), SARIF output, 429 backoff, and run
history.

## Configuration

`afford init` writes a starter `afford.toml`. The interesting knobs:

- `[run] depth`: `smoke` (one payload per input), `standard` (boundaries and
  unicode), `deep` (injection sentinels)
- `[run] viewports`: extra `[width, height]` pairs, captured after the primary size
- `[run] a11y`, `[run] keyboard`: switch the slower page audits off
- `[limits] concurrency` and page budgets
- `[scope]` include and exclude globs
- `[uploads]` file fixtures, `[popups] mode`
- `[safety]` grants and `mutation_hosts`
- `[readiness]` per-route render conditions
- `[routes]` enumeration, `[fixtures]` hooks, `[privacy] mask`
- `[visual]` baseline settings

The [user guide](docs/user-guide.md) documents every key.

## MCP: let an agent check one page

```sh
afford mcp --config afford.toml      # stdio; no browser starts until the first call
```

Six tools: `inspect_page` (capture only, never interacts), `check_page` (click,
fill, optionally submit), `crawl_site` (bounded whole-site sweep that writes a
report), `list_runs`, `get_run`, and `get_screenshot` (serves PNGs only from the
output directory). Results carry structured findings, probe counts and the path to
a screenshot.

The safety posture belongs to the server, not the caller: deletion needs
`--allow-delete`, other irreversible actions need `--allow-side-effects`, forms are
not submitted unless a call asks, the URL host allowlist blocks an agent from
aiming the browser at arbitrary hosts, and credentials come from the server's
environment rather than tool arguments. Wiring, arguments and the agent flow are in
[`docs/mcp.md`](docs/mcp.md).

## CI

```sh
afford install
AFFORD_USERNAME=… AFFORD_PASSWORD=… \
  afford run --config afford.toml --no-prompt \
    --out artifacts/afford --fail-on critical --timeout 900
```

Exit codes are the contract:

| code | meaning |
|---|---|
| 0 | no findings at or above `--fail-on` |
| 1 | findings at or above `--fail-on` (artifacts still written) |
| 2 | usage or configuration error |
| 3 | the run could not complete (browser, credentials, zero pages captured) |

`--fail-on` accepts `blocker`, `critical` (default), `major`, `minor`, `info`, or
`never`. A run that captures no page exits 3: a clean report from a crawl that saw
nothing is the failure mode this tool exists to avoid. See [`docs/ci.md`](docs/ci.md).

## Development

```sh
make build
make test        # unit tests
make canary      # the tool's own regression gate, about 48 minutes
make mcp-smoke   # JSON-RPC over stdio against the shipped binary
make ci          # llocal-ci: format, vet, build, test, mcp-smoke, canary
```

The canary fixture is an app with planted defects and a ground-truth table in
`fixtures/canary/GROUND_TRUTH.md`. Every gate in `ci.toml` declares a `falsify`
mutation that must make it fail, and `llocal-ci --falsify` proves each one. If
`make canary` fails, afford broke.

Contributor notes, invariants and the recipe for adding a check are in
[`docs/contributing.md`](docs/contributing.md). Design decisions and rejected
alternatives are in [`DESIGN.md`](DESIGN.md) and
[`docs/architecture.md`](docs/architecture.md).

## Status

0.1.0. Finding codes and the report schema are stable enough to build on; flags and
TOML keys may still move between minor versions. Issues and pull requests are
welcome. For a security problem, use the repository's private vulnerability
reporting rather than a public issue.

## License

MIT. See [LICENSE](LICENSE).

Bundled third-party code: axe-core 4.10.2, MPL-2.0, shipped unmodified and
injected at runtime for the accessibility rules. Provenance and licence text are
recorded in [`sources/INDEX.md`](sources/INDEX.md).
