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
# THIS SUITE MUTATES ROLES, AND ROLES ARE CLUSTER-GLOBAL. Run against a shared container it would alter the
# attributes of roles other databases in that cluster depend on -- including the accepted baseline it clones
# from. So it builds its own cluster from repository sources, mutates inside it, and destroys it. Point it at a
# shared container with PHASE7_SELFTEST_CONTAINER only when you know what that costs.
C="${PHASE7_SELFTEST_CONTAINER:-phase7-fidsel}"
SRC="${PHASE7_ACCEPTED_DB:-iam_recon}"
TMPDB="p7_fidelity_mutation"
SELFBUILT=0

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
x(){ docker exec -i "$C" psql -U postgres -d "$TMPDB" -tAqc "$1" </dev/null 2>&1; }
digest(){ docker exec -i "$C" psql -U postgres -d "$TMPDB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1; }

dropdb(){ docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $TMPDB" </dev/null >/dev/null 2>&1; return 0; }
# cleanup is the TRAP, and it tears down the private cluster. It must never be called mid-run: doing so
# destroyed the cluster immediately after building it, and the failure surfaced as "cannot clone".
cleanup(){
  dropdb
  [ "${SELFBUILT:-0}" = "1" ] && docker rm -f "$C" >/dev/null 2>&1
  rm -f "$B4" "$AF" 2>/dev/null
  return 0
}
B4="$(mktemp)"; AF="$(mktemp)"
detail(){ docker exec -i "$C" psql -U postgres -d "$TMPDB" -tAq -v detail=1 < "$HERE/phase7_fidelity.sql" 2>&1 | sort; }
trap cleanup EXIT INT TERM

echo "== Phase-7 fidelity proof: mutation suite =="
if ! docker inspect "$C" >/dev/null 2>&1; then
  echo "-- building a private cluster from repository sources (this is the subject under test) --"
  PHASE7_CONTAINER="$C" PHASE7_KEEP=1 PHASE7_RECON_DB="$SRC" \
    bash "$HERE/phase7_reconstruct_from_sources.sh" >/dev/null 2>&1 \
    || { echo "could not build the private cluster; refusing to test against nothing"; exit 2; }
  SELFBUILT=1
fi
dropdb
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

# mutate <label> <sql-that-breaks-something> <pattern-the-changed-lines-must-match>
#
# A moving digest is NOT enough. A digest that moves for the wrong reason -- a side effect, a dropped object, a
# cascade -- proves only that something happened, and would let a case pass while the property it names stays
# invisible. So each case now names the digest COMPONENT it expects to change, and the changed detail lines must
# match it. That is what "the digest moved for that reason" means, and it is checkable.
mutate(){
  local label="$1" break_sql="$2" want="${3:-.}" err after changed
  reclone
  if [ "$(digest)" != "$BASE" ]; then
    no "$label" "the fresh clone did not start from the baseline digest"; return
  fi
  detail > "$B4"
  err="$(x "$break_sql")"
  case "$err" in *ERROR*) no "$label" "the mutation itself failed: $(printf '%s' "$err" | head -1 | cut -c1-90)"; return ;; esac
  after="$(digest)"
  if [ "$after" = "$BASE" ]; then
    no "$label" "THE DIGEST DID NOT MOVE -- the proof cannot see this change"; return
  fi
  detail > "$AF"
  # strip diff's own '< ' / '> ' markers first, or an anchored pattern can never match a changed line
  changed="$(diff "$B4" "$AF" | grep -E '^[<>]' | sed 's/^[<>] //' || true)"
  if printf '%s' "$changed" | grep -Eq "$want"; then
    ok "$label"
  else
    no "$label" "the digest moved, but not on a '$want' line -- moved for the wrong reason"
  fi
}

# mutate_global <label> <break-sql> <restore-sql> <pattern>
#
# For CLUSTER-GLOBAL properties -- role attributes, role existence -- which a template reclone cannot undo,
# because they do not live in the database being cloned. These cases must restore what they changed and PROVE
# the restoration, or every later case inherits the damage and the suite silently tests a different system.
mutate_global(){
  local label="$1" break_sql="$2" restore_sql="$3" want="${4:-.}" err after changed back
  reclone
  if [ "$(digest)" != "$BASE" ]; then
    no "$label" "the fresh clone did not start from the baseline digest"; return
  fi
  detail > "$B4"
  err="$(x "$break_sql")"
  case "$err" in *ERROR*) no "$label" "the mutation itself failed: $(printf '%s' "$err" | head -1 | cut -c1-90)"; return ;; esac
  after="$(digest)"
  detail > "$AF"
  # strip diff's own '< ' / '> ' markers first, or an anchored pattern can never match a changed line
  changed="$(diff "$B4" "$AF" | grep -E '^[<>]' | sed 's/^[<>] //' || true)"
  x "$restore_sql" >/dev/null
  back="$(digest)"
  if [ "$after" = "$BASE" ]; then
    no "$label" "THE DIGEST DID NOT MOVE -- the proof cannot see this change"
  elif ! printf '%s' "$changed" | grep -Eq "$want"; then
    no "$label" "the digest moved, but not on a '$want' line -- moved for the wrong reason"
  elif [ "$back" != "$BASE" ]; then
    no "$label" "the digest moved back to '$back', not the baseline -- restoration is unproven"
  else
    ok "$label (and the baseline was provably restored)"
  fi
}

