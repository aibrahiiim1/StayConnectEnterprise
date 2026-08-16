#!/usr/bin/env bash
# PHASE-7 — THE COMPLETE MATRIX, AS ONE RUNNABLE THING WITH ONE HONEST VERDICT.
#
# The FINAL contract §18 gives Phase 7 the gate "complete matrix". Until this existed, that matrix was a list
# of scripts with six different invocation conventions -- one refuses to start without an environment variable
# set, another names its container inline -- and a gate that is hard to run is a gate that stops being run.
#
# STRICT MODE IS THE POINT, and it is the default.
#
# A matrix runner is the last thing that should be generous, because it is the single line somebody reads
# before saying "the system passes". Two false-green paths existed here and both are closed:
#
#   1. A SKIPPED gate incremented a counter and then had no effect on the exit status, which depended only on
#      the parsed failure total. A matrix missing half its gates exited 0.
#   2. The CHILD PROCESS EXIT STATUS was never consulted. A gate that crashed after printing a plausible last
#      line -- or whose last line came from something other than its verdict -- was read as passing.
#
# In strict mode the run fails, non-zero, on ANY of:
#
#   * a gate script that does not exist;
#   * a required container that is missing or not running;
#   * any gate skipped for any reason;
#   * a gate whose verdict cannot be parsed;
#   * a gate whose child process exited non-zero, whatever it printed;
#   * any parsed fail count above zero;
#   * an expected gate that never executed (the roster is checked against what ran);
#   * an overlapping run (the exclusive lock).
#
# --lenient exists for a developer iterating on one gate. It prints the same lines and the same reasons; it
# only stops the run from failing. Acceptance evidence is strict-mode output, and the mode is printed in the
# verdict line so a lenient run can never be quoted as one.
#
#   usage:  phase7_full_matrix.sh                  strict, every gate
#           phase7_full_matrix.sh --phase7         strict, the Phase-7 composition gates only
#           phase7_full_matrix.sh --lenient        do not fail on skips/verdict problems (NOT acceptance)
#
# It contacts no appliance, no Production database, no PMS and no provider.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"

MODE=strict
ONLY_PHASE7=0
for a in "$@"; do
  case "$a" in
    --lenient) MODE=lenient ;;
    --strict)  MODE=strict ;;
    --phase7)  ONLY_PHASE7=1 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

# ONE RUN AT A TIME, ENFORCED. The matrix includes phase6_backup_restore, which DROPS the scratch database and
# restores it. Run it while anything else is touching that database and both runs produce numbers that
# describe neither -- which is exactly what happened: two matrix runs overlapped, every gate reported
# failures, and not one of those failures was real. The lock is a directory, not advice in a comment, because
# advice does not stop the second terminal.
LOCK="${TMPDIR:-/tmp}/phase7_full_matrix.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  echo "REFUSED: another full-matrix run holds $LOCK." >&2
  echo "  The matrix drops and restores the scratch database; two concurrent runs corrupt each other and" >&2
  echo "  produce failures that are artifacts. Wait for it, or remove the lock if you are certain it is stale." >&2
  exit 2
fi
trap 'rmdir "$LOCK" 2>/dev/null' EXIT INT TERM

total_pass=0; total_fail=0; ran=0; skipped=0; broken=0
EXECUTED=""
line(){ printf '%s\n' "------------------------------------------------------------"; }
have(){ docker inspect "$1" >/dev/null 2>&1; }

# THE ROSTER. Naming the gates that MUST run is what turns "nothing failed" into "everything was checked".
# Without it, deleting a gate file is indistinguishable from a clean run.
EXPECTED_ALL="phase3_lifecycle phase4_financial phase4_db_invariants phase4_least_privilege \
phase5_foundation phase5_least_privilege phase7_environment phase4_least_privilege_full phase6_foundation phase6_device_self_service \
phase6_aggregate_online_time phase6_least_privilege phase6_backup_restore phase6_rollback_rehearsal \
phase7_m1 phase7_m2 phase7_m3 phase7_reconstruct phase7_fidelity_selftest phase7_ledger"
EXPECTED_PHASE7="phase7_m1 phase7_m2 phase7_m3 phase7_reconstruct phase7_fidelity_selftest phase7_ledger"

