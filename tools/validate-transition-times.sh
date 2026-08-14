#!/usr/bin/env bash
# A TRANSITION CANNOT BE RECORDED AFTER THE COMMIT THAT INTRODUCES IT.
#
# Transition receipts carry a timestamp that readers treat as when the transition happened. Nothing checked it,
# and an approximation typed by hand can easily land in the future: T0019 recorded 15:30:00Z in a file first
# committed at 15:03:39Z — twenty-six minutes before the moment it claimed. A receipt that describes its own
# future is not evidence, and it is exactly the kind of wrong that reads as right.
#
# The rule is the one thing that can be checked from the repository alone: a transition's timestamp must not be
# later than the commit that FIRST INTRODUCED its file. It may be earlier — a decision genuinely precedes the
# commit that records it — but it can never be later.
#
# GRANDFATHERED RECEIPTS. Four receipts written before this rule existed are also future-dated against their
# introducing commits. They are historical evidence and are NOT rewritten: changing an old receipt to satisfy a
# new check would be manufacturing the very thing the check exists to prevent. They are listed explicitly, with
# their measured discrepancy, so the exception is visible rather than silent, and the rule is enforced from
# T0018 onward.
#
# ---------------------------------------------------------------------------------------------------------
# SECOND RULE: A MERGE RECEIPT CANNOT PRE-DATE THE MERGE IT RECORDS.
#
# The first rule bounds a receipt from ABOVE -- it cannot be later than the commit that introduced it. That
# says nothing about a receipt landing too EARLY, and a merge receipt is precisely where too-early matters:
# T0054 records PHASE_5_PULL_REQUEST_MERGED_TO_MASTER at 2026-08-14T22:20:00Z, but GitHub merged PR #13 at
# 2026-08-14T22:25:11Z. The receipt describes an event that had not happened yet -- 311 seconds of a document
# asserting a merge before the merge existed. It passed every check in the project, because the number it was
# compared against was the commit that CARRIED it, not the event it DESCRIBES.
#
# The authoritative merge time is available without any network call: the merge commit named inside the
# receipt carries it, and its committer date matches GitHub's merged_at exactly (verified for both merge
# receipts in this ledger). So the rule is checkable from the repository alone, like the first one.
#
# A receipt whose record_type says a pull request was merged MUST also name its merge commit. Without that it
# is unfalsifiable, and an unfalsifiable receipt is not evidence.
#
# T0054 IS GRANDFATHERED, deliberately and visibly. It is preserved byte-for-byte as historical evidence and
# corrected FORWARD by T0055 under D23; rewriting it to satisfy a rule invented afterwards would manufacture
# exactly the tidiness this check exists to detect. Its measured discrepancy is printed on every run.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

# Pre-rule receipts, left as written. Documented, not hidden.
GRANDFATHERED="T0007 T0008 T0016 T0017"

# Merge receipts written before the merge-timing rule existed, left as written. T0054 pre-dates the merge it
# records by 311 seconds; T0055 corrects it forward under D23.
MERGE_GRANDFATHERED="${TRANSITION_TIMES_MERGE_GRANDFATHERED-T0054}"

# The receipt directory, overridable so the rule itself can be driven against fixtures by
# tools/validate-merge-receipt-times-selftest.sh. A rule nobody has watched fail has not been shown to check
# anything.
TDIR="${TRANSITION_TIMES_DIR:-governance/transitions}"

PY3=""
for cand in python3 python; do
  if [ "$("$cand" -c 'print(42)' 2>/dev/null)" = "42" ]; then PY3="$cand"; break; fi
done
if [ -z "$PY3" ]; then
  echo "  FAIL: no working Python interpreter; transition timestamps cannot be checked"
  echo "TRANSITION_TIMESTAMPS = FAIL (1)"; exit 1
fi
if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "  skipped: not a git repository (extracted-pack mode); introducing commits are not knowable here"
  echo "TRANSITION_TIMESTAMPS = SKIPPED"; exit 0
fi

fail=0
grandfathered_seen=0
for f in "$TDIR"/T*.json; do
  [ -f "$f" ] || continue
  id="$(basename "$f" .json)"
  ts="$($PY3 -c "import json,io,sys;print(json.load(io.open(sys.argv[1],encoding='utf-8')).get('timestamp',''))" "$f" 2>/dev/null)"
  if [ -z "$ts" ]; then
    echo "  FAIL: $id has no timestamp"; fail=$((fail+1)); continue
  fi
  # The FIRST commit that added the file. `tail -1` because git lists newest first.
  add="$(git log --diff-filter=A --format='%cI' -- "$f" 2>/dev/null | tail -1)"
  if [ -z "$add" ]; then
    # Not yet committed: this is the receipt being written right now, and it will be checked on the next run.
    echo "  note: $id is not yet committed; its introducing commit does not exist yet"
    continue
  fi
  verdict="$($PY3 -c "
