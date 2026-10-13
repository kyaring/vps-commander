#!/bin/sh
# Optional final-boundary hook for repository agents. Missing credentials are a
# non-blocking skip; this script must not be run for intermediate commits.
set -eu

usage() {
  echo "Usage: $0 development-completed|review-completed --task-id ID --commit SHA --summary TEXT [--result PASS|FAIL]" >&2
  echo "       $0 task-reported --task-id ID --status done|blocked|failed --summary TEXT [--type TYPE] [--employee ID] [--project NAME] [--ref TEXT]" >&2
  exit 2
}
[ "$#" -ge 1 ] || usage
event="$1"; shift
case "$event" in development-completed|review-completed|task-reported) ;; *) usage ;; esac
task_id=""; commit=""; summary=""; result=""; task_type=""; status=""; employee=""; project=""; ref=""; employee_set=0; project_set=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --task-id) [ "$#" -ge 2 ] || usage; task_id="$2"; shift 2 ;;
    --commit) [ "$#" -ge 2 ] || usage; commit="$2"; shift 2 ;;
    --summary) [ "$#" -ge 2 ] || usage; summary="$2"; shift 2 ;;
    --result) [ "$#" -ge 2 ] || usage; result="$2"; shift 2 ;;
    --type) [ "$#" -ge 2 ] || usage; task_type="$2"; shift 2 ;;
    --status) [ "$#" -ge 2 ] || usage; status="$2"; shift 2 ;;
    --employee) [ "$#" -ge 2 ] || usage; employee="$2"; employee_set=1; shift 2 ;;
    --project) [ "$#" -ge 2 ] || usage; project="$2"; project_set=1; shift 2 ;;
    --ref) [ "$#" -ge 2 ] || usage; ref="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$task_id" ] && [ -n "$summary" ] || usage
if [ "$event" = "task-reported" ]; then
  [ -z "$commit" ] || usage
else
  [ -n "$commit" ] || usage
fi
case "$task_id$commit" in *[!A-Za-z0-9._:-]*) echo "task-id and commit may contain only letters, digits, dot, underscore, colon and hyphen" >&2; exit 2 ;; esac
if [ "$event" = "review-completed" ]; then
  case "$result" in PASS|FAIL) ;; *) echo "review-completed requires --result PASS or FAIL" >&2; exit 2 ;; esac
elif [ "$event" = "development-completed" ]; then
  [ -z "$result" ] || usage
else
  [ -z "$commit" ] || { echo "task-reported does not accept --commit" >&2; exit 2; }
  [ -z "$result" ] || usage
  case "$status" in done|blocked|failed) ;; *) echo "task-reported requires --status done, blocked or failed" >&2; exit 2 ;; esac
  [ -n "$summary" ] || { echo "task-reported requires --summary TEXT" >&2; exit 2; }
  [ "$employee_set" -eq 1 ] || employee="${VPS_COMMANDER_EMPLOYEE:-}"
  [ "$project_set" -eq 1 ] || project="${VPS_COMMANDER_PROJECT:-}"
  TASK_TYPE="$task_type" EMPLOYEE="$employee" PROJECT="$project" REF="$ref" SUMMARY="$summary" python3 -c '
import os,sys,unicodedata
for name,limit in (("EMPLOYEE",32),("PROJECT",32),("TASK_TYPE",16),("REF",64)):
 value=os.environ.get(name,"").strip()
 if not value: continue
 if len(value)>limit or any(unicodedata.category(ch)=="Cc" or ch in "【】" for ch in value):
  print("invalid task-reported field: "+name.lower(),file=sys.stderr); sys.exit(2)
if not os.environ.get("SUMMARY","").strip():
 print("task-reported requires --summary TEXT",file=sys.stderr); sys.exit(2)
'
fi
if [ -z "${VPS_COMMANDER_HUB_URL:-}" ] || [ -z "${VPS_COMMANDER_EVENT_TOKEN:-}" ]; then
  echo "SKIP: VPS_COMMANDER_HUB_URL or VPS_COMMANDER_EVENT_TOKEN is not configured; task continues without notification." >&2
  exit 0
fi
if [ "$event" = "development-completed" ]; then
  variables=$(TASK="$task_id" COMMIT="$commit" SUMMARY="$summary" python3 -c 'import json,os; print(json.dumps({"task":os.environ["TASK"],"commit":os.environ["COMMIT"],"summary":os.environ["SUMMARY"]},ensure_ascii=False))')
  key="development:$task_id:$commit"
elif [ "$event" = "review-completed" ]; then
  variables=$(RESULT="$result" COMMIT="$commit" SUMMARY="$summary" python3 -c 'import json,os; print(json.dumps({"result":os.environ["RESULT"],"commit":os.environ["COMMIT"],"summary":os.environ["SUMMARY"]},ensure_ascii=False))')
  key="review:$task_id:$commit:$result"
else
  variables=$(TASK="$task_id" TASK_TYPE="$task_type" STATUS="$status" SUMMARY="$summary" EMPLOYEE="$employee" PROJECT="$project" REF="$ref" python3 -c 'import json,os; d={"task":os.environ["TASK"],"status":os.environ["STATUS"],"summary":os.environ["SUMMARY"]}; [d.__setitem__(k,os.environ[e]) for k,e in (("type","TASK_TYPE"),("employee","EMPLOYEE"),("project","PROJECT"),("ref","REF")) if os.environ[e].strip()]; print(json.dumps(d,ensure_ascii=False))')
  key_suffix=$(REF="$ref" SUMMARY="$summary" python3 -c 'import hashlib,os; value=os.environ["REF"].strip() or os.environ["SUMMARY"]; print(hashlib.sha256(value.encode()).hexdigest()[:12])')
  key="task:$task_id:$status:$key_suffix"
fi
if output=$(python3 "$(dirname "$0")/emit-notification-event.py" "$event" --variables "$variables" --idempotency-key "$key" 2>&1); then
  [ -z "$output" ] || printf '%s\n' "$output"
  exit 0
else
  status=$(printf '%s\n' "$output" | python3 -c 'import json,sys;
for line in reversed(sys.stdin.read().splitlines()):
 try:
  value=json.loads(line)
  if isinstance(value,dict) and value.get("ok") is False and isinstance(value.get("status"),int):
   print(value["status"]); break
 except (ValueError,TypeError):
  pass
else:
 print("network")')
  [ -z "$output" ] || printf '%s\n' "$output" >&2
  echo "WARN: completion notification not delivered (status=$status); task completion is not blocked." >&2
  exit 0
fi
