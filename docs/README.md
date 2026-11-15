# afford documentation

| document | what it covers |
|---|---|
| [User guide](user-guide.md) | Install, configure, run, read the report, use MCP, and put afford in CI. |
| [Contributor guide](contributing.md) | Build, test, the invariants the code depends on, and how to add a check. |
| [Architecture](architecture.md) | Components, data flow, probing, oracles, persistence, reporting, security and trade-offs. |
| [CI](ci.md) | A complete GitHub Actions job and the exit-code contract. |
| [MCP](mcp.md) | Tool reference, host wiring and the agent workflow. |
| [Design notes](../DESIGN.md) | The original problem statement and the reasoning behind the shape of the tool. |

`afford checks` prints the current catalogue of checks. The catalogue in
`internal/model` is authoritative; the user guide's tables mirror it.
