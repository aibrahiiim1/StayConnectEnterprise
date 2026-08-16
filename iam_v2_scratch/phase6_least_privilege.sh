#!/usr/bin/env bash
# PHASE-6 LEAST-PRIVILEGE GATE — measured AS THE REAL SERVICE ROLES, not read off a grant file.
#
# A grant file is a claim. This gate SETs ROLE to the actual runtime roles and tries the things they must and
# must not be able to do, which is the only way to find out whether a privilege boundary is where the
# migration says it is. Phase 4 established the technique; this applies it to the Phase-6 surface.
#
# THE PARTICULAR TRAP IT WATCHES FOR: p6_guest_release_device calls deauthorize_entitlement_device, and the
# lazy way to satisfy that dependency is to grant the guest-facing role EXECUTE on the primitive. That would
# hand it a release with no throttle, no ownership scope, no offline check and no audit -- a complete bypass
# around the policy, invisible in the calling code. The gate proves the role cannot reach it.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE6_CONTAINER:-iamv2-p6}"
DB="${PHASE6_DB:-iam_scratch}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
# asrole <role> <sql> -- run one statement AS that role and return its output/error
asrole(){ docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc "SET ROLE $1; $2" 2>&1; }
denied(){ local out; out="$(asrole "$1" "$2")"
  case "$out" in *"permission denied"*) ok "$3";; *) no "$3" "not denied: $(echo "$out" | head -1)";; esac; }
allowed(){ local out; out="$(asrole "$1" "$2")"
  case "$out" in *"permission denied"*) no "$3" "denied: $(echo "$out" | head -1)";; *) ok "$3";; esac; }
# eqv <label> <got> <want> -- for catalog readings, where the answer is a value rather than a refusal.
eqv(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
A=44444444-4444-4444-4444-444444444444

echo "== Phase-6 least privilege, measured as the real roles =="

for r in svc_scd svc_edged; do
  [ "$(q "SELECT count(*) FROM pg_roles WHERE rolname='$r'")" = "1" ] && ok "role $r exists" || no "role $r exists" "missing"
done

# ---------------------------------------------------------------- the bypass, closed
# The parameterized primitive must be out of reach: a role that can choose its own hourly limit can pass
# 2147483647 and bypass the throttle while still calling an approved function.
denied svc_scd "SELECT iam_v2.p6_guest_release_device('$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")', 2147483647)" \
  "svc_scd CANNOT call the parameterized release -- the throttle is not caller-selectable"
denied svc_scd "SELECT iam_v2.deauthorize_entitlement_device('$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")',now(),'X')" \
  "svc_scd CANNOT execute deauthorize_entitlement_device -- the policy bypass is closed"
denied svc_scd "SELECT iam_v2.authorize_entitlement_device('$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")',now())" \
  "svc_scd cannot execute authorize_entitlement_device directly"
denied svc_scd "UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED' WHERE false" \
  "svc_scd cannot write entitlement_devices directly"
denied svc_scd "UPDATE iam_v2.entitlement_device_authorizations SET deauthorized_at=now() WHERE false" \
  "svc_scd cannot close an authorization interval directly"
denied svc_scd "UPDATE iam_v2.appliance_product_settings SET guest_device_self_service=true WHERE false" \
  "svc_scd cannot flip the product setting -- that is the operator surface"

# ---------------------------------------------------------------- what it MUST be able to do
allowed svc_scd "SELECT count(*) FROM iam_v2.entitlement_devices" "svc_scd can read device bindings (the listing)"
allowed svc_scd "SELECT count(*) FROM iam_v2.devices"             "svc_scd can read devices"
allowed svc_scd "SELECT count(*) FROM iam_v2.sessions"            "svc_scd can read sessions (online state)"
allowed svc_scd "SELECT count(*) FROM iam_v2.appliance_product_settings" "svc_scd can read the per-appliance setting"

