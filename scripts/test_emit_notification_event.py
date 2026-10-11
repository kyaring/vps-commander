#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

SCRIPT = Path(__file__).with_name("emit-notification-event.py")
seen = {}

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        seen["path"] = self.path
        seen["auth"] = self.headers.get("Authorization")
        seen["idempotency"] = self.headers.get("Idempotency-Key")
        seen["body"] = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        payload = json.dumps({"ok": True, "event_id": "evt_test", "replayed": False}).encode()
        self.send_response(202)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
    def log_message(self, *_args):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
try:
    env = os.environ.copy()
    env["VPS_COMMANDER_HUB_URL"] = f"http://127.0.0.1:{server.server_port}"
    env["VPS_COMMANDER_EVENT_TOKEN"] = "do-not-print-this-token"
    result = subprocess.run([sys.executable, str(SCRIPT), "review-completed", "--variables", json.dumps({"result":"PASS","commit":"abc123","summary":"all tests passed"}), "--idempotency-key", "review:abc123"], env=env, capture_output=True, text=True, check=True)
    assert seen["path"] == "/api/v1/events", seen
    assert seen["auth"] == "Bearer do-not-print-this-token", seen
    assert seen["idempotency"] == "review:abc123", seen
    assert seen["body"]["template_id"] == "review-completed" and seen["body"]["variables"]["result"] == "PASS", seen
    assert "do-not-print-this-token" not in result.stdout and "do-not-print-this-token" not in result.stderr
    print("PASS notification event CLI: auth header, payload, idempotency key, and secret redaction")
finally:
    server.shutdown()
    thread.join(timeout=2)
    server.server_close()
