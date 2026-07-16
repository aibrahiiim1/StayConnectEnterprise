#!/usr/bin/env bash
# iam_v2 SCRATCH harness library — disposable scratch/test PostgreSQL ONLY.
# ALLOWLIST safety guard: refuses EVERY target except an explicitly disposable local scratch target.
set -euo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"

SCRATCH_CONTAINER="${SCRATCH_CONTAINER:-iamv2-scratch}"
SCRATCH_DB="${SCRATCH_DB:-iam_scratch}"
SCRATCH_HOST_ALLOW="127.0.0.1"          # ONLY loopback is allowed
SCRATCH_PORT_ALLOW="${SCRATCH_PORT_ALLOW:-55432}"
SCRATCH_DB_PREFIX="iam_scratch"         # strict scratch prefix
SCRATCH_MARKER_VALUE="DISPOSABLE_SCRATCH_ONLY"
SCRATCH_ACK_REQUIRED="I_UNDERSTAND_DISPOSABLE"

_die(){ echo "SAFETY ABORT: $1" >&2; exit 90; }

# ALLOWLIST guard — ALL conditions must hold; no fallback.
require_scratch(){
  # (1) explicit disposable acknowledgment flag
  [ "${SCRATCH_ACK:-}" = "$SCRATCH_ACK_REQUIRED" ] || _die "missing/invalid SCRATCH_ACK ack flag"
  # (2) container exists and is published ONLY on the allowed loopback host + expected port
  local ports; ports="$(docker inspect -f '{{json .NetworkSettings.Ports}}' "$SCRATCH_CONTAINER" 2>/dev/null || echo '')"
  [ -n "$ports" ] || _die "scratch container '$SCRATCH_CONTAINER' not found"
  echo "$ports" | grep -qE "\"HostIp\":\"$SCRATCH_HOST_ALLOW\"" || _die "container not bound to allowed host $SCRATCH_HOST_ALLOW"
  echo "$ports" | grep -qE "\"HostPort\":\"$SCRATCH_PORT_ALLOW\""  || _die "container not on allowed port $SCRATCH_PORT_ALLOW"
  echo "$ports" | grep -qE "\"HostIp\":\"(0\.0\.0\.0|::)\"" && _die "container published on a non-loopback address"
  # (3) db name matches strict scratch prefix and is NOT a live db
  case "$SCRATCH_DB" in
    "$SCRATCH_DB_PREFIX"*) : ;;
    *) _die "db name '$SCRATCH_DB' does not match required scratch prefix '$SCRATCH_DB_PREFIX'";;
  esac
  case "$SCRATCH_DB" in stayconnect_site|stayconnect|stayconnect_site_b) _die "db name is a LIVE database";; esac
  # (4) current_database() must equal the scratch db and never a live db
  local cur; cur="$(docker exec "$SCRATCH_CONTAINER" psql -U postgres -tAc 'select current_database()' "$SCRATCH_DB" 2>/dev/null || true)"
  [ "$cur" = "$SCRATCH_DB" ] || _die "current_database()='$cur' != scratch db '$SCRATCH_DB'"
  case "$cur" in stayconnect_site|stayconnect|stayconnect_site_b) _die "current_database() is a LIVE database";; esac
  # (5) disposable marker table/value must be present (proves this DB was created as scratch)
  local mk; mk="$(docker exec "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -tAc "SELECT marker FROM public._scratch_marker LIMIT 1" 2>/dev/null || true)"
  [ "$mk" = "$SCRATCH_MARKER_VALUE" ] || _die "disposable marker missing/false (got '$mk')"
}

# create the disposable marker (bootstrap): allowed only after the non-marker allowlist checks pass
scratch_init_marker(){
  [ "${SCRATCH_ACK:-}" = "$SCRATCH_ACK_REQUIRED" ] || _die "missing/invalid SCRATCH_ACK ack flag"
  local cur; cur="$(docker exec "$SCRATCH_CONTAINER" psql -U postgres -tAc 'select current_database()' "$SCRATCH_DB" 2>/dev/null || true)"
  case "$cur" in stayconnect_site|stayconnect|stayconnect_site_b) _die "refusing to mark a LIVE database";; esac
  [ "$cur" = "$SCRATCH_DB" ] || _die "current_database()='$cur' != scratch db '$SCRATCH_DB'"
  docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt \
    -c "CREATE TABLE IF NOT EXISTS public._scratch_marker(marker text PRIMARY KEY); INSERT INTO public._scratch_marker VALUES ('$SCRATCH_MARKER_VALUE') ON CONFLICT DO NOTHING;" >/dev/null
}

safety_guard(){ require_scratch; }   # back-compat alias

# guarded runners (require the full allowlist)
psqlx()  { require_scratch; docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }
psqlf()  { require_scratch; docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 < "$1"; }
psql_ac(){ require_scratch; docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }
# run as a specific (non-superuser) role for least-privilege tests
psql_as(){ require_scratch; docker exec -i "$SCRATCH_CONTAINER" psql -U "$1" -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$2"; }
