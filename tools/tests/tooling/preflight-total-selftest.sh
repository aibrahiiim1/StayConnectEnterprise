#!/usr/bin/env bash
# THE DOCUMENTED PREFLIGHT SUITE SIZE IS pass + fail, NEVER pass ALONE.
#
# tools/validate-project-state.sh checks that the Phase-3 Final Report records how many checks the offline
# preflight HAS. It read the JSON's "pass" count and used it as that total, so one red preflight check
# silently redefined the suite: {"pass":107,"fail":1} was treated as a 107-check suite, and the report's
# correct "PASS 108/108" was reported as stale documentation. The suite still has 108 checks either way.
#
# That also crossed a line between two gates. Whether the preflight PASSES belongs to Phase 3 Software CI,
# which fails loudly on it. This check owns one question -- is the documented SIZE current -- so it must read
# a size.
#
# The check shells out to scripts/phase3-preflight.sh, which needs a full toolchain and minutes to run, so
# these drive the real validator against a STUB preflight on a throwaway tree. Every case is deterministic and
# offline.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
pass=0; fail=0
ok()  { echo "  ok: $*"; pass=$((pass+1)); }
no()  { echo "  FAIL: $*"; fail=$((fail+1)); }

# A minimal tree carrying only what the preflight-total block reads: the validator, the report it checks, and
# a stub preflight whose JSON each case controls.
mk_tree() {
  local report_claim="$1" stub_json="$2"
  rm -rf "$WORK/t"; mkdir -p "$WORK/t/tools" "$WORK/t/scripts" "$WORK/t/docs/reports"
  cp "$ROOT/tools/validate-project-state.sh" "$WORK/t/tools/"
  printf '%s\n' '#!/usr/bin/env bash' "printf '%s\\n' '$stub_json'" > "$WORK/t/scripts/phase3-preflight.sh"
  chmod +x "$WORK/t/scripts/phase3-preflight.sh"
  printf '%s\n' \
    '# Phase 3 Final Report' '' \
    "| Offline preflight (everything) | **PASS $report_claim** | \`scripts/phase3-preflight.sh --json\` |" \
    > "$WORK/t/docs/reports/StayConnect-IAM-Phase3-Final-Report.md"
}

# Run ONLY the preflight-total block, by sourcing the validator's own lines in a shell that defines what they
# read. Extracting the block keeps this test on the real implementation rather than a copy of it.
run_block() {
  local out
  sed -n '/^p3drift=0$/,/^fi$/p' "$WORK/t/tools/validate-project-state.sh" > "$WORK/block.sh"
  out="$(REPO_ROOT="$WORK/t" DOCS="$WORK/t/docs" bash -c '
    set -u
    REPO_ROOT="'"$WORK"'/t"; DOCS="$REPO_ROOT/docs"
    . "'"$WORK"'/block.sh"
    echo "P3DRIFT=$p3drift"
  ' 2>&1)"
  printf '%s\n' "$out"
}

echo "== the total is pass + fail, whatever the split =="
# The report claims 108/108. Every one of these describes a 108-CHECK suite, so none of them is stale
# documentation -- including the ones where the preflight is red.
for split in "108 0" "107 1" "106 2" "100 8" "0 108"; do
  set -- $split
  mk_tree "108/108" "{\"pass\":$1,\"fail\":$2}"
  if run_block | grep -q "P3DRIFT=0"; then
    ok "pass=$1 fail=$2 reads as a 108-check suite"
  else
    no "pass=$1 fail=$2 was not read as a 108-check suite: $(run_block | grep HIT || true)"
  fi
done

echo "== a genuinely stale documented size is still caught =="
# The whole point of the check: the suite grew to 108 and the report still says 107.
mk_tree "107/107" '{"pass":108,"fail":0}'
if run_block | grep -q "stale preflight total"; then
  ok "a report claiming 107/107 against a 108-check suite is REFUSED"
else
  no "a stale documented size was accepted"
fi
mk_tree "108/108" '{"pass":100,"fail":9}'
if run_block | grep -q "stale preflight total"; then
  ok "a report claiming 108/108 against a 109-check suite is REFUSED"
else
  no "a grown suite did not make the documented size stale"
fi

echo "== unreadable preflight output remains FAIL-CLOSED =="
for bad in '{}' '{"pass":108}' '{"fail":0}' 'not json at all' '' '{"pass":"x","fail":"y"}'; do
  mk_tree "108/108" "$bad"
  if run_block | grep -q "preflight total unreadable"; then
    ok "refused: ${bad:-(empty output)}"
  else
    no "accepted malformed preflight output: ${bad:-(empty output)}"
  fi
done

echo "== the check does not judge whether the preflight PASSED =="
# Phase 3 Software CI owns that. A red preflight of the documented size is not a documentation defect, and
# this check must not report one -- otherwise one flaky check makes the governance gate fail for the wrong
# reason, which is exactly what happened.
mk_tree "108/108" '{"pass":0,"fail":108}'
if run_block | grep -q "P3DRIFT=0"; then
  ok "an entirely failing preflight of the documented size raises no documentation hit"
else
  no "a red preflight was reported as stale documentation"
fi

echo "PREFLIGHT_TOTAL_SELFTEST pass=$pass fail=$fail"
[ "$fail" = "0" ] || exit 1
