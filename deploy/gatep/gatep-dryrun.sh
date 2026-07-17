#!/usr/bin/env bash
# Gate P — end-to-end dry run of the EXACT committed scripts against a disposable database
# reconstructed from the verified production backup. Proves: role creation, secure SCRAM password
# setting (no cleartext), reconciler grants, role attributes, default-privilege denial, positive
# (as the role), negative, idempotency, rollback (clean removal), and reapply. No production mutation.
#
# Usage: gatep-dryrun.sh <backup_dump_path>
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
DUMP="${1:?need backup dump path}"
PGEXEC="docker exec -i stayconnect-pg"
PGX="docker exec stayconnect-pg"
DB="gatep_dryrun"
DSN_OUT="/dev/shm/gatep_dryrun_dsn.$$"; : > "$DSN_OUT"; chmod 600 "$DSN_OUT"
ROLES=(svc_scd svc_edged svc_acctd svc_netd)
FAIL=0; ok(){ echo "  ok: $*"; }; bad(){ echo "  *** FAIL: $*"; FAIL=$((FAIL+1)); }
sx(){ $PGX psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -Atc "$1" 2>&1; }
asrole(){ local role="$1" sql="$2"; local pw; pw=$(grep -oiE "://$role:[^@]+@" "$DSN_OUT" | sed -E "s#://$role:(.*)@#\1#"); $PGX env PGPASSWORD="$pw" psql -h 127.0.0.1 -U "$role" -d "$DB" -Atc "$sql" 2>&1; }
# cleanup: roles are CLUSTER-GLOBAL, so drop the disposable DB FIRST (removes all grants + default
# ACLs referencing the roles), THEN drop the roles from a surviving DB. Order matters — dropping a
# role while it still has grants in the disposable DB fails and leaks cluster-global roles.
cleanup(){ $PGX dropdb -U stayconnect --if-exists "$DB" >/dev/null 2>&1; for r in "${ROLES[@]}"; do $PGX psql -U stayconnect -d postgres -c "DROP ROLE IF EXISTS $r;" >/dev/null 2>&1; done; rm -f "$DSN_OUT"; }
trap cleanup EXIT

# start guard: never run if the real Gate-P svc_* roles already exist in the cluster (would clobber them)
pre=$($PGX psql -U stayconnect -d postgres -Atc "select count(*) from pg_roles where rolname like 'svc_%'")
[ "$pre" = "0" ] || { echo "REFUSE: svc_* roles already exist in the cluster ($pre) — dry run would clobber real Gate-P roles"; exit 4; }

echo "=== script checksums (exact committed) ==="
sha256sum "$HERE/gatep-roles.sql" "$HERE/gatep-grants.sql" "$HERE/gatep-rollback.sql" "$HERE/gatep-set-passwords.sh" "$HERE/scram_verifier.py"

echo "=== reconstruct disposable DB from verified backup ==="
$PGX dropdb -U stayconnect --if-exists "$DB" >/dev/null 2>&1
$PGX createdb -U stayconnect "$DB"
$PGX psql -U stayconnect -d "$DB" -c "CREATE EXTENSION IF NOT EXISTS timescaledb;" >/dev/null 2>&1
$PGX psql -U stayconnect -d "$DB" -c "SELECT timescaledb_pre_restore();" >/dev/null 2>&1
cat "$DUMP" | $PGEXEC pg_restore -U stayconnect --no-owner -d "$DB" >/dev/null 2>&1
$PGX psql -U stayconnect -d "$DB" -c "SELECT timescaledb_post_restore();" >/dev/null 2>&1
pub=$(sx "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE'")
iam=$(sx "select count(*) from information_schema.tables where table_schema='iam_v2'")
[ "$pub" = "42" ] && [ "$iam" = "49" ] && ok "reconstruction 42 public + 49 iam_v2" || bad "reconstruction $pub/$iam"
FP=$(sx "select string_agg(table_name||'.'||column_name||':'||data_type,',' order by table_name,ordinal_position) from information_schema.columns where table_schema='public'" | sha256sum | awk '{print $1}')
echo "  isolated public fingerprint: $FP"

echo "=== run EXACT gatep-roles.sql ==="
cat "$HERE/gatep-roles.sql" | $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -q 2>&1 | grep -iE "error" && bad "roles.sql errored" || ok "roles.sql applied"

echo "=== set passwords securely (SCRAM; no cleartext in SQL) ==="
bash "$HERE/gatep-set-passwords.sh" --pg-exec "$PGEXEC" --db "$DB" --dsn-out "$DSN_OUT" 2>&1 | sed -E 's#(://[^:]+:)[^@]+@#\1***@#g'
sx "select left(rolpassword,13) from pg_authid where rolname='svc_scd'" | grep -qi "SCRAM-SHA-256" && ok "svc_scd stored as SCRAM-SHA-256 (no cleartext)" || bad "svc_scd password not SCRAM"

echo "=== run EXACT gatep-grants.sql (reconciler) ==="
cat "$HERE/gatep-grants.sql" | $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -q 2>&1 | grep -iE "error|BLOCKER" && bad "grants.sql errored" || ok "grants.sql applied"

