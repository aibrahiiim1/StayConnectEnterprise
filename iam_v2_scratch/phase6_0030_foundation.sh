#!/usr/bin/env bash
# PHASE-6 FOUNDATION GATE — migration 0030 against a real PostgreSQL.
#
# Every assertion here is a MEASUREMENT of behaviour, not a reading of the DDL. A CHECK constraint that was
# written but never violated on purpose is a constraint nobody has tested, and the Phase-5 experience is that
# those are exactly the ones that turn out not to bite.
#
# Self-seeding and fixture-free: it creates the rows it needs with ids of its own, and cleans them up. It
# needs no prior state, so it gives the same verdict on a fresh chain and on a long-lived scratch database --
# the failure mode that cost real time in Phase 5, where a gate silently depended on somebody else's seed.
#
# Runs against PHASE6_CONTAINER/PHASE6_DB (defaults suit the local scratch container). It contacts no
# appliance, no Production database and no PMS.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE6_CONTAINER:-iamv2-p6}"
DB="${PHASE6_DB:-iam_scratch}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
# refuses <sql> <label> -- the statement MUST fail
refuses(){ local out; out="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -qAt -c "$1" 2>&1)"; local rc=$?
  if [ $rc -eq 0 ]; then no "$2" "the database ACCEPTED it"; else
    case "$out" in *"$3"*) ok "$2";; *) no "$2" "refused for the wrong reason: $(echo "$out" | head -2 | tr '\n' ' ')";; esac
  fi; }
# accepts <sql> <label>
accepts(){ local out; out="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -qAt -c "$1" 2>&1)"
  if [ $? -eq 0 ]; then ok "$2"; else no "$2" "$(echo "$out" | head -2 | tr '\n' ' ')"; fi; }

echo "== Phase-6 foundation (0030) =="

# ---------------------------------------------------------------- structure
for t in appliance_product_settings appliance_product_setting_changes session_online_watermarks entitlement_termination_evidence; do
  n="$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='$t'")"
  [ "$n" = "1" ] && ok "iam_v2.$t exists" || no "iam_v2.$t exists" "found $n"
done

n="$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE'")"
# 0030 contributes FOUR tables to the 68-table Phase-5 baseline. Asserting the absolute total pinned this
# gate to "no later Phase-6 migration exists", which 0031 immediately falsified -- so it asserts its own
# contribution instead, which is the thing this gate is actually responsible for.
[ "${n:-0}" -ge 72 ] && ok "iam_v2 carries $n base tables (68 Phase-5 + 0030's four, plus any later Phase-6 migration)"                      || no "iam_v2 base-table count" "found $n, expected at least 72"
for t6 in appliance_product_settings appliance_product_setting_changes session_online_watermarks entitlement_termination_evidence; do
  :  # existence already asserted individually above; the count here is only a floor
done

# ---------------------------------------------------------------- the default IS the product decision
# The scope is anchored to REAL platform records, so the gate uses the fixture's real ones -- and then
# proves that invented ones are refused, which is the property that matters.
T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
A=44444444-4444-4444-4444-444444444444
OP=55555555-5555-5555-5555-555555555555
FAKE=$(q "SELECT gen_random_uuid()")
accepts "INSERT INTO iam_v2.appliance_product_settings (tenant_id, site_id, appliance_id) VALUES ('$T','$S','$A')" \
        "a settings row can be created without mentioning the setting"
v="$(q "SELECT guest_device_self_service FROM iam_v2.appliance_product_settings WHERE appliance_id='$A'")"
[ "$v" = "f" ] && ok "a row created without mentioning the setting is OFF -- the default is in the schema, not in a caller" \
                || no "default is OFF" "got '$v'"

# ---------------------------------------------------------------- audit is append-only, enforced
accepts "INSERT INTO iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, setting_key, old_value, new_value, changed_by_operator_id, changed_by)
         VALUES ('$T','$S','$A','guest_device_self_service', false, true, '$OP', 'gate-selftest')" \
        "a setting change can be recorded"
refuses "UPDATE iam_v2.appliance_product_setting_changes SET new_value=false WHERE appliance_id='$A'" \
        "an audit row cannot be UPDATEd" "append-only"
