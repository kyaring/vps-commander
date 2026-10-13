#!/bin/sh
set -eu
script="$(dirname "$0")/notify-agent-completion.sh"
out=$(env -u VPS_COMMANDER_HUB_URL -u VPS_COMMANDER_EVENT_TOKEN "$script" development-completed --task-id unit-test --commit abc123 --summary 'tests passed' 2>&1)
case "$out" in SKIP:*) ;; *) echo "expected non-blocking SKIP, got: $out" >&2; exit 1 ;; esac
out=$(env -u VPS_COMMANDER_HUB_URL -u VPS_COMMANDER_EVENT_TOKEN "$script" review-completed --task-id unit-test --commit abc123 --summary 'review failed' --result FAIL 2>&1)
case "$out" in SKIP:*) ;; *) echo "expected non-blocking review SKIP, got: $out" >&2; exit 1 ;; esac
if env -u VPS_COMMANDER_HUB_URL -u VPS_COMMANDER_EVENT_TOKEN "$script" review-completed --task-id unit-test --commit abc123 --summary 'bad result' --result UNKNOWN >/dev/null 2>&1; then
  echo "invalid review result was accepted" >&2; exit 1
fi
out=$(VPS_COMMANDER_HUB_URL=http://127.0.0.1:1 VPS_COMMANDER_EVENT_TOKEN=test-token "$script" development-completed --task-id unit-test --commit abc123 --summary 'tests passed' 2>&1)
case "$out" in *"WARN: completion notification not delivered (status=network); task completion is not blocked."*) ;; *) echo "network failure warning was wrong: $out" >&2; exit 1 ;; esac
python3 - "$script" <<'PY'
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

script = sys.argv[1]
class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        body = b"error code: 1010"
        self.send_response(403)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *_args):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
try:
    env = os.environ.copy()
    env["VPS_COMMANDER_HUB_URL"] = f"http://127.0.0.1:{server.server_port}"
    env["VPS_COMMANDER_EVENT_TOKEN"] = "test-token"
    result = subprocess.run([script, "development-completed", "--task-id", "unit-test", "--commit", "abc123", "--summary", "tests passed"], env=env, capture_output=True, text=True)
    assert result.returncode == 0, (result.returncode, result.stdout, result.stderr)
    assert "WARN: completion notification not delivered (status=403); task completion is not blocked." in result.stderr, result.stderr
    assert "test-token" not in result.stdout + result.stderr, (result.stdout, result.stderr)
finally:
    server.shutdown()
    thread.join(timeout=2)
    server.server_close()
PY
echo "PASS agent completion hook: missing credentials skip, invalid result rejected, and network/403 failures are non-blocking with diagnostic status"