import datetime,sys
ts,add=sys.argv[1],sys.argv[2]
t=datetime.datetime.fromisoformat(ts.replace('Z','+00:00'))
a=datetime.datetime.fromisoformat(add)
print('LATE:%d' % int((t-a).total_seconds()) if t>a else 'OK')
" "$ts" "$add" 2>/dev/null)"
  case "$verdict" in
    OK) ;;
    LATE:*)
      secs="${verdict#LATE:}"
      case " $GRANDFATHERED " in
        *" $id "*)
          echo "  grandfathered: $id is ${secs}s later than the commit that introduced it (pre-rule receipt, left as written)"
          grandfathered_seen=$((grandfathered_seen+1))
          ;;
        *)
          echo "  FAIL: $id records $ts, which is ${secs}s AFTER the commit that introduced it ($add)"
          fail=$((fail+1))
          ;;
      esac
      ;;
    *)
      echo "  FAIL: $id timestamp $ts could not be compared with $add"; fail=$((fail+1));;
  esac
done

# ---------------------------------------------------------------------------------------------------------
# RULE 2: a receipt that records a merge cannot be dated before that merge.
merge_seen=0
merge_grandfathered_seen=0
for f in "$TDIR"/T*.json; do
  [ -f "$f" ] || continue
  id="$(basename "$f" .json)"
  read -r rtype mcommit ts <<EOF
$($PY3 -c "
import json, io, sys
d = json.load(io.open(sys.argv[1], encoding='utf-8'))
m = d.get('merge') or {}
mc = m.get('merge_commit') or m.get('merge_commit_sha') or '-'
print('%s %s %s' % (d.get('record_type') or '-', mc, d.get('timestamp') or '-'))
" "$f" 2>/dev/null)
EOF
  records_merge=0
  case "$rtype" in *MERGED_TO_MASTER*) records_merge=1;; esac
  [ "$mcommit" != "-" ] && records_merge=1
  [ "$records_merge" = "1" ] || continue
  merge_seen=$((merge_seen+1))

  if [ "$mcommit" = "-" ]; then
    echo "  FAIL: $id records a pull-request merge but names no merge commit, so its timing cannot be checked"
    fail=$((fail+1)); continue
  fi
  mtime="$(git log -1 --format='%cI' "$mcommit" 2>/dev/null)"
  if [ -z "$mtime" ]; then
    echo "  note: $id names merge commit $mcommit, which is not present in this checkout; timing not checked"
    continue
  fi
  verdict="$($PY3 -c "
import datetime, sys
ts, mt = sys.argv[1], sys.argv[2]
t = datetime.datetime.fromisoformat(ts.replace('Z', '+00:00'))
m = datetime.datetime.fromisoformat(mt)
print('EARLY:%d' % int((m - t).total_seconds()) if t < m else 'OK')
" "$ts" "$mtime" 2>/dev/null)"
  case "$verdict" in
    OK) ;;
    EARLY:*)
      secs="${verdict#EARLY:}"
      case " $MERGE_GRANDFATHERED " in
        *" $id "*)
          echo "  grandfathered: $id records the merge ${secs}s BEFORE it happened (pre-rule receipt, left as written; corrected forward by T0055)"
          merge_grandfathered_seen=$((merge_grandfathered_seen+1))
          ;;
        *)
          echo "  FAIL: $id records $ts, which is ${secs}s BEFORE the merge it describes ($mcommit at $mtime)"
          fail=$((fail+1))
          ;;
      esac
      ;;
    *)
      echo "  FAIL: $id merge timestamp $ts could not be compared with $mtime"; fail=$((fail+1));;
  esac
done
echo "  ($merge_seen merge receipt(s) checked against the merge commit each one names; $merge_grandfathered_seen grandfathered)"

echo "  ($grandfathered_seen pre-rule receipt(s) grandfathered and listed above; the rule is enforced from T0018 onward)"
if [ "$fail" -eq 0 ]; then
  echo "TRANSITION_TIMESTAMPS = PASS"; exit 0
fi
echo "TRANSITION_TIMESTAMPS = FAIL ($fail)"; exit 1
