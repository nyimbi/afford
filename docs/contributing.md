# Contributing to afford

This guide covers building the tool, the rules the code depends on, and the
recipe for adding a check without breaking the ones already there.

## Setup

You need Go 1.22 or newer. The canary fixture is a Python application, so you
also need Python 3.11 with FastAPI and Uvicorn installed; it is the only Python
in the repository and it exists to be tested.

```sh
go build -o bin/afford ./cmd/afford
./bin/afford install
```

`afford install` downloads the Playwright browsers. The Go module pins a driver
version, and the browser revisions must match it, so rerun this after upgrading
`playwright-go`.

## Commands

| command | what it does |
|---|---|
| `make build` | Build `bin/afford`. |
| `make test` | `go test ./...`. |
| `make vet` | `go vet ./...`. |
| `make canary` | Start the fixture app, run afford against it, assert every planted defect is found, then prove `--allow-delete` works. |
| `make mcp-smoke` | Drive the shipped binary over MCP: list tools, check a page, inspect a page, refuse an off-origin URL, crawl two pages. |
| `make fmt` | `gofmt -w` over `cmd` and `internal`. |
| `make install` | `go install ./cmd/afford`. |

`go build ./...` checks compilation but does not touch `bin/afford`. After any
change you intend to run, use `make build`. A stale binary once produced a review
full of fixes that were not actually there.

## Layout

| path | responsibility |
|---|---|
| `cmd/afford` | Command parsing, flag overrides, exit codes. |
| `internal/model` | Domain types and the finding catalogue. No dependencies on other internal packages. |
| `internal/config` | TOML loading, defaults, validation, permission predicates. |
| `internal/discover` | URL normalization, route templates, scope globs, link dispositions, the DOM scan JavaScript. |
| `internal/payloads` | Field classification, valid values, payload batteries, destructive vocabulary. |
| `internal/browser` | Playwright host, contexts, event collector, settling, screenshots. |
| `internal/auth` | Credential resolution, form login, storage state. |
| `internal/probe` | The browser-driving layer: one action or one form, returning an observation. |
| `internal/oracles` | Rules that turn an observation into findings. No browser access. |
| `internal/check` | Single-page checker used by MCP. |
| `internal/run` | The crawl loop, findings orchestration, summary. |
| `internal/store` | Append-only JSONL event log and in-memory replay. |
| `internal/visual` | Baseline store and pixel diff. |
| `internal/uploads` | Fixture resolution and synthesized files. |
| `internal/sanitize` | Masks credential-looking query values in recorded URLs. |
| `internal/report` | HTML, JSON, JUnit and Markdown writers. |
| `internal/mcpserver` | MCP tools over stdio. |
| `fixtures/canary` | The app with planted defects and its ground-truth table. |
| `scripts` | `canary.sh` and `mcp_smoke.sh`. |

Dependencies point inward. `model` and `config` depend on nothing internal.
`discover`, `payloads` and `visual` depend on `model` and `config`. `browser`,
`probe` and `oracles` sit above them. `run`, `check` and `mcpserver` orchestrate.
`report` reads the store and the model. No package imports `run` or `mcpserver`
except `main`.

## Invariants

These are not preferences. Each one exists because breaking it produced a wrong
report or a silent pass.

**Oracles decide, probes drive.** `internal/oracles` takes an observation and
returns findings. It never touches a browser. `internal/probe` drives the browser
and never assigns severity. If a rule needs a new fact, add the fact to
`model.Observation`, capture it in `probe`, and decide in `oracles`.

**Every finding code is catalogued once.** Codes live in `model.specs`. `NewFinding`
panics on an unknown code, which is deliberate: an uncatalogued finding is a bug
in the tool, not a runtime condition. Remove codes that stop firing; a code nobody
can emit is dead weight that makes `afford checks` lie.

**One collector per probe.** `Collector.Reset()` runs at the top of every probe.
An observation that inherits the previous probe's console errors reports the same
exception on every control of the page.

