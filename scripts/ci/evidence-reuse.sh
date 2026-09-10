#!/usr/bin/env bash
# TREE-EQUALITY EVIDENCE REUSE — has this gate ALREADY proven THIS EXACT CONTENT green, in a context whose
# every relevant input is provably the same?
#
# WHY THIS EXISTS. Every delivery ran the full required validation twice: once on the pull-request head and
# again on the merge commit pushed to master. Measured over the last thirteen merges in this repository, the
# merge commit's GIT TREE was byte-identical to the pull-request head's tree in THIRTEEN of THIRTEEN cases --
# a merge introduces a commit, not content. The second run therefore spent roughly half an hour of the
# delivery path re-deriving, from identical bytes, a verdict it already held.
#
# WHAT THIS SCRIPT DOES AND DOES NOT DECIDE. It answers ONE question: may the TREE-PURE steps of this gate be
# satisfied by an earlier run? It does NOT decide which steps those are. That classification lives in
# governance/ci-reuse-policy.json, is enforced by tools/validate-ci-reuse-policy.py, and is fail-closed: a
# step nobody has classified makes the governance gate FAIL rather than silently inheriting a skip.
#
# ---------------------------------------------------------------------------------------------------------
# WHY A TREE HASH ALONE IS NOT ENOUGH, which is what the first version of this script got wrong.
#
# Tree equality proves that two commits have byte-identical CONTENT. It does NOT prove they have the same
# HISTORY, were validated at the same TIME, or ran against the same EXTERNAL WORLD. Three real checks in this
# repository depend on exactly those things:
#
#   tools/validate-transition-times.sh reads `git log --diff-filter=A` for a receipt's INTRODUCING COMMIT and
#   `git log -1 <merge-commit>` for the moment a merge actually happened. Both are functions of the commit
#   graph. Two commits with the same tree can have different graphs and therefore different verdicts.
#
#   scripts/ci/phase4-dependency-gate.sh runs `npm audit` against the LIVE registry and compares the result
#   with acceptances that carry an `expires_on` date checked against TODAY. The same tree is a PASS today and
#   a FAIL after a new advisory is published or an acceptance lapses.
#
#   The evidence artifacts are PRODUCED OUTPUT, not a verdict. Skipping them does not weaken a check; it
#   means the artifact for the commit being accepted does not exist at all.
#
# Those steps are now classified `always` in the policy and run on every run, hit or not. This script's job
# is to make the remaining reuse SOUND, which needs more than the tree:
#
#   1. SAME GATE. Only successful runs of the same workflow file count. A green Phase-4 run can never satisfy
#      governance.
#
#   2. IDENTICAL TREE. Byte-identical content in every tracked path -- and .github/workflows/**, tools/**,
#      scripts/** and governance/** are all tracked, so an identical tree is also the same gate definition,
#      the same validators, the same policy file. A changed check is a changed tree, and a changed tree never
#      matches.
#
#   3. ANCESTRY. The matched commit must be an ANCESTOR of the commit being validated (or the same commit, on
#      a re-run). This is precisely the delivery relationship -- a merge commit has the pull-request head as a
#      parent -- and it was true for twelve of the last twelve merges here. It is what rules out CROSS-CONTEXT
#      evidence: an unrelated branch, a fork, or a stray commit that happens to produce the same tree is not
#      an ancestor, so its verdict can never be borrowed.
#
#   4. RECENCY. The matched run must be recent (EVIDENCE_REUSE_MAX_AGE_HOURS, default 24). The legitimate
#      case is a merge minutes after its pull request was validated. A bound stops evidence from being
#      RESURRECTED: a revert-of-a-revert restores an old tree and IS a descendant, so ancestry alone would
#      let a months-old verdict stand for content being re-accepted today, on a different runner image, with
#      a different toolchain patch level. Twenty-four hours keeps the entire delivery-path saving and gives
#      stale proof nowhere to hide.
#
# FAIL CLOSED, ALWAYS. Every path that cannot establish all four -- no token, an API error, an unparsable
# response, no candidate, an object this checkout does not have, an unreadable timestamp -- reports NO HIT,
# and the caller runs the full validation. Reuse can only ever remove duplicated work; it can never be why
# something went unchecked.
#
# IT SAYS WHY IT DECLINED. Every earlier bug in this script made it SILENTLY NEVER REUSE, which is
# indistinguishable from working. Rejections are counted by reason and printed.
#
# Usage:  evidence-reuse.sh <workflow-file.yml>
# Emits to $GITHUB_OUTPUT:  hit=true|false  tree=<sha>  matched_run=<id>  matched_sha=<sha>  matched_at=<iso>
set -uo pipefail

