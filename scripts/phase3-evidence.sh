#!/usr/bin/env bash
# Phase-3 EVIDENCE COLLECTOR.
#
# Produces one dated evidence bundle for a Phase-3 go/no-go decision, from THIS repository only. It records
# what was actually run, with exit codes and durations, and it is explicit about what it did NOT do: it never
# contacts an appliance, a production database or a PMS, so nothing in the bundle may be read as live
# verification. The live Increment-9 evidence is produced separately by the operator, on the appliance, and
# is recorded as PENDING here until it exists.
#
# Usage: bash scripts/phase3-evidence.sh [output-dir]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT/evidence/phase3}"
mkdir -p "$OUT"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
BRANCH="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
DIRTY="clean"
[ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ] && DIRTY="dirty"
BUNDLE="$OUT/phase3-evidence-$STAMP.md"

run() { # run <label> <command...>
  local label="$1"; shift
  local start end rc log
  log="$(mktemp)"
  start=$(date +%s)
  "$@" >"$log" 2>&1
  rc=$?
  end=$(date +%s)
  {
    echo "### $label"
    echo
    echo "- command: \`$*\`"
    echo "- exit code: **$rc**"
    echo "- duration: $((end - start))s"
    echo
    echo '```text'
    tail -30 "$log"
    echo '```'
    echo
  } >>"$BUNDLE"
  rm -f "$log"
  return $rc
}

{
  echo "# Phase-3 evidence bundle"
  echo
  echo "- generated (UTC): \`$STAMP\`"
  echo "- source HEAD: \`$HEAD_SHA\`"
  echo "- branch: \`$BRANCH\`"
  echo "- working tree: **$DIRTY**"
  echo "- go: \`$(go version 2>/dev/null || echo 'not available')\`"
  echo
  echo "> **Scope of this bundle.** Everything below was executed OFFLINE against this repository and"
  echo "> disposable containers created and destroyed by the scripts themselves. No appliance, production"
  echo "> database or PMS was contacted, and nothing here constitutes live verification. Items marked"
  echo "> PENDING require an operator to run them on the target appliance."
  echo
  echo "## Automated results"
  echo
} >"$BUNDLE"

overall=0
run "Offline preflight (build, flags, migration reversibility, zero runtime privilege)" \
  bash "$ROOT/scripts/phase3-preflight.sh" || overall=1
run "Go unit tests (whole module, race-free run)" \
  bash -c "cd '$ROOT/data-plane' && go test ./... -count=1" || overall=1
run "Migration lifecycle gate (disposable PG16: apply, behaviour, down, re-apply, teardown)" \
  bash "$ROOT/iam_v2_scratch/phase3_0010_lifecycle.sh" || overall=1
run "PG16 integration suites (pmsd, stayengine, authctx, checkout, staygrant, pmsresolve, enforce)" \
  bash "$ROOT/scripts/pmsd-pg-integration.sh" || overall=1

{
  echo "## Live verification — PENDING"
  echo
  echo "The following are deliberately NOT in this bundle. They can only be produced on the target appliance"
  echo "by an authorized operator, and must never be inferred, simulated or written here in advance:"
  echo
  echo "- read-only PMS protocol verification against the live interface;"
  echo "- controlled live-dark deployment of this exact HEAD;"
  echo "- one full reboot with post-reboot convergence evidence;"
  echo "- rollback rehearsal (migration down + previous release restored);"
  echo "- flags-OFF confirmation on the running unit (zero Phase-3 SQL, no PMS socket)."
  echo
  echo "## Result"
  echo
  if [ $overall -eq 0 ]; then
    echo "**OFFLINE EVIDENCE COMPLETE — every automated gate passed on \`$HEAD_SHA\`.**"
    echo "Live Increment-9 evidence remains PENDING (see above)."
  else
    echo "**OFFLINE EVIDENCE INCOMPLETE — at least one automated gate failed. Do not deploy.**"
  fi
} >>"$BUNDLE"

echo "evidence bundle: $BUNDLE"
[ $overall -eq 0 ]
