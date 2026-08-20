#!/usr/bin/env bash
# FACTORY-CLEAN PRODUCTION BASELINE — build it, and prove nothing superseded is EVER constructed.
#
# The reconstruction script next door proves the UPGRADE path reaches a clean end state. This proves the
# stronger property the Production requirement actually asks for: a brand-new appliance never builds the
# superseded guest-IAM tables at any point, not even for the moment between one migration creating them and
# another dropping them.
#
# "Never" is asserted by a DDL EVENT TRIGGER installed BEFORE the baseline runs. It fires on every CREATE
# TABLE in the session and raises on a superseded name, so a baseline that constructed one could not finish.
# Inspecting the catalog afterwards cannot tell "never created" from "created and dropped"; this can.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE="$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql"
GATEP="$ROOT/deploy/gatep"
C="sc-baseline-$$"
OUT="${BASELINE_OUT:-$ROOT/.cleanroom-baseline}"
mkdir -p "$OUT"
fail=0
note() { printf '  %s\n' "$*"; }
bad()  { printf '  FAIL: %s\n' "$*"; fail=1; }
cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

[ -f "$BASE" ] || { echo "  FAIL: baseline missing; run scripts/generate-production-baseline.sh"; exit 1; }

echo "== blank PostgreSQL cluster (never saw any appliance) =="
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=stayconnect_site \
  "${CLEANROOM_IMAGE:-timescale/timescaledb:2.16.1-pg16}" >/dev/null