WF="${1:?usage: evidence-reuse.sh <workflow-file.yml>}"
REPO="${GITHUB_REPOSITORY:-aibrahiiim1/StayConnectEnterprise}"
SHA="${GITHUB_SHA:?GITHUB_SHA is required}"
SELF_RUN="${GITHUB_RUN_ID:-0}"
OUT="${GITHUB_OUTPUT:-/dev/stdout}"
LOOKBACK="${EVIDENCE_REUSE_LOOKBACK:-40}"
MAX_AGE_HOURS="${EVIDENCE_REUSE_MAX_AGE_HOURS:-24}"

emit() { printf '%s\n' "$*" >> "$OUT"; }
no_hit() { echo "  -> NO REUSE: $*"; echo "  the full validation will run."; emit "hit=false"; exit 0; }

PY=python3; python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "  no usable python"; emit "hit=false"; exit 0; }

echo "== evidence reuse: same gate + identical tree + ancestry + recency =="

TREE="$(git rev-parse "$SHA^{tree}" 2>/dev/null)"
[ -n "$TREE" ] || no_hit "this commit's tree could not be resolved"
echo "  this commit: $SHA"
echo "  this tree:   $TREE"
echo "  max evidence age: ${MAX_AGE_HOURS}h"

[ -n "${GITHUB_TOKEN:-}" ] || no_hit "no GITHUB_TOKEN, so the run history cannot be consulted"

# </dev/null keeps curl off the caller's stdin; retried because an intermittently empty response must not be
# read as "never validated" -- that direction is safe, but a check that fails for reasons it cannot see is
# one nobody can reason about.
api() {
  local url="$1" out="" i
  for i in 1 2 3; do
    out="$(curl -sS --max-time 25 -H "Authorization: Bearer $GITHUB_TOKEN" \
             -H "Accept: application/vnd.github+json" "$url" </dev/null 2>/dev/null)"
    [ -n "$out" ] && { printf '%s' "$out"; return 0; }
    sleep "$i"
  done
  return 1
}

RUNS="${EVIDENCE_REUSE_RUNS_JSON:-}"
if [ -z "$RUNS" ]; then
  RUNS="$(api "https://api.github.com/repos/$REPO/actions/workflows/$WF/runs?status=success&per_page=$LOOKBACK")"
fi
[ -n "$RUNS" ] || no_hit "the workflow-run history could not be read"

# Candidate runs: successful runs of THIS workflow, excluding this run and this commit. Each line is
# "<run id> <head sha> <started at>".
#
# The JSON arrives on STDIN and the program is passed with -c, so stdin carries data and only data. tr -d
# strips carriage returns: a python that writes CRLF -- any Windows host, and this was developed on one --
# leaves a CR on every line but the last, which turns a valid SHA into an object git cannot find. That
# produced a lookup which resolved only the final candidate and silently never matched, which is exactly the
# quiet no-op this mechanism must not become.
CANDS="$(printf '%s' "$RUNS" | "$PY" -c '
import json, sys
self_run, self_sha = sys.argv[1], sys.argv[2]
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
seen = set()
for r in d.get("workflow_runs", []):
    if r.get("conclusion") != "success":
        continue
    if str(r.get("id")) == self_run:
        continue
    sha = r.get("head_sha") or ""
    if not sha or sha == self_sha or sha in seen:
        continue
    seen.add(sha)
    print("%s %s %s" % (r.get("id"), sha, r.get("run_started_at") or r.get("created_at") or "-"))