# ---------------------------------------------------------------- the release works AS svc_scd, end to end
ENT=$(q "SELECT gen_random_uuid()"); DEV=$(q "SELECT gen_random_uuid()")
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
INSERT INTO iam_v2.entitlements (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot,
    service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status)
  VALUES ('$ENT','$T','$S', gen_random_uuid(), gen_random_uuid(), '{}'::jsonb, gen_random_uuid(),
          gen_random_uuid(),'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE');
ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac)
  VALUES ('$DEV','$T','$S','$A', ('0a:' || substr(replace('$DEV','-',''),1,2) || ':' ||
          substr(replace('$DEV','-',''),3,2) || ':' || substr(replace('$DEV','-',''),5,2) || ':' ||
          substr(replace('$DEV','-',''),7,2) || ':' || substr(replace('$DEV','-',''),9,2))::macaddr);
INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, first_authorized, last_authorized)
  VALUES ('$T','$S','$ENT','$DEV','AUTHORIZED', now(), now());
INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
  VALUES ('$T','$S','$ENT','$DEV',1, now());
COMMIT;
SQL
out="$(asrole svc_scd "SELECT iam_v2.p6_guest_release_device_policy('$ENT','$DEV')")"
[ "$out" = "OK" ] && ok "svc_scd CAN perform the approved release through the POLICY function (SECURITY DEFINER)" \
                  || no "svc_scd can perform the approved release" "got '$out'"
st="$(q "SELECT status||'/'||coalesce(disconnected_reason,'-') FROM iam_v2.entitlement_devices WHERE entitlement_id='$ENT' AND device_id='$DEV'")"
[ "$st" = "DISCONNECTED/GUEST_SELF_SERVICE" ] && ok "the definer's write landed with the guest's own reason" \
                                              || no "the release wrote the binding" "got '$st'"
n="$(q "SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id='$ENT' AND outcome='OK'")"
[ "$n" = "1" ] && ok "the audit row was written by the definer path" || no "the audit row exists" "found $n"

# ---------------------------------------------------------------- the 0032 guard under the ADMISSION role
# svc_scd's session INSERT is a REQUIRED POSITIVE CAPABILITY: the real admission path opens the session
# itself. The previous version of this gate passed when the INSERT was permission-denied before the guard was
# even reached -- a missing privilege reported as a passing security property. Both halves are asserted now,
# and the positive one FAILS the gate if it is missing.
GENT=$(q "SELECT gen_random_uuid()"); GDEV=$(q "SELECT gen_random_uuid()")
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
INSERT INTO iam_v2.entitlements (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot,
    service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status)
  VALUES ('$GENT','$T','$S', gen_random_uuid(), gen_random_uuid(), '{}'::jsonb, gen_random_uuid(),
          gen_random_uuid(),'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE');
ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac)
  VALUES ('$GDEV','$T','$S','$A', ('0e:' || substr(replace('$GDEV','-',''),1,2) || ':' ||
          substr(replace('$GDEV','-',''),3,2) || ':' || substr(replace('$GDEV','-',''),5,2) || ':' ||
          substr(replace('$GDEV','-',''),7,2) || ':' || substr(replace('$GDEV','-',''),9,2))::macaddr);
INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, first_authorized, last_authorized)
  VALUES ('$T','$S','$GENT','$GDEV','AUTHORIZED', now(), now());
COMMIT;
SQL

# (a) REQUIRED POSITIVE: a PENDING_ENFORCEMENT session on an AUTHORIZED binding must SUCCEED as svc_scd.
out="$(asrole svc_scd "INSERT INTO iam_v2.sessions (id,tenant_id,site_id,entitlement_id,device_id,state,started)
       VALUES (gen_random_uuid(),'$T','$S','$GENT','$GDEV','PENDING_ENFORCEMENT',now())")"
case "$out" in
  *"permission denied"*) no "svc_scd CAN admit a PENDING_ENFORCEMENT session on an AUTHORIZED binding" \
                            "REQUIRED capability missing: $(echo "$out" | head -1)" ;;
  *ERROR*)               no "svc_scd CAN admit a PENDING_ENFORCEMENT session on an AUTHORIZED binding" \
                            "$(echo "$out" | head -1)" ;;
  *) ok "svc_scd CAN admit a PENDING_ENFORCEMENT session on an AUTHORIZED binding (required capability present)" ;;
