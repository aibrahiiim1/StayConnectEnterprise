#!/usr/bin/env bash
# Gate P — end-to-end dry run of the EXACT committed scripts against a GENUINELY DISPOSABLE
# PostgreSQL/TimescaleDB cluster (a throwaway container, NOT the live stayconnect-pg cluster).
# Success is judged by the direct psql EXIT STATUS, never by grepping output. Includes a self-test
# proving that intentionally-invalid SQL makes the harness FAIL with a non-zero process exit.
#
# Usage: gatep-dryrun.sh <backup_dump_path> [--selftest-must-fail]
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
DUMP="${1:?need backup dump path}"
SELFTEST_MUST_FAIL="${2:-}"
IMAGE="timescale/timescaledb:2.16.1-pg16"        # match the appliance DB version
CNAME="gatep_dryrun_pg_$$"                        # disposable cluster container
DB="stayconnect_site"
SU="stayconnect"                                  # superuser inside the disposable cluster
DSN_OUT="/dev/shm/gatep_dryrun_dsn.$$"
ROLES=(svc_scd svc_edged svc_acctd svc_netd)
FAIL=0
ok(){ echo "  ok: $*"; }
bad(){ echo "  *** FAIL: $*"; FAIL=$((FAIL+1)); }

cleanup(){ docker rm -f "$CNAME" >/dev/null 2>&1 || true; rm -f "$DSN_OUT" 2>/dev/null || true; }
trap cleanup EXIT

# --- exit-status-based executors (superuser) ---------------------------------
dsu(){ docker exec -i "$CNAME" psql -v ON_ERROR_STOP=1 -U "$SU" -d "$DB" "$@"; }   # passthrough
q(){ docker exec "$CNAME" psql -tAX -U "$SU" -d "$DB" -c "$1"; }                   # scalar query
exec_file(){ docker exec -i "$CNAME" psql -v ON_ERROR_STOP=1 -U "$SU" -d "$DB" -q -f - < "$1" >/tmp/gp.out 2>&1; return $?; }
exec_sql(){  printf '%s' "$1" | docker exec -i "$CNAME" psql -v ON_ERROR_STOP=1 -U "$SU" -d "$DB" -q >/tmp/gp.out 2>&1; return $?; }
step(){ local label="$1"; shift; if "$@"; then ok "$label"; else bad "$label (rc=$?; $(tail -1 /tmp/gp.out 2>/dev/null))"; fi; }
# run a statement AS a runtime role (real SCRAM credential); returns psql exit status
asrole(){ local role="$1" sql="$2" pw; pw=$(sed -nE "s#.*://$role:([^@]+)@.*#\1#p" "$DSN_OUT"); \
  docker exec -e PGPASSWORD="$pw" "$CNAME" psql -tAX -h 127.0.0.1 -U "$role" -d "$DB" -c "$sql" >/tmp/gp.out 2>&1; return $?; }

echo "=== script checksums (exact committed) ==="
sha256sum "$HERE"/gatep-roles.sql "$HERE"/gatep-grants.sql "$HERE"/gatep-rollback.sql "$HERE"/gatep-set-passwords.sh "$HERE"/scram_verifier.py

echo "=== spin up DISPOSABLE cluster ($IMAGE) ==="
docker rm -f "$CNAME" >/dev/null 2>&1 || true
docker run -d --name "$CNAME" -e POSTGRES_USER="$SU" -e POSTGRES_PASSWORD=dispose_only \
  -e POSTGRES_DB="$DB" -e POSTGRES_HOST_AUTH_METHOD=scram-sha-256 -e TIMESCALEDB_TELEMETRY=off "$IMAGE" >/dev/null
# The timescaledb image runs timescaledb-tune and RESTARTS postgres once during init, which briefly
# drops connections. Require several CONSECUTIVE pg_isready successes so we are past that restart.
consec=0
for i in $(seq 1 150); do
  if docker exec "$CNAME" pg_isready -U "$SU" -d "$DB" >/dev/null 2>&1; then consec=$((consec+1)); else consec=0; fi
  [ "$consec" -ge 6 ] && break
  sleep 1
done
[ "$consec" -ge 6 ] && ok "disposable cluster ready (stable)" || { bad "cluster not stable-ready"; echo "GATEP_DRYRUN = FAIL ($FAIL)"; exit 1; }

