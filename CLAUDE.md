# afford

Go 1.22+, stdlib plus Playwright-Go. No cgo.

- Layout: `cmd/afford` (CLI, exit codes), `internal/{config,model,store,discover,payloads,browser,auth,probe,oracles,run,report}`.
- Every finding code is declared once in `internal/model`. Oracles return codes; nothing else invents one. `NewFinding` panics on an unregistered code.
- **Oracles** (`internal/oracles`) take an observation and return findings. No browser access. **Probes** (`internal/probe`) drive the browser and never decide severity.
- Never assert semantics the app did not declare. Record coverage instead: skipped, disabled, out of scope, over budget.
- `Collector.Reset()` at the top of every probe. An observation that inherits the previous probe's events is the bug the canary caught once already.
- A page's load-time errors are the capture pass's; action observations start from the post-load baseline.
- Findings merge across pages by `(code, element, detail signature)` so shared chrome is one finding with N locations, not N findings.
- Docs: `docs/user-guide.md`, `docs/contributing.md`, `docs/architecture.md`. The catalogue in `internal/model` is authoritative; the guide mirrors it.
- Commands: `make build`, `make test`, `make vet`, `make canary`, `make mcp-smoke`, `make install`.
- The MCP server (`internal/mcpserver`) is a second entry point over the same `check`/`run` code. It must never prompt, never widen the safety posture beyond the CLI's, and must keep `internal/check`'s URL allowlist intact.
- MCP tool failures return a Go error, not a zero output struct: the SDK validates structured output against the inferred schema even on error results, so a zero struct with nil maps fails the call.
- Mutation permissions are classed: creation (`submit_forms`), deletion (`allow_delete`), other irreversible effects (`allow_side_effects`), with `allow_destructive` as the umbrella. A skipped control reports the class and the flag that would allow it.
- The destructive vocabulary matches on word boundaries. Substring matching quarantined a nav link called "dropdown" because it contains "drop".
- New subsystems: `internal/uploads` (fixture resolution, never a host path), `internal/visual` (baseline store + pixel diff). Popup capture lives in `probe` and is attributed in `run`/`check`.
- Popup capture polls `context.Pages()` after the action; do not close a popup before it has been settled and scanned.
- `Finding.Key()` includes `popup_url` and the visual baseline key so popup and visual findings do not merge into the page finding.
- `make canary` is the gate. It starts `fixtures/canary`, runs the tool, and asserts every planted defect in `fixtures/canary/GROUND_TRUTH.md` was found. If it fails, the tool broke.
- `make mcp-smoke` speaks JSON-RPC over stdio to the shipped binary: tools/list, check_page, inspect_page, off-origin refusal, crawl_site. A tool that lists but cannot be called is not a tool.
- Gate layers, in order: confirm-on-reprobe, `[policy]` severity, suppression, baseline. Only `Finding.Gates()` findings count toward `Summary.Counts` and the exit code; `Summary.Totals` holds all. Fingerprints exclude the page; baseline entries own the union of every page sharing a fingerprint.
- Shared page-level oracles live in `internal/pass` so `run` and `check` cannot diverge. Add new page checks there.
- Each page visit uses a fresh context from the storage state; `visitSafely(nil, item)` is the unit-test seam, keep its signature.
- Page audits (`internal/audit`: text, images, rejections, axe, keyboard) run from `internal/pass`. `audit.InitScript` is installed in `browser.NewProbePage`. Extra passes (viewports, roles, orphan routes) live in `internal/run/extras.go`; they only add findings, never probe controls.
- `Finding.Key()` includes `viewport` and `role`; add any detail that distinguishes two findings there, or they merge into one with `AlsoOn`.
- Canary assertions on merged findings must read `also_on`, not just `page_url`.
- Every screenshot goes through `Config.ScreenshotMasks()` (password inputs always masked).
- Dockerfile and `.github/actions/afford` are untested; podman only here.
- gofmt before committing.
