#!/usr/bin/env bash
# GENERATE THE FACTORY-CLEAN PRODUCTION BASELINE.
#
# WHY A BASELINE EXISTS AT ALL
# ----------------------------
# A brand-new Production appliance must never CONSTRUCT the superseded guest-IAM tables, not even for the
# moment between migration 0048 creating them and 0049 dropping them again. "The final catalog is clean" is a
# weaker property than "they were never built": create-then-delete leaves them in every WAL segment, in any
# backup taken mid-install, and in the install log a reviewer reads.
#
# So the repository carries TWO paths, and they are different things:
#
#   UPGRADE PATH   data-plane/migrations/0001..0049 -- how an EXISTING installation reaches the current
#                  schema. It still creates the superseded tables, because that is what actually happened,
#                  and 0049 removes them. History is not rewritten.
#
#   BASELINE PATH  data-plane/migrations/baseline/  -- how a NEW installation is built. Current structures
#                  only. Nothing superseded is ever created.
#
# THE BASELINE IS GENERATED, NOT HAND-MAINTAINED. It is a schema dump of the end state of the upgrade path,
# so the two cannot drift: if a migration changes, the baseline is regenerated from it and
# scripts/factory-clean-baseline-verify.sh proves the two paths still agree object for object.
#
# THREE THINGS pg_dump WILL NOT GIVE YOU, each of which cost a verification run to discover:
#
#   EXTENSIONS  a --schema-restricted dump omits CREATE EXTENSION entirely, so pgcrypto, uuid-ossp and
#               timescaledb (and every function they own) simply vanish from the baseline.
#   ROLES       pg_dump never dumps roles. Not all of this system's roles come from Gate-P: migration 0017
#               and its successors create the least-privilege financial roles. Without them the baseline
#               installs grants naming roles that do not exist.
#   HYPERTABLES a --schema-only dump of a TimescaleDB database emits the plain table AND its
#               ts_insert_blocker trigger, but NOT the TimescaleDB catalog registration that makes the
#               table a hypertable. The restored table therefore has a trigger that refuses every INSERT
#               into an unregistered root -- "invalid INSERT on the root table of hypertable" -- and
#               audit_log and accounting_records silently reject every write on a freshly deployed
#               appliance. Caught in Production bring-up, not in the catalog comparison, because the
#               comparison sees the same columns, constraints, indexes and triggers on both sides.
#   PRIVILEGES  --no-privileges looks right -- ownership and grants are Gate-P's job -- but the migrations do
#               not only CREATE, they REVOKE. Migration 0010 revokes EXECUTE on the raw grace writer from
#               PUBLIC, and the D32 boundary assertion in gatep-grants.sql checks exactly that. Dumped
#               without privileges, every function came back PUBLIC-executable and Gate-P refused the install.
#
# Usage: bash scripts/generate-production-baseline.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/data-plane/migrations/baseline"
C="sc-baseline-gen-$$"
# Derived, not written down: the header names the upgrade range, and a hardcoded number silently becomes a
# lie the first time someone adds a migration — which is exactly what happened at 0050.
LAST_MIGRATION="$(ls "$ROOT"/data-plane/migrations/*.up.sql | sed -E 's#.*/([0-9]+)_.*#\1#' | sort -n | tail -1)"
mkdir -p "$OUT"
cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== building the upgrade path to obtain its end state =="
CLEANROOM_KEEP=1 CLEANROOM_NAME="$C" bash "$ROOT/scripts/clean-install-reconstruction.sh" >/dev/null
docker inspect "$C" >/dev/null 2>&1 || { echo "FAIL: no cleanroom container to dump from"; exit 1; }

echo "== extensions =="
docker exec "$C" psql -U postgres -d stayconnect_site -tAqc \
  "SELECT 'CREATE EXTENSION IF NOT EXISTS '||quote_ident(e.extname)||' WITH SCHEMA '||quote_ident(n.nspname)||';'
     FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
    WHERE e.extname <> 'plpgsql' ORDER BY 1" | sed '/^$/d' > "$OUT/.ext.tmp"
echo "  $(wc -l < "$OUT/.ext.tmp") extension(s)"

echo "== roles the MIGRATIONS create (Gate-P creates the rest) =="
docker exec "$C" psql -U postgres -d stayconnect_site -tAqc \
  "SELECT 'DO \$do\$ BEGIN CREATE ROLE '||quote_ident(rolname)||' NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$do\$;'
     FROM pg_roles
    WHERE NOT rolsuper AND rolname NOT LIKE 'pg\_%'
      AND rolname NOT IN ('svc_scd','svc_edged','svc_acctd','svc_netd','iam_v2_owner','iam_v2_migrator',
                          'stayconnect','postgres')
    ORDER BY rolname" | sed '/^$/d' > "$OUT/.roles.tmp"
