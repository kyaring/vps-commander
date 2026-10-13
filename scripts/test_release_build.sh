#!/bin/sh
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SOURCE_ROOT=$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)
TMP=$(mktemp -d)
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT HUP INT TERM
REPO="$TMP/repo"
git clone --quiet --shared "$SOURCE_ROOT" "$REPO"
cd "$REPO"
# Ensure the clone has a clean, committed copy of the script under test even
# when this test is first run before the feature commit is created.
cp "$SCRIPT_DIR/release-build.sh" scripts/release-build.sh
chmod 755 scripts/release-build.sh
git add scripts/release-build.sh
if ! git diff --cached --quiet; then
  git -c user.name='Build Test' -c user.email='build-test@example.invalid' commit --quiet -m 'test fixture: include release build script'
fi
OUT1="$TMP/out-one"
OUT2="$TMP/out-two"
sh scripts/release-build.sh --out "$OUT1" > "$TMP/build-one.log"
sh scripts/release-build.sh --out "$OUT2" > "$TMP/build-two.log"
HASH1=$(sha256sum "$OUT1/vps-commander-hub" | awk '{print $1}')
HASH2=$(sha256sum "$OUT2/vps-commander-hub" | awk '{print $1}')
[ "$HASH1" = "$HASH2" ] || { echo "reproducibility failed: $HASH1 != $HASH2" >&2; exit 1; }
test -s "$OUT1/vps-commander-hub"
test -s "$OUT1/vps-commander-hub.sha256"
test -s "$OUT1/build-info.txt"
grep -q "^$HASH1  $OUT1/vps-commander-hub$" "$OUT1/vps-commander-hub.sha256"
grep -q "^commit=[0-9a-f]\{40\}$" "$OUT1/build-info.txt"
grep -q '^go_version=.*go1.24.4' "$OUT1/build-info.txt"
grep -q '^built_at_utc=.*Z$' "$OUT1/build-info.txt"
grep -q "^sha256=$HASH1$" "$OUT1/build-info.txt"
# An untracked file makes the worktree dirty and must be rejected before output.
touch "$REPO/dirty-untracked-file"
if sh scripts/release-build.sh --out "$TMP/dirty-out" > "$TMP/dirty.log" 2>&1; then
  echo "dirty working tree was accepted" >&2; exit 1
fi
grep -q 'working tree must be clean' "$TMP/dirty.log"
test ! -e "$TMP/dirty-out"
printf 'PASS reproducible release build: sha256-one=%s sha256-two=%s\n' "$HASH1" "$HASH2"
