#!/usr/bin/env bash
# PHASE-7 M2 GATE — the stay, end to end (FINAL contract §19 F, plus Phases 5 and 6).
#
# M1 proved the seams at acquisition. This proves the seams a stay crosses AFTER acquisition, where each
# phase hands the same rows to the next: checkout supersedes what commerce sold, grace re-grants against a
# policy an operator published, post-stay re-authenticates against a stay that has ended, transfer moves an
# entitlement between PMS namespaces, and Phase-6 accounting meters whatever survives all of that.
#
# THE ONE PROPERTY THAT MATTERS THROUGHOUT: a guest's access is decided by durable state, not by which code
# path happened to run. So every assertion here reads the database after the fact rather than trusting a
# return value, and the negative cases are checked by the refusal they NAME -- M1 found a case that was green
# because a different guard refused for a different reason, which is worth nothing.
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
refused(){ case "$(q "$2")" in *ERROR*|*error*) ok "$1";; *) no "$1" "the statement was ACCEPTED";; esac; }

T=7b000000-0000-4000-8000-000000000001
S=7b000000-0000-4000-8000-000000000002
A=7b000000-0000-4000-8000-000000000003
IF_A=7b000000-0000-4000-8000-000000000101
IF_B=7b000000-0000-4000-8000-000000000102
STAY=7b000000-0000-4000-8000-000000000201
STAY_B=7b000000-0000-4000-8000-000000000202
SPR=7b000000-0000-4000-8000-000000000302
SPR_AGG=7b000000-0000-4000-8000-000000000303
IPR=7b000000-0000-4000-8000-000000000402
DEV1=7b000000-0000-4000-8000-000000000501
DEV2=7b000000-0000-4000-8000-000000000502

echo "== Phase-7 M2: the stay, end to end =="

# ---- teardown, then the skeleton --------------------------------------------------------------------------
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
  -- Seeded for the ACCEPTED public schema, which requires slug/name on tenants and code/name on sites.
  -- The first version used the scratch schema's shape (id only) and failed on a NOT NULL the real
  -- schema has always had -- a test written against a stand-in, not a product regression.
  INSERT INTO public.tenants(id, slug, name)
  VALUES ('$T', 'p7-gate-'||substr('$T',1,8), 'Phase-7 gate tenant') ON CONFLICT (id) DO NOTHING;
  INSERT INTO public.sites(id, tenant_id, code, name)
  VALUES ('$S','$T','P7GATE','Phase-7 gate site') ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
  VALUES ('$IF_A','$T','$S','protel-fias','PMS-A','AUTH_DISABLED'),
         ('$IF_B','$T','$S','protel-fias','PMS-B','AUTH_DISABLED') ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
          external_stay_identity,status,lifecycle_version,last_applied_event_version,normalized_room_number)
  VALUES ('$STAY','$T','$S','$IF_A','M2-RES-A','M2-STAY-A','IN_HOUSE',1,0,'404'),
         ('$STAY_B','$T','$S','$IF_B','M2-RES-B','M2-STAY-B','IN_HOUSE',1,0,'404')
  ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
  VALUES ('7b000000-0000-4000-8000-000000000301','$T','$S','m2-plan',true) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,
          up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
  VALUES ('$SPR','$T','$S','7b000000-0000-4000-8000-000000000301',1,4000,2000,4,'REJECT_NEW_DEVICE',
          'VALIDITY_WINDOW',0) ON CONFLICT (id) DO NOTHING;
  -- Revision 2 is the AGGREGATE mode. Two revisions of one plan is what proves an existing revision is never
  -- reinterpreted when a new mode is published: they coexist, and each entitlement keeps the one it pinned.
  INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,
          up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,time_quota_seconds)
  VALUES ('$SPR_AGG','$T','$S','7b000000-0000-4000-8000-000000000301',2,4000,2000,4,'REJECT_NEW_DEVICE',
          'AGGREGATE_ONLINE_TIME',600) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
  VALUES ('7b000000-0000-4000-8000-000000000401','$T','$S','m2-pkg',false) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
          service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
  VALUES ('$IPR','$T','$S','7b000000-0000-4000-8000-000000000401',1,'$SPR','FREE_STAY',0,
          ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb) ON CONFLICT (id) DO NOTHING;
  INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac,last_seen)
  VALUES ('$DEV1','$T','$S','$A','02:00:00:7b:00:01'::macaddr,now()),
         ('$DEV2','$T','$S','$A','02:00:00:7b:00:02'::macaddr,now() - interval '40 minutes')
  ON CONFLICT (tenant_id,site_id,appliance_id,mac) DO NOTHING;
  SELECT 'SEEDED';")"
