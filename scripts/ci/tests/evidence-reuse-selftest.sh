#!/usr/bin/env bash
# DOES THE REUSE LOOKUP ACTUALLY REFUSE THE EVIDENCE IT CLAIMS TO REFUSE?
#
# scripts/ci/evidence-reuse.sh reports a hit on this repository's delivery path, which is also what a script
# that returns "hit" unconditionally would do -- and a script that never matches is equally invisible, which
# is the shape all three of its earlier bugs took. So this drives the REAL script (never a copy) against a
# purpose-built git repository with a KNOWN topology and a STUBBED run history, and asserts each eligibility
# rule separately by making exactly one of them false at a time.
#
# The stub is the run-history JSON only. Everything the decision actually turns on -- tree hashes, ancestry --
# is computed by git from real objects, so the rules are exercised rather than simulated.
#
# It touches no network, no database, no appliance, and it never writes inside the repository.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="$ROOT/scripts/ci/evidence-reuse.sh"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT

PY=python3; python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "INFRA: no usable python"; exit 2; }

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# ---------------------------------------------------------------------------------------------------------
# A fixture repository with the exact topology the rules are about.
#
#   base --- A ------------- M      A is the "pull-request head": same tree as M, and an ancestor of it.
#             \             /       M is the merge commit being validated.
#              `--------- (2nd parent)
#   base --- X                      X carries the SAME TREE as A but lives on an unrelated branch, so it is
#                                   NOT an ancestor of M. This is the cross-context case.
#   base --- D                      D is an ancestor of nothing relevant and has a DIFFERENT tree.
R="$W/repo"
mkdir -p "$R"
git init -q "$R"
git -C "$R" config user.email selftest@example.invalid
git -C "$R" config user.name  selftest
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"

echo base > "$R/f.txt"; git -C "$R" add -A; git -C "$R" commit -qm base
BASE="$(git -C "$R" rev-parse HEAD)"

echo delivered > "$R/f.txt"; git -C "$R" add -A; git -C "$R" commit -qm "PR head"
A="$(git -C "$R" rev-parse HEAD)"
TREE_A="$(git -C "$R" rev-parse HEAD^{tree})"

# X: an unrelated branch that renders the IDENTICAL tree. Built from the tree object directly, so the match
# is genuine content equality rather than a coincidence of file names.
X="$(git -C "$R" commit-tree "$TREE_A" -p "$BASE" -m "unrelated branch, identical content")"

# D: a different tree, on the same lineage, so only the tree rule can reject it.
git -C "$R" checkout -q "$BASE"
echo something-else > "$R/f.txt"; git -C "$R" add -A; git -C "$R" commit -qm "different content"
D="$(git -C "$R" rev-parse HEAD)"

# M: the merge commit under validation. Its tree is A's tree.
M="$(git -C "$R" commit-tree "$TREE_A" -p "$D" -p "$A" -m "merge")"
git -C "$R" checkout -q "$M" 2>/dev/null || git -C "$R" reset -q --hard "$M"
unset GIT_AUTHOR_DATE GIT_COMMITTER_DATE

iso(){ "$PY" -c "import datetime,sys;print((datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(hours=float(sys.argv[1]))).strftime('%Y-%m-%dT%H:%M:%SZ'))" "$1"; }
FRESH="$(iso 0.1)"
STALE="$(iso 100)"

runs_json(){ # runs_json <id> <sha> <started>
  "$PY" -c '
import json, sys
print(json.dumps({"workflow_runs": [
    {"id": int(sys.argv[1]), "head_sha": sys.argv[2], "conclusion": "success", "run_started_at": sys.argv[3]}
]}))' "$1" "$2" "$3"
}

# Drive the real script. OUTF is a fresh $GITHUB_OUTPUT each time, so `hit=` is read rather than guessed.
drive(){ # drive <runs-json> [extra env assignments...]
  OUTF="$W/out.$$"; : > "$OUTF"
  OUT="$(cd "$R" && env GITHUB_SHA="$M" GITHUB_RUN_ID=999 GITHUB_OUTPUT="$OUTF" \
        GITHUB_TOKEN="${TOKEN_OVERRIDE-stub}" GITHUB_REPOSITORY="owner/repo" \
        EVIDENCE_REUSE_RUNS_JSON="$1" EVIDENCE_REUSE_MAX_AGE_HOURS="${AGE_OVERRIDE-24}" \
        bash "$SCRIPT" some-gate.yml 2>&1)"
  HIT="$(grep -E '^hit=' "$OUTF" | tail -1 | cut -d= -f2)"
}

echo "== evidence-reuse eligibility self-test =="
echo "  fixture: PR head A=${A:0:12}  merge M=${M:0:12}  unrelated-same-tree X=${X:0:12}  other-tree D=${D:0:12}"

