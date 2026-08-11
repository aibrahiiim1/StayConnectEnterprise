#!/usr/bin/env bash
# THE REAL-KERNEL GATE.
#
# Builds a disposable three-namespace topology, renders the real StayConnect nftables ruleset into the router
# namespace, and runs internal/kerneltest against the REAL nft and tc binaries with REAL packets.
#
#   p3-guest ── veth ──> p3-rtr ── veth ──> p3-wan
#   10.77.0.2            10.77.0.1 / 10.88.0.1        10.88.0.2
#
# Everything the suite touches lives inside those namespaces. The Go clients are pointed at wrapper scripts
# that `ip netns exec` into the router namespace, so no command this suite issues can reach the host's own
# nftables ruleset or its qdiscs — and that claim is verified, not asserted: the host ruleset is fingerprinted
# before and after and the run fails if it changed.
#
# It contacts no appliance, no production database and no PMS. It is evidence about the KERNEL CONTRACT the
# Phase-3 design rests on, produced on a disposable machine. It is NOT live appliance evidence.
#
# Exit codes follow the CI contract: 0 pass, 1 assertion failure, 2 infrastructure (retried once).
set -uo pipefail

RTR=p3-rtr
GUEST=p3-guest
WAN=p3-wan
GUEST_IF=br-guest          # the router-side interface guests arrive on; named like a real guest bridge
WAN_IF=wan0
GUEST_IP=10.77.0.2
GUEST_GW=10.77.0.1
WAN_IP=10.88.0.2
WAN_GW=10.88.0.1

EVID="${EVID:-}"
WORK="$(mktemp -d)"
LIMITATIONS="$WORK/LIMITATIONS.txt"
: > "$LIMITATIONS"

log() { printf '%s\n' "$*" >&2; }

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    log "kernel gate: must run as root (it creates network namespaces)"
    exit 2
  fi
}

have() { command -v "$1" >/dev/null 2>&1; }

