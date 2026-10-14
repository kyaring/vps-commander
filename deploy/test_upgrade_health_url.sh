#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/vc-health-url.XXXXXX")
trap 'rm -rf "$TMP_DIR"' 0 HUP INT TERM

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

cat > "${TMP_DIR}/systemctl" <<'STUB'
#!/bin/sh
if [ "${SYSTEMCTL_STUB_MODE:-}" = fail ]; then exit 1; fi
case "$*" in
    'show vps-commander-hub.service -p ExecStart --value')
        case "${SYSTEMCTL_STUB_MODE:-}" in
            remote) printf '%s\n' '{ path=/usr/local/bin/vps-commander-hub ; argv[]=/usr/local/bin/vps-commander-hub -addr 192.168.20.132:9521 -db /var/lib/commander.db ; ignore_errors=no ; }' ;;
            loopback) printf '%s\n' '{ path=/usr/local/bin/vps-commander-hub ; argv[]=/usr/local/bin/vps-commander-hub -addr 127.0.0.1:9521 -db /var/lib/commander.db ; ignore_errors=no ; }' ;;
            malformed) printf '%s\n' '{ path=/usr/local/bin/vps-commander-hub ; argv[]=/usr/local/bin/vps-commander-hub -db /var/lib/commander.db ; ignore_errors=no ; }' ;;
            *) exit 2 ;;
        esac ;;
    *) exit 2 ;;
esac
STUB
chmod +x "${TMP_DIR}/systemctl"

# Integration guard makes case 1 fail if upgrade-hub.sh is reverted to a hard-coded loopback URL.
if ! grep -q 'HUB_HEALTH_URL=$(hub_health_check_url)' "${SCRIPT_DIR}/upgrade-hub.sh" || \
   ! grep -q '"${HUB_HEALTH_URL}"' "${SCRIPT_DIR}/upgrade-hub.sh" || \
   grep -q 'http://127\.0\.0\.1:' "${SCRIPT_DIR}/upgrade-hub.sh"; then
    fail 'case 1: upgrade-hub.sh must use the derived health URL, not a hard-coded loopback URL'
fi

PATH="${TMP_DIR}:$PATH"
export PATH
. "${SCRIPT_DIR}/lib-hub-addr.sh"

SYSTEMCTL_STUB_MODE=remote; export SYSTEMCTL_STUB_MODE
actual=$(hub_health_check_url 2>"${TMP_DIR}/stderr")
[ "$actual" = 'http://192.168.20.132:9521/healthz' ] || fail "case 1: expected remote URL, got ${actual}"
[ ! -s "${TMP_DIR}/stderr" ] || fail 'case 1: unexpected stderr output'
pass 'case 1: derives remote bind address from systemd argv'

SYSTEMCTL_STUB_MODE=loopback; export SYSTEMCTL_STUB_MODE
actual=$(hub_health_check_url 2>"${TMP_DIR}/stderr")
[ "$actual" = 'http://127.0.0.1:9521/healthz' ] || fail "case 2: expected loopback URL, got ${actual}"
[ ! -s "${TMP_DIR}/stderr" ] || fail 'case 2: unexpected stderr output'
pass 'case 2: preserves loopback bind address'

SYSTEMCTL_STUB_MODE=malformed; export SYSTEMCTL_STUB_MODE
actual=$(hub_health_check_url 2>"${TMP_DIR}/stderr")
[ "$actual" = 'http://127.0.0.1:9521/healthz' ] || fail "case 3: expected fallback URL, got ${actual}"
grep -qi 'WARNING: unable to determine Hub listen address' "${TMP_DIR}/stderr" || fail 'case 3: fallback warning missing from stderr'
pass 'case 3: malformed systemd argv falls back with stderr warning'

SYSTEMCTL_STUB_MODE=fail; export SYSTEMCTL_STUB_MODE
actual=$(hub_health_check_url 2>"${TMP_DIR}/stderr")
[ "$actual" = 'http://127.0.0.1:9521/healthz' ] || fail "case 4: expected fallback URL when systemctl fails, got ${actual}"
grep -qi 'WARNING: unable to determine Hub listen address' "${TMP_DIR}/stderr" || fail 'case 4: systemctl failure warning missing from stderr'
pass 'case 4: systemctl failure falls back with stderr warning'

VPS_COMMANDER_HUB_ADDR=10.10.10.20:9522; export VPS_COMMANDER_HUB_ADDR
SYSTEMCTL_STUB_MODE=remote; export SYSTEMCTL_STUB_MODE
actual=$(hub_health_check_url 2>"${TMP_DIR}/stderr")
[ "$actual" = 'http://10.10.10.20:9522/healthz' ] || fail "override case: expected override URL, got ${actual}"
[ ! -s "${TMP_DIR}/stderr" ] || fail 'override case: unexpected stderr output'
pass 'override case: VPS_COMMANDER_HUB_ADDR takes precedence'

printf '%s\n' 'All health URL tests passed.'
