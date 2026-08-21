#!/usr/bin/env bash
# INSTALL THE VERSIONED CENTRAL ENDPOINT ONTO A CENTRAL HOST.
#
# deploy/config/central-endpoint.env is the fleet's single statement of what appliances dial. This copies it
# to /etc/stayconnect/central-endpoint.env, which the ctrlapi unit reads AFTER ctrlapi.env so the versioned
# value wins over anything hand-edited on the host.
#
# Without this step the endpoint would exist only as a line somebody typed into one server's environment
# file — the exact shape of problem the next deployment rediscovers.
#
# Idempotent: re-running is a no-op when the content already matches.
#
# Usage (as root on Central):
#   install-central-endpoint.sh [path/to/central-endpoint.env]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${DEPLOY:-$(cd "$HERE/.." && pwd)}"
SRC="${1:-$DEPLOY/config/central-endpoint.env}"
DEST="${CENTRAL_ENDPOINT_FILE:-/etc/stayconnect/central-endpoint.env}"

say() { echo "[central-endpoint] $*"; }
die() { echo "[central-endpoint] ABORT: $*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"
[ -f "$SRC" ] || die "no endpoint configuration at $SRC"

# shellcheck source=lib-central-endpoint.sh
. "$HERE/lib-central-endpoint.sh"

# Validate BEFORE installing. A malformed endpoint installed here would be written into every activation
# package this host mints, and each of those goes to a hotel.
( unset CENTRAL_BASE CENTRAL_MTLS_BASE CTRLAPI_APPLIANCE_BASE
  CENTRAL_ENDPOINT_CONFIG="$SRC"
  sc_load_central_endpoint >/dev/null
  sc_validate_central_base "$CENTRAL_BASE"
  [ -z "${CENTRAL_MTLS_BASE:-}" ]      || sc_validate_central_base "$CENTRAL_MTLS_BASE" CENTRAL_MTLS_BASE
  [ -z "${CTRLAPI_APPLIANCE_BASE:-}" ] || sc_validate_central_base "$CTRLAPI_APPLIANCE_BASE" CTRLAPI_APPLIANCE_BASE
) || die "the endpoint configuration in $SRC is not valid — nothing was installed"

if [ -f "$DEST" ] && cmp -s "$SRC" "$DEST"; then
  say "already installed and identical: $DEST"
  exit 0
fi
if [ -f "$DEST" ]; then
  cp -a "$DEST" "$DEST.previous.$(date -u +%Y%m%dT%H%M%SZ)"
  say "kept the previous endpoint configuration alongside for audit"
fi
mkdir -p "$(dirname "$DEST")"
install -o root -g root -m 0644 "$SRC" "$DEST.tmp"
mv -f "$DEST.tmp" "$DEST"
say "installed $DEST"
grep -E '^(CENTRAL_BASE|CTRLAPI_APPLIANCE_BASE)=' "$DEST" | sed 's/^/[central-endpoint]   /'
say ""
say "Restart ctrlapi for it to take effect:  systemctl restart stayconnect-ctrlapi"
