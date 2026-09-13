#!/usr/bin/env bash
# THE PRODUCTION PRIVILEGE HARNESS.
#
# The ordinary integration database asserts the DARK posture: svc_scd holds no runtime grants, and a test
# proves it. That assertion is correct and stays untouched. But it means the ordinary database can never
# exercise a privilege requirement, and the least-privilege tests skipped whenever the role was absent — which
# is how two defects reached production with CI green:
#
#   * svc_scd could not SELECT iam_v2.pms_interface_runtime, found only when the deployment ran;
#   * svc_scd could not lock iam_v2.auth_context_offers, found only when the first real guest tried to sign in
#     and was refused with package_not_offered_to_this_context.
#
# Both were invisible because every suite ran as a superuser. So this builds a SECOND, PRODUCTION-LIKE
# database and runs only the `prodprivilege` tag against it.
#
# THE BOOTSTRAP BELOW IS COPIED FROM scripts/factory-clean-baseline-verify.sh, deliberately and almost line for
# line. Three hand-written approximations failed here in a row — wrong image, wrong order, wrong readiness
# check — each costing a full CI round. The proven sequence is: the TimescaleDB image, readiness by "ready to
# accept connections" twice plus three clean queries, Gate-P roles, the stayconnect platform role,
# gatep-iam-roles BEFORE the baseline because the baseline's own privileges name iam_v2_owner, the baseline
# applied as the platform role, then IAM ownership and the real grant chain.
#
# NO TEST-ONLY PRIVILEGES ARE ADDED. Everything svc_scd holds here it holds because deploy/gatep says so. If a
# test fails for want of a privilege, the fix belongs in deploy/gatep, not in this file.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATEP="$ROOT/deploy/gatep"
BASE="$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql"
OUT="$(mktemp -d)"
C="sc-prodpriv-$$"
DB=stayconnect_site

cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

[ -f "$BASE" ] || { echo "  FAIL: baseline missing"; exit 1; }

echo "== blank PostgreSQL cluster, port published for the Go suite =="
docker run -d --name "$C" -p 0:5432 -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
  "${CLEANROOM_IMAGE:-timescale/timescaledb:2.16.1-pg16}" >/dev/null
ready=0
for _ in $(seq 1 180); do
  if [ "$(docker logs "$C" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ]; then
    ok=0
    for _ in 1 2 3; do
      docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && ok=$((ok+1))
      sleep 1
    done
    [ "$ok" = "3" ] && { ready=1; break; }
  fi
  sleep 1
done
[ "$ready" = "1" ] || { echo "  FAIL: database never became ready"; docker logs "$C" 2>&1 | tail -10; exit 1; }
psql_run() { docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 "$@"; }

echo "== Gate-P roles, then the baseline, then Gate-P ownership and grants =="
psql_run < "$GATEP/gatep-roles.sql" >"$OUT/roles.log" 2>&1 \
  || { echo "  FAIL gatep-roles.sql:"; tail -3 "$OUT/roles.log"; exit 1; }
psql_run -tAqc "DO \$\$ BEGIN CREATE ROLE stayconnect NOLOGIN SUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;" >/dev/null
docker cp "$GATEP" "$C:/tmp/gatep" >/dev/null

# BEFORE the baseline: the baseline carries its own privileges and those name iam_v2_owner.
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-iam-roles.sql >"$OUT/iam-roles-pre.log" 2>&1 \
  || { echo "  FAIL gatep-iam-roles.sql (pre):"; tail -3 "$OUT/iam-roles-pre.log"; exit 1; }

# The dump carries both schemas in one file, so it is applied as the platform role; Gate-P reasserts IAM
# ownership afterwards, which is what gatep-iam-ownership.sql is for and is proven idempotent.
if { printf 'SET ROLE stayconnect;\n'; cat "$BASE"; } | psql_run >"$OUT/baseline.log" 2>&1; then
  echo "  baseline applied"
else
  echo "  FAIL baseline:"; grep -iE '^ERROR|^psql:' "$OUT/baseline.log" | tail -5; tail -3 "$OUT/baseline.log"; exit 1
fi

for f in gatep-iam-ownership.sql gatep-iam-roles.sql gatep-grants.sql svc-edged-phase345-admin-grants.sql; do
  [ -f "$GATEP/$f" ] || continue
  MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
    -f "/tmp/gatep/$f" >"$OUT/$f.log" 2>&1 \
    || { echo "  FAIL $f:"; tail -3 "$OUT/$f.log"; exit 1; }
  echo "  applied $f"
done

# Migrations published after the baseline snapshot. 0057 is the one this harness exists to exercise; the
# others keep the schema matching a current appliance.
for m in 0056_materialization_readiness 0057_lock_auth_context_offer \
         0058_guest_auth_row_locks \
         0059_speed_allocation \
         0060_last_good_roster_survives_a_failed_resync \
         0061_the_entitlement_records_what_it_spent \
         0062_the_crossing_sample_still_belongs_to_the_entitlement_that_spent_it \
         0063_scoped_reader_for_current_package_conditions \
         0064_the_allowance_a_stay_earned_is_frozen_when_it_is_granted \
         0071_central_serves_this_appliance_for_licensing_only; do
  f="$ROOT/data-plane/migrations/$m.up.sql"
  [ -f "$f" ] || continue
  psql_run < "$f" >"$OUT/$m.log" 2>&1 || { echo "  FAIL $m:"; tail -3 "$OUT/$m.log"; exit 1; }
  echo "  applied $m"
done

# 0057's own grant is guarded on the role existing. Reassert the Gate-P chain afterwards so the test proves
# the grant survives a RECONCILE, not merely a fresh migration — the failure mode that has bitten twice.
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-grants.sql >"$OUT/grants-recheck.log" 2>&1 \
  || { echo "  FAIL gatep-grants.sql (reconcile):"; tail -3 "$OUT/grants-recheck.log"; exit 1; }

have="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_roles WHERE rolname='svc_scd'")"
[ "${have:-0}" = "1" ] || { echo "  FAIL: svc_scd is not provisioned; this harness exists to exercise it"; exit 1; }

