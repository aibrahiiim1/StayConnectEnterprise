#!/usr/bin/env bash
# PHASE-6 M4 — ROLLBACK REHEARSAL, in the order a rollback must actually be performed.
#
# THE HAZARD IS NOT THAT THE DOWN MIGRATIONS FAIL. It is that they SUCCEED while the system is still using
# what they remove. Every Phase-6 down migration is faithful, and faithful means each one restores a weaker
# predecessor:
#
#   0042 down  restores writers that fabricate an exhaustion instant from now()
#   0041 down  restores a writer that terminates on a caller-supplied reason and instant
#   0040 down  removes the sanctioned writer entirely, so the sweep needs an identity that may call the
#              termination primitive directly
#   0038 down  restores a tick that ignores an earlier DATA crossing
#   0037 down  restores a tick that ignores the outer window and mis-dates the crossing
#   0035 down  removes the appliance-anchored serialization of the first setting write and re-widens the
#              guest action set
#   0034 down  restores a release policy whose throttle the CALLER may choose, and returns direct setting
#              writes to edged's role
#   0033 down  removes the runtime least-privilege grants the slice was wired with
#   0032 down  removes the structural guard, so a live session on a released binding becomes representable
#              -- the load-bearing hazard the plan records
#   0031 down  removes the guest release function and its append-only audit
#   0030 down  removes the per-appliance setting, its audit, the online watermark and the termination
#              evidence -- and with them the product state an appliance was operating on
#
# So the ORDER is the safety property, and this rehearsal proves it in that order:
#
#   1. the deployment flags go OFF and the guest/operator surfaces are gone
#   2. accrual is quiesced -- no entitlement is left in the aggregate mode
#   3. the guest device capability is quiesced -- no appliance still offers it, and no released binding is
#      carrying a live session, because 0032 down makes that state representable again
#   4. only then do the migrations come down, newest first, THROUGH 0030
#   5. and the whole slice comes back up when they are re-applied
#
# Disposable database only. It contacts no appliance and no Production database.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE6_CONTAINER:-iamv2-p6}"
DB="${PHASE6_DB:-iam_scratch}"
MIG="$(cd "$(dirname "$0")/../data-plane/migrations" && pwd)"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
apply(){ docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q < "$MIG/$1" 2>&1; }
eqv(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

# THE SEQUENCE, DEFINED ONCE. Down is this list reversed; up is this list in order. Two hand-maintained
# lists is how a migration ends up skipped in one direction and not the other, and how a stray line-
# continuation token (there was a literal "\n" in the down list) goes unnoticed because the runner only ever
# looked at output text.
PHASE6_MIGRATIONS="
0030_phase6_foundation
0031_phase6_guest_device_self_service
0032_phase6_release_admission_serialization
0033_phase6_runtime_least_privilege
0034_phase6_policy_boundaries
0035_phase6_setting_serialization_and_list_audit
0036_phase6_aggregate_online_time
0037_phase6_aggregate_window_and_exact_crossing
0038_phase6_aggregate_respects_data_crossing
0039_phase6_acctd_aggregate_privilege
0040_phase6_acctd_expiry_writer
0041_phase6_expiry_writer_derives_the_condition
0042_phase6_exhaustion_instant_must_be_provable
0043_phase6_exhaustion_instant_from_the_real_crossing
0044_phase6_exhaustion_instant_lower_bound
0045_phase6_over_budget_fail_closed
0046_phase6_suspension_reason_is_not_terminal
0047_phase6_guest_surface_can_resolve_a_device
"

# EVERY FILE MUST EXIST BEFORE ANYTHING RUNS. A rollback that discovers a missing down migration halfway
# through is a rollback that cannot finish and cannot go back.
preflight() {
  local m missing=0 n=0
  for m in $PHASE6_MIGRATIONS; do
    n=$((n+1))
    [ -f "$MIG/$m.up.sql" ]   || { no "migration file present" "$m.up.sql is missing";   missing=1; }
    [ -f "$MIG/$m.down.sql" ] || { no "migration file present" "$m.down.sql is missing"; missing=1; }
  done
  [ "$missing" = "0" ] && ok "all $n Phase-6 migrations have an up and a down file" || return 1
  # The list must be strictly ascending: a duplicated or out-of-order entry would run the sequence wrong in
  # one direction and still look plausible in the log.
  local sorted; sorted="$(printf '%s\n' $PHASE6_MIGRATIONS | sort -u | tr '\n' ' ')"
  local given;  given="$(printf '%s\n' $PHASE6_MIGRATIONS | tr '\n' ' ')"
  [ "$sorted" = "$given" ] && ok "the sequence is strictly ascending with no duplicates" \
    || { no "the migration sequence is out of order or duplicated" "$given"; return 1; }
  return 0
}

# apply_checked runs one migration and FAILS ON THE EXIT STATUS, not on whether the word ERROR appeared in
# the output. The old runner grepped text, so a psql that could not start -- or a file that did not exist --
# produced no "ERROR" line and was counted as a PASS.
apply_checked() {
  local m="$1" dir="$2" out rc
  [ -f "$MIG/$m.$dir.sql" ] || { no "$m $dir" "file missing"; return 1; }
  out="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q < "$MIG/$m.$dir.sql" 2>&1)"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    no "$m $dir" "psql exit $rc: $(printf '%s' "$out" | head -1)"
    return 1
  fi
  case "$out" in *ERROR*) no "$m $dir" "$(printf '%s' "$out" | head -1)"; return 1;; esac
  ok "$m $dir"
  return 0
}

