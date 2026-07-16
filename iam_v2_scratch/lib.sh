#!/usr/bin/env bash
# iam_v2 SCRATCH harness library — disposable scratch/test PostgreSQL only.
# HARD SAFETY GUARD: refuses to run against anything that looks like a live/pilot target.
set -euo pipefail

export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"

SCRATCH_CONTAINER="${SCRATCH_CONTAINER:-iamv2-scratch}"
SCRATCH_DB="${SCRATCH_DB:-iam_scratch}"

# --- Live-target blocklist. If any of these appear in the resolved target, ABORT. ---
LIVE_PATTERNS='172\.21\.60\.23|172\.21\.96\.150|150\.0\.0\.|120\.0\.0\.|stayconnect_site|stayconnect-pg|stayconnect_site_b|/opt/stayconnect|appliance'

safety_guard() {
  # 1) Never allow the container name / db to be a known live identifier.
  if echo "$SCRATCH_CONTAINER $SCRATCH_DB" | grep -qiE "$LIVE_PATTERNS"; then
    echo "SAFETY ABORT: scratch target matches a known live identifier ($SCRATCH_CONTAINER/$SCRATCH_DB)." >&2; exit 90
  fi
  # 2) The target must be our disposable local docker container, bound to loopback only.
  local ports; ports="$(docker inspect -f '{{json .NetworkSettings.Ports}}' "$SCRATCH_CONTAINER" 2>/dev/null || echo '')"
  if echo "$ports" | grep -qE '"HostIp":"(0\.0\.0\.0|::)"'; then
    echo "SAFETY ABORT: scratch container is published on a non-loopback address." >&2; exit 91
  fi
  # 3) The container must NOT be able to reach a live appliance DSN via its env.
  local envv; envv="$(docker inspect -f '{{json .Config.Env}}' "$SCRATCH_CONTAINER" 2>/dev/null || echo '')"
  if echo "$envv" | grep -qiE "$LIVE_PATTERNS"; then
    echo "SAFETY ABORT: scratch container env references a live target." >&2; exit 92
  fi
  # 4) Confirm the DB we will write to is exactly the disposable scratch DB.
  local cur; cur="$(docker exec "$SCRATCH_CONTAINER" psql -U postgres -tAc 'select current_database()' "$SCRATCH_DB" 2>/dev/null || true)"
  if [ "$cur" != "$SCRATCH_DB" ]; then
    echo "SAFETY ABORT: could not confirm current_database() == $SCRATCH_DB (got '$cur')." >&2; exit 93
  fi
}

# psql runner (guarded). Usage: psqlx "<sql>"   OR   psqlf <file>
psqlx() { safety_guard; docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }
psqlf() { safety_guard; docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 < "$1"; }
# autocommit (no txn) runner — REQUIRED for CREATE/DROP INDEX CONCURRENTLY
psql_ac() { safety_guard; docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }
