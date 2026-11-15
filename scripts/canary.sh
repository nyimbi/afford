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
		--no-baseline --no-prompt --quiet --fail-on never --out "$OUT"

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
    "E_NOT_DEEPLINKABLE", "E_NOT_READY", "E_RENDERED_GARBAGE", "E_BROKEN_IMAGE",
    "E_UNHANDLED_REJECTION", "E_A11Y_SERIOUS", "E_KEYBOARD_UNREACHABLE",
    "E_NO_FOCUS_INDICATOR", "E_KEYBOARD_TRAP",
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

late = [f for f in payload["findings"]
        if f["code"] == "E_DEAD_CONTROL" and "Late dead" in (f.get("element") or "")]
if not late:
    raise SystemExit("canary: the readiness selector did not hold the scan until /late rendered")

dead = {f["element"]: f for f in payload["findings"] if f["code"] == "E_DEAD_CONTROL"}
refresh = next((f for e, f in dead.items() if "Refresh" in e), None)
flaky_all = [f for e, f in dead.items() if "Flaky save" in e]
flaky = next((f for f in flaky_all if f.get("confidence") == "unconfirmed"), None)
if any(f.get("confidence") == "confirmed" for f in flaky_all):
    raise SystemExit("canary: a handler missing on alternate loads was reported as confirmed")
if not refresh or refresh.get("confidence") != "confirmed":
    raise SystemExit(f"canary: a deterministic dead control must be confirmed: {refresh and refresh.get('confidence')}")
if not flaky or flaky.get("confidence") != "unconfirmed":
    raise SystemExit(f"canary: a control dead on one load of two must be unconfirmed: {[f.get('confidence') for f in flaky_all]}")
counts = payload["summary"]["counts"]
totals = payload["summary"]["totals"]
if payload["summary"].get("unconfirmed", 0) < 1 or totals["major"] <= counts["major"]:
    raise SystemExit(f"canary: unconfirmed findings leaked into the gating counts: {counts} vs {totals}")

dead_names = " ".join(f.get("element") or "" for f in payload["findings"] if f["code"] == "E_DEAD_CONTROL")
for needle, why in [
    ("Menu dead item", "a control revealed by a click was not rescanned"),
    ("Shadow dead", "a control inside an open shadow root was not scanned"),
    ("Frame dead", "a control inside a same-origin iframe was not scanned"),
    ("Lazy dead", "scroll-to-load did not reach the lazy content"),
]:
    if needle not in dead_names:
        raise SystemExit(f"canary: {why}")
if "Foreign dead" in dead_names:
    raise SystemExit("canary: a cross-origin frame was driven")
revealed = [f for f in payload["findings"] if "Menu dead item" in (f.get("element") or "")]
if not revealed or "Open menu" not in str(revealed[0]["detail"].get("revealed_by")):
    raise SystemExit("canary: a revealed finding must name the control that revealed it")
if not any("cross-origin frame" in n for n in payload["summary"]["not_tested"]):
    raise SystemExit("canary: the skipped cross-origin frame was not listed")

leaks = [f["page_url"] for f in payload["findings"] if f["code"] == "E_RENDERED_GARBAGE"]
if any(u.endswith("/code-sample") for u in leaks):
    raise SystemExit("canary: undefined/NaN inside code and pre was reported as leaked text")
if not any(u.endswith("/garbage") for u in leaks):
    raise SystemExit("canary: leaked text on /garbage was not found")
traps = [f["page_url"] for f in payload["findings"] if f["code"] == "E_KEYBOARD_TRAP"]
if not traps or not all(u.endswith("/trap") for u in traps):
    raise SystemExit(f"canary: keyboard trap must be found on /trap only: {traps}")
clean = [f for f in payload["findings"] if f["code"].startswith(("E_KEYBOARD", "E_NO_FOCUS")) and f["page_url"].endswith("/dead")]
if clean:
    raise SystemExit(f"canary: keyboard findings on a page that tabs cleanly: {[(f['code'], f.get('element')) for f in clean]}")

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
		--allow-delete --max-pages 2 --no-baseline \
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

# A logout control clicked mid-run ends the shared session. The crawl must log
# in again and finish, not report the rest of the app as a login wall.
OUT3="$(mktemp -d)"
AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
	go run ./cmd/afford run \
		--config fixtures/canary/afford.toml \
		--base-url "http://127.0.0.1:$PORT/dashboard" \
		--allow-side-effects --max-pages 8 --no-baseline \
		--no-prompt --quiet --fail-on never --out "$OUT3"

python3 - "$OUT3" <<'PY'
import glob, json, sys

reports = sorted(glob.glob(sys.argv[1] + "/runs/*/report.json"))
if not reports:
    raise SystemExit("canary: logout run wrote no report")
payload = json.load(open(reports[-1]))
codes = {f["code"] for f in payload["findings"]}
if "E_AUTH_WALL" in codes or "E_AUTH_REDIRECT" in codes:
    raise SystemExit(f"canary: the session loss was not recovered: {sorted(codes)}")
if payload["summary"]["pages_visited"] < 4:
    raise SystemExit(f"canary: crawl stopped after the logout: {payload['summary']['pages_visited']} pages")
if not any("re-authenticated" in n for n in payload["summary"]["not_tested"]):
    raise SystemExit("canary: the logout control was never clicked, so the recovery path did not run")
