#!/usr/bin/env bash
# Checks deploy/sysctl/99-stayconnect.conf (audit Phase 8 L2):
#   - IPv4 forwarding stays on (the appliance routes guests over IPv4);
#   - IPv6 is never forwarded (all and default forwarding = 0, and no line turns it back on);
#   - nothing switches off the host's own IPv6 (no disable_ipv6=1 or accept_ra=0 on all/default), so the
#     WAN/management interface keeps its IPv6 address and router advertisements.
# With an IPv6-capable kernel and SYSCTL_POLICY_KERNEL=1 it also loads the file into a throwaway network namespace
# and reads the values back. Pure file check otherwise; changes nothing on the host.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
F="${1:-$HERE/../sysctl/99-stayconnect.conf}"
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }
val() { # val <key> -> last value set in the file for exactly that key
  sed -n "s/^[[:space:]]*$(printf '%s' "$1" | sed 's/\./\\./g')[[:space:]]*=[[:space:]]*\([^[:space:]#]*\).*/\1/p" "$F" | tail -1
}

[ "$(val net.ipv4.ip_forward)" = 1 ] && ok "IPv4 forwarding on" || bad "net.ipv4.ip_forward is not 1"
[ "$(val net.ipv6.conf.all.forwarding)" = 0 ] && ok "IPv6 forwarding off (all)" || bad "net.ipv6.conf.all.forwarding is not 0"
[ "$(val net.ipv6.conf.default.forwarding)" = 0 ] && ok "IPv6 forwarding off (default)" || bad "net.ipv6.conf.default.forwarding is not 0"
if grep -Eq '^[[:space:]]*net\.ipv6\.conf\.[^.]+\.forwarding[[:space:]]*=[[:space:]]*1' "$F"; then
  bad "an interface turns IPv6 forwarding back on"
else ok "no interface turns IPv6 forwarding back on"; fi
if grep -Eq '^[[:space:]]*net\.ipv6\.conf\.(all|default)\.(disable_ipv6[[:space:]]*=[[:space:]]*1|accept_ra[[:space:]]*=[[:space:]]*0)' "$F"; then
  bad "the file switches off the host's own IPv6 (disable_ipv6 / accept_ra on all or default)"
else ok "the host's own IPv6 (address, router advertisements) is left alone"; fi

if [ "${SYSCTL_POLICY_KERNEL:-0}" = 1 ]; then
  ns="sysctl-policy-$$"
  if ip netns add "$ns" 2>/dev/null && ip netns exec "$ns" test -e /proc/sys/net/ipv6/conf/all/forwarding; then
    ip netns exec "$ns" sysctl -q -p "$F" >/dev/null 2>&1 || true
    ip netns exec "$ns" ip link add wan0 type dummy && ip netns exec "$ns" ip link set wan0 up
    f="$(ip netns exec "$ns" cat /proc/sys/net/ipv6/conf/all/forwarding)"
    ra="$(ip netns exec "$ns" cat /proc/sys/net/ipv6/conf/wan0/accept_ra)"
    dis="$(ip netns exec "$ns" cat /proc/sys/net/ipv6/conf/wan0/disable_ipv6)"
    [ "$f" = 0 ] && [ "$ra" != 0 ] && [ "$dis" = 0 ] && ok "kernel: forwarding=$f, a new interface accepts RAs ($ra) and keeps IPv6" \
      || bad "kernel: forwarding=$f accept_ra=$ra disable_ipv6=$dis"
  else
    echo "SKIP: no IPv6-capable network namespace on this kernel"
  fi
  ip netns del "$ns" 2>/dev/null || true
fi
exit "$fail"
