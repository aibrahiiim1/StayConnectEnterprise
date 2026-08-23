#!/usr/bin/env bash
# IS A GATE-P RECONCILE SAFE TO RUN AGAINST PRODUCTION?
#
# gatep-grant-survives-reconcile.sh answers "do the right privileges come out the other side". This answers the
# prior question: can running it hurt anything. Both matter, and they fail for different reasons, so they are
# separate scripts.
#
# The properties proven here, each of which was a real defect rather than a hypothetical:
#
#   ATOMIC      a reconcile that fails partway leaves the effective privilege set EXACTLY as it was. Before the
#               file wrapped itself in BEGIN/COMMIT, a failure after the revoke left Production with the revoke
#               applied and the re-grant not — which is what happened when the D32 assertion aborted a run and
#               svc_scd lost the PMS authentication path.
#
#   IDEMPOTENT  running it twice changes nothing the second time.
#
#   D32         the no-direct-grace-mutation invariant passes, with svc_pmsd now inside the reconcile rather
#               than beside it.
#
#   PMSD MODEL  svc_pmsd holds no INSERT/UPDATE/DELETE on the grace config, and reaches the row lock it needs
#               through iam_v2.p3_lock_grace_config instead.
#
#   NO PUBLIC   the authentication predicates are not world-executable.
#
#   STAGING     the runner refuses a stale or incomplete source set BEFORE opening a transaction.
#
# EXIT CODES: 0 pass · 1 a property failed (deterministic) · 2 the disposable cluster could not be built.

set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GP="$ROOT/deploy/gatep"
C="gatep-acceptance-$$"
DB="stayconnect_site"

FAIL=0
ok(){  echo "  ok: $*"; }
bad(){ echo "  *** FAIL: $*"; FAIL=$((FAIL+1)); }

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; rm -rf "${FAKE:-/nonexistent}"; }
trap cleanup EXIT

q(){ docker exec "$C" psql -tAX -U postgres -d "$DB" -c "$1" 2>/dev/null | tr -d '[:space:]'; }
qraw(){ docker exec "$C" psql -tAX -U postgres -d "$DB" -c "$1" 2>/dev/null; }

# The full ACL surface, ordered, as one comparable blob. Comparing whole snapshots rather than a chosen list is
# the point of the rollback test: a change anywhere must show up, including somewhere nobody thought to check.
acl_snapshot(){
  docker exec "$C" psql -tAX -U postgres -d "$DB" -c "
    SELECT string_agg(x, E'\n' ORDER BY x) FROM (
      SELECT n.nspname||'.'||c.relname||'='||COALESCE(c.relacl::text,'-') AS x
        FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
       WHERE n.nspname IN ('iam_v2','public') AND c.relkind IN ('r','S','v')
      UNION ALL
      SELECT 'fn:'||n.nspname||'.'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||')='
             ||COALESCE(p.proacl::text,'-')
        FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
       WHERE n.nspname IN ('iam_v2','public')
      UNION ALL
      SELECT 'schema:'||nspname||'='||COALESCE(nspacl::text,'-') FROM pg_namespace
       WHERE nspname IN ('iam_v2','public')
    ) s;" 2>/dev/null
}

echo "== disposable cluster with the real schema and the real Gate-P roles =="
if ! CLEANROOM_KEEP=1 CLEANROOM_NAME="$C" bash "$ROOT/scripts/clean-install-reconstruction.sh" >/tmp/ga-build.log 2>&1; then
  echo "INFRA: could not build the cleanroom"; tail -20 /tmp/ga-build.log; exit 2
fi
docker inspect "$C" >/dev/null 2>&1 || { echo "INFRA: no cleanroom container"; exit 2; }
ok "cleanroom built"

# ---------------------------------------------------------------------------------------------------------
echo
echo "== 1. a full reconcile succeeds, and is idempotent =="
# Applied through the real runner so the staging and hash verification are exercised too, not bypassed.
if ! bash "$ROOT/scripts/gatep-reconcile.sh" --container "$C" --db "$DB" >/tmp/ga-r1.log 2>&1; then
  bad "the first reconcile failed"; tail -25 /tmp/ga-r1.log
else
  ok "first reconcile completed"
fi
SNAP1="$(acl_snapshot)"
if ! bash "$ROOT/scripts/gatep-reconcile.sh" --container "$C" --db "$DB" >/tmp/ga-r2.log 2>&1; then
  bad "the second reconcile failed"; tail -25 /tmp/ga-r2.log
