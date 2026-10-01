#!/usr/bin/env bash
# DOES THE MERGE-RECEIPT TIMING RULE ACTUALLY CATCH T0054's DEFECT?
#
# The rule is grandfathered for T0054 on the real ledger, so a normal run reports it as a listed exception and
# passes. That is correct behaviour and it is also indistinguishable, from the outside, from a rule that does
# nothing. So this drives the REAL validator (tools/validate-transition-times.sh -- the same file CI runs,
# never a copy) with the grandfather list EMPTIED, against the EXACT historical receipt, and fails if it lets
# the defect through.
#
# It uses real data rather than invented fixtures wherever it can: T0054 is the receipt that pre-dates its
# merge by 311 seconds, and T0048 is the Phase-4 merge receipt that post-dates its merge by 17 seconds. One
# must fail and the other must pass, which is exactly the discrimination the rule is claimed to have.
#
# It touches no database, no appliance and no network, and it never writes to governance/.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT

PY3=""
for cand in python3 python; do
  if [ "$("$cand" -c 'print(42)' 2>/dev/null)" = "42" ]; then PY3="$cand"; break; fi
done
[ -n "$PY3" ] || { echo "INFRA: no usable python"; exit 2; }

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# Run the real validator over a fixture directory with NO merge receipt grandfathered.
run_ungrandfathered(){ # run_ungrandfathered <dir> -> exit code, output in $OUT
  OUT="$(TRANSITION_TIMES_DIR="$1" TRANSITION_TIMES_MERGE_GRANDFATHERED=" " \
         bash "$ROOT/tools/validate-transition-times.sh" 2>&1)"
  return $?
}

# 1. THE HISTORICAL DEFECT ITSELF. T0054 records PHASE_5_PULL_REQUEST_MERGED_TO_MASTER at 22:20:00Z while the
#    merge commit it names was created at 22:25:11Z. Ungrandfathered, this must FAIL.
mkdir -p "$W/defect"
cp governance/transitions/T0054.json "$W/defect/T0054.json"
if run_ungrandfathered "$W/defect"; then
  no "the real T0054 defect is caught when not grandfathered" "the validator PASSED a receipt that pre-dates its own merge"
else
  case "$OUT" in
    *"BEFORE the merge it describes"*)
      case "$OUT" in
        *"311s"*) ok "the real T0054 defect is caught, and the refusal states the exact 311s discrepancy" ;;
        *)        ok "the real T0054 defect is caught (discrepancy not stated as 311s: $OUT)" ;;
      esac ;;
    *) no "the real T0054 defect is caught" "wrong wording: $OUT" ;;
  esac
fi

# 2. A VALID MERGE RECEIPT STILL PASSES. T0048 records the Phase-4 merge 17 seconds AFTER it happened, which
#    is what a correctly written receipt looks like. Without this case, case 1 would also "pass" for a
#    validator that refuses every merge receipt ever written.
mkdir -p "$W/valid"
cp governance/transitions/T0048.json "$W/valid/T0048.json"
if run_ungrandfathered "$W/valid"; then
  ok "a valid merge receipt (T0048, recorded 17s after its merge) still passes"
else
  no "a valid merge receipt still passes" "the validator refused a correct receipt: $OUT"
fi

# 3. EQUALITY IS NOT EARLY. A receipt stamped at exactly the merge time is not describing the future, and an
#    off-by-one that refused it would push authors to pad their timestamps.
mkdir -p "$W/equal"
"$PY3" - "$W/equal/T9001.json" <<'PY'
import io, json, subprocess, sys
mc = "4f27b4d0ea4de57f9bbf6a062d9bb9d294ec6e6a"
mt = subprocess.check_output(["git", "log", "-1", "--format=%cI", mc], text=True).strip()
import datetime
ts = datetime.datetime.fromisoformat(mt).astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
io.open(sys.argv[1], "w", encoding="utf-8", newline="\n").write(json.dumps({
    "transition_id": "T9001", "seq": 9001, "timestamp": ts,
    "record_type": "SELFTEST_PULL_REQUEST_MERGED_TO_MASTER",
    "merge": {"merge_commit": mc},
}, indent=2) + "\n")
PY
if run_ungrandfathered "$W/equal"; then
  ok "a receipt stamped at exactly the merge time is accepted (equality is not 'before')"
else
  no "equality is not treated as early" "$OUT"
fi

