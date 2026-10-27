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
# the Hotel module: room sign-in (scd) and the Hotel section of the Admin Console (edged)
ensure_kv scd.env     STAYCONNECT_PHASE3_MASTER   true
ensure_kv scd.env     STAYCONNECT_PHASE3_PMS_AUTH true
ensure_kv edged.env   STAYCONNECT_PHASE3_MASTER   true
ensure_kv edged.env   STAYCONNECT_PHASE3_ADMIN    true

bash "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$ETC" >/dev/null \
  || die "the environment is not coherent: $(bash "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$ETC" 2>&1 | head -3)"
bash "$DEPLOY/scripts/check-phase6-guest-dependency.sh" "$ETC" >/dev/null 2>&1 \
  || die "the environment is not coherent: $(bash "$DEPLOY/scripts/check-phase6-guest-dependency.sh" "$ETC" 2>&1 | head -3)"
echo "[product-profile] OK (client access, enforcement plane, Hotel module installed; nothing financial)"