# 1. THE LEGITIMATE CASE. Same gate, identical tree, ancestor, fresh. This must HIT, or every rejection below
#    is meaningless -- a script that never matches passes all of them.
drive "$(runs_json 111 "$A" "$FRESH")"
if [ "$HIT" = "true" ]; then
  case "$OUT" in *"MATCH: run 111"*) ok "the delivery case hits: identical tree on an ancestor, validated minutes ago" ;;
                 *) no "the delivery case hits" "hit=true but the match was not reported: $OUT" ;; esac
else
  no "the delivery case hits" "hit=$HIT :: $OUT"
fi

# 2. CROSS-CONTEXT. Identical tree, same gate, fresh -- but an unrelated commit that is NOT an ancestor.
drive "$(runs_json 222 "$X" "$FRESH")"
if [ "$HIT" = "true" ]; then
  no "an identical tree on a NON-ANCESTOR is refused" "cross-context evidence was reused"
else
  case "$OUT" in *"NOT an ancestor"*) ok "an identical tree on a NON-ANCESTOR is refused, and says so" ;;
                 *) no "an identical tree on a non-ancestor is refused" "refused for the wrong reason: $OUT" ;; esac
fi

# 3. STALE EVIDENCE. Identical tree AND an ancestor, but the run is 100h old. This is the revert-of-a-revert
#    shape: old content legitimately returns, and a months-old verdict must not return with it.
drive "$(runs_json 333 "$A" "$STALE")"
if [ "$HIT" = "true" ]; then
  no "evidence older than the age limit is refused" "a 100h-old verdict was reused"
else
  case "$OUT" in *"too old"*|*"h old (limit"*) ok "evidence older than the age limit is refused, and says how old" ;;
                 *) no "stale evidence is refused" "refused for the wrong reason: $OUT" ;; esac
fi

# 4. DIFFERENT CONTENT. The rule the whole mechanism rests on.
drive "$(runs_json 444 "$D" "$FRESH")"
if [ "$HIT" = "true" ]; then
  no "a different tree is refused" "a verdict about different content was reused"
else
  ok "a different tree is refused"
fi

# 5. NO TOKEN. Inability to establish equivalence must fail closed, not fail open.
TOKEN_OVERRIDE="" drive "$(runs_json 555 "$A" "$FRESH")"
if [ "$HIT" = "true" ]; then
  no "no token fails closed" "a hit was reported with no way to read the run history"
else
  case "$OUT" in *"no GITHUB_TOKEN"*) ok "no token fails closed" ;;
                 *) no "no token fails closed" "wrong reason: $OUT" ;; esac
fi

# 6. AN UNREADABLE TIMESTAMP. Age cannot be established, so equivalence cannot be established.
drive "$(runs_json 666 "$A" "not-a-timestamp")"
if [ "$HIT" = "true" ]; then
  no "an unreadable run time fails closed" "age was not established and the verdict was reused anyway"
else
  case "$OUT" in *"could not be read"*) ok "an unreadable run time fails closed" ;;
                 *) no "an unreadable run time fails closed" "wrong reason: $OUT" ;; esac
fi

# 7. A FAILED RUN IS NOT EVIDENCE, even on an identical tree.
FAILED_JSON="$("$PY" -c '
import json, sys
print(json.dumps({"workflow_runs": [
    {"id": 777, "head_sha": sys.argv[1], "conclusion": "failure", "run_started_at": sys.argv[2]}
]}))' "$A" "$FRESH")"
drive "$FAILED_JSON"
if [ "$HIT" = "true" ]; then no "a FAILED run is never reused" "a failed run satisfied the gate"
else ok "a failed run is never reused"; fi

# 8. AN EMPTY HISTORY. No candidate is not a pass.
drive '{"workflow_runs": []}'
if [ "$HIT" = "true" ]; then no "an empty run history fails closed" "hit with nothing to match"
else ok "an empty run history fails closed"; fi

# 9. THE RUN CANNOT SATISFY ITSELF.
drive "$(runs_json 999 "$A" "$FRESH")"   # 999 == GITHUB_RUN_ID
if [ "$HIT" = "true" ]; then no "a run cannot reuse its own result" "the run matched itself"
else ok "a run cannot reuse its own result"; fi

echo "------------------------------------------------------------"
echo "EVIDENCE_REUSE_SELFTEST pass=$pass fail=$fail"
[ "$fail" -eq 0 ] || exit 1
echo "the lookup hits on the real delivery shape and refuses cross-context, stale, different-content,"
echo "unreadable, failed, absent and self-referential evidence"
exit 0
