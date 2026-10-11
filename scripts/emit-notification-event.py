#!/usr/bin/env python3
"""Emit a templated VPS-Commander notification event using an events:emit token."""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request


def main() -> int:
    parser = argparse.ArgumentParser(description="Emit a custom notification event.")
    parser.add_argument("template_id")
    parser.add_argument("--variables", required=True, help='JSON object, e.g. \'{"task":"build","commit":"abc","summary":"tests passed"}\'')
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
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json", "Idempotency-Key": args.idempotency_key},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            status = response.status
            result = json.loads(response.read())
    except urllib.error.HTTPError as exc:
        status = exc.code
        try:
            result = json.loads(exc.read())
        except Exception:
            result = {"error": "event request rejected"}
    except (OSError, urllib.error.URLError) as exc:
        print(f"Notification request failed: {type(exc).__name__}", file=sys.stderr)
        return 2
    if status not in (200, 202):
        print(json.dumps({"ok": False, "status": status, "result": result}, ensure_ascii=False), file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