' "$SELF_RUN" "$SHA" | tr -d '\r')"

[ -n "$CANDS" ] || no_hit "no earlier successful run of $WF to compare against"

# How old is a run, in whole seconds? Fails loudly (empty) rather than returning a number nobody checked.
age_seconds() { "$PY" -c '
import datetime, sys
try:
    t = datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00"))
except Exception:
    sys.exit(1)
now = datetime.datetime.now(datetime.timezone.utc)
print(int((now - t).total_seconds()))
' "$1" 2>/dev/null; }

# TREES COME FROM GIT, NOT FROM THE API. Asking the API per candidate put a network call inside a loop and
# made the answer depend on it. git already knows, and the commit that matters is always present where it
# matters: on a merge to master the pull-request head is a PARENT of the commit being validated.
#
# Nothing is fetched here. `git fetch --depth=1` into a full clone makes the clone SHALLOW and discards the
# history this lookup depends on -- it broke this script's own testing before it was removed. A candidate
# whose object is absent is skipped; it cannot become a match, so skipping it can only cost a re-run.
n=0; resolved=0; tree_match=0
rej_unresolved=0; rej_tree=0; rej_ancestry=0; rej_age=0; rej_badtime=0
while read -r rid rsha rstarted; do
    [ -n "$rsha" ] || continue
    n=$((n + 1))
    rtree="$(git rev-parse -q --verify "$rsha^{tree}" 2>/dev/null)"
    if [ -z "$rtree" ]; then rej_unresolved=$((rej_unresolved + 1)); continue; fi
    resolved=$((resolved + 1))
    if [ "$rtree" != "$TREE" ]; then rej_tree=$((rej_tree + 1)); continue; fi
    tree_match=$((tree_match + 1))

    # ANCESTRY. Same content is not the same lineage. `--is-ancestor X X` is true, so a re-run of the very
    # same commit is allowed; an unrelated branch that happens to render the same tree is not.
    if ! git merge-base --is-ancestor "$rsha" "$SHA" 2>/dev/null; then
        echo "  rejected $rid ($rsha): identical tree, but it is NOT an ancestor of this commit"
        rej_ancestry=$((rej_ancestry + 1)); continue
    fi

    # RECENCY.
    age="$(age_seconds "$rstarted")"
    if [ -z "$age" ]; then
        echo "  rejected $rid: its start time ($rstarted) could not be read, so its age cannot be established"
        rej_badtime=$((rej_badtime + 1)); continue
    fi
    max=$((MAX_AGE_HOURS * 3600))
    if [ "$age" -gt "$max" ]; then
        echo "  rejected $rid ($rsha): identical tree and an ancestor, but the run is $((age / 3600))h old (limit ${MAX_AGE_HOURS}h)"
        rej_age=$((rej_age + 1)); continue
    fi

    echo "  MATCH: run $rid already validated this exact tree, and it qualifies on every rule"
    echo "    matched commit: $rsha"
    echo "    tree:           $rtree"
    echo "    started:        $rstarted ($((age / 60))m ago)"
    echo "    ancestor of this commit: yes"
    echo "  Identical tree means identical content in every tracked path, INCLUDING this workflow definition,"
    echo "  the validators it runs and governance/ci-reuse-policy.json. Only the steps that policy classifies"
    echo "  TREE-PURE are satisfied by this evidence; every step classified ALWAYS still runs below."
    emit "hit=true"
    emit "tree=$TREE"
    emit "matched_run=$rid"
    emit "matched_sha=$rsha"
    emit "matched_at=$rstarted"
    exit 0
done <<< "$CANDS"

echo "  considered $n successful run(s) of $WF: $resolved resolvable, $tree_match with an identical tree."
echo "  rejected -- object absent: $rej_unresolved, different tree: $rej_tree, not an ancestor: $rej_ancestry, too old: $rej_age, unreadable time: $rej_badtime"
no_hit "no candidate satisfied all of: same gate, identical tree, ancestry, and age under ${MAX_AGE_HOURS}h"
