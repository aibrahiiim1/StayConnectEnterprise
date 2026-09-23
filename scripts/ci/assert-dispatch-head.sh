#!/usr/bin/env bash
# THE RUN MUST BE ABOUT THE COMMIT THE ORCHESTRATOR INTENDED, AND ABOUT NOTHING ELSE.
#
# A gate reports its required status context against GITHUB_SHA -- the head of the run. When the nightly
# orchestrator dispatches a gate it can only name a REF, not a commit, so a commit pushed in the seconds
# between "read the candidate head" and "the runner checks it out" produces the one outcome this model must
# never allow: a genuine, honestly-earned green verdict attached to a commit nobody decided to merge.
#
# So the orchestrator passes the sha it decided on, and this refuses the run outright unless the head it is
# actually validating is that sha. The mismatch is loud and the gate fails; the orchestrator then sees a gate
# that did not pass, refuses to merge, and the next night judges whatever the head is by then.
#
# It also refuses a dispatch that carries no expected sha at all, because "validate whatever is there" is not
# a thing this gate may be asked to do.
#
# Usage: assert-dispatch-head.sh <expected_sha> <actual_sha> [correlation_id]
set -uo pipefail

EXPECTED="${1:-}"
ACTUAL="${2:-}"
CORRELATION="${3:-}"

echo "== the head under test =="
echo "  expected (orchestrator): ${EXPECTED:-<none>}"
echo "  actual   (GITHUB_SHA):   ${ACTUAL:-<none>}"
[ -n "$CORRELATION" ] && echo "  correlation:             $CORRELATION"

case "$EXPECTED" in
  "")
    echo "REFUSED: this gate was dispatched without an expected head sha. A run that does not know which"
    echo "         commit it is supposed to be validating cannot be authoritative evidence for any commit."
    exit 1
    ;;
  *[!0-9a-f]*)
    echo "REFUSED: the expected head sha '$EXPECTED' is not a hexadecimal object name."
    exit 1
    ;;
esac

if [ "${#EXPECTED}" -ne 40 ]; then
  echo "REFUSED: the expected head sha must be the full 40 characters; '$EXPECTED' is ${#EXPECTED}."
  echo "         An abbreviated sha is not an identity -- it is a prefix that more than one object can share."
  exit 1
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "REFUSED: THIS RUN IS VALIDATING THE WRONG COMMIT."
  echo "         The orchestrator decided on $EXPECTED and this runner checked out $ACTUAL,"
  echo "         which means the branch moved between the decision and the checkout."
  echo "         Failing closed: a true verdict about the wrong commit is worse than no verdict, because it"
  echo "         would be merged. The next nightly run judges whatever the head is by then."
  exit 1
fi

echo "  -> the head under test is exactly the commit the orchestrator decided on."