# run <key> <label> <script> [env assignments...]
run(){
  local key="$1" label="$2" script="$3"; shift 3
  local need="${NEED_CONTAINER:-}"

  if [ ! -f "$HERE/$script" ]; then
    printf '  %-44s MISSING GATE (%s)\n' "$label" "$script"; skipped=$((skipped+1)); return
  fi
  if [ -n "$need" ] && ! have "$need"; then
    printf '  %-44s SKIPPED (container %s is not running)\n' "$label" "$need"; skipped=$((skipped+1)); return
  fi

  local out rc last p f
  out="$(env "$@" bash "$HERE/$script" 2>&1)"; rc=$?
  last="$(printf '%s' "$out" | tail -1)"

  # CASE-INSENSITIVE, because the gates do not agree on capitalisation: Phase 6 prints `pass=50 fail=0` and
  # Phase 5 prints `PASS=70 FAIL=2`. Matching only lowercase once let a gate carrying two failures be reported
  # as passing -- the same class of error this phase exists to catch, except that in a runner it hides every
  # gate at once instead of one.
  p="$(printf '%s' "$last" | grep -oiE 'pass=[0-9]+' | head -1 | cut -d= -f2)"
  f="$(printf '%s' "$last" | grep -oiE 'fail=[0-9]+' | head -1 | cut -d= -f2)"

  EXECUTED="$EXECUTED $key"; ran=$((ran+1))

  if [ -z "${p:-}" ]; then
    printf '  %-44s NO VERDICT (rc=%d) :: %s\n' "$label" "$rc" "$(printf '%s' "$last" | cut -c1-58)"
    broken=$((broken+1)); return
  fi
  total_pass=$((total_pass+p)); total_fail=$((total_fail+${f:-0}))

  # THE CHILD'S EXIT STATUS IS PART OF THE VERDICT, not a second opinion. A gate can print `fail=0` on its
  # last line and still have died -- a trap firing, an abort after the summary, a shell error on the way out.
  # Reading only the text believes the gate's account of itself over the operating system's.
  if [ "$rc" -ne 0 ]; then
    printf '  %-44s pass=%-4s fail=%-3s rc=%d  <-- CHILD EXITED NON-ZERO\n' "$label" "$p" "${f:-0}" "$rc"
    broken=$((broken+1)); return
  fi
  if [ "${f:-0}" = "0" ]; then printf '  %-44s pass=%-4s fail=%s\n' "$label" "$p" "${f:-0}"
  else                          printf '  %-44s pass=%-4s fail=%s   <-- FAILED\n' "$label" "$p" "${f:-0}"; fi
}

# rebuild_env -- restore the gate environment to its freshly built state.
#
# Several gates SEED. Two others -- backup/restore and the rollback rehearsal -- can only tell the truth about a
# database whose data satisfies its own constraints, and the seeding gates leave rows behind that do not (they
# insert under session_replication_role=replica to skip building the parent chain). Run in the wrong order,
# pg_restore fails on a foreign key nobody touched and it reads as a backup defect. So the order is deliberate:
# the two data-sensitive gates run against a pristine environment, and the environment is rebuilt between them.
#
# A failed rebuild is NOT a gate result and must never be silent -- every gate after it would be measuring the
# wrong database.
rebuild_env(){
  if ! PHASE7_ORACLE_DIGEST="${PHASE7_ORACLE_DIGEST:-}" PHASE7_ENV_CONTAINER="${P6_CONTAINER:-phase7-env}" \
       PHASE7_ENV_DB="${P6_DB:-iam_scratch_full}" bash "$HERE/phase7_build_environment.sh" >/dev/null 2>&1; then
    echo "  !! THE GATE ENVIRONMENT COULD NOT BE REBUILT -- refusing to run further gates against a stale one" >&2
    broken=$((broken+1))
    return 1
  fi
  return 0
}

echo "== Phase 7: the complete acceptance matrix (mode: $MODE) =="
echo