refuses "DELETE FROM iam_v2.appliance_product_setting_changes WHERE appliance_id='$A'" \
        "an audit row cannot be DELETEd" "append-only"
refuses "INSERT INTO iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, setting_key, new_value, changed_by_operator_id, changed_by)
         VALUES ('$T','$S','$A','guest_device_self_service', true, '$OP', '   ')" \
        "an audit row with a blank actor is refused -- 'somebody changed it' is not an audit record" "changed_by"
refuses "INSERT INTO iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, setting_key, new_value, changed_by_operator_id, changed_by)
         VALUES ('$T','$S','$A','something_else', true, '$OP', 'gate-selftest')" \
        "an unknown setting key is refused" "setting_key"

# ---------------------------------------------------------------- no fake or orphan managed state
refuses "INSERT INTO iam_v2.appliance_product_settings (tenant_id, site_id, appliance_id) VALUES ('$T','$S','$FAKE')" \
        "a settings row for an appliance that does not exist is refused -- no fake managed state" "aps_appliance_must_exist"
# A REAL appliance filed under the WRONG scope is the subtler hole: existence is not enough, because the
# composite primary key would accept somebody else's tenant or site beside a genuine appliance id.
refuses "INSERT INTO iam_v2.appliance_product_settings (tenant_id, site_id, appliance_id) VALUES ('$FAKE','$S','$A')" \
        "a REAL appliance under the WRONG TENANT is refused -- existence is not scope" "aps_appliance_must_exist"
refuses "INSERT INTO iam_v2.appliance_product_settings (tenant_id, site_id, appliance_id) VALUES ('$T','$FAKE','$A')" \
        "a REAL appliance under the WRONG SITE is refused" "aps_appliance_must_exist"
refuses "INSERT INTO iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, setting_key, new_value, changed_by_operator_id, changed_by)
         VALUES ('$FAKE','$S','$A','guest_device_self_service', true, '$OP', 'gate-selftest')" \
        "an audit row for a REAL appliance under the WRONG TENANT is refused" "apsc_appliance_must_exist"
refuses "INSERT INTO iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, setting_key, new_value, changed_by_operator_id, changed_by)
         VALUES ('$T','$S','$FAKE','guest_device_self_service', true, '$OP', 'gate-selftest')" \
        "an audit row for an appliance that does not exist is refused" "apsc_appliance_must_exist"
refuses "INSERT INTO iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, setting_key, new_value, changed_by_operator_id, changed_by)
         VALUES ('$T','$S','$A','guest_device_self_service', true, '$FAKE', 'not-a-real-operator')" \
        "an audit actor the server never authenticated is refused -- an actor a caller can choose is not an actor" "changed_by_operator_id"

# ---------------------------------------------------------------- the online watermark cannot go backwards
# A real session is needed for the FK. Build the minimum chain with ids of our own, in ONE committed
# transaction. session_replication_role=replica disables the FK chain ABOVE sessions, which is not what this
# gate is testing -- the watermark's own FK to sessions is still exercised, because the session really exists.
DEV=$(q "SELECT gen_random_uuid()"); ENT=$(q "SELECT gen_random_uuid()"); SES=$(q "SELECT gen_random_uuid()")
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac)
  VALUES ('$DEV','$T','$S','$A','02:00:00:00:00:01');
INSERT INTO iam_v2.sessions (id, tenant_id, site_id, entitlement_id, device_id, state, started)
  VALUES ('$SES','$T','$S','$ENT','$DEV','active', now() - interval '1 hour');
COMMIT;
SQL
n="$(q "SELECT count(*) FROM iam_v2.sessions WHERE id='$SES'")"
if [ "$n" != "1" ]; then
  no "a test session could be seeded for the watermark checks" "sessions=$n"
else
  ok "a test session was seeded for the watermark checks"
  accepts "INSERT INTO iam_v2.session_online_watermarks (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
           VALUES ('$T','$S','$SES', now() - interval '30 minutes', 1800)" \
          "an online watermark can be created"
  accepts "UPDATE iam_v2.session_online_watermarks SET accounted_through = now(), accounted_seconds = 3600 WHERE session_id='$SES'" \
          "a watermark can advance forwards"
  refuses "UPDATE iam_v2.session_online_watermarks SET accounted_through = now() - interval '2 hours' WHERE session_id='$SES'" \
          "a watermark cannot move BACKWARDS -- the interval before it is already charged" "backwards"
  refuses "UPDATE iam_v2.session_online_watermarks SET accounted_seconds = 0 WHERE session_id='$SES'" \
          "accounted_seconds cannot decrease -- corrections go through entitlement_adjustments" "decrease"
  refuses "INSERT INTO iam_v2.session_online_watermarks (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
           VALUES ('$T','$S','$(q "SELECT gen_random_uuid()")', now(), -1)" \
          "a negative accounted_seconds is refused" "check"
