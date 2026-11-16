# Running afford in CI

afford is designed to sit after your unit tests and before you call a deploy good.
The contract is the exit code plus the artifacts, never log scraping.

## GitHub Actions

```yaml
name: ui-smoke

on:
  pull_request:
  deployment_status:

jobs:
  afford:
    if: github.event_name == 'pull_request' || github.event.deployment_status.state == 'success'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.23"
      - name: Build
        run: go build -o bin/afford ./cmd/afford
      - name: Install browser
        run: ./bin/afford install
      - name: Smoke the preview deployment
        env:
          AFFORD_USERNAME: ${{ secrets.AFFORD_USERNAME }}
          AFFORD_PASSWORD: ${{ secrets.AFFORD_PASSWORD }}
        run: |
          ./bin/afford run \
            --base-url "${{ steps.deploy.outputs.url }}" \
            --config afford.toml \
            --no-prompt \
            --out artifacts/afford \
            --fail-on critical \
            --timeout 900
      - name: Upload report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: afford-report
          path: artifacts/afford
      - name: Publish JUnit
        if: always()
        uses: mikepenz/action-junit-report@v4
        with:
          report_paths: "artifacts/afford/**/junit.xml"
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

Each run writes `results.sarif` next to `report.json`. Upload it with `github/codeql-action/upload-sarif` to show findings in code scanning. The Dockerfile and `.github/actions/afford` are untested.