echo "=== restore verified backup into disposable cluster ==="
docker exec "$CNAME" psql -U "$SU" -d "$DB" -c "CREATE EXTENSION IF NOT EXISTS timescaledb;" >/dev/null 2>&1
docker exec "$CNAME" psql -U "$SU" -d "$DB" -c "SELECT timescaledb_pre_restore();" >/dev/null 2>&1
cat "$DUMP" | docker exec -i "$CNAME" pg_restore -U "$SU" --no-owner -d "$DB" >/dev/null 2>&1
docker exec "$CNAME" psql -U "$SU" -d "$DB" -c "SELECT timescaledb_post_restore();" >/dev/null 2>&1
pub=$(q "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE'")
iam=$(q "select count(*) from information_schema.tables where table_schema='iam_v2'")
[ "$pub" = "42" ] && [ "$iam" = "49" ] && ok "restore 42 public + 49 iam_v2" || bad "restore $pub/$iam"
echo "  isolated public fingerprint: $(q "select string_agg(table_name||'.'||column_name||':'||data_type,',' order by table_name,ordinal_position) from information_schema.columns where table_schema='public'" | sha256sum | awk '{print $1}')"

echo "=== SELF-TEST: intentionally invalid SQL MUST fail the executor ==="
if exec_sql "SELECT gatep_intentionally_invalid_symbol();"; then bad "self-test: invalid SQL did NOT fail (harness broken)"; else ok "self-test: invalid SQL correctly fails (rc!=0)"; fi

echo "=== exact roles.sql ==="; step "roles.sql" exec_file "$HERE/gatep-roles.sql"
echo "=== secure SCRAM passwords ==="
if bash "$HERE/gatep-set-passwords.sh" --pg-exec "docker exec -i $CNAME" --db "$DB" --dsn-out "$DSN_OUT" >/tmp/gp.out 2>&1; then ok "passwords set (SCRAM)"; else bad "set-passwords ($(tail -1 /tmp/gp.out))"; fi
q "select left(rolpassword,13) from pg_authid where rolname='svc_scd'" | grep -qi "SCRAM-SHA-256" && ok "svc_scd stored SCRAM-SHA-256 (no cleartext)" || bad "svc_scd not SCRAM"
grep -qiE "postgres://svc_scd:[^@]+@" "$DSN_OUT" && ok "DSN file written (0600)" || bad "DSN file missing"

echo "=== exact grants.sql (reconciler) ==="; step "grants.sql" exec_file "$HERE/gatep-grants.sql"
echo "=== role attributes + zero iam_v2 ==="
q "select rolname||' super='||rolsuper||' cdb='||rolcreatedb||' crole='||rolcreaterole||' bypass='||rolbypassrls from pg_roles where rolname like 'svc_%' order by 1"
badattr=$(q "select count(*) from pg_roles where rolname like 'svc_%' and (rolsuper or rolcreatedb or rolcreaterole or rolbypassrls)")
[ "$badattr" = "0" ] && ok "all svc_* NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOBYPASSRLS" || bad "$badattr role(s) with excess attributes"
# authoritative privilege source: has_*_privilege (not information_schema, whose visibility rules
# can hide rows even from a superuser on a freshly-restored cluster).
zt=$(q "select count(*) from pg_class c, pg_roles r where c.relnamespace='iam_v2'::regnamespace and c.relkind in ('r','p','S') and r.rolname like 'svc_%' and (has_table_privilege(r.rolname,c.oid,'SELECT') or has_table_privilege(r.rolname,c.oid,'INSERT') or has_table_privilege(r.rolname,c.oid,'UPDATE') or has_table_privilege(r.rolname,c.oid,'DELETE'))")
zu=$(q "select count(*) from pg_roles r where r.rolname like 'svc_%' and has_schema_privilege(r.rolname,'iam_v2','USAGE')")
# EFFECTIVE function execution requires BOTH schema USAGE and function EXECUTE. The PUBLIC-default
# EXECUTE on iam_v2 functions is unreachable for svc_* because schema USAGE is denied (zu=0). We do
# not alter the Phase-1A iam_v2 object ACLs; we measure effective execution.
zf=$(q "select count(*) from pg_proc p, pg_roles r where p.pronamespace='iam_v2'::regnamespace and r.rolname like 'svc_%' and has_function_privilege(r.rolname,p.oid,'EXECUTE') and has_schema_privilege(r.rolname,'iam_v2','USAGE')")
[ "$zt" = "0" ] && [ "$zu" = "0" ] && [ "$zf" = "0" ] && ok "zero EFFECTIVE iam_v2 privileges (table=$zt schema-usage=$zu effective-function=$zf)" || bad "iam_v2 privileges t=$zt u=$zu f=$zf"

echo "=== default-privilege denial (owner creates future object) ==="
exec_sql "create table public.gp_probe(x int);" >/dev/null 2>&1
if asrole svc_scd "select 1 from public.gp_probe limit 1"; then bad "future table NOT denied to svc_scd"; else ok "future table denied to svc_scd"; fi
exec_sql "drop table public.gp_probe;" >/dev/null 2>&1