if [ "$ONLY_PHASE7" = "0" ]; then
  echo "-- Phase 3: stay domain, resolution, checkout grace (contract D, F) --"
  # SELF-BUILDING GATES GET NO CONTAINER REQUIREMENT. phase3 and phase4-financial create and destroy
  # their own disposable container, and each builds the schema of ITS OWN ERA -- phase3 asserts a
  # 49-table Phase-2-era iam_v2 and would fail against the complete Phase-6 schema. Demanding a
  # pre-existing container for them was a defect in this runner: it reported SKIPPED for gates that
  # were perfectly able to run, and in strict mode a skip is a failure. Era-scoped is not stale.
  run phase3_lifecycle "phase3 lifecycle (0010)" \
      phase3_0010_lifecycle.sh IGNORE=1

  echo "-- Phase 4: financial core, DARK (contract E) --"
  # KEEP the container: phase4_db_invariants and phase4_least_privilege run against it, and this gate used to
  # destroy it on the way out -- so both dependants reported SKIPPED and strict mode failed on the skips alone.
  # The matrix asked for the container to survive, so the matrix destroys it, below.
  run phase4_financial "phase4 financial (0011)" \
      phase4_0011_financial.sh PHASE4_KEEP=1
  # SCRATCH_PORT_ALLOW is not optional. The invariants gate refuses to touch a container that is not on the
  # port it was told to expect -- a deliberate safety check against pointing it at a real database -- and the
  # runner never passed one, so the gate aborted with rc=90 and no verdict on every matrix run. The phase4
  # gate builds on 55433; the default here must be the same number or the abort simply returns.
  NEED_CONTAINER="${P4_CONTAINER:-iamv2-p4gate}" run phase4_db_invariants "phase4 db invariants" \
      phase4_db_invariants.sh SCRATCH_CONTAINER="${P4_CONTAINER:-iamv2-p4gate}" \
      SCRATCH_DB="${P4_DB:-iam_scratch}" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
      SCRATCH_PORT_ALLOW="${P4_PORT:-55433}"
  NEED_CONTAINER="${P4_CONTAINER:-iamv2-p4gate}" run phase4_least_privilege "phase4 least privilege" \
      phase4_least_privilege.sh PHASE4_LP_CONTAINER="${P4_CONTAINER:-iamv2-p4gate}" \
      PHASE4_LP_DB="${P4_DB:-iam_scratch}" SCRATCH_PORT_ALLOW="${P4_PORT:-55433}"
  # ...and now that its dependants have run, the matrix cleans up what it asked to be kept.
  docker rm -f "${P4_CONTAINER:-iamv2-p4gate}" >/dev/null 2>&1 || true

  echo "-- Phase 5: post-stay and cross-PMS transfer (contract F8, F9) --"
  NEED_CONTAINER="${P5_CONTAINER:-iamv2-p5}" run phase5_foundation "phase5 foundation (0027)" \
      phase5_0027_foundation.sh IGNORE=1
  NEED_CONTAINER="${P5_CONTAINER:-iamv2-p5}" run phase5_least_privilege "phase5 least privilege" \
      phase5_least_privilege.sh IGNORE=1

  echo "-- Phase 6: guest device self-service + aggregate online time --"
  # THE ENVIRONMENT IS ITSELF A GATE. It is built from repository sources and proved equal to the appliance
  # before anything runs against it; an environment nobody can rebuild is how six Phase-6 cases came to fail on
  # a missing fixture and be read as product defects.
  run phase7_environment "phase7 rebuildable gate environment" \
      phase7_build_environment.sh PHASE7_ORACLE_DIGEST="${PHASE7_ORACLE_DIGEST:-}" \
      PHASE7_ENV_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE7_ENV_DB="${P6_DB:-iam_scratch_full}"

  # PRISTINE FIRST: these two are the only gates whose subject is the DATA, so they run before anything seeds.
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase6_backup_restore "phase6 backup and restore" \
      phase6_backup_restore.sh PHASE6_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE6_DB="${P6_DB:-iam_scratch_full}"
  rebuild_env || exit 1
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase6_rollback_rehearsal "phase6 rollback rehearsal (0030-0047)" \
      phase6_rollback_rehearsal.sh PHASE6_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE6_DB="${P6_DB:-iam_scratch_full}"
  rebuild_env || exit 1

  # THE PRIVILEGE MODEL, AGAINST THE COMPLETE SCHEMA. The Phase-4-era run above is kept -- the model must hold
  # in the era that introduced it -- but two of the three failures it reported were objects that era does not
  # have yet (begin_payment_execution, and the payment runtime's grant on a schema built later). Least
  # privilege is a property of the finished system, so it is proved there as well, as its own roster entry.
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase4_least_privilege_full "phase4 least privilege (complete schema)"       phase4_least_privilege.sh PHASE4_LP_CONTAINER="${P6_CONTAINER:-phase7-env}"       PHASE4_LP_DB="${P6_DB:-iam_scratch_full}"

  # ...and now the gates that seed.
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase6_foundation "phase6 foundation (0030)" \
      phase6_0030_foundation.sh PHASE6_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE6_DB="${P6_DB:-iam_scratch_full}"
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase6_device_self_service "phase6 device self-service (0031)" \
      phase6_0031_device_self_service.sh PHASE6_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE6_DB="${P6_DB:-iam_scratch_full}"
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase6_aggregate_online_time "phase6 aggregate online time (0036)" \
      phase6_0036_aggregate_online_time.sh PHASE6_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE6_DB="${P6_DB:-iam_scratch_full}"
  NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase6_least_privilege "phase6 least privilege" \
      phase6_least_privilege.sh PHASE6_CONTAINER="${P6_CONTAINER:-phase7-env}" PHASE6_DB="${P6_DB:-iam_scratch_full}"
  echo
