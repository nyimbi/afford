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

## Safety

Clicks mutate state. By default the tool refuses to activate controls whose names
match a destructive vocabulary (`delete`, `reset`, `pay`, `send`, `publish`,
`logout`, …) and reports them as skipped so coverage stays honest.

- `--allow-destructive` — opt in, on a disposable environment only
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
unicode; `deep` — injection sentinels), `[limits] concurrency`, and
`[safety] allow_destructive`.

## Development

```sh
make test      # unit tests
make canary    # the tool's own regression gate against fixtures/canary
make vet
```

The canary fixture is an app with ten planted defects and a ground-truth table in
`fixtures/canary/GROUND_TRUTH.md`. If `make canary` fails, afford broke.
