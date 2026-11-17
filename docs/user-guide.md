# afford user guide

afford opens a running web application in a real browser, walks every page it can
reach, exercises every interactive control it finds, and writes a report of what
broke. It is a smoke test at the level of affordances: buttons, inputs, forms,
links, dropdowns, uploads and popups, checked against a browser rather than a
test double.

It does not test business rules. It proves that the things a user can touch
render, respond, and fail visibly when they fail.

- [Before you start](#before-you-start)
- [Install](#install)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [Safety and mutation grants](#safety-and-mutation-grants)
- [What afford checks](#what-afford-checks)
- [Reading the report](#reading-the-report)
- [Pages, screenshots and timings](#pages-screenshots-and-timings)
- [Visual regression](#visual-regression)
- [File uploads](#file-uploads)
- [Popups](#popups)
- [Links not crawled](#links-not-crawled)
- [MCP: letting an agent check a page](#mcp-letting-an-agent-check-a-page)
- [CI](#ci)
- [Troubleshooting](#troubleshooting)
- [Limits and non-goals](#limits-and-non-goals)

## Before you start

You need:

- Go 1.22 or newer to build the binary.
- A running application the browser can reach, usually on localhost or a preview
  deployment.
- Credentials for that application if the pages you care about sit behind a login.
- A disposable database if you intend to submit forms or grant mutation
  permissions. A click can create a record; a submit can charge a card.

The tool controls a real Chromium, Firefox or WebKit browser. It is not a load
generator and it does not attack the app. It behaves like an impatient user who
clicks everything and writes down what happened.

## Install

```sh
git clone <repository> afford
cd afford
go build -o bin/afford ./cmd/afford
./bin/afford install
```

`afford install` downloads the Playwright browser binaries once. Run it again
after upgrading the Playwright Go module, because the driver and browser
revisions are pinned together.

To put the binary on your PATH:

```sh
go install ./cmd/afford
```

Check the build:

```sh
afford version
afford checks
```

## Quick start

Generate a configuration file next to the application you want to test:

```sh
afford init --base-url http://localhost:3000
```

Run a crawl:

```sh
afford run --config afford.toml
```

If the app has a login page, set the credentials in the environment. The tool
prompts on a terminal when they are missing, and fails clearly when it cannot
prompt:

```sh
export AFFORD_USERNAME=qa@example.com
export AFFORD_PASSWORD='…'
afford run --config afford.toml
```

Artifacts land under `.afford/runs/<run-id>/`. Open `report.html` first.

For a single page, or for a programmatic caller, use the MCP server described
below. For CI, read [CI](#ci).

## Configuration

afford reads a TOML file. Every key has a default, so a file with only
`[run] base_url` is valid. Command line flags override the file.

```toml
[run]
base_url = "http://localhost:3000"
depth = "standard"
concurrency = 3
```

### `[run]`

| key | default | meaning |
|---|---|---|
| `base_url` | required | Seed URL. Its origin is the crawl's origin. |
| `out` | `.afford` | Artifact root. Runs go under `runs/<run-id>/`. |
| `depth` | `standard` | Payload battery: `smoke`, `standard` or `deep`. |
| `headful` | `false` | Show the browser window. Useful when debugging a probe. |
| `viewport` | `[1280, 900]` | Viewport size in pixels. Used by screenshots, overflow checks and visual baselines. |
| `embed_images` | `false` | Inline screenshots in the HTML report as data URIs. |
| `browser` | `chromium` | `chromium`, `firefox` or `webkit`. |
| `timeout_secs` | `900` | Wall clock budget for the whole run. `0` disables it. |

### `[scope]`

| key | default | meaning |
|---|---|---|
| `include` | `["/**"]` | Path globs the crawler may visit. `**` crosses path segments, `*` does not. |
| `exclude` | `["/logout", "/signout", "/sign-out", "/api/**"]` | Path globs the crawler must not visit. Exclusion wins over inclusion. |
| `allow_hosts` | `[]` | Additional hosts the crawler may visit. The origin must still match the base URL's scheme. |

Scope governs crawling and link following. It does not stop an explicit MCP
`check_page` request to an allowed host, and it does not stop background requests
the page makes.

### `[auth]`

| key | default | meaning |
|---|---|---|
| `mode` | `none` | `none`, `form`, `basic`, `bearer` or `storage`. |
| `login_url` | `/login` | Login page path, resolved against `base_url`. |
| `username_field` | `""` | CSS selector. Empty means auto-detect. |
| `password_field` | `""` | CSS selector. Empty means `input[type=password]`. |
| `submit_selector` | `""` | CSS selector for the login button. Empty means the first submit control in the password field's form, then Enter. |
| `storage_state` | `""` | Path to a Playwright storage state file. Written after a form login; reused by `mode = "storage"`. |
| `success_url_contains` | `""` | Extra success condition for a form login. |
| `username_env` | `AFFORD_USERNAME` | Environment variable read first. |
| `password_env` | `AFFORD_PASSWORD` | Environment variable read first. For `bearer` mode this holds the token. |
| `extra_headers` | `{}` | Headers sent on every request, in every mode. Useful for preview-host bypass headers. |
| `login_path_patterns` | `["login", "signin", "sign-in", "auth", "sso"]` | Path segments treated as a login wall. |

See [Authentication](#authentication) for the modes.

### `[safety]`

| key | default | meaning |
|---|---|---|
| `submit_forms` | `true` | Submit forms with valid data. Set `false` for a read-only run. |
| `allow_delete` | `false` | Click controls whose names mean delete, erase, reset, purge and similar. |
| `allow_side_effects` | `false` | Click pay, send, publish, deploy, logout, approve and similar. |
| `allow_destructive` | `false` | Umbrella grant for both classes above. |
| `extra_destructive` | `[]` | Additional phrases treated as deletion. |
| `mutation_hosts` | built-in list | Host globs afford may write to. Default: localhost, loopback, private ranges, `*.test`, `*.local`, and staging/dev/qa/uat names. `["*"]` lifts the guard. |

With any write enabled (`submit_forms` or a grant), a base URL outside `mutation_hosts` exits 2 before a browser starts. Use `--no-submit` for a read-only run against production.

See [Safety and mutation grants](#safety-and-mutation-grants).

### `[readiness]`

A page is "ready" when it has rendered the thing a user came for. Without a
declaration afford uses a quiet-network, quiet-DOM heuristic; a page that renders
late fools it, so declare what ready means.

| key | default | meaning |
|---|---|---|
| `selector` | `""` | CSS selector that must be visible before the scan. |
| `loading` | `[]` | Selectors (spinners, skeletons) that must be gone. |
| `routes` | `{}` | Per-route overrides: `"/late" = "#late-ready"`. Keys are route templates. |
| `timeout_ms` | `8000` | How long to wait. Expiry raises `E_NOT_READY`; the scan still runs. |

### `[policy]`

| key | meaning |
|---|---|
| `severity` | Per-code override: `E_OVERFLOW_X = "off"` or `"critical"`. `off` drops the code. |
| `ignore_console` | Regexes; matching console messages are not findings. |
| `ignore_requests` | Regexes; matching failed or non-OK requests are not findings. |

### `[baseline]`

| key | default | meaning |
|---|---|---|
| `file` | `.afford/findings-baseline.json` | Accepted findings. |
| `suppress_file` | `afford.suppress.toml` | Rules that mute a finding on purpose. |
| `disabled` | `false` | Ignore both files (`--no-baseline`). |

See [Gating on what changed](#gating-on-what-changed).

### `[limits]`

| key | default | meaning |
|---|---|---|
| `max_pages` | `200` | Hard page budget. |
| `max_depth` | `4` | Maximum link depth from the seed. |
| `max_instances_per_template` | `3` | How many instances of `/orders/{id}` to visit. |
| `max_controls_per_page` | `80` | Cap on controls activated per page. |
| `max_forms_per_page` | `8` | Cap on forms probed per page. |
| `concurrency` | `3` | Worker contexts. Each worker has its own browser context. |
| `navigation_timeout_ms` | `15000` | Navigation timeout. |
| `probe_timeout_ms` | `8000` | Click, fill and submit timeout. |
| `settle_ms` | `1200` | Network settling budget. |
| `quiet_ms` | `350` | How long the DOM must stay unchanged before a page counts as settled. |

### `[mcp]`

| key | default | meaning |
|---|---|---|
| `allow_any_host` | `false` | Drop the URL host allowlist. Dangerous on a server an agent can drive. |
| `call_timeout_secs` | `120` | Per-call wall clock budget. |
| `max_controls` | `40` | Default control cap for `check_page`. |
| `default_read_only` | `false` | Make `check_page` behave like `inspect_page` unless `read_only` is explicitly false. |

### `[uploads]`

| key | default | meaning |
|---|---|---|
| `dir` | `""` | Directory for relative fixture names. |
| `default` | `""` | Fixture used when no rule matches. |
| `by_name` | `{}` | Map from input name or id to fixture. |
| `by_accept` | `{}` | Map from an `accept` token such as `image/*` or `application/pdf` to fixture. |

See [File uploads](#file-uploads).

### `[popups]`

| key | default | meaning |
|---|---|---|
| `mode` | `capture` | `capture` or `off`. |
| `max` | `5` | Popups inspected per action. |

### `[visual]`

| key | default | meaning |
|---|---|---|
| `enabled` | `false` | Compare screenshots against the baseline store. |
| `update_baseline` | `false` | Write the current rendering as the new baseline. |
| `threshold_pct` | `0.1` | Percentage of pixels that may differ before a finding is raised. |
| `tolerance` | `12` | Per-channel difference treated as identical. |
| `max_diff_pixels` | `0` | Absolute changed-pixel floor. `0` disables it. |
| `mask` | `[]` | CSS selectors hidden in the screenshot, for clocks, ads and live indicators. |
| `ignore` | `[]` | Path globs excluded from visual comparison. |
| `dir` | `.afford/baselines` | Baseline store. |

See [Visual regression](#visual-regression).

## Beyond the click: page audits, viewports, roles, routes

Each page also gets these checks. Switch the heavy ones off with `[run] a11y = false` and `[run] keyboard = false`.

| Check | Code | What it means |
|---|---|---|
| Rendered text | `E_RENDERED_GARBAGE` | `undefined`, `NaN`, `[object Object]`, `{{ x }}` or a stack trace in visible prose. Text inside `code` and `pre` is ignored. |
| Images | `E_BROKEN_IMAGE` | An image finished loading with zero pixels. |
| Promises | `E_UNHANDLED_REJECTION` | A rejection nothing caught, recorded by an init script from the first byte of the page. |
| axe-core | `E_A11Y_SERIOUS`, `E_A11Y_MINOR` | A bounded set of 21 axe rules (the embedded copy is archived in `sources/`). Serious and critical map to the first code, the rest to the second. |
| Keyboard | `E_KEYBOARD_TRAP`, `E_KEYBOARD_UNREACHABLE`, `E_NO_FOCUS_INDICATOR` | Tab never moves on, a visible control never receives focus, or focus changes nothing visible. Cross-origin frames are opaque and not judged. |

### Viewports

`[run] viewports = [[390, 800]]` revisits every page at each extra size. This pass only loads, scans for horizontal overflow and takes a screenshot; it clicks nothing. Baselines are keyed per route template and viewport, and an overflow finding carries `viewport` in its detail so it never merges with the primary-size finding.

### Roles

```toml
[[auth.profiles]]
name = "viewer"
mode = "form"
username_env = "VIEWER_USER"
password_env = "VIEWER_PASSWORD"
deny = ["/admin", "/admin/**"]
```

After the main crawl afford logs in as each profile and loads every page the primary role found that matches `deny`. A page that loads (no login redirect, status below 400, same URL) is `E_AUTHZ_REACHABLE`: the menu hides the route and the server does not. A profile that cannot log in is listed under not-tested, not skipped silently.

Pages that load for both roles but show different controls get `E_ROLE_AFFORDANCE_DIFF` (info): the detail names the control keys only the primary role sees (`only_primary`) and the ones only the profile sees (`only_<role>`). Read it to confirm each role is offered what it should be and nothing more. Pages the profile cannot load at all are counted under not-tested instead.

### Routes the crawl did not reach

```toml
[routes]
sitemap = true            # /robots.txt Sitemap lines, /sitemap.xml, nested sitemaps (same origin only)
manifest = true           # Next.js _buildManifest.js
extra = ["/admin/audit"]
```

A route named there that no link led to is `E_ORPHAN_ROUTE`, and it is then visited like any other page.

### Fixtures and run identity

```toml
[fixtures]
setup = "./scripts/seed.sh"
teardown = "./scripts/unseed.sh"
timeout_secs = 120
```

Both commands run under `sh -c` with `AFFORD_RUN_ID` and `AFFORD_BASE_URL` in the environment. A failing setup stops the run before the browser starts. Teardown runs even when the crawl fails. Every browser request carries `X-Afford-Run: <run id>`, so the server and its logs can tell test traffic apart.

### Rate limiting

A navigation answered with 429 waits for `Retry-After` (capped at 8 s, 2 s when absent) and retries up to twice. Any 429 raises `E_RATE_LIMITED` (minor); `still_limited` in its detail says whether the retries also failed. Lower `[limits] concurrency` if you see it.

### Privacy

Password inputs are masked in every screenshot, including popups and MCP captures. Add selectors with `[privacy] mask = ["#ssn"]`.

### Visual maintenance

With `[visual]` on, each page is captured twice 400 ms apart. If the two differ by more than `threshold_pct`, the page is reported as not compared and the reason names the moving region; a diff against an unstable page is noise. `update_pages = ["/pricing"]` limits `--update-baseline` to matching routes. Baselines older than `max_age_days` (default 90) are listed under not-tested.

### History and SARIF

Every run appends one line to `<out>/history.jsonl` (counts, load and settle p50/p95, duration) and prints how it moved against the previous run for the same base URL. `afford history [--out DIR] [-n N]` lists them. Each run directory also holds `results.sarif` (SARIF 2.1.0) for code-scanning upload.

## Running a crawl

```sh
afford run --config afford.toml [flags]
```

Frequently used flags:

| flag | effect |
|---|---|
| `--base-url URL` | Override `run.base_url`. |
| `--out DIR` | Override `run.out`. |
| `--depth MODE` | Override the payload battery. |
| `--max-pages N`, `--max-depth N` | Lower the budgets for a quick run. |
| `--concurrency N` | More workers, more parallel contexts. |
| `--auth MODE`, `--login-url PATH`, `--storage-state PATH` | Override the auth section. |
| `--username NAME` | Set the username without exporting the variable. The password still comes from the environment or a prompt. |
| `--no-submit` | Fill and validate forms without submitting them. |
| `--allow-delete`, `--allow-side-effects`, `--allow-destructive` | Grant mutation classes. |
| `--visual`, `--update-baseline` | Turn on screenshot comparison. |
| `--timeout SECONDS` | Wall clock budget. `0` disables it. |
| `--fail-on LEVEL` | Exit 1 at or above this severity. Default `critical`. `never` always exits 0. |
| `--no-prompt` | Fail instead of asking for credentials. Required in CI. |
| `--headful` | Show the browser. |
| `--embed-images` | Inline screenshots in the HTML. |
| `--quiet` | Suppress the console summary. Artifacts are still written. |

The run prints severity counts, coverage numbers, the list of things it did not
test, and the path to the report.

## Authentication

Five modes, selected with `[auth] mode`:

- `none`. No session. Public pages only.
- `form`. Navigate to `login_url`, fill the username and password fields, submit,
  and wait until the URL leaves the login path or the password field disappears.
  Auto-detection tries, in order, `input[autocomplete=username]`,
  `input[type=email]`, name or id containing `user` or `email`, then the first
  visible text input. Override the selectors when the app is unusual. On success
  the context's storage state is written to `storage_state`, defaulting to
  `.afford/state/session.json`, with mode `0600`.
- `basic`. HTTP basic credentials are attached to every context.
- `bearer`. The token comes from `password_env` and is sent as
  `Authorization: Bearer <token>`. `extra_headers` can supply other headers.
- `storage`. Reuse a storage state file saved earlier, from this tool or from
  Playwright's `storage_state`.

Credentials come from the TTY prompt, `AFFORD_USERNAME` and `AFFORD_PASSWORD`, or
the storage state file. They never enter argv, the report or the run log. The
storage state file is the only artifact that contains a session, so treat it like
a cookie jar.

If every navigation lands on a login path, the run raises `E_AUTH_WALL` at
blocker severity and exits non-zero. A crawl that never got past the login screen
must not report a clean one-page site.

The MCP server never prompts. A missing credential becomes a tool error that the
agent can report to its user.

## Safety and mutation grants

Clicks change state. afford separates three levels of mutating action and grants
them independently.

| action class | controlled by | examples |
|---|---|---|
| Creation or change through form submission | `submit_forms` / `--no-submit`; `submit: true` per MCP call | Save, Create, Checkout forms |
| Deletion or erasure | `allow_delete` / `--allow-delete` | Delete, Remove, Purge, Reset, Deactivate |
| Other irreversible effects | `allow_side_effects` / `--allow-side-effects` | Pay, Send, Publish, Deploy, Log out, Approve, Transfer |

`--allow-destructive` grants both deletion and side effects. A control that is
skipped still appears in the report as `E_DESTRUCTIVE_SKIPPED`, with the class it
belongs to and the flag that would allow it. The report therefore tells you what
it did not do, instead of quietly doing less.

The vocabulary matches whole words and phrases. `dropdown` is not destructive;
`drop` is. Multi-word entries match across spaces, hyphens and underscores, so
`sign out`, `sign-out` and `signout` are the same control.

Two more boundaries:

- Links are followed only within `[scope]`. Off-origin and excluded links are
  inventoried, not visited.
- The browser respects the app's own authentication and authorization. afford
  does not guess at roles. Run it once per role if you want per-role coverage.

## What afford checks

The catalogue is versioned in code. `afford checks` prints it worst-first, and
`afford checks --json` returns the same data with `code`, `severity`, `title` and
`why`.

### Blockers

| code | meaning |
|---|---|
| `E_AUTH_WALL` | Every navigation landed on a login path. The app was never seen. |
| `E_BLANK_PAGE` | A 2xx document rendered almost no text and fewer than three controls. |

### Critical

| code | meaning |
|---|---|
| `E_HTTP_5XX` | A document or subresource returned 5xx during capture. |
| `E_ERROR_BOUNDARY` | The app replaced its UI with an error screen. |
| `E_500_ON_ACTION` | A click caused a 5xx. |
| `E_500_ON_INVALID` | An invalid submit reached the server and got 5xx instead of a validation response. |
| `E_500_ON_VALID` | A submit filled with values the fields advertise as valid got 5xx. |
| `E_NAV_TO_NONOK` | A non-link control navigated to 4xx or 5xx. |

### Major

| code | meaning |
|---|---|
| `E_UNCAUGHT` | An uncaught JavaScript exception. |
| `E_CLIENT_ERROR_ON_ACTION` | A control's activation threw. |
| `E_DEAD_CONTROL` | An enabled button did nothing observable. |
| `E_DEAD_LINK` | A link target returned 4xx/5xx, or answered 200 with "page not found" text. |
| `E_FAILED_REQUEST_ON_ACTION` | A click caused a request that never produced a response. |
| `E_REQUEST_FAILED` | A request failed below HTTP during validation. |
| `E_HTTP_4XX` | A document or API call returned 4xx where success was expected. |
| `E_CONTROL_ERROR` | The control vanished, stayed disabled or was detached mid-action. |
| `E_OCCLUDED` | Another element covers the control, so a user cannot click it. |
| `E_FORM_SUCCESS_UNVERIFIED` | A valid submit produced no navigation, dialog, DOM change or error. |
| `E_VALIDATION_NOT_ENFORCED` | Submitting with required fields empty navigated or succeeded. |
| `E_NOT_DEEPLINKABLE` | A discovered link landed on a different route than the one it named. |
| `E_UPLOAD_FAILED` | A file fixture could not be attached. |
| `E_VISUAL_DIFF` | The page differs from its stored baseline beyond the configured tolerance. |

### Minor

| code | meaning |
|---|---|
| `E_CONSOLE_ERROR` | `console.error` was called. |
| `E_ALERT` | A native `alert`, `confirm` or `prompt` opened. |
| `E_EMPTY_HREF` | A link points to `#`, an empty string or `javascript:`. |
| `E_EMPTY_SELECT` | A visible, enabled dropdown has no selectable option. |
| `E_UUID_EXPOSED` | A dropdown option's visible label is a UUID or ULID. |
| `E_OVERFLOW_X` | Content is wider than the viewport. |
| `E_UNLABELED_CONTROL` | A form control has no accessible name. |

### Informational

| code | meaning |
|---|---|
| `E_AUTH_REDIRECT` | A page redirected to a login path during the crawl. |
| `E_DESTRUCTIVE_SKIPPED` | A control was quarantined by the permission model. |
| `E_FORM_NO_SUBMIT` | A form has no submit control. |
| `E_HARNESS_TIMEOUT` | The tool gave up waiting. A tool limitation, not a product defect. |
| `E_NEW_TAB` | A control opened a popup. |
| `E_NO_TITLE` | The document has no title. |
| `E_OUT_OF_SCOPE` | A link points outside the crawl scope. |
| `E_SCREENSHOT_FAILED` | The page was visited and no image was written. |
| `E_UUID_IN_OPTION_VALUE` | Dropdown option values are UUIDs or ULIDs while the labels are readable. |
| `E_VISUAL_BASELINE_WRITTEN` | A visual baseline was seeded or refreshed. |

Severity is a starting point for triage, not a claim about impact. A `minor`
contrast problem on a checkout page may matter more than a `major` dead control
on an admin screen.

## Gating on what changed

A legacy app has hundreds of findings; a gate that fails on all of them is switched
off within a week. afford gates in four layers, in this order:

1. **Confirmation.** A control finding (`E_DEAD_CONTROL`, `E_500_ON_ACTION` and
   similar) is probed a second time from a fresh load. If the second probe disagrees
   the finding is `unconfirmed`: reported, never gating. `[run] confirm = false`
   turns this off.
2. **Policy.** `[policy] severity` re-grades or drops a code.
3. **Suppression.** `afford.suppress.toml` mutes one finding on purpose:

   ```toml
   [[suppress]]
   code = "E_CONSOLE_ERROR"
   page = "/console"          # route glob, optional
   element = "*widget*"       # glob, optional
   reason = "vendor widget, tracked upstream"
   owner = "web-team"
   expires = "2027-03-01"
   ```

   `reason`, `owner` and `expires` are required. A rule past its expiry stops
   muting and raises `E_SUPPRESSION_EXPIRED`.
4. **Baseline.** `afford run --accept` records every current finding in
   `.afford/findings-baseline.json`. Later runs label each finding `new` or `known`
   and list baseline entries now `fixed`. Only new findings gate. Commit the file.

A finding's identity is its code, its normalized element and a per-code discriminator.
The page is not part of it, so renaming a route does not make old findings new; a
finding that spreads to a template outside its baseline entry is new. An entry counts
as fixed only if the run visited every page it lived on; otherwise it is "not reached".
After a run that hit its deadline nothing counts as fixed.

Sessions: each page visit gets a fresh browser context built from the saved login, so
client storage never leaks between pages. If a click ends the session (a logout
control with `--allow-side-effects`), afford logs in again, up to three times, and
probes controls that could do this last.

## Reading the report

A run writes five things:

| path | audience |
|---|---|
| `report.html` | A person. Self-contained, opens from the filesystem. |
| `report.json` | Machines. Summary, pages, findings and link inventory. |
| `junit.xml` | CI test reporters. |
| `summary.md` | A short digest for a commit comment or a chat message. |
| `run.jsonl` | The append-only event log the report is built from. |

Alongside them: `screenshots/`, `popups/` and `visual/` hold the images the report
links to.

### `report.html`

The page is ordered for triage:

1. Severity counts.
2. Coverage tiles: pages, screenshots taken, controls probed, forms, inputs,
   payloads, skipped links, visual mode, load and settled percentiles.
3. **Start here**, shown when there are more than eight findings. The three worst
   findings with their first piece of evidence.
4. **Findings**. Filter by text and severity. Each row gives severity, code, plain
   language title, page, control, and the payload or effect that produced it.
   Expanding the row shows why the check matters, the console and HTTP evidence,
   the structured detail, every other page carrying the same defect, and a popup
   screenshot when one exists.
5. **Pages**. A filterable table with a thumbnail, URL, route template and depth,
   status, load, settled, control count, probe count and findings. Clicking a
   finding count filters the findings table to that page. Clicking a thumbnail
   opens the full screenshot.
6. **Visual changes**, when visual mode produced diffs. Baseline, current and diff
   side by side.
7. **Links not crawled**, with the reason for each.
8. **Coverage limits**. What this run did not test, and what no run of this tool
   proves.

### `report.json`

```json
{
  "summary": {
    "run_id": "20261115T120515Z-ce7cba",
    "base_url": "http://localhost:3000",
    "started_at": "2026-11-15T12:05:15Z",
    "finished_at": "2026-11-15T12:07:03Z",
    "pages_discovered": 13,
    "pages_visited": 13,
    "pages_failed": 0,
    "controls_total": 164,
    "controls_probed": 150,
    "controls_skipped": 4,
    "forms_total": 5,
    "forms_probed": 5,
    "inputs_swept": 7,
    "payloads_sent": 47,
    "counts": {"blocker": 0, "critical": 2, "major": 8, "minor": 6, "info": 3},
    "not_tested": ["delete control (1)", "outside scope or off-origin (2)"],
    "visual_enabled": false
  },
  "pages": [],
  "findings": [],
  "links": []
}
```

Each finding has `code`, `severity`, `title`, `why`, `page_url`, optional
`element`, `probe_kind`, `payload`, `evidence`, `detail`, `also_on` and
`occurrences`. A finding seen on several pages is merged: `also_on` lists the other
pages and `occurrences` counts them.

Each page has `url`, `template`, `depth`, `parent`, `status`, `title`, `load_ms`,
`settled_ms`, `controls`, `screenshot`, `visited_at`, `probes` and
`auth_redirect`.

Each link record has `source`, `href`, `resolved`, `disposition` and `reason`.
Dispositions are `followed`, `excluded`, `off_origin`, `empty`, `javascript`,
`mail`, `tel` and `invalid`. Only non-followed links are recorded.

### `junit.xml`

One testcase per page, plus one `afford.run` testcase when a finding belongs to no
page, such as an auth wall. Blocker, critical and major findings make the testcase
fail; minor and informational findings are written to `system-out`. The suite's
`tests` attribute counts testcases and `failures` counts failing testcases, so the
file is valid JUnit.

### `summary.md`

Severity counts, coverage numbers, and a flat finding list. Suitable for a pull
request comment.

## Pages, screenshots and timings

Every visited page gets one full-page PNG, written before any interaction. The
screenshot therefore shows the page as it arrived, not as a click left it.

Two timings are recorded:

- `load_ms` is the browser's `loadEventEnd` from the Navigation Timing API. It is
  what a user's browser measures.
- `settled_ms` is wall clock from navigation start until afford's own stability
  criteria are met: load state reached, network quiet, and the DOM node count
  unchanged across quiet polls.

`settled_ms` includes the quiet windows, so it has a floor of roughly 700 to 900
milliseconds with the default settings. Compare pages against each other rather
than reading it as a render time. The report shows p50 and p95 for both.

A page that cannot be photographed gets `E_SCREENSHOT_FAILED`. The pages table
shows screenshots taken against pages visited, so a gap is visible rather than
silent.

## Visual regression

Visual mode compares each page's screenshot against a baseline.

```sh
afford run --visual              # compare; seed any missing baselines
afford run --update-baseline     # accept the current rendering as reference
```

Baselines are keyed by route template and viewport, not by full URL. `/orders/1042`
and `/orders/881` share one baseline because they are the same template. The key
is a hash of the template and viewport dimensions.

The first `--visual` run writes a baseline for each template it sees and reports
`E_VISUAL_BASELINE_WRITTEN` at info severity. Later runs compare. A comparison
fails when any of these is true:

- the image dimensions differ, which always counts as a change;
- the fraction of changed pixels exceeds `threshold_pct`;
- `max_diff_pixels` is set and the changed pixel count exceeds it.

Pixels are compared per channel against `tolerance`. The default tolerance of 12
absorbs antialiasing noise; a changed button label is well above it. A diff
writes three images into the run's `visual/` directory and shows them side by side
in the report: baseline, current, and the diff with changed pixels in magenta.

Mask anything that legitimately changes between runs:

```toml
[visual]
enabled = true
mask = [".timestamp", ".live-indicator", "[data-testid=avatar]"]
ignore = ["/admin/**", "/reports/{id}"]
```

A baseline is an artifact of one browser on one platform. Font rendering and
antialiasing differ across machines, so produce and compare baselines in the same
environment. A container in CI is the usual answer. If contributors on macOS
regenerate baselines and CI runs Linux, the gate will fail for reasons nobody can
fix.

The MCP server also honors `[visual] enabled`, so `check_page` can compare against
the same store.

## File uploads

A file input gets a real file attached with Playwright's `SetInputFiles`, so change
handlers, client validation and the server's upload path all run.

Fixtures resolve in this order:

1. `[uploads] by_name` matching the input's `name` attribute, then its `id`.
2. `[uploads] by_accept` matching an `accept` token, such as `image/*` or
   `application/pdf`.
3. `[uploads] default`.
4. A synthesized file chosen from the `accept` attribute: a two by two PNG, a
   minimal PDF, a small CSV, a small JSON document, or a text file.

Relative names are resolved against `[uploads] dir`. Absolute paths are used as
given. The browser never receives a path the operator did not choose.

```toml
[uploads]
dir = "fixtures/uploads"
default = "sample.pdf"

[uploads.by_name]
avatar = "avatar.png"

[uploads.by_accept]
"application/pdf" = "sample.pdf"
"image/*" = "photo.jpg"
```

A missing fixture produces `E_UPLOAD_FAILED`, not silence. The upload is also
attached before a form submit, so a server that rejects the file shows up as a 5xx
or a validation response like any other submit.

If a file input's handler throws when a file is chosen, the exception appears as
`E_CLIENT_ERROR_ON_ACTION`. That is how the tool's own canary proves uploads
happen.

## Popups

When a control opens a new tab, afford captures the popup instead of ignoring it.
With `[popups] mode = "capture"`, each popup is settled, scanned, screenshotted and
closed. Its findings are attributed to the opener page, with `popup_url` and a
screenshot path in the finding's detail. A popup that opens off-origin is recorded
as out of scope and not loaded.

`E_NEW_TAB` is always reported at info severity. A popup that renders a 404 raises
`E_DEAD_LINK`; a popup with console errors, overflow or a blank body raises the
corresponding capture findings.

Two limits are worth knowing:

- Popups are captured, not driven. afford does not click inside them.
- The event collector attaches after the popup exists, so console output emitted
  in the first few milliseconds may be missed.

Set `[popups] mode = "off"` if an app opens OAuth or payment windows and the extra
work is not useful.

## Links not crawled

Every anchor the crawler sees is classified once: followed, excluded by a scope
glob, off-origin, empty, `javascript:`, mail or tel. Only followed links enter the
crawl queue. The rest are stored with their source page, href, resolved URL and
reason, and listed under **Links not crawled** in the report.

`E_OUT_OF_SCOPE` is raised at info severity for `excluded` and `off_origin`, the two
classes an operator can change. Findings merge across pages, so a nav link that
points off-origin is one finding with a list of the pages carrying it.

`afford crawl_site` over MCP returns the same inventory as `skipped_links`, capped
at 50 records, plus `link_counts` by disposition.

## MCP: letting an agent check a page

```sh
afford mcp --config afford.toml
```

The server speaks MCP over stdio and starts no browser until the first tool call.
Wire it into Claude Code:

```sh
claude mcp add afford --scope user -- \
  /absolute/path/to/bin/afford mcp --config /absolute/path/to/afford.toml
```

Credentials come from the environment of the process that launches the server.
They are never tool arguments, because tool arguments appear in the host's
transcript.

### Tools

**`check_page`** opens one page, captures it, then exercises its affordances.

| argument | type | default | meaning |
|---|---|---|---|
| `url` | string, required | | Absolute http(s) URL. Must be the configured origin or an allowed host. |
| `read_only` | boolean | `false` | Capture only. No clicks, fills or submits. |
| `submit` | boolean | `false` | Submit forms with valid data. Mutates server state. |
| `depth` | string | `standard` | Payload battery: `smoke`, `standard` or `deep`. |
| `no_screenshot` | boolean | `false` | Skip the PNG. |
| `max_controls` | integer | `40` | Control cap for this call. |

**`inspect_page`** does the capture and nothing else. It never clicks, fills or
submits. Use it first when mutation is unacceptable.

**`crawl_site`** runs a bounded crawl and writes a report.

| argument | type | default | meaning |
|---|---|---|---|
| `max_pages` | integer | `25` | Page budget. |
| `max_depth` | integer | `2` | Link depth. |
| `depth` | string | server config | Payload battery. |
| `submit` | boolean | `false` | Submit forms during the crawl. |

### Results

Every result has `structuredContent` and a short text digest. The digest gives the
URL, status and title, severity counts, load and settled timings, probe counts,
one line per finding with its element and first evidence line, the screenshot path,
and notes about what was skipped. The digest caps at 20 findings; the structured
payload has all of them.

`check_page` and `inspect_page` return findings, counts, `load_ms`, `settled_ms`,
control and form counts, `ok` (no finding at major or worse) and notes.

`crawl_site` returns the run id, report path, summary, findings, page URLs,
`link_counts`, `skipped_links` and the five slowest pages.

### Safety

The server does not grant more than the CLI:

- Deletion needs `--allow-delete` at server start. Side effects need
  `--allow-side-effects`. `--allow-destructive` grants both.
- Forms are submitted only when a call passes `submit: true`. Inputs are still
  filled and validated.
- The URL allowlist admits the configured origin and `[scope] allow_hosts`. It
  exists so a prompt-injected agent cannot point a real browser at internal hosts.
  `--allow-any-host` removes it; do not use that on a server an LLM can drive.
- The server never prompts. A missing credential is a tool error.
- Probes are serialized. Concurrent calls against one app produce evidence nobody
  can attribute.
- `[mcp] call_timeout_secs` bounds each call.

A recommended agent flow: `inspect_page` to see whether the page renders, then
`check_page` with defaults, then `check_page` with `submit: true` only on
disposable data.

## CI

```sh
afford install
AFFORD_USERNAME=… AFFORD_PASSWORD=… \
  afford run --config afford.toml --no-prompt \
    --out artifacts/afford --fail-on critical --timeout 900
```

Exit codes:

| code | meaning |
|---|---|
| 0 | No findings at or above `--fail-on`. |
| 1 | Findings at or above `--fail-on`. Artifacts are still written. |
| 2 | Usage or configuration error. The run did not start. |
| 3 | The run could not complete: browser launch, authentication, or zero pages captured. |

A crawl that captures no page exits 3. A clean report from a crawl that saw
nothing is the failure mode this tool exists to avoid.

Artifacts are written before the exit code is decided, including on partial runs.
The `run.jsonl` log is append-only, so a crash mid-crawl leaves the pages already
recorded.

`docs/ci.md` has a complete GitHub Actions job. `docs/mcp.md` has MCP details.

## Troubleshooting

| symptom | likely cause | fix |
|---|---|---|
| `start playwright driver: … run afford install` | Browsers or driver missing for this Playwright version | `afford install` |
| `credentials not found` | No TTY and no environment variables | Export `AFFORD_USERNAME` and `AFFORD_PASSWORD`, or drop `--no-prompt` |
| Report says `E_AUTH_WALL` | Login did not establish a session | Check selectors, `success_url_contains`, and whether the app needs MFA |
| One page, zero findings | The crawler never left the seed, or the seed is a login page | Inspect `E_AUTH_WALL` and `E_OUT_OF_SCOPE`; run with `--headful` |
| `E_HARNESS_TIMEOUT` | A probe exceeded its budget | Raise `probe_timeout_ms` or narrow the run |
| Every page shows the same finding | Shared chrome, merged by the dedupe rule | Open the finding; `also_on` lists the pages |
| Visual diff on every page | Baselines came from another platform, or unmasked dynamic content | Regenerate in the same environment; add masks |
| `E_VISUAL_BASELINE_WRITTEN` every run | `update_baseline` is on, or the baseline directory is not writable or not persisted | Check `[visual] dir` and CI caching |
| `E_UPLOAD_FAILED` | Fixture missing or browser refused the file | Check `[uploads]`; run with `--headful` |
| CI passes when the app is down | You used `--fail-on never` | Use the default, or `critical` |

## Limits and non-goals

afford is a smoke test, not a specification.

- It does not know business rules. A button that saves the wrong amount is a
  working button to this tool.
- It does not prove data was stored correctly. It proves the request and the
  visible response.
- It samples page states. A route with ten data-driven variants gets at most a few
  instances, and the report says how many.
- It does not test pages it cannot reach. Role profiles replay only the pages the
  primary role found; a feature flag or a route nothing names stays outside the run.
- It does not drive popups, file pickers or native dialogs beyond the synthetic
  interactions described here.
- Visual comparison is platform-specific and must be maintained. A stale baseline
  is worse than no baseline.
- Accessibility coverage is a bounded axe-core rule set plus a Tab pass. Automated
  checks find a minority of real accessibility problems; this is not an audit.
- The reports state what was skipped. Read that section before trusting a clean
  run.

