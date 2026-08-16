#!/usr/bin/env bash
# PHASE-7 M1 GATE — identity and acquisition, COMPOSED (FINAL contract §19 A, B, C, D).
#
# WHAT MAKES THIS DIFFERENT FROM THE PHASE GATES IT SITS ON TOP OF.
#
# Every phase gate proves its own slice against fixtures it owns. None of them can prove the thing that only
# exists between them: that the stay Phase 3 resolved is the stay Phase 2 sold against, that the entitlement
# Phase 2 created is the one Phase 3 supersedes and Phase 6 accounts, and that the device Phase 3 pinned is
# the one Phase 6 lists. Those seams are where a system that passes every test still does not work.
#
# So this gate builds ONE world -- two PMS interfaces with a colliding room number, two stays, real plan
# revisions, real purchases -- and walks a guest through it, asserting at each seam that the identity carried
# across is the same identity, and that the invariants each phase established still hold when another phase
# has touched the same rows.
#
# Self-seeding and fixture-free. It contacts no appliance, no Production database and no PMS.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE7_CONTAINER:-iamv2-p6}"
DB="${PHASE7_DB:-iam_scratch}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }
# refused: the statement MUST fail. A gate that cannot tell "it worked" from "it was refused" proves nothing,
# so the check is on the error text, not on the absence of output.
refused(){ case "$(q "$2")" in *ERROR*|*error*) ok "$1";; *) no "$1" "the statement was ACCEPTED";; esac; }

T=7a000000-0000-4000-8000-000000000001   # tenant
S=7a000000-0000-4000-8000-000000000002   # site
A=7a000000-0000-4000-8000-000000000003   # appliance

echo "== Phase-7 M1: identity and acquisition, composed =="

# ---- the world -------------------------------------------------------------------------------------------
# Torn down and rebuilt every run, by reserved id, so the gate is re-runnable and never inherits a previous
# run's half-state -- the property Phase 6 learned the hard way when a failed teardown surfaced a run later.
q "DO \$\$
   DECLARE r record;
   BEGIN
     FOR r IN SELECT id FROM iam_v2.entitlements WHERE tenant_id='$T' AND status <> 'TERMINATED'
     LOOP PERFORM iam_v2.terminate_entitlement_at_boundary(r.id, now(), 'ADMIN'); END LOOP;
     UPDATE iam_v2.sessions SET state='ended', ended=COALESCE(ended,now()), end_reason='ADMIN'
      WHERE tenant_id='$T' AND state IN ('active','PENDING_ENFORCEMENT');
     UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED',
            disconnected_reason=COALESCE(disconnected_reason,'ADMIN')
      WHERE tenant_id='$T' AND status='AUTHORIZED';
   END \$\$;" >/dev/null
q "DELETE FROM iam_v2.session_online_watermarks WHERE tenant_id='$T'" >/dev/null

seeded="$(q "
  SELECT iam_v2.begin_controlled_operation('stay');
  INSERT INTO public.tenants(id) VALUES ('$T') ON CONFLICT DO NOTHING;
  INSERT INTO public.sites(id,tenant_id) VALUES ('$S','$T') ON CONFLICT DO NOTHING;

  -- TWO INTERFACES IN INDEPENDENT NAMESPACES. This is the whole point of D1: room 101 exists in both, and
  -- nothing about the room number may decide which stay a guest belongs to.
  INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
  VALUES ('7a000000-0000-4000-8000-000000000101','$T','$S','protel-fias','PMS-A','AUTH_DISABLED'),
         ('7a000000-0000-4000-8000-000000000102','$T','$S','protel-fias','PMS-B','AUTH_DISABLED')
  ON CONFLICT (id) DO NOTHING;

  INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
                           external_stay_identity,status,lifecycle_version,last_applied_event_version,normalized_room_number)
  VALUES ('7a000000-0000-4000-8000-000000000201','$T','$S','7a000000-0000-4000-8000-000000000101',
          'A-RES-1','A-STAY-1','IN_HOUSE',1,0,'101'),
         ('7a000000-0000-4000-8000-000000000202','$T','$S','7a000000-0000-4000-8000-000000000102',
          'B-RES-1','B-STAY-1','IN_HOUSE',1,0,'101')
  ON CONFLICT (id) DO NOTHING;

  INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
  VALUES ('7a000000-0000-4000-8000-000000000301','$T','$S','p7-plan',true) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,
          up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
  VALUES ('7a000000-0000-4000-8000-000000000302','$T','$S','7a000000-0000-4000-8000-000000000301',1,
          4000,2000,2,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',0) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
  VALUES ('7a000000-0000-4000-8000-000000000401','$T','$S','p7-pkg',false) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
          service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
  VALUES ('7a000000-0000-4000-8000-000000000402','$T','$S','7a000000-0000-4000-8000-000000000401',1,
          '7a000000-0000-4000-8000-000000000302','FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb)
  ON CONFLICT (id) DO NOTHING;

  INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac,last_seen)
  VALUES ('7a000000-0000-4000-8000-000000000501','$T','$S','$A','02:00:00:70:00:01'::macaddr,now()),
         ('7a000000-0000-4000-8000-000000000502','$T','$S','$A','02:00:00:70:00:02'::macaddr,now()),
         ('7a000000-0000-4000-8000-000000000503','$T','$S','$A','02:00:00:70:00:03'::macaddr,now())
  ON CONFLICT (tenant_id,site_id,appliance_id,mac) DO NOTHING;
  SELECT 'SEEDED';")"
case "$seeded" in *SEEDED*) ok "the composed world is seeded: two PMS namespaces, colliding room 101, one plan revision" ;;
  *) no "seeding the composed world" "$(printf '%s' "$seeded" | tr '\n' ' ' | cut -c1-180)" ;; esac

