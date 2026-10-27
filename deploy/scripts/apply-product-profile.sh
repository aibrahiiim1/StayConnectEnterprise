#!/usr/bin/env bash
# THE SUPPORTED PRODUCT PROFILE -- written by a fresh install (provision-fresh-appliance.sh) and by every upgrade
# (appliance-install.sh --upgrade), so an appliance installed by an older release gets what the current one ships.
#
# Usage (root): apply-product-profile.sh <deploy-dir> [<etc-dir>]        Exit 0 coherent · 1 refused
#
# CLIENT ACCESS WITH ENFORCEMENT. Without these keys a provisioned appliance signed Clients in and let none of them
# online: every IAM-v2 session opens in PENDING_ENFORCEMENT and only the enforcement plane promotes it, and vouchers,
# Free packages and client accounts are redeemed through the commerce routes (STAYCONNECT_PHASE2_*).
#
# THE HOTEL MODULE IS INSTALLED; WHETHER IT IS USED IS THE LICENCE'S AND THE SITE'S DECISION. A module is in use
# only when it is licensed (Central), installed (here) and switched on (the site). Leaving the Hotel surface out of
# the installation meant a licence that included it still showed "not available" and no Hotel section. The surface is
# inert until an operator commissions a PMS: room sign-in has no roster to check, and pmsd -- the only process that
# talks to a PMS -- stays dark (its env file is untouched here) until the commissioning step of the installation
# manual (C.10) configures it with its keys.
#
# NOT HERE, ON PURPOSE: anything financial (STAYCONNECT_PHASE4_*: card payment calls a payment provider, room charge
# posts to the PMS) and STAYCONNECT_PAYMENT_LIVE_ALLOWED. Those need the vendor's authorisation (manual C.12).
#
# ensure_kv adds a key only when it is ABSENT and never changes an existing value, so an operator's choice on an
# appliance already in service (a flag set to false) survives every re-run and upgrade.
set -euo pipefail
DEPLOY="${1:?usage: apply-product-profile.sh <deploy-dir> [<etc-dir>]}"
ETC="${2:-/etc/stayconnect}"
die() { echo "[product-profile] REFUSED: $*" >&2; exit 1; }

ensure_kv() { # <file> <key> <value>
  [ -f "$ETC/$1" ] || die "$ETC/$1 does not exist"
  grep -qE "^$2=" "$ETC/$1" 2>/dev/null && return 0
  printf '%s=%s\n' "$2" "$3" >> "$ETC/$1"
  echo "[product-profile] $1: $2 added"
}
# The plane's producer is whichever account acctd runs as: the uid the kernel reports to netd over SO_PEERCRED.
ACCTD_USER="$(sed -n 's/^User=\(.*\)$/\1/p' "$DEPLOY/systemd/stayconnect-acctd.service" | tail -1)"
PRODUCER_UID="$(id -u "${ACCTD_USER:-root}")" || die "cannot resolve a uid for acctd's user '${ACCTD_USER:-root}'"

# client access with enforcement
ensure_kv scd.env     STAYCONNECT_PHASE2_MASTER true
ensure_kv scd.env     STAYCONNECT_PHASE2_PORTAL true
ensure_kv portald.env STAYCONNECT_PHASE2_MASTER true
ensure_kv portald.env STAYCONNECT_PHASE2_PORTAL true
ensure_kv edged.env   STAYCONNECT_PHASE2_MASTER true
ensure_kv edged.env   STAYCONNECT_PHASE2_ADMIN  true
ensure_kv netd.env    STAYCONNECT_PHASE3_MASTER true
ensure_kv netd.env    NETD_PHASE3_PRODUCER_UID  "$PRODUCER_UID"
ensure_kv acctd.env   STAYCONNECT_PHASE3_MASTER true
# the Hotel module: room sign-in (scd) and the Hotel section of the Admin Console (edged). A child flag is added only
# where that file's own master is on: an operator who set STAYCONNECT_PHASE3_MASTER=false keeps a coherent file
# (a child flag under a disabled master is a start-up refusal in iamv2.PMSConfig.Validate).
val_in() { grep -E "^$2=" "$ETC/$1" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' "'"'"; }
truthy() { case "$1" in true|TRUE|True|1) return 0;; *) return 1;; esac; }
ensure_kv scd.env     STAYCONNECT_PHASE3_MASTER   true
ensure_kv edged.env   STAYCONNECT_PHASE3_MASTER   true
if truthy "$(val_in scd.env STAYCONNECT_PHASE3_MASTER)"; then
  ensure_kv scd.env   STAYCONNECT_PHASE3_PMS_AUTH true
else
  echo "[product-profile] scd.env: STAYCONNECT_PHASE3_MASTER is off here by choice; room sign-in not added"
fi
if truthy "$(val_in edged.env STAYCONNECT_PHASE3_MASTER)"; then
  ensure_kv edged.env STAYCONNECT_PHASE3_ADMIN    true
else
  echo "[product-profile] edged.env: STAYCONNECT_PHASE3_MASTER is off here by choice; the Hotel section not added"
fi
# Each daemon validates its OWN file: a Phase-3 child flag on while that file's master is off stops it at start.
for f in scd.env edged.env; do
  truthy "$(val_in "$f" STAYCONNECT_PHASE3_MASTER)" && continue
  for k in STAYCONNECT_PHASE3_PMS_AUTH STAYCONNECT_PHASE3_ADMIN STAYCONNECT_PHASE3_PMS_CONNECTOR STAYCONNECT_PHASE3_PMS_INGEST STAYCONNECT_PHASE3_CHECKOUT_GRACE; do
    truthy "$(val_in "$f" "$k")" && die "$ETC/$f sets $k while its STAYCONNECT_PHASE3_MASTER is off: that daemon would refuse to start"
  done
done

bash "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$ETC" >/dev/null \
  || die "the environment is not coherent: $(bash "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$ETC" 2>&1 | head -3)"
bash "$DEPLOY/scripts/check-phase6-guest-dependency.sh" "$ETC" >/dev/null 2>&1 \
  || die "the environment is not coherent: $(bash "$DEPLOY/scripts/check-phase6-guest-dependency.sh" "$ETC" 2>&1 | head -3)"
echo "[product-profile] OK (client access, enforcement plane, Hotel module installed; nothing financial)"
