#!/bin/bash
# Source the rendered production script with shell functions replacing every
# external command. No host services are queried or changed by these tests.
set -uo pipefail
sleeps=0
kubelet_starts=0
chain_active=0

systemctl() {
    printf 'systemctl %s\n' "$*" >> "$TRACE"
    case "$*" in
        'reset-failed ovsdb-server.service ovs-vswitchd.service openvswitch.service nmstate-configuration.service ovs-configuration.service kubelet-dependencies.target crio.service kubelet.service')
            [[ "$SCENARIO" != reset-failure ]]
            ;;
        'start --no-block openvswitch.service')
            [[ "$SCENARIO" != queue-failure || "$sleeps" -gt 0 ]]
            ;;
        'start kubelet.service')
            kubelet_starts=$((kubelet_starts + 1))
            [[ "$SCENARIO" != interrupted ]] || kill -TERM "$$"
            [[ "$SCENARIO" != persistent-kubelet ]] || return 1
            [[ "$SCENARIO" != transient-kubelet || "$kubelet_starts" -gt 1 ]] || return 1
            chain_active=1
            ;;
        'is-active --quiet ovsdb-server.service' | \
        'is-active --quiet ovs-vswitchd.service' | \
        'is-active --quiet openvswitch.service')
            [[ "$SCENARIO" != persistent-ovs ]] || return 1
            if [[ "$sleeps" -eq 0 ]]; then
                case "$SCENARIO" in
                    delayed-ovs | queue-failure) return 1 ;;
                    missing-ovs-unit) [[ "$3" != "$INACTIVE_UNIT" ]] || return 1 ;;
                    ovs-regression) [[ "$kubelet_starts" -eq 0 ]] || return 1 ;;
                esac
            fi
            return 0
            ;;
        'is-active --quiet kubelet-dependencies.target' | \
        'is-active --quiet crio.service' | \
        'is-active --quiet kubelet.service')
            [[ "$SCENARIO" == healthy ]] && return 0
            if [[ "$SCENARIO" == inactive-chain ]]; then
                [[ "$3" != "$INACTIVE_UNIT" || "$kubelet_starts" -gt 1 ]]
            else
                [[ "$chain_active" -eq 1 ]]
            fi
            ;;
        *)
            printf 'unexpected systemctl command: %s\n' "$*" >> "$TRACE"
            exit 99
            ;;
    esac
}

ovs-vsctl() {
    printf 'ovs-vsctl %s\n' "$*" >> "$TRACE"
    [[ "$*" == '--timeout=5 show' ]] || exit 99
    [[ "$SCENARIO" != persistent-database ]] || return 1
    [[ "$SCENARIO" != database-unavailable || "$sleeps" -gt 0 ]]
}

sleep() {
    printf 'sleep %s\n' "$*" >> "$TRACE"
    [[ "$*" == 10 ]] || exit 99
    sleeps=$((sleeps + 1))
}

source "$1"
