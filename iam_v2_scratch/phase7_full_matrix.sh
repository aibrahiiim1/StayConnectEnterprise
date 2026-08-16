#!/usr/bin/env bash
# PHASE-7 — THE COMPLETE MATRIX, AS ONE RUNNABLE THING.
#
# The FINAL contract §18 gives Phase 7 the gate "complete matrix". Until now that matrix existed only as a
# list of scripts a person had to know how to invoke: each gate hard-codes or requires a different container
# and database, one of them refuses to start without an environment variable set (PHASE4_LP_CONTAINER is
# `:?`), and the phase-3 gate names its container inline. Running "the matrix" therefore meant remembering
# six conventions, and a gate that is hard to run is a gate that stops being run.
#
# So the deliverable is this: one command, every gate, the right environment for each, one verdict. A gate
# whose environment is missing is reported as SKIPPED WITH THE REASON -- never as a pass, because a matrix
# that silently omits what it could not run is worse than one that fails.
#
#   usage:  phase7_full_matrix.sh              run every gate that can run here
#           phase7_full_matrix.sh --phase7     the Phase-7 composition gates only
#
# It contacts no appliance, no Production database, no PMS and no provider.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"

# ONE RUN AT A TIME, ENFORCED. The matrix includes phase6_backup_restore, which DROPS the scratch database
# and restores it. Run it while anything else is touching that database and both runs produce numbers that
# describe neither -- which is exactly what happened here: two matrix runs overlapped, every gate reported
# failures, and none of those failures were real. The lock is a file rather than advice, because advice in a
# comment does not stop the second terminal.
LOCK="${TMPDIR:-/tmp}/phase7_full_matrix.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  echo "REFUSED: another full-matrix run holds $LOCK." >&2
  echo "  The matrix drops and restores the scratch database; two concurrent runs corrupt each other and" >&2
  echo "  produce failures that are artifacts. Wait for it, or remove the lock if you are sure it is stale." >&2
  exit 2
fi
trap 'rmdir "$LOCK" 2>/dev/null' EXIT INT TERM

total_pass=0; total_fail=0; ran=0; skipped=0
line(){ printf '%s\n' "------------------------------------------------------------"; }

have(){ docker inspect "$1" >/dev/null 2>&1; }

# run <label> <script> <env assignments...>
run(){
  local label="$1" script="$2"; shift 2
  if [ ! -f "$HERE/$script" ]; then
    printf '  %-46s SKIPPED (no such gate: %s)\n' "$label" "$script"; skipped=$((skipped+1)); return
  fi
  # Every gate needs a live container. Naming the missing one is the difference between "this environment
  # cannot run that gate" and "that gate is fine".
  local need="${NEED_CONTAINER:-}"
  if [ -n "$need" ] && ! have "$need"; then
    printf '  %-46s SKIPPED (container %s is not running)\n' "$label" "$need"; skipped=$((skipped+1)); return
  fi
  local out last p f
  out="$(env "$@" bash "$HERE/$script" 2>&1)"
  last="$(printf '%s' "$out" | tail -1)"
  # CASE-INSENSITIVE, because the gates do not agree on capitalisation: Phase 6 prints `pass=50 fail=0` and
  # Phase 5 prints `PASS=70 FAIL=2`. The first version of this runner matched only lowercase, fell through to
  # a word-match on "PASS", and reported the Phase-5 gate as PASSING while it was carrying two real failures.
  # A runner that cannot read a verdict must say so rather than guess -- this whole phase exists because
  # assertions that look green and mean nothing are the expensive kind of wrong, and a matrix runner that
  # commits the same error hides every gate at once instead of one.
  p="$(printf '%s' "$last" | grep -oiE 'pass=[0-9]+' | head -1 | cut -d= -f2)"
  f="$(printf '%s' "$last" | grep -oiE 'fail=[0-9]+' | head -1 | cut -d= -f2)"
  if [ -z "${p:-}" ]; then
    printf '  %-46s NO VERDICT :: %s\n' "$label" "$(printf '%s' "$last" | cut -c1-70)"
    total_fail=$((total_fail+1)); ran=$((ran+1)); return
  fi
  total_pass=$((total_pass+p)); total_fail=$((total_fail+${f:-0})); ran=$((ran+1))
  if [ "${f:-0}" = "0" ]; then printf '  %-46s pass=%-4s fail=%s\n' "$label" "$p" "${f:-0}"
  else                          printf '  %-46s pass=%-4s fail=%s   <-- FAILED\n' "$label" "$p" "${f:-0}"; fi
}