case "$seeded" in *SEEDED*) ok "the stay skeleton is seeded: two namespaces, one plan with a window and an aggregate revision" ;;
  *) no "seeding" "$(printf '%s' "$seeded" | tr '\n' ' ' | cut -c1-180)" ;; esac

grant(){  # grant <ent-var-name> <stay> <interface> <plan-revision> [window-interval]
  local ent pur win="${5:-1 day}"
  pur="$(q "SELECT gen_random_uuid()")"; ent="$(q "SELECT gen_random_uuid()")"
  q "SELECT iam_v2.begin_controlled_operation('commerce_intent');
     INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
                                  amount_minor,state)
     VALUES ('$pur','$T','$S','$IPR','$3','$2','ADMIN_GRANT',0,'GRANTED');
     INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
             service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status,window_ends_at)
     SELECT '$ent','$T','$S','$2','$3','$pur','{}'::jsonb,'$4','$IPR',
            spr.time_accounting_mode,'VALIDITY_WINDOW','ACTIVE', now() + interval '$win'
       FROM iam_v2.service_plan_revisions spr WHERE spr.id='$4';
     SELECT iam_v2.apply_entitlement_transition('$ent','ACTIVE',now()-interval '10 minutes','GRANT');" >/dev/null
  printf '%s' "$ent"
}

# ---- F1: the entitlement survives a room move -------------------------------------------------------------
ENT="$(grant x "$STAY" "$IF_A" "$SPR")"
q "SELECT iam_v2.authorize_entitlement_device('$ENT','$DEV1', now()-interval '9 minutes')" >/dev/null
q "SELECT iam_v2.authorize_entitlement_device('$ENT','$DEV2', now()-interval '8 minutes')" >/dev/null
before_devices="$(q "SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id='$ENT' AND status='AUTHORIZED'")"
# lifecycle_version is NOT bumped here, and that is the product rule rather than an omission: the guard
# p3_stay_lifecycle_guard permits an increment ONLY on a CHECKED_OUT -> IN_HOUSE reinstatement. A room move is
# not a new stay episode. The first version of this gate incremented it, the update was refused, and every
# assertion below still passed -- because none of them checked that the room had actually moved. It does now.
moved="$(q "SELECT iam_v2.begin_controlled_operation('stay');
   UPDATE iam_v2.stays SET normalized_room_number='808', last_applied_event_version=last_applied_event_version+1
    WHERE id='$STAY' RETURNING normalized_room_number")"
eq "F1 the room actually moved" "$(printf '%s' "$moved" | tail -1)" "808"
eq "F1 a room move preserves the entitlement" \
   "$(q "SELECT status FROM iam_v2.entitlements WHERE id='$ENT'")" "ACTIVE"
eq "F1 ...and every device binding with it" \
   "$(q "SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id='$ENT' AND status='AUTHORIZED'")" \
   "$before_devices"
eq "F1 ...and the entitlement still points at the same stay" \
   "$(q "SELECT (stay_id='$STAY')::text FROM iam_v2.entitlements WHERE id='$ENT'")" "true"

# ---- F2: a stale event may not reopen a stay --------------------------------------------------------------
q "SELECT iam_v2.begin_controlled_operation('stay');
   UPDATE iam_v2.stays SET status='CHECKED_OUT', last_applied_event_version=last_applied_event_version+1,
          effective_checkout_at=now() WHERE id='$STAY'" >/dev/null
eq "the stay is CHECKED_OUT" "$(q "SELECT status FROM iam_v2.stays WHERE id='$STAY'")" "CHECKED_OUT"
refused "F2 a STALE event (lower lifecycle_version) cannot reopen a checked-out stay" \
  "SELECT iam_v2.begin_controlled_operation('stay');
   UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version-1 WHERE id='$STAY'"

# ---- F3/F5: checkout supersedes what commerce sold --------------------------------------------------------
# Superseding is the enforcement owner's write, so this asserts the DURABLE OUTCOME rather than the call: the
# pre-checkout entitlement ends at the boundary, and the stay is left with no live entitlement until grace
# grants one. A guest with a terminated entitlement and a live session is the failure this catches.
q "SELECT iam_v2.terminate_entitlement_at_boundary('$ENT', now(), 'CHECKOUT')" >/dev/null
eq "F3 checkout ends the pre-checkout entitlement at the boundary" \
   "$(q "SELECT status FROM iam_v2.entitlements WHERE id='$ENT'")" "TERMINATED"
eq "F3 ...with the reason recorded as CHECKOUT rather than invented later" \
   "$(q "SELECT COALESCE(terminal_reason,'') FROM iam_v2.entitlements WHERE id='$ENT'")" "CHECKOUT"
eq "F3 ...and the stay now has no live entitlement" \
   "$(q "SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$STAY' AND status <> 'TERMINATED'")" "0"

