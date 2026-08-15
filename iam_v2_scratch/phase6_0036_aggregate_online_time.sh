#!/usr/bin/env bash
# PHASE-6 M3 GATE — AGGREGATE_ONLINE_TIME accrual against a real PostgreSQL.
#
# The plan names the adversarial matrix this has to survive BEFORE the mode is wired to anything, because
# every case in it is a way of charging a guest for time they were not online, and none of them is visible in
# a unit test with a fake clock:
#
#   nothing charged while PENDING_ENFORCEMENT · nothing charged before enforcement became active · nothing
#   charged when enforcement never succeeds · nothing charged after a session ends · replay charges zero ·
#   a gap far longer than the charge bound is skipped, not charged · a watermark may not move backwards ·
#   a terminal entitlement is never reopened · shared device-minutes · exhaustion at the TRUE instant.
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

echo "== Phase-6 M3: AGGREGATE_ONLINE_TIME accrual =="

# Fixtures from a previous run are RETIRED first. This is not tidiness: the tick processes a whole site in
# ONE transaction, so a single leftover entitlement that is not coherent with its own state history aborts
# every accrual in the site -- and the symptom is "consumed=0" on assertions that have nothing to do with the
# leftover. (In production that incoherence cannot arise: the coherence trigger refuses the write that would
# create it. Only a fixture built with triggers disabled can.)
#
# They are TERMINATED rather than deleted, because entitlements are not deletable by design, and terminated
# ones are exactly what the tick skips.
retire=$(q "
DO \$\$
DECLARE r record;
BEGIN
  ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
  ALTER TABLE iam_v2.entitlement_state_transitions DISABLE TRIGGER ALL;
  FOR r IN SELECT e.id FROM iam_v2.entitlements e
             JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
            WHERE spr.name IN ('m3-gate','m3-vw') AND e.status <> 'TERMINATED'
  LOOP
    UPDATE iam_v2.entitlements
       SET status='TERMINATED', terminal_reason='OTHER', terminated_at=now() WHERE id = r.id;
    INSERT INTO iam_v2.entitlement_state_transitions
      (id, tenant_id, site_id, entitlement_id, seq, from_state, to_state, effective_at, reason)
    SELECT gen_random_uuid(), e.tenant_id, e.site_id, e.id,
           COALESCE((SELECT max(seq) FROM iam_v2.entitlement_state_transitions t WHERE t.entitlement_id=e.id),0)+1,
           NULL, 'TERMINATED', now(), 'GATE_FIXTURE_RETIRED'
      FROM iam_v2.entitlements e WHERE e.id = r.id;
  END LOOP;
  ALTER TABLE iam_v2.entitlement_state_transitions ENABLE TRIGGER ALL;
  ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
END \$\$;")
case "$retire" in *ERROR*) echo "  [WARN] could not retire previous fixtures :: $retire";; esac

# ---------------------------------------------------------------------------------------------------------
# seed: one aggregate entitlement with a budget, plus devices. Triggers are disabled ONLY for the fixture
# rows themselves; every assertion below runs against the real function with everything armed.
# ---------------------------------------------------------------------------------------------------------
seed_ent(){ # $1 budget seconds -> entitlement id
  q "
  DO \$\$
  DECLARE v_spr uuid := gen_random_uuid(); v_ent uuid := gen_random_uuid();
  BEGIN
    ALTER TABLE iam_v2.service_plan_revisions DISABLE TRIGGER ALL;
    INSERT INTO iam_v2.service_plan_revisions
      (id, tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps,
       max_concurrent_devices, device_limit_policy, idle_timeout_seconds, time_accounting_mode,
       time_quota_seconds)
    VALUES (v_spr,'$T','$S', gen_random_uuid(), 1, 'm3-gate', 1000, 1000, 5, 'REJECT_NEW_DEVICE', 900,
            'AGGREGATE_ONLINE_TIME', $1);
    ALTER TABLE iam_v2.service_plan_revisions ENABLE TRIGGER ALL;
    ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
    INSERT INTO iam_v2.entitlements
      (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot, service_plan_revision_id,
       package_revision_id, time_accounting_mode, end_mode, status, consumed_online_seconds)
    VALUES (v_ent,'$T','$S', gen_random_uuid(), gen_random_uuid(), '{}'::jsonb, v_spr, gen_random_uuid(),
            'AGGREGATE_ONLINE_TIME','VALIDITY_WINDOW','ACTIVE', 0);
    ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
    -- THE ENTITLEMENT MUST BE COHERENT WITH ITS OWN HISTORY. iam_v2 refuses any later write to an
    -- entitlement whose status is not backed by its latest state transition, and a fixture without one is
    -- not a state the product can produce -- the first version of this gate had that gap and every accrual
    -- assertion failed for a reason that had nothing to do with accrual.
    ALTER TABLE iam_v2.entitlement_state_transitions DISABLE TRIGGER ALL;
    INSERT INTO iam_v2.entitlement_state_transitions
      (id, tenant_id, site_id, entitlement_id, seq, from_state, to_state, effective_at, reason)
    VALUES (gen_random_uuid(),'$T','$S', v_ent, 1, NULL, 'ACTIVE', now(), 'GATE_FIXTURE');
    ALTER TABLE iam_v2.entitlement_state_transitions ENABLE TRIGGER ALL;
    PERFORM set_config('m3.ent', v_ent::text, false);
  END \$\$;
  SELECT current_setting('m3.ent');"
}

