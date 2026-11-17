# Running afford in CI

afford is designed to sit after your unit tests and before you call a deploy good.
The contract is the exit code plus the artifacts, never log scraping.

## Local CI (`llocal-ci`)

This repository has no hosted CI. Gates live in `ci.toml` and run on the machine the code is written on:

```sh
llocal-ci --tier fast      # gofmt, vet, build, tests
llocal-ci                  # the above plus canary and mcp-smoke
llocal-ci --only canary    # one gate
make ci                    # the same, so the project has one entry point
```

Every gate declares a `falsify` mutation: a small edit that must make the gate fail. `llocal-ci --falsify` runs the gate on a clean copy first, applies the mutation, and requires a non-zero exit. A gate with no current proof is reported as UNPROVEN, and this repo sets `require_falsification = true` so an unproven gate exits 2 instead of shipping.

`canary` is the slow gate (about ten minutes): it starts `fixtures/canary`, runs afford against it, and asserts every planted defect is found. Run it with `--only canary` while iterating.

### Gating a deployed environment

`afford run` is the CI step. Point it at a preview URL and gate on the exit code:

```sh
./bin/afford run --base-url "$PREVIEW_URL" --config afford.toml \
  --no-prompt --out artifacts/afford --fail-on critical --timeout 900
```

## Exit codes

| code | meaning | what CI should do |
|---|---|---|
| 0 | clean | proceed |
| 1 | findings at or above `--fail-on` | fail the job, publish the report |
| 2 | bad flags or config | fail fast; the tool did not run |
| 3 | run could not complete | fail; check browser install and credentials |

## Things that make CI runs trustworthy

- **`--no-prompt`** makes a missing credential a hard failure instead of a hang.
- **`--timeout`** is a wall clock, not a per-page budget: the crawl stops, the
  report is written from what was captured, and the exit code reflects the
  findings so far.
- **A zero-page run exits 3.** Nothing captured is not a pass.
- **The JUnit file marks blocker/critical/major findings as failures** and the
  rest as system output, so a strict reporter does not fail on `E_NO_TITLE`.
- **Artifacts are written before the exit code is decided**, including on partial
  runs.
- **`--fail-on never`** is for the tool's own canary, never for an application.

## Recommended placement

Run against a preview deployment with disposable data. Against production, use
`--no-submit` and `--max-pages` low; the tool will still catch broken routes,
dead controls, and console errors without writing anything.

Deletion and other irreversible actions stay off unless granted: `--allow-delete` for
delete/erase controls, `--allow-side-effects` for pay/send/publish/logout/approve, or
`--allow-destructive` for both. Do not grant them unless the database can be thrown away.

## SARIF

Each run writes `results.sarif` next to `report.json`, for any consumer that reads SARIF 2.1.0.