fi
SNAP2="$(acl_snapshot)"
if [ "$SNAP1" = "$SNAP2" ]; then ok "idempotent: the second reconcile changed no ACL"
else bad "the second reconcile changed the ACL set"; diff <(echo "$SNAP1") <(echo "$SNAP2") | head -10; fi

# ---------------------------------------------------------------------------------------------------------
echo
echo "== 2. every runtime role keeps what it needs =="
while IFS='|' read -r role obj kind priv; do
  [ -z "$role" ] && continue
  if [ "$kind" = fn ]; then held="$(q "SELECT has_function_privilege('$role','$obj','$priv')")"
  else                     held="$(q "SELECT has_table_privilege('$role','$obj','$priv')")"; fi
  if [ "$held" = "t" ]; then ok "$role $priv $obj"; else bad "$role LOST $priv on $obj"; fi
done <<'PAIRS'
svc_scd|iam_v2.issue_or_return_pms_context(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid, integer)|fn|EXECUTE
svc_scd|iam_v2.record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamptz)|fn|EXECUTE
svc_scd|iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz)|fn|EXECUTE
svc_scd|iam_v2.p3_cfg_secs(jsonb, text, int)|fn|EXECUTE
svc_acctd|iam_v2.p6_data_crossing(uuid)|fn|EXECUTE
svc_acctd|iam_v2.p6_expire_entitlement(uuid)|fn|EXECUTE
svc_acctd|iam_v2.p6_suspend_over_budget(uuid, uuid)|fn|EXECUTE
svc_acctd|iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[])|fn|EXECUTE
svc_netd|iam_v2.record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamptz)|fn|EXECUTE
svc_pmsd|iam_v2.p3_lock_grace_config(uuid, uuid)|fn|EXECUTE
svc_pmsd|iam_v2.site_checkout_grace_config|tbl|SELECT
svc_pmsd|iam_v2.stays|tbl|SELECT
svc_pmsd|iam_v2.stay_events|tbl|SELECT
svc_edged|iam_v2.site_checkout_grace_config|tbl|SELECT
PAIRS
for r in svc_scd svc_edged svc_acctd svc_netd svc_pmsd; do
  [ "$(q "SELECT has_schema_privilege('$r','iam_v2','USAGE')")" = "t" ] || bad "$r lost USAGE on schema iam_v2"
  [ "$(q "SELECT has_schema_privilege('$r','public','USAGE')")" = "t" ] || bad "$r lost USAGE on schema public"
done
ok "schema USAGE intact for all five runtime roles"

# ---------------------------------------------------------------------------------------------------------
echo
echo "== 3. the pmsd privilege model: lock without write =="
for p in INSERT UPDATE DELETE; do
  if [ "$(q "SELECT has_table_privilege('svc_pmsd','iam_v2.site_checkout_grace_config','$p')")" = "t" ]; then
    bad "svc_pmsd still holds $p on site_checkout_grace_config — D32 forbids it and the lock no longer needs it"
  fi
