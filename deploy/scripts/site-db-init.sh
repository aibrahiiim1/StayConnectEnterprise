#!/usr/bin/env bash
# BUILD (OR CONFIRM) THE SITE DATABASE OF A FACTORY-CLEAN APPLIANCE — atomically, and safe to run again.
#
# This is step 4 of provision-fresh-appliance.sh, moved into its own script so it can be proven on a disposable
# database (scripts/ci/site-db-init-selftest.sh) instead of only on a real appliance. It replaces an inline
# sequence that had three defects, all measured on a blank database in October 2026:
#
#   K1  the committed baseline embodied migrations only to 0149, while gatep-grants.sql from the same revision
#       already granted on objects created by 0155. A blank install aborted in the middle of Gate-P.
#   K2  the baseline was applied unconditionally, statement by statement. After any failure the database held
#       half a schema and every re-run died at its first statement ("schema iam_v2 already exists"), so the
#       provisioner could not be resumed.
#   -   nothing proved, afterwards, that ownership of the new objects had actually converged.
#
# What it does now:
#
#   1. Refuses BEFORE touching the database if the baseline does not embody the newest migration it ships
#      with (deploy/scripts/lib-baseline.sh). A stale baseline is a release defect, not something to repair
#      on a customer's appliance.
#   2. Applies the baseline in ONE transaction (psql --single-transaction). It either commits completely or
#      leaves nothing behind, so an interrupted first run is followed by a clean second one.
#   3. On an existing schema it never re-applies the baseline. A schema built by a baseline (empty migration
#      ledger) must match the baseline's table count; one that does not was left by an older, non-atomic
#      install, or is an older release's schema — both are reported, never "repaired" in place. A schema with
#      migration-ledger rows is a live, upgraded database and is left to the upgrade procedure.
#   4. Converges Gate-P every time: IAM roles, ownership of every iam_v2 object, then the hash-verified grant
#      reconcile (scripts/gatep-reconcile.sh) in one transaction. All three are idempotent.
#   5. Asserts the result: two registered hypertables, and no iam_v2 relation or function owned by anybody but
#      iam_v2_owner.
#
# The one destructive option, --reinitialise-unused, exists for a box whose first install was interrupted by
# an OLDER provisioner. It drops and recreates the database only after proving the database holds no data that
# could matter: no operator, no tenant, no client identity, no session, no ledger row. Anything else refuses.
#
# Usage (root on the appliance; the provisioner calls it):
#   site-db-init.sh [--container stayconnect-pg] [--user stayconnect] [--db stayconnect_site]
#                   [--migrations /opt/stayconnect/data-plane/migrations] [--reinitialise-unused]
#
# Exit: 0 ready · 1 refused, database unchanged · 2 a step failed (its transaction rolled back) · 3 usage
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$(cd "$HERE/.." && pwd)"
ROOT="$(cd "$DEPLOY/.." && pwd)"
# shellcheck source=lib-baseline.sh
. "$HERE/lib-baseline.sh"

CONTAINER="stayconnect-pg"; PGUSER_ROLE="stayconnect"; DB="stayconnect_site"
MIGRATIONS="$ROOT/data-plane/migrations"; REINIT=0
while [ $# -gt 0 ]; do
  case "$1" in
    --container) CONTAINER="${2:?}"; shift 2;;
    --user) PGUSER_ROLE="${2:?}"; shift 2;;
    --db) DB="${2:?}"; shift 2;;
    --migrations) MIGRATIONS="${2:?}"; shift 2;;
    --reinitialise-unused) REINIT=1; shift;;
    -h|--help) sed -n '2,40p' "$0"; exit 0;;
    *) echo "site-db-init: unknown argument $1" >&2; exit 3;;
  esac
done

say() { echo "[site-db] $*"; }
refuse() { echo "[site-db] REFUSED (database unchanged): $*" >&2; exit 1; }
fail() { echo "[site-db] FAILED: $*" >&2; exit 2; }

GP="$DEPLOY/gatep"
BASELINE="$MIGRATIONS/baseline/0000_production_baseline.sql"
[ -f "$BASELINE" ] || refuse "missing $BASELINE (a new appliance is built from the baseline, never from 0001..)"
[ -d "$GP" ] || refuse "missing $GP"
why="$(baseline_is_current "$BASELINE" "$MIGRATIONS")" || refuse "$why"
THROUGH="$(baseline_through "$BASELINE")"