# seed_session <ent> <state> <started-ago> [observed-since-ago] -> session id
#
# The optional fourth argument seeds a WATERMARK that old, which is the only honest way to give a test
# genuinely observed time: a watermark cannot be moved backwards afterwards (the monotonic trigger refuses
# it, and case 6 proves that), and a test cannot wait ten real minutes.
seed_session(){
  q "
  DO \$\$
  DECLARE v_dev uuid := gen_random_uuid(); v_ses uuid := gen_random_uuid();
  BEGIN
    ALTER TABLE iam_v2.sessions DISABLE TRIGGER ALL;
    INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac, last_seen)
      VALUES (v_dev,'$T','$S','$A', ('02:00:' || substr(replace(v_dev::text,'-',''),1,2) || ':' ||
              substr(replace(v_dev::text,'-',''),3,2) || ':' || substr(replace(v_dev::text,'-',''),5,2) || ':' ||
              substr(replace(v_dev::text,'-',''),7,2))::macaddr, now());
    INSERT INTO iam_v2.entitlement_devices
      (tenant_id, site_id, entitlement_id, device_id, status, first_authorized, last_authorized)
      VALUES ('$T','$S','$1', v_dev, 'AUTHORIZED', now(), now());
    INSERT INTO iam_v2.sessions (id, tenant_id, site_id, entitlement_id, device_id, state, started)
      VALUES (v_ses,'$T','$S','$1', v_dev, '$2', now() - interval '$3');
    IF '${4:-}' <> '' THEN
      INSERT INTO iam_v2.session_online_watermarks
        (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
      VALUES ('$T','$S', v_ses, now() - interval '${4:-0 seconds}', 0);
    END IF;
    ALTER TABLE iam_v2.sessions ENABLE TRIGGER ALL;
    PERFORM set_config('m3.ses', v_ses::text, false);
  END \$\$;
  SELECT current_setting('m3.ses');"
}

# tick runs one accrual and FAILS LOUDLY. The first version of this gate sent its output to /dev/null, so a
# tick that errored on every call looked exactly like a tick that charged nothing -- twelve assertions failed
# with "consumed=0" and none of them said why.
tick(){
  local out; out="$(q "SELECT count(*) FROM iam_v2.p6_tick_online_time('$T','$S', now(), ${1:-600})")"
  case "$out" in *ERROR*) no "the accrual tick itself failed" "$out";; esac
  printf '%s' "$out"
}
consumed(){ q "SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id='$1'"; }

# ---- 1. PENDING_ENFORCEMENT never consumes --------------------------------------------------------------
E1=$(seed_ent 3600); SP=$(seed_session "$E1" 'PENDING_ENFORCEMENT' '30 minutes')
tick >/dev/null
eq "a PENDING_ENFORCEMENT session consumes nothing" "$(consumed "$E1")" "0"
eq "...and gets no watermark at all" "$(q "SELECT count(*) FROM iam_v2.session_online_watermarks WHERE session_id='$SP'")" "0"

# ...and the interval BEFORE it became active is never charged: promoting it now must charge from now, not
# from when the guest was told to wait.
q "UPDATE iam_v2.sessions SET state='active' WHERE id='$SP'" >/dev/null
tick >/dev/null
c=$(consumed "$E1")
[ "$c" -le 2 ] && ok "promotion to active charges from promotion, not from session start ($c s)" \
  || no "promotion to active charged the pre-enforcement interval" "consumed=$c"

# ---- 2. a session that never got enforced, then ended ---------------------------------------------------
E2=$(seed_ent 3600); SN=$(seed_session "$E2" 'PENDING_ENFORCEMENT' '45 minutes')
q "UPDATE iam_v2.sessions SET state='ended', ended=now() WHERE id='$SN'" >/dev/null
tick >/dev/null
eq "a session that ended without ever being enforced consumes nothing" "$(consumed "$E2")" "0"