fi

echo "-- Phase 7: the composition gates --"
NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase7_m1 "phase7 M1 identity and acquisition" \
    phase7_m1_identity_and_acquisition.sh PHASE7_CONTAINER="${P6_CONTAINER:-phase7-env}"  PHASE7_DB="${P6_DB:-iam_scratch_full}"
NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase7_m2 "phase7 M2 the stay end to end" \
    phase7_m2_the_stay_end_to_end.sh PHASE7_CONTAINER="${P6_CONTAINER:-phase7-env}"  PHASE7_DB="${P6_DB:-iam_scratch_full}"
NEED_CONTAINER="${P6_CONTAINER:-phase7-env}" run phase7_m3 "phase7 M3 the boundaries hold" \
    phase7_m3_boundaries.sh PHASE7_CONTAINER="${P6_CONTAINER:-phase7-env}"  PHASE7_DB="${P6_DB:-iam_scratch_full}"

# ---- Phase 7: the proofs the composition gates rest on ------------------------------------------------------
#
# These three are in the roster because the composition gates are only as good as the database they run
# against. The reconstruction proves that database can be rebuilt from repository sources; the fidelity suite
# proves the digest making that claim can actually fail; the ledger proof shows the backfilled migration rows
# are backed by every material effect their migrations describe. Leaving them out would let the matrix pass
# while the ground under it went unchecked.
#
# The reconstruction and the fidelity suite build and destroy their OWN isolated clusters, so they need no
# pre-existing container -- and must not be given one.
run phase7_reconstruct "phase7 rebuild from repository sources" \
    phase7_reconstruct_from_sources.sh PHASE7_ORACLE_DIGEST="${PHASE7_ORACLE_DIGEST:-}" \
    PHASE7_CONTAINER=phase7-matrix-recon
run phase7_fidelity_selftest "phase7 fidelity proof mutation suite" \
    phase7_fidelity_selftest.sh PHASE7_SELFTEST_CONTAINER=phase7-matrix-fidsel
run phase7_ledger "phase7 ledger material effect" \
    phase7_ledger_material_effect.sh PHASE7_TARGET="${PHASE7_LEDGER_TARGET:-appliance}"

# ---- the roster check -------------------------------------------------------------------------------------
expected="$EXPECTED_ALL"; [ "$ONLY_PHASE7" = "1" ] && expected="$EXPECTED_PHASE7"
absent=""
for want in $expected; do
  case " $EXECUTED " in *" $want "*) : ;; *) absent="$absent $want" ;; esac
done

line
if [ -n "$absent" ]; then
  echo "EXPECTED GATES THAT NEVER EXECUTED:$absent"
  echo "  A gate that did not run is not a gate that passed. Losing a gate file, or losing its container,"
  echo "  must never be indistinguishable from a clean run."
fi
if [ "$skipped" -gt 0 ]; then
  echo "NOTE: $skipped gate(s) were SKIPPED. A skipped gate is NOT a passing gate."
fi

printf 'PHASE7_FULL_MATRIX mode=%s gates_run=%d skipped=%d unverdicted_or_crashed=%d pass=%d fail=%d\n' \
  "$MODE" "$ran" "$skipped" "$broken" "$total_pass" "$total_fail"

if [ "$MODE" = "strict" ]; then
  if [ "$total_fail" -ne 0 ] || [ "$skipped" -ne 0 ] || [ "$broken" -ne 0 ] || [ -n "$absent" ]; then
    echo "PHASE7_FULL_MATRIX = FAIL (strict: skips, missing gates, crashes and unparsable verdicts all count)"
    exit 1
  fi
  echo "PHASE7_FULL_MATRIX = PASS (strict)"
  exit 0
fi
echo "PHASE7_FULL_MATRIX = lenient run -- NOT acceptance evidence"
[ "$total_fail" -eq 0 ]