esac

# (b) the SAME insert on a DISCONNECTED binding must reach the 0032 guard and be refused BY THE GUARD.
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 -c \
  "UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED', disconnected_reason='GUEST_SELF_SERVICE'
    WHERE entitlement_id='$GENT' AND device_id='$GDEV'"
out="$(asrole svc_scd "INSERT INTO iam_v2.sessions (id,tenant_id,site_id,entitlement_id,device_id,state,started)
       VALUES (gen_random_uuid(),'$T','$S','$GENT','$GDEV','PENDING_ENFORCEMENT',now())")"
case "$out" in
  *"may not exist on a DISCONNECTED binding"*)
     ok "the 0032 guard REFUSES the same insert on a DISCONNECTED binding, under the real role" ;;
  *"permission denied"*)
     no "the guard refuses the insert on a DISCONNECTED binding" "denied by privilege, so the guard never ran" ;;
  *) no "the guard refuses the insert on a DISCONNECTED binding" "unexpected: $(echo "$out" | head -1)" ;;
esac

# (c) UPDATE of session state stays denied: promoting to active is the enforcement owner's write.
denied svc_scd "UPDATE iam_v2.sessions SET state='active' WHERE entitlement_id='$GENT'" \
  "svc_scd cannot UPDATE session state -- promoting to active is the enforcement owner's write"

docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
DELETE FROM iam_v2.sessions WHERE entitlement_id='$GENT';
DELETE FROM iam_v2.entitlement_devices WHERE entitlement_id='$GENT';
DELETE FROM iam_v2.devices WHERE id='$GDEV';
DELETE FROM iam_v2.entitlements WHERE id='$GENT';
COMMIT;
SQL

# ---------------------------------------------------------------- svc_edged: the operator surface only
allowed svc_edged "SELECT count(*) FROM iam_v2.appliance_product_settings" "svc_edged can read the setting"
denied  svc_edged "SELECT iam_v2.p6_guest_release_device('$ENT','$DEV')" \
  "svc_edged cannot release a guest device -- the operator surface is not the guest surface"
denied  svc_edged "UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED' WHERE false" \
  "svc_edged cannot write device bindings"
denied  svc_edged "SELECT count(*) FROM iam_v2.guest_device_actions" \
  "svc_edged cannot read the guest action audit (it is not its surface)"

# THE UNAUDITED-WRITE BYPASS. The Go service always wrote the setting and its audit together, but the ROLE
# could write either alone -- so the audit was mandatory by convention and optional by privilege. An operator
# could have flipped a guest-facing capability with no trace.
denied svc_edged "UPDATE iam_v2.appliance_product_settings SET guest_device_self_service=true WHERE false" \
  "svc_edged CANNOT write the setting directly -- no unaudited path exists"
denied svc_edged "INSERT INTO iam_v2.appliance_product_settings (tenant_id,site_id,appliance_id) VALUES ('$T','$S','$A')" \
  "svc_edged cannot INSERT a setting row directly"
denied svc_edged "INSERT INTO iam_v2.appliance_product_setting_changes
       (tenant_id,site_id,appliance_id,setting_key,new_value,changed_by_operator_id,changed_by)
       VALUES ('$T','$S','$A','guest_device_self_service',true,'55555555-5555-5555-5555-555555555555','x')" \
  "svc_edged cannot forge an audit row directly"

# ...and the controlled operation works, writing BOTH halves.
out="$(asrole svc_edged "SELECT iam_v2.p6_set_guest_device_self_service('$T','$S','$A', true,
        '55555555-5555-5555-5555-555555555555','Fixture Operator','least-privilege gate')")"
case "$out" in
  *"permission denied"*) no "svc_edged CAN change the setting through the controlled operation" "denied: $out" ;;
  *) ok "svc_edged CAN change the setting through the controlled operation" ;;
