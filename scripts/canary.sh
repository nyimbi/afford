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
    "E_500_ON_VALID", "E_VALIDATION_NOT_ENFORCED", "E_DEAD_LINK",
    "E_UUID_EXPOSED", "E_UUID_IN_OPTION_VALUE", "E_EMPTY_SELECT",
    "E_DESTRUCTIVE_SKIPPED", "E_NEW_TAB", "E_CLIENT_ERROR_ON_ACTION",
    "E_NOT_DEEPLINKABLE",
}
missing = expected - found
if missing:
    raise SystemExit(f"canary: missing {sorted(missing)}\nfound {sorted(found)}")

uploaded = any(
    f["code"] == "E_CLIENT_ERROR_ON_ACTION" and "Attachment" in (f.get("element") or "")
    for f in payload["findings"]
)
if not uploaded:
    raise SystemExit("canary: the file input was not exercised (no upload handler error)")

popups = [f for f in payload["findings"] if f["detail"].get("popup_url")]
if not popups:
    raise SystemExit("canary: no finding was attributed to a popup")
shot = popups[0]["detail"].get("screenshot", "")
if shot:
    import os
    run_dir = os.path.dirname(reports[-1])
    path = shot if os.path.isabs(shot) else os.path.join(run_dir, shot)
    if not os.path.exists(path):
        raise SystemExit(f"canary: popup screenshot missing: {path}")

kinds = {f["detail"].get("kind") for f in payload["findings"]
         if f["code"] == "E_DESTRUCTIVE_SKIPPED"}
if not {"delete", "side_effect"} <= kinds:
    raise SystemExit(f"canary: destructive classes not distinguished: {kinds}")
print(f"canary ok: {len(expected)}/{len(expected)} planted defects found, "
      f"permission classes {sorted(kinds)}, popup findings {len(popups)}")
PY

# The deletion grant must actually be wired, and must not leak into the
# side-effect class. Seed at /dashboard so the run is short.
OUT2="$(mktemp -d)"
AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
	go run ./cmd/afford run \
		--config fixtures/canary/afford.toml \
		--base-url "http://127.0.0.1:$PORT/dashboard" \
		--allow-delete --max-pages 2 \
		--no-prompt --quiet --fail-on never --out "$OUT2"

python3 - "$OUT2" <<'PY'
import glob
import json
import sys

reports = sorted(glob.glob(sys.argv[1] + "/runs/*/report.json"))
if not reports:
    raise SystemExit("canary: --allow-delete run wrote no report")
payload = json.load(open(reports[-1]))
clicked = any(
    f["code"] == "E_DEAD_CONTROL" and "Delete order" in (f.get("element") or "")
    for f in payload["findings"]
)
still_skipped = any(
    f["code"] == "E_DESTRUCTIVE_SKIPPED" and "Publish" in (f.get("element") or "")
    for f in payload["findings"]
)
if not clicked:
    raise SystemExit("canary: --allow-delete did not click the deletion control")
if not still_skipped:
    raise SystemExit("canary: --allow-delete leaked into the side-effect class")
print("canary ok: --allow-delete clicks deletion, side effects stay quarantined")
PY
