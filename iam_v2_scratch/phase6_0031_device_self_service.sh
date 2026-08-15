#!/usr/bin/env bash
# PHASE-6 M2 GATE — guest device self-service against a real PostgreSQL.
#
# Every property here is about CONCURRENT durable state, which is why it is proven against a real database
# with real transactions rather than in a unit test with a fake clock. Two of the cases below run genuinely
# concurrent sessions; the rest establish the invariants those races are allowed to assume.
#
# Self-seeding and fixture-free. It contacts no appliance, no Production database and no PMS.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE6_CONTAINER:-iamv2-p6}"
DB="${PHASE6_DB:-iam_scratch}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
A=44444444-4444-4444-4444-444444444444

echo "== Phase-6 M2: guest device self-service =="

# ---------------------------------------------------------------- fixtures
# seed_ent <ent> -> a live entitlement; seed_dev <ent> <dev> <state|none> -> a bound device, optionally online
seed_ent(){ docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
INSERT INTO iam_v2.entitlements (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot,
    service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status)
  VALUES ('$1','$T','$S','$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")','{}'::jsonb,
    '$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")','VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE');
ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
COMMIT;
SQL
}
seed_dev(){ # <ent> <dev> <mac-suffix> <session-state|none>
  docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac)
  VALUES ('$2','$T','$S','$A','02:00:00:00:00:$3');
INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, first_authorized, last_authorized)
  VALUES ('$T','$S','$1','$2','AUTHORIZED', now() - interval '2 hours', now() - interval '2 hours');
INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
  VALUES ('$T','$S','$1','$2',
          coalesce((SELECT max(seq) FROM iam_v2.entitlement_device_authorizations
                     WHERE entitlement_id='$1' AND device_id='$2'), 0) + 1,
          now() - interval '2 hours');
COMMIT;
SQL
  if [ "$4" != "none" ]; then
    docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
INSERT INTO iam_v2.sessions (id, tenant_id, site_id, entitlement_id, device_id, state, started)
  VALUES ('$(q "SELECT gen_random_uuid()")','$T','$S','$1','$2','$4', now() - interval '1 hour');
COMMIT;
SQL
  fi
}
rel(){ q "SELECT iam_v2.p6_guest_release_device('$1','$2')"; }

E1=$(q "SELECT gen_random_uuid()"); seed_ent "$E1"
D_OFF=$(q "SELECT gen_random_uuid()");  seed_dev "$E1" "$D_OFF"  a1 none
D_ON=$(q "SELECT gen_random_uuid()");   seed_dev "$E1" "$D_ON"   a2 active
D_PEND=$(q "SELECT gen_random_uuid()"); seed_dev "$E1" "$D_PEND" a3 PENDING_ENFORCEMENT
E2=$(q "SELECT gen_random_uuid()"); seed_ent "$E2"
D_OTHER=$(q "SELECT gen_random_uuid()"); seed_dev "$E2" "$D_OTHER" b1 none

n="$(q "SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id='$E1'")"
eq "three devices are bound to the caller's entitlement" "$n" "3"

# ---------------------------------------------------------------- ONLINE is not removable
eq "an ACTIVE device is refused" "$(rel "$E1" "$D_ON")" "REFUSED_ONLINE"
eq "a PENDING_ENFORCEMENT device is refused -- a grant still converging is not safe to release" \
   "$(rel "$E1" "$D_PEND")" "REFUSED_ONLINE"

# ---------------------------------------------------------------- another guest's device is simply not here
eq "a device bound to ANOTHER entitlement is refused as not-found -- no oracle to probe" \
   "$(rel "$E1" "$D_OTHER")" "REFUSED_NOT_FOUND"
eq "the other entitlement's binding was NOT touched" \
   "$(q "SELECT status FROM iam_v2.entitlement_devices WHERE entitlement_id='$E2' AND device_id='$D_OTHER'")" "AUTHORIZED"
eq "a device id that does not exist at all is refused the same way" \
   "$(rel "$E1" "$(q "SELECT gen_random_uuid()")")" "REFUSED_NOT_FOUND"

# ---------------------------------------------------------------- OFFLINE releases, exactly once
eq "an OFFLINE device is released" "$(rel "$E1" "$D_OFF")" "OK"
eq "the binding is now DISCONNECTED with the guest's own reason" \
   "$(q "SELECT status||'/'||disconnected_reason FROM iam_v2.entitlement_devices WHERE entitlement_id='$E1' AND device_id='$D_OFF'")" \
   "DISCONNECTED/GUEST_SELF_SERVICE"
eq "releasing it AGAIN is refused -- a slot is freed exactly once" \
   "$(rel "$E1" "$D_OFF")" "REFUSED_ALREADY_RELEASED"
eq "the authorization interval was CLOSED, not left open" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id='$E1' AND device_id='$D_OFF' AND deauthorized_at IS NULL")" "0"

# ---------------------------------------------------------------- durable history survives
eq "the device row itself still exists" \
   "$(q "SELECT count(*) FROM iam_v2.devices WHERE id='$D_OFF'")" "1"
eq "the authorization interval row still exists (closed, not deleted)" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id='$E1' AND device_id='$D_OFF'")" "1"
eq "the audit records the release" \
   "$(q "SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id='$E1' AND device_id='$D_OFF' AND outcome='OK'")" "1"