echo "== Phase-6 rollback rehearsal =="

preflight || { echo "PHASE6_ROLLBACK_REHEARSAL pass=$pass fail=$fail (preflight)"; exit 1; }

# ---- 1. the precondition the runbook states, asserted rather than assumed --------------------------------
live="$(q "SELECT count(*) FROM iam_v2.entitlements WHERE time_accounting_mode='AGGREGATE_ONLINE_TIME' AND status IN ('ACTIVE','PENDING','SUSPENDED')")"
if [ "$live" = "0" ]; then
  ok "no live AGGREGATE_ONLINE_TIME entitlement remains: accrual is quiesced and the rollback may proceed"
else
  no "accrual is not quiesced" "$live live aggregate entitlement(s); rolling back now would restore weaker writers underneath them"
fi

# The guest surfaces are gated by deployment flags, not by the schema, so the runbook's first step is
# verifiable here only as "the flags are off". The coherence tool is the authority on the appliance.
eqv "the schema still carries the Phase-6 guard that 0032 down would remove (so it is worth ordering)" \
   "$(q "SELECT count(*) FROM pg_trigger WHERE tgname='p6_session_requires_authorized_binding'")" "1"

# THE 0032 PRECONDITIONS, checked rather than asserted. Its down migration makes "a live session on a
# released binding" representable again, so the rollback may only proceed when no appliance is still offering
# the capability that creates released bindings, and when no such pair exists right now.
enabled="$(q "SELECT count(*) FROM iam_v2.appliance_product_settings WHERE guest_device_self_service")"
[ "$enabled" = "0" ] && ok "no appliance still offers guest device self-service: the capability is quiesced" \
  || no "the guest device capability is still ON somewhere" \
        "$enabled appliance(s); rolling back 0032 underneath it would make the forbidden state representable"

forbidden="$(q "SELECT count(*) FROM iam_v2.entitlement_devices ed
                  JOIN iam_v2.sessions se ON se.entitlement_id = ed.entitlement_id AND se.device_id = ed.device_id
                 WHERE ed.status='DISCONNECTED' AND se.state IN ('active','PENDING_ENFORCEMENT')")"
eqv "no released binding is carrying a live session before the guard is removed" "$forbidden" "0"

# ---- 2. down, newest first ------------------------------------------------------------------------------
# DOWN: newest first, and the whole run stops at the first failure -- continuing past a failed down
# migration would leave the schema in a state no list describes.
for m in $(printf '%s\n' $PHASE6_MIGRATIONS | sort -r); do
  apply_checked "$m" down || { echo "PHASE6_ROLLBACK_REHEARSAL pass=$pass fail=$fail (down aborted at $m)"; exit 1; }
done

