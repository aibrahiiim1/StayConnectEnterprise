#!/usr/bin/env bash
# DOES ENDING A GUEST'S ACCESS STOP THE FLOWS THAT WERE ALREADY OPEN?  (audit B-1)
#
# The guest forward chain accepts `ct state established,related` before it consults the authorization sets
# (internal/netcfg/render_nft.go). Removing an address from a set therefore stops only NEW connections; a
# download that was already running kept going. netd now deletes the address's conntrack entries whenever it
# revokes the address (cmd/netd/phase3_gate.go flushingGate, internal/conntrack), and this script proves, with
# real packets on a real kernel, that this is what stops the transfer.
#
# It builds three DISPOSABLE network namespaces and touches nothing else -- not the appliance's own ruleset, not
# its guest networks, not a single real guest:
#
#   ft-guest  10.91.0.2 ── veth ──> ft-rtr 10.91.0.1 | 10.92.0.1 ── veth ──> ft-wan 10.92.0.2
#
# ft-rtr carries a minimal copy of the guest forward chain IN THE SAME ORDER as the real render (established
# accept FIRST, then the Phase-3 authorization set) and masquerades out of its WAN side. A server in ft-wan
# streams bytes forever; a client in ft-guest counts what arrives.
#
#   CONTROL  authorize, start a transfer, revoke the element only -> the transfer KEEPS FLOWING (the defect)
#   FIX      authorize, start a transfer, revoke the element, run `netd flows-end` inside ft-rtr
#            -> the transfer must STOP within 2 seconds
#
# Usage (as root, on the appliance or any Linux host with ip, nft, python3):
#   scripts/flow-teardown-netns-test.sh                       # uses /opt/stayconnect/bin/netd
#   NETD=/path/to/netd scripts/flow-teardown-netns-test.sh
#
# Exit: 0 PASS, 1 FAIL, 2 the harness could not run.
set -uo pipefail

NETD="${NETD:-/opt/stayconnect/bin/netd}"
RTR=ft-rtr
GUEST=ft-guest
WAN=ft-wan
GUEST_IP=10.91.0.2
GUEST_GW=10.91.0.1
WAN_GW=10.92.0.1
WAN_IP=10.92.0.2
PORT=9091
STOP_WITHIN=2
WORK="$(mktemp -d)"

log() { printf '[flow-teardown] %s\n' "$*" >&2; }
die2() { log "HARNESS: $*"; exit 2; }

cleanup() {
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "${CLI_PID:-}" ] && kill "$CLI_PID" 2>/dev/null
  ip netns del "$GUEST" 2>/dev/null
  ip netns del "$WAN" 2>/dev/null
  ip netns del "$RTR" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

[ "$(id -u)" = 0 ] || die2 "must run as root (it creates network namespaces)"
for b in ip nft python3; do command -v "$b" >/dev/null || die2 "$b is not installed"; done
[ -x "$NETD" ] || die2 "netd binary not found at $NETD (set NETD=...)"
for ns in "$RTR" "$GUEST" "$WAN"; do
  ip netns list | grep -qw "$ns" && die2 "namespace $ns already exists; remove it or rerun after cleanup"
done

# ---- topology --------------------------------------------------------------------------------------------
ip netns add "$RTR" && ip netns add "$GUEST" && ip netns add "$WAN" || die2 "netns add failed"
ip link add ft-g0 type veth peer name ft-rg netns "$RTR" || die2 "veth guest"
ip link set ft-g0 netns "$GUEST"
ip link add ft-w0 type veth peer name ft-rw netns "$RTR" || die2 "veth wan"
ip link set ft-w0 netns "$WAN"
ip -n "$GUEST" addr add "$GUEST_IP/24" dev ft-g0 && ip -n "$GUEST" link set ft-g0 up && ip -n "$GUEST" link set lo up
ip -n "$RTR" addr add "$GUEST_GW/24" dev ft-rg && ip -n "$RTR" link set ft-rg up
ip -n "$RTR" addr add "$WAN_GW/24" dev ft-rw && ip -n "$RTR" link set ft-rw up && ip -n "$RTR" link set lo up
ip -n "$WAN" addr add "$WAN_IP/24" dev ft-w0 && ip -n "$WAN" link set ft-w0 up && ip -n "$WAN" link set lo up
ip -n "$GUEST" route add default via "$GUEST_GW" || die2 "guest route"
ip netns exec "$RTR" sysctl -qw net.ipv4.ip_forward=1 || die2 "ip_forward"
# Strict TCP tracking, as on the appliance (the default): a mid-stream segment after a flush is not "established".
ip netns exec "$RTR" sysctl -qw net.netfilter.nf_conntrack_tcp_loose=0 2>/dev/null || true