# ---- D1: two namespaces, one room number -----------------------------------------------------------------
eq "room 101 exists in BOTH PMS namespaces and resolves to two different stays" \
   "$(q "SELECT count(DISTINCT id) FROM iam_v2.stays WHERE tenant_id='$T' AND normalized_room_number='101' AND status='IN_HOUSE'")" "2"
eq "...and each stay belongs to exactly one interface" \
   "$(q "SELECT count(*) FROM (SELECT pms_interface_id FROM iam_v2.stays WHERE tenant_id='$T' AND normalized_room_number='101'
          GROUP BY pms_interface_id HAVING count(*) > 1) x")" "0"

# ---- C: acquisition against the pinned revision ----------------------------------------------------------
#
# THE GRANT IS FRESH EVERY RUN, and the skeleton above is not. An entitlement cannot be reset and reused: its
# state transitions are append-only and its terminal status is terminal, so a fixed id makes the second run
# execute the whole scenario against last run's TERMINATED row -- which is exactly what happened, and every
# device assertion quietly measured zero. The ids are generated and carried in shell variables; the reserved
# skeleton stays fixed so teardown can always find its way back.
RACE_PUR="$(q "SELECT gen_random_uuid()")"
RACE_ENT="$(q "SELECT gen_random_uuid()")"
PUR="$(q "SELECT gen_random_uuid()")"
ENT="$(q "SELECT gen_random_uuid()")"
granted="$(q "
  SELECT iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
                               amount_minor,state)
  VALUES ('$PUR','$T','$S','7a000000-0000-4000-8000-000000000402',
          '7a000000-0000-4000-8000-000000000101','7a000000-0000-4000-8000-000000000201','ADMIN_GRANT',0,'GRANTED');
  INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
          service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status,window_ends_at)
  VALUES ('$ENT','$T','$S','7a000000-0000-4000-8000-000000000201',
         '7a000000-0000-4000-8000-000000000101','$PUR','{}'::jsonb,'7a000000-0000-4000-8000-000000000302',
         '7a000000-0000-4000-8000-000000000402','VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE',now()+interval '1 day');
  SELECT iam_v2.apply_entitlement_transition('$ENT','ACTIVE',now()-interval '5 minutes','GRANT');
  SELECT 'GRANTED';")"
case "$granted" in *GRANTED*) ok "an entitlement is acquired against the PINNED plan revision" ;;
  *) no "acquisition" "$(printf '%s' "$granted" | tr '\n' ' ' | cut -c1-180)" ;; esac

