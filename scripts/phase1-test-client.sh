#!/usr/bin/env bash
# Create a network namespace `client1` plumbed into br-lan via veth.
# Usage:
#   ./phase1-test-client.sh up       # bring up the client
#   ./phase1-test-client.sh dhcp     # request a DHCP lease
#   ./phase1-test-client.sh probe    # run the captive/auth test probes
#   ./phase1-test-client.sh shell    # drop into a shell inside the ns
#   ./phase1-test-client.sh down     # tear down
set -euo pipefail

NS=client1
VETH_HOST=vcli1a
VETH_NS=eth0
BRIDGE=br-lan

need_root() { [ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }; }

up() {
    need_root
    ip netns add "$NS" 2>/dev/null || true
    ip link add "$VETH_HOST" type veth peer name "${VETH_NS}_p"
    ip link set "$VETH_HOST" master "$BRIDGE"
    ip link set "$VETH_HOST" up
    ip link set "${VETH_NS}_p" netns "$NS"
    ip netns exec "$NS" ip link set lo up
    ip netns exec "$NS" ip link set "${VETH_NS}_p" name "$VETH_NS"
    ip netns exec "$NS" ip link set "$VETH_NS" up
    echo "ns=$NS veth=$VETH_HOST → $BRIDGE, inner=$VETH_NS (up, no address)"
    ip -n "$NS" link show "$VETH_NS"
}

dhcp() {
    need_root
    # Release any prior lease, then request fresh.
    ip netns exec "$NS" pkill -9 dhclient 2>/dev/null || true
    rm -f "/var/lib/dhcp/dhclient.$NS.leases" 2>/dev/null
    ip netns exec "$NS" dhclient -4 -v -1 "$VETH_NS" 2>&1 | tail -10
    echo "---"
    ip -n "$NS" addr show "$VETH_NS"
    echo "---"
    ip -n "$NS" route
    echo "---"
    cat /etc/netns/"$NS"/resolv.conf 2>/dev/null || ip netns exec "$NS" cat /etc/resolv.conf 2>/dev/null | head -5
}

probe() {
    need_root
    local IP
    IP=$(ip -n "$NS" -4 addr show "$VETH_NS" | awk '/inet / {print $2}' | cut -d/ -f1)
    echo "[probe] client IP = $IP"

    echo "[probe] DNS: dig portal.stayconnect.local @10.10.0.1"
    ip netns exec "$NS" dig +short @10.10.0.1 portal.stayconnect.local || true

    echo
    echo "[probe] HTTP to external — expect DNAT to captive portal (HTTP 200 landing)"
    ip netns exec "$NS" curl -s -o /tmp/probe-unauth.html -w "HTTP %{http_code} (%{url_effective} size=%{size_download})\n" \
        http://example.com/ --max-time 5 || true
    grep -q "Welcome" /tmp/probe-unauth.html && echo "  ✓ captive landing served" || echo "  ✗ unexpected body"

    echo
    echo "[probe] External ping — expect FAIL pre-auth (non-walled-garden target)"
    ip netns exec "$NS" ping -c 2 -W 2 -n 93.184.216.34 >/dev/null 2>&1 \
        && echo "  ✗ unexpected success (pre-auth ping leaked)" \
        || echo "  ✓ blocked"

    echo
    echo "[probe] POST /auth/voucher TESTCODE"
    ip netns exec "$NS" curl -s -o /tmp/probe-auth.html -w "HTTP %{http_code}\n" \
        -X POST -d 'code=TESTCODE' http://portal.stayconnect.local/auth/voucher -L --max-time 5
    echo "--- success body head ---"
    head -c 400 /tmp/probe-auth.html ; echo

    echo
    echo "[probe] nft set now contains:"
    nft -a list set inet stayconnect auth_ipv4 | sed -n '/elements/,/}/p'

    echo
    echo "[probe] HTTP to external — expect real site now"
    ip netns exec "$NS" curl -s -o /tmp/probe-post.html -w "HTTP %{http_code} size=%{size_download}\n" \
        http://example.com/ --max-time 8 || true
    head -c 80 /tmp/probe-post.html ; echo

    echo
    echo "[probe] External ping 1.1.1.1 (walled garden) and 9.9.9.9 (not) — expect OK then OK (authorized)"
    ip netns exec "$NS" ping -c 1 -W 2 -n 1.1.1.1 >/dev/null 2>&1 && echo "  1.1.1.1 ✓" || echo "  1.1.1.1 ✗"
    ip netns exec "$NS" ping -c 1 -W 2 -n 9.9.9.9 >/dev/null 2>&1 && echo "  9.9.9.9 ✓" || echo "  9.9.9.9 ✗"
}

shell_in() {
    need_root
    ip netns exec "$NS" bash
}

down() {
    need_root
    ip netns exec "$NS" pkill -9 dhclient 2>/dev/null || true
    ip link del "$VETH_HOST" 2>/dev/null || true
    ip netns del "$NS" 2>/dev/null || true
    echo "torn down"
}

case "${1:-}" in
    up)    up ;;
    dhcp)  dhcp ;;
    probe) probe ;;
    shell) shell_in ;;
    down)  down ;;
    *) echo "usage: $0 {up|dhcp|probe|shell|down}"; exit 2 ;;
esac