echo "  $(wc -l < "$OUT/.roles.tmp") role(s)"

echo "== hypertables =="
# Captured from the SOURCE database's own TimescaleDB catalog, so the baseline re-registers exactly what
# the upgrade path built -- table, time column and chunk interval -- rather than a hardcoded guess.
docker exec "$C" psql -U postgres -d stayconnect_site -tAqc   "SELECT format('SELECT public.create_hypertable(%L, %L, chunk_time_interval => INTERVAL %L, if_not_exists => TRUE, migrate_data => TRUE);',
                 h.hypertable_schema||'.'||h.hypertable_name, d.column_name, d.time_interval)
     FROM timescaledb_information.hypertables h
     JOIN timescaledb_information.dimensions d
       ON d.hypertable_schema = h.hypertable_schema AND d.hypertable_name = h.hypertable_name
    WHERE d.dimension_number = 1
    ORDER BY 1" | sed '/^$/d' > "$OUT/.hyper.tmp"
echo "  $(wc -l < "$OUT/.hyper.tmp") hypertable(s)"
[ -s "$OUT/.hyper.tmp" ] || { echo "  FAIL: the source database registers no hypertables -- refusing to write a baseline that would silently block audit_log/accounting_records writes"; exit 1; }

echo "== dumping the CURRENT schema (no data, no ownership; privileges KEPT) =="
docker exec "$C" pg_dump -U postgres -d stayconnect_site \
  --schema-only --no-owner --schema=public --schema=iam_v2 2>/dev/null \
  > "$OUT/.schema.tmp"

{
  echo "-- FACTORY-CLEAN PRODUCTION BASELINE -- GENERATED, DO NOT HAND-EDIT."
  echo "--"
  echo "-- Regenerate with: bash scripts/generate-production-baseline.sh"
  echo "-- Verify with:    bash scripts/factory-clean-baseline-verify.sh"
  echo "--"
  echo "-- This is the CURRENT schema and only the current schema. A new Production appliance is built from"
  echo "-- this file and never constructs the superseded guest-IAM tables, not even transiently. Existing"
  echo "-- installations continue to upgrade through data-plane/migrations/0001..$LAST_MIGRATION, which still create"
  echo "-- those tables and then remove them, because that is what actually happened to them."
  echo "--"
  echo "-- OWNERSHIP is deliberately absent: it belongs to Gate-P (deploy/gatep/gatep-iam-ownership.sql), and"
  echo "-- a second source for it would be a competing security model. PRIVILEGES are present, because the"
  echo "-- migrations REVOKE as well as create and those revocations are part of the schema's meaning."
  echo ""
  echo "-- Extensions. A --schema-restricted pg_dump omits these entirely."
  cat "$OUT/.ext.tmp"
  echo ""
  echo "-- Roles created by the migrations rather than by Gate-P. Idempotent, so a rebuild is safe."
  cat "$OUT/.roles.tmp"
  echo ""
  # Two edits to the dump:
  #   CREATE SCHEMA public  -- already present on every database; left in, it aborts the first statement.
  #   ts_insert_blocker     -- dropped, because create_hypertable() installs its own below. Restoring the
  #                            dumped trigger onto an UNREGISTERED table is exactly what blocks every write.
  sed -e 's/^CREATE SCHEMA public;$/-- CREATE SCHEMA public;  -- always present; see generate-production-baseline.sh/' \
      -e '/^CREATE TRIGGER ts_insert_blocker/,/;[[:space:]]*$/d' \
      "$OUT/.schema.tmp"
  echo ""
  echo "-- TimescaleDB hypertable registration. A --schema-only dump does not carry it, and without this the"
  echo "-- tables exist, match every catalog comparison, and refuse every INSERT."
  cat "$OUT/.hyper.tmp"
} > "$OUT/0000_production_baseline.sql"
rm -f "$OUT/.schema.tmp" "$OUT/.ext.tmp" "$OUT/.roles.tmp" "$OUT/.hyper.tmp"
docker rm -f "$C" >/dev/null 2>&1 || true

echo "  wrote $OUT/0000_production_baseline.sql ($(wc -l < "$OUT/0000_production_baseline.sql") lines)"
for t in sessions guests guest_accounts vouchers voucher_batches ticket_templates payments; do
  if grep -qE "^CREATE TABLE public\.$t " "$OUT/0000_production_baseline.sql"; then
    echo "  FAIL: the generated baseline creates public.$t"; exit 1
  fi
done
echo "  the baseline creates none of the superseded guest-IAM tables"