cleanup() {
  ip netns del "$GUEST" 2>/dev/null
  ip netns del "$WAN"   2>/dev/null
  ip netns del "$RTR"   2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

need_root
for b in ip nft tc ping; do
  have "$b" || { log "kernel gate: $b is not installed"; exit 2; }
done

# ---- host fingerprint: nothing this suite does may change it -------------------
#
# Counters are stripped before hashing. The runner has Docker running, its nft rules carry packet/byte
# counters, and those tick the whole time this suite is running — so a raw digest of `nft list ruleset` reports
# "the host changed" on every run regardless of what this suite did, which is a check that cannot fail
# usefully. What must be true is that the RULES are identical and that the host never acquires a stayconnect
# table of its own, and both of those are asserted.
host_ruleset() {
  nft list ruleset 2>/dev/null | sed -E 's/counter packets [0-9]+ bytes [0-9]+//g' | sha256sum | cut -d' ' -f1
}
host_has_stayconnect() { nft list table inet stayconnect >/dev/null 2>&1 && echo yes || echo no; }
HOST_BEFORE="$(host_ruleset)"
HOST_TABLE_BEFORE="$(host_has_stayconnect)"

# ---- topology ------------------------------------------------------------------
build() {
  ip netns add "$RTR"   || return 1
  ip netns add "$GUEST" || return 1
  ip netns add "$WAN"   || return 1

  ip link add "$GUEST_IF" netns "$RTR" type veth peer name eth0 netns "$GUEST" || return 1
  ip link add "$WAN_IF"   netns "$RTR" type veth peer name eth0 netns "$WAN"   || return 1

  ip -n "$RTR" addr add "$GUEST_GW/24" dev "$GUEST_IF" || return 1
  ip -n "$RTR" addr add "$WAN_GW/24"   dev "$WAN_IF"   || return 1
  ip -n "$RTR" link set "$GUEST_IF" up
  ip -n "$RTR" link set "$WAN_IF" up
  ip -n "$RTR" link set lo up
  ip netns exec "$RTR" sysctl -qw net.ipv4.ip_forward=1 || return 1

  ip -n "$GUEST" addr add "$GUEST_IP/24" dev eth0 || return 1
  ip -n "$GUEST" link set eth0 up
  ip -n "$GUEST" link set lo up
  ip -n "$GUEST" route add default via "$GUEST_GW" || return 1

  ip -n "$WAN" addr add "$WAN_IP/24" dev eth0 || return 1
  ip -n "$WAN" link set eth0 up
  ip -n "$WAN" link set lo up
  ip -n "$WAN" route add 10.77.0.0/24 via "$WAN_GW" || return 1
}

if ! build; then
  log "kernel gate: could not build the disposable topology"
  exit 2
fi

# ---- the REAL ruleset, as the appliance renders it ------------------------------
# This is the same shape render_nft.go emits: forward policy drop, the two authorization sets, one accept rule
# per set, and the captive DNAT rules excluding both. The suite's job is to prove the kernel behaves as the
# design assumes when this ruleset is in force.
cat > "$WORK/ruleset.nft" <<EOF
# The appliance applies its generated ruleset as an ATOMIC REPLACE of the whole table: declare it (so the
# delete cannot fail on a fresh unit), delete it, then define it. That is exactly the operation the
# surgical foundation exists to avoid doing casually — it recreates the authorization sets EMPTY — and it
# is reproduced here so the reboot case is modelled the way the unit actually restarts.
table inet stayconnect
delete table inet stayconnect
table inet stayconnect {
	set auth_ipv4 {
		type ifname . ipv4_addr
		flags timeout
		comment "Authenticated guests: (ingress bridge, IP)"
	}
	set phase3_auth_ipv4 {
		type ifname . ipv4_addr
		flags timeout
		comment "Phase-3 authorized guests (netd-owned): (ingress bridge, IP)"
	}
	set walled_garden_ip {
		type ipv4_addr
		flags interval
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		oifname "$WAN_IF" iifname . ip saddr @auth_ipv4 accept comment "authenticated guests"
		oifname "$WAN_IF" iifname . ip saddr @phase3_auth_ipv4 accept comment "phase-3 authorized guests"
	}
	chain prerouting_nat {
		type nat hook prerouting priority dstnat; policy accept;
		iifname "$GUEST_IF" iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport 80 dnat ip to $GUEST_GW:8080
		iifname "$GUEST_IF" iifname . ip saddr != @auth_ipv4 iifname . ip saddr != @phase3_auth_ipv4 ip daddr != @walled_garden_ip tcp dport 443 dnat ip to $GUEST_GW:8443
	}
}
EOF
if ! ip netns exec "$RTR" nft -f "$WORK/ruleset.nft"; then
  log "kernel gate: this kernel/nftables does not accept the StayConnect ruleset"
  exit 2
fi

# ---- namespace-scoped wrappers --------------------------------------------------
# Everything the Go clients execute goes through these, so a command that tried to touch the host ruleset
# would have to bypass the client's configured binary path to do it.
for b in nft tc ip; do
  cat > "$WORK/$b" <<EOF
#!/usr/bin/env bash
exec ip netns exec $RTR $b "\$@"
EOF
  chmod +x "$WORK/$b"
done

# ---- ifb probe: classify the limitation honestly rather than silently skipping ---
KG_IFB=0
if ip netns exec "$RTR" ip link add name ifbprobe type ifb >/dev/null 2>&1; then
  ip netns exec "$RTR" ip link del ifbprobe >/dev/null 2>&1
  KG_IFB=1
else
  {
    echo "LIMITATION: the ifb kernel module is unavailable in this runner's kernel."
    echo "  AFFECTED: the staged tc surface (PrepareSession/ActivateSession/ReRateSession) creates the UPLOAD"
    echo "  class on an IFB device, so the tc half of this suite cannot run here. The nft half — which is what"
    echo "  decides internet access — runs in full, including the proof that removing a tc classification does"
    echo "  NOT deny access."
    echo "  NOT CLAIMED: real-kernel evidence for tc prepare/activate/re-rate on this run."
  } >> "$LIMITATIONS"
  log "kernel gate: ifb unavailable; tc-dependent cases will be skipped and the limitation recorded"
fi

# ---- topology precondition ------------------------------------------------------
# Before any assertion is trusted, prove the harness itself is sound: an UNAUTHORIZED guest must not reach the
# WAN, and an AUTHORIZED one must. A suite whose topology silently forwards everything would report the most
# alarming failures possible for reasons that have nothing to do with the code under test — so a broken
# harness exits 2 (infrastructure) rather than 1 (assertion).
precheck_fail() {
  log "kernel gate: TOPOLOGY PRECONDITION FAILED — $1"
  log "--- router ruleset ---"; ip netns exec "$RTR" nft list ruleset >&2
  log "--- router links ---";   ip -n "$RTR" addr >&2
  exit 2
}
if ip netns exec "$GUEST" ping -c 1 -W 1 "$WAN_IP" >/dev/null 2>&1; then
  precheck_fail "an unauthorized guest already reaches the WAN; the forward chain is not gating"
fi
ip netns exec "$RTR" nft add element inet stayconnect auth_ipv4 "{ \"$GUEST_IF\" . $GUEST_IP }"   || precheck_fail "the legacy authorization set would not accept a concatenated element"
if ! ip netns exec "$GUEST" ping -c 2 -W 1 -i 0.2 "$WAN_IP" >/dev/null 2>&1; then
  precheck_fail "an AUTHORIZED guest cannot reach the WAN; routing or forwarding is broken"
fi
ip netns exec "$RTR" nft delete element inet stayconnect auth_ipv4 "{ \"$GUEST_IF\" . $GUEST_IP }"   || precheck_fail "the legacy element could not be removed"
if ip netns exec "$GUEST" ping -c 1 -W 1 "$WAN_IP" >/dev/null 2>&1; then
  precheck_fail "the guest still reaches the WAN after their authorization was removed"
fi
log "kernel gate: topology precondition OK (deny -> allow -> deny, with real packets)"

# ---- build the suite OUTSIDE the namespace, run it against it --------------------
BIN="$WORK/kernelgate.test"
if ! (cd data-plane && go test -tags kernelgate -c -o "$BIN" ./internal/kerneltest/); then
  log "kernel gate: could not build the suite"
  exit 2
fi

set -o pipefail
KG_NFT="$WORK/nft" KG_TC="$WORK/tc" KG_IP="$WORK/ip" \
KG_GUEST_NS="$GUEST" KG_GUEST_IP="$GUEST_IP" KG_WAN_IP="$WAN_IP" KG_GUEST_IF="$GUEST_IF" KG_IFB="$KG_IFB" \
KG_WAN_IF="$WAN_IF" \
KG_RULESET="$WORK/ruleset.nft" \
  "$BIN" -test.v -test.timeout 10m 2>&1 | tee "$WORK/kernel.log"
RC=${PIPESTATUS[0]}

PASSED=$(grep -c '^--- PASS' "$WORK/kernel.log" || true)
FAILED=$(grep -c '^--- FAIL' "$WORK/kernel.log" || true)
SKIPPED=$(grep -c '^--- SKIP' "$WORK/kernel.log" || true)

# ---- the host must be exactly as it was ------------------------------------------
HOST_AFTER="$(host_ruleset)"
HOST_TABLE_AFTER="$(host_has_stayconnect)"
HOST_CLEAN=true
if [ "$HOST_TABLE_BEFORE" != "$HOST_TABLE_AFTER" ] || [ "$HOST_TABLE_AFTER" = "yes" ]; then
  log "kernel gate: THE HOST ACQUIRED A stayconnect TABLE. The suite escaped its namespaces."
  HOST_CLEAN=false
  RC=1
fi
if [ "$HOST_BEFORE" != "$HOST_AFTER" ]; then
  log "kernel gate: THE HOST RULESET CHANGED (rules, counters excluded). The suite mutated state outside its disposable namespaces."
  HOST_CLEAN=false
  RC=1
fi

if [ -n "$EVID" ]; then
  mkdir -p "$EVID/counts"
  {
    echo "suite=kernel-netns"
    echo "passed=$PASSED"
    echo "failed=$FAILED"
    echo "skipped=$SKIPPED"
    echo "ifb_available=$KG_IFB"
    echo "host_ruleset_unchanged=$HOST_CLEAN"
    echo "host_acquired_stayconnect_table=$HOST_TABLE_AFTER"
    echo "kernel=$(uname -r)"
    echo "nft=$(nft --version 2>/dev/null | head -1)"
    echo "tc=$(tc -V 2>/dev/null | head -1)"
  } > "$EVID/counts/kernel-netns.txt"
  if [ -s "$LIMITATIONS" ]; then
    cp "$LIMITATIONS" "$EVID/kernel-limitations.txt"
  fi
fi

log "kernel gate: passed=$PASSED failed=$FAILED skipped=$SKIPPED ifb=$KG_IFB rc=$RC"
if [ "$RC" -ne 0 ]; then exit 1; fi
if [ "$PASSED" -eq 0 ]; then
  log "kernel gate: no test ran; that is an infrastructure failure, not a pass"
  exit 2
fi
exit 0
