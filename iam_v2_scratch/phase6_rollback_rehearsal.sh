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
#   0032 down  removes the structural guard, so a live session on a released binding becomes representable
#
# So the ORDER is the safety property, and this rehearsal proves it in that order:
#
#   1. the deployment flags go OFF and the guest/operator surfaces are gone
#   2. accrual is quiesced -- no entitlement is left in the aggregate mode
#   3. only then do the migrations come down, newest first
#   4. and the system comes back up when they are re-applied
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

echo "== Phase-6 rollback rehearsal =="

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

# ---- 2. down, newest first ------------------------------------------------------------------------------
for m in 0042_phase6_exhaustion_instant_must_be_provable \
         0041_phase6_expiry_writer_derives_the_condition \
         0040_phase6_acctd_expiry_writer \
         0039_phase6_acctd_aggregate_privilege \
         0038_phase6_aggregate_respects_data_crossing \
         0037_phase6_aggregate_window_and_exact_crossing \
         0036_phase6_aggregate_online_time; do
  out="$(apply "$m.down.sql")"
  case "$out" in *ERROR*) no "$m down" "$(echo "$out" | head -1)";; *) ok "$m down";; esac
done

# The aggregate surface is gone from the schema at this point.
eqv "the accrual tick no longer exists after the aggregate rollback" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p6_tick_online_time'")" "0"
eqv "...and neither does the skipped-interval evidence table" \
   "$(q "SELECT to_regclass('iam_v2.online_time_skipped_intervals') IS NULL")" "t"

# ---- 3. back up, oldest first ---------------------------------------------------------------------------
for m in 0036_phase6_aggregate_online_time \
         0037_phase6_aggregate_window_and_exact_crossing \
         0038_phase6_aggregate_respects_data_crossing \
         0039_phase6_acctd_aggregate_privilege \
         0040_phase6_acctd_expiry_writer \
         0041_phase6_expiry_writer_derives_the_condition \
         0042_phase6_exhaustion_instant_must_be_provable; do
  out="$(apply "$m.up.sql")"
  case "$out" in *ERROR*) no "$m re-up" "$(echo "$out" | head -1)";; *) ok "$m re-up";; esac
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

echo "------------------------------------------------------------"
printf 'PHASE6_ROLLBACK_REHEARSAL pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