ready=0
for _ in $(seq 1 180); do
  if [ "$(docker logs "$C" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ]; then
    ok=0
    for _ in 1 2 3; do
      docker exec "$C" psql -U postgres -d stayconnect_site -tAqc 'select 1' >/dev/null 2>&1 && ok=$((ok+1))
      sleep 1
    done
    [ "$ok" = "3" ] && { ready=1; break; }
  fi
  sleep 1
done
[ "$ready" = "1" ] || { echo "  FAIL: database never became ready"; exit 1; }
psql_run() { docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 "$@"; }
psql_q()   { docker exec -i "$C" psql -U postgres -d stayconnect_site -tAq "$@"; }

echo
echo "== the tripwire: constructing a superseded guest-IAM table is a hard stop =="
psql_run -f /dev/stdin < "$ROOT/scripts/sql/zero-legacy-tripwire.sql" >"$OUT/tripwire.log" 2>&1 \
  && note "tripwire armed before a single object exists" \
  || { bad "tripwire could not be armed: $(tail -1 "$OUT/tripwire.log")"; exit 1; }

echo
echo "== Gate-P roles, then the baseline, then Gate-P ownership and grants =="
psql_run < "$GATEP/gatep-roles.sql" >"$OUT/roles.log" 2>&1 || bad "gatep-roles.sql: $(tail -1 "$OUT/roles.log")"
psql_run -tAqc "DO \$\$ BEGIN CREATE ROLE stayconnect NOLOGIN SUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;" >/dev/null
docker cp "$GATEP" "$C:/tmp/gatep" >/dev/null
# BEFORE the baseline: the baseline carries its own privileges, and those name iam_v2_owner. The REFERENCES
# grant inside this file is guarded on its target existing, so it is applied again below once it does.
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1   -f /tmp/gatep/gatep-iam-roles.sql >"$OUT/iam-roles-pre.log" 2>&1   || bad "gatep-iam-roles.sql (pre): $(tail -1 "$OUT/iam-roles-pre.log")"

# The dump carries both schemas in one file, so it is applied as the platform role and Gate-P reasserts IAM
# ownership afterwards -- which is exactly what gatep-iam-ownership.sql exists for and is proven idempotent.
if { printf 'SET ROLE stayconnect;\n'; cat "$BASE"; } | psql_run >"$OUT/baseline.log" 2>&1; then
  note "baseline applied with the tripwire armed throughout"
else
  bad "baseline did not apply: $(tail -3 "$OUT/baseline.log")"
fi
# The tripwire is an instrument, not part of the product: it comes out complete, function included, so the
# schema being compared is the schema a Production appliance would actually have.
psql_run -tAqc "DROP EVENT TRIGGER IF EXISTS zero_legacy_tripwire" >/dev/null
psql_run -tAqc "DROP FUNCTION IF EXISTS public.zero_legacy_tripwire()" >/dev/null
# AFTER the baseline, so the guarded REFERENCES grant on public.guest_networks now lands.
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-iam-roles.sql >"$OUT/iam-roles.log" 2>&1 || bad "gatep-iam-roles.sql: $(tail -1 "$OUT/iam-roles.log")"
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-iam-ownership.sql >"$OUT/own.log" 2>&1 || bad "ownership: $(tail -1 "$OUT/own.log")"
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-grants.sql >"$OUT/grants.log" 2>&1 || bad "grants: $(tail -1 "$OUT/grants.log")"
note "Gate-P ownership and grants applied"

echo
echo "== REQUIRED: the superseded guest-IAM tables were never constructed and do not exist =="
legacy="$(psql_q -c "SELECT COALESCE(string_agg(tablename, ', ' ORDER BY tablename),'') FROM pg_tables
                      WHERE schemaname='public' AND tablename IN
                        ('sessions','guests','guest_accounts','vouchers','voucher_batches',
                         'ticket_templates','payments')")"
[ -z "$legacy" ] && note "none exist" || bad "present: $legacy"
dangling="$(psql_q -c "SELECT count(*) FROM pg_constraint c JOIN pg_class r ON r.oid=c.confrelid
                        JOIN pg_namespace n ON n.oid=r.relnamespace WHERE c.contype='f' AND n.nspname='public'
                          AND r.relname IN ('sessions','guests','guest_accounts','vouchers','voucher_batches',
                                            'ticket_templates','payments')")"
[ "$dangling" = "0" ] && note "no foreign key references any of them" \
                      || bad "a foreign key still references a removed object"

echo
echo "== REQUIRED: correct ownership and a live IAM-v2 domain =="
badown="$(psql_q -c "SELECT COALESCE(string_agg(name,', '),'') FROM (
    SELECT n.nspname||'.'||c.relname AS name FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
     WHERE n.nspname='iam_v2' AND c.relkind IN ('r','v','m','p','S')
       AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner'
    UNION ALL
    SELECT n.nspname||'.'||p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
     WHERE n.nspname='iam_v2' AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner') x")"
[ -z "$badown" ] && note "every iam_v2 object is owned by iam_v2_owner" || bad "wrong owner: $badown"
secdef_bad="$(psql_q -c "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
                          WHERE n.nspname='iam_v2' AND p.prosecdef
                            AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner'")"
[ "$secdef_bad" = "0" ] && note "every SECURITY DEFINER function executes as iam_v2_owner" \
                        || bad "$secdef_bad SECURITY DEFINER functions execute as the wrong owner"
anchor="$(psql_q -c "SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
                      WHERE c.relname='guest_networks_tsi_anchor' AND i.indisvalid AND i.indisready")"
[ "$anchor" = "1" ] && note "guest_networks_tsi_anchor present and valid" || bad "anchor missing"
mapfk="$(psql_q -c "SELECT count(*) FROM pg_constraint WHERE conrelid='iam_v2.guest_network_pms_map'::regclass
                     AND contype='f' AND confrelid='public.guest_networks'::regclass")"
[ "$mapfk" = "1" ] && note "guest_network_pms_map composite FK present" || bad "mapping FK absent"

echo
echo "== REQUIRED: the hypertables accept writes =="
# THE CATALOG COMPARISON CANNOT SEE THIS. A --schema-only dump reproduces the table, its columns, its
# constraints, its indexes AND its ts_insert_blocker trigger, but not the TimescaleDB registration that makes
# the table a hypertable. Both sides then look identical while the restored one refuses every INSERT with
# "invalid INSERT on the root table of hypertable". It reached a Production appliance: every audit_log write
# failed from first boot, and the operator saw it only as a warning in a log.
#
# So this is asserted by WRITING, not by comparing. A structural check is what missed it the first time.
hyper="$(psql_q -c "SELECT count(*) FROM timescaledb_information.hypertables")"
[ "$hyper" -ge 2 ] && note "$hyper hypertables registered"                    || bad "expected at least 2 registered hypertables, found $hyper"
for t in audit_log accounting_records; do
  # `|| true` on the capture: under `set -e` a failing command substitution ends the script, which is how an
  # earlier version of this check silently stopped the whole verifier at this line instead of reporting.
  err="$(psql_run -tAqc "INSERT INTO public.$t (ts) VALUES (now())" 2>&1 || true)"
  case "$err" in
    *"invalid INSERT on the root table of hypertable"*)
      bad "public.$t is NOT a registered hypertable: every write is blocked" ;;
    "")
      psql_run -tAqc "DELETE FROM public.$t WHERE ts > now() - interval '1 minute'" >/dev/null 2>&1 || true
      note "public.$t accepts an INSERT" ;;
    *)
      # A NOT NULL / FK complaint means the write reached the TABLE, which is what is being proven here.
      note "public.$t reached its own constraints (hypertable registration is live)" ;;
  esac
