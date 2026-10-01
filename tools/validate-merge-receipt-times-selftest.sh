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

# 6-11. FORWARD CORRECTION OF A LATE RECEIPT IS EXACT, OR IT IS NOTHING. T0199 is a real receipt written under
#    the rule and introduced 654s before the time it records. With no correction it must fail; with ONE LATER
#    receipt that names it and states exactly what the repository measures it passes as "corrected forward".
#    It must still fail when the lateness is wrong, when the introducing-commit time is wrong, when the
#    "correction" sits in an EARLIER receipt, and when two receipts both claim to correct it.
T199FIRST="$(git log --diff-filter=A --format='%H %cI' -- governance/transitions/T0199.json | tail -1)"
T199ADD="${T199FIRST%% *}"; T199AT="${T199FIRST#* }"
T199TS="$("$PY3" -c "import json,io;print(json.load(io.open('governance/transitions/T0199.json',encoding='utf-8'))['timestamp'])")"
corr_receipt(){ # corr_receipt <dir> <corrector_id> <late_by> <introducing_commit_time>
  "$PY3" - "$1/$2.json" "$2" "$T199TS" "$T199ADD" "$3" "$4" <<'PY'
import io, json, sys
f, cid, ts, commit, late, at = sys.argv[1:7]
io.open(f, "w", encoding="utf-8", newline=chr(10)).write(json.dumps({
    "transition_id": cid, "seq": int(cid[1:]), "timestamp": "2099-01-01T00:00:00Z",
    "record_type": "SELFTEST_TIMESTAMP_CORRECTION",
    "timestamp_corrections": [{"transition_id": "T0199", "recorded_timestamp": ts, "introducing_commit": commit,
                               "introducing_commit_time": at, "late_by_seconds": int(late)}],
}, indent=2) + chr(10))
PY
}
fixture(){ mkdir -p "$W/$1"; cp governance/transitions/T0199.json "$W/$1/T0199.json"; }
expect_refused(){ # expect_refused <dir> <label> <wording>
  if run_ungrandfathered "$W/$1"; then no "$2" "accepted: $OUT"
  else case "$OUT" in *"$3"*) ok "$2" ;; *) no "$2" "wrong wording: $OUT" ;; esac; fi
}

fixture late
expect_refused late "a late receipt with no correction is refused, stating the exact 654s" "654s AFTER"

fixture corrected; corr_receipt "$W/corrected" T9100 654 "$T199AT"
if run_ungrandfathered "$W/corrected"; then
  case "$OUT" in
    *"corrected forward: T0199 is 654s later"*"corrected forward by T9100"*) ok "an exact forward correction is accepted and printed with its lateness and its corrector" ;;
    *) no "an exact forward correction is visible" "accepted silently: $OUT" ;;
  esac
else
  no "an exact forward correction is accepted" "$OUT"
fi

fixture wronglate; corr_receipt "$W/wronglate" T9100 600 "$T199AT"
expect_refused wronglate "a correction stating the wrong lateness is refused" "does not match"

fixture wrongtime; corr_receipt "$W/wrongtime" T9100 654 "2026-01-01T00:00:00+03:00"
expect_refused wrongtime "a correction stating the wrong introducing-commit time is refused" "does not match"

fixture earlier; corr_receipt "$W/earlier" T0100 654 "$T199AT"
expect_refused earlier "a 'correction' inside an EARLIER receipt is refused" "not a later receipt"

fixture twice; corr_receipt "$W/twice" T9100 654 "$T199AT"; corr_receipt "$W/twice" T9101 654 "$T199AT"
expect_refused twice "two receipts correcting the same receipt are refused, not first-wins" "more than one receipt"

echo "------------------------------------------------------------"
echo "MERGE_RECEIPT_TIMES_SELFTEST pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
echo "the merge-timing rule catches the exact historical defect and accepts a correctly written receipt"
exit 0