# The full matrix for every table the guest-auth chain touches, printed on every run. A row lock needs
# UPDATE as well as SELECT, so this is where a missing capability is visible BEFORE the test says
# "permission denied" — and where an over-broad grant is visible too.
echo "== svc_scd on the guest-auth chain (select/insert/update) =="
docker exec "$C" psql -U postgres -d "$DB" -tAqc "
  SELECT '  ' || rpad(t, 38) ||
         CASE WHEN has_table_privilege('svc_scd','iam_v2.'||t,'SELECT') THEN 's' ELSE '-' END ||
         CASE WHEN has_table_privilege('svc_scd','iam_v2.'||t,'INSERT') THEN 'i' ELSE '-' END ||
         CASE WHEN has_table_privilege('svc_scd','iam_v2.'||t,'UPDATE') THEN 'u' ELSE '-' END
    FROM unnest(ARRAY['stays','auth_contexts','auth_context_offers','pms_interface_runtime','offer_quotes',
                      'purchases','entitlements','entitlement_state_transitions',
                      'entitlement_device_authorizations','sessions','session_entitlement_bindings',
                      'stay_guests','stay_events']) AS t"

echo "== the privilege model under test =="
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "SELECT '  lock helper EXECUTE by svc_scd: ' ||
          has_function_privilege('svc_scd','iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid)','EXECUTE') ||
          '  |  auth_context_offers UPDATE by svc_scd: ' ||
          has_table_privilege('svc_scd','iam_v2.auth_context_offers','UPDATE') ||
          '  |  lock helper EXECUTE by PUBLIC: ' ||
          has_function_privilege('public','iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid)','EXECUTE')"

# GUEST SIGN-IN PROTECTION (0068), against the REAL service roles after a Gate-P reconcile.
#
# This block exists because of what Delivery B cost: svc_scd read a table it had no privilege on, every suite
# passed as superuser, and the first real guest on the appliance was refused — along with every other guest on
# the property. The privileges below are asserted rather than printed, because a printed matrix is only read
# when somebody already suspects something.
#
# Each line is a property, not a preference:
#   scd may ask the gate and report outcomes, and holds NO table privilege — it cannot read the restriction
#   list, change the policy it is subject to, or release anybody;
#   edged may read both tables and call the three operator functions, and holds NO UPDATE on either table and
#   no INSERT on the change log — so there is no privilege that moves the policy without recording who moved
#   it, and none that writes a change record that did not happen.
echo "== guest sign-in protection: the privilege model (0068) =="
fails=0
assert_priv() {  # description | SQL returning boolean | expected
  got="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "$2" 2>&1 | tr -d '[:space:]')"
  if [ "$got" != "$3" ]; then
    echo "  FAIL: $1 -> '$got', expected '$3'"
    fails=$((fails+1))
  else
    echo "  ok: $1"
  fi
}

