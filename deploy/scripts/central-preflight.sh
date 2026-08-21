#!/usr/bin/env bash
# IS THIS CENTRAL HOST ACTUALLY ABLE TO ACTIVATE AN APPLIANCE? Read-only; changes nothing.
#
# Every check here corresponds to a way Central can be running, healthy, serving the admin UI, and quietly
# unable to do the one thing a new hotel needs. None of them announce themselves: the vendor key is
# optional at boot, the appliance endpoint is optional at boot, and a missing table only surfaces when an
# operator finally clicks the button. That is a fine set of properties for a service and a terrible one for
# a deployment, so they are checked here, before anyone drives to a site.
#
# Run it after installing or upgrading a Central host, and after moving one.
#
# Usage:
#   central-preflight.sh                                   uses /etc/stayconnect/ctrlapi.env
#   CTRLAPI_ENV_FILE=/path/to/env central-preflight.sh
#   central-preflight.sh --pg-exec "docker exec -i central-pg" --db stayconnect
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${DEPLOY:-$(cd "$HERE/.." && pwd)}"
ENV_FILE="${CTRLAPI_ENV_FILE:-/etc/stayconnect/ctrlapi.env}"
PG_ARGS=()

say() { echo "[preflight] $*"; }
die() { echo "[preflight] ABORT: $*" >&2; exit 1; }

fail=0
ok()   { printf '  \033[32mOK\033[0m    %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; fail=1; }
warn() { printf '  \033[33mWARN\033[0m  %s\n' "$*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --pg-exec) PG_ARGS+=(--pg-exec "$2"); shift 2 ;;
    --db)      PG_ARGS+=(--db "$2"); shift 2 ;;
    --dsn)     PG_ARGS+=(--dsn "$2"); shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# Read the service environment as DATA. Sourcing it would run whatever is in it and would also import
# secrets into this shell for no reason.
#
# PRECEDENCE MIRRORS THE SYSTEMD UNIT: ctrlapi.env first, then central-endpoint.env, which is listed last in
# the unit and therefore wins. A preflight that read them in the other order would cheerfully approve a host
# that is actually running with a different endpoint.
ENDPOINT_FILE="${CENTRAL_ENDPOINT_FILE:-/etc/stayconnect/central-endpoint.env}"
envget() {
  local v="" f
  for f in "$ENV_FILE" "$ENDPOINT_FILE"; do
    [ -f "$f" ] || continue
    local found
    found="$(grep -E "^[[:space:]]*$1=" "$f" | tail -1 | cut -d= -f2- | sed 's/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//')"
    if [ -n "$found" ]; then v="$found"; fi
  done
  [ -n "$v" ] || return 1
  printf '%s' "$v"
}

echo
say "Central host preflight"
echo

# ---------------------------------------------------------------- 1. the appliance-facing endpoint
echo "Appliance endpoint"
if [ -f "$ENDPOINT_FILE" ]; then
  ok "versioned endpoint configuration installed at $ENDPOINT_FILE"
else
  warn "$ENDPOINT_FILE is not installed — this host's endpoint exists only as a hand-edited line in
        $ENV_FILE, which the next deployment will not inherit.
        Install it: deploy/scripts/install-central-endpoint.sh"
fi
CONFIGURED=""
if CONFIGURED="$(envget CTRLAPI_APPLIANCE_BASE)"; then
  ok "CTRLAPI_APPLIANCE_BASE is set: $CONFIGURED"
else
  bad "CTRLAPI_APPLIANCE_BASE is not set in $ENV_FILE or $ENDPOINT_FILE.
        Central will start normally and OFFLINE FIRST ACTIVATION WILL BE DISABLED — no packages can be
        minted, because Central refuses to name an endpoint it was not given. Online activation still works."
fi

# The versioned config is the source of truth for what appliances are told to dial. A Central whose service
# env disagrees with it will hand out an endpoint the rest of the deployment does not expect.
CFG="$DEPLOY/config/central-endpoint.env"
if [ -f "$CFG" ]; then
  want="$(grep -E '^CTRLAPI_APPLIANCE_BASE=' "$CFG" | tail -1 | cut -d= -f2-)"
  if [ -n "$want" ] && [ -n "$CONFIGURED" ] && [ "$want" != "$CONFIGURED" ]; then
    bad "this host says $CONFIGURED but deploy/config/central-endpoint.env says $want.
        Appliances activated here would be pointed somewhere the rest of the fleet is not."
  elif [ -n "$want" ] && [ -n "$CONFIGURED" ]; then
    ok "matches deploy/config/central-endpoint.env"
  fi