# Every in-container path is written with a doubled leading slash: Git Bash (the self-test's host on a
# workstation) rewrites a lone leading "/" into a Windows path, and Linux collapses "//" to "/".
psql_db() { docker exec -i -e PGOPTIONS="-c client_min_messages=warning" "$CONTAINER" psql -v ON_ERROR_STOP=1 -X -q -U "$PGUSER_ROLE" -d "$1" "${@:2}"; }
q() { docker exec -i "$CONTAINER" psql -X -tAq -U "$PGUSER_ROLE" -d "$DB" -c "$1" </dev/null; }
# rows_or <schema.table> <value-if-absent>: a table's row count, or the given value when it does not exist. Two
# queries on purpose: a subquery naming a missing table fails at parse time even behind CASE WHEN to_regclass.
rows_or() {
  if [ "$(q "SELECT to_regclass('$1') IS NOT NULL")" = t ]; then q "SELECT count(*) FROM $1"; else echo "$2"; fi
}

docker exec "$CONTAINER" pg_isready -U "$PGUSER_ROLE" >/dev/null 2>&1 || fail "postgres in $CONTAINER is not ready"

# ---- stage a file set into the container and prove it arrived byte-identical
STAGE_REMOTE="/tmp/site-db-init.$$"
cleanup() { docker exec "$CONTAINER" rm -rf "/$STAGE_REMOTE" >/dev/null 2>&1 || true; }
trap cleanup EXIT
stage() { # <local file> <remote name>
  docker exec "$CONTAINER" mkdir -p "/$STAGE_REMOTE" >/dev/null 2>&1 || fail "cannot create a staging directory in $CONTAINER"
  # Streamed through stdin rather than `docker cp`: no host-path or container-path argument to be rewritten,
  # and no "copy into an existing directory nests one level deeper" behaviour (the PRE-LIVE Gate-P mistake).
  docker exec -i "$CONTAINER" sh -c "cat > '$STAGE_REMOTE/$2'" < "$1" || fail "cannot copy $1 into $CONTAINER"
  local want got
  want="$(sha256sum "$1" | awk '{print $1}')"
  got="$(docker exec "$CONTAINER" sha256sum "/$STAGE_REMOTE/$2" 2>/dev/null | awk '{print $1}')"
  [ "$want" = "$got" ] || fail "$2 differs inside the container after copying (expected $want, found ${got:-absent})"
}
run_staged() { # <remote name> [psql options...]
  local n="$1"; shift
  psql_db "$DB" "$@" -f "/$STAGE_REMOTE/$n" </dev/null >/dev/null || return 1
}

# ---- 1. the database
exists="$(docker exec -i "$CONTAINER" psql -X -tAq -U "$PGUSER_ROLE" -d postgres -c "SELECT 1 FROM pg_database WHERE datname='$DB'" </dev/null)"
if [ "$exists" != 1 ]; then
  docker exec -i "$CONTAINER" psql -X -q -v ON_ERROR_STOP=1 -U "$PGUSER_ROLE" -d postgres -c "CREATE DATABASE $DB" </dev/null >/dev/null \
    || fail "could not create database $DB"
  say "created database $DB"
fi

# ---- 2. what is already there
schema="$(q "SELECT count(*) FROM pg_namespace WHERE nspname='iam_v2'")"
want_tables="$(grep -cE '^CREATE (UNLOGGED )?TABLE iam_v2\.' "$BASELINE")"
if [ "$schema" = 1 ]; then
  ledger_rows="$(rows_or public.schema_migrations -1)"
  have_tables="$(q "SELECT count(*) FROM pg_tables WHERE schemaname='iam_v2'")"
  if [ "${ledger_rows:-0}" -gt 0 ] 2>/dev/null; then
    say "existing schema with $ledger_rows migration-ledger row(s): a live, upgraded database — the baseline is not re-applied"
    say "  (pending migrations are applied by the upgrade procedure, deploy/scripts/appliance-install.sh --upgrade)"
  elif [ "$have_tables" = "$want_tables" ]; then
    say "existing schema built from a baseline ($have_tables iam_v2 tables, as the shipped baseline defines) — not re-applied"
  else
    # Partial (an older, non-atomic install was interrupted) or an older release's baseline. Either way the
    # schema is not what this release installs, and patching it in place would invent an unrecorded state.
    data=0
    for t in public.operators public.tenants iam_v2.guest_principals iam_v2.sessions iam_v2.vouchers; do
      n="$(rows_or "$t" 0)"
      case "$n" in ''|*[!0-9]*) data=unknown; break;; esac
      data=$((data + n))
    done
    if [ "$REINIT" = 1 ] && [ "${data:-1}" = 0 ] && [ "${ledger_rows:-0}" -le 0 ] 2>/dev/null; then
      say "schema is incomplete or from another release ($have_tables of $want_tables iam_v2 tables) and holds NO data:"
      say "  --reinitialise-unused given — dropping and recreating $DB"
      docker exec -i "$CONTAINER" psql -X -q -v ON_ERROR_STOP=1 -U "$PGUSER_ROLE" -d postgres \
        -c "DROP DATABASE $DB WITH (FORCE)" -c "CREATE DATABASE $DB" </dev/null >/dev/null || fail "could not recreate $DB"
      schema=0
    elif [ "${data:-1}" = 0 ]; then
      refuse "the iam_v2 schema has $have_tables tables but this release's baseline defines $want_tables, and the
         migration ledger is empty. This is an interrupted install from an older provisioner, or an older
         release's schema. It holds no operators, tenants, clients, sessions or vouchers, so it can be rebuilt:
         re-run with --reinitialise-unused (provision-fresh-appliance.sh: REINITIALISE_UNUSED_DB=1)."
    else
      refuse "the iam_v2 schema has $have_tables tables but this release's baseline defines $want_tables, and it HOLDS
         DATA ($data row(s) of operators/tenants/clients/sessions/vouchers). It is never rebuilt. Upgrade it with
         deploy/scripts/appliance-install.sh --upgrade, or restore it from a backup."
    fi
  fi