# ---- 3. the ordinary case, and replay ------------------------------------------------------------------
# The first tick only BASELINES (see the first-observation rule): nothing has been observed yet.
E3F=$(seed_ent 3600); S3F=$(seed_session "$E3F" 'active' '10 minutes')
tick 3600 >/dev/null
eq "the first observation of a session charges nothing" "$(consumed "$E3F")" "0"

# ...and a session that HAS been observed for ten minutes accrues exactly those ten minutes.
E3=$(seed_ent 3600); S3=$(seed_session "$E3" 'active' '10 minutes' '10 minutes')
tick 3600 >/dev/null
c1=$(consumed "$E3")
[ "$c1" -ge 599 ] && [ "$c1" -le 601 ] && ok "a continuously active session accrues its elapsed time ($c1 s)" \
  || no "ordinary accrual is wrong" "consumed=$c1"
tick 3600 >/dev/null; tick 3600 >/dev/null
c2=$(consumed "$E3")
[ $((c2 - c1)) -le 2 ] && ok "two replayed ticks charge (nearly) nothing more ($((c2-c1)) s)" \
  || no "a replayed tick double-charged" "before=$c1 after=$c2"

# ---- 4. the charge bound: a long unobserved gap is skipped, not charged ---------------------------------
E4=$(seed_ent 86400); S4=$(seed_session "$E4" 'active' '6 hours' '6 hours')
tick 300 >/dev/null
c=$(consumed "$E4")
eq "a six-hour unobserved gap charges exactly the bound" "$c" "300"
eq "...and the remainder is RECORDED as skipped, not silently dropped" \
   "$(q "SELECT count(*) FROM iam_v2.online_time_skipped_intervals WHERE session_id='$S4' AND cause='UNOBSERVED_GAP'")" "1"
sk=$(q "SELECT round(extract(epoch from (skipped_to - skipped_from))) FROM iam_v2.online_time_skipped_intervals WHERE session_id='$S4'")
[ "$sk" -ge 21000 ] && ok "the skipped interval covers the unobserved remainder (${sk}s)" \
  || no "the skipped interval is wrong" "seconds=$sk"
# the watermark moved past the skipped interval, so the next tick does not charge it after all
tick 300 >/dev/null
c2=$(consumed "$E4")
[ $((c2 - c)) -le 2 ] && ok "the skipped interval is never charged by a later tick" \
  || no "a later tick charged the skipped interval" "delta=$((c2-c))"

# ---- 5. nothing is charged after a session ends ---------------------------------------------------------
E5=$(seed_ent 3600); S5=$(seed_session "$E5" 'active' '5 minutes' '5 minutes')
tick 3600 >/dev/null; c1=$(consumed "$E5")
q "UPDATE iam_v2.sessions SET state='ended', ended=now() - interval '1 minute' WHERE id='$S5'" >/dev/null
tick 3600 >/dev/null; tick 3600 >/dev/null
eq "time after a session ended is never charged" "$(consumed "$E5")" "$c1"

# ---- 6. the watermark may not move backwards -------------------------------------------------------------
back=$(q "UPDATE iam_v2.session_online_watermarks SET accounted_through = accounted_through - interval '1 hour' WHERE session_id='$S3'")
case "$back" in *ERROR*) ok "a watermark cannot be moved backwards (restore-from-backup case)";;
  *) no "a watermark moved backwards" "$back";; esac

# ---- 7. SHARED DEVICE-MINUTES: two devices online for the same minute consume two -------------------------
E7=$(seed_ent 3600)
A7=$(seed_session "$E7" 'active' '10 minutes' '10 minutes'); B7=$(seed_session "$E7" 'active' '10 minutes' '10 minutes')
tick 3600 >/dev/null
c=$(consumed "$E7")
[ "$c" -ge 1198 ] && [ "$c" -le 1202 ] && ok "two devices online for ten minutes consume twenty aggregate minutes ($c s)" \
  || no "shared device-minute semantics are wrong" "consumed=$c (want ~1200)"

