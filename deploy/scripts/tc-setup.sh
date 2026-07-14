#!/usr/bin/env bash
# Prime the root HTB qdisc trees used for per-session shaping.
# Idempotent — safe to re-run.
#
# Layout:
#   ens160 egress  →  guest UPLOAD   shaping  (root 1: htb, default 1:1 unshaped)
#   br-lan egress  →  guest DOWNLOAD shaping  (root 1: htb, default 1:1 unshaped)
#
# Per-session classes/filters are added at runtime by scd with classid
#     1:<last_octet_of_ip>   (see data-plane/internal/shape/shape.go).
set -euo pipefail

WAN=ens160
LAN=br-lan

# Total aggregate ceiling. Tune per-appliance later via control-plane.
CEIL=1gbit

prime() {
    local IF=$1
    # Wipe any existing qdisc and rebuild.
    tc qdisc del dev "$IF" root 2>/dev/null || true
    tc qdisc add dev "$IF" root handle 1: htb default 1 r2q 10

    # Root aggregate class.
    tc class add dev "$IF" parent 1: classid 1:fffe htb \
        rate "$CEIL" ceil "$CEIL" burst 1m

    # Default class — catches anything not matched by a per-session filter
    # (includes the appliance's own outbound traffic on WAN, and portal/DHCP/DNS
    # replies on LAN). Given ceil, guests still share this bucket pre-auth.
    tc class add dev "$IF" parent 1:fffe classid 1:1 htb \
        rate "$CEIL" ceil "$CEIL" burst 1m
    tc qdisc add dev "$IF" parent 1:1 handle 10: fq_codel

    echo "primed $IF:"
    tc -s qdisc show dev "$IF"
}

prime "$WAN"
prime "$LAN"
