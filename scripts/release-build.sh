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

AGENT_AMD64_BIN="$OUT/vps-commander-agent-linux-amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -buildid=" -o "$AGENT_AMD64_BIN" ./cmd/agent
AGENT_AMD64_HASH=$(sha256sum "$AGENT_AMD64_BIN" | awk '{print $1}')
printf '%s  %s\n' "$AGENT_AMD64_HASH" "$AGENT_AMD64_BIN" > "$AGENT_AMD64_BIN.sha256"

AGENT_ARM64_BIN="$OUT/vps-commander-agent-linux-arm64"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -buildid=" -o "$AGENT_ARM64_BIN" ./cmd/agent
AGENT_ARM64_HASH=$(sha256sum "$AGENT_ARM64_BIN" | awk '{print $1}')
printf '%s  %s\n' "$AGENT_ARM64_HASH" "$AGENT_ARM64_BIN" > "$AGENT_ARM64_BIN.sha256"

AGENT_ARMV7_BIN="$OUT/vps-commander-agent-linux-armv7"
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w -buildid=" -o "$AGENT_ARMV7_BIN" ./cmd/agent
AGENT_ARMV7_HASH=$(sha256sum "$AGENT_ARMV7_BIN" | awk '{print $1}')
printf '%s  %s\n' "$AGENT_ARMV7_HASH" "$AGENT_ARMV7_BIN" > "$AGENT_ARMV7_BIN.sha256"

{
  printf '%s  vps-commander-hub\n' "$HASH"
  printf '%s  vps-commander-agent-linux-amd64\n' "$AGENT_AMD64_HASH"
  printf '%s  vps-commander-agent-linux-arm64\n' "$AGENT_ARM64_HASH"
  printf '%s  vps-commander-agent-linux-armv7\n' "$AGENT_ARMV7_HASH"
} > "$OUT/checksums.txt"

BUILT_AT=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
{
  printf 'commit=%s\n' "$COMMIT"
  printf 'go_version=%s\n' "$GOVERSION"
  printf 'built_at_utc=%s\n' "$BUILT_AT"
  printf 'sha256=%s\n' "$HASH"
  printf 'agent_linux_amd64_sha256=%s\n' "$AGENT_AMD64_HASH"
  printf 'agent_linux_arm64_sha256=%s\n' "$AGENT_ARM64_HASH"
  printf 'agent_linux_armv7_sha256=%s\n' "$AGENT_ARMV7_HASH"
} > "$OUT/build-info.txt"
printf 'BUILD OK %s %s\n' "$HASH" "$BIN"
