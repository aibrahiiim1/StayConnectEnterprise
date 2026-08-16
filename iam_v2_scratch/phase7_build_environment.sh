#!/usr/bin/env bash
# PHASE-7 — BUILD THE COMPLETE PHASE-2-THROUGH-6 GATE ENVIRONMENT, REPRODUCIBLY.
#
# The matrix used to run against a database called iam_full that somebody had built by hand, months earlier,
# in a long-lived shared container. Nothing in the repository could recreate it, nothing recorded what was in
# it, and it had quietly fallen behind: it was missing the deterministic fixture entirely, so six Phase-6 cases
# failed on foreign keys to an appliance and an operator that did not exist. Those failures read as product
# defects. They were an environment nobody could rebuild.
#
# THE ENVIRONMENT IS NOW BUILT FROM THE SAME SOURCES THE PRODUCT SHIPS.
#
#   1. phase7_reconstruct_from_sources.sh applies the accepted history -- 0001-0008, the Phase-1A role model,
#      mg0-mg9, Gate P, the sc_* role bootstrap, 0009-0047 -- into a FRESH, PRIVATE cluster, and proves the
#      result equals the appliance under the semantic fidelity digest.
#   2. phase7_fixture.sql adds the deterministic test rows the gates address by fixed UUID.
#   3. The digest is taken again, and must NOT have moved: test data is not schema. If seeding the fixture
#      changed a definition, the fixture is doing something it has no business doing.
#
# So the environment is reproducible, its cluster is private (roles are cluster-global; sharing one makes role
# assertions compare a database against itself), and its provenance is the repository rather than somebody's
# memory of a docker command.
#
#   usage: phase7_build_environment.sh                 # builds phase7-env / iam_full and leaves it running
#          PHASE7_ENV_CONTAINER=... PHASE7_ENV_DB=...
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"

C="${PHASE7_ENV_CONTAINER:-phase7-env}"
# The name is not cosmetic: lib.sh refuses any database whose name does not begin with "iam_scratch",
# which is how the harness tells a disposable target from a live one. An environment called iam_full could not
# be addressed by the gates built on that library at all.
DB="${PHASE7_ENV_DB:-iam_scratch_full}"
ORACLE="${PHASE7_ORACLE_DIGEST:-}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
digest(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1; }

echo "== Phase 7: building the complete gate environment ($C / $DB) =="

# The reconstruction owns the container's lifecycle and refuses one it did not create, so a stale environment
# from a previous run is removed here -- deliberately, by the script that is about to replace it -- rather
# than by the reconstruction silently adopting it.
if docker inspect "$C" >/dev/null 2>&1; then
  if [ "$(docker inspect "$C" --format '{{index .Config.Labels "phase7-disposable"}}' 2>/dev/null)" = "1" ]; then
    docker rm -f "$C" >/dev/null 2>&1
  else
    echo "REFUSED: '$C' exists and is not a phase7 disposable container. Not touching it." >&2
    exit 2
  fi
fi

# The environment publishes a loopback port because gates built on lib.sh refuse a container that has none;
# it is bound to 127.0.0.1 only, which is what that safety check exists to require.
out="$(PHASE7_CONTAINER="$C" PHASE7_KEEP=1 PHASE7_RECON_DB="$DB" PHASE7_ORACLE_DIGEST="$ORACLE" \
       PHASE7_PORT="${PHASE7_ENV_PORT:-55440}" \
       bash "$HERE/phase7_reconstruct_from_sources.sh" 2>&1)"
rc=$?
build="$(printf '%s' "$out" | grep -E '^RECONSTRUCTION_BUILD' | head -1)"
fid="$(printf '%s' "$out"   | grep -E '^APPLIANCE_FIDELITY'   | head -1)"
[ -n "$build" ] || { printf '%s\n' "$out" | tail -20; echo "the reconstruction produced no verdict"; exit 2; }
echo "  $build"
echo "  $fid"
case "$build" in *"= PASS"*) ok "the environment was rebuilt from repository sources" ;;
                 *) no "the reconstruction did not build cleanly" "$build" ;; esac
case "$fid" in *"= FAIL"*) no "the rebuilt environment contradicts the appliance oracle" "$fid" ;;
               *) : ;; esac
[ "$rc" -eq 0 ] || no "the reconstruction exited non-zero" "rc=$rc"

BEFORE="$(digest)"
case "$BEFORE" in *parts=*) ok "the rebuilt schema yields a digest: $BEFORE" ;;
                  *) no "no digest from the rebuilt schema" "$BEFORE"; exit 1 ;; esac

# ---- the deterministic fixture ---------------------------------------------------------------------------------
fixerr="$(docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 < "$HERE/phase7_fixture.sql" 2>&1 >/dev/null)"
[ -z "$fixerr" ] && ok "the deterministic Phase-7 fixture applied without error" \
  || no "the fixture failed" "$(printf '%s' "$fixerr" | grep -i '^ERROR' | head -1 | cut -c1-120)"

# The gates address these by fixed UUID, so their presence is the environment's contract with them.
eq "fixture tenant 1111... present"    "$(q "SELECT count(*) FROM public.tenants    WHERE id='11111111-1111-1111-1111-111111111111'")" "1"
eq "fixture site 2222... present"      "$(q "SELECT count(*) FROM public.sites      WHERE id='22222222-2222-2222-2222-222222222222'")" "1"
eq "fixture network 3333... present"   "$(q "SELECT count(*) FROM public.guest_networks WHERE id='33333333-3333-3333-3333-333333333333'")" "1"
eq "fixture appliance 4444... present" "$(q "SELECT count(*) FROM public.appliances WHERE id='44444444-4444-4444-4444-444444444444'")" "1"
eq "fixture operator 5555... present"  "$(q "SELECT count(*) FROM public.operators  WHERE id='55555555-5555-5555-5555-555555555555'")" "1"

# ---- and the fixture must be DATA, not schema --------------------------------------------------------------------
AFTER="$(digest)"
eq "the fixture changed no schema definition (the digest did not move)" "$AFTER" "$BEFORE"

# ---- the disposable marker the scratch-safety library requires ------------------------------------------------
#
# lib.sh refuses to touch a database that cannot prove it was created as scratch: it reads public._scratch_marker
# and compares the value. Creating it here is what lets phase4_db_invariants and the other guarded gates address
# this environment. It lives in public and is invisible to the iam_v2 fidelity digest, so it changes no claim.
docker exec -i "$C" psql -U postgres -d "$DB" -qc \
  "CREATE TABLE IF NOT EXISTS public._scratch_marker(marker text PRIMARY KEY);
   INSERT INTO public._scratch_marker VALUES ('DISPOSABLE_SCRATCH_ONLY') ON CONFLICT DO NOTHING;" </dev/null >/dev/null 2>&1
eq "the disposable-scratch marker is present, so the guarded gates may address this environment" \
   "$(q "SELECT marker FROM public._scratch_marker LIMIT 1")" "DISPOSABLE_SCRATCH_ONLY"
eq "...and the marker did not disturb the schema digest" "$(digest)" "$BEFORE"

echo "------------------------------------------------------------"
printf 'PHASE7_BUILD_ENVIRONMENT container=%s db=%s pass=%d fail=%d\n' "$C" "$DB" "$pass" "$fail"
[ "$fail" -eq 0 ]
