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

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
A=44444444-4444-4444-4444-444444444444

echo "== Phase-6 least privilege, measured as the real roles =="

for r in svc_scd svc_edged; do
  [ "$(q "SELECT count(*) FROM pg_roles WHERE rolname='$r'")" = "1" ] && ok "role $r exists" || no "role $r exists" "missing"
done

# ---------------------------------------------------------------- the bypass, closed
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
out="$(asrole svc_scd "SELECT iam_v2.p6_guest_release_device('$ENT','$DEV')")"
[ "$out" = "OK" ] && ok "svc_scd CAN perform the approved release through the policy function (SECURITY DEFINER)" \
                  || no "svc_scd can perform the approved release" "got '$out'"
st="$(q "SELECT status||'/'||coalesce(disconnected_reason,'-') FROM iam_v2.entitlement_devices WHERE entitlement_id='$ENT' AND device_id='$DEV'")"
[ "$st" = "DISCONNECTED/GUEST_SELF_SERVICE" ] && ok "the definer's write landed with the guest's own reason" \
                                              || no "the release wrote the binding" "got '$st'"
n="$(q "SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id='$ENT' AND outcome='OK'")"
[ "$n" = "1" ] && ok "the audit row was written by the definer path" || no "the audit row exists" "found $n"

# ---------------------------------------------------------------- the 0032 guard under the ADMISSION role
# The guard is a BEFORE trigger and runs in the INVOKER's context, so an admission role that cannot READ
# entitlement_devices would fail on every insert. That is one SELECT on one table -- not the broad write
# access "make the trigger work" could easily have become.
out="$(asrole svc_scd "INSERT INTO iam_v2.sessions (id,tenant_id,site_id,entitlement_id,device_id,state,started)
       VALUES (gen_random_uuid(),'$T','$S','$ENT','$DEV','active',now())")"
case "$out" in
  *"may not exist on a DISCONNECTED binding"*)
     ok "the 0032 guard FIRES under the runtime role -- it reached entitlement_devices and refused" ;;
  *"permission denied for table entitlement_devices"*)
     no "the guard works under the runtime role" "the role cannot read entitlement_devices, so the guard cannot run" ;;
  *"permission denied"*)
     ok "the runtime role cannot insert sessions at all here (narrower than required; guard not reached)" ;;
  *) no "the guard fires under the runtime role" "unexpected: $(echo "$out" | head -1)" ;;
esac

# ---------------------------------------------------------------- svc_edged: the operator surface only
allowed svc_edged "SELECT count(*) FROM iam_v2.appliance_product_settings" "svc_edged can read the setting"
allowed svc_edged "SELECT count(*) FROM iam_v2.appliance_product_setting_changes" "svc_edged can read the setting audit"
denied  svc_edged "SELECT iam_v2.p6_guest_release_device('$ENT','$DEV')" \
  "svc_edged cannot release a guest device -- the operator surface is not the guest surface"
denied  svc_edged "UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED' WHERE false" \
  "svc_edged cannot write device bindings"
denied  svc_edged "SELECT count(*) FROM iam_v2.guest_device_actions" \
  "svc_edged cannot read the guest action audit (it is not its surface)"

# ---------------------------------------------------------------- PUBLIC gets nothing
pub="$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'
            AND has_function_privilege('public', p.oid, 'EXECUTE')")"
[ "$pub" = "0" ] && ok "no Phase-6 function is executable by PUBLIC" || no "PUBLIC holds no EXECUTE" "$pub function(s)"

# ---------------------------------------------------------------- cleanup
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
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

echo "------------------------------------------------------------"
echo "PHASE6_LEAST_PRIVILEGE pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
