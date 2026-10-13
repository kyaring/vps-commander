#!/bin/sh
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DEPLOY="$SCRIPT_DIR/deploy-hub-local.sh"
TMP=$(mktemp -d)
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
  rm -rf "$TMP"
}
trap cleanup EXIT HUP INT TERM

cat > "$TMP/health_server.py" <<'PY'
import http.server, pathlib, sys
mode_file = pathlib.Path(sys.argv[1])
restart_file = pathlib.Path(sys.argv[2])
port_file = pathlib.Path(sys.argv[3])
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        mode = mode_file.read_text().strip()
        restarts = int(restart_file.read_text().strip() or "0") if restart_file.exists() else 0
        healthy = mode == "ok" or (mode == "fail-until-rollback" and restarts >= 2)
        body = b'{"status":"ok"}' if healthy else b'{"status":"degraded"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *_args): pass
server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
port_file.write_text(str(server.server_port))
server.serve_forever()
PY
start_server() {
  mode=$1; restarts=$2
  printf '%s\n' "$mode" > "$TMP/mode"
  printf '%s\n' "$restarts" > "$TMP/restarts"
  rm -f "$TMP/port"
  python3 "$TMP/health_server.py" "$TMP/mode" "$TMP/restarts" "$TMP/port" &
  SERVER_PID=$!
  i=0
  while [ ! -s "$TMP/port" ]; do i=$((i + 1)); [ "$i" -lt 50 ] || { echo 'health server failed to start' >&2; exit 1; }; sleep 0.1; done
  export VC_HEALTH_URL="http://127.0.0.1:$(cat "$TMP/port")/healthz"
}
stop_server() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; SERVER_PID=""; fi
}
make_fixture() {
  base=$1
  mkdir -p "$base/install/data" "$base/backup"
  printf '#!/bin/sh\necho OLD-BINARY\n' > "$base/install/vps-commander-hub"
  chmod 755 "$base/install/vps-commander-hub"
  sqlite3 "$base/install/data/commander.db" 'CREATE TABLE sample (value TEXT); INSERT INTO sample VALUES ("preserved");'
  printf '#!/bin/sh\necho NEW-BINARY\n' > "$base/new-binary"
  chmod 755 "$base/new-binary"
  sha256sum "$base/new-binary" | awk '{print $1}' > "$base/hash"
  cat > "$base/fake-systemctl" <<'SH2'
#!/bin/sh
[ "$1" = restart ] || exit 9
printf '%s\n' "$2" >> "$VC_TEST_SYSTEMCTL_LOG"
count=$(wc -l < "$VC_TEST_SYSTEMCTL_LOG")
printf '%s\n' "$count" > "$VC_TEST_RESTART_COUNT"
SH2
  chmod 755 "$base/fake-systemctl"
}
base_env() {
  export VC_INSTALL_DIR="$1/install"
  export VC_DB="$1/install/data/commander.db"
  export VC_BACKUP_ROOT="$1/backup"
  export VC_SERVICE='test-hub'
  export VC_HEALTH_TIMEOUT=2
  export VC_SYSTEMCTL="$1/fake-systemctl"
  export VC_TEST_SYSTEMCTL_LOG="$1/systemctl.log"
  export VC_TEST_RESTART_COUNT="$TMP/restarts"
  : > "$VC_TEST_SYSTEMCTL_LOG"
  printf '0\n' > "$VC_TEST_RESTART_COUNT"
}

# (a) Successful deployment: backup old binary and online SQLite snapshot.
SUCCESS="$TMP/success"
mkdir -p "$SUCCESS"
make_fixture "$SUCCESS"
base_env "$SUCCESS"
start_server ok 0
OUT=$(sh "$DEPLOY" "$SUCCESS/new-binary" --sha256 "$(cat "$SUCCESS/hash")" 2>&1)
stop_server
case "$OUT" in *'DEPLOY OK '*) ;; *) echo "success path missing DEPLOY OK: $OUT" >&2; exit 1 ;; esac
BACKUP=$(printf '%s\n' "$OUT" | sed -n 's/.*backup=//p' | tail -1)
test -f "$BACKUP/vps-commander-hub"
test -s "$BACKUP/commander.db"
grep -q 'OLD-BINARY' "$BACKUP/vps-commander-hub"
grep -q 'NEW-BINARY' "$VC_INSTALL_DIR/vps-commander-hub"
test "$(sqlite3 "$BACKUP/commander.db" 'SELECT value FROM sample;')" = preserved
[ "$(stat -c '%a' "$VC_INSTALL_DIR/vps-commander-hub")" = 755 ]

# (b) Hash mismatch must not mutate installed binary, DB, backup tree or call systemctl.
MISMATCH="$TMP/mismatch"
mkdir -p "$MISMATCH"
make_fixture "$MISMATCH"
base_env "$MISMATCH"
BEFORE=$(sha256sum "$VC_INSTALL_DIR/vps-commander-hub" "$VC_DB" | sha256sum | awk '{print $1}')
if sh "$DEPLOY" "$MISMATCH/new-binary" --sha256 "$(printf '0%.0s' $(seq 1 64))" > "$TMP/mismatch.log" 2>&1; then echo 'sha256 mismatch was accepted' >&2; exit 1; fi
AFTER=$(sha256sum "$VC_INSTALL_DIR/vps-commander-hub" "$VC_DB" | sha256sum | awk '{print $1}')
test "$BEFORE" = "$AFTER"
test -z "$(find "$VC_BACKUP_ROOT" -mindepth 1 -print -quit)"
test ! -s "$VC_TEST_SYSTEMCTL_LOG"

# (c) Health check fails on new binary; fake service becomes healthy after rollback restart.
ROLLBACK="$TMP/rollback"
mkdir -p "$ROLLBACK"
make_fixture "$ROLLBACK"
base_env "$ROLLBACK"
start_server fail-until-rollback 0
set +e
OUT=$(sh "$DEPLOY" "$ROLLBACK/new-binary" --sha256 "$(cat "$ROLLBACK/hash")" 2>&1)
RC=$?
set -e
stop_server
test "$RC" -eq 1 || { echo "rollback expected exit 1, got $RC: $OUT" >&2; exit 1; }
case "$OUT" in *'ROLLBACK OK backup='*) ;; *) echo "rollback success marker missing: $OUT" >&2; exit 1 ;; esac
grep -q 'OLD-BINARY' "$VC_INSTALL_DIR/vps-commander-hub"
case "$OUT" in *'Database was not rolled back.'*) ;; *) echo "manual DB recovery note missing: $OUT" >&2; exit 1 ;; esac

# (d) Dry run validates hash and prints plan without any filesystem or service changes.
DRY="$TMP/dry"
mkdir -p "$DRY"
make_fixture "$DRY"
base_env "$DRY"
BEFORE=$(find "$DRY" -type f -printf '%P %s %T@\n' | sort | sha256sum | awk '{print $1}')
OUT=$(sh "$DEPLOY" "$DRY/new-binary" --sha256 "$(cat "$DRY/hash")" --dry-run 2>&1)
case "$OUT" in *'DRY RUN: verified sha256'*) ;; *) echo "dry-run plan missing: $OUT" >&2; exit 1 ;; esac
AFTER=$(find "$DRY" -type f -printf '%P %s %T@\n' | sort | sha256sum | awk '{print $1}')
test "$BEFORE" = "$AFTER"
test ! -s "$VC_TEST_SYSTEMCTL_LOG"
test ! -e "$VC_INSTALL_DIR/vps-commander-hub.new"

echo 'PASS local deploy script: success/SQLite backup, hash mismatch no-op, health rollback, and dry-run no-op'