fi

if [ "$schema" != 1 ]; then
  # ---- 3. a blank database: roles, then the whole baseline as ONE transaction
  stage "$GP/gatep-roles.sql" gatep-roles.sql
  stage "$GP/gatep-iam-roles.sql" gatep-iam-roles.sql
  stage "$BASELINE" baseline.sql
  run_staged gatep-roles.sql || fail "gatep-roles.sql"
  # The IAM roles run before the baseline (its privileges name them) and again after (their guarded REFERENCES
  # grants need tables the baseline creates). The file is idempotent and order-tolerant.
  run_staged gatep-iam-roles.sql 2>/dev/null || fail "gatep-iam-roles.sql (before the baseline)"
  say "applying the factory-clean baseline (migrations 0001..$THROUGH) in a single transaction"
  run_staged baseline.sql --single-transaction || fail "the baseline did not apply; its transaction rolled back and the database is as it was"
  say "baseline applied"
fi

# ---- 4. Gate-P, every time
stage "$GP/gatep-iam-roles.sql" gatep-iam-roles.sql
stage "$GP/gatep-iam-ownership.sql" gatep-iam-ownership.sql
run_staged gatep-iam-roles.sql || fail "gatep-iam-roles.sql"
run_staged gatep-iam-ownership.sql || fail "gatep-iam-ownership.sql"
if [ -f "$ROOT/scripts/gatep-reconcile.sh" ]; then
  bash "$ROOT/scripts/gatep-reconcile.sh" --container "$CONTAINER" --db "$DB" --user "$PGUSER_ROLE" >/dev/null \
    || fail "the Gate-P grant reconcile did not apply (it rolled back; effective privileges are unchanged)"
else
  # A deploy tree without scripts/ (hand-staged, older procedure): the same file, staged and hash-checked here.
  for f in gatep-grants.sql $(grep -E '^\\ir[[:space:]]+' "$GP/gatep-grants.sql" | awk '{print $2}'); do stage "$GP/$f" "$f"; done
  run_staged gatep-grants.sql || fail "gatep-grants.sql (its transaction rolled back)"
fi
say "Gate-P converged (roles, ownership, grants)"

# ---- 5. prove it
hyper="$(q "SELECT count(*) FROM timescaledb_information.hypertables")"
[ "${hyper:-0}" -ge 2 ] || fail "only ${hyper:-0} hypertables registered — audit_log/accounting_records would reject every write"
foreign="$(q "SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles r ON r.oid=c.relowner
                       WHERE n.nspname='iam_v2' AND c.relkind IN ('r','v','m','S','p') AND r.rolname<>'iam_v2_owner')
                   + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles r ON r.oid=p.proowner
                       WHERE n.nspname='iam_v2' AND r.rolname<>'iam_v2_owner')")"
[ "${foreign:-1}" = 0 ] || fail "$foreign iam_v2 object(s) are not owned by iam_v2_owner after the ownership step"
tables="$(q "SELECT count(*) FROM pg_tables WHERE schemaname='iam_v2'")"
ledger="$(q "SELECT count(*) FROM public.schema_migrations")"
say "ready: $DB — iam_v2 $tables tables, $hyper hypertables, ownership converged, baseline through $THROUGH, $ledger ledger row(s)"
exit 0
