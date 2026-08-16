#!/usr/bin/env bash
# PHASE-7 — THE MATRIX RUNNER'S OWN MUTATION SUITE.
#
# The matrix produces the single line somebody reads before saying "the system passes". Its strict mode claims
# to fail on eight distinct conditions. This proves each one, by CAUSING it: a claim that a runner fails on
# something is worth nothing until the runner has been watched failing on it.
#
# Everything happens in a disposable copy of the gate directory. The real gates are never modified, and no
# database, appliance or container is touched: the mutations are fake gate scripts whose only job is to exit
# in a particular way.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

SB="$(mktemp -d "${TMPDIR:-/tmp}/p7matrix-selftest.XXXXXX")"
cleanup(){ rm -rf "$SB"; rmdir "${TMPDIR:-/tmp}/phase7_full_matrix.lock" 2>/dev/null; }
trap cleanup EXIT INT TERM

cp "$HERE/phase7_full_matrix.sh" "$SB/"

# A stand-in for each Phase-7 gate. The suite drives ONLY --phase7, so the roster is exactly these three and
# the mutations stay small and legible.
mk(){ # mk <file> <last-line> <exit-code>
  printf '#!/usr/bin/env bash\necho "%s"\nexit %s\n' "$2" "$3" > "$SB/$1"
  chmod +x "$SB/$1"
}
reset_gates(){
  mk phase7_m1_identity_and_acquisition.sh "PHASE7_M1 pass=20 fail=0" 0
  mk phase7_m2_the_stay_end_to_end.sh      "PHASE7_M2 pass=22 fail=0" 0
  mk phase7_m3_boundaries.sh               "PHASE7_M3 pass=31 fail=0" 0
}

# run_sb <extra-args...> -> sets OUT and RC.
#
# NOT called through $( ) -- a command substitution runs in a SUBSHELL, so anything the function assigns is
# discarded the moment it returns, and the first version of this suite died on `OUT: unbound variable` one
# case after the control. The capture has to happen in this shell.
OUT=""; RC=0
run_sb(){
  rmdir "${TMPDIR:-/tmp}/phase7_full_matrix.lock" 2>/dev/null
  OUT="$(P6_CONTAINER="${P6_CONTAINER:-iamv2-p6}" bash "$SB/phase7_full_matrix.sh" --phase7 "$@" 2>&1)"
  RC=$?
}

echo "== Phase-7 matrix runner: mutation suite =="

# ---- 0. the control. Without this, every case below could be passing because the runner always fails. ------
reset_gates
run_sb; rc="$RC"
[ "$rc" = "0" ] && ok "CONTROL: three healthy gates, strict mode exits 0" \
  || no "CONTROL: healthy gates should exit 0" "rc=$rc :: $(printf '%s' "$OUT" | tail -1)"
case "$OUT" in *"PASS (strict)"*) ok "CONTROL: and says PASS (strict)" ;; *) no "CONTROL verdict wording" "$(printf '%s' "$OUT" | tail -1)" ;; esac

# ---- 1. a missing gate file --------------------------------------------------------------------------------
reset_gates; rm -f "$SB/phase7_m2_the_stay_end_to_end.sh"
run_sb; rc="$RC"
[ "$rc" != "0" ] && ok "a MISSING GATE FILE fails the run (rc=$rc)" || no "a missing gate file did not fail the run" "rc=0"
case "$OUT" in *"MISSING GATE"*) ok "...and is reported as a missing gate, not as a skip" ;; *) no "missing-gate wording" "" ;; esac
case "$OUT" in *"EXPECTED GATES THAT NEVER EXECUTED"*) ok "...and the roster names it as never executed" ;; *) no "roster did not name the absent gate" "" ;; esac

# ---- 2. a required container that is not running ------------------------------------------------------------
reset_gates
P6_CONTAINER=no-such-container-xyz run_sb; rc="$RC"
[ "$rc" != "0" ] && ok "a MISSING CONTAINER fails the run (rc=$rc)" || no "a missing container did not fail the run" "rc=0"
case "$OUT" in *"SKIPPED (container no-such-container-xyz is not running)"*) ok "...and names the container it could not find" ;; *) no "container-skip wording" "$(printf '%s' "$OUT" | head -6 | tail -1)" ;; esac

