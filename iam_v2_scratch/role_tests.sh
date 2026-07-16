#!/usr/bin/env bash
# Least-privilege / role-isolation tests (scratch, NOLOGIN roles via SET ROLE).
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
Q(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1" 2>&1; }
PASSN=0; FAILN=0
ok(){ echo "PASS  $1"; PASSN=$((PASSN+1)); }
no(){ echo "FAIL  $1 :: $2"; FAILN=$((FAILN+1)); }
efail(){ local l="$1" s="$2" sub="$3" o; o=$(Q "$s"); if [ $? -eq 0 ]; then no "$l" "expected denial, got success"; elif [[ "$o" == *"$sub"* ]]; then ok "$l"; else no "$l" "wrong error: ${o:0:120}"; fi; }
eeq(){ local l="$1" got="$2" exp="$3"; [ "$got" = "$exp" ] && ok "$l" || no "$l" "got '$got' want '$exp'"; }

echo "===== ROLE / LEAST-PRIVILEGE TESTS ====="
eeq   "ROLE-01 schema iam_v2 owned by iam_v2_owner (not superuser)" "$(Q "SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname='iam_v2';")" "iam_v2_owner"
eeq   "ROLE-02 all 49 iam_v2 tables owned by iam_v2_owner" "$(Q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relkind='r' AND c.relowner='iam_v2_owner'::regrole;")" "49"
eeq   "ROLE-03 zero iam_v2 tables owned by superuser" "$(Q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relkind='r' AND c.relowner='postgres'::regrole;")" "0"
eeq   "ROLE-04 iam_v2_migrator is a member of iam_v2_owner (can SET ROLE owner)" "$(Q "SELECT pg_has_role('iam_v2_migrator','iam_v2_owner','MEMBER')::text;")" "true"

# service roles: NO USAGE on schema, NO SELECT, NO write
for svc in iam_v2_svc_scd iam_v2_svc_edged iam_v2_svc_acctd iam_v2_svc_portald iam_v2_svc_hoteladm; do
  efail "ROLE-05 $svc SELECT denied"  "SET ROLE $svc; SELECT * FROM iam_v2.pms_interfaces LIMIT 1;" "permission denied"
  efail "ROLE-06 $svc INSERT denied"  "SET ROLE $svc; INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind) VALUES ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','x');" "permission denied"
done

# service roles hold NO table privileges at all in iam_v2 (introspection)
eeq   "ROLE-07 service roles have 0 privileges on iam_v2 tables" "$(Q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee ~ 'iam_v2_svc_';")" "0"
# PUBLIC has no privileges on iam_v2
eeq   "ROLE-08 PUBLIC has 0 privileges on iam_v2 tables" "$(Q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee='PUBLIC';")" "0"
efail "ROLE-09 PUBLIC (a no-priv role) SELECT denied" "SET ROLE iam_v2_svc_scd; SELECT * FROM iam_v2.entitlements LIMIT 1;" "permission denied"

# default privileges: a NEW table created by owner grants nothing to service roles
Q "SET ROLE iam_v2_owner; CREATE TABLE iam_v2._defpriv_probe(x int);" >/dev/null 2>&1
efail "ROLE-10 default-privileges: service role denied on a NEW owner table" "SET ROLE iam_v2_svc_edged; SELECT * FROM iam_v2._defpriv_probe;" "permission denied"
Q "DROP TABLE iam_v2._defpriv_probe;" >/dev/null 2>&1

# search_path must not include iam_v2 (no accidental routing)
sp="$(Q "SHOW search_path;")"; [[ "$sp" != *iam_v2* ]] && ok "ROLE-11 default search_path excludes iam_v2 ($sp)" || no "ROLE-11 search_path includes iam_v2" "$sp"

# owner CAN operate (positive control)
eeq   "ROLE-12 owner can read its own tables (positive control)" "$(Q "SET ROLE iam_v2_owner; SELECT (count(*)>=0)::text FROM iam_v2.pms_interfaces;")" "true"

echo "===== ROLE RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
