#!/bin/sh
# Shared Hub listen-address discovery. Sourcing this file only defines functions.

hub_listen_addr() {
    if [ -n "${VPS_COMMANDER_HUB_ADDR:-}" ]; then
        printf '%s\n' "$VPS_COMMANDER_HUB_ADDR"
        return 0
    fi

    exec_start=
    if command -v systemctl >/dev/null 2>&1; then
        exec_start=$(systemctl show vps-commander-hub.service -p ExecStart --value 2>/dev/null) || exec_start=
    fi

    parsed_addr=$(printf '%s\n' "$exec_start" | awk -F';' '
        {
            for (i = 1; i <= NF; i++) {
                if ($i ~ /argv\[\]=/) {
                    segment = $i
                    sub(/^.*argv\[\]=/, "", segment)
                    count = split(segment, args, /[[:space:]]+/)
                    for (j = 1; j < count; j++) {
                        if (args[j] == "-addr" && args[j + 1] != "") {
                            print args[j + 1]
                            exit
                        }
                    }
                }
            }
        }
    ')
    case "$parsed_addr" in
        *:*)
            case "$parsed_addr" in *[!A-Za-z0-9._:\[\]-]*) parsed_addr= ;; esac
            ;;
        *) parsed_addr= ;;
    esac
    if [ -n "$parsed_addr" ]; then
        printf '%s\n' "$parsed_addr"
        return 0
    fi

    fallback_port=${HUB_PORT:-9521}
    printf '%s\n' "WARNING: unable to determine Hub listen address from VPS_COMMANDER_HUB_ADDR or systemd ExecStart; falling back to 127.0.0.1:${fallback_port}" >&2
    printf '127.0.0.1:%s\n' "$fallback_port"
}

hub_health_check_url() {
    addr=$(hub_listen_addr) || return 1
    printf 'http://%s/healthz\n' "$addr"
}