done
ok "svc_pmsd has no direct grace-config mutation path"
[ "$(q "SELECT prosecdef FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p3_lock_grace_config'")" = "t" ] \
  && ok "p3_lock_grace_config is SECURITY DEFINER" || bad "p3_lock_grace_config is not SECURITY DEFINER"
[ "$(q "SELECT pg_get_userbyid(p.proowner) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p3_lock_grace_config'")" = "iam_v2_owner" ] \
  && ok "p3_lock_grace_config is owned by iam_v2_owner" || bad "p3_lock_grace_config has the wrong owner"

# ---------------------------------------------------------------------------------------------------------
echo
echo "== 4. PUBLIC holds no authentication or lock predicate =="
for fn in 'iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz)' \
          'iam_v2.p3_cfg_secs(jsonb, text, int)' \
          'iam_v2.p3_lock_grace_config(uuid, uuid)'; do
  if [ "$(q "SELECT has_function_privilege('public','$fn','EXECUTE')")" = "t" ]; then
    bad "PUBLIC can EXECUTE $fn"
  else ok "PUBLIC cannot EXECUTE $fn"; fi
done

# ---------------------------------------------------------------------------------------------------------
echo
echo "== 5. ATOMIC: a reconcile that fails after the revoke changes nothing =="
BEFORE="$(acl_snapshot)"
# A copy of the real entry point with a deliberate error injected immediately before COMMIT — i.e. after the
# revoke and after every include has been applied. That is the exact shape of the failure that hurt
# Production: the destructive half done, the restorative half done, and then an abort.
docker exec "$C" rm -rf /tmp/gafail >/dev/null 2>&1
docker cp "$GP" "$C:/tmp/gafail" >/dev/null 2>&1
docker exec "$C" sh -c "sed 's/^COMMIT;\$/DO \$\$ BEGIN RAISE EXCEPTION '\''DELIBERATE ACCEPTANCE FAILURE AFTER REVOKE'\''; END \$\$;\nCOMMIT;/' /tmp/gafail/gatep-grants.sql > /tmp/gafail/gatep-grants-fail.sql" 2>/dev/null
if [ "$(docker exec "$C" sh -c "grep -c 'DELIBERATE ACCEPTANCE FAILURE' /tmp/gafail/gatep-grants-fail.sql" 2>/dev/null | tr -d '[:space:]')" != "1" ]; then
  bad "could not inject the deliberate failure; the atomicity test did not run"
else
  if docker exec "$C" psql -v ON_ERROR_STOP=1 -U postgres -d "$DB" -q -f /tmp/gafail/gatep-grants-fail.sql >/tmp/ga-fail.log 2>&1; then
    bad "the deliberately-broken reconcile SUCCEEDED; the failure was not injected where it matters"
  else
    ok "the broken reconcile failed, as intended"
    AFTER="$(acl_snapshot)"
    if [ "$BEFORE" = "$AFTER" ]; then
      ok "ROLLBACK: every ACL is byte-identical to before the failed run"
    else
      bad "a failed reconcile CHANGED the effective privilege set"
      diff <(echo "$BEFORE") <(echo "$AFTER") | head -20
    fi
  fi
fi
docker exec "$C" rm -rf /tmp/gafail >/dev/null 2>&1

# ---------------------------------------------------------------------------------------------------------
echo
echo "== 6. STAGING: a stale or incomplete source set is refused before any database change =="
FAKE="$(mktemp -d "${TMPDIR:-/tmp}/gatep-fake-XXXXXX")"
mkdir -p "$FAKE/scripts" "$FAKE/deploy/gatep"
cp "$ROOT/scripts/gatep-reconcile.sh" "$FAKE/scripts/"
cp "$GP"/*.sql "$GP"/*.sh "$FAKE/deploy/gatep/" 2>/dev/null

if bash "$FAKE/scripts/gatep-reconcile.sh" --dsn "postgres://unused" --dry-run >/tmp/ga-s1.log 2>&1; then
  ok "an intact source set passes verification"
else
  bad "an intact source set was refused"; tail -5 /tmp/ga-s1.log
fi

# Remove one included file: the runner must refuse, naming it, without contacting a database.
MISSING="$(grep -E '^\\ir[[:space:]]+' "$GP/gatep-grants.sql" | awk '{print $2}' | tail -1)"
rm -f "$FAKE/deploy/gatep/$MISSING"
if bash "$FAKE/scripts/gatep-reconcile.sh" --container "$C" --db "$DB" >/tmp/ga-s2.log 2>&1; then
  bad "a source set missing '$MISSING' was accepted"
else
  if grep -q "REFUSED (no database change)" /tmp/ga-s2.log; then
    ok "a missing include is refused before any database change ($MISSING)"
  else
    bad "the run failed but not as a pre-database refusal"; tail -5 /tmp/ga-s2.log
  fi
fi
cp "$GP/$MISSING" "$FAKE/deploy/gatep/$MISSING"

# Tamper with an include so its bytes differ from the entry point's revision: the stale-file case exactly.
printf '\n-- tampered\n' >> "$FAKE/deploy/gatep/$MISSING"
BEFORE_T="$(acl_snapshot)"
if bash "$FAKE/scripts/gatep-reconcile.sh" --container "$C" --db "$DB" >/tmp/ga-s3.log 2>&1; then
  bad "a tampered include was accepted"
else
  ok "a modified include is refused (hash mismatch or dirty tree)"
fi
[ "$BEFORE_T" = "$(acl_snapshot)" ] && ok "no ACL changed during the refused runs" || bad "a refused run changed an ACL"

# ---------------------------------------------------------------------------------------------------------
echo
if [ "$FAIL" -eq 0 ]; then echo "GATEP_RECONCILE_ACCEPTANCE = PASS"; exit 0; fi
echo "GATEP_RECONCILE_ACCEPTANCE = FAIL ($FAIL)"
exit 1
