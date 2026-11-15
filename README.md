# afford

Crawl a running web app, exercise every interactive affordance, and report what breaks.
Unit tests prove functions; this proves the wiring a user actually touches.

```sh
go build -o bin/afford ./cmd/afford
./bin/afford install                      # once: fetch the Playwright browser
./bin/afford init --base-url http://localhost:3000
./bin/afford run --config afford.toml
```

Output per run, under `.afford/runs/<run-id>/`:

- `report.html` — findings, evidence, and a screenshot of every page
- `report.json` — the same data for machines
- `junit.xml` — for CI test reporters
- `summary.md` — a short Markdown digest
- `run.jsonl` — the append-only event log the report is built from
- `screenshots/` — full-page PNG per page

## What it does

For each reachable page it captures console errors, uncaught exceptions, 4xx/5xx
responses, failed requests, blank renders, framework error screens and horizontal
overflow. Then, from a fresh load each time, it clicks every enabled control and
classifies what happened: `navigated`, `dialog`, `dom`, `network`, `popup`, or
**`none`** — a no-op on an enabled button is the highest-value finding the tool
produces. Inputs are filled with a battery derived from the field's own declared
type and constraints; forms are submitted once empty-required and once with valid
values.

Dropdowns are audited: an option whose visible label is a UUID or ULID is reported,
as are UUID option values and a select with nothing to choose. Links are checked by
clicking them, so both 4xx/5xx targets and SPA "soft 404s" — HTTP 200 with "page not
found" text — are caught, and every link the crawler refuses is listed with its reason
in **Links not crawled**.

File inputs are exercised with a real file: a fixture from `[uploads]`, or a small valid
file synthesized to match the input's `accept` attribute. Change handlers, client
validation and the submit path all run. Pages opened in a new tab are captured too —
status, console errors, overflow, screenshot — and attributed to the control that opened
them. Every page gets a full-page screenshot and two timings:
the browser load event, and the settled time when afford's quiet-network and
quiet-DOM criteria are met.

With `--visual`, each page's screenshot is compared against a baseline keyed by route
template and viewport. See [Visual regression](#visual-regression).

`report.html` leads with severity counts and, past eight findings, a **Start here**
list of the three worst with their first piece of evidence. Then the findings table:
severity, code, what it means, the page, the control, and the payload or effect that
produced it. Expanding a row shows *why* the check matters, the console/HTTP evidence,
the structured detail, every other page carrying the same defect, and a popup
screenshot where one exists. Below that: a filterable page table (thumbnail, status,
load, settled, controls, probes, findings — clicking a count filters the findings),
visual diffs side by side, **Links not crawled** with reasons, and an explicit list of
what was not tested. `afford checks` prints every check the tool performs.

It does not prove business rules are correct. It proves the affordances it could
reach did not crash, fail, or go inert, and it prints what it did not test.

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

## Visual regression

```sh
afford run --visual              # compare against .afford/baselines
afford run --update-baseline     # accept the current rendering as the reference
```

The first `--visual` run seeds a baseline per route template and viewport and reports
`E_VISUAL_BASELINE_WRITTEN`. Later runs report `E_VISUAL_DIFF` with the percentage of
changed pixels, a bounding box, and baseline/current/diff images side by side in the
report. Tuning:

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
antialiasing differ across machines, so generate and compare baselines in the same
image — a container in CI is the usual answer. Mask anything that legitimately changes
between runs; a visual gate that cries wolf gets switched off.

## MCP — let an agent check one page

```sh
afford mcp --config afford.toml      # stdio; no browser starts until the first call
```

Three tools: `inspect_page` (capture only, never interacts), `check_page` (click, fill,
optionally submit), `crawl_site` (bounded whole-site sweep that writes a report). Results
carry structured findings, probe counts and the path to a screenshot.

The safety posture belongs to the server, not the caller: deletion needs
`--allow-delete`, other irreversible actions need `--allow-side-effects`, forms are not
submitted unless a call asks, the URL host allowlist blocks an agent from aiming the
browser at arbitrary hosts, and credentials come from the server's environment rather
than tool arguments. Wiring, arguments and the agent flow are in
[`docs/mcp.md`](docs/mcp.md).

## Safety

Clicks mutate state. Creation is governed by form submission; deletion and other
irreversible actions are separate, explicit grants, and every skipped control is
reported so coverage stays honest.

- `--allow-delete` — click controls that delete or erase data
- `--allow-side-effects` — click pay/send/publish/approve/logout controls
- `--allow-destructive` — both grants at once, on a disposable environment only
- `--no-submit` — fill and validate without ever submitting a form
- `--max-pages`, `--max-depth`, `--timeout` — hard budgets
- Links off-origin or excluded by `[scope]` are recorded, never followed

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
nothing is the failure mode this tool exists to avoid. See `docs/ci.md` for a
complete GitHub Actions job.

## Configuration

`afford init` writes a starter `afford.toml`; every key is documented in
`cmd/afford/main.go` usage and `internal/config/config.go`. The interesting knobs
are `[run] depth` (`smoke` — one payload per input; `standard` — boundaries and
unicode; `deep` — injection sentinels), `[limits] concurrency`, `[uploads]` for file
fixtures, `[popups] mode` (`capture` or `off`), `[visual]`, and the `[safety]` grants.

## Documentation

- [User guide](docs/user-guide.md): install, configure, run, read reports, use MCP, run in CI.
- [Contributor guide](docs/contributing.md): invariants, adding a check, testing and debugging.
- [Architecture](docs/architecture.md): components, data flow, probing, oracles, security and trade-offs.
- [CI](docs/ci.md) and [MCP](docs/mcp.md): focused references.

## Development

```sh
make test        # unit tests
make canary      # the tool's own regression gate against fixtures/canary
make mcp-smoke   # speaks MCP over stdio to the binary and asserts tool behaviour
make vet
```

The canary fixture is an app with ten planted defects and a ground-truth table in
`fixtures/canary/GROUND_TRUTH.md`. If `make canary` fails, afford broke.
