#!/bin/sh
# Optional final-boundary hook for repository agents. Missing credentials are a
# non-blocking skip; this script must not be run for intermediate commits.
set -eu

usage() {
  echo "Usage: $0 development-completed|review-completed --task-id ID --commit SHA --summary TEXT [--result PASS|FAIL]" >&2
  exit 2
}
[ "$#" -ge 1 ] || usage
event="$1"; shift
case "$event" in development-completed|review-completed) ;; *) usage ;; esac
task_id=""; commit=""; summary=""; result=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --task-id) [ "$#" -ge 2 ] || usage; task_id="$2"; shift 2 ;;
    --commit) [ "$#" -ge 2 ] || usage; commit="$2"; shift 2 ;;
    --summary) [ "$#" -ge 2 ] || usage; summary="$2"; shift 2 ;;
    --result) [ "$#" -ge 2 ] || usage; result="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$task_id" ] && [ -n "$commit" ] && [ -n "$summary" ] || usage
case "$task_id$commit" in *[!A-Za-z0-9._:-]*) echo "task-id and commit may contain only letters, digits, dot, underscore, colon and hyphen" >&2; exit 2 ;; esac
if [ "$event" = "review-completed" ]; then
  case "$result" in PASS|FAIL) ;; *) echo "review-completed requires --result PASS or FAIL" >&2; exit 2 ;; esac
else
  [ -z "$result" ] || usage
fi
if [ -z "${VPS_COMMANDER_HUB_URL:-}" ] || [ -z "${VPS_COMMANDER_EVENT_TOKEN:-}" ]; then
  echo "SKIP: VPS_COMMANDER_HUB_URL or VPS_COMMANDER_EVENT_TOKEN is not configured; task continues without notification." >&2
  exit 0
fi
if [ "$event" = "development-completed" ]; then
  variables=$(TASK="$task_id" COMMIT="$commit" SUMMARY="$summary" python3 -c 'import json,os; print(json.dumps({"task":os.environ["TASK"],"commit":os.environ["COMMIT"],"summary":os.environ["SUMMARY"]},ensure_ascii=False))')
  key="development:$task_id:$commit"
else
  variables=$(RESULT="$result" COMMIT="$commit" SUMMARY="$summary" python3 -c 'import json,os; print(json.dumps({"result":os.environ["RESULT"],"commit":os.environ["COMMIT"],"summary":os.environ["SUMMARY"]},ensure_ascii=False))')
  key="review:$task_id:$commit:$result"
fi
if python3 "$(dirname "$0")/emit-notification-event.py" "$event" --variables "$variables" --idempotency-key "$key"; then
  exit 0
else
  echo "WARN: completion notification could not be delivered; task completion is not blocked." >&2
  exit 0
fi
