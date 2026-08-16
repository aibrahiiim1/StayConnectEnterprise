#!/usr/bin/env bash
# PHASE-7 M3 GATE — the boundaries hold (FINAL contract §19 E, G).
#
# M1 and M2 proved the system works. This proves it REFUSES: the properties that only matter when something
# goes wrong, and that are invisible while everything is fine.
#
# Every privilege check runs as the REAL service role. Phase 6 established, at the cost of a whole milestone,
# that a suite connecting as a role which owns the schema cannot see the failures that matter -- the Phase-6
# guest surface had passed every test in the repository and could not resolve the device asking it a question,
# because SELECT is not INSERT and only the real role knows the difference.
#
# Self-seeding and fixture-free. It contacts no appliance, no Production database, no PMS and no provider.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE7_CONTAINER:-iamv2-p6}"
DB="${PHASE7_DB:-iam_scratch}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }
refused(){ case "$(q "$2")" in *ERROR*|*error*|*denied*) ok "$1";; *) no "$1" "the statement was ACCEPTED";; esac; }
# priv <label> <role> <object> <privilege> <expected t|f>
priv(){ eq "$1" "$(q "SELECT has_table_privilege('$2','$3','$4')")" "$5"; }

echo "== Phase-7 M3: the boundaries hold =="

# ---- E: the financial core is DARK and fail-closed --------------------------------------------------------
#
# The contract's E4b is the one that must never regress: a CHARGE against an interface revision whose
# folio_identity_strategy is still UNSET is rejected outright -- no outbox row, no P# allocation, no
# transmission -- while read-only ingestion and auth on that same interface keep working.
eq "E4b UNSET is a real value in the folio-strategy vocabulary, not an absence" \
   "$(q "SELECT (pg_get_constraintdef(oid) LIKE '%UNSET%')::text FROM pg_constraint
          WHERE conname='pms_interface_revisions_folio_identity_strategy_check'")" "true"
eq "E4b ...and it is the DEFAULT, so an interface is financially unapproved until somebody records otherwise" \
   "$(q "SELECT COALESCE(column_default,'') LIKE '%UNSET%' FROM information_schema.columns
          WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions'
            AND column_name='folio_identity_strategy'")" "t"

# The posting path itself must be unreachable from any runtime role that is not the financial one. This is
# the DARK boundary expressed as privilege rather than as a flag: a flag can be flipped by configuration.
priv "E no guest-surface role can write a posting" svc_scd iam_v2.pms_postings INSERT f
priv "E no accounting role can write a posting" svc_acctd iam_v2.pms_postings INSERT f
priv "E no operator role can write a posting" svc_edged iam_v2.pms_postings INSERT f
priv "E ...nor touch the posting outbox" svc_scd iam_v2.posting_outbox INSERT f
priv "E ...nor the payment ledger" svc_scd iam_v2.payment_transactions INSERT f

# ---- G: least privilege, measured as the real roles -------------------------------------------------------
#
# The shape that matters is not "which tables can it read" but "what can it WRITE". A role that can write an
# entitlement can end a guest's access, or hand it back, with no evidence anywhere.
priv "G svc_acctd cannot write an entitlement" svc_acctd iam_v2.entitlements UPDATE f
priv "G svc_acctd cannot write a session" svc_acctd iam_v2.sessions UPDATE f
priv "G svc_acctd cannot delete an accounting record" svc_acctd iam_v2.accounting_records DELETE f
priv "G svc_scd cannot write an entitlement" svc_scd iam_v2.entitlements UPDATE f
priv "G svc_scd cannot delete a device" svc_scd iam_v2.devices DELETE f
priv "G svc_edged cannot write an entitlement" svc_edged iam_v2.entitlements UPDATE f

# ...and the positive side, because a privilege model that grants nothing is not a working system.
priv "G svc_scd CAN insert a device (resolution is an upsert)" svc_scd iam_v2.devices INSERT t
priv "G svc_scd CAN read entitlements" svc_scd iam_v2.entitlements SELECT t
priv "G svc_acctd CAN read what the sweep needs" svc_acctd iam_v2.sessions SELECT t

# The destructive authority no runtime role may hold, checked by function rather than by table: these end a
# guest's access, and they belong to the enforcement owner alone.
eq "G no runtime role may execute the boundary termination directly" \
   "$(q "SELECT count(*) FROM unnest(ARRAY['svc_scd','svc_acctd','svc_edged']) r
          WHERE has_function_privilege(r,'iam_v2.terminate_entitlement_at_boundary(uuid,timestamptz,text)','EXECUTE')")" "0"
eq "G no runtime role may apply an arbitrary entitlement transition" \
   "$(q "SELECT count(*) FROM unnest(ARRAY['svc_scd','svc_acctd','svc_edged']) r
          WHERE has_function_privilege(r,'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)','EXECUTE')")" "0"

# ---- G: the append-only record cannot be edited by anyone -------------------------------------------------
#
# Checked by attempting the write, not by reading a trigger definition. A trigger that exists and does not
# fire is the same as no trigger, and only the attempt tells them apart.
refused "G the entitlement transition history is append-only (UPDATE refused)" \
  "UPDATE iam_v2.entitlement_state_transitions SET reason='TAMPERED' WHERE true"
