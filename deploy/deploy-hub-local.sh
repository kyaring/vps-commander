#!/bin/sh
set -eu

usage() {
  echo "Usage: $0 <binary-path> --sha256 <hex> [--dry-run]" >&2
  exit 2
}
[ "$#" -ge 3 ] || usage
BINARY=$1; shift
EXPECTED=""; DRY_RUN=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --sha256) [ "$#" -ge 2 ] || usage; EXPECTED=$(printf '%s' "$2" | tr 'A-F' 'a-f'); shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    *) usage ;;
  esac
done
case "$EXPECTED" in *[!0-9a-fA-F]*|'') echo "ERROR: --sha256 must be a 64-character hexadecimal digest" >&2; exit 1 ;; esac
[ "${#EXPECTED}" -eq 64 ] || { echo "ERROR: --sha256 must be a 64-character hexadecimal digest" >&2; exit 1; }
[ -f "$BINARY" ] && [ -r "$BINARY" ] || { echo "ERROR: binary is not a readable regular file" >&2; exit 1; }
ACTUAL=$(sha256sum "$BINARY" | awk '{print $1}')
[ "$ACTUAL" = "$EXPECTED" ] || { echo "ERROR: sha256 mismatch expected=$EXPECTED actual=$ACTUAL" >&2; exit 1; }

INSTALL_DIR=${VC_INSTALL_DIR:-/opt/vps-commander}
DB=${VC_DB:-/opt/vps-commander/data/commander.db}
BACKUP_ROOT=${VC_BACKUP_ROOT:-/opt/vps-commander}
SERVICE=${VC_SERVICE:-vps-commander-hub}
HEALTH_URL=${VC_HEALTH_URL:-http://192.168.20.132:9521/healthz}
HEALTH_TIMEOUT=${VC_HEALTH_TIMEOUT:-30}
SYSTEMCTL=${VC_SYSTEMCTL:-systemctl}
NEW_PATH="$INSTALL_DIR/vps-commander-hub.new"
FINAL_PATH="$INSTALL_DIR/vps-commander-hub"

if [ "$DRY_RUN" -eq 1 ]; then
  printf 'DRY RUN: verified sha256 %s\n' "$ACTUAL"
  printf 'Would create backup under %s/backup-pre-%s-<YYYYmmddHHMMSS>/\n' "$BACKUP_ROOT" "$(printf '%s' "$ACTUAL" | cut -c1-12)"
  printf 'Would back up old binary %s and SQLite database %s using sqlite3 .backup\n' "$FINAL_PATH" "$DB"
  printf 'Would atomically replace %s, restart %s via %s, and check %s (timeout %ss)\n' "$FINAL_PATH" "$SERVICE" "$SYSTEMCTL" "$HEALTH_URL" "$HEALTH_TIMEOUT"
  printf 'Would restore the old binary and restart the service if health check fails; database is not auto-rolled back.\n'
  exit 0
fi

[ -d "$INSTALL_DIR" ] || { echo "ERROR: install directory does not exist: $INSTALL_DIR" >&2; exit 1; }
[ -f "$FINAL_PATH" ] || { echo "ERROR: existing installed binary does not exist: $FINAL_PATH" >&2; exit 1; }
[ -f "$DB" ] || { echo "ERROR: database does not exist: $DB" >&2; exit 1; }
command -v sqlite3 >/dev/null 2>&1 || { echo "ERROR: sqlite3 is required for online database backup" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "ERROR: curl is required for health checks" >&2; exit 1; }

STAMP=$(date '+%Y%m%d%H%M%S')
BACKUP_DIR="$BACKUP_ROOT/backup-pre-$(printf '%s' "$ACTUAL" | cut -c1-12)-$STAMP"
mkdir -p "$BACKUP_ROOT"
if ! mkdir "$BACKUP_DIR"; then echo "ERROR: backup directory already exists: $BACKUP_DIR" >&2; exit 1; fi
if ! cp -p "$FINAL_PATH" "$BACKUP_DIR/vps-commander-hub"; then
  echo "ERROR: old binary backup failed; deployment aborted" >&2; exit 1
fi
DB_BACKUP="$BACKUP_DIR/commander.db"
if ! sqlite3 "$DB" ".backup '$DB_BACKUP'"; then
  echo "ERROR: SQLite online backup failed; deployment aborted; partial backup=$BACKUP_DIR" >&2
  exit 1
fi
[ -s "$DB_BACKUP" ] || { echo "ERROR: SQLite backup is empty; deployment aborted; partial backup=$BACKUP_DIR" >&2; exit 1; }

print_db_restore_hint() {
  printf 'Database was not rolled back. After confirming a safe stop-write window and obtaining approval, manual restore command:\n  sqlite3 "%s" ".restore %s"\n' "$DB" "$DB_BACKUP" >&2
}

health_ok() {
  deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while [ "$(date +%s)" -le "$deadline" ]; do
    response=$(curl -fsS --max-time 2 "$HEALTH_URL" 2>/dev/null || true)
    if printf '%s' "$response" | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if isinstance(d,dict) and d.get("status")=="ok" else 1)' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

if ! cp "$BINARY" "$NEW_PATH"; then echo "ERROR: staging new binary failed; backup=$BACKUP_DIR" >&2; exit 1; fi
chmod 0755 "$NEW_PATH"
if ! mv -f "$NEW_PATH" "$FINAL_PATH"; then echo "ERROR: atomic replacement failed; backup=$BACKUP_DIR" >&2; exit 1; fi
if ! "$SYSTEMCTL" restart "$SERVICE"; then
  echo "WARN: service restart command failed; attempting binary rollback" >&2
else
  if health_ok; then
    printf 'DEPLOY OK %s backup=%s\n' "$ACTUAL" "$BACKUP_DIR"
    exit 0
  fi
  echo "WARN: health check failed; attempting binary rollback" >&2
fi

if cp "$BACKUP_DIR/vps-commander-hub" "$NEW_PATH" && chmod 0755 "$NEW_PATH" && mv -f "$NEW_PATH" "$FINAL_PATH" && "$SYSTEMCTL" restart "$SERVICE" && health_ok; then
  printf 'ROLLBACK OK backup=%s\n' "$BACKUP_DIR" >&2
  print_db_restore_hint
  exit 1
fi
printf 'ROLLBACK FAILED backup=%s\n' "$BACKUP_DIR" >&2
print_db_restore_hint
exit 2