# ---- the guest forward chain, in the render's order ----------------------------------------------------------
cat > "$WORK/rtr.nft" <<EOF
table inet ft
delete table inet ft
table inet ft {
	set phase3_auth_ipv4 {
		type ifname . ipv4_addr
		flags timeout
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		oifname "ft-rw" iifname . ip saddr @phase3_auth_ipv4 accept
	}
	chain postrouting_nat {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr 10.91.0.0/24 oifname "ft-rw" masquerade
	}
}
EOF
ip netns exec "$RTR" nft -f "$WORK/rtr.nft" || die2 "could not load the router ruleset"
authorize() { ip netns exec "$RTR" nft add element inet ft phase3_auth_ipv4 "{ \"ft-rg\" . $GUEST_IP timeout 10m }"; }
revoke()    { ip netns exec "$RTR" nft delete element inet ft phase3_auth_ipv4 "{ \"ft-rg\" . $GUEST_IP }"; }

# ---- a server that streams forever, a client that counts ----------------------------------------------------
cat > "$WORK/server.py" <<'EOF'
import socket, threading, time, sys
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", int(sys.argv[1]))); s.listen(8)
def feed(c):
    chunk = b"x" * 16384
    try:
        while True:
            c.sendall(chunk); time.sleep(0.008)   # about 2 MB/s
    except OSError:
        pass
while True:
    c, _ = s.accept(); threading.Thread(target=feed, args=(c,), daemon=True).start()
EOF
cat > "$WORK/client.py" <<'EOF'
import socket, sys, time
out = sys.argv[3]
c = socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=30)
c.settimeout(0.2)
total = 0
last = 0.0
while True:
    try:
        b = c.recv(65536)
        if not b:
            break
        total += len(b)
    except socket.timeout:
        pass
    except OSError:
        break
    now = time.time()
    if now - last > 0.05:
        with open(out + ".tmp", "w") as f: f.write(str(total))
        import os; os.replace(out + ".tmp", out); last = now
EOF
ip netns exec "$WAN" python3 "$WORK/server.py" "$PORT" & SRV_PID=$!
sleep 0.5

bytes() { cat "$WORK/count" 2>/dev/null || echo 0; }

start_transfer() {
  rm -f "$WORK/count"
  ip netns exec "$GUEST" python3 "$WORK/client.py" "$WAN_IP" "$PORT" "$WORK/count" & CLI_PID=$!
  for _ in $(seq 1 50); do [ "$(bytes)" -gt 100000 ] && return 0; sleep 0.1; done
  die2 "the transfer never started (authorization or routing in the harness is wrong)"
}
stop_transfer() { kill "$CLI_PID" 2>/dev/null; wait "$CLI_PID" 2>/dev/null; CLI_PID=""; }

# ---- CONTROL: revoking the element alone does not stop an open flow --------------------------------------
authorize || die2 "authorize"
start_transfer
revoke
b0="$(bytes)"; sleep 3; b1="$(bytes)"
stop_transfer
control_grew=$((b1 - b0))
if [ "$control_grew" -gt 1000000 ]; then
  log "CONTROL: after revoking only the set element, the open transfer kept going (+$control_grew bytes in 3 s) -- the defect this fixes"
else
  log "CONTROL: the transfer stopped without a flush (+$control_grew bytes). This kernel does not reproduce the defect;"
  log "         the FIX result below is still checked, but it proves less."
fi
ip netns exec "$RTR" "$NETD" flows-end --ip "$GUEST_IP" >/dev/null 2>&1 || true   # clean slate

# ---- FIX: revoke, then netd ends the address's flows ---------------------------------------------------------
authorize || die2 "authorize"
start_transfer
revoke
t0="$(date +%s.%N)"
flushed="$(ip netns exec "$RTR" "$NETD" flows-end --ip "$GUEST_IP" 2>"$WORK/flush.err")" || {
  log "FAIL: netd flows-end failed: $(cat "$WORK/flush.err")"; exit 1; }
sleep "$STOP_WITHIN"
b2="$(bytes)"; sleep 3; b3="$(bytes)"
stop_transfer
t1="$(date +%s.%N)"
after=$((b3 - b2))
log "FIX: netd flows-end deleted $flushed conntrack entr(y/ies); bytes received ${STOP_WITHIN}s..$((STOP_WITHIN+3))s after the flush: $after"
if [ "${flushed:-0}" -lt 1 ]; then
  log "FAIL: netd found no conntrack entry for $GUEST_IP while a transfer was running"
  exit 1
fi
if [ "$after" -gt 0 ]; then
  log "FAIL: the transfer was still flowing more than ${STOP_WITHIN}s after its flows were ended"
  exit 1
fi
# and a fresh connection is refused too (no authorization)
if ip netns exec "$GUEST" timeout 3 python3 -c "import socket; socket.create_connection(('$WAN_IP', $PORT), timeout=2)" 2>/dev/null; then
  log "FAIL: a new connection succeeded after revocation"
  exit 1
fi
log "PASS: the open transfer stopped within ${STOP_WITHIN}s of netd ending its flows, and no new connection opens"
exit 0