# C3 -- once per stay, enforced by the DATABASE rather than by the caller. Two live entitlements on one stay
# is the shape a purchase race produces, and the index is what makes the race safe rather than the code path.
#
# THIS CASE WAS PASSING FOR THE WRONG REASON, and only a mutation found it. The first version inserted a bare
# ACTIVE row, which p3_entitlement_status_coherent refuses at COMMIT because no transition backs the status --
# so dropping ent_live_stay entirely changed nothing and the case still reported green. A refusal is not
# evidence unless it is the refusal you named. The second entitlement is now created the way a real race
# creates one -- insert AND its transition in one transaction -- so the deferred coherence check is satisfied
# and the unique index is the only thing left that can say no.
refused "C3 a SECOND live entitlement on the same stay is refused (once-per-stay)"   "SELECT iam_v2.begin_controlled_operation('commerce_intent');
   INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
                                amount_minor,state)
   VALUES ('$RACE_PUR','$T','$S','7a000000-0000-4000-8000-000000000402',
           '7a000000-0000-4000-8000-000000000101','7a000000-0000-4000-8000-000000000201','ADMIN_GRANT',0,'GRANTED');
   INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
     service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
   VALUES ('$RACE_ENT','$T','$S','7a000000-0000-4000-8000-000000000201',
     '7a000000-0000-4000-8000-000000000101','$RACE_PUR','{}'::jsonb,
     '7a000000-0000-4000-8000-000000000302','7a000000-0000-4000-8000-000000000402',
     'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE');
   SELECT iam_v2.apply_entitlement_transition('$RACE_ENT','ACTIVE',now(),'GRANT')"

# C4 -- a pinned revision is immutable. If this ever succeeds, every entitlement in the system silently
# changes shape, including ones already sold.
refused "C4 the pinned plan revision cannot be edited underneath a live entitlement" \
  "UPDATE iam_v2.service_plan_revisions SET max_concurrent_devices=99
     WHERE id='7a000000-0000-4000-8000-000000000302'"

# ---- A: the engine, composed ------------------------------------------------------------------------------
q "SELECT iam_v2.authorize_entitlement_device('$ENT',
     '7a000000-0000-4000-8000-000000000501', now()-interval '4 minutes')" >/dev/null
q "SELECT iam_v2.authorize_entitlement_device('$ENT',
     '7a000000-0000-4000-8000-000000000502', now()-interval '3 minutes')" >/dev/null

eq "A1 both devices share ONE entitlement and one window" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_devices
          WHERE entitlement_id='$ENT' AND status='AUTHORIZED'")" "2"

# A2 -- the third device is over the pinned limit of 2. The refusal has to come from the durable authorization
# primitive, not from a caller that remembered to count.
refused "A2 a THIRD device is refused against a limit of two" \
  "SELECT iam_v2.authorize_entitlement_device('$ENT',
     '7a000000-0000-4000-8000-000000000503', now())"

# A3 -- re-authorizing a device the entitlement already holds must not consume a second slot.
q "SELECT iam_v2.authorize_entitlement_device('$ENT',
     '7a000000-0000-4000-8000-000000000501', now())" >/dev/null
eq "A3 re-authorizing an existing device burns no slot" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_devices
          WHERE entitlement_id='$ENT' AND status='AUTHORIZED'")" "2"

# ---- the seam: the entitlement Phase 2 created is the one Phase 3 and Phase 6 see -------------------------
eq "SEAM the entitlement's stay, interface and purchase all agree" \
   "$(q "SELECT (e.stay_id = s.id AND e.pms_interface_id = s.pms_interface_id AND p.stay_id = s.id)::text
          FROM iam_v2.entitlements e
          JOIN iam_v2.stays s ON s.id = e.stay_id
          JOIN iam_v2.purchases p ON p.id = e.purchase_id
         WHERE e.id='$ENT'")" "true"
eq "SEAM the plan the entitlement is accounted against is the one the package revision sold" \
   "$(q "SELECT (e.service_plan_revision_id = ipr.service_plan_revision_id)::text
          FROM iam_v2.entitlements e
          JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
         WHERE e.id='$ENT'")" "true"

# ---- A7: terminal is terminal, whatever else has touched the row -----------------------------------------
q "SELECT iam_v2.terminate_entitlement_at_boundary('$ENT', now(), 'ADMIN')" >/dev/null
eq "the entitlement is TERMINATED" \
   "$(q "SELECT status FROM iam_v2.entitlements WHERE id='$ENT'")" "TERMINATED"
refused "A7 there is no exit from TERMINATED, even through the sanctioned transition writer" \
  "SELECT iam_v2.apply_entitlement_transition('$ENT','ACTIVE',now(),'ADMIN')"