assert_priv "svc_scd may ask the gate" \
  "SELECT has_function_privilege('svc_scd','iam_v2.guest_signin_gate(uuid,uuid,macaddr)','EXECUTE')" t
assert_priv "svc_scd may report a wrong credential" \
  "SELECT has_function_privilege('svc_scd','iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text)','EXECUTE')" t
assert_priv "svc_scd may report a success" \
  "SELECT has_function_privilege('svc_scd','iam_v2.guest_signin_note_success(uuid,uuid,macaddr)','EXECUTE')" t
assert_priv "svc_scd CANNOT change the policy it is subject to" \
  "SELECT has_function_privilege('svc_scd','iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text)','EXECUTE')" f
assert_priv "svc_scd CANNOT release a restriction" \
  "SELECT has_function_privilege('svc_scd','iam_v2.guest_signin_release(uuid,uuid,uuid,text,text)','EXECUTE')" f
assert_priv "svc_scd CANNOT read the restriction list" \
  "SELECT has_table_privilege('svc_scd','iam_v2.guest_signin_restrictions','SELECT')" f
assert_priv "svc_scd CANNOT read the policy table directly" \
  "SELECT has_table_privilege('svc_scd','iam_v2.site_guest_signin_protection','SELECT')" f

assert_priv "svc_edged may read the restrictions" \
  "SELECT has_table_privilege('svc_edged','iam_v2.guest_signin_restrictions','SELECT')" t
assert_priv "svc_edged may read the change log" \
  "SELECT has_table_privilege('svc_edged','iam_v2.guest_signin_protection_changes','SELECT')" t
assert_priv "svc_edged may read the effective policy" \
  "SELECT has_function_privilege('svc_edged','iam_v2.guest_signin_protection_get(uuid,uuid)','EXECUTE')" t
assert_priv "svc_edged may change the policy through the audited function" \
  "SELECT has_function_privilege('svc_edged','iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text)','EXECUTE')" t
assert_priv "svc_edged may release through the audited function" \
  "SELECT has_function_privilege('svc_edged','iam_v2.guest_signin_release(uuid,uuid,uuid,text,text)','EXECUTE')" t
assert_priv "svc_edged CANNOT write the settings table directly" \
  "SELECT has_table_privilege('svc_edged','iam_v2.site_guest_signin_protection','UPDATE')" f
assert_priv "svc_edged CANNOT write a restriction directly" \
  "SELECT has_table_privilege('svc_edged','iam_v2.guest_signin_restrictions','UPDATE')" f
assert_priv "svc_edged CANNOT write the change log directly" \
  "SELECT has_table_privilege('svc_edged','iam_v2.guest_signin_protection_changes','INSERT')" f

assert_priv "PUBLIC holds nothing on the restrictions" \
  "SELECT has_table_privilege('public','iam_v2.guest_signin_restrictions','SELECT')" f
assert_priv "PUBLIC holds nothing on the policy" \
  "SELECT has_table_privilege('public','iam_v2.site_guest_signin_protection','SELECT')" f
assert_priv "PUBLIC cannot ask the gate" \
  "SELECT has_function_privilege('public','iam_v2.guest_signin_gate(uuid,uuid,macaddr)','EXECUTE')" f

