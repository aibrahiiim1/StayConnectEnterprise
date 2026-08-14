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
#
# ORDER MATTERS: the LIVE PR is read FIRST, and the webhook payload is only a fallback.
#
# This used to read GITHUB_EVENT_PATH first. That payload is a SNAPSHOT taken when the event fired, so a
# re-run replays the ORIGINAL body no matter what the PR says now -- which means correcting a stale PR body
# and re-running the job could never turn it green, and the only way out was to push a commit. Observed on
# run 31746141832: attempt 1 failed on a missing receipt id, the body was corrected, attempt 2 replayed the
# old body and failed identically. A check on a live surface has to read the live surface.
body=""
if [ -n "${GITHUB_TOKEN:-}" ]; then
  # BY NUMBER FIRST. On a pull_request event GITHUB_REF_NAME is the MERGE ref ("12/merge"), not the source
  # branch, so a `head=owner:$GITHUB_REF_NAME` query matches nothing, returns empty, and this check silently
  # fell through to the frozen webhook payload -- the exact fallback it was rewritten to avoid. The event
  # payload still carries the PR NUMBER, which is the unambiguous handle; the branch query is the fallback
  # for a push build where no PR number is available.
  prnum=""
  if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "${GITHUB_EVENT_PATH}" ]; then
    prnum="$($PY3 -c "
import json,sys
try: e=json.load(open(sys.argv[1],encoding='utf-8'))
except Exception: sys.exit(0)
pr=e.get('pull_request') or {}
n=pr.get('number') or (e.get('issue') or {}).get('number')
sys.stdout.write(str(n) if n else '')
" "$GITHUB_EVENT_PATH" 2>/dev/null)"
  fi
  if [ -n "$prnum" ]; then
    body="$(curl -sS -H "Authorization: Bearer $GITHUB_TOKEN"         "https://api.github.com/repos/$REPO/pulls/$prnum" 2>/dev/null       | $PY3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: d={}
sys.stdout.write(d.get('body') or '')
" 2>/dev/null)"
    [ -n "$body" ] && note "PR body read LIVE from the API by number (#$prnum), not from the frozen event payload"
  fi
  if [ -z "$body" ]; then
    # GITHUB_HEAD_REF is the SOURCE branch on a pull_request event; GITHUB_REF_NAME is the branch on a push.
    ref="${GITHUB_HEAD_REF:-${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null)}}"
    owner="${REPO%%/*}"
    body="$(curl -sS -H "Authorization: Bearer $GITHUB_TOKEN"         "https://api.github.com/repos/$REPO/pulls?state=open&head=$owner:$ref" 2>/dev/null       | $PY3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: d=[]
sys.stdout.write((d[0].get('body') or '') if isinstance(d,list) and d else '')
" 2>/dev/null)"
    [ -n "$body" ] && note "PR body read LIVE from the API by head branch ($ref)"
  fi
fi
if [ -z "$body" ] && [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "${GITHUB_EVENT_PATH}" ]; then
  body="$($PY3 -c "
import json,sys
try: e=json.load(open(sys.argv[1],encoding='utf-8'))
except Exception: sys.exit(0)
pr=e.get('pull_request') or {}
sys.stdout.write(pr.get('body') or '')
" "$GITHUB_EVENT_PATH" 2>/dev/null)"
fi
# (Kept as a last resort for a token that only became available after the block above ran.)
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

# ---- the current candidate state, DERIVED FROM CANONICAL STATE -------------------------------------------
#
# This used to be a `case` over current_activity mapping each activity to a hard-coded phrase, and the
# ACCEPTED_AND_CLOSED arm demanded the literal string "ACCEPTED_AND_CLOSED AT DARK MATURITY". That phrase is
# Phase-3's maturity. When Phase 4 closed at LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity, a PR body that stated
# the true current state exactly and completely was reported as stale, and the only way to satisfy the gate
# would have been to write a false maturity into the most visible surface in the delivery. A staleness check
# that can only be satisfied by a wrong statement is worse than none.
#
# So nothing about the expected wording is hard-coded now. Everything comes from canonical state:
#
#   phases[current_phase].status     the status TOKEN the body must carry (ACCEPTED_AND_CLOSED, IN_PROGRESS)
#   phases[current_phase].maturity   the maturity the body must not contradict
#   latest_accepted_po_decision      the decision id that granted the current status (D19)
#   latest_transition_id             the receipt that recorded it (T0044)
#
# A phase closing at a maturity nobody has invented yet needs no edit here.
META="$($PY3 -c "
import json,sys
d=json.load(open(sys.argv[1],encoding='utf-8'))
cp=str(d.get('current_phase',''))
ph=(d.get('phases') or {}).get(cp) or {}
print(cp)
print(str(ph.get('status') or ''))
print(str(d.get('latest_accepted_po_decision') or ''))
print(str(d.get('latest_transition_id') or ''))
" "$STATE" 2>/dev/null)"
cur_phase="$(printf '%s' "$META" | sed -n '1p')"
cur_status="$(printf '%s' "$META" | sed -n '2p')"
cur_decision="$(printf '%s' "$META" | sed -n '3p')"
cur_transition="$(printf '%s' "$META" | sed -n '4p')"

if [ -z "$cur_phase" ] || [ -z "$cur_status" ]; then
  bad "current_phase / phases[current_phase].status could not be read; the PR-body check cannot be derived"
else
  note "derived from canonical state: phase $cur_phase is $cur_status (decision ${cur_decision:-none}, receipt ${cur_transition:-none})"

  # 1. the body must carry the recorded STATUS TOKEN, in either spelling.
  token_re="$(printf '%s' "$cur_status" | sed 's/_/[ _]/g')"
  if grep -qiE -- "$token_re" "$BODYFILE"; then
    note "PR body states the recorded status for phase $cur_phase: $cur_status"
  else
    bad "the PR body does not state the recorded status for phase $cur_phase ($cur_status); it is the most visible authoritative surface in the delivery"
  fi

  # 2. when a decision and a receipt granted that status, the body must cite them. A status without its
  #    authority is a claim; with them it is checkable.
  case "$cur_status" in
    ACCEPTED_AND_CLOSED|FINAL_CLOSED)
      for ref in "$cur_decision" "$cur_transition"; do
        [ -n "$ref" ] || continue
        if grep -qF -- "$ref" "$BODYFILE"; then
          note "PR body cites $ref"
        else
          bad "the PR body states the phase is $cur_status but never cites $ref, the record that granted it"
        fi
      done
      # 3. and its Status line must not simultaneously announce an unfinished state.
      grep -iE "^[[:space:]]*[>*# ]*(\*\*)?status" "$BODYFILE" > "$BODYFILE.status" 2>/dev/null || true
      if grep -qiE "in[ _-]progress|not accepted|not closed|acceptance candidate|awaiting (product[- ]owner )?acceptance" "$BODYFILE.status"; then
        bad "the PR body's Status line announces an unfinished state while the repository records $cur_status"
      else
        note "the PR body's Status line does not contradict the recorded status"
      fi
      ;;
    *)
      # The mirror direction: an unfinished phase must not have a PR body announcing acceptance.
      if grep -qiE "phase[- ]?$cur_phase (is )?(accepted and closed|accepted|closed)" "$BODYFILE"; then
        bad "the PR body claims phase $cur_phase is accepted or closed; the repository records $cur_status"
      else
        note "the PR body does not claim an acceptance the repository has not recorded"
      fi
      ;;
  esac
fi

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
