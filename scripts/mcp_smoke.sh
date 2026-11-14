#!/usr/bin/env bash
# MCP smoke gate: speak the protocol to the shipped binary and assert behaviour.
set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${AFFORD_MCP_PORT:-8097}"
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
	echo "mcp smoke: fixture app did not start on port $PORT" >&2
	exit 3
fi

AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
AFFORD_MCP_PORT="$PORT" AFFORD_MCP_OUT="$OUT" \
	python3 - <<'PY'
import json
import os
import queue
import subprocess
import sys
import threading
import time

port = os.environ["AFFORD_MCP_PORT"]
out = os.environ["AFFORD_MCP_OUT"]
base = f"http://127.0.0.1:{port}"

proc = subprocess.Popen(
    ["./bin/afford", "mcp", "--config", "fixtures/canary/afford.toml",
     "--base-url", base, "--out", out],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    text=True, bufsize=1,
)
lines: "queue.Queue[str]" = queue.Queue()


def pump(stream, sink):
    for line in stream:
        sink.put(line)


threading.Thread(target=pump, args=(proc.stdout, lines), daemon=True).start()
stderr_tail: "queue.Queue[str]" = queue.Queue()
threading.Thread(target=pump, args=(proc.stderr, stderr_tail), daemon=True).start()

next_id = 0


def send(method, params=None, notify=False):
    global next_id
    message = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        message["params"] = params
    if not notify:
        next_id += 1
        message["id"] = next_id
    proc.stdin.write(json.dumps(message) + "\n")
    proc.stdin.flush()
    return message.get("id")


def recv(want_id, timeout=240):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            line = lines.get(timeout=max(0.1, deadline - time.time()))
        except queue.Empty:
            break
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except json.JSONDecodeError:
            continue
        if message.get("id") == want_id:
            return message
    raise SystemExit(f"mcp smoke: timed out waiting for id={want_id}")


def call(tool, arguments, timeout=240):
    response = recv(send("tools/call", {"name": tool, "arguments": arguments}), timeout)
    if "error" in response:
        raise SystemExit(f"mcp smoke: {tool} protocol error: {response['error']}")
    return response["result"]


def codes(result):
    return {f["code"] for f in result.get("structuredContent", {}).get("findings", [])}


try:
    send("initialize", {
        "protocolVersion": "2025-06-18",
        "capabilities": {},
        "clientInfo": {"name": "afford-smoke", "version": "1"},
    })
    recv(1)
    send("notifications/initialized", {}, notify=True)

    listed = recv(send("tools/list", {}))
    names = {t["name"] for t in listed["result"]["tools"]}
    missing_tools = {"check_page", "inspect_page", "crawl_site"} - names
    if missing_tools:
        raise SystemExit(f"mcp smoke: missing tools {sorted(missing_tools)}")

    dead = call("check_page", {"url": f"{base}/dead"})
    if dead.get("isError"):
        raise SystemExit(f"mcp smoke: check_page failed: {dead}")
    if "E_DEAD_CONTROL" not in codes(dead):
        raise SystemExit(f"mcp smoke: check_page missed E_DEAD_CONTROL: {codes(dead)}")
    shot = dead["structuredContent"].get("screenshot", "")
    if not shot or not os.path.exists(shot):
        raise SystemExit(f"mcp smoke: screenshot not written: {shot!r}")
    if "E_DEAD_CONTROL" not in dead["content"][0]["text"]:
        raise SystemExit("mcp smoke: text digest omits the finding")
    if dead["structuredContent"]["controls_probed"] < 1:
        raise SystemExit("mcp smoke: check_page probed no controls")

    crash = call("inspect_page", {"url": f"{base}/crash"})
    if "E_UNCAUGHT" not in codes(crash):
        raise SystemExit(f"mcp smoke: inspect_page missed E_UNCAUGHT: {codes(crash)}")
    if crash["structuredContent"]["controls_probed"] != 0:
        raise SystemExit("mcp smoke: inspect_page interacted with the page")

    refused = call("check_page", {"url": "http://example.com/"})
    if not refused.get("isError") or "refusing to open" not in refused["content"][0]["text"]:
        raise SystemExit(f"mcp smoke: off-origin URL was not refused: {refused}")

    crawl = call("crawl_site", {"max_pages": 2, "max_depth": 1, "depth": "smoke"}, timeout=300)
    if crawl.get("isError"):
        raise SystemExit(f"mcp smoke: crawl_site failed: {crawl}")
    cr = crawl["structuredContent"]
    if not cr.get("pages"):
        raise SystemExit("mcp smoke: crawl_site captured no pages")
    if not os.path.exists(cr.get("report", "")):
        raise SystemExit(f"mcp smoke: crawl report missing: {cr.get('report')!r}")

    print(f"mcp smoke ok: tools={sorted(names)} screenshot={os.path.basename(shot)} "
          f"crawl_pages={len(cr['pages'])}")
finally:
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
    if proc.returncode not in (0, None):
        tail = "".join(list(stderr_tail.queue)[-5:])
        if tail:
            print(tail, file=sys.stderr)
PY