# 4. A MERGE RECEIPT THAT NAMES NO MERGE COMMIT IS UNFALSIFIABLE, and must be refused rather than skipped.
#    This is the failure mode that would otherwise let a bad receipt through by omission.
mkdir -p "$W/nameless"
cat > "$W/nameless/T9002.json" <<'JSON'
{
  "transition_id": "T9002",
  "seq": 9002,
  "timestamp": "2020-01-01T00:00:00Z",
  "record_type": "SELFTEST_PULL_REQUEST_MERGED_TO_MASTER"
}
JSON
if run_ungrandfathered "$W/nameless"; then
  no "a merge receipt naming no merge commit is refused" "an unfalsifiable receipt was accepted"
else
  case "$OUT" in
    *"names no merge commit"*) ok "a merge receipt naming no merge commit is refused as unfalsifiable" ;;
    *) no "a merge receipt naming no merge commit is refused" "wrong wording: $OUT" ;;
  esac
fi

# 5. THE GRANDFATHER LIST EXCUSES, IT DOES NOT HIDE. On the real ledger T0054 must be reported as a listed
#    exception with its discrepancy -- not silently skipped, and not reported as clean.
OUT="$(bash "$ROOT/tools/validate-transition-times.sh" 2>&1)"; rc=$?
if [ "$rc" -ne 0 ]; then
  no "the real ledger passes with T0054 grandfathered" "$OUT"
else
  case "$OUT" in
    *"grandfathered: T0054 records the merge 311s BEFORE it happened"*)
      ok "on the real ledger T0054 is reported as a grandfathered exception with its measured discrepancy" ;;
    *) no "T0054 is visibly grandfathered on the real ledger" "the exception is not printed: $OUT" ;;
  esac
fi

# 6-8. FORWARD CORRECTION OF A LATE RECEIPT IS EXACT, OR IT IS NOTHING. T0199 is a real receipt written under
#    the rule and introduced 654s before the time it records. With no correction it must fail; with a later
#    receipt that names it and states exactly what the repository measures it passes as "corrected forward";
#    with a correction whose figures do not match it must still fail.
T199ADD="$(git log --diff-filter=A --format='%H' -- governance/transitions/T0199.json | tail -1)"
T199TS="$("$PY3" -c "import json,io;print(json.load(io.open('governance/transitions/T0199.json',encoding='utf-8'))['timestamp'])")"
corr_receipt(){ # corr_receipt <file> <late_by>
  "$PY3" - "$1" "$T199TS" "$T199ADD" "$2" <<'PY'
import io, json, sys
io.open(sys.argv[1], "w", encoding="utf-8", newline=chr(10)).write(json.dumps({
    "transition_id": "T9100", "seq": 9100, "timestamp": "2099-01-01T00:00:00Z",
    "record_type": "SELFTEST_TIMESTAMP_CORRECTION",
    "timestamp_corrections": [{"transition_id": "T0199", "recorded_timestamp": sys.argv[2],
                               "introducing_commit": sys.argv[3], "late_by_seconds": int(sys.argv[4])}],
}, indent=2) + chr(10))
PY
}
mkdir -p "$W/late"
cp governance/transitions/T0199.json "$W/late/T0199.json"
if run_ungrandfathered "$W/late"; then
  no "a late receipt with no correction is refused" "T0199 passed with nothing correcting it"
else
  case "$OUT" in
    *"T0199 records"*"654s AFTER"*) ok "a late receipt with no correction is refused, stating the exact 654s" ;;
    *) no "a late receipt with no correction is refused" "wrong wording: $OUT" ;;
  esac
fi
mkdir -p "$W/corrected"
cp governance/transitions/T0199.json "$W/corrected/T0199.json"
corr_receipt "$W/corrected/T9100.json" 654
if run_ungrandfathered "$W/corrected"; then
  case "$OUT" in
    *"corrected forward: T0199 is 654s later"*"corrected forward by T9100"*) ok "an exact forward correction is accepted and printed with its lateness and its corrector" ;;
    *) no "an exact forward correction is visible" "accepted silently: $OUT" ;;
  esac
else
  no "an exact forward correction is accepted" "$OUT"
fi
mkdir -p "$W/mismatch"
cp governance/transitions/T0199.json "$W/mismatch/T0199.json"
corr_receipt "$W/mismatch/T9100.json" 600
if run_ungrandfathered "$W/mismatch"; then
  no "a correction whose figures do not match is refused" "a 600s correction excused a 654s receipt"
else
  case "$OUT" in
    *"does not match"*) ok "a correction whose figures do not match the measurement is refused" ;;
    *) no "a mismatched correction is refused" "wrong wording: $OUT" ;;
  esac
fi

echo "------------------------------------------------------------"
echo "MERGE_RECEIPT_TIMES_SELFTEST pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
echo "the merge-timing rule catches the exact historical defect and accepts a correctly written receipt"
exit 0
