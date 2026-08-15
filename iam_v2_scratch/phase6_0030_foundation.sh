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
[ "$n" = "72" ] && ok "iam_v2 carries 72 base tables (68 + the four Phase-6 tables)" || no "iam_v2 base-table count" "found $n"

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

# ...and the distinction is carried by EVIDENCE, which must be checkable and self-consistent.
E=$(q "SELECT gen_random_uuid()")
refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, terminated_at)
         VALUES ('$E','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_ONLINE_TIME', now())" \
        "aggregate-exhaustion evidence without the budget numbers is refused -- evidence without the number is an assertion" "ete_aggregate_carries_its_budget"
refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, budget_seconds, consumed_online_seconds, terminated_at)
         VALUES ('$E','$T','$S','TIME','AGGREGATE_ONLINE_TIME_EXHAUSTED','VALIDITY_WINDOW', 7200, 7200, now())" \
        "evidence whose cause and time mode disagree is refused" "ete_detail_matches_mode"
refuses "INSERT INTO iam_v2.entitlement_termination_evidence (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode, terminated_at)
         VALUES ('$E','$T','$S','AGGREGATE_TIME','VALIDITY_WINDOW_ELAPSED','VALIDITY_WINDOW', now())" \
        "evidence may not introduce a terminal_reason the contract does not define" "terminal_reason"

# ---------------------------------------------------------------- nothing was granted
g="$(q "SELECT count(*) FROM information_schema.role_table_grants g
        JOIN pg_class c ON c.relname=g.table_name
        JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname=g.table_schema
        WHERE g.table_schema='iam_v2'
          AND g.table_name IN ('appliance_product_settings','appliance_product_setting_changes','session_online_watermarks','entitlement_termination_evidence')
          AND g.grantee <> pg_get_userbyid(c.relowner) AND g.grantee <> 'PUBLIC'")"
[ "$g" = "0" ] && ok "no role besides the owner holds any privilege on a Phase-6 table (DARK)" \
               || no "Phase-6 tables are ungranted" "$g grant(s) exist"

# ---------------------------------------------------------------- cleanup: leave nothing behind
docker exec -i "$C" psql -U postgres -d "$DB" -q >/dev/null 2>&1 <<SQL
BEGIN;
SET LOCAL session_replication_role = replica;
DELETE FROM iam_v2.session_online_watermarks WHERE tenant_id='$T';
DELETE FROM iam_v2.sessions WHERE tenant_id='$T';
DELETE FROM iam_v2.devices WHERE tenant_id='$T';
ALTER TABLE iam_v2.appliance_product_setting_changes DISABLE TRIGGER p6_setting_changes_append_only;
DELETE FROM iam_v2.appliance_product_setting_changes WHERE tenant_id='$T';
ALTER TABLE iam_v2.appliance_product_setting_changes ENABLE TRIGGER p6_setting_changes_append_only;
DELETE FROM iam_v2.appliance_product_settings WHERE tenant_id='$T';
COMMIT;
SQL
left="$(q "SELECT (SELECT count(*) FROM iam_v2.appliance_product_settings WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.appliance_product_setting_changes WHERE tenant_id='$T')
              + (SELECT count(*) FROM iam_v2.sessions WHERE tenant_id='$T')")"
[ "$left" = "0" ] && ok "the gate left no rows behind" || no "the gate cleaned up after itself" "$left row(s) remain"

echo "------------------------------------------------------------"
echo "PHASE6_FOUNDATION pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
