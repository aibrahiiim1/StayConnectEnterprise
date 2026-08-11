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

# ---- A MERGED PR MUST NOT STILL ADVERTISE THAT IT IS UNMERGED --------------------------------------------
#
# Everything below this block looks for an OPEN PR and, finding none, passes: "there is no PR metadata that
# could be stale". That was true only while an unmerged PR was the only PR worth checking. After PR #6 was
# merged its page still carried MERGE_DECISION_PENDING in the title and "PR #6 remains OPEN and UNMERGED" in
# the body, and this script passed every time — because the merge is precisely what removed the PR from the
# query it was looking at. The PR page is the first thing a reviewer opens and the last thing anyone
# remembers to update, so the merged PR is now checked BY NUMBER, from the recorded facts.
merged_pr="$($PY3 -c "
import json,sys
try: f=json.load(open(sys.argv[1],encoding='utf-8')).get('current_state_facts') or {}
except Exception: f={}
print(f.get('merged_pr') or f.get('pr_number') or '' if f.get('merged') else '')
" "$STATE" 2>/dev/null)"

if [ -n "$merged_pr" ]; then
  if [ -z "${GITHUB_TOKEN:-}" ]; then
    note "PR #$merged_pr is recorded as merged; no GITHUB_TOKEN here, so its page could not be read (CI always can)"
  else
    MPR="$(mktemp)"
    curl -sS -H "Authorization: Bearer $GITHUB_TOKEN"       "https://api.github.com/repos/$REPO/pulls/$merged_pr" 2>/dev/null       | $PY3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: d={}
sys.stdout.write(((d.get('title') or '') + chr(10) + (d.get('body') or '')))
" > "$MPR" 2>/dev/null
    if [ ! -s "$MPR" ]; then
      bad "PR #$merged_pr is recorded as merged but its metadata could not be read; an unreadable surface is not a passing one"
    else
      # Same labelling contract as every other zero-stale check: a line that admits to being history is history.
      mstale() { grep -inE "$1" "$MPR" | grep -viE "historical|superseded|no longer|was then|at the time|until the merge|before the merge"; }
      if mstale "merge_decision_pending" >/dev/null; then
        bad "merged PR #$merged_pr still presents MERGE_DECISION_PENDING as its current state"
      else
        note "merged PR #$merged_pr does not present MERGE_DECISION_PENDING as current"
      fi
      if mstale "(remains |is )?open and unmerged|do not merge|not authorized to merge|merge is a separate product-owner decision" >/dev/null; then
        bad "merged PR #$merged_pr still says it is open/unmerged or must not be merged"
      else
        note "merged PR #$merged_pr does not claim to be open or unmerged"
      fi
      # The positive half: the page must actually record the merge, or it is merely silent about it.
      if grep -qiE "merged" "$MPR"; then
        note "merged PR #$merged_pr records the merge on its own page"
      else
        bad "merged PR #$merged_pr never states that it was merged; its page is silent on the fact that matters most"
      fi
    fi
    rm -f "$MPR"
  fi
fi

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
  # ...but "no OPEN PR" says nothing about the merged-PR checks above, and this exit used to report PASS
  # unconditionally. A merged PR advertising MERGE_DECISION_PENDING therefore printed two FAIL lines and a
  # green verdict. An early exit must still honour what has already failed.
  if [ "$fail" -eq 0 ]; then
    printf 'PR_METADATA_ZERO_STALE = PASS (no open PR)\n'; exit 0
  fi
  printf 'PR_METADATA_ZERO_STALE = FAIL (%d)\n' "$fail"; exit 1
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
  *ACCEPTED_AND_CLOSED*) want="ACCEPTED_AND_CLOSED AT DARK MATURITY"; superseded="DARK ACCEPTANCE CANDIDATE";;
  *DARK_ACCEPTANCE_CANDIDATE*) want="DARK ACCEPTANCE CANDIDATE"; superseded="INCREMENT-9 DURABILITY CORRECTION CANDIDATE";;
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