# The aggregate surface is gone from the schema at this point.
eqv "the accrual tick no longer exists after the aggregate rollback" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_tick_online_time'")" "0"
eqv "...and neither does the skipped-interval evidence table" \
   "$(q "SELECT to_regclass('iam_v2.online_time_skipped_intervals') IS NULL")" "t"

# THE WHOLE SLICE IS GONE, which is what makes this the complete rollback rather than the aggregate half.
eqv "the per-appliance setting table is gone" "$(q "SELECT to_regclass('iam_v2.appliance_product_settings') IS NULL")" "t"
eqv "the guest device action audit is gone" "$(q "SELECT to_regclass('iam_v2.guest_device_actions') IS NULL")" "t"
eqv "the session-binding guard is gone -- the 0032 hazard, now real" \
   "$(q "SELECT count(*) FROM pg_trigger WHERE tgname='p6_session_requires_authorized_binding'")" "0"
eqv "no Phase-6 function remains" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'")" "0"
# The platform anchor 0030 created is removed with it -- and only if 0030 created it, which the marker records.
eqv "the appliance scope anchor 0030 owned is gone with it" \
   "$(q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='appliances_tsi_anchor'")" "0"

# ---- 3. back up, oldest first ---------------------------------------------------------------------------
# UP: oldest first, same list, same stop-on-failure rule.
for m in $(printf '%s\n' $PHASE6_MIGRATIONS | sort); do
  apply_checked "$m" up || { echo "PHASE6_ROLLBACK_REHEARSAL pass=$pass fail=$fail (re-apply aborted at $m)"; exit 1; }
done

# ---- 4. the system is back, in its CURRENT shape rather than a historical one ----------------------------
eqv "the accrual tick is back, with the caps parameter" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_tick_online_time' AND pg_get_function_arguments(p.oid) LIKE '%p_caps%'")" "1"
eqv "the expiry writer is back in its id-only form, not the caller-supplied one" \
   "$(q "SELECT pg_get_function_arguments(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_expire_entitlement'")" "p_entitlement uuid"
eqv "the provable-instant function is back" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_exhaustion_instant'")" "1"
eqv "svc_acctd holds EXECUTE on the writer again" \
   "$(q "SELECT has_function_privilege('svc_acctd','iam_v2.p6_expire_entitlement(uuid)','EXECUTE')")" "t"
eqv "...and still holds NO write anywhere in iam_v2" \
   "$(q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee='svc_acctd' AND privilege_type <> 'SELECT'")" "0"
eqv "PUBLIC still cannot execute the writer" \
   "$(q "SELECT has_function_privilege('public','iam_v2.p6_expire_entitlement(uuid)','EXECUTE')")" "f"
eqv "the session-binding guard is back" \
   "$(q "SELECT count(*) FROM pg_trigger WHERE tgname='p6_session_requires_authorized_binding'")" "1"
eqv "the per-appliance setting is back, and still defaults OFF" \
   "$(q "SELECT column_default FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='appliance_product_settings' AND column_name='guest_device_self_service'")" "false"
eqv "the guest release policy is back in its non-caller-selectable form" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_guest_release_device_policy'")" "1"
eqv "the guest action set is narrowed again to RELEASE" \
   "$(q "SELECT count(*) FROM pg_constraint WHERE conname='guest_device_actions_action_check'")" "1"
# 0047's grants must come back with it: without them the guest surface cannot resolve the device that is
# asking, and every request answers UNAVAILABLE while looking perfectly configured.
eqv "the guest surface can resolve a device again"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.devices','INSERT')")" "t"
eqv "...and read the entitlement it belongs to"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.entitlements','SELECT')")" "t"
eqv "...while still not being able to write one"    "$(q "SELECT has_table_privilege('svc_scd','iam_v2.entitlements','UPDATE')")" "f"
eqv "suspension closes children as SUSPENDED, not ENDED" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_suspend_over_budget' AND pg_get_functiondef(p.oid) LIKE '%ENTITLEMENT_SUSPENDED%'")" "1"
eqv "the fail-closed suspension writer is back" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_suspend_over_budget'")" "1"
eqv "the exhaustion instant comes from the real crossing again" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_exhaustion_instant'")" "1"

echo "------------------------------------------------------------"
printf 'PHASE6_ROLLBACK_REHEARSAL pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
