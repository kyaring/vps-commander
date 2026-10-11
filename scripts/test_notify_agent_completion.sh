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
case "$out" in *"task completion is not blocked"*) ;; *) echo "network failure blocked completion: $out" >&2; exit 1 ;; esac
echo "PASS agent completion hook: missing credentials/network failures skip non-blockingly; invalid result rejected"
