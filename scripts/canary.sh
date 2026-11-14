#!/usr/bin/env bash
# Canary gate: the tool's own regression test.
#
# Starts the fixture app, runs afford against it, and asserts that every planted
# defect was found. This gate failing means afford broke, not the fixture.
set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${AFFORD_CANARY_PORT:-8099}"
OUT="$(mktemp -d)"
SERVER_PID=""

cleanup() {
	if [ -n "$SERVER_PID" ]; then
		kill "$SERVER_PID" 2>/dev/null || true
		wait "$SERVER_PID" 2>/dev/null || true
	fi
}
trap cleanup EXIT

python3 -m uvicorn app:app --app-dir fixtures/canary --port "$PORT" --log-level warning &
SERVER_PID=$!

ready=0
for _ in $(seq 1 100); do
	if curl -sf "http://127.0.0.1:$PORT/" >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 0.2
done
if [ "$ready" -ne 1 ]; then
	echo "canary: fixture app did not start on port $PORT" >&2
	exit 3
fi

AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
	go run ./cmd/afford run \
		--config fixtures/canary/afford.toml \
		--no-prompt --quiet --fail-on never --out "$OUT"

python3 - "$OUT" <<'PY'
import glob
import json
import sys

reports = sorted(glob.glob(sys.argv[1] + "/runs/*/report.json"))
if not reports:
    raise SystemExit("canary: no report was written")
payload = json.load(open(reports[-1]))
found = {f["code"] for f in payload["findings"]}
expected = {
    "E_DEAD_CONTROL", "E_EMPTY_HREF", "E_UNCAUGHT", "E_CONSOLE_ERROR",
    "E_OVERFLOW_X", "E_UNLABELED_CONTROL", "E_HTTP_4XX", "E_500_ON_ACTION",
    "E_500_ON_VALID", "E_VALIDATION_NOT_ENFORCED",
}
missing = expected - found
if missing:
    raise SystemExit(f"canary: missing {sorted(missing)}\nfound {sorted(found)}")
print(f"canary ok: {len(expected)}/{len(expected)} planted defects found")
PY