# ---- 1. a SECURITY DEFINER body replaced, name and signature untouched -------------------------------------
# The case the name-list fingerprint missed 46 times over. Nothing about the function's identity changes.
mutate "a definer function's BODY is replaced (same name, same signature)" \
  "CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) RETURNS timestamptz
     LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS \$\$ SELECT NULL::timestamptz \$\$;" \
  "^FN:"

# ---- 2. a CHECK constraint rewritten to admit what it used to refuse ----------------------------------------
mutate "a CHECK is widened to admit a value it refused (same constraint name)" \
  "ALTER TABLE iam_v2.guest_device_actions DROP CONSTRAINT guest_device_actions_action_check;
   ALTER TABLE iam_v2.guest_device_actions ADD CONSTRAINT guest_device_actions_action_check
     CHECK (action IN ('LIST','RELEASE','ANYTHING_ELSE'));" \
  "^CON:"

# ---- 3. a column's type changed, name kept -------------------------------------------------------------------
mutate "a column's TYPE changes (same column name)" \
  "ALTER TABLE iam_v2.entitlements ALTER COLUMN consumed_online_seconds TYPE numeric;" \
  "^COL:"

# ---- 4. a NOT NULL dropped -----------------------------------------------------------------------------------
mutate "a NOT NULL is dropped (same column name)" \
  "ALTER TABLE iam_v2.entitlements ALTER COLUMN status DROP NOT NULL;" \
  "^COL:"

# ---- 5. an index loses its uniqueness -------------------------------------------------------------------------
mutate "a UNIQUE index becomes non-unique (same index name)" \
  "DROP INDEX iam_v2.ent_live_stay;
   CREATE INDEX ent_live_stay ON iam_v2.entitlements (stay_id)
     WHERE status = ANY (ARRAY['PENDING'::text,'ACTIVE'::text,'SUSPENDED'::text]);" \
  "^IDX:"

# ---- 6. PUBLIC gains execute on a definer function --------------------------------------------------------------
# The privilege regression that matters most, and the one an ACL-TEXT comparison would have missed when the
# grant merely restored a default.
mutate "PUBLIC gains EXECUTE on a definer function" \
  "GRANT EXECUTE ON FUNCTION iam_v2.p6_data_crossing(uuid) TO PUBLIC;" \
  "^FNEXEC:"

# ---- 7. a service role gains a write it must never have -----------------------------------------------------------
mutate "svc_acctd gains UPDATE on entitlements" \
  "GRANT UPDATE ON iam_v2.entitlements TO svc_acctd;" \
  "^GRT:"

# ---- 8. a trigger repointed at a different function ------------------------------------------------------------
mutate "a trigger is repointed at a different function (same trigger name)" \
  "DROP TRIGGER p6_setting_changes_append_only ON iam_v2.appliance_product_setting_changes;
   CREATE TRIGGER p6_setting_changes_append_only BEFORE UPDATE OR DELETE
     ON iam_v2.appliance_product_setting_changes
     FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_guest_device_actions_append_only();" \
  "^TRG:"

# ---- 9. a CHECK's OPERATOR PRECEDENCE changed, every token identical -----------------------------------------
# ppa_unverified_is_never_live is A OR (B AND C). Regrouped as (A OR B) AND C it contains the same columns, the
# same literals and the same operators in the same order -- and refuses rows it used to accept. A digest that
# normalised parentheses away, as an early version did, would call these two constraints equal.
mutate "a CHECK is REGROUPED, same tokens, different meaning" \
  "ALTER TABLE iam_v2.payment_provider_accounts DROP CONSTRAINT ppa_unverified_is_never_live;
   ALTER TABLE iam_v2.payment_provider_accounts ADD CONSTRAINT ppa_unverified_is_never_live
     CHECK (((provenance = 'CONFIGURED'::text) OR (status = 'DISABLED'::text)) AND (NOT is_default));" \
  "^CON:"

# ---- 10. a SECURITY DEFINER function's search_path repointed --------------------------------------------------
# The body, signature, owner and definer flag all stay exactly as they were. Only proconfig moves -- and a
# definer function whose search_path can be influenced is the classic privilege-escalation shape, so a proof
# that cannot see this is not a security proof.
mutate "a definer function's SET search_path is repointed (body untouched)" \
  "ALTER FUNCTION iam_v2.p6_data_crossing(uuid) SET search_path = public, pg_temp;" \
  "^FN:"

# ---- 11. a SECURITY DEFINER function changes OWNER ------------------------------------------------------------
# Same body, same signature, same proconfig -- and it now executes with a different role's authority. This is
# the single most consequential property that no body comparison can detect.
mutate "a definer function changes OWNER (body and signature untouched)" \
  "ALTER FUNCTION iam_v2.p6_data_crossing(uuid) OWNER TO iam_v2_migrator;" \
  "^OWNFN:"

