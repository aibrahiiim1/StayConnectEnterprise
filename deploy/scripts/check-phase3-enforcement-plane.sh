#!/usr/bin/env bash
# Fail closed BEFORE an appliance can grant access it cannot deliver.
#
# A Phase-3 guest surface (Room authentication, Checkout Grace) mints Sessions in PENDING_ENFORCEMENT. Only the
# ENFORCEMENT PLANE makes them real: acctd derives the shaping plan, netd applies it to the kernel and promotes
# the Session. Both read their own environment, so a surface can be enabled in one service's env file while the
# plane is absent from the other two -- and nothing anywhere complains.
#
# That is not hypothetical. On the PRE-LIVE appliance scd.env carried STAYCONNECT_PHASE3_PMS_AUTH=1 while
# netd.env and acctd.env carried no Phase-3 flags at all. Two real guests authenticated, were granted
# entitlements and sessions, were told the connection had failed, and were never enforced: the nft set stayed
# empty, no tc class was ever created, and both Sessions sat in PENDING_ENFORCEMENT indefinitely.
#
# This refuses that combination at config time. It reads only files; it changes nothing.
set -uo pipefail
DIR="${1:-/etc/stayconnect}"
bad=0
val() { grep -hoE "^$1=.*" "$DIR"/*.env 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' '; }
on()  { case "$(val "$1")" in true|TRUE|1|yes|on) return 0;; *) return 1;; esac; }
in_file() { grep -qE "^$2=" "$DIR/$1" 2>/dev/null; }

surface=""
on STAYCONNECT_PHASE3_PMS_AUTH      && surface="STAYCONNECT_PHASE3_PMS_AUTH"
on STAYCONNECT_PHASE3_CHECKOUT_GRACE && surface="${surface:+$surface, }STAYCONNECT_PHASE3_CHECKOUT_GRACE"
if [ -z "$surface" ]; then
  echo "phase3 enforcement-plane check: OK ($DIR — no session-minting surface is enabled)"
  exit 0
fi

# The plane follows the MASTER flag (iamv2.PMSConfig.EnforcementOn), and each daemon reads its OWN env file, so
# the flag has to be in each of them rather than merely somewhere in the directory.
for svc in netd acctd; do
  if ! in_file "$svc.env" STAYCONNECT_PHASE3_MASTER; then
    echo "REFUSED: $surface is enabled but $DIR/$svc.env carries no STAYCONNECT_PHASE3_MASTER." >&2
    echo "  Guests would be granted entitlements and sessions that nothing ever enforces." >&2
    echo "  Fix: deploy/scripts/enable-phase3-enforcement-plane.sh" >&2
    bad=1
  fi
done

# ...and netd refuses to run the plane at all without a producer it can authenticate, which is the correct
# fail-closed behaviour and a crash loop if the config is half-applied.
if ! in_file netd.env NETD_PHASE3_PRODUCER_UID; then
  echo "REFUSED: $surface is enabled but $DIR/netd.env carries no NETD_PHASE3_PRODUCER_UID." >&2
  echo "  netd exits at startup rather than accept plans from an unauthenticated local process." >&2
  echo "  Fix: deploy/scripts/enable-phase3-enforcement-plane.sh" >&2
  bad=1
fi

[ "$bad" = "0" ] && echo "phase3 enforcement-plane check: OK ($DIR — $surface, plane configured in netd and acctd)"
exit "$bad"
