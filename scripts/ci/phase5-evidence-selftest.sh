#!/usr/bin/env bash
# DOES THE PHASE-5 EVIDENCE PUBLISHER ACTUALLY FAIL?
#
# This exists because of a measured defect, not a hypothetical one. The Phase-5 gate ran green for four
# milestones and published NO artifact at all: the staging directory is dot-prefixed, upload-artifact@v4
# skips hidden files, and `if-no-files-found: warn` demoted "the evidence does not exist" to a warning line.
# Four green runs and zero evidence, and nothing in the gate objected.
#
# So the publisher is now fail-closed -- and a fail-closed component that has only ever been watched
# succeeding has not been shown to fail-close at all. This drives the REAL assembler (scripts/ci/
# phase5_evidence.py, the same file CI runs, never a copy) against deliberately incomplete staging
# directories and asserts it refuses each one.
#
# It touches no database, no appliance and no network.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
PY=python3
python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "INFRA: no usable python"; exit 2; }

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# Every step slug the assembler requires, and one representative machine count, so a COMPLETE staging
# directory can be built and then damaged one way at a time.
STEPS="gofmt go-build go-vet go-vet-phase5 phase5-unit phase5-dark-guard phase5-integration phase4-regression"

stage(){ # stage <dir> -- a complete, valid staging directory
  local d="$1"
  rm -rf "$d"; mkdir -p "$d/counts" "$d/logs"
  {
    printf 'head\t%s\n' "0000000000000000000000000000000000000000"
    printf 'branch\tselftest\n'
    printf 'run_id\t0\n'
    printf 'go\tgo version selftest\n'
    printf 'postgres\tselftest\n'
    printf 'start_utc\t1970-01-01T00:00:00Z\n'
  } > "$d/env.tsv"
  : > "$d/steps.tsv"
  for s in $STEPS; do printf '%s\t0\t1\n' "$s" >> "$d/steps.tsv"; done
  # The count fixture is PRODUCED BY THE REAL PRODUCER rather than hand-written here. A hand-written
  # fixture is a second, independent statement of the file's shape, and the two drift silently -- which is
  # how a self-test ends up validating a shape that CI never writes.
  printf '%s\n' \
    '{"Action":"run","Test":"TestSelftest"}' \
    '{"Action":"pass","Test":"TestSelftest"}' \
    '{"Action":"pass"}' \
    | "$PY" "$ROOT/scripts/ci/gojson_summary.py" "$d/counts/phase5-unit" >/dev/null
}

assemble(){ # assemble <dir> -> exit code, output in $OUTTXT
  OUTTXT="$(cd "$ROOT" && EVID="$1" "$PY" "$ROOT/scripts/ci/phase5_evidence.py" 2>&1)"
  return $?
}

# 0. THE CONTROL. A complete staging directory must be ACCEPTED -- otherwise every case below would "pass"
#    for the trivial reason that the assembler refuses everything.
stage "$W/good"
if assemble "$W/good"; then
  if [ -s "$W/good/phase5-post-stay-transfer-evidence.json" ]; then
    ok "a complete staging directory is accepted and writes a non-empty artifact"
  else
    no "a complete staging directory writes a non-empty artifact" "the artifact file is missing or empty"
  fi
else
  no "a complete staging directory is accepted" "the assembler refused a valid input: $OUTTXT"
fi

# 1. The defect that actually happened, in its purest form: nothing was staged at all.
rm -rf "$W/empty"; mkdir -p "$W/empty"
if assemble "$W/empty"; then
  no "an empty staging directory is refused" "the assembler returned success on nothing"
else
  case "$OUTTXT" in
    *FAIL-CLOSED*) ok "an empty staging directory is refused, and says so as a fail-closed refusal" ;;
    *) no "an empty staging directory is refused" "wrong wording: $OUTTXT" ;;
  esac
fi

# 2. EVID unset -- the shape this takes when the workflow forgets the env var rather than the files.
OUTTXT="$(cd "$ROOT" && env -u EVID "$PY" "$ROOT/scripts/ci/phase5_evidence.py" 2>&1)"
if [ $? -eq 0 ]; then
  no "an unset EVID is refused" "the assembler returned success with no staging directory at all"
else
  ok "an unset EVID is refused"
fi

# 3. A step that recorded no outcome. This is the case that matters most: a gate step deleted, renamed or
#    skipped is a MISSING MEASUREMENT, and a green run must not be able to hide one.
for missing in phase5-integration phase5-dark-guard phase4-regression; do
  stage "$W/nostep"
  grep -v "^$missing	" "$W/nostep/steps.tsv" > "$W/nostep/steps.tsv.tmp" && mv "$W/nostep/steps.tsv.tmp" "$W/nostep/steps.tsv"
  if assemble "$W/nostep"; then
    no "a run missing the '$missing' step is refused" "the assembler published it as complete"
  else
    case "$OUTTXT" in
      *"$missing"*) ok "a run missing the '$missing' step is refused, and the refusal names it" ;;
      *) no "a run missing the '$missing' step is refused" "the refusal does not name the step: $OUTTXT" ;;
    esac
  fi
done

# 4. A missing machine count. The step ran, but the number that would make its result checkable is absent.
stage "$W/nocount"; rm -f "$W/nocount/counts/phase5-unit.json"
if assemble "$W/nocount"; then
  no "a missing machine count is refused" "the assembler published a run with no test total"
else
  ok "a missing machine count is refused"
fi

# 5. A machine count that exists but carries no totals -- a truncated or half-written file reads as
#    present to `test -f` and proves nothing.
stage "$W/badcount"; printf '{"note":"no totals here"}\n' > "$W/badcount/counts/phase5-unit.json"
if assemble "$W/badcount"; then
  no "a machine count with no pass/fail/skip total is refused" "presence was treated as evidence"
else
  ok "a machine count with no pass/fail/skip total is refused"
fi

# 6. The staging directory has steps but no env record: nothing states which head or toolchain produced it.
#    An artifact that cannot be tied to a commit is not evidence.
stage "$W/noenv"; rm -f "$W/noenv/env.tsv"
if assemble "$W/noenv"; then
  no "a run with no env record is refused" "an artifact with no provenance was published"
else
  ok "a run with no env record is refused"
fi

echo "------------------------------------------------------------"
echo "PHASE5_EVIDENCE_SELFTEST pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
echo "the Phase-5 evidence publisher fails closed on every incomplete input, and accepts a complete one"
exit 0
