#!/usr/bin/env bash
# TREE-EQUALITY EVIDENCE REUSE — has this gate ALREADY proven THIS EXACT CONTENT green?
#
# WHY THIS EXISTS. Every delivery ran the full required validation twice: once on the pull-request head and
# again on the merge commit pushed to master. Measured over the last thirteen merges in this repository, the
# merge commit's GIT TREE was byte-identical to the pull-request head's tree in THIRTEEN of THIRTEEN cases --
# a merge introduces a commit, not content. The second run therefore spent roughly half an hour of the
# delivery path re-deriving, from identical bytes, a verdict it already held.
#
# WHAT MAKES REUSE SOUND HERE, rather than merely convenient:
#
#   THE KEY IS THE TREE, NOT THE COMMIT. Two commits with the same tree hash have byte-identical content in
#   every tracked path. A verdict that is a pure function of that content is the same verdict, and saying so
#   is arithmetic rather than optimism.
#
#   THE GATE IS INSIDE THE TREE. .github/workflows/**, tools/**, scripts/** and governance/** are all
#   tracked, so an identical tree means the same workflow definition running the same validators over the
#   same inputs. There is no "same content, different checks" case to worry about: a changed check is a
#   changed tree, and a changed tree never matches.
#
#   THE EVIDENCE IS GITHUB'S OWN. The match is looked up in the repository's real workflow-run history, in
#   this run. Nothing is cached by us, nothing rides in an artifact a job could write, and nothing counts as
#   proof that GitHub does not already record as a successful run of THIS workflow.
#
#   IT IS SCOPED TO ONE WORKFLOW. A green Phase-4 run says nothing about governance, so only successful runs
#   of the same workflow file are considered.
#
# FAIL CLOSED, ALWAYS. Every path that cannot establish a match -- no token, an API error, an unparsable
# response, no candidate, an object this checkout does not have -- reports NO HIT, and the caller runs the
# full validation. Reuse can only ever remove duplicated work; it can never be why something went unchecked.
#
# Usage:  evidence-reuse.sh <workflow-file.yml>
# Emits to $GITHUB_OUTPUT:  hit=true|false  tree=<sha>  matched_run=<id>  matched_sha=<sha>
set -uo pipefail

WF="${1:?usage: evidence-reuse.sh <workflow-file.yml>}"
REPO="${GITHUB_REPOSITORY:-aibrahiiim1/StayConnectEnterprise}"
SHA="${GITHUB_SHA:?GITHUB_SHA is required}"
SELF_RUN="${GITHUB_RUN_ID:-0}"
OUT="${GITHUB_OUTPUT:-/dev/stdout}"
LOOKBACK="${EVIDENCE_REUSE_LOOKBACK:-40}"

emit() { printf '%s\n' "$*" >> "$OUT"; }
no_hit() { echo "  -> NO REUSE: $*"; echo "  the full validation will run."; emit "hit=false"; exit 0; }

PY=python3; python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "  no usable python"; emit "hit=false"; exit 0; }

echo "== tree-equality evidence reuse =="

TREE="$(git rev-parse "$SHA^{tree}" 2>/dev/null)"
[ -n "$TREE" ] || no_hit "this commit's tree could not be resolved"
echo "  this commit: $SHA"
echo "  this tree:   $TREE"

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

RUNS="$(api "https://api.github.com/repos/$REPO/actions/workflows/$WF/runs?status=success&per_page=$LOOKBACK")"
[ -n "$RUNS" ] || no_hit "the workflow-run history could not be read"

# Candidate head SHAs: successful runs of THIS workflow, excluding this run and this commit.
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
    print("%s %s" % (r.get("id"), sha))
' "$SELF_RUN" "$SHA" | tr -d '\r')"

[ -n "$CANDS" ] || no_hit "no earlier successful run of $WF to compare against"

# TREES COME FROM GIT, NOT FROM THE API. Asking the API per candidate put a network call inside a loop and
# made the answer depend on it. git already knows, and the commit that matters is always present where it
# matters: on a merge to master the pull-request head is a PARENT of the commit being validated.
#
# Nothing is fetched here. `git fetch --depth=1` into a full clone makes the clone SHALLOW and discards the
# history this lookup depends on -- it broke this script's own testing before it was removed. A candidate
# whose object is absent is skipped; it cannot become a match, so skipping it can only cost a re-run.
n=0
resolved=0
while read -r rid rsha; do
    [ -n "$rsha" ] || continue
    n=$((n + 1))
    rtree="$(git rev-parse -q --verify "$rsha^{tree}" 2>/dev/null)"
    [ -n "$rtree" ] || continue
    resolved=$((resolved + 1))
    if [ "$rtree" = "$TREE" ]; then
        echo "  MATCH: run $rid already validated this exact tree"
        echo "    matched commit: $rsha"
        echo "    tree:           $rtree"
        echo "  Identical tree means identical content in every tracked path, INCLUDING this workflow"
        echo "  definition and the validators it runs, so the verdict cannot differ. The heavy validation is"
        echo "  reused rather than recomputed; checks whose input is NOT the tree still run below."
        emit "hit=true"
        emit "tree=$TREE"
        emit "matched_run=$rid"
        emit "matched_sha=$rsha"
        exit 0
    fi
done <<< "$CANDS"

no_hit "none of the $resolved resolvable candidate tree(s), out of $n successful run(s), matched"