# The PR body and the repository must agree about acceptance, in BOTH directions.
#
# This check used to say only "the PR must not claim Phase 3 is accepted", which was correct for as long as
# acceptance had not happened — and would have become wrong the moment it did, forbidding the PR from stating
# the true final state. The direction is now taken from the recorded fact rather than assumed.
accepted="$($PY3 -c "
import json,sys
try: f=json.load(open(sys.argv[1],encoding='utf-8')).get('current_state_facts') or {}
except Exception: f={}
print('yes' if (f.get('accepted') and f.get('closed')) else 'no')
" "$STATE" 2>/dev/null)"
case "$activity" in
  *PHASE_3*)
    if [ "$accepted" = "yes" ]; then
      if grep -qiE "phase 3 (is )?accepted and closed|ACCEPTED_AND_CLOSED" "$BODYFILE"; then
        note "the PR body states the accepted-and-closed state, matching the repository"
      else
        bad "the repository records Phase 3 as ACCEPTED AND CLOSED but the PR body does not say so"
      fi
    else
      if grep -qiE "phase 3 (is )?(accepted|closed|complete and accepted)" "$BODYFILE"; then
        bad "the PR body claims Phase 3 is accepted or closed; the repository says it is not"
      fi
    fi
    ;;
esac

# ---- the PR must not contradict the recorded current state -------------------------------------------------
#
# The PR body is one document, and a reader scrolls it. It shipped with an accurate Increment-9 verdict table at
# the top and, a hundred lines lower, a "Restrictions still in force" block calling migration 0010 undeployed
# and a heading reading "Live Increment-9 evidence - PENDING". Both halves were once true. Only the top still
# was, and nothing compared them.
#
# These checks are semantic rather than lexical: each is derived from current_state_facts, and each excuses a
# hit whose own line admits to being history.
FACTS="$($PY3 -c "
import json,sys
try: d=json.load(open(sys.argv[1],encoding='utf-8')).get('current_state_facts') or {}
except Exception: d={}
keys=('live_increment9_executed','migration_0010_applied_production','guest_portal_uniform_budget_ms','surgical_foundation_retired_from_procedure')
print(json.dumps({k:d.get(k) for k in keys}))
" "$STATE" 2>/dev/null)"

fact_true() { printf '%s' "$FACTS" | grep -q "\"$1\": true"; }
stale_lines() { grep -inE "$1" "$BODYFILE" | grep -viE "historical|superseded|no longer|was then|already (happened|occurred)|have (occurred|already)|as at|executed"; }

if fact_true live_increment9_executed; then
  if stale_lines "live[- ]increment-?9 evidence[^a-z]{0,4}pending|increment-?9 evidence: pending" >/dev/null; then
    bad "the PR body still presents Live Increment-9 evidence as PENDING; it was executed on 2026-08-10"
  else
    note "the PR body does not present Increment-9 evidence as pending"
  fi
  if stale_lines "no appliance (access|contact)|no live[- ]?pms contact|no production (db|database) (access|contact)" >/dev/null; then
    bad "the PR body denies appliance/Production-DB/live-PMS contact that has already occurred"
  else
    note "the PR body does not deny live contact that has already occurred"
  fi
fi

if fact_true migration_0010_applied_production; then
  if stale_lines "migration 0010[^.]{0,30}undeployed|0010[^.]{0,20}undeployed" >/dev/null; then
    bad "the PR body calls migration 0010 undeployed; it is applied on the site database"
  else
    note "the PR body does not call migration 0010 undeployed"
  fi
fi

budget="$(printf '%s' "$FACTS" | $PY3 -c "import json,sys;print(json.load(sys.stdin).get('guest_portal_uniform_budget_ms') or '')" 2>/dev/null)"
if [ -n "$budget" ] && [ "$budget" != "1200" ]; then
  if stale_lines "1200 ?ms" >/dev/null; then
    bad "the PR body presents 1200ms as a current Guest-Portal budget; the recorded budget is ${budget}ms"
  else
    note "the PR body carries no unlabelled superseded Guest-Portal budget"
  fi
fi

if fact_true surgical_foundation_retired_from_procedure; then
  if grep -inE "phase3-foundation (install|rollback)" "$BODYFILE" | grep -viE "retired|diagnostic|do not run|historical|superseded" >/dev/null; then
    bad "the PR body still presents the retired surgical foundation install/rollback as a procedure step"
  else
    note "the PR body does not require the retired surgical-foundation step"
  fi
fi

if [ "$fail" -eq 0 ]; then
  printf 'PR_METADATA_ZERO_STALE = PASS\n'; exit 0
fi
printf 'PR_METADATA_ZERO_STALE = FAIL (%d)\n' "$fail"; exit 1