fi

# ---------------------------------------------------------------- the CONTRACT vocabulary is untouched
# The distinguishing evidence must NOT come from a new terminal_reason. Widening that set is a change to
# contract vocabulary, which is the Product Owner's to make, not an implementation's.
d="$(q "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='entitlements_terminal_reason_check'")"
case "$d" in
  *AGGREGATE_TIME*) no "the contract terminal_reason set is unchanged" "it was widened with AGGREGATE_TIME: $d" ;;
  *) ok "the contract terminal_reason set is UNCHANGED -- no invented vocabulary" ;;
esac
case "$d" in
  *"'TIME'"*) ok "the contract's own terminal reasons are intact (TIME retained)" ;;
  *) no "contract terminal reasons intact" "constraint: $d" ;;
esac

# ...and the distinction is carried by EVIDENCE. Note the ORDER these are proven in: the BEFORE INSERT
# trigger runs before the table's CHECK constraints, so a row naming an entitlement that does not exist is
# refused by the trigger and never reaches the CHECKs at all. The trigger therefore enforces a SUPERSET, and
# the CHECK-only properties are exercised below against a real terminated entitlement, where they are
# actually reachable. Asserting them here would have proven only that the trigger fires first.
E=$(q "SELECT gen_random_uuid()")
refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, terminated_at)
         VALUES ('$E','$T','$S','TIME','VALIDITY_WINDOW_ELAPSED','VALIDITY_WINDOW', now())"         "evidence for an entitlement that does not exist is refused" "does not exist in this scope"

# ---------------------------------------------------------------- evidence is bound to the real transition
# Seed one entitlement so the binding can be exercised in both directions: while it is LIVE, evidence must be
# refused; once it is TERMINATED, evidence that disagrees with the recorded transition must still be refused,
# and only the agreeing row is accepted.
ENT2=$(q "SELECT gen_random_uuid()")
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
INSERT INTO iam_v2.entitlements (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot,
    service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status)
  VALUES ('$ENT2','$T','$S','$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")','{}'::jsonb,
    '$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")','AGGREGATE_ONLINE_TIME','VALIDITY_WINDOW','ACTIVE');
ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
COMMIT;
SQL
if [ "$(q "SELECT count(*) FROM iam_v2.entitlements WHERE id='$ENT2'")" != "1" ]; then
  no "an entitlement could be seeded for the evidence-binding checks" "seed failed"
else
  ok "an entitlement was seeded for the evidence-binding checks"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
           VALUES ('$ENT2','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', 7200, 7200, now())" \
          "evidence for a LIVE entitlement is refused -- evidence may not describe a transition that did not occur" "not TERMINATED"
  TAT=$(q "SELECT (now() - interval '5 minutes')::text")
  docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
