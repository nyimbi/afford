# afford over MCP

`afford mcp` speaks MCP on stdio. It exists so an agent can ask "does this page work?"
and get back evidence instead of a guess, without shelling out and parsing logs.

```sh
afford mcp --config afford.toml
```

The server starts no browser until the first tool call. That call also establishes the
session; credentials come from the environment of the process that launches the server,
never from tool arguments (those land in the host's transcript).

## Tools

| tool | mutates? | use it for |
|---|---|---|
| `inspect_page` | no | status, title, console errors, uncaught exceptions, failed requests, blank/error render, overflow, control inventory, screenshot |
| `check_page` | clicks and fills; submits only with `submit=true` | the full affordance check on one page |
| `crawl_site` | same, bounded by `max_pages` | a whole-site sweep with an HTML/JSON/JUnit report |

`check_page` arguments: `url`, `read_only`, `submit`, `depth` (`smoke`/`standard`/`deep`),
`no_screenshot`, `max_controls`. `crawl_site`: `max_pages` (default 25), `max_depth`
(default 2), `depth`, `submit`.

Every result carries `structuredContent` (findings, counts, probe counts, `load_ms`,
`settled_ms`, screenshot path) and a short text digest an agent can read directly.
`load_ms` is the browser's load event; `settled_ms` is when the page stopped changing
under afford's quiet-network and quiet-DOM criteria.

## Wiring it into Claude Code

```sh
claude mcp add afford --scope user -- \
  /absolute/path/to/bin/afford mcp --config /absolute/path/to/afford.toml
```

Export the credentials in the environment that launches the host, or the server will
report a tool error on first use:

```sh
export AFFORD_USERNAME=qa@example.com
export AFFORD_PASSWORD='…'
```

A `.mcp.json` works too, but anything you put in `env` is a secret in a file that gets
committed. Prefer the shell environment or a launcher script.

```json
{
  "mcpServers": {
    "afford": {
      "command": "/absolute/path/to/bin/afford",
      "args": ["mcp", "--config", "/absolute/path/to/afford.toml"]
    }
  }
}
```

## The safety posture is the server's

An agent does not get a wider blast radius than a human running the CLI:

- **Deletion is a separate, explicit grant**: `--allow-delete`. Paying, sending,
  publishing, approving and logging out need `--allow-side-effects`.
  `--allow-destructive` is the umbrella for both. A skipped control still appears in
  the result, with a note naming the flag that would have allowed it.
- **Creation is governed by form submission**: forms are not submitted unless the call
  passes `submit: true`; inputs are still filled and validated, which is where most
  client-side defects surface.
- **The URL allowlist is enforced.** `check_page` refuses hosts other than the configured
  origin and `[scope] allow_hosts`, so a prompt-injected agent cannot point a real browser
  at `http://169.254.169.254/` or an internal admin panel. `--allow-any-host` drops that
  gate; do not use it on a server an LLM can drive.
- **No prompts.** A missing credential is an error the agent can report, not a hang.
- **One browser, serialized probes.** Concurrent probes against one app produce
  unattributable evidence; the checker takes a lock.
- **Call timeout.** `[mcp] call_timeout_secs` (default 120) bounds each call, and
  `crawl_site` is additionally bounded by page and depth budgets.

## Recommended agent flow

1. `inspect_page` — cheap, no mutation. If the page does not render, stop; the rest is noise.
2. `check_page` with defaults — clicks controls, fills inputs, does not submit.
3. `check_page` with `submit: true` only on an environment whose data can be thrown away.
4. `crawl_site` when the question is about the app rather than one page.

Read `structuredContent.findings[].detail` and `.evidence` before summarising: the
evidence is what makes a finding checkable, and a finding without evidence is a rumour.

## Failure modes you will see as tool errors

| message | cause |
|---|---|
| `afford is not authenticated: credentials not found: set …` | server launched without credentials in its environment |
| `refusing to open http://…: it is neither the configured origin …` | URL outside the allowlist |
| `could not open http://…: …` | navigation failed — DNS, TLS, or the app is down |
| `depth must be smoke, standard or deep` | bad argument |