**Load-time events belong to capture.** After navigation, take a baseline snapshot
of the collector. An action observation reports only events after that baseline.
Otherwise a page that throws on load reports a client error for every click on it.

**Findings merge on a key, not on a page.** `model.Finding.Key()` combines the code,
the element, and the identifying detail (`url`, `status`, `error`, `requests`,
`popup_url`, visual baseline `key`). A nav bug present on twelve pages is one
finding with `also_on` listing the pages. If a new finding is inherently
page-specific, its detail must contain something that distinguishes pages, or it
will merge.

**Permission classes are separate.** Deletion, side effects and form submission
are distinct grants. `payloads.DestructiveKind` returns `delete`, `side_effect` or
empty. Matching is on word boundaries: substring matching quarantined a nav link
called "dropdown" because it contains "drop".

**Evidence is part of the finding.** A finding without evidence is a rumour.
Findings derived from a probe observation fill `Evidence`, with the actionable line
first: exception, then HTTP status, then console, then effect. Inventory and policy
findings carry their facts in `Detail` instead, because there is no observation to
quote.

**The crawler and the checker agree.** `internal/run` and `internal/check` use the
same oracles, payloads and probe drivers. A rule added to one path and not the
other produces a report and an agent tool that disagree.

**MCP failures return Go errors, not zero structs.** The MCP SDK validates
structured output against the inferred schema even when the handler returns an
error result. Returning a zero `Out` with nil maps fails the call and hides the
real message. Return `nil, Out{}, err`.

**Never widen safety in the MCP server.** Server flags grant capability. Tool
arguments may narrow it. A per-call argument must not be able to enable deletion
that the operator did not grant at startup.

**Reports are deterministic.** No random payloads, no map iteration in output, no
timestamps inside compared content. A report you cannot reproduce is a rumour.

## Adding a check

Work through these steps in order. The canary is what tells you the check fires.

1. **Declare it.** Add a `FindingSpec` to `model.specs` with a code, severity,
   title and a `why` that explains the risk to someone who has never seen the
   code. Pick severity by impact on a user, not by how hard it was to detect.

2. **Decide where the rule lives.**
   - Capture-level and action-level predicates: `oracles.Evaluate`.
   - Inventory audits over the scan: functions such as `Unlabeled`, `EmptyHref`,
     `OptionsAudit`.
   - Form-specific rules that need the mode and the accepted flag:
     `probe.FormFindings`.
   - Findings that need the run or the checker (visual comparison, screenshot
     failure, popup attribution): `run` and `check`.

3. **Make the fact observable if it is not.** Add a field to `model.Observation`,
   `model.Element` or `model.ScanResult`. If it comes from the DOM, extend the scan
   in `discover.ScanJS` and the Go struct together. The JavaScript runs once per
   scan and returns one JSON object.

4. **Emit with the catalogue.** `model.NewFinding(code, pageURL)` fills severity and
   prose. Set `Element`, `Detail`, `Evidence` and `Payload`. Do not construct a
   `Finding` literal with a severity chosen at the call site.

5. **Wire both paths.** Add the call in `run` and the equivalent in `check`. For
   action findings, set the payload to `effect=<effect>`; for validation findings,
   `<label>=<value>`; for forms, `submit=<mode>`.

6. **Plant it in the canary.** Add a route or control to `fixtures/canary/app.py`,
   a row to `fixtures/canary/GROUND_TRUTH.md`, and the code to the `expected` set
   in `scripts/canary.sh`. If the defect has a distinguishing attribute, assert
   that too, as the script does for `kind`, `popup_url` and the upload handler.

7. **Unit-test the rule if it is a pure function.** Build an `Observation` or an
   `Element` in Go and call the oracle. `internal/oracles/oracles_test.go` has
   examples for options, dead links and soft 404s.

8. **Run the gates.**

   ```sh
   make build
   go test ./...
   make canary
   make mcp-smoke
   ```

9. **Document it.** Add a row to the catalogue in `docs/user-guide.md`. If it
   changes behavior, update the README and, when relevant, `DESIGN.md`.

### Worked example: a check for disabled buttons