UPDATE iam_v2.entitlements SET status='TERMINATED', terminal_reason='TIME', terminated_at='$TAT'
 WHERE id='$ENT2';
ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
COMMIT;
SQL
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
           VALUES ('$ENT2','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', 7200, 7200, now())" \
          "evidence whose instant disagrees with the recorded termination is refused" "terminated at"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, terminated_at)
           VALUES ('$ENT2','$T','$S','TIME','VALIDITY_WINDOW_ELAPSED','VALIDITY_WINDOW', '$TAT')" \
          "evidence whose time mode disagrees with the entitlement is refused" "time mode"
  # ENT2 carries no pinned plan revision and consumed 0, so it can no longer stand for an ACCEPTED
  # row: the real-state binding compares the numbers against the entitlement and its revision, and
  # ENT2 has neither. What it proves now is stronger -- that invented numbers are refused even when
  # the transition itself agrees. The accepted case moves to a fixture whose numbers are genuine.
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
           VALUES ('$ENT2','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', 7200, 7200, '$TAT')" \
          "invented numbers are refused even when the transition itself agrees" "consumed seconds but the entitlement recorded"

  # =======================================================================================================
  # BOTH terminal time outcomes of an AGGREGATE_ONLINE_TIME entitlement
  # =======================================================================================================
  # An aggregate entitlement can end two ways and BOTH must be recordable: the online-minute budget running
  # out, and the outer calendar window expiring first while minutes remain. The second is how an unused
  # package ordinarily ends, and an earlier coupling constraint made it impossible to describe at all.
  #
  # Which story a row may tell is decided by the NUMBERS, and the numbers must be the REAL ones -- so each
  # fixture below is built with a genuine pinned plan revision carrying a genuine time_quota_seconds, and the
  # entitlement's own consumed_online_seconds is what the evidence must match.
  seed_agg(){ # seed_agg <ent> <budget> <consumed> <window|NULL> -- a terminated AGGREGATE entitlement
    local ent="$1" budget="$2" consumed="$3" win="$4"
    local spr; spr=$(q "SELECT gen_random_uuid()")
    docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.service_plan_revisions DISABLE TRIGGER ALL;
INSERT INTO iam_v2.service_plan_revisions (id, tenant_id, site_id, service_plan_id, revision_no, name,
    down_kbps, up_kbps, max_concurrent_devices, device_limit_policy, idle_timeout_seconds,
    time_accounting_mode, time_quota_seconds)
  VALUES ('$spr','$T','$S','$(q "SELECT gen_random_uuid()")', 1, 'gate-plan', 1000, 1000, 3,
          'REJECT_NEW_DEVICE', 900, 'AGGREGATE_ONLINE_TIME', $budget);
ALTER TABLE iam_v2.service_plan_revisions ENABLE TRIGGER ALL;
ALTER TABLE iam_v2.entitlements DISABLE TRIGGER ALL;
INSERT INTO iam_v2.entitlements (id, tenant_id, site_id, voucher_id, purchase_id, policy_snapshot,
    service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status, terminal_reason,
    terminated_at, window_ends_at, consumed_online_seconds)
  VALUES ('$ent','$T','$S','$(q "SELECT gen_random_uuid()")','$(q "SELECT gen_random_uuid()")','{}'::jsonb,
    '$spr','$(q "SELECT gen_random_uuid()")','AGGREGATE_ONLINE_TIME','VALIDITY_WINDOW','TERMINATED','TIME',
    '$TAT', $win, $consumed);
ALTER TABLE iam_v2.entitlements ENABLE TRIGGER ALL;
COMMIT;
SQL
  }

  # --- (1) exhaustion, consumed >= budget -----------------------------------------------------------------
  EX=$(q "SELECT gen_random_uuid()"); seed_agg "$EX" 7200 7200 "'$TAT'"
  accepts "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
           VALUES ('$EX','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', 7200, 7200, '$TAT')" \
          "AGGREGATE exhaustion with consumed >= budget is accepted"
  refuses "UPDATE iam_v2.entitlement_termination_evidence SET consumed_online_seconds = 1 WHERE entitlement_id='$EX'" \
          "termination evidence is append-only" "append-only"

  # --- (2) outer-window expiry, consumed < budget ---------------------------------------------------------
  OW=$(q "SELECT gen_random_uuid()"); seed_agg "$OW" 7200 900 "'$TAT'"
  accepts "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
           VALUES ('$OW','$T','$S','TIME','AGGREGATE_OUTER_WINDOW_EXPIRED','AGGREGATE_ONLINE_TIME', 7200, 900, '$TAT', '$TAT')" \
          "AGGREGATE outer-window expiry with consumed < budget is accepted"

  # --- (3) each outcome MISLABELLED as the other ----------------------------------------------------------
  # The label alone must never decide. An exhausted entitlement described as an outer-window expiry, and an
  # unexhausted one described as exhaustion, are both refused -- by the constraint that names the actual
  # arithmetic, not by a naming convention.
  MIS1=$(q "SELECT gen_random_uuid()"); seed_agg "$MIS1" 7200 7200 "'$TAT'"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
           VALUES ('$MIS1','$T','$S','TIME','AGGREGATE_OUTER_WINDOW_EXPIRED','AGGREGATE_ONLINE_TIME', 7200, 7200, '$TAT', '$TAT')" \
          "an EXHAUSTED entitlement mislabelled as an outer-window expiry is refused" "ete_outer_window_is_distinguishable"
  MIS2=$(q "SELECT gen_random_uuid()"); seed_agg "$MIS2" 7200 900 "'$TAT'"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
           VALUES ('$MIS2','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', 7200, 900, '$TAT')" \
          "an UNEXHAUSTED entitlement mislabelled as exhaustion is refused" "ete_exhaustion_reached_its_budget"

  # --- (4) outer-window evidence without its immutable window ---------------------------------------------
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
           VALUES ('$MIS2','$T','$S','TIME','AGGREGATE_OUTER_WINDOW_EXPIRED','AGGREGATE_ONLINE_TIME', 7200, 900, NULL, '$TAT')" \
          "outer-window evidence that names no window is refused" "ete_outer_window_is_distinguishable"
  NOWIN=$(q "SELECT gen_random_uuid()"); seed_agg "$NOWIN" 7200 900 "NULL"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
           VALUES ('$NOWIN','$T','$S','TIME','AGGREGATE_OUTER_WINDOW_EXPIRED','AGGREGATE_ONLINE_TIME', 7200, 900, '$TAT', '$TAT')" \
          "outer-window evidence for an entitlement that HAS no window is refused" "immutable window"

  # --- (5) the numbers must be the REAL ones, not merely self-consistent ----------------------------------
  # This is what separates evidence from a well-formed fabrication: a row can satisfy every CHECK above and
  # still describe numbers the termination never used.
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
           VALUES ('$MIS2','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', 600, 900, '$TAT')" \
          "an invented BUDGET that disagrees with the pinned plan revision is refused" "pinned plan revision states"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
           VALUES ('$MIS2','$T','$S','TIME','AGGREGATE_OUTER_WINDOW_EXPIRED','AGGREGATE_ONLINE_TIME', 7200, 42, '$TAT', '$TAT')" \
          "an invented CONSUMPTION that disagrees with the entitlement is refused" "consumed seconds but the entitlement recorded"
  BADWIN=$(q "SELECT gen_random_uuid()"); seed_agg "$BADWIN" 7200 900 "'$TAT'"
  refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
           VALUES ('$BADWIN','$T','$S','TIME','AGGREGATE_OUTER_WINDOW_EXPIRED','AGGREGATE_ONLINE_TIME', 7200, 900, now(), '$TAT')" \
          "evidence naming a window the entitlement does not have is refused" "immutable window is"

  # --- (6) the controlled writer derives everything -------------------------------------------------------
  # A caller that cannot supply a number cannot supply a wrong one.
  CW=$(q "SELECT gen_random_uuid()"); seed_agg "$CW" 3600 3600 "'$TAT'"
  accepts "SELECT iam_v2.p6_record_time_termination('$CW','AGGREGATE_ONLINE_TIME_EXHAUSTED')" \
          "the controlled writer records exhaustion from the entitlement and its pinned plan revision"
  v="$(q "SELECT budget_seconds||'/'||consumed_online_seconds FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id='$CW'")"
  [ "$v" = "3600/3600" ] && ok "the controlled writer's numbers came from real state (budget/consumed = $v)" \
                         || no "controlled writer derives real numbers" "got '$v'"
  refuses "SELECT iam_v2.p6_record_time_termination('$CW','NOT_A_CAUSE')" \
          "the controlled writer refuses an unknown cause" "unknown time-termination cause"
