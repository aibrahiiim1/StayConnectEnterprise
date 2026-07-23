#!/usr/bin/env bash
# Record-and-run ONE Phase-3 gate step, for the evidence artifact.
#
# Usage:  scripts/ci/step.sh <slug> -- <command> [args...]
#
# It runs the command in the CURRENT working directory (so the workflow's per-step
# working-directory still applies), streams the combined output to the job log AND to
# a per-step file, and records one row  <slug> <exit> <seconds>  to $EVID/steps.tsv.
# It propagates the command's real exit code, so a failing step still fails the job.
#
# The per-step log stays in the runner and in the job's own logs; it is deliberately
# NOT placed inside the uploaded artifact (see scripts/ci/phase3_evidence.py — the
# artifact carries derived, PII-free summaries, never raw test output).
set -uo pipefail

slug="${1:?slug required}"; shift
[ "${1:-}" = "--" ] && shift
: "${EVID:?EVID must point at the evidence staging directory}"

mkdir -p "$EVID/logs"
start="$(date -u +%s)"
"$@" 2>&1 | tee "$EVID/logs/$slug.log"
rc="${PIPESTATUS[0]}"
end="$(date -u +%s)"
printf '%s\t%d\t%d\n' "$slug" "$rc" "$((end - start))" >> "$EVID/steps.tsv"
exit "$rc"
