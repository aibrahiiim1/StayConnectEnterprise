#!/usr/bin/env bash
# CENTRAL HOST FIREWALL — the ports appliances actually need, in source rather than in one admin's memory.
#
# The live Central allowed 22 and 443 only. ctrlapi's mutual-TLS appliance listener was bound on 9443 and
# blocked, so `ss` showed it listening, Central's own loopback tests passed, and every appliance in the
# world got a connection timeout. Nothing on Central logs a connection that never arrives, so the port had
# been unreachable since it was introduced and nothing said so.
#
# WHY 9443 IS SAFE TO EXPOSE, AND WHY IT IS NOT "JUST ANOTHER OPEN PORT"
#
# It is not an unauthenticated surface. The listener is configured RequireAndVerifyClientCert against the
# appliance CA: a connection without a certificate signed by that CA is rejected during the handshake,
# before any request is read. It has to be reachable from wherever hotels are, because the entire design is
# appliance-initiated outbound — Central never dials in — so restricting it to today's management subnet
# would quietly break the first appliance installed anywhere else.
#
# Ports deliberately NOT opened here: Postgres, Redis, ctrlapi's plain HTTP (8080) and cloud-admin (3000).
# Those are loopback or private-network only and Caddy fronts what needs to be public.
#
# Idempotent: ufw rules are declarative, so re-running changes nothing.
#
# Usage (as root on Central):
#   central-firewall.sh            apply the rules
#   central-firewall.sh --show     what is allowed now
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${DEPLOY:-$(cd "$HERE/.." && pwd)}"

say() { echo "[central-fw] $*"; }
die() { echo "[central-fw] ABORT: $*" >&2; exit 1; }

command -v ufw >/dev/null || die "ufw not found (this script targets the Ubuntu Central host)"

if [ "${1:-}" = "--show" ]; then
  ufw status verbose | head -20
  exit 0
fi

[ "$(id -u)" = 0 ] || die "run as root"

# The mTLS port comes from the same versioned endpoint configuration everything else uses, so it cannot
# drift from what appliances are told to dial.
# shellcheck source=lib-central-endpoint.sh
. "$HERE/lib-central-endpoint.sh"
sc_load_central_endpoint "$DEPLOY" >/dev/null
MTLS="${CENTRAL_MTLS_BASE:-${CENTRAL_BASE}:9443}"
mtls_hostport="${MTLS#https://}"; mtls_hostport="${mtls_hostport%%/*}"
case "$mtls_hostport" in *:*) MTLS_PORT="${mtls_hostport##*:}" ;; *) MTLS_PORT=443 ;; esac
[ "$MTLS_PORT" -gt 0 ] 2>/dev/null || die "could not derive the mTLS port from $MTLS"

say "ensuring the appliance-facing ports are open (mTLS port $MTLS_PORT, from central-endpoint.env)"

# SSH first and explicitly: enabling ufw without it locks the host out of its own management path.
ufw allow 22/tcp     >/dev/null && say "  22/tcp    ssh"
ufw allow 443/tcp    >/dev/null && say "  443/tcp   admin UI + /cloud/v1 + appliance protocol (Caddy)"
ufw allow "$MTLS_PORT/tcp" >/dev/null && say "  $MTLS_PORT/tcp  appliance mutual-TLS (client certificate required)"

say ""
say "current policy:"
ufw status | sed 's/^/  /' | head -12
