#!/usr/bin/env bash
# PHASE-4 PRODUCTION DEPENDENCY ADVISORY GATE.
#
# What this gate is for, and what it deliberately is not.
#
# It does NOT fail the build on any advisory. `npm audit` on a Next application reports dozens of findings
# in build and test tooling that never reach the appliance, and a gate that goes red on all of them is a
# gate people learn to ignore -- which is worse than no gate at all.
#
# It fails when the PRODUCTION dependency tree -- what actually ships -- acquires a vulnerable package that
# is not in the recorded, triaged baseline, or loses one that is. A new production advisory is a decision
# somebody has to make; an accepted-risk entry for a risk that no longer exists is how a real one gets lost
# in the noise.
#
# The baseline is ACCEPTED below and is assessed in docs/PHASE4_DEPENDENCY_TRIAGE.md. Changing it means
# editing this file, which is the point: accepting a production risk should be a deliberate act with a name
# attached to the commit.
#
# Evidence for BOTH trees is written on every run, so a reviewer sees what was true when the gate passed
# rather than re-running it later against a registry that has moved.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${EVID:-$ROOT/.phase4-evidence}/deps"
mkdir -p "$OUT"
cd "$ROOT/hotel-admin" || { echo "INFRA: no hotel-admin directory"; exit 2; }

# python3 on a Linux runner. On a Windows developer host the Microsoft Store ships a python3 alias that
# resolves but does not run, so ask it for a version rather than trusting `command -v`.
PY=python3
python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "INFRA: no usable python"; exit 2; }

# --- the accepted production baseline -------------------------------------------------------------------
# `next` is here because its minimum fixing version is a framework major that was attempted in this
# milestone, failed the Hotel-Admin browser regression suite, and was reverted rather than shipped red.
# `postcss` is here because the production tree's copy is the one bundled inside next.
ACCEPTED="next postcss"

echo "== production dependency advisories =="

# The JSON is PIPED into python rather than handed over as a path. Passing a path from bash to python is
# where an earlier version of this gate went wrong: on a Windows host the two disagree about what /tmp
# means, python silently read a different empty file, and the gate reported every triaged advisory as gone.
# A pipe has no path in it to disagree about.
npm audit --omit=dev --json 2>/dev/null | tee "$OUT/npm-audit-production.json" \
  | "$PY" -c 'import json,sys; d=json.load(sys.stdin); sys.stdout.write(" ".join(sorted(d.get("vulnerabilities",{}))))' \
  > "$OUT/prod-packages.txt"
npm audit --json 2>/dev/null | tee "$OUT/npm-audit-full.json" \
  | "$PY" -c 'import json,sys; d=json.load(sys.stdin); sys.stdout.write(str(len(d.get("vulnerabilities",{}))))' \
  > "$OUT/full-count.txt"

[ -s "$OUT/npm-audit-production.json" ] || { echo "INFRA: npm audit produced no output"; exit 2; }

PROD="$(tr -d '\r' < "$OUT/prod-packages.txt")"
FULL_N="$(tr -d '\r' < "$OUT/full-count.txt")"
PROD_N=0
for _p in $PROD; do PROD_N=$((PROD_N + 1)); done

echo "  full tree:       ${FULL_N:-?} advisory group(s)   (recorded, not gated)"
echo "  production tree: $PROD_N advisory group(s)"
echo "  evidence:        $OUT/npm-audit-production.json, $OUT/npm-audit-full.json"

rc=0

# Anything in production that is not on the accepted list is a decision nobody has made yet.
for pkg in $PROD; do
  case " $ACCEPTED " in
    *" $pkg "*) echo "  [accepted]  $pkg — assessed in docs/PHASE4_DEPENDENCY_TRIAGE.md" ;;
    *)          echo "  [NEW]       $pkg — a production advisory that has never been assessed"; rc=1 ;;
  esac
done

# ...and anything on the accepted list that production no longer reports is a stale accepted risk.
for pkg in $ACCEPTED; do
  case " $PROD " in
    *" $pkg "*) ;;
    *) echo "  [stale]     $pkg is accepted here but is no longer reported — remove it from this gate and"
       echo "              from docs/PHASE4_DEPENDENCY_TRIAGE.md"
       rc=1 ;;
  esac
done

# The triage document must exist and must still assess what this gate claims it assesses.
DOC="$ROOT/docs/PHASE4_DEPENDENCY_TRIAGE.md"
if [ ! -f "$DOC" ]; then
  echo "  [FAIL]      docs/PHASE4_DEPENDENCY_TRIAGE.md is missing"; rc=1
else
  for pkg in $ACCEPTED; do
    grep -q "$pkg" "$DOC" || { echo "  [FAIL]      the triage document does not assess $pkg"; rc=1; }
  done
fi

echo
if [ "$rc" = 0 ]; then
  echo "PHASE4_DEPENDENCY_GATE: PASS (production advisories are exactly the triaged baseline)"
else
  echo "PHASE4_DEPENDENCY_GATE: FAIL — the production dependency risk has changed and needs a decision"
fi
exit $rc