# ---- 3. any skipped gate ------------------------------------------------------------------------------------
# Case 2 is one way to skip; this asserts the general rule from the counters rather than from the cause.
case "$OUT" in *"skipped=3"*) ok "ANY SKIP is counted and reported (skipped=3)" ;; *) no "skip counter" "$(printf '%s' "$OUT" | grep -o 'skipped=[0-9]*' | head -1)" ;; esac
case "$OUT" in *"A skipped gate is NOT a passing gate"*) ok "...and the run says so in words" ;; *) no "skip wording" "" ;; esac

# ---- 4. an unparsable verdict --------------------------------------------------------------------------------
reset_gates; mk phase7_m2_the_stay_end_to_end.sh "everything went fine, honestly" 0
run_sb; rc="$RC"
[ "$rc" != "0" ] && ok "an UNPARSABLE VERDICT fails the run (rc=$rc)" || no "an unparsable verdict did not fail the run" "rc=0"
case "$OUT" in *"NO VERDICT"*) ok "...and is reported as NO VERDICT rather than assumed" ;; *) no "no-verdict wording" "" ;; esac

# ---- 5. a child that exits non-zero while PRINTING A CLEAN VERDICT --------------------------------------------
# The important one. The gate's own account of itself says fail=0; the operating system disagrees.
reset_gates; mk phase7_m3_boundaries.sh "PHASE7_M3 pass=31 fail=0" 3
run_sb; rc="$RC"
[ "$rc" != "0" ] && ok "a CHILD EXITING NON-ZERO fails the run even though it printed fail=0 (rc=$rc)" \
  || no "a crashed child that printed fail=0 was accepted" "rc=0"
case "$OUT" in *"CHILD EXITED NON-ZERO"*) ok "...and the reason names the exit status, not the text" ;; *) no "child-exit wording" "" ;; esac

# ---- 6. a parsed failure count above zero ---------------------------------------------------------------------
reset_gates; mk phase7_m1_identity_and_acquisition.sh "PHASE7_M1 pass=19 fail=1" 1
run_sb; rc="$RC"
[ "$rc" != "0" ] && ok "a PARSED FAILURE fails the run (rc=$rc)" || no "a parsed failure did not fail the run" "rc=0"

# ...including the uppercase form, which once slipped through as a pass.
reset_gates; mk phase7_m1_identity_and_acquisition.sh "===== RESULT PASS=70 FAIL=2 =====" 0
run_sb; rc="$RC"
[ "$rc" != "0" ] && ok "an UPPERCASE 'FAIL=2' is read as a failure (rc=$rc)" \
  || no "uppercase FAIL= was not read" "rc=0 -- the defect that reported a failing gate as passing"

# ---- 7. an expected gate that never executed -------------------------------------------------------------------
# Distinct from case 1: the file exists and the roster still must notice when it does not run. Simulated by a
# gate that the container guard removes -- the roster, not the file system, is what catches it.
reset_gates
P6_CONTAINER=no-such-container-xyz run_sb; rc="$RC"
case "$OUT" in *"EXPECTED GATES THAT NEVER EXECUTED: phase7_m1 phase7_m2 phase7_m3"*)
    ok "the ROSTER lists every expected gate that never executed" ;;
  *) no "roster listing" "$(printf '%s' "$OUT" | grep -A1 'NEVER EXECUTED' | head -1)" ;; esac

# ---- 8. overlapping execution -----------------------------------------------------------------------------------
reset_gates
mkdir -p "${TMPDIR:-/tmp}/phase7_full_matrix.lock"
OUT="$(P6_CONTAINER=iamv2-p6 bash "$SB/phase7_full_matrix.sh" --phase7 2>&1)"; rc=$?
rmdir "${TMPDIR:-/tmp}/phase7_full_matrix.lock" 2>/dev/null
[ "$rc" != "0" ] && ok "an OVERLAPPING RUN is refused (rc=$rc)" || no "a second concurrent run was allowed" "rc=0"
case "$OUT" in *"REFUSED: another full-matrix run"*) ok "...and says why, naming the lock" ;; *) no "lock wording" "" ;; esac

# ---- 9. and lenient mode must NOT be quotable as acceptance --------------------------------------------------------
reset_gates; rm -f "$SB/phase7_m2_the_stay_end_to_end.sh"
run_sb --lenient; rc="$RC"
case "$OUT" in *"NOT acceptance evidence"*) ok "lenient mode labels itself NOT acceptance evidence" ;; *) no "lenient labelling" "" ;; esac
case "$OUT" in *"mode=lenient"*) ok "...and the mode appears in the machine-readable summary line" ;; *) no "mode in summary" "" ;; esac

echo "------------------------------------------------------------"
printf 'PHASE7_MATRIX_SELFTEST pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