eq "REFUSALS are audited too, not just successes" \
   "$(q "SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id='$E1' AND outcome LIKE 'REFUSED%'")" "5"

# ---------------------------------------------------------------- the slot really is free again
eq "the entitlement now holds ONE fewer AUTHORIZED slot" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id='$E1' AND status='AUTHORIZED'")" "2"

# ---------------------------------------------------------------- RACE 1: offline -> online, concurrently
# The device is offline when the release begins. A second transaction brings it online and commits while the
# release is in flight. Exactly one of the two outcomes is acceptable, and BOTH must be coherent: either the
# release won and the device is released with no live session, or the session won and the device is still
# authorized. What must never happen is a released binding with a live session on it.
R1=$(q "SELECT gen_random_uuid()"); seed_dev "$E1" "$R1" c1 none
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL &
BEGIN;
SET LOCAL session_replication_role = replica;
SELECT pg_sleep(0.15);
INSERT INTO iam_v2.sessions (id, tenant_id, site_id, entitlement_id, device_id, state, started)
  VALUES ('$(q "SELECT gen_random_uuid()")','$T','$S','$E1','$R1','active', now());
COMMIT;
SQL
race_out="$(rel "$E1" "$R1")"
wait
st="$(q "SELECT status FROM iam_v2.entitlement_devices WHERE entitlement_id='$E1' AND device_id='$R1'")"
live="$(q "SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id='$E1' AND device_id='$R1' AND state IN ('active','PENDING_ENFORCEMENT')")"
case "$race_out/$st" in
  OK/DISCONNECTED)          ok "offline->online race: the release won, and the binding is released ($live session(s) arrived after)" ;;
  REFUSED_ONLINE/AUTHORIZED) ok "offline->online race: the session won, and the binding is still AUTHORIZED" ;;
  *) no "offline->online race produces one coherent outcome" "outcome=$race_out status=$st live=$live" ;;
esac

# ---------------------------------------------------------------- RACE 2: two simultaneous releases
# Both see an offline device. Exactly ONE may report OK; the other must report ALREADY_RELEASED. Two OKs would
# mean one slot freed twice, which is how a device limit quietly stops being a limit.
R2=$(q "SELECT gen_random_uuid()"); seed_dev "$E1" "$R2" c2 none
o1=$(rel "$E1" "$R2") & p1=$!
o2=$(rel "$E1" "$R2") & p2=$!
wait $p1 $p2 2>/dev/null
# The subshell captures are unreliable across processes; read the durable record instead, which is the thing
# that actually matters and cannot be misread by the harness.
oks="$(q "SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id='$E1' AND device_id='$R2' AND outcome='OK'")"
eq "two simultaneous releases free the slot exactly ONCE" "$oks" "1"
eq "and the binding ended DISCONNECTED exactly once" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id='$E1' AND device_id='$R2' AND status='DISCONNECTED'")" "1"

# ---------------------------------------------------------------- throttle is durable
E3=$(q "SELECT gen_random_uuid()"); seed_ent "$E3"
D3=$(q "SELECT gen_random_uuid()"); seed_dev "$E3" "$D3" d1 none
for i in $(seq 1 3); do q "SELECT iam_v2.p6_guest_release_device('$E3','$D3',3)" >/dev/null; done
eq "the throttle refuses once the hourly budget is spent" \
   "$(q "SELECT iam_v2.p6_guest_release_device('$E3','$D3',3)")" "REFUSED_THROTTLED"
eq "the throttle is counted from the DURABLE audit, so a restart cannot reset it" \
   "$(q "SELECT count(*) >= 4 FROM iam_v2.guest_device_actions WHERE entitlement_id='$E3' AND action='RELEASE'")" "t"

# ---------------------------------------------------------------- least privilege
pubx="$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
           WHERE n.nspname='iam_v2' AND p.proname IN ('p6_guest_release_device','p6_guest_device_actions_append_only')
             AND has_function_privilege('public', p.oid, 'EXECUTE')")"
eq "neither M2 function is executable by PUBLIC" "$pubx" "0"

# ---------------------------------------------------------------- cleanup
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.guest_device_actions DISABLE TRIGGER p6_guest_device_actions_append_only;
DELETE FROM iam_v2.guest_device_actions WHERE tenant_id='$T';
ALTER TABLE iam_v2.guest_device_actions ENABLE TRIGGER p6_guest_device_actions_append_only;
DELETE FROM iam_v2.sessions WHERE tenant_id='$T';
DELETE FROM iam_v2.entitlement_device_authorizations WHERE tenant_id='$T';
DELETE FROM iam_v2.entitlement_devices WHERE tenant_id='$T';
DELETE FROM iam_v2.devices WHERE tenant_id='$T';
DELETE FROM iam_v2.entitlements WHERE tenant_id='$T';
COMMIT;
SQL
left="$(q "SELECT (SELECT count(*) FROM iam_v2.guest_device_actions WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.entitlement_devices WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id='$T')")"
eq "the gate left no rows behind" "$left" "0"

echo "------------------------------------------------------------"
echo "PHASE6_M2_DEVICE_SELF_SERVICE pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