esac
v="$(q "SELECT guest_device_self_service FROM iam_v2.appliance_product_settings WHERE appliance_id='$A'")"
[ "$v" = "t" ] && ok "the setting moved" || no "the setting moved" "got '$v'"
n="$(q "SELECT count(*) FROM iam_v2.appliance_product_setting_changes WHERE appliance_id='$A' AND new_value=true")"
[ "$n" = "1" ] && ok "and its audit row was written in the SAME operation -- mandatory by privilege, not by convention" \
               || no "the audit row accompanied the change" "found $n"

# ---------------------------------------------------------------- no speculative grants survive
# Audited against what the implemented routes actually query. The audit rows are written by the definer
# functions under the definer's rights, so the guest surface needs no access to that table at all.
denied svc_scd "SELECT count(*) FROM iam_v2.guest_device_actions" \
  "svc_scd holds NO grant on guest_device_actions -- the definer writes it, not the caller"
denied svc_scd "SELECT count(*) FROM iam_v2.entitlement_device_authorizations" \
  "svc_scd holds no grant on the authorization intervals -- the listing never reads them"
denied svc_edged "SELECT count(*) FROM iam_v2.appliance_product_setting_changes" \
  "svc_edged holds no direct read on the setting audit -- no implemented route needs it"

# ---------------------------------------------------------------- PUBLIC gets nothing
pub="$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'
            AND has_function_privilege('public', p.oid, 'EXECUTE')")"
[ "$pub" = "0" ] && ok "no Phase-6 function is executable by PUBLIC" || no "PUBLIC holds no EXECUTE" "$pub function(s)"

# ---------------------------------------------------------------- cleanup
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.appliance_product_setting_changes DISABLE TRIGGER p6_setting_changes_append_only;
DELETE FROM iam_v2.appliance_product_setting_changes WHERE appliance_id='$A';
ALTER TABLE iam_v2.appliance_product_setting_changes ENABLE TRIGGER p6_setting_changes_append_only;
DELETE FROM iam_v2.appliance_product_settings WHERE appliance_id='$A';
ALTER TABLE iam_v2.guest_device_actions DISABLE TRIGGER p6_guest_device_actions_append_only;
DELETE FROM iam_v2.guest_device_actions WHERE entitlement_id='$ENT';
ALTER TABLE iam_v2.guest_device_actions ENABLE TRIGGER p6_guest_device_actions_append_only;
DELETE FROM iam_v2.sessions WHERE entitlement_id='$ENT';
DELETE FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id='$ENT';
DELETE FROM iam_v2.entitlement_devices WHERE entitlement_id='$ENT';
DELETE FROM iam_v2.devices WHERE id='$DEV';
DELETE FROM iam_v2.entitlements WHERE id='$ENT';
COMMIT;
SQL
[ "$(q "SELECT count(*) FROM iam_v2.entitlements WHERE id='$ENT'")" = "0" ] && ok "the gate left no rows behind" \
  || no "the gate cleaned up" "rows remain"


# ---- svc_acctd: the aggregate tick, and nothing else ------------------------------------------------------
#
# The accounting daemon needs to CALL one definer function. Everything that function writes -- consumption,
# the crossing instant, the watermark, the skipped-interval evidence -- it writes as its owner, which is the
# whole point of the boundary: the caller gets one audited operation instead of the authority to reproduce it.
eqv "svc_acctd can execute the aggregate tick"    "$(q "SELECT has_function_privilege('svc_acctd','iam_v2.p6_tick_online_time(uuid,uuid,timestamptz,int,uuid[],timestamptz[])','EXECUTE')")" "t"
eqv "PUBLIC cannot"    "$(q "SELECT has_function_privilege('public','iam_v2.p6_tick_online_time(uuid,uuid,timestamptz,int,uuid[],timestamptz[])','EXECUTE')")" "f"
eqv "svc_acctd holds NO write anywhere in iam_v2"    "$(q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee='svc_acctd' AND privilege_type <> 'SELECT'")" "0"
eqv "svc_acctd cannot write consumption directly"    "$(q "SELECT has_table_privilege('svc_acctd','iam_v2.entitlements','UPDATE')")" "f"
eqv "svc_acctd cannot move a watermark directly"    "$(q "SELECT has_table_privilege('svc_acctd','iam_v2.session_online_watermarks','UPDATE')")" "f"
eqv "svc_acctd cannot write skipped-interval evidence directly"    "$(q "SELECT has_table_privilege('svc_acctd','iam_v2.online_time_skipped_intervals','INSERT')")" "f"
for fn in authorize_entitlement_device deauthorize_entitlement_device p6_record_time_termination           p6_guest_release_device p6_set_guest_device_self_service terminate_entitlement_at_boundary           begin_controlled_operation p6_due_terminal; do
  eqv "svc_acctd cannot execute $fn (another boundary owns it)"      "$(q "SELECT bool_or(has_function_privilege('svc_acctd', p.oid, 'EXECUTE')) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='$fn'")" "f"