print("canary ok: a mid-run logout was recovered by re-authenticating")
PY

# The findings baseline: accept, re-run unchanged, then change exactly one thing.
BASE="$(mktemp -d)/baseline.json"
run_gate() {
	AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
		go run ./cmd/afford run \
			--config fixtures/canary/afford.toml \
			--baseline "$BASE" "$@" \
			--no-prompt --quiet
}
OUT4="$(mktemp -d)"
run_gate --accept --fail-on minor --out "$OUT4"
[ -s "$BASE" ] || { echo "canary: --accept wrote no baseline" >&2; exit 1; }

OUT5="$(mktemp -d)"
set +e
run_gate --fail-on minor --out "$OUT5"
unchanged=$?
set -e
python3 - "$OUT5" "$unchanged" <<'PY'
import glob, json, sys
payload = json.load(open(sorted(glob.glob(sys.argv[1] + "/runs/*/report.json"))[-1]))
b = payload["summary"].get("baseline")
if sys.argv[2] != "0" or not b or b["new"] != 0:
    new = [(f["code"], f.get("element"), f["page_url"]) for f in payload["findings"] if f.get("status") == "new"]
    raise SystemExit(f"canary: an unchanged app must have no new findings (exit {sys.argv[2]}): {new}")
print(f"canary ok: unchanged app against its baseline is clean ({b['known']} known)")
PY

curl -sf "http://127.0.0.1:$PORT/_canary/mutate?plant=1" >/dev/null
OUT6="$(mktemp -d)"
set +e
run_gate --fail-on minor --out "$OUT6"
changed=$?
set -e
python3 - "$OUT6" "$changed" <<'PY'
import glob, json, sys
payload = json.load(open(sorted(glob.glob(sys.argv[1] + "/runs/*/report.json"))[-1]))
b = payload["summary"]["baseline"]
new = [f for f in payload["findings"] if f.get("status") == "new"]
if sys.argv[2] != "1":
    raise SystemExit(f"canary: a new finding must fail the gate, exit {sys.argv[2]}")
if b["new"] != 1 or len(new) != 1 or new[0]["code"] != "E_CONSOLE_ERROR":
    raise SystemExit(f"canary: expected exactly the one planted finding as new: {[(f['code'], f.get('element')) for f in new]}")
fixed = [e["code"] for e in b.get("fixed", [])]
if fixed != ["E_CONSOLE_ERROR"]:
    raise SystemExit(f"canary: expected the replaced console error as fixed, got {fixed}")
print("canary ok: baseline diff reports one new, one fixed, and fails only on the new one")
PY
curl -sf "http://127.0.0.1:$PORT/_canary/mutate?plant=0" >/dev/null

# Suppression: a valid rule hides the finding from the gate; the same rule past
# its expiry stops hiding it and raises its own finding.
SUP="$(mktemp -d)/suppress.toml"
cat > "$SUP" <<'TOML'
[[suppress]]
code = "E_CONSOLE_ERROR"
page = "/console"
reason = "vendor widget, tracked upstream"
owner = "web-team"
expires = "2099-01-01"
TOML
run_one() {
	AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
		go run ./cmd/afford run \
			--config fixtures/canary/afford.toml \
			--base-url "http://127.0.0.1:$PORT/console" --max-pages 1 \
			--baseline "$(mktemp -d)/none.json" "$@" \
			--no-prompt --quiet --fail-on never
}
OUT7="$(mktemp -d)"
run_one --suppress "$SUP" --out "$OUT7"
sed -i.bak 's/2099-01-01/2020-01-01/' "$SUP"
OUT8="$(mktemp -d)"
run_one --suppress "$SUP" --out "$OUT8"
python3 - "$OUT7" "$OUT8" <<'PY'
import glob, json, sys

def load(d):
    return json.load(open(sorted(glob.glob(d + "/runs/*/report.json"))[-1]))

live, expired = load(sys.argv[1]), load(sys.argv[2])
c = [f for f in live["findings"] if f["code"] == "E_CONSOLE_ERROR"]
if len(c) != 1 or c[0].get("status") != "suppressed" or "web-team" not in str(c[0].get("suppression")):
    raise SystemExit(f"canary: a live suppression did not mute the finding: {c}")
if live["summary"]["counts"].get("minor", 0) != 0:
    raise SystemExit(f"canary: a suppressed finding still gates: {live['summary']['counts']}")
c = [f for f in expired["findings"] if f["code"] == "E_CONSOLE_ERROR"]
if len(c) != 1 or c[0].get("status") == "suppressed":
    raise SystemExit(f"canary: an expired suppression still muted the finding: {c}")
if "E_SUPPRESSION_EXPIRED" not in {f["code"] for f in expired["findings"]}:
    raise SystemExit("canary: an expired suppression raised no finding")
print("canary ok: suppression mutes while live, stops and nags once expired")
PY

# The production guard: writing to a host outside [safety] mutation_hosts is a
# configuration error (exit 2) before any browser starts; a read-only run is not.
set +e
./bin/afford run --config fixtures/canary/afford.toml \
	--base-url "https://shop.example.com" --no-prompt --quiet --out "$(mktemp -d)" 2>/dev/null
guarded=$?
set -e
[ "$guarded" -eq 2 ] || { echo "canary: submitting forms on a production host must exit 2, got $guarded" >&2; exit 1; }
echo "canary ok: the production guard refuses a non-allowlisted host"