else
  warn "no $CFG in this deploy tree — cannot cross-check the endpoint"
fi

if [ -n "$CONFIGURED" ]; then
  # shellcheck source=lib-central-endpoint.sh
  . "$HERE/lib-central-endpoint.sh"
  # In a SUBSHELL: sc_validate_central_base calls die(), which exits. Here a bad endpoint is one finding
  # among several, not a reason to stop reporting the rest.
  if ( sc_validate_central_base "$CONFIGURED" CTRLAPI_APPLIANCE_BASE ) 2>/tmp/.pf.$$; then
    ok "endpoint is an https FQDN with no path"
  else
    bad "$(cat /tmp/.pf.$$)"
  fi
  rm -f /tmp/.pf.$$
fi
echo

# ---------------------------------------------------------------- 2. the vendor signing identity
echo "Vendor signing identity"
VKEY="$(envget CTRLAPI_VENDOR_KEY || echo /etc/stayconnect/vendor-license.key)"
if [ -s "$VKEY" ]; then
  size="$(wc -c < "$VKEY")"
  if [ "$size" = "64" ]; then
    fpr="$(CTRLAPI_VENDOR_KEY="$VKEY" bash "$HERE/vendor-signing-key.sh" show 2>/dev/null | sed -n 's/^.*fingerprint: //p')"
    ok "vendor signing key present (fingerprint ${fpr:-unknown})"
    say "        appliances must have THIS fingerprint pinned; confirm it out of band"
  else
    bad "$VKEY is $size bytes, not a 64-byte ed25519 private key"
  fi
  perms="$(stat -c '%a' "$VKEY" 2>/dev/null || echo '?')"
  case "$perms" in
    600|400) ok "key file permissions $perms" ;;
    *) bad "key file permissions are $perms — the vendor signing key must be 0600" ;;
  esac
else
  bad "no vendor signing key at $VKEY.
        Licence issuing and offline activation are both disabled without it.
        FIRST Central host ever:  deploy/scripts/vendor-signing-key.sh init
        A REPLACEMENT host:       deploy/scripts/vendor-signing-key.sh restore <escrow-file>
        Do NOT run init on a replacement — it would mint a new identity and every appliance already in the
        field is pinned to the old one."
fi

AKEY="$(envget CTRLAPI_ASSIGN_KEY || echo /etc/stayconnect/assignment-signing.key)"
if [ -s "$AKEY" ]; then
  if cmp -s "$AKEY" "$VKEY" 2>/dev/null; then
    bad "the assignment key and the vendor key are the SAME FILE. ctrlapi refuses to sign assignments in
        this state, so activation cannot complete."
  else
    ok "assignment signing key present and distinct from the vendor key"
  fi
else
  bad "no assignment signing key at $AKEY — signed assignments are disabled, so no appliance can be
        activated at all. Create one: ctrlapi gen-assignment-key --out $AKEY"
fi
echo

# ---------------------------------------------------------------- 3. schema
echo "Database schema"
if [ "${#PG_ARGS[@]}" -gt 0 ] || [ -n "${CENTRAL_DB_URL:-}" ] || envget CTRLAPI_DB_URL >/dev/null; then
  if [ "${#PG_ARGS[@]}" = 0 ] && [ -z "${CENTRAL_DB_URL:-}" ]; then
    CENTRAL_DB_URL="$(envget CTRLAPI_DB_URL)"; export CENTRAL_DB_URL
  fi
  if out="$(bash "$HERE/central-migrate.sh" "${PG_ARGS[@]}" verify 2>&1)"; then
    ok "schema fully migrated (includes 0044_offline_activation_requests)"
  else
    bad "$(printf '%s' "$out" | tail -5)
        Apply it with: deploy/scripts/central-migrate.sh up
        A Central missing 0044 accepts an activation request and then fails when it tries to record it."
  fi
else
  warn "no database connection available — schema not checked. Pass --dsn or --pg-exec/--db."
fi
echo

# ---------------------------------------------------------------- verdict
if [ "$fail" = 0 ]; then
  say "READY: this Central host can activate appliances, online and offline."
  exit 0
fi
say "NOT READY — fix the FAIL lines above. Each one is something that fails silently at a hotel."
exit 1