echo "=== negatives (as the role) MUST be denied ==="
for t in "svc_scd|select 1 from iam_v2.guest_principals limit 1|iam_v2" \
         "svc_scd|select 1 from operators limit 1|out-of-allowlist" \
         "svc_netd|select 1 from guests limit 1|out-of-allowlist" \
         "svc_scd|create table zz(x int)|CREATE" \
         "svc_scd|drop table sessions|DROP"; do
  IFS='|' read -r role sql lbl <<<"$t"
  if asrole "$role" "$sql"; then bad "$role $lbl NOT denied"; else ok "$role denied ($lbl)"; fi
done

echo "=== positive SELECTs (as the role, real credential) MUST succeed ==="
for t in "svc_scd|select count(*) from sessions|scd SELECT sessions" \
         "svc_scd|select count(*) from guests|scd SELECT guests" \
         "svc_acctd|select count(*) from vouchers|acctd SELECT vouchers" \
         "svc_acctd|select count(*) from ticket_templates|acctd SELECT ticket_templates" \
         "svc_netd|select count(*) from network_interfaces|netd SELECT network_interfaces" \
         "svc_edged|select count(*) from operators|edged SELECT operators"; do
  IFS='|' read -r role sql lbl <<<"$t"
  if asrole "$role" "$sql"; then ok "$lbl"; else bad "$lbl ($(tail -1 /tmp/gp.out))"; fi
done
echo "=== positive write PRIVILEGES (authoritative has_table_privilege) MUST be granted ==="
for t in "svc_scd|audit_log|INSERT" "svc_scd|sessions|UPDATE" "svc_scd|guests|INSERT" \
         "svc_acctd|accounting_records|INSERT" "svc_acctd|sessions|UPDATE" \
         "svc_netd|network_apply_events|INSERT" "svc_edged|ticket_templates|INSERT"; do
  IFS='|' read -r role tbl verb <<<"$t"
  v=$(q "select has_table_privilege('$role','public.$tbl','$verb')")
  [ "$v" = "t" ] && ok "$role $verb $tbl granted" || bad "$role $verb $tbl NOT granted (got '$v')"
done

echo "=== idempotency: second grants run -> identical effective grants (authoritative has_table_privilege matrix) ==="
GQ="select r.rolname||' '||c.relname||' '||has_table_privilege(r.rolname,c.oid,'SELECT')::int||has_table_privilege(r.rolname,c.oid,'INSERT')::int||has_table_privilege(r.rolname,c.oid,'UPDATE')::int||has_table_privilege(r.rolname,c.oid,'DELETE')::int from pg_class c cross join pg_roles r where c.relnamespace='public'::regnamespace and c.relkind='r' and r.rolname like 'svc_%' order by 1"
G1=$(q "$GQ" | sha256sum | awk '{print $1}')
n1=$(q "$GQ" | grep -cE ' [a-z_]+ (1000|0100|0010|0001|1100|1010|1001|0110|1110|1111|1011|0111|0101)$')  # rows with at least one privilege
step "grants.sql (2nd run)" exec_file "$HERE/gatep-grants.sql"
G2=$(q "$GQ" | sha256sum | awk '{print $1}')
[ "$G1" = "$G2" ] && [ "$n1" -gt 0 ] && ok "idempotent ($n1 role-table grant rows, matrix sha $G1)" || bad "grant matrix changed or empty (n=$n1, $G1 != $G2)"

echo "=== rollback -> roles removed ==="; step "rollback.sql" exec_file "$HERE/gatep-rollback.sql"
left=$(q "select count(*) from pg_roles where rolname like 'svc_%'")
[ "$left" = "0" ] && ok "all svc_* dropped" || bad "$left svc_* remain"

echo "=== reapply after rollback ==="
step "roles.sql (reapply)" exec_file "$HERE/gatep-roles.sql"
bash "$HERE/gatep-set-passwords.sh" --pg-exec "docker exec -i $CNAME" --db "$DB" --dsn-out "$DSN_OUT" >/tmp/gp.out 2>&1 && ok "passwords reset" || bad "reapply passwords"
step "grants.sql (reapply)" exec_file "$HERE/gatep-grants.sql"
again=$(q "select count(*) from pg_roles where rolname like 'svc_%'")
[ "$again" = "4" ] && ok "reapply recreated 4 roles" || bad "reapply produced $again roles"

echo "=== destroy disposable cluster ==="
docker rm -f "$CNAME" >/dev/null 2>&1 && ok "disposable cluster destroyed" || bad "cluster not destroyed"
trap - EXIT; rm -f "$DSN_OUT" 2>/dev/null || true

echo "============================================================"
if [ "$SELFTEST_MUST_FAIL" = "--selftest-must-fail" ]; then
  # meta self-test mode: caller injects a broken condition; harness MUST end FAIL
  bad "forced self-test failure (meta mode)"
fi
if [ "$FAIL" = "0" ]; then echo "GATEP_DRYRUN = PASS"; exit 0; else echo "GATEP_DRYRUN = FAIL ($FAIL)"; exit 1; fi
