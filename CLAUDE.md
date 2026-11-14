# afford

Go 1.22+, stdlib plus Playwright-Go. No cgo.

- Layout: `cmd/afford` (CLI, exit codes), `internal/{config,model,store,discover,payloads,browser,auth,probe,oracles,run,report}`.
- Every finding code is declared once in `internal/model`. Oracles return codes; nothing else invents one. `NewFinding` panics on an unregistered code.
- **Oracles** (`internal/oracles`) take an observation and return findings. No browser access. **Probes** (`internal/probe`) drive the browser and never decide severity.
- Never assert semantics the app did not declare. Record coverage instead: skipped, disabled, out of scope, over budget.
- `Collector.Reset()` at the top of every probe. An observation that inherits the previous probe's events is the bug the canary caught once already.
- A page's load-time errors are the capture pass's; action observations start from the post-load baseline.
- Findings merge across pages by `(code, element, detail signature)` so shared chrome is one finding with N locations, not N findings.
- Commands: `make build`, `make test`, `make vet`, `make canary`, `make mcp-smoke`, `make install`.
- The MCP server (`internal/mcpserver`) is a second entry point over the same `check`/`run` code. It must never prompt, never widen the safety posture beyond the CLI's, and must keep `internal/check`'s URL allowlist intact.
- MCP tool failures return a Go error, not a zero output struct: the SDK validates structured output against the inferred schema even on error results, so a zero struct with nil maps fails the call.
- `make canary` is the gate. It starts `fixtures/canary`, runs the tool, and asserts every planted defect in `fixtures/canary/GROUND_TRUTH.md` was found. If it fails, the tool broke.
- `make mcp-smoke` speaks JSON-RPC over stdio to the shipped binary: tools/list, check_page, inspect_page, off-origin refusal, crawl_site. A tool that lists but cannot be called is not a tool.
- gofmt before committing.