# ---- 8. exhaustion: reported once, at the TRUE instant, capped, never reopened ----------------------------
E8=$(seed_ent 60); S8=$(seed_session "$E8" 'active' '10 minutes' '10 minutes')
rows=$(q "SELECT count(*) FROM iam_v2.p6_tick_online_time('$T','$S', now(), 3600) WHERE entitlement_id='$E8'")
eq "an exhausted entitlement is reported exactly once" "$rows" "1"
eq "consumption is CAPPED at the budget, so remaining time is never negative" "$(consumed "$E8")" "60"
# the crossing instant is the moment the budget ran out -- one session, so 60s after the session's baseline,
# which is ~9 minutes ago, NOT the moment the sweep ran.
# A LATER TICK MUST REPORT IT AGAIN -- an entitlement over budget that is still live has to be terminated,
# and a sweep that reported the crossing once would leave a crashed caller's entitlement running forever.
# What it must NOT do is re-date it, or charge anything more.
stamp=$(q "SELECT online_time_exhausted_at FROM iam_v2.entitlements WHERE id='$E8'")
again=$(q "SELECT exhausted_at FROM iam_v2.p6_tick_online_time('$T','$S', now(), 3600) WHERE entitlement_id='$E8'")
eq "a re-reported exhaustion carries the ORIGINAL instant, not the retry's clock" "$again" "$stamp"
eq "...and charges nothing more" "$(consumed "$E8")" "60"
# re-derive the instant from a fresh fixture, where the tick that crosses is the FIRST one
E9=$(seed_ent 60); S9=$(seed_session "$E9" 'active' '10 minutes' '10 minutes')
age=$(q "SELECT round(extract(epoch from (now() - exhausted_at))) FROM iam_v2.p6_tick_online_time('$T','$S', now(), 3600) WHERE entitlement_id='$E9'")
[ -n "$age" ] && [ "$age" -ge 480 ] && ok "exhaustion is dated when the budget ran out, ~${age}s ago, not when the sweep looked" \
  || no "the crossing instant was stamped at sweep time" "age=${age:-none}"

# ---- 9. a terminal entitlement is never touched again -----------------------------------------------------
q "SELECT iam_v2.terminate_entitlement_at_boundary('$E9', now(), 'TIME')" >/dev/null
before=$(consumed "$E9"); tick 3600 >/dev/null
eq "a late tick may not charge a terminated entitlement" "$(consumed "$E9")" "$before"

# ---- 10. VALIDITY_WINDOW entitlements are untouched by this function --------------------------------------
EV=$(q "
DO \$\$
DECLARE v_spr uuid := gen_random_uuid(); v_ent uuid := gen_random_uuid();
BEGIN
  ALTER TABLE iam_v2.service_plan_revisions DISABLE TRIGGER ALL;
  INSERT INTO iam_v2.service_plan_revisions
    (id, tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps, max_concurrent_devices,
     device_limit_policy, idle_timeout_seconds, time_accounting_mode, time_quota_seconds)
  VALUES (v_spr,'$T','$S', gen_random_uuid(), 1, 'm3-vw', 1000, 1000, 5, 'REJECT_NEW_DEVICE', 900,
          'VALIDITY_WINDOW', 60);
  ALTER TABLE iam_v2.service_plan_revisions ENABLE TRIGGER ALL;
  ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
  INSERT INTO iam_v2.entitlements
    (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot, service_plan_revision_id,
     package_revision_id, time_accounting_mode, end_mode, status, consumed_online_seconds)
  VALUES (v_ent,'$T','$S', gen_random_uuid(), gen_random_uuid(), '{}'::jsonb, v_spr, gen_random_uuid(),
          'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE', 0);
  ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
  PERFORM set_config('m3.ent', v_ent::text, false);
END \$\$;
SELECT current_setting('m3.ent');")
SV=$(seed_session "$EV" 'active' '30 minutes')
tick 3600 >/dev/null
eq "a VALIDITY_WINDOW entitlement is not touched by aggregate accrual" "$(consumed "$EV")" "0"
eq "...and gets no watermark either" "$(q "SELECT count(*) FROM iam_v2.session_online_watermarks WHERE session_id='$SV'")" "0"

# ---- 11. the function refuses to run unbounded -------------------------------------------------------------
r=$(q "SELECT count(*) FROM iam_v2.p6_tick_online_time('$T','$S', now(), 0)")
case "$r" in *ERROR*) ok "an unbounded tick is refused: it would charge unobserved time";;
  *) no "the tick accepted a zero/absent charge bound" "$r";; esac

# ---- 12. skipped-interval evidence is append-only -----------------------------------------------------------
r=$(q "UPDATE iam_v2.online_time_skipped_intervals SET cause='UNOBSERVED_GAP' WHERE session_id='$S4'")
case "$r" in *ERROR*) ok "skipped-interval evidence cannot be rewritten";;
  *) no "skipped-interval evidence was rewritten" "$r";; esac
r=$(q "DELETE FROM iam_v2.online_time_skipped_intervals WHERE session_id='$S4'")
case "$r" in *ERROR*) ok "skipped-interval evidence cannot be deleted";;
  *) no "skipped-interval evidence was deleted" "$r";; esac

# ---- 13. no PUBLIC execute on the new function ---------------------------------------------------------------
r=$(q "SELECT has_function_privilege('public','iam_v2.p6_tick_online_time(uuid,uuid,timestamptz,int)','EXECUTE')")
eq "PUBLIC cannot execute the accrual tick" "$r" "f"

echo "------------------------------------------------------------"
printf 'PHASE6_M3_AGGREGATE_ONLINE_TIME pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
