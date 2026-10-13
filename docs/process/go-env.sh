#!/bin/sh
# Ensure the Go toolchain is available when this file is sourced.
GO_ENV_CANDIDATES="${GO_ENV_CANDIDATES:-/usr/local/go/bin:/usr/lib/go/bin:/opt/go/bin}"
if command -v go >/dev/null 2>&1; then
    return 0
fi
GO_ENV_REMAINING=$GO_ENV_CANDIDATES
while [ -n "$GO_ENV_REMAINING" ]; do
    case $GO_ENV_REMAINING in
        *:*) GO_ENV_DIR=${GO_ENV_REMAINING%%:*}; GO_ENV_REMAINING=${GO_ENV_REMAINING#*:} ;;
        *) GO_ENV_DIR=$GO_ENV_REMAINING; GO_ENV_REMAINING= ;;
    esac
    if [ -n "$GO_ENV_DIR" ] && [ -f "$GO_ENV_DIR/go" ] && [ -x "$GO_ENV_DIR/go" ]; then
        PATH="$GO_ENV_DIR:$PATH"
        export PATH
        unset GO_ENV_DIR GO_ENV_REMAINING
        return 0
    fi
done
unset GO_ENV_DIR GO_ENV_REMAINING
printf '%s\n' 'go toolchain not found: searched GO_ENV_CANDIDATES' >&2
exit 1
