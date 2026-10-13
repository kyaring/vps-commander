#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

SCRIPT = Path(__file__).with_name("emit-notification-event.py")
seen = []
response_mode = {"status": 202, "content_type": "application/json", "payload": b'{"ok":true,"event_id":"evt_test","replayed":false}'}


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        seen.append({
            "path": self.path,
            "auth": self.headers.get("Authorization"),
            "idempotency": self.headers.get("Idempotency-Key"),
            "user_agent": self.headers.get("User-Agent"),
            "body": json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0")))),
        })
        self.send_response(response_mode["status"])
        self.send_header("Content-Type", response_mode["content_type"])
        self.send_header("Content-Length", str(len(response_mode["payload"])))
        self.end_headers()
        self.wfile.write(response_mode["payload"])

    def log_message(self, *_args):
        pass


def invoke(env, template="review-completed"):
    return subprocess.run(
        [sys.executable, str(SCRIPT), template, "--variables",
         json.dumps({"result": "PASS", "commit": "abc123", "summary": "all tests passed"}),
         "--idempotency-key", "review:abc123"],
        env=env, capture_output=True, text=True, check=False,
    )


server = HTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
try:
    env = os.environ.copy()
    env["VPS_COMMANDER_HUB_URL"] = f"http://127.0.0.1:{server.server_port}"
    env["VPS_COMMANDER_EVENT_TOKEN"] = "do-not-print-this-token"
    env.pop("VPS_COMMANDER_USER_AGENT", None)

    result = invoke(env)
    assert result.returncode == 0, (result.stdout, result.stderr)
    assert seen[-1]["path"] == "/api/v1/events", seen[-1]
    assert seen[-1]["auth"] == "Bearer do-not-print-this-token", seen[-1]
    assert seen[-1]["idempotency"] == "review:abc123", seen[-1]
    assert seen[-1]["user_agent"] == "vps-commander-notify/1.0", seen[-1]
    assert seen[-1]["body"]["template_id"] == "review-completed" and seen[-1]["body"]["variables"]["result"] == "PASS", seen[-1]
    assert "do-not-print-this-token" not in result.stdout and "do-not-print-this-token" not in result.stderr

    env["VPS_COMMANDER_USER_AGENT"] = "custom-notify-agent/9"
    result_override = invoke(env)
    assert result_override.returncode == 0, (result_override.stdout, result_override.stderr)
    assert seen[-1]["user_agent"] == "custom-notify-agent/9", seen[-1]

    response_mode.update(status=403, content_type="text/plain", payload=b"error code: 1010")
    env.pop("VPS_COMMANDER_USER_AGENT", None)
    failed = invoke(env)
    assert failed.returncode != 0, (failed.stdout, failed.stderr)
    diagnostic = json.loads(failed.stderr.strip().splitlines()[-1])
    assert diagnostic["ok"] is False and diagnostic["status"] == 403, diagnostic
    assert "error code: 1010" in diagnostic["body"], diagnostic
    assert "server" in diagnostic, diagnostic
    assert "do-not-print-this-token" not in failed.stdout and "do-not-print-this-token" not in failed.stderr
    print("PASS notification event CLI: User-Agent/default override, payload, idempotency, HTTP diagnostics, and secret redaction")
finally:
    server.shutdown()
    thread.join(timeout=2)
    server.server_close()
