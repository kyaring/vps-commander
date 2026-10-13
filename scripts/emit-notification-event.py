#!/usr/bin/env python3
"""Emit a templated VPS-Commander notification event using an events:emit token."""
import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

DEFAULT_USER_AGENT = "vps-commander-notify/1.0"


def redact(value, token):
    """Keep response diagnostics useful without echoing credentials or auth headers."""
    if isinstance(value, dict):
        return {
            key: ("[REDACTED]" if str(key).lower() == "authorization" else redact(item, token))
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [redact(item, token) for item in value]
    if isinstance(value, str):
        if token:
            value = value.replace(token, "[REDACTED]")
        return re.sub(r"(?im)(authorization\s*:\s*)(?:bearer\s+)?[^\r\n]+", r"\1[REDACTED]", value)
    return value


def main() -> int:
    parser = argparse.ArgumentParser(description="Emit a custom notification event.")
    parser.add_argument("template_id")
    parser.add_argument("--variables", required=True, help="JSON object containing string variables")
    parser.add_argument("--idempotency-key", required=True, help="Stable key for retries of the same job")
    parser.add_argument("--severity", choices=["P1", "P2", "P3", "P4"])
    parser.add_argument("--source", default="agent")
    parser.add_argument("--device", default=os.getenv("VPS_COMMANDER_EVENT_DEVICE", ""))
    args = parser.parse_args()

    base = os.getenv("VPS_COMMANDER_HUB_URL", "").rstrip("/")
    token = os.getenv("VPS_COMMANDER_EVENT_TOKEN", "")
    if not base or not token:
        parser.error("VPS_COMMANDER_HUB_URL and VPS_COMMANDER_EVENT_TOKEN are required environment variables")
    try:
        variables = json.loads(args.variables)
    except json.JSONDecodeError as exc:
        parser.error(f"--variables is not valid JSON: {exc}")
    if not isinstance(variables, dict) or any(not isinstance(k, str) or not isinstance(v, str) for k, v in variables.items()):
        parser.error("--variables must be a JSON object with string keys and values")

    body = {"template_id": args.template_id, "variables": variables, "source": args.source, "device": args.device}
    if args.severity:
        body["severity"] = args.severity
    request = urllib.request.Request(
        base + "/api/v1/events",
        data=json.dumps(body, separators=(",", ":"), ensure_ascii=False).encode("utf-8"),
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
            "Idempotency-Key": args.idempotency_key,
            "User-Agent": os.getenv("VPS_COMMANDER_USER_AGENT", DEFAULT_USER_AGENT),
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            result = json.loads(response.read())
            status = response.status
    except urllib.error.HTTPError as exc:
        status = exc.code
        server = exc.headers.get("Server", "") if exc.headers else ""
        raw_body = exc.read().decode("utf-8", errors="replace")
        try:
            result = json.loads(raw_body)
            diagnostic = {"ok": False, "status": status, "server": server, "result": result}
        except (json.JSONDecodeError, TypeError):
            diagnostic = {"ok": False, "status": status, "server": server, "body": raw_body[:200]}
        print(json.dumps(redact(diagnostic, token), ensure_ascii=False), file=sys.stderr)
        return 1
    except (OSError, urllib.error.URLError) as exc:
        print(f"Notification request failed: {type(exc).__name__}", file=sys.stderr)
        return 2
    if status not in (200, 202):
        print(json.dumps(redact({"ok": False, "status": status, "server": "", "result": result}, token), ensure_ascii=False), file=sys.stderr)
        return 1
    print(json.dumps(redact(result, token), ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
