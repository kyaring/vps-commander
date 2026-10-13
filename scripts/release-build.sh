#!/bin/sh
set -eu

usage() {
  echo "Usage: $0 [--out DIR]" >&2
  exit 2
}
OUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --out) [ "$#" -ge 2 ] || usage; OUT=$2; shift 2 ;;
    *) usage ;;
  esac
done

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "ERROR: not inside a git worktree" >&2; exit 1; }
cd "$ROOT"
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
  echo "ERROR: working tree must be clean" >&2
  exit 1
fi

export GOTOOLCHAIN=go1.24.4
GOVERSION=$(go version 2>&1) || { echo "ERROR: unable to run go version" >&2; exit 1; }
case "$GOVERSION" in
  *go1.24.4*) ;;
  *) echo "ERROR: required Go toolchain go1.24.4, got: $GOVERSION" >&2; exit 1 ;;
esac
COMMIT=$(git rev-parse HEAD)
SHORT=$(printf '%s' "$COMMIT" | cut -c1-12)
if [ -z "$OUT" ]; then OUT="/root/work/release/$SHORT"; fi
mkdir -p "$OUT"
BIN="$OUT/vps-commander-hub"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -buildid=" -o "$BIN" ./cmd/hub
HASH=$(sha256sum "$BIN" | awk '{print $1}')
printf '%s  %s\n' "$HASH" "$BIN" > "$BIN.sha256"
BUILT_AT=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
{
  printf 'commit=%s\n' "$COMMIT"
  printf 'go_version=%s\n' "$GOVERSION"
  printf 'built_at_utc=%s\n' "$BUILT_AT"
  printf 'sha256=%s\n' "$HASH"
} > "$OUT/build-info.txt"
printf 'BUILD OK %s %s\n' "$HASH" "$BIN"