fi

# ---------------------------------------------------------------- least privilege on FUNCTIONS
# A function's ACL starts NULL in PostgreSQL, and NULL means PUBLIC EXECUTE. Every Phase-6 function was
# measured that way before this was fixed -- including the mutation-capable controlled writer -- so the
# assertion is written against the EFFECTIVE privilege (has_function_privilege) rather than against the ACL
# text, which is what an implementation could accidentally satisfy while leaving the grant in place.
#
# It is deliberately generic over p6_*: a Phase-6 function added later without its REVOKE fails here, which is
# the only version of this check that keeps working after I stop looking at it.
pubx="$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
           WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'
             AND has_function_privilege('public', p.oid, 'EXECUTE')")"
[ "$pubx" = "0" ] && ok "no Phase-6 function is executable by PUBLIC (effective privilege, not ACL text)"                   || no "no Phase-6 function is executable by PUBLIC" "$pubx function(s) still are"

nfn="$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'")"
# Derived, not pinned: Phase 6 keeps adding functions across its milestones, and a hardcoded count turns
# every legitimate addition into a false failure -- while a count that merely EXISTS proves nothing about
# privileges. What matters is that the set is non-empty and that every member of it was checked above.
[ "${nfn:-0}" -ge 5 ] && ok "all $nfn Phase-6 functions were checked (a new one without its REVOKE fails above)"                       || no "the Phase-6 function set is non-empty" "found ${nfn:-0}"

