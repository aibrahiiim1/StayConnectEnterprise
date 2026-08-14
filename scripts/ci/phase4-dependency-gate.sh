#!/usr/bin/env bash
# PHASE-4 PRODUCTION DEPENDENCY ADVISORY GATE.
#
# WHAT CHANGED, AND WHY IT MATTERED.
#
# The previous version of this gate carried its own accepted-risk list (`ACCEPTED="next postcss"`) written
# by the same agent that was delivering the code. That is self-acceptance: the party shipping the risk
# recorded that the risk was fine, and the gate then reported PASS on a production tree with known high
# severity advisories in it. A gate that can grant itself an exception is not measuring anything.
#
# The distinction this version enforces:
#
#   TRIAGED   an advisory that has been investigated and written up in docs/PHASE4_DEPENDENCY_TRIAGE.md.
#             Understanding a risk is not the same as accepting it. The gate FAILS on a triaged-only
#             production advisory, because somebody still has to decide.
#
#   ACCEPTED  a decision the PRODUCT OWNER made, recorded in governance/dependency-acceptances.json with the
#             exact advisory ids it covers, who decided, which governance transition carries the decision,
#             and when it expires. Only this makes the gate pass with a vulnerability in the tree.
#
# Acceptances name ADVISORIES, not packages. A package-level exception would silently absorb the next,
# unrelated advisory published against the same package.
#
# It gates the PRODUCTION tree -- what actually ships to the appliance. The full tree, which includes build
# and test tooling that never reaches an appliance, is measured and recorded on every run but is reported
# rather than gated; conflating the two is how a gate becomes noise people learn to ignore.
#
# Evidence for BOTH trees is written on every run, so a reviewer sees what was true when the gate passed
# rather than re-running it later against a registry that has moved.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${EVID:-$ROOT/.phase4-evidence}/deps"
mkdir -p "$OUT"
ACCEPT_FILE="$ROOT/governance/dependency-acceptances.json"
DOC="$ROOT/docs/PHASE4_DEPENDENCY_TRIAGE.md"
cd "$ROOT/hotel-admin" || { echo "INFRA: no hotel-admin directory"; exit 2; }

# python3 on a Linux runner. On a Windows developer host the Microsoft Store ships a python3 alias that
# resolves but does not run, so ask it for a version rather than trusting `command -v`.
PY=python3
python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "INFRA: no usable python"; exit 2; }

[ -f "$ACCEPT_FILE" ] || { echo "INFRA: governance/dependency-acceptances.json is missing"; exit 2; }

echo "== production dependency advisories =="

# The audit output is written to the evidence directory and the judgement reads it from there. An earlier
# version passed a path through /tmp, where a Windows host and MSYS disagree about what the path means and
# python silently read a different, empty file. Everything here stays inside $OUT, which both agree about.

npm audit --omit=dev --json 2>/dev/null > "$OUT/npm-audit-production.json"
npm audit --json 2>/dev/null > "$OUT/npm-audit-full.json"
[ -s "$OUT/npm-audit-production.json" ] || { echo "INFRA: npm audit produced no output"; exit 2; }

# One python pass does the whole judgement, because the comparison is between two structured documents and
# expressing it in shell is how the previous version accumulated its quoting bugs.
"$PY" "$ROOT/scripts/ci/dependency-judgement.py" \
  "$OUT/npm-audit-production.json" "$OUT/npm-audit-full.json" "$ACCEPT_FILE" "$DOC"
rc=$?

echo "  evidence:        $OUT/npm-audit-production.json, $OUT/npm-audit-full.json"
exit $rc