done
# The sanctioned expiry writer, which takes ONLY an entitlement id and establishes the terminal condition
# from authoritative state. It is the single write capability this role has.
eqv "svc_acctd can execute the sanctioned expiry writer"    "$(q "SELECT has_function_privilege('svc_acctd','iam_v2.p6_expire_entitlement(uuid)','EXECUTE')")" "t"
eqv "PUBLIC cannot execute the expiry writer"    "$(q "SELECT has_function_privilege('public','iam_v2.p6_expire_entitlement(uuid)','EXECUTE')")" "f"
eqv "the old caller-supplied-reason writer is gone"    "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_expire_entitlement' AND pg_get_function_arguments(p.oid) <> 'p_entitlement uuid'")" "0"

# The fail-closed suspension writer: acctd may call it, and may not change entitlement status any other way.
eqv "svc_acctd can execute the over-budget suspension writer" \
   "$(q "SELECT has_function_privilege('svc_acctd','iam_v2.p6_suspend_over_budget(uuid,uuid)','EXECUTE')")" "t"
eqv "PUBLIC cannot execute the suspension writer" \
   "$(q "SELECT has_function_privilege('public','iam_v2.p6_suspend_over_budget(uuid,uuid)','EXECUTE')")" "f"
eqv "svc_acctd cannot change entitlement status directly" \
   "$(q "SELECT has_function_privilege('svc_acctd','iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)','EXECUTE')")" "f"

# THE GUEST SURFACE MUST BE ABLE TO RESOLVE A DEVICE, AND NOTHING MORE (0047). Found on the appliance under
# the real role: SELECT alone made every request fail on its first line with "permission denied for table
# devices", because resolution is an upsert.
eqv "svc_scd can insert a device"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.devices','INSERT')")" "t"
eqv "svc_scd can advance last_seen"    "$(q "SELECT has_column_privilege('svc_scd','iam_v2.devices','last_seen','UPDATE')")" "t"
eqv "svc_scd CANNOT delete a device"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.devices','DELETE')")" "f"
eqv "svc_scd CANNOT move a device between tenants"    "$(q "SELECT has_column_privilege('svc_scd','iam_v2.devices','tenant_id','UPDATE')")" "f"
eqv "svc_scd CANNOT move a device between appliances"    "$(q "SELECT has_column_privilege('svc_scd','iam_v2.devices','appliance_id','UPDATE')")" "f"
eqv "svc_scd can read entitlements"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.entitlements','SELECT')")" "t"
eqv "svc_scd CANNOT write an entitlement"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.entitlements','UPDATE')")" "f"
eqv "svc_scd CANNOT rewrite a plan revision"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.service_plan_revisions','UPDATE')")" "f"

# ...and the required POSITIVE privileges, because a gate that only asserts absences passes for a role that
# cannot do its job either.
for tbl in entitlements service_plan_revisions sessions session_online_watermarks; do
  eqv "svc_acctd can read iam_v2.$tbl" "$(q "SELECT has_table_privilege('svc_acctd','iam_v2.$tbl','SELECT')")" "t"
done

echo "------------------------------------------------------------"
echo "PHASE6_LEAST_PRIVILEGE pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