echo "== Phase 7: the complete acceptance matrix =="
echo

if [ "${1:-}" != "--phase7" ]; then
  echo "-- Phase 3: stay domain, resolution, checkout grace (contract D, F) --"
  NEED_CONTAINER=iamv2-scratch run "phase3 lifecycle (0010)" phase3_0010_lifecycle.sh IGNORE=1

  echo "-- Phase 4: financial core, DARK (contract E) --"
  NEED_CONTAINER=iamv2-p4gate run "phase4 financial (0011)" phase4_0011_financial.sh IGNORE=1
  NEED_CONTAINER=iamv2-p4gate run "phase4 db invariants" phase4_db_invariants.sh \
      SCRATCH_CONTAINER=iamv2-p4gate SCRATCH_DB=iam_scratch
  NEED_CONTAINER=iamv2-p4gate run "phase4 least privilege" phase4_least_privilege.sh \
      PHASE4_LP_CONTAINER=iamv2-p4gate PHASE4_LP_DB=iam_scratch

  echo "-- Phase 5: post-stay and cross-PMS transfer (contract F8, F9) --"
  NEED_CONTAINER=iamv2-p5 run "phase5 foundation (0027)" phase5_0027_foundation.sh IGNORE=1
  NEED_CONTAINER=iamv2-p5 run "phase5 least privilege" phase5_least_privilege.sh IGNORE=1

  echo "-- Phase 6: guest device self-service + aggregate online time --"
  NEED_CONTAINER=iamv2-p6 run "phase6 foundation (0030)" phase6_0030_foundation.sh IGNORE=1
  NEED_CONTAINER=iamv2-p6 run "phase6 device self-service (0031)" phase6_0031_device_self_service.sh IGNORE=1
  NEED_CONTAINER=iamv2-p6 run "phase6 aggregate online time (0036)" phase6_0036_aggregate_online_time.sh IGNORE=1
  NEED_CONTAINER=iamv2-p6 run "phase6 least privilege" phase6_least_privilege.sh IGNORE=1
  NEED_CONTAINER=iamv2-p6 run "phase6 backup and restore" phase6_backup_restore.sh IGNORE=1
  NEED_CONTAINER=iamv2-p6 run "phase6 rollback rehearsal (0030-0047)" phase6_rollback_rehearsal.sh IGNORE=1
  echo
fi

echo "-- Phase 7: the composition gates --"
NEED_CONTAINER=iamv2-p6 run "phase7 M1 identity and acquisition" phase7_m1_identity_and_acquisition.sh IGNORE=1
NEED_CONTAINER=iamv2-p6 run "phase7 M2 the stay end to end" phase7_m2_the_stay_end_to_end.sh IGNORE=1
NEED_CONTAINER=iamv2-p6 run "phase7 M3 the boundaries hold" phase7_m3_boundaries.sh IGNORE=1

line
printf 'PHASE7_FULL_MATRIX gates_run=%d skipped=%d pass=%d fail=%d\n' \
  "$ran" "$skipped" "$total_pass" "$total_fail"
if [ "$skipped" -gt 0 ]; then
  echo "NOTE: $skipped gate(s) were SKIPPED because their scratch container is not running here. A skipped"
  echo "      gate is not a passing gate; the acceptance evidence must name which, and why."
fi
[ "$total_fail" -eq 0 ]