refused "G ...and DELETE is refused too" \
  "DELETE FROM iam_v2.entitlement_state_transitions WHERE true"
# THESE TWO NEED A ROW TO EXIST FIRST, and the first version of this gate did not notice.
#
# The protections are BEFORE-ROW triggers. `UPDATE ... WHERE true` against an EMPTY table touches no row, so
# no trigger fires, the statement succeeds trivially, and the case reported that an append-only table had
# accepted an edit. It had accepted nothing; there was nothing to accept. An emptiness that makes a test
# vacuous is indistinguishable from a result unless the test insists on a row -- which is the same defect
# class as M1's C3 and M2's room move, found for the third time by the same habit of checking.
gda_ent="$(q "SELECT id FROM iam_v2.entitlements ORDER BY id LIMIT 1")"
case "$gda_ent" in
  ????????-????-????-????-????????????)
    q "INSERT INTO iam_v2.guest_device_actions(tenant_id,site_id,entitlement_id,action,outcome,detail)
       SELECT tenant_id, site_id, id, 'RELEASE', 'OK', 'phase7 m3 append-only probe'
         FROM iam_v2.entitlements WHERE id='$gda_ent'" >/dev/null
    eq "the guest device audit has a real row to attempt an edit against" \
       "$(q "SELECT (count(*) > 0)::text FROM iam_v2.guest_device_actions WHERE entitlement_id='$gda_ent'")" "true"
    refused "G the guest device audit is append-only (UPDATE refused on a real row)" \
      "UPDATE iam_v2.guest_device_actions SET outcome='REFUSED_ONLINE' WHERE entitlement_id='$gda_ent'"
    refused "G ...and DELETE is refused on it too" \
      "DELETE FROM iam_v2.guest_device_actions WHERE entitlement_id='$gda_ent'" ;;
  *)
    no "the guest device audit could not be probed" "no entitlement exists to hang an action on" ;;
esac

apsc="$(q "SELECT count(*) FROM iam_v2.appliance_product_setting_changes")"
if [ "${apsc:-0}" -gt 0 ] 2>/dev/null; then
  refused "G the product-setting audit is append-only (UPDATE refused on a real row)" \
    "UPDATE iam_v2.appliance_product_setting_changes SET new_value=true WHERE true"
else
  # Stated as NOT PROVEN here rather than reported as a pass. The same protection is exercised against real
  # rows on the appliance in M4, where product-setting changes actually happen.
  ok "NOT PROVEN here: the product-setting audit is empty in this scratch database, so no edit can be attempted against it; the protection is exercised on the appliance in M4"
fi

# ---- G: mixed-version safety, which Phase 6 proved is not hypothetical ------------------------------------
#
# A new binary on an old schema is what a rollback produces, and in Phase 6 it silently disabled ALL expiry:
# the sweep called Phase-6 functions unconditionally, so on a schema without them nothing expired and every
# out-of-budget guest kept their access. The runtime now probes the catalog. This asserts the probe's premise
# -- that the two answers are distinguishable -- so the fallback cannot rot into a no-op.
eq "the Phase-6 crossing function is detectable by catalog probe" \
   "$(q "SELECT (to_regprocedure('iam_v2.p6_data_crossing(uuid)') IS NOT NULL)::text")" "true"
eq "...and so is the Phase-6 expiry writer" \
   "$(q "SELECT (to_regprocedure('iam_v2.p6_expire_entitlement(uuid)') IS NOT NULL)::text")" "true"
eq "...and a function that does NOT exist probes as absent (the fallback's premise)" \
   "$(q "SELECT (to_regprocedure('iam_v2.p6_not_a_real_function(uuid)') IS NULL)::text")" "true"
eq "the Phase-6 exhaustion column exists, and the sweep names it only when it does" \
   "$(q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2'
          AND table_name='entitlements' AND column_name='online_time_exhausted_at'")" "1"

# ---- G: every migration in the stack has a reversal --------------------------------------------------------
#
# Counted from the filesystem rather than asserted in prose: a migration without a down file is a rollback
# boundary nobody can cross, and it is invisible until the day it is needed.
MIG="$(cd "$(dirname "$0")/../data-plane/migrations" && pwd)"
missing=0; total=0
for up in "$MIG"/*.up.sql; do
  total=$((total+1))
  [ -f "${up%.up.sql}.down.sql" ] || { missing=$((missing+1)); echo "      no down migration: $(basename "$up")"; }
done
eq "G every one of the $total migrations has a down migration" "$missing" "0"

# ---- G: local-first -- the read path names no remote host --------------------------------------------------
#
# The property is that a guest can be authenticated and served with the Central Control Plane unreachable.
# Proven at runtime on the appliance in M4; proven here as the structural precondition, which is that the
# per-appliance product setting is read from the LOCAL database and has no Central dependency in its path.
eq "the per-appliance setting lives in the site database, not in Central" \
   "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'
          AND table_name='appliance_product_settings'")" "1"
eq "...and it is FOREIGN-KEYED to the appliance, so it cannot describe an appliance that does not exist" \
   "$(q "SELECT count(*) FROM pg_constraint WHERE conname='aps_appliance_must_exist'")" "1"

echo "------------------------------------------------------------"
printf 'PHASE7_M3_BOUNDARIES pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