# ---------------------------------------------------------------------------------------------------------
# CLOUD SYNC AND PMS RECONCILIATION (0069), against the REAL service roles after a Gate-P reconcile.
#
# Two audit trails are made mandatory BY PRIVILEGE here rather than by convention, and each has a specific
# thing it prevents:
#
#   * no UPDATE on iam_v2.stay_events for svc_edged — a re-offer moves a departure's processing state back to
#     PENDING, and the outcome of the engine reconsidering it can check a guest out and revoke their access.
#     Direct UPDATE would let that happen with nothing recording that anybody decided it.
#   * no UPDATE on public.sync_outbox for svc_edged — recovery clears the abandoned flag AND writes who
#     released the records, in one call. Direct UPDATE would also permit marking undelivered records as sent,
#     which is the one way to empty a backlog without delivering it.
# ---------------------------------------------------------------------------------------------------------
echo "== cloud sync and PMS reconciliation: the privilege model (0069) =="
assert_priv "svc_edged may read the reconciliation cases"   "SELECT has_table_privilege('svc_edged','iam_v2.pms_reconciliation_cases','SELECT')" t
# THE RE-OFFER FUNCTION MUST NOT EXIST (0070). It shipped in 0069 and could never work: stay_events is
# one-way and a checkout boundary must be an APPLIED GO event. It is asserted ABSENT rather than merely
# unused, because a function that cannot succeed is worse than no function -- an operator reading its failure
# would conclude the appliance is broken rather than that the design says no.
assert_priv "the re-offer function is GONE, not merely ungranted"   "SELECT to_regprocedure('iam_v2.pms_reoffer_stay_event(uuid,uuid,uuid,uuid,text,text,jsonb)') IS NULL" t
assert_priv "and its log table is gone with it"   "SELECT to_regclass('iam_v2.stay_event_reoffers') IS NULL" t
# Unchanged and now load-bearing on its own: with the re-offer function gone, this is the only thing standing
# between the admin API and a PMS event's processing state. The database trigger refuses it too; this refuses
# it a layer earlier.
assert_priv "svc_edged CANNOT move a PMS event's processing state directly"   "SELECT has_table_privilege('svc_edged','iam_v2.stay_events','UPDATE')" f

assert_priv "svc_edged may read the retention setting"   "SELECT has_function_privilege('svc_edged','iam_v2.cloud_sync_settings_get(uuid,uuid)','EXECUTE')" t
assert_priv "svc_edged may change retention through the audited function"   "SELECT has_function_privilege('svc_edged','iam_v2.cloud_sync_settings_set(uuid,uuid,integer,text,text)','EXECUTE')" t
assert_priv "svc_edged CANNOT write the retention setting directly"   "SELECT has_table_privilege('svc_edged','iam_v2.site_cloud_sync_settings','UPDATE')" f
assert_priv "svc_edged CANNOT write the retention change log directly"   "SELECT has_table_privilege('svc_edged','iam_v2.cloud_sync_settings_changes','INSERT')" f

assert_priv "svc_edged may recover abandoned records through the audited function"   "SELECT has_function_privilege('svc_edged','iam_v2.sync_outbox_recover_exhausted(text,text,integer)','EXECUTE')" t
assert_priv "svc_edged CANNOT write the recovery log directly"   "SELECT has_table_privilege('svc_edged','iam_v2.sync_outbox_recovery_log','INSERT')" f
assert_priv "svc_edged CANNOT mark an undelivered record as sent"   "SELECT has_table_privilege('svc_edged','public.sync_outbox','UPDATE')" f

# scd prunes DELIVERED records and does not recover abandoned ones. A daemon that could recover could do it
# on a loop; releasing records back onto the wire is an operator decision with a far end that absorbs it.
assert_priv "svc_scd may prune delivered records"   "SELECT has_function_privilege('svc_scd','iam_v2.sync_outbox_prune_delivered(integer)','EXECUTE')" t
assert_priv "svc_scd CANNOT recover abandoned records"   "SELECT has_function_privilege('svc_scd','iam_v2.sync_outbox_recover_exhausted(text,text,integer)','EXECUTE')" f