`E_DEAD_CONTROL` covers enabled controls that do nothing. A separate check for a
control that is enabled but has `aria-disabled="true"` would live in
`oracles.Evaluate` under the action context, because the scan already exposes
`Disabled`. Declare `E_ARIA_DISABLED_ACTION` in `model.specs`, add the predicate,
plant a button in the canary with that attribute and a handler, and assert the
code. The control is still clicked: it is enabled, so the question is whether the
app treats it as usable.

## Adding a page audit

A page audit inspects the live page and returns findings, so it belongs in `internal/audit` and is called from `internal/pass` (never from `run` or `check` directly). Steps:

1. Register the code in `specs` in `internal/model/model.go` with a severity, title and why. `NewFinding` panics on an unregistered code.
2. Write the audit as a function returning `[]model.Finding`. Return nothing on an evaluation error; record coverage instead of guessing.
3. Add a config switch only if the audit is slow or noisy (`a11y`, `keyboard` are the precedent).
4. Plant the defect on a page in `fixtures/canary/app.py`, plus a control page that must stay clean (`/code-sample` for text leakage). Assert both in `scripts/canary.sh` and list the page in `GROUND_TRUTH.md`.
5. If two findings differ only by a detail (viewport, role), add that detail name to `Finding.Key()` or they merge into one finding with `also_on`. Canary assertions on merged findings read `also_on` as well as `page_url`.

## Adding an extra pass

Passes that revisit pages (viewports, roles, routes) live in `internal/run/extras.go`. They load and scan but never click, so they cannot violate the mutation grants. Record every page they cannot evaluate under not-tested with a reason.

## Adding a field type or payload

Field classification lives in `payloads.InferKind`, which prefers the declared
`type` and falls back to name, label, placeholder and autocomplete. Valid values
are in `ValidValue`; the battery is in `Sweep`.

For a new semantic kind:

1. Add a pattern to `semantics`, ordered so broader patterns come later.
2. Add a valid value in `ValidValue` that satisfies the field's own constraints.
3. Add boundary and malformed payloads in `Sweep` for the new kind.
4. Add a case to `IsTextLike` if the sweep should fill it.
5. Unit-test inference and the battery in `payloads_test.go`.

If the browser cannot fill the control, as with a file input, it needs a probe
path rather than a payload. File inputs are handled in `probe.ProbeControl` and in
the form fill loop, with fixture bytes from `internal/uploads`.

## Adding a configuration option

1. Add the field to the relevant struct in `internal/config/config.go` with a
   `toml` tag.
2. Set the default in `Default()`.
3. Add validation in `Validate()` if the value has a valid range.
4. Add it to `Example`, which `afford init` writes.
5. Read it somewhere. A key nobody reads is worse than no key, because the docs
   will claim it works.
6. Add a row to the configuration tables in `docs/user-guide.md`.
7. Add a unit test in `config_test.go` if it participates in a predicate.

## Extending the MCP server

Tools live in `internal/mcpserver/mcpserver.go`.

1. Define an input struct with `json` tags for the wire and `jsonschema` tags for
   the description the agent sees. Required fields have no `omitempty`.
2. Define an output struct. It becomes `structuredContent` and the SDK validates
   it, so every map and slice must be non-nil on success.
3. Write the handler with the signature
   `func(ctx, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)`.
4. Call `s.ensure()` first. It starts the browser and authenticates lazily and
   caches the result.
5. Wrap the work in `s.callContext(ctx)` to apply `[mcp] call_timeout_secs`.
6. Respect the server's grants. Do not add a tool argument that widens them.
7. Return a text digest as `Content` and the full data as `Out`. Agents read the
   digest first.
8. Register it with `mcp.AddTool` in `Run`.
9. Extend `scripts/mcp_smoke.sh` with a call and an assertion. A tool that lists
   but cannot be called is not a tool.

## Testing

`make test` covers pure logic. `make canary` covers the browser paths.

The canary is the project's regression test. It starts `fixtures/canary` with
Uvicorn on port 8099, runs a crawl with form credentials, and asserts:

- every planted defect in `GROUND_TRUTH.md` produced its code;
- the delete and side-effect classes are distinguished;
- the file input was exercised, proven by the handler error the fixture throws;
- a popup finding exists and its screenshot is on disk;
- a second run with `--allow-delete` clicks the deletion control and still
  quarantines the side effect.

It uses `--fail-on never` so the gate is about detection, not exit status.

`make mcp-smoke` starts the same fixture on port 8097 and speaks JSON-RPC over
stdio to the built binary. It asserts the tool list, a dead control, an uncaught
exception from a capture-only call, an off-origin refusal, and a two-page crawl
with a report on disk.

Visual comparison has no automated gate yet. Verify it with three runs:

```sh
afford run --visual --out /tmp/v/run1
afford run --visual --out /tmp/v/run2          # expect no E_VISUAL_DIFF
cp /tmp/v/run2/screenshots/<one>.png .afford/baselines/<one>.png
afford run --visual --out /tmp/v/run3          # expect E_VISUAL_DIFF with artifacts
```

Use a temporary `[visual] dir` if you do not want to disturb a real baseline store.

## Debugging

| tool | use |
|---|---|
| `AFFORD_DEBUG=1` | One line per probe on stderr: context, page, effect, action error, error and request counts, element. |
| `--headful` | Watch the browser. The only practical way to see an occlusion or a popup. |
| `run.jsonl` | Every page, finding, link and probe count, in order. Append-only; a crash keeps the prefix. |
| `screenshots/` | The page before interaction. Check what the capture pass actually saw. |
| `popups/` | Popup screenshots, referenced from finding detail. |
| `visual/` | Baseline, current and diff images for a comparison. |

Playwright tracing is not implemented. If you need it, add a `[run] trace`
option and start tracing on the context; it is the natural next debugging tool.

## Style

Go code is `gofmt` formatted, no exceptions. Tabs for indentation, matching the
repository conventions. Standard library first; the dependency list is
Playwright, the MCP SDK, a TOML parser and `golang.org/x/term`, and it should stay
short. No cgo.

Comments explain why, not what. Assertions at function boundaries are welcome
when they encode a real precondition, such as a URL scheme or a store path.

Prose in docs and commit messages follows the same rules as the code comments:
concrete, active voice, sentence case headings, no em dashes, no filler. Say what
the thing does and what it costs.

## Releasing

```sh
git tag -a v0.2.0 -m "afford 0.2.0" && git push origin v0.2.0
make release VERSION=v0.2.0            # or: scripts/release.sh v0.2.0 notes.md
```

`scripts/release.sh` builds the five platform archives from a clean tree, writes
`checksums.txt`, and creates the GitHub release on the pushed tag. The version
reaches the binary through `-ldflags`, so `afford version` cannot disagree with
the tag. `--dry-run` packages without uploading. There is no hosted build: the
artifacts come from the machine that made the tag.

## Commits

Commit a unit at a time. Run `gofmt`, `go test ./...` and `make canary` before
committing a behavior change; run `make mcp-smoke` when the MCP surface moved.
Stage explicit paths and check `git diff --cached --stat` before committing. Do
not commit `__pycache__`, `.afford/` or `bin/`.

Commit messages state the problem and the change, not the effort. Do not add AI
attribution lines.

## Review checklist

Before calling a change done:

- [ ] `gofmt -l ./cmd ./internal` prints nothing.
- [ ] `go vet ./...` is clean.
- [ ] `go test ./...` passes.
- [ ] `make canary` reports every planted defect plus the permission, upload and
      popup properties.
- [ ] `make mcp-smoke` passes if `internal/mcpserver`, `internal/check` or `probe`
      changed.
- [ ] Every new finding code is in `model.specs` and in the user guide.
- [ ] Every new finding has evidence and, where it applies, a payload.
- [ ] The crawl path and the single-page path both emit it.
- [ ] No config key was added without a reader, and none was removed without
      updating the docs.
- [ ] Reports remain deterministic.
- [ ] `bin/afford` was rebuilt before any manual verification.