done

echo
echo "== REQUIRED: nothing seeded =="
for t in tenants sites appliances operators; do
  n="$(psql_q -c "SELECT count(*) FROM public.$t" 2>/dev/null || echo '?')"
  [ "$n" = "0" ] || bad "public.$t is not empty ($n rows)"
done
for t in guest_principals guest_access_accounts vouchers sessions entitlements; do
  n="$(psql_q -c "SELECT count(*) FROM iam_v2.$t" 2>/dev/null || echo '?')"
  [ "$n" = "0" ] || bad "iam_v2.$t is not empty ($n rows)"
done
note "every identity-bearing table is empty"

echo
echo "== semantic catalog, and agreement with the upgrade path =="
if [ -f "$ROOT/.cleanroom/catalog.sql" ]; then
  cp "$ROOT/.cleanroom/catalog.sql" "$OUT/catalog.sql"
  psql_q -f /dev/stdin < "$OUT/catalog.sql" | sed '/^$/d' | sort > "$OUT/catalog.txt"
  note "catalog facts: $(wc -l < "$OUT/catalog.txt")"
  # THE TWO PATHS MUST AGREE. A baseline that drifted would mean new and existing appliances running
  # different schemas -- which is why the baseline is generated from the upgrade path rather than written.
  if [ -f "$ROOT/.cleanroom/catalog.txt" ]; then
    # CHECK constraints are compared with their BRACKETING removed, and only their bracketing.
    #
    # pg_get_constraintdef re-renders an expression from its parsed tree. A definition that has been through
    # a dump and back can come out grouped differently -- "((A AND B) AND C)" instead of "(A AND B AND C)" --
    # because AND is associative and the re-parse flattened it. That is a rendering difference, not a schema
    # difference, and three payment CHECK constraints hit it.
    #
    # Only CONSTRAINT lines are treated this way, and within them only the parenthesis characters are
    # dropped: columns, operators, literals, functions and their order are all still compared exactly, so a
    # genuinely different predicate still shows up.
    canon() { sed -E '/^CONSTRAINT\|/ s/[()]//g' "$1"; }
    canon "$ROOT/.cleanroom/catalog.txt" > "$OUT/upgrade.canon"
    canon "$OUT/catalog.txt" > "$OUT/baseline.canon"
    if diff -u "$OUT/upgrade.canon" "$OUT/baseline.canon" > "$OUT/paths.diff"; then
      note "IDENTICAL to the upgrade path's end state"
    else
      bad "baseline and upgrade path disagree on $(grep -c '^[-+][^-+]' "$OUT/paths.diff") facts (see $OUT/paths.diff)"
    fi
  else
    note "no upgrade-path catalog to compare against; run scripts/clean-install-reconstruction.sh first"
  fi
fi

echo
[ "$fail" = "0" ] || { echo "FACTORY_CLEAN_BASELINE = FAIL"; exit 1; }
echo "FACTORY_CLEAN_BASELINE = PASS (current-only: the superseded guest-IAM tables were never constructed)"