# ---- 12. a table changes OWNER ---------------------------------------------------------------------------------
# An owner holds every privilege on its table implicitly, so this silently rewrites the effective grant surface
# without touching one ACL entry -- exactly the mechanism behind the 182 grants the reconstruction had to explain.
mutate "a table changes OWNER (definition untouched)" \
  "ALTER TABLE iam_v2.entitlements OWNER TO iam_v2_migrator;" \
  "^OWNTBL:"

# ---- 13. an UNEXPECTED PRINCIPAL gains access ------------------------------------------------------------------
# A role matching none of the svc_/sc_/iam_v2_ prefixes the digest used to watch. Under the old allowlist this
# grant was invisible, which is backwards: an unrecognised grantee is more alarming than a recognised one.
mutate_global "an UNEXPECTED role (matching no known prefix) gains SELECT on entitlements" \
  "CREATE ROLE p7_intruder NOLOGIN; GRANT USAGE ON SCHEMA iam_v2 TO p7_intruder;
   GRANT SELECT ON iam_v2.entitlements TO p7_intruder;" \
  "REVOKE ALL ON iam_v2.entitlements FROM p7_intruder;
   REVOKE ALL ON SCHEMA iam_v2 FROM p7_intruder; DROP ROLE p7_intruder;" \
  "p7_intruder"

# ---- 14. PUBLIC gains a TABLE privilege --------------------------------------------------------------------------
mutate "PUBLIC gains SELECT on a table" \
  "GRANT SELECT ON iam_v2.entitlements TO PUBLIC;" \
  "^GRT:"

# ---- 15..18. the DANGEROUS ROLE ATTRIBUTES, one at a time -------------------------------------------------------
# Each is a distinct escalation and each is proved separately, because a suite that granted all four at once
# would pass even if the digest could only see one of them. Every case restores the attribute and proves the
# restoration -- these live in the cluster, not the database, so a reclone cannot undo them.
mutate_global "svc_acctd gains BYPASSRLS (row-level security defeated)" \
  "ALTER ROLE svc_acctd BYPASSRLS;" "ALTER ROLE svc_acctd NOBYPASSRLS;" "^ROLE:svc_acctd"
mutate_global "svc_acctd gains CREATEROLE (a path to every non-superuser role)" \
  "ALTER ROLE svc_acctd CREATEROLE;" "ALTER ROLE svc_acctd NOCREATEROLE;" "^ROLE:svc_acctd"
mutate_global "svc_acctd gains CREATEDB" \
  "ALTER ROLE svc_acctd CREATEDB;" "ALTER ROLE svc_acctd NOCREATEDB;" "^ROLE:svc_acctd"
mutate_global "svc_acctd gains REPLICATION (it can stream the whole cluster)" \
  "ALTER ROLE svc_acctd REPLICATION;" "ALTER ROLE svc_acctd NOREPLICATION;" "^ROLE:svc_acctd"

# ---- 19. a role MEMBERSHIP granted --------------------------------------------------------------------------------
mutate_global "svc_acctd is made a member of iam_v2_owner" \
  "GRANT iam_v2_owner TO svc_acctd;" "REVOKE iam_v2_owner FROM svc_acctd;" "^MEMBER:"

# ---- 20. THE CONTROL, with its premise verified ---------------------------------------------------------------
# Fidelity must not be so brittle that ordinary data movement fails it, or nobody can tell a regression from
# noise. But the previous control inserted into public.tenants supplying only id -- and tenants requires slug
# and name, so the INSERT errored, zero rows appeared, and "the digest did not move" was true for the wrong
# reason. It proved nothing while reading as a pass. The control now proves the row ARRIVED, proves the digest
# ignored it, then removes it and proves the removal.
reclone
before="$(digest)"
tid="$(x "INSERT INTO public.tenants(id, slug, name) VALUES (gen_random_uuid(), 'p7ctl', 'Phase 7 control')
        RETURNING id")"
case "$tid" in
  *ERROR*|"") no "CONTROL premise" "the insert did not succeed: $(printf '%s' "$tid" | head -1 | cut -c1-90)" ;;
  *)
    if [ "$(x "SELECT count(*) FROM public.tenants WHERE id='$tid'")" = "1" ]; then
      ok "CONTROL premise: the row provably exists (a control cannot rest on a failed insert)"
      [ "$(digest)" = "$before" ] \
        && ok "CONTROL: inserting DATA does not move the schema digest" \
        || no "CONTROL" "the digest moved for a data change; it is fingerprinting the wrong thing"
      x "DELETE FROM public.tenants WHERE id='$tid'" >/dev/null
      [ "$(x "SELECT count(*) FROM public.tenants WHERE id='$tid'")" = "0" ] \
        && ok "CONTROL cleanup: the row provably no longer exists" \
        || no "CONTROL cleanup" "the control row survived; later cases would inherit it"
    else
      no "CONTROL premise" "the insert reported success but the row is not there"
    fi ;;
esac

echo "------------------------------------------------------------"
printf 'PHASE7_FIDELITY_SELFTEST pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