echo "=== role attributes + ZERO iam_v2 ==="
sx "select rolname||' super='||rolsuper||' cdb='||rolcreatedb||' crole='||rolcreaterole||' bypass='||rolbypassrls from pg_roles where rolname like 'svc_%' order by 1"
z=$(sx "select count(*) from information_schema.role_table_grants where table_schema='iam_v2' and grantee like 'svc_%'")
[ "$z" = "0" ] && ok "zero iam_v2 table grants" || bad "$z iam_v2 grants"
zc=$(sx "select count(*) from information_schema.role_table_grants where table_schema='iam_v2' and grantee like 'svc_%' union all select count(*) from information_schema.routine_privileges where specific_schema='iam_v2' and grantee like 'svc_%'" | paste -sd+ | bc)
[ "$zc" = "0" ] && ok "zero iam_v2 table+function grants" || bad "$zc iam_v2 table/function grants"

echo "=== default-privilege denial proof (owner creates future objects) ==="
sx "create table public.gatep_probe_t(x int); create sequence public.gatep_probe_s; create function public.gatep_probe_f() returns int language sql as 'select 1';" >/dev/null
d=$(asrole svc_scd "select 1 from public.gatep_probe_t limit 1"); echo "$d" | grep -qiE "permission denied" && ok "future table denied to svc_scd" || bad "future table NOT denied: $d"
sx "drop table public.gatep_probe_t; drop sequence public.gatep_probe_s; drop function public.gatep_probe_f();" >/dev/null

echo "=== NEGATIVE (must be denied), as the role ==="
for t in "svc_scd|select 1 from iam_v2.guest_principals limit 1|iam_v2" "svc_scd|select 1 from operators limit 1|out-of-allowlist" "svc_netd|select 1 from guests limit 1|out-of-allowlist" "svc_scd|create table zzz(x int)|CREATE" "svc_scd|drop table sessions|DROP"; do
  IFS='|' read -r role sql lbl <<<"$t"; out=$(asrole "$role" "$sql"); echo "$out" | grep -qiE "permission denied|must be owner" && ok "$role denied ($lbl)" || bad "$role NOT denied ($lbl): $out"
done

echo "=== POSITIVE (as the role) ==="
sel(){ local role="$1" sql="$2" lbl="$3"; out=$(asrole "$role" "$sql"); echo "$out" | grep -qiE "permission denied|ERROR" && bad "$lbl: $out" || ok "$lbl"; }
perm(){ local role="$1" sql="$2" lbl="$3"; out=$(asrole "$role" "begin; $sql; rollback"); echo "$out" | grep -qiE "permission denied" && bad "$lbl (perm denied): $out" || ok "$lbl (insert permission ok)"; }
sel  svc_scd   "select count(*) from sessions"           "svc_scd SELECT sessions"
sel  svc_scd   "select count(*) from guests"             "svc_scd SELECT guests"
perm svc_scd   "insert into audit_log default values"    "svc_scd INSERT audit_log"
sel  svc_acctd "select count(*) from vouchers"           "svc_acctd SELECT vouchers"
sel  svc_acctd "select count(*) from ticket_templates"   "svc_acctd SELECT ticket_templates"
perm svc_acctd "insert into accounting_records default values" "svc_acctd INSERT accounting_records"
sel  svc_netd  "select count(*) from network_interfaces" "svc_netd SELECT network_interfaces"
sel  svc_edged "select count(*) from operators"          "svc_edged SELECT operators"

echo "=== idempotency: second grants run -> identical effective grants ==="
G1=$(sx "select grantee||' '||table_name||' '||privilege_type from information_schema.role_table_grants where grantee like 'svc_%' order by 1,2,3" | sha256sum | awk '{print $1}')
cat "$HERE/gatep-grants.sql" | $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -q >/dev/null 2>&1
G2=$(sx "select grantee||' '||table_name||' '||privilege_type from information_schema.role_table_grants where grantee like 'svc_%' order by 1,2,3" | sha256sum | awk '{print $1}')
[ "$G1" = "$G2" ] && ok "idempotent (grant set sha $G1)" || bad "grant set changed on 2nd run ($G1 != $G2)"

echo "=== rollback (exact gatep-rollback.sql) -> roles removed ==="
cat "$HERE/gatep-rollback.sql" | $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -q 2>&1 | grep -iE "error|BLOCKER" && bad "rollback errored"
left=$(sx "select count(*) from pg_roles where rolname like 'svc_%'")
[ "$left" = "0" ] && ok "all svc_* roles dropped" || bad "$left svc_* roles remain after rollback"

echo "=== reapply after rollback ==="
cat "$HERE/gatep-roles.sql" | $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -q >/dev/null 2>&1
bash "$HERE/gatep-set-passwords.sh" --pg-exec "$PGEXEC" --db "$DB" --dsn-out "$DSN_OUT" >/dev/null 2>&1
cat "$HERE/gatep-grants.sql" | $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" -q 2>&1 | grep -iE "error|BLOCKER" && bad "reapply errored"
again=$(sx "select count(*) from pg_roles where rolname like 'svc_%'")
[ "$again" = "4" ] && ok "reapply recreated 4 roles" || bad "reapply produced $again roles"

echo "============================================================"
[ "$FAIL" = "0" ] && echo "GATEP_DRYRUN = PASS" || echo "GATEP_DRYRUN = FAIL ($FAIL)"
exit $FAIL