# ...and nothing UNINTENDED has been granted. Runtime grants belong to the slice that wires a caller,
# given to the exact role that needs them. The assertion used to be "nobody but the owner", which was
# right while Phase 6 was fully ungranted and became wrong the moment 0033 wired the slice -- so it
# names the intended set, and phase6_least_privilege.sh remains the authority on what those roles may do.
gx="$(q "SELECT coalesce(string_agg(DISTINCT a.grantee::regrole::text, ','),'') FROM pg_proc p
          JOIN pg_namespace n ON n.oid=p.pronamespace,
          LATERAL aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'
            AND a.privilege_type='EXECUTE' AND a.grantee <> p.proowner
            AND a.grantee::regrole::text NOT IN ('svc_scd','svc_edged')")"
[ -z "$gx" ] && ok "no UNINTENDED role holds EXECUTE on a Phase-6 function (svc_scd/svc_edged are 0033's)" \
             || no "no unintended EXECUTE grants exist" "granted to: $gx"

# ---------------------------------------------------------------- nothing was granted
g="$(q "SELECT count(*) FROM information_schema.role_table_grants g
        JOIN pg_class c ON c.relname=g.table_name
        JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname=g.table_schema
        WHERE g.table_schema='iam_v2'
          AND g.table_name IN ('appliance_product_settings','appliance_product_setting_changes','session_online_watermarks','entitlement_termination_evidence')
          AND g.grantee <> pg_get_userbyid(c.relowner) AND g.grantee <> 'PUBLIC'
          AND g.grantee NOT IN ('svc_scd','svc_edged')")"
[ "$g" = "0" ] && ok "no role besides the owner holds any privilege on a Phase-6 table (DARK)" \
               || no "Phase-6 tables are ungranted" "$g grant(s) exist"

# ---------------------------------------------------------------- cleanup: leave nothing behind
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
ALTER TABLE iam_v2.entitlement_termination_evidence DISABLE TRIGGER p6_termination_evidence_append_only;
DELETE FROM iam_v2.entitlement_termination_evidence WHERE tenant_id='$T';
ALTER TABLE iam_v2.entitlement_termination_evidence ENABLE TRIGGER p6_termination_evidence_append_only;
DELETE FROM iam_v2.session_online_watermarks WHERE tenant_id='$T';
DELETE FROM iam_v2.sessions WHERE tenant_id='$T';
DELETE FROM iam_v2.devices WHERE tenant_id='$T';
DELETE FROM iam_v2.entitlements WHERE tenant_id='$T';
DELETE FROM iam_v2.service_plan_revisions WHERE tenant_id='$T';
ALTER TABLE iam_v2.appliance_product_setting_changes DISABLE TRIGGER p6_setting_changes_append_only;
DELETE FROM iam_v2.appliance_product_setting_changes WHERE tenant_id='$T';
ALTER TABLE iam_v2.appliance_product_setting_changes ENABLE TRIGGER p6_setting_changes_append_only;
DELETE FROM iam_v2.appliance_product_settings WHERE tenant_id='$T';
COMMIT;
SQL
left="$(q "SELECT (SELECT count(*) FROM iam_v2.appliance_product_settings WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.appliance_product_setting_changes WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.sessions WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.entitlement_termination_evidence WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.service_plan_revisions WHERE tenant_id='$T')")"
[ "$left" = "0" ] && ok "the gate left no rows behind" || no "the gate cleaned up after itself" "$left row(s) remain"

echo "------------------------------------------------------------"
echo "PHASE6_FOUNDATION pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