# THE CONSTRAINT THAT DECIDED WHERE 0069's QUEUE OBJECTS LIVE, asserted so it cannot be rediscovered on an
# appliance at deploy time -- which is exactly how it WAS discovered.
#
# A live-site migration is applied by a least-privilege non-superuser; edge-migrate.sh refuses anything else.
# That role is iam_v2_owner, and it holds no CREATE on schema public. A migration creating objects there
# fails whole with "permission denied for schema public" -- and the runner's success check greps only for its
# own pre-apply echo, so it reports EDGE_MIGRATE_OK for an apply that rolled back entirely.
#
# So the operator-facing queue objects live in iam_v2 where that role may create them, the queue TABLE stays
# in public where 0001 put it, and the definer functions reach it on a grant Gate-P makes.
assert_priv "iam_v2_owner CANNOT create in schema public (the constraint 0069 is shaped by)"   "SELECT has_schema_privilege('iam_v2_owner','public','CREATE')" f
assert_priv "iam_v2_owner CAN read the queue it must account for"   "SELECT has_table_privilege('iam_v2_owner','public.sync_outbox','SELECT')" t
assert_priv "iam_v2_owner CAN return abandoned records (definer runs as the owner)"   "SELECT has_table_privilege('iam_v2_owner','public.sync_outbox','UPDATE')" t
assert_priv "iam_v2_owner CAN remove delivered records under retention"   "SELECT has_table_privilege('iam_v2_owner','public.sync_outbox','DELETE')" t
assert_priv "the queue's operator functions live in iam_v2"   "SELECT count(*)=4 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('sync_outbox_recover_exhausted','sync_outbox_prune_delivered','sync_outbox_accounting','sync_outbox_recovery_log_append_only')" t
assert_priv "and none was left behind in public"   "SELECT count(*)=0 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname LIKE 'sync_outbox!_%' ESCAPE '!'" t

# CLOUD OPERATING MODE (0071). The decision is "Central serves this appliance for licensing only", and the
# requirement is that it cannot silently reactivate. Two halves are asserted: nobody with a runtime role can
# change the mode, and the daemon the mode governs cannot rewrite it.
assert_priv "scd may READ the mode it is subject to"   "SELECT has_function_privilege('svc_scd','iam_v2.cloud_mode_get(uuid,uuid)','EXECUTE')" t
assert_priv "scd CANNOT change the mode it is subject to"   "SELECT has_function_privilege('svc_scd','iam_v2.cloud_mode_set(uuid,uuid,text,text,text)','EXECUTE')" f
assert_priv "scd CANNOT write the mode table directly"   "SELECT has_table_privilege('svc_scd','iam_v2.site_cloud_mode','UPDATE')" f
assert_priv "edged may READ the mode, to show it"   "SELECT has_function_privilege('svc_edged','iam_v2.cloud_mode_get(uuid,uuid)','EXECUTE')" t
assert_priv "edged CANNOT change it: this decision has no UI switch"   "SELECT has_function_privilege('svc_edged','iam_v2.cloud_mode_set(uuid,uuid,text,text,text)','EXECUTE')" f
assert_priv "edged CANNOT write the mode change log directly"   "SELECT has_table_privilege('svc_edged','iam_v2.cloud_mode_changes','INSERT')" f
assert_priv "PUBLIC holds nothing on the mode"   "SELECT has_table_privilege('public','iam_v2.site_cloud_mode','SELECT')" f

assert_priv "PUBLIC holds nothing on the retention setting"   "SELECT has_table_privilege('public','iam_v2.site_cloud_sync_settings','SELECT')" f
assert_priv "PUBLIC cannot recover the queue"   "SELECT has_function_privilege('public','iam_v2.sync_outbox_recover_exhausted(text,text,integer)','EXECUTE')" f

[ "$fails" = "0" ] || { echo "  FAIL: $fails privilege assertion(s) — fix deploy/gatep, not this file"; exit 1; }

PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$C")"
export PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:${PORT}/${DB}?sslmode=disable"
# ONLY the prodprivilege tests. -run Integration pulled in every other cmd/scd integration suite, whose
# fixtures build their own schema rather than the baseline and so fail on constraints the real schema has
# (tenants.slug NOT NULL, among others). Those suites are the DARK harness's job; this one exists solely to
# exercise the real service role against the real Gate-P grants.
echo "== go test -run TestIntegration_Phase3Grant_ -tags 'integration prodprivilege' ./cmd/scd =="
( cd "$ROOT/data-plane" && go test -tags "integration prodprivilege" -run 'TestIntegration_Phase3Grant_' -timeout 15m ./cmd/scd/ -count=1 )
echo "PROD_PRIVILEGE = PASS"
