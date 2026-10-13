#!/bin/sh
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
GO_ENV_SCRIPT=$SCRIPT_DIR/go-env.sh

if ! PATH=/usr/bin:/bin sh -c '. "$1" && command -v go >/dev/null 2>&1' sh "$GO_ENV_SCRIPT"; then
    echo 'FAIL: Go PATH fallback did not find go' >&2
    exit 1
fi

set +e
output=$(PATH=/usr/bin:/bin GO_ENV_CANDIDATES=/nonexistent sh -c '. "$1"' sh "$GO_ENV_SCRIPT" 2>&1)
status=$?
set -e
if [ "$status" -eq 0 ]; then
    echo 'FAIL: missing toolchain unexpectedly succeeded' >&2
    exit 1
fi
case $output in
    *'go toolchain not found'*) ;;
    *) echo 'FAIL: missing toolchain error was not explicit' >&2; exit 1 ;;
esac
printf '%s\n' 'go-env tests: PASS'