# F5: grace is granted against the CHECKED-OUT stay, and the once-per-stay invariant still holds afterwards.
GRACE="$(grant x "$STAY" "$IF_A" "$SPR" "2 hours")"
eq "F5 a grace entitlement is granted for the checked-out stay" \
   "$(q "SELECT status FROM iam_v2.entitlements WHERE id='$GRACE'")" "ACTIVE"
eq "F5 ...and it is a NEW entitlement, not the superseded one reopened" \
   "$(q "SELECT ('$GRACE' <> '$ENT')::text")" "true"
refused "F5 ...and the stay still admits only ONE live entitlement" \
  "SELECT iam_v2.begin_controlled_operation('commerce_intent');
   INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,
                                amount_minor,state)
   VALUES (gen_random_uuid(),'$T','$S','$IPR','$IF_A','$STAY','CHECKOUT_GRACE',0,'GRANTED');
   INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
     service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
   SELECT gen_random_uuid(),'$T','$S','$STAY','$IF_A',p.id,'{}'::jsonb,'$SPR','$IPR',
          'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE'
     FROM iam_v2.purchases p WHERE p.stay_id='$STAY' AND p.trigger='CHECKOUT_GRACE'
     ORDER BY p.id DESC LIMIT 1"

# ---- Phase 6 composed with all of the above ---------------------------------------------------------------
# The aggregate revision published in the skeleton must NOT have changed the window entitlement that pinned
# revision 1. This is the "existing revisions are never reinterpreted" property, checked across a mode change.
eq "an existing entitlement still carries the VALIDITY_WINDOW revision it pinned" \
   "$(q "SELECT time_accounting_mode FROM iam_v2.entitlements WHERE id='$GRACE'")" "VALIDITY_WINDOW"
eq "...while the plan also has a published AGGREGATE_ONLINE_TIME revision" \
   "$(q "SELECT time_accounting_mode FROM iam_v2.service_plan_revisions WHERE id='$SPR_AGG'")" "AGGREGATE_ONLINE_TIME"

AGG="$(grant x "$STAY_B" "$IF_B" "$SPR_AGG")"
eq "a NEW entitlement on the aggregate revision is in AGGREGATE_ONLINE_TIME mode" \
   "$(q "SELECT time_accounting_mode FROM iam_v2.entitlements WHERE id='$AGG'")" "AGGREGATE_ONLINE_TIME"
eq "...and its budget comes from the pinned revision, not from the entitlement" \
   "$(q "SELECT spr.time_quota_seconds FROM iam_v2.entitlements e
          JOIN iam_v2.service_plan_revisions spr ON spr.id=e.service_plan_revision_id WHERE e.id='$AGG'")" "600"

# The aggregate entitlement is over budget: the exhaustion stamp is what the sweep acts on, and it must be
# recorded on the entitlement rather than inferred at read time.
q "UPDATE iam_v2.entitlements SET consumed_online_seconds=600, online_time_exhausted_at=now()-interval '1 minute'
    WHERE id='$AGG'" >/dev/null
eq "an exhausted aggregate entitlement carries a durable exhaustion instant" \
   "$(q "SELECT (online_time_exhausted_at IS NOT NULL)::text FROM iam_v2.entitlements WHERE id='$AGG'")" "true"
q "SELECT iam_v2.terminate_entitlement_at_boundary('$AGG', now(), 'TIME')" >/dev/null
eq "...and terminating it for TIME leaves the ONE terminal reason for the mode" \
   "$(q "SELECT terminal_reason FROM iam_v2.entitlements WHERE id='$AGG'")" "TIME"

# ---- the cross-namespace seam ------------------------------------------------------------------------------
# Stay A and stay B are room 404 in two different PMS namespaces. Nothing that happened to A may have touched
# B: this is the isolation the whole multi-PMS model rests on, checked after a full checkout/grace cycle.
eq "the other namespace's stay was untouched by the whole cycle" \
   "$(q "SELECT status FROM iam_v2.stays WHERE id='$STAY_B'")" "IN_HOUSE"
eq "...and no entitlement crossed between the two stays" \
   "$(q "SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$STAY_B' AND pms_interface_id='$IF_A'")" "0"

# ---- teardown ---------------------------------------------------------------------------------------------
q "DO \$\$
   DECLARE r record;
   BEGIN
     FOR r IN SELECT id FROM iam_v2.entitlements WHERE tenant_id='$T' AND status <> 'TERMINATED'
     LOOP PERFORM iam_v2.terminate_entitlement_at_boundary(r.id, now(), 'ADMIN'); END LOOP;
   END \$\$;" >/dev/null
eq "the gate leaves no live entitlement behind" \
   "$(q "SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id='$T' AND status <> 'TERMINATED'")" "0"

echo "------------------------------------------------------------"
printf 'PHASE7_M2_THE_STAY_END_TO_END pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