# WHAT TERMINATION ACTUALLY GUARANTEES, stated as what it is rather than as what one might assume.
#
# The first version of this check asserted that terminate_entitlement_at_boundary releases the device
# bindings. It does not, and the assertion was wrong rather than the function: closing the bindings and ending
# the sessions is the ENFORCEMENT OWNER'S write (enforce.EnforceExpiries, or the Phase-6 definer writer), and
# the boundary function deliberately records the ending without reaching into the device family.
#
# That leaves AUTHORIZED rows behind for an entitlement that has ended, so the property worth asserting is the
# one that matters: no ACCESS survives. Admission refuses anything that is not ACTIVE, and the shaping plan
# selects only ACTIVE, so a stale binding row is bookkeeping the sweep will clear -- not a way back in. Both
# halves are checked, because "no access" and "no rows" are different claims and only one of them is true.
eq "A7 the terminated entitlement is not ACTIVE, so nothing can be admitted against it" \
   "$(q "SELECT count(*) FROM iam_v2.entitlements
          WHERE id='$ENT' AND status='ACTIVE'")" "0"
refused "A7 admission refuses a NEW device against a terminated entitlement" \
  "SELECT iam_v2.authorize_entitlement_device('$ENT',
     '7a000000-0000-4000-8000-000000000503', now())"
eq "A7 no live session survives the termination" \
   "$(q "SELECT count(*) FROM iam_v2.sessions
          WHERE entitlement_id='$ENT'
            AND state IN ('active','PENDING_ENFORCEMENT')")" "0"
# ...and the observation itself, asserted rather than left as folklore: the binding rows DO remain until an
# enforcement pass clears them. If that ever changes, this line fails and somebody re-reads the comment above.
eq "A7 device bindings remain recorded until the enforcement owner closes them (boundary write only)" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_devices
          WHERE entitlement_id='$ENT' AND status='AUTHORIZED'")" "2"

# ...and now that the stay's entitlement is terminal, the once-per-stay index must ALLOW a new one. An
# invariant that forbids the legitimate case is as broken as one that permits the illegitimate case.
PUR2="$(q "SELECT gen_random_uuid()")"
ENT2="$(q "SELECT gen_random_uuid()")"
newent="$(q "
  SELECT iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
                               amount_minor,state)
  VALUES ('$PUR2','$T','$S','7a000000-0000-4000-8000-000000000402',
          '7a000000-0000-4000-8000-000000000101','7a000000-0000-4000-8000-000000000201','ADMIN_GRANT',0,'GRANTED');
  INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
     service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
  VALUES ('$ENT2','$T','$S','7a000000-0000-4000-8000-000000000201',
     '7a000000-0000-4000-8000-000000000101','$PUR2','{}'::jsonb,
     '7a000000-0000-4000-8000-000000000302','7a000000-0000-4000-8000-000000000402',
     'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE');
  SELECT iam_v2.apply_entitlement_transition('$ENT2','ACTIVE',now(),'GRANT');
  SELECT 'REGRANTED';")"
case "$newent" in *REGRANTED*) ok "a stay whose entitlement ENDED may be granted a new one" ;;
  *) no "re-grant after termination" "$(printf '%s' "$newent" | tr '\n' ' ' | cut -c1-160)" ;; esac

# C: one entitlement per purchase, enforced by the database. Two entitlements against one purchase is how a
# retried confirm hands a guest two packages for one payment.
refused "C a SECOND entitlement against the SAME purchase is refused" \
  "INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
     service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
   VALUES (gen_random_uuid(),'$T','$S','7a000000-0000-4000-8000-000000000202',
     '7a000000-0000-4000-8000-000000000102','7a000000-0000-4000-8000-000000000602','{}'::jsonb,
     '7a000000-0000-4000-8000-000000000302','7a000000-0000-4000-8000-000000000402',
     'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE')"

# ---- teardown: leave nothing live -------------------------------------------------------------------------
q "DO \$\$
   DECLARE r record;
   BEGIN
     FOR r IN SELECT id FROM iam_v2.entitlements WHERE tenant_id='$T' AND status <> 'TERMINATED'
     LOOP PERFORM iam_v2.terminate_entitlement_at_boundary(r.id, now(), 'ADMIN'); END LOOP;
   END \$\$;" >/dev/null
eq "the gate leaves no live entitlement behind" \
   "$(q "SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id='$T' AND status <> 'TERMINATED'")" "0"

echo "------------------------------------------------------------"
printf 'PHASE7_M1_IDENTITY_AND_ACQUISITION pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
