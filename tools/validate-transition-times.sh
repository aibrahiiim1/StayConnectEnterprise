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
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

# Pre-rule receipts, left as written. Documented, not hidden.
GRANDFATHERED="T0007 T0008 T0016 T0017"

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
for f in governance/transitions/T*.json; do
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

echo "  ($grandfathered_seen pre-rule receipt(s) grandfathered and listed above; the rule is enforced from T0018 onward)"
if [ "$fail" -eq 0 ]; then
  echo "TRANSITION_TIMESTAMPS = PASS"; exit 0
fi
echo "TRANSITION_TIMESTAMPS = FAIL ($fail)"; exit 1
