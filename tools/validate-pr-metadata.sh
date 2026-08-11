#!/usr/bin/env bash
# ZERO-STALE FOR PR METADATA.
#
# The PR body is authoritative for a reader — it is the first thing anyone opens — and it is the one
# authoritative surface that is NOT committed repository content. Every check in validate-project-state.sh
# stops at the repository boundary, so a PR whose top-level Status still announced a superseded candidate
# state passed every gate while being the most visible wrong statement in the delivery.
#
# This closes that gap at the CI boundary: it reads the PR body through the API in the SAME run, derives the
# expected state from governance/project-state.json, and fails if they disagree. It hard-codes no run ids and
# no future values — everything it compares comes from the repository or from the live PR.
#
# It is deliberately quiet about what it cannot know: if the branch has no open PR there is no PR metadata to
# be stale, and that is a pass rather than a skip-shaped hole.
set -uo pipefail

REPO="${GITHUB_REPOSITORY:-aibrahiiim1/StayConnectEnterprise}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE="$ROOT/governance/project-state.json"

fail=0
note() { printf '  %s\n' "$*"; }
bad() { printf '  FAIL: %s\n' "$*"; fail=$((fail+1)); }

PY3=""
for cand in python3 python; do
  if [ "$("$cand" -c 'print(42)' 2>/dev/null)" = "42" ]; then PY3="$cand"; break; fi
done
if [ -z "$PY3" ]; then
  bad "no working Python interpreter; the PR-metadata check cannot run"
  printf 'PR_METADATA_ZERO_STALE = FAIL (%d)\n' "$fail"; exit 1
fi

activity="$($PY3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8')).get('current_activity',''))" "$STATE" 2>/dev/null)"
if [ -z "$activity" ]; then
  bad "current_activity could not be read from governance/project-state.json"
  printf 'PR_METADATA_ZERO_STALE = FAIL (%d)\n' "$fail"; exit 1
fi
note "repository current_activity: $activity"

# ---- find the PR body, in this run, without guessing -----------------------------------------------------
body=""
if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "${GITHUB_EVENT_PATH}" ]; then
  body="$($PY3 -c "
import json,sys
try: e=json.load(open(sys.argv[1],encoding='utf-8'))
except Exception: sys.exit(0)
pr=e.get('pull_request') or {}
sys.stdout.write(pr.get('body') or '')
" "$GITHUB_EVENT_PATH" 2>/dev/null)"
fi
if [ -z "$body" ] && [ -n "${GITHUB_TOKEN:-}" ]; then
  ref="${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null)}"
  owner="${REPO%%/*}"
  body="$(curl -sS -H "Authorization: Bearer $GITHUB_TOKEN" \
      "https://api.github.com/repos/$REPO/pulls?state=open&head=$owner:$ref" 2>/dev/null \
    | $PY3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: d=[]
sys.stdout.write((d[0].get('body') or '') if isinstance(d,list) and d else '')
" 2>/dev/null)"
fi

if [ -z "$body" ]; then
  note "no open PR body reachable for this ref; there is no PR metadata that could be stale"
  printf 'PR_METADATA_ZERO_STALE = PASS (no PR)\n'; exit 0
fi

# The body is written to a FILE and every check greps the file.
#
# `printf '%s' "$body" | grep -q ...` looks equivalent and is not: grep -q exits the moment it matches, printf
# then takes SIGPIPE, and under `set -o pipefail` the pipeline reports THAT failure — so a successful match is
# reported as a failed check. It is how this script first ran red in CI while passing locally, where the body
# was small enough for printf to finish before grep exited. A file removes the pipeline, and with it the race.
BODYFILE="$(mktemp)"
trap 'rm -f "$BODYFILE" "$BODYFILE.status"' EXIT
printf '%s' "$body" > "$BODYFILE"

# ---- the current candidate state, derived rather than hard-coded ------------------------------------------
#
# An UNRECOGNISED activity used to fall through to want="" — which silently disabled every check below. That
# is the wrong default for a staleness gate: renaming the activity (exactly what a new correction round does)
# would have turned the check off rather than making it fail, and the PR body could then say anything. An
# activity this script does not know about is now a FAILURE that names itself.
case "$activity" in
  *INCREMENT9_DURABILITY_CORRECTION*) want="INCREMENT-9 DURABILITY CORRECTION CANDIDATE"; superseded="PRE-LIVE SAFETY CANDIDATE";;
  *PRE_LIVE_SAFETY*) want="PRE-LIVE SAFETY CANDIDATE"; superseded="DARK ACCEPTANCE CANDIDATE";;
  *SOFTWARE_CANDIDATE*) want="DARK ACCEPTANCE CANDIDATE"; superseded="";;
  *)
    bad "activity '$activity' has no expected PR candidate-state mapping in this script; add one rather than leaving the PR-body check silently disabled"
    want=""; superseded="";;
esac

if [ -n "$want" ]; then
  if grep -qF -- "$want" "$BODYFILE"; then
    note "PR body states the current candidate state: $want"
  else
    bad "the PR body does not state the current candidate state ($want); it is the most visible authoritative surface in the delivery"
  fi
  if [ -n "$superseded" ] && grep -qF -- "$superseded" "$BODYFILE"; then
    # Historical mentions are legitimate; a STATUS line is not.
    grep -iE "^[[:space:]]*(\*\*)?status" "$BODYFILE" > "$BODYFILE.status" 2>/dev/null || true
    if grep -qF -- "$superseded" "$BODYFILE.status"; then
      bad "the PR body's Status still announces the superseded state: $superseded"
    else
      note "the superseded state appears only outside the Status line (historical); allowed"
    fi
  fi
fi

# Phase 3 is not accepted and not closed while this activity holds; the PR must not say otherwise.
case "$activity" in
  *PHASE_3*)
    if grep -qiE "phase 3 (is )?(accepted|closed|complete and accepted)" "$BODYFILE"; then
      bad "the PR body claims Phase 3 is accepted or closed; the repository says it is IN_PROGRESS"
    fi
    ;;
esac

if [ "$fail" -eq 0 ]; then
  printf 'PR_METADATA_ZERO_STALE = PASS\n'; exit 0
fi
printf 'PR_METADATA_ZERO_STALE = FAIL (%d)\n' "$fail"; exit 1
