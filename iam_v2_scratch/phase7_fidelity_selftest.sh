#!/usr/bin/env bash
# PHASE-7 — MUTATION SUITE FOR THE SEMANTIC FIDELITY PROOF.
#
# The fidelity digest is the basis for the claim "this scratch database IS the accepted appliance schema".
# Everything downstream -- every gate result, every diagnosis of a failure as product or environment -- rests
# on it. So it has to be shown to BREAK when the schema materially changes while every object name stays
# exactly as it was. That is the failure mode a name-list fingerprint cannot see, and the earlier one did not:
# it reported an identical digest for a database whose function bodies differed in 46 places.
#
# Each case mutates a load-bearing definition WITHOUT renaming anything, checks the digest moved, and restores
# the database. It runs against a disposable copy, never the accepted baseline itself.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"
C="${PHASE7_CONTAINER:-iamv2-p6}"
SRC="${PHASE7_ACCEPTED_DB:-iam_accepted}"
TMPDB="p7_fidelity_mutation"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
x(){ docker exec -i "$C" psql -U postgres -d "$TMPDB" -tAqc "$1" </dev/null 2>&1; }
digest(){ docker exec -i "$C" psql -U postgres -d "$TMPDB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1; }

cleanup(){ docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $TMPDB" </dev/null >/dev/null 2>&1; }
trap cleanup EXIT INT TERM

echo "== Phase-7 fidelity proof: mutation suite =="
cleanup
docker exec -i "$C" psql -U postgres -d postgres -qc "CREATE DATABASE $TMPDB TEMPLATE $SRC" </dev/null >/dev/null 2>&1 \
  || { echo "cannot clone $SRC (is anything connected to it?)"; exit 2; }

BASE="$(digest)"
case "$BASE" in
  *parts=*) ok "the disposable clone reproduces a digest: $BASE" ;;
  *) no "could not compute a baseline digest" "$BASE"; exit 1 ;;
esac

# mutate <label> <sql-that-breaks-something>
#
# EACH CASE STARTS FROM A FRESH CLONE. The first version applied a hand-written inverse afterwards and checked
# the digest came back -- which failed for six cases out of eight, not because the digest was wrong but because
# an inverse written by hand is not the same object as the original: re-adding a CHECK or an index reproduces
# the behaviour, not necessarily the identical definition. Rebuilding from the template is exact, needs no
# inverse to be written or trusted, and makes every case independent of the ones before it. It also exercises
# the reproducibility claim eight more times.
reclone(){
  docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $TMPDB" </dev/null >/dev/null 2>&1
  docker exec -i "$C" psql -U postgres -d postgres -qc "CREATE DATABASE $TMPDB TEMPLATE $SRC" </dev/null >/dev/null 2>&1
}

mutate(){
  local label="$1" break_sql="$2" err after
  reclone
  if [ "$(digest)" != "$BASE" ]; then
    no "$label" "the fresh clone did not start from the baseline digest"; return
  fi
  err="$(x "$break_sql")"
  case "$err" in *ERROR*) no "$label" "the mutation itself failed: $(printf '%s' "$err" | head -1 | cut -c1-90)"; return ;; esac
  after="$(digest)"
  if [ "$after" = "$BASE" ]; then
    no "$label" "THE DIGEST DID NOT MOVE -- the proof cannot see this change"
  else
    ok "$label"
  fi
}

# ---- 1. a SECURITY DEFINER body replaced, name and signature untouched -------------------------------------
# The case the name-list fingerprint missed 46 times over. Nothing about the function's identity changes.
mutate "a definer function's BODY is replaced (same name, same signature)" \
  "CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) RETURNS timestamptz
     LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS \$\$ SELECT NULL::timestamptz \$\$;"

# ---- 2. a CHECK constraint rewritten to admit what it used to refuse ----------------------------------------
mutate "a CHECK is widened to admit a value it refused (same constraint name)" \
  "ALTER TABLE iam_v2.guest_device_actions DROP CONSTRAINT guest_device_actions_action_check;
   ALTER TABLE iam_v2.guest_device_actions ADD CONSTRAINT guest_device_actions_action_check
     CHECK (action IN ('LIST','RELEASE','ANYTHING_ELSE'));"

# ---- 3. a column's type changed, name kept -------------------------------------------------------------------
mutate "a column's TYPE changes (same column name)" \
  "ALTER TABLE iam_v2.entitlements ALTER COLUMN consumed_online_seconds TYPE numeric;"

# ---- 4. a NOT NULL dropped -----------------------------------------------------------------------------------
mutate "a NOT NULL is dropped (same column name)" \
  "ALTER TABLE iam_v2.entitlements ALTER COLUMN status DROP NOT NULL;"

# ---- 5. an index loses its uniqueness -------------------------------------------------------------------------
mutate "a UNIQUE index becomes non-unique (same index name)" \
  "DROP INDEX iam_v2.ent_live_stay;
   CREATE INDEX ent_live_stay ON iam_v2.entitlements (stay_id)
     WHERE status = ANY (ARRAY['PENDING'::text,'ACTIVE'::text,'SUSPENDED'::text]);"

# ---- 6. PUBLIC gains execute on a definer function --------------------------------------------------------------
# The privilege regression that matters most, and the one an ACL-TEXT comparison would have missed when the
# grant merely restored a default.
mutate "PUBLIC gains EXECUTE on a definer function" \
  "GRANT EXECUTE ON FUNCTION iam_v2.p6_data_crossing(uuid) TO PUBLIC;"

# ---- 7. a service role gains a write it must never have -----------------------------------------------------------
mutate "svc_acctd gains UPDATE on entitlements" \
  "GRANT UPDATE ON iam_v2.entitlements TO svc_acctd;"

# ---- 8. a trigger repointed at a different function ------------------------------------------------------------
mutate "a trigger is repointed at a different function (same trigger name)" \
  "DROP TRIGGER p6_setting_changes_append_only ON iam_v2.appliance_product_setting_changes;
   CREATE TRIGGER p6_setting_changes_append_only BEFORE UPDATE OR DELETE
     ON iam_v2.appliance_product_setting_changes
     FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_guest_device_actions_append_only();"

# ---- 9. and the control: a change the digest SHOULD ignore ------------------------------------------------------
# Fidelity must not be so brittle that ordinary data movement fails it, or nobody will be able to tell a real
# regression from noise. Inserting a row changes no definition.
before="$(digest)"
x "INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) ON CONFLICT DO NOTHING" >/dev/null
[ "$(digest)" = "$before" ] && ok "CONTROL: inserting DATA does not move the schema digest" \
  || no "CONTROL" "the digest moved for a data change; it is fingerprinting the wrong thing"

echo "------------------------------------------------------------"
printf 'PHASE7_FIDELITY_SELFTEST pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
