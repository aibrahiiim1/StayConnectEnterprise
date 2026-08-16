#!/usr/bin/env bash
# validate-project-state.sh — enforces the Zero-Stale-Leftovers rule (docs/ZERO_STALE_LEFTOVERS_RULE.md).
#
# PORTABLE: runs in two modes.
#   repository mode      — validates repository sources + generated Project Pack + Evidence Pack dirs.
#   extracted-pack mode  — validates already-extracted Project/Evidence Pack dirs with no repository layout.
#
# Path resolution (flags override env; env overrides auto-detect):
#   --repo-root DIR / REPO_ROOT
#   --project-pack-dir DIR / PROJECT_PACK_DIR
#   --evidence-pack-dir DIR / EVIDENCE_PACK_DIR
#
# Rules:
#   * supplied paths are never silently ignored (a bad path is a hard error);
#   * resolved paths are printed;
#   * repository-only checks are clearly classified as SKIPPED when no repository root is available
#     (they never cause failure in extracted-pack mode);
#   * pack checks always execute fully (hashes, links, maturity, next action, acceptance record,
#     permanent rule, validator presence, secret/PII scan);
#   * exit non-zero on any genuine pack failure.
#
# A matching stale line is excused ONLY by an inline per-line marker (historical, or — for architecture
# terms — an explicit negation). Broad file-level exceptions are NOT allowed.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-}"; PROJECT_PACK_DIR="${PROJECT_PACK_DIR:-}"; EVIDENCE_PACK_DIR="${EVIDENCE_PACK_DIR:-}"
usage(){ sed -n '2,25p' "$0"; }
while [ $# -gt 0 ]; do
  case "$1" in
    --repo-root) REPO_ROOT="${2:?}"; shift 2;;            --repo-root=*) REPO_ROOT="${1#*=}"; shift;;
    --project-pack-dir) PROJECT_PACK_DIR="${2:?}"; shift 2;; --project-pack-dir=*) PROJECT_PACK_DIR="${1#*=}"; shift;;
    --evidence-pack-dir) EVIDENCE_PACK_DIR="${2:?}"; shift 2;; --evidence-pack-dir=*) EVIDENCE_PACK_DIR="${1#*=}"; shift;;
    -h|--help) usage; exit 0;;
    *) echo "ERROR: unknown argument: $1" >&2; exit 2;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# auto-detect repository root only when nothing was supplied at all
if [ -z "$REPO_ROOT" ] && [ -z "$PROJECT_PACK_DIR" ] && [ -z "$EVIDENCE_PACK_DIR" ]; then
  cand="$(cd "$SCRIPT_DIR/.." && pwd)"
  [ -d "$cand/docs" ] && [ -d "$cand/exports" ] && REPO_ROOT="$cand"
fi
# a supplied repo root that actually looks like the repo enables repository-only checks
if [ -n "$REPO_ROOT" ]; then
  [ -d "$REPO_ROOT" ] || { echo "ERROR: --repo-root '$REPO_ROOT' is not a directory" >&2; exit 2; }
  { [ -d "$REPO_ROOT/docs" ] && [ -d "$REPO_ROOT/exports" ]; } || { echo "ERROR: --repo-root '$REPO_ROOT' has no docs/ + exports/ (not a repo root)" >&2; exit 2; }
  [ -n "$PROJECT_PACK_DIR" ]  || PROJECT_PACK_DIR="$REPO_ROOT/exports/chatgpt/stayconnectenterprise"
  [ -n "$EVIDENCE_PACK_DIR" ] || EVIDENCE_PACK_DIR="$REPO_ROOT/exports/chatgpt/phase-evidence"
fi
for v in PROJECT_PACK_DIR EVIDENCE_PACK_DIR; do
  p="${!v}"
  [ -n "$p" ] || { echo "ERROR: $v not resolved — supply --$(echo $v|tr 'A-Z_' 'a-z-') or --repo-root" >&2; exit 2; }
  [ -d "$p" ] || { echo "ERROR: $v='$p' is not a directory" >&2; exit 2; }
done
PROJECT_PACK_DIR="$(cd "$PROJECT_PACK_DIR" && pwd)"; EVIDENCE_PACK_DIR="$(cd "$EVIDENCE_PACK_DIR" && pwd)"
[ -n "$REPO_ROOT" ] && REPO_ROOT="$(cd "$REPO_ROOT" && pwd)"

MODE="extracted-pack"; [ -n "$REPO_ROOT" ] && MODE="repository"
PACK="$PROJECT_PACK_DIR"; EVID="$EVIDENCE_PACK_DIR"; DOCS="${REPO_ROOT:+$REPO_ROOT/docs}"
RULE="ZERO_STALE_LEFTOVERS_RULE.md"; MAT="PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED"
FAIL=0; SKIP=0
fail(){ echo "  FAIL: $*"; FAIL=$((FAIL+1)); }; ok(){ echo "  ok: $*"; }
skipped(){ echo "  SKIPPED (repository-only; no --repo-root): $*"; SKIP=$((SKIP+1)); }
have_repo(){ [ -n "$REPO_ROOT" ]; }

echo "=================================================="
echo "validate-project-state.sh — mode: $MODE"
echo "  REPO_ROOT          = ${REPO_ROOT:-<none — extracted-pack mode>}"
echo "  PROJECT_PACK_DIR   = $PACK"
echo "  EVIDENCE_PACK_DIR  = $EVID"
echo "=================================================="

HIST='HISTORICAL|SUPERSEDED|does \*\*not\*\* describe current|does not describe current status|that gate is now satisfied|that gate has since been satisfied|at Phase-0 close|originally approved for scratch|was originally|\[Historical|\(Historical:'
NEG='\bno\b|\bnot\b|\bnever\b|\bwithout\b'   # inline negation excuses an architecture-term reference

# Phase-1A-scoped stale current-status patterns (NOT the spike's generic gate "Planning only" section).
declare -a RULES=(
  'next authorized activity is Phase 1A \*?planning only|next authorized activity is .{0,40}planning only::Phase 1A named as planning-only (current)'
  'Phase 1A \*\*planning\*\* is authorized|Phase[- ]1A planning is authorized::Phase 1A planning stated as currently authorized'
  'implementation requires separate approval of the Phase 1A plan|implementation stays gated on .{0,30}approval of the Phase 1A plan|Phase 1A \*\*implementation\*\* requires separate approval::Phase 1A implementation gated on plan approval (current)'
  'until the Phase 1A plan is (separately )?approved|Forbidden Until the Phase 1A Plan Is Approved::Phase 1A blocked until plan approval (current)'
  'implemented and verified in scratch only|Phase-?1A is .{0,30}in scratch only|in scratch only[ ,.-]+not created on live::Phase 1A described as scratch-only'
  'not created on live|not yet created (on|in) (live|production)|live-dark .{0,20}(is|remains) (future|to be created|not yet)|separate approval to create dark .?iam_v2.? in live|Phase.?1A[^.]{0,40}live-dark creation[^.]{0,20}(future|still a future)::live-dark creation described as still-future'
  'READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL::stale approval-status label'
  'Phase.?1A[^.]{0,30}(is )?the current phase|the current phase[^.]{0,30}Phase.?1A::Phase 1A described as the current phase (stale — Phase 1B planning is current)'
  'pending final Product-Owner acceptance|Phase.?1A[^.]{0,40}pending[^.]{0,25}acceptance::Phase 1A described as pending acceptance (stale — accepted/closed)'
  'Phase.?1A[^.]{0,40}NOT live accepted|Phase.?1A[^.]{0,40}not live accepted::Phase 1A described as not-live-accepted at its authorized maturity (stale — accepted/closed)'
)
ARCH='blue/green|standby site db|standby .{0,15}stayconnect_site|with a standby|whole-database swap|whole-db swap|swap-back'  # superseded standby/whole-DB terms

echo "== 1. no stale Phase-1A current-status phrases (per-line historical excused) =="
declare -a SCAN=("$PACK"); have_repo && SCAN+=("$DOCS")
s1=0
for entry in "${RULES[@]}"; do
  pat="${entry%%::*}"; desc="${entry##*::}"
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    echo "$line" | grep -qiE "$HIST" && continue
    echo "    HIT [$desc]: $line"; s1=$((s1+1))
  done < <(grep -rniE "$pat" "${SCAN[@]}" --include=*.md 2>/dev/null)
done
[ "$s1" = "0" ] && ok "no stale Phase-1A current-status phrases ($([ ${#SCAN[@]} -gt 1 ] && echo 'docs + pack' || echo 'pack only'))" || fail "$s1 stale Phase-1A current-status line(s)"

echo "== 1b. no stale architecture terms as current unlabeled values (blue/green, standby site DB, whole-database swap, swap-back) =="
a1=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "$line" | grep -qiE "$HIST" && continue          # explicitly historical -> excused
  echo "$line" | grep -qiE "$NEG"  && continue          # explicit negation (\"no whole-database swap\") -> excused
  echo "    HIT [stale architecture term]: $line"; a1=$((a1+1))
done < <(grep -rniE "$ARCH" "${SCAN[@]}" --include=*.md 2>/dev/null | grep -v "validate-project-state.sh")
[ "$a1" = "0" ] && ok "no stale architecture terms presented as current values" || fail "$a1 stale architecture-term line(s)"

echo "== 1c. Phase-3 enforcement/evidence phrases must not drift =="
# These are the exact phrases a previous round got wrong, each in a way that read as correct:
#   - a stale preflight total in the Final Report (18/18 after the suite grew to 20);
#   - "N/N entries passed sha256sum -c", which miscounts a manifest that cannot list itself;
#   - any claim that removing a tc filter denies internet access, which it does not;
#   - any claim that the cutover is already flag-only before the surgical nft foundation is installed;
#   - any description of a modelled (fake-kernel) suite as real-kernel or live evidence;
#   - any description of a permanent authorization as fail-closed;
#   - current_activity and the Phase-3 stage narrative describing different stages;
#   - a superseded candidate-state declaration left in the Report or the Plan;
#   - the previous Portal budget (1200ms) quoted as the current one;
#   - Final-Report delivery evidence (run ids, artifact id, integrity hash) that is not the current run.
# Catching them here means the next drift of this class fails a gate instead of being reviewed for.
p3drift=0
REPORT="$DOCS/reports/StayConnect-IAM-Phase3-Final-Report.md"
if [ -f "$REPORT" ]; then
  # the preflight total in the report must match what the preflight actually reports
  actual_pf="$(bash "$REPO_ROOT/scripts/phase3-preflight.sh" --json 2>/dev/null | sed -n 's/.*"pass":\([0-9]*\).*/\1/p')"
  # NOTE: this used to read "$ROOT/..." — a variable this script never defines. Under `set -u` the unbound
  # expansion killed only the COMMAND SUBSTITUTION's subshell, so actual_pf came back empty and the guard
  # below skipped the entire check in silence. A Zero-Stale check that never runs is exactly the failure
  # this file exists to catch, so an unreadable total is now a HIT rather than a quiet pass.
  if [ -z "$actual_pf" ]; then
    echo "    HIT [preflight total unreadable]: the preflight could not be run, so the report's total cannot be checked"
    p3drift=$((p3drift+1))
  fi
  if [ -n "$actual_pf" ]; then
    grep -qE "PASS $actual_pf/$actual_pf" "$REPORT" || { echo "    HIT [stale preflight total]: report does not state PASS $actual_pf/$actual_pf"; p3drift=$((p3drift+1)); }
    for bad in 17 18 19 20 21; do
      [ "$bad" = "$actual_pf" ] && continue
      grep -qE "Offline preflight[^|]*\| \*\*PASS $bad/$bad\*\*" "$REPORT" && { echo "    HIT [stale preflight total]: PASS $bad/$bad still claimed"; p3drift=$((p3drift+1)); }
    done
  fi
fi
# no document may claim a tc-only action denies internet access
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "    HIT [tc-denies-access overclaim]: $line"; p3drift=$((p3drift+1))
done < <(grep -rniE "(deleting|removing|stripping) (a )?(tc )?(filter|class)[^.]{0,60}(denies|denying|blocks|cuts off)[^.]{0,30}(internet|access|forwarding)" "${SCAN[@]}" --include=*.md 2>/dev/null | grep -viE "does not|never|cannot|not equivalent|is not" | grep -v "validate-project-state.sh")
# the manifest-integrity wording must not claim the manifest verifies itself
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "    HIT [manifest self-count overclaim]: $line"; p3drift=$((p3drift+1))
done < <(grep -rniE "([0-9]+)/\1 (entries|files) (passed|verified)[^.]{0,40}sha256sum" "${SCAN[@]}" --include=*.md 2>/dev/null | grep -v "validate-project-state.sh" | grep -viE "wrong to describe|must not|never describe|do not call")
# no document may claim the cutover is already "flag-only" without the surgical nft foundation having been
# installed on the unit. The software makes it flag-only AFTERWARDS; saying so beforehand is the same class of
# overclaim as calling a modelled test live evidence.
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "    HIT [premature flag-only cutover claim]: $line"; p3drift=$((p3drift+1))
done < <(grep -rniE "cutover is (a )?flag[- ]only|flag[- ]only cutover is (ready|prepared|complete)" "${SCAN[@]}" --include=*.md 2>/dev/null | grep -viE "not .{0,20}flag-only|only after|is NOT|until|becomes flag-only|makes .{0,30}flag[- ]only|no document may|must not" | grep -v "validate-project-state.sh")
# no document may present a modelled (fake-kernel) suite as real-kernel or live evidence
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "    HIT [fake-kernel described as kernel/live evidence]: $line"; p3drift=$((p3drift+1))
done < <(grep -rniE "fake[- ]kernel[^.]{0,60}(live evidence|real[- ]kernel evidence|proves the kernel)" "${SCAN[@]}" --include=*.md 2>/dev/null | grep -viE "not |never |do not|must not" | grep -v "validate-project-state.sh")
# and no document may describe a Phase-3 authorization as permanent/non-expiring while calling it fail-closed
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "    HIT [permanent authorization described as fail-closed]: $line"; p3drift=$((p3drift+1))
done < <(grep -rniE "(permanent|non-expiring|never expires)[^.]{0,60}authorization[^.]{0,40}fail[- ]closed" "${SCAN[@]}" --include=*.md 2>/dev/null | grep -viE "not |never be|must not|cannot" | grep -v "validate-project-state.sh")
# ---- current-state agreement ---------------------------------------------------------------------------
# current_activity and the Phase-3 stage narrative are written by different hands at different times. A
# snapshot that says PRE_LIVE while the phase entry still says IMPLEMENTATION IN PROGRESS reads as
# authoritative in both places and is wrong in one, which is the whole failure mode this file exists for.
# PY is whichever interpreter this host actually has. A check that cannot read the state must FAIL, never
# silently pass — a validator that quietly does nothing is worse than no validator, because it is trusted.
# Probe that the interpreter RUNS, not merely that a file of that name is on PATH: on Windows hosts
# `python3` is often an App-Store stub that exits silently, which would make every check below no-op.
PY3=""
for cand in python3 python; do
  if [ "$("$cand" -c 'print(42)' 2>/dev/null)" = "42" ]; then PY3="$cand"; break; fi
done
act="$($PY3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8')).get('current_activity',''))" "$REPO_ROOT/governance/project-state.json" 2>/dev/null)"
if [ -z "$PY3" ] || [ -z "$act" ]; then
  echo "    HIT [state unreadable]: current_activity could not be read; the current-state agreement checks cannot run"
  p3drift=$((p3drift+1))
fi
p3mat="$($PY3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8')).get('phases',{}).get('3',{}).get('maturity',''))" "$REPO_ROOT/governance/project-state.json" 2>/dev/null)"
p3stage="$($PY3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8')).get('phase3_execution',{}).get('stage',''))" "$REPO_ROOT/governance/project-state.json" 2>/dev/null)"
case "$act" in
  *PRE_LIVE_SAFETY*)
    case "$p3mat" in
      *"PRE-LIVE SAFETY"*) : ;;
      *) echo "    HIT [stage disagreement]: current_activity is PRE_LIVE_SAFETY but the Phase-3 stage reads: $(printf '%.90s' "$p3mat")"; p3drift=$((p3drift+1));;
    esac
    case "$p3mat" in
      *"IMPLEMENTATION IN PROGRESS"*)
        echo "    HIT [stage disagreement]: the Phase-3 stage still claims IMPLEMENTATION IN PROGRESS"; p3drift=$((p3drift+1));;
    esac
    # phase3_execution.stage is a SECOND current-state field, read by different documents, and it is where
    # the previous overclaim survived: current_activity said PRE-LIVE while this still opened with the
    # superseded SOFTWARE-candidate token. Both must agree, and the check must look at both.
    case "$p3stage" in
      PHASE_3_PRE_LIVE_SAFETY_CANDIDATE*) : ;;
      *) echo "    HIT [stage disagreement]: phase3_execution.stage opens with: $(printf '%.70s' "$p3stage")"; p3drift=$((p3drift+1));;
    esac
    # The CURRENT portion of that narrative must not quote the superseded Portal budget. Everything after the
    # explicit historical marker is history and is allowed to.
    p3head="$(printf '%s' "$p3stage" | head -c 1200)"
    case "$p3head" in
      *"1200ms"*)
        case "$p3head" in
          *HISTORICAL*|*superseded*|*SUPERSEDED*) : ;;
          *) echo "    HIT [stale Portal budget in the current stage narrative]: phase3_execution.stage quotes 1200ms as current"; p3drift=$((p3drift+1));;
        esac
        ;;
    esac
    if [ -f "$REPORT" ] && grep -q "Status: DARK ACCEPTANCE CANDIDATE" "$REPORT"; then
      echo "    HIT [superseded candidate state]: the Final Report still declares DARK ACCEPTANCE CANDIDATE"; p3drift=$((p3drift+1))
    fi
    if grep -q "PHASE-3 SOFTWARE CANDIDATE COMPLETE" "$DOCS/architecture/StayConnect-IAM-Phase3-Plan.md" 2>/dev/null; then
      echo "    HIT [superseded candidate state]: the Plan still declares the SOFTWARE CANDIDATE state"; p3drift=$((p3drift+1))
    fi
    ;;
esac

# ---- the Portal budget quoted as CURRENT must be the one in the software --------------------------------
# 1200ms was the budget before enforcement convergence was part of a guest-visible success. Quoting it as
# current describes software that no longer exists; quoting it as history is fine and is excused.
while IFS= read -r line; do
  [ -z "$line" ] && continue
  echo "    HIT [stale Portal budget quoted as current]: $line"; p3drift=$((p3drift+1))
done < <(grep -rniE "(uniform|response-time|failure) budget[^.]{0,60}1200 ?ms" "${SCAN[@]}" --include=*.md 2>/dev/null \
          | grep -viE "historical|superseded|no longer|used to be|previous|old |not " | grep -v "validate-project-state.sh")

# ---- delivery evidence must not be baked into a committed document -------------------------------------
# The Final Report deliberately carries NO numeric CI run id and NO artifact id: those describe the run that
# the delivery commit itself triggers, so a committed document cannot cite them without being a round behind.
# The report says exactly that and points the reader at the PR body.
#
# Enforcing the protocol is therefore the check that prevents stale delivery metadata, and unlike comparing
# against a recorded value it has no self-reference problem: any numeric run/artifact id present in the report
# is, by construction, from a previous round.
if [ -f "$REPORT" ]; then
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    echo "    HIT [stale delivery evidence baked into the report]: $line"; p3drift=$((p3drift+1))
  done < <(grep -niE "(run|artifact)[^0-9]{0,25}[0-9]{9,}" "$REPORT" 2>/dev/null \
            | grep -viE "HISTORICAL|superseded|must not|deliberately|are in the PR|not recorded")
  # and it must keep telling the reader where those numbers actually live
  grep -q "PR #6 body" "$REPORT" || {
    echo "    HIT [delivery-evidence pointer lost]: the report no longer says where the run ids and artifact metadata live"
    p3drift=$((p3drift+1))
  }
fi

[ "$p3drift" = "0" ] && ok "no Phase-3 enforcement/evidence phrase drift" || fail "$p3drift Phase-3 phrase drift hit(s)"

echo "== 1d. transition receipts cannot be dated after their introducing commit, nor before the merge they record =="
if have_repo; then
  if bash "$REPO_ROOT/tools/validate-transition-times.sh" >/dev/null 2>&1; then
    ok "no receipt is dated after its introducing commit, and no merge receipt pre-dates its own merge"
  else
    bash "$REPO_ROOT/tools/validate-transition-times.sh" 2>&1 | grep -E "^  (FAIL|grandfathered)" | sed 's/^/  /'
    fail "a transition receipt describes its own future, or a merge receipt pre-dates the merge it records"
  fi
  # The rule above is grandfathered for T0054, so watching it pass proves nothing on its own. This drives it
  # against the historical defect with the grandfather list emptied.
  if bash "$REPO_ROOT/tools/validate-merge-receipt-times-selftest.sh" >/dev/null 2>&1; then
    ok "the merge-timing rule provably catches the T0054 defect and still accepts a valid merge receipt"
  else
    bash "$REPO_ROOT/tools/validate-merge-receipt-times-selftest.sh" 2>&1 | grep -E "^  \[FAIL\]" | sed 's/^/  /'
    fail "the merge-timing rule cannot be shown to catch the defect it exists for"
  fi
else skipped "transition timestamps (no repository)"; fi

echo "== 2. single current maturity + consistent next action =="
for f in "$PACK/StayConnect-IAM-Phase1A-Plan.md" "$PACK/StayConnect-IAM-Handoff.md" "$PACK/00-START-HERE.md" "$PACK/MANIFEST.md"; do
  [ -f "$f" ] && grep -q "$MAT" "$f" && ok "maturity present in $(basename "$f")" || fail "maturity string missing in $(basename "$f")"
done
if have_repo; then
  for f in "$DOCS/architecture/StayConnect-IAM-Phase1A-Plan.md" "$DOCS/context/StayConnect-IAM-Handoff.md"; do
    [ -f "$f" ] && grep -q "$MAT" "$f" && ok "maturity present in repo $(basename "$f")" || fail "maturity missing in repo $(basename "$f")"
  done
else skipped "repo docs maturity presence"; fi
na=$(grep -rhoiE "next authorized (activity|action|step)[^.]*" "$PACK/StayConnect-IAM-Handoff.md" "$PACK/00-START-HERE.md" 2>/dev/null | grep -ciE "acceptance of Phase 1A|acceptance of the live-dark|review of the live-dark|review of the Phase 1A LIVE-DARK|approval or rejection of the .{0,30}Phase 1B plan|approval of the .{0,30}Phase 1B (implementation )?plan|approval[^.]{0,90}Phase 1B plan|complete Phase 1B execution|(review/?)?acceptance of[^.]{0,40}Phase 1B live-dark|no next-phase implementation is authorized|await explicit product-owner authorization|complete phase 2 execution|return the single final phase-2 report|final phase-2 report for one product-owner|(review/?)?acceptance of[^.]{0,40}Phase 2 live-dark|merge PR #4[^.]{0,80}post-merge|merge PR #4 to master|execute the authorized phase 3|final phase-3 acceptance report|authoriz[a-z]*[^.]{0,40}Live Increment 9|separate Product-Owner decision[^.]{0,80}Live Increment 9|await[^.]{0,60}Live Increment 9|Product-Owner decision on the Increment-9 durability correction|Product-Owner FINAL ACCEPTANCE decision for Phase 3|decision on merging PR #6|re-run[a-z]*[^.]{0,60}BLOCKED subset of Live Increment 9|None for Phase 3[^.]{0,80}(closed and merged|merged to master)|no further Phase-3 action is authorized|Execute the authorized Phase-4 implementation|Continue the authorized Phase-4 implementation|Obtain the Product Owner.{0,3}s acceptance decision on the WS-L|acceptance decision on the WS-L controlled live-DARK result|Product-Owner decision on merging the Phase-4 pull request|decision on merging the Phase-4 pull request|No further Phase-4 action is authorized|no further Phase-4 action is authorized|Execute Phase 5|Execute the authorized Phase-5|Continue the authorized Phase-5|acceptance decision on the Phase-5|Obtain the Product Owner's acceptance decision on the Phase-5|Maintain project governance and documentation for the closed phases|No further Phase-5 action is authorized|Execute the authorized Phase-6 implementation|Continue the authorized Phase-6|Continue Phase-6 milestone M[0-9]|acceptance decision on the Phase-6|Open the single Phase-6 pull request|No further Phase-6 action is authorized|Execute the authorized Phase-7|Continue the authorized Phase-7|acceptance decision on the Phase-7|No further Phase-7 action is authorized")
[ "$na" -ge 2 ] && ok "next-action consistent" || fail "next-action inconsistent ($na)"

echo "== 3. conflicting maturity WITHIN a single pack file =="
c3=0
for f in "$PACK"/*.md; do
  [ -f "$f" ] || continue
  hl=$(grep -ciE "live-dark|LIVE-DARK|$MAT" "$f")
  si=$(grep -inE 'Phase.?1A[^.]{0,40}planning only|Phase.?1A[^.]{0,40}in scratch only|not created on live|implementation requires separate approval of the Phase 1A plan|Forbidden Until the Phase 1A Plan Is Approved' "$f" 2>/dev/null | grep -viE "$HIST" | grep -viE "nothing in this section is authorized to execute" | wc -l)
  [ "$hl" -gt 0 ] && [ "$si" -gt 0 ] && { echo "    conflict in $(basename "$f"): live-dark + $si unlabeled stale line(s)"; c3=$((c3+1)); }
done
[ "$c3" = "0" ] && ok "no within-file maturity conflicts" || fail "$c3 file(s) with conflicting maturity"

echo "== 4. acceptance record present + references V2 evidence + V1 superseded =="
AR="$PACK/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"
[ -f "$AR" ] && ok "acceptance record present (pack)" || fail "acceptance record missing from pack"
[ -f "$AR" ] && grep -q "PROD_LIVE_DARK_EVIDENCE_V2.txt" "$AR" && ok "acceptance references V2 evidence" || fail "acceptance does not reference V2"
V1="$EVID/review/prod/PROD_LIVE_DARK_EVIDENCE.txt"
[ -f "$V1" ] && grep -q "SUPERSEDED — EVIDENCE ERROR" "$V1" && ok "broken V1 evidence marked superseded (evidence pack)" || fail "V1 evidence not marked superseded"
if have_repo; then
  [ -f "$DOCS/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md" ] && ok "acceptance record present (repo source)" || fail "acceptance record missing from repo docs"
else skipped "repo acceptance source presence"; fi

echo "== 5. deterministic provenance in MANIFEST (SOURCE_COMMIT; export commit is external) =="
M="$PACK/MANIFEST.md"
src_hash=$(grep -E "SOURCE_COMMIT" "$M" 2>/dev/null | head -1 | grep -oE '`[0-9a-f]{7,40}`' | head -1 | tr -d '`')
exp_line=$(grep -E "PROJECT_PACK_EXPORT_COMMIT" "$M" 2>/dev/null | head -1)
[ -n "$src_hash" ] && ok "SOURCE_COMMIT = $src_hash" || fail "SOURCE_COMMIT (exact hex) missing from MANIFEST"
if echo "$exp_line" | grep -qiE "\*external\*|external —|external\b"; then ok "PROJECT_PACK_EXPORT_COMMIT recorded as external (no self-reference)"; else fail "PROJECT_PACK_EXPORT_COMMIT must be recorded as external (deterministic model)"; fi
if [ -n "$src_hash" ] && have_repo && command -v git >/dev/null 2>&1 && git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  git -C "$REPO_ROOT" rev-parse --verify -q "$src_hash^{commit}" >/dev/null 2>&1 && ok "SOURCE_COMMIT $src_hash exists in git" || echo "  note: SOURCE_COMMIT $src_hash not yet committed (pre-commit build)"
else skipped "git existence of SOURCE_COMMIT"; fi

echo "== 6. permanent rule bundled in the Project Pack + links resolve =="
[ -f "$PACK/$RULE" ] && ok "$RULE present in Project Pack" || fail "$RULE missing from Project Pack"
b=0
for f in "$PACK"/*.md; do
  [ -f "$f" ] || continue
  while IFS= read -r t; do
    base="${t##*/}"; base="${base%%#*}"
    [ "$base" = "$RULE" ] || continue
    if [ "$t" != "$RULE" ] && [ "$t" != "./$RULE" ]; then
      [ -f "$PACK/$t" ] || { echo "    broken/unflattened rule link in $(basename "$f"): $t"; b=$((b+1)); }
    fi
  done < <(grep -oE '\]\([^)]*'"$RULE"'[^)]*\)' "$f" | sed -E 's/^\]\(//;s/\)$//;s/#.*$//')
done
[ "$b" = "0" ] && ok "all permanent-rule links resolve inside the pack" || fail "$b unresolved permanent-rule link(s)"

echo "== 6b. permanent GitHub execution/delivery rule bundled in the Project Pack + links resolve =="
GHRULE="GITHUB_EXECUTION_AND_DELIVERY_RULE.md"
[ -f "$PACK/$GHRULE" ] && ok "$GHRULE present in Project Pack" || fail "$GHRULE missing from Project Pack"
gb=0
for f in "$PACK"/*.md; do
  [ -f "$f" ] || continue
  while IFS= read -r t; do
    base="${t##*/}"; base="${base%%#*}"
    [ "$base" = "$GHRULE" ] || continue
    if [ "$t" != "$GHRULE" ] && [ "$t" != "./$GHRULE" ]; then
      [ -f "$PACK/$t" ] || { echo "    broken/unflattened GH-rule link in $(basename "$f"): $t"; gb=$((gb+1)); }
    fi
  done < <(grep -oE '\]\([^)]*'"$GHRULE"'[^)]*\)' "$f" | sed -E 's/^\]\(//;s/\)$//;s/#.*$//')
done
[ "$gb" = "0" ] && ok "all GitHub-rule links resolve inside the pack" || fail "$gb unresolved GitHub-rule link(s)"

echo "== 7. validator physically shipped in the Evidence Pack with a checksum =="
[ -f "$EVID/tools/validate-project-state.sh" ] && ok "validator present in Evidence Pack" || fail "validator file missing from Evidence Pack"
grep -q "tools/validate-project-state.sh" "$EVID/PACK_SHA256SUMS.txt" 2>/dev/null && ok "validator checksum in PACK_SHA256SUMS" || fail "validator not checksummed in PACK_SHA256SUMS"

echo "== 8. Project Pack MANIFEST checksums match packaged files =="
if [ -f "$M" ]; then
  bad=0
  while IFS= read -r line; do
    fn=$(echo "$line" | grep -oE '`[A-Za-z0-9._-]+\.md`' | head -1 | tr -d '`')
    h=$(echo "$line" | grep -oE '[0-9a-f]{64}' | tail -1)
    [ -n "$fn" ] && [ -f "$PACK/$fn" ] || continue
    have=$(sha256sum "$PACK/$fn" | cut -d' ' -f1)
    [ "$have" = "$h" ] || { echo "    mismatch: $fn"; bad=$((bad+1)); }
  done < <(grep -E '^\| [0-9]+ \|' "$M")
  [ "$bad" = "0" ] && ok "all MANIFEST checksums match" || fail "$bad MANIFEST checksum mismatch(es)"
else fail "pack MANIFEST missing"; fi

echo "== 8b. Evidence Pack PACK_SHA256SUMS match packaged files =="
PS="$EVID/PACK_SHA256SUMS.txt"
if [ -f "$PS" ]; then
  ebad=0
  while IFS= read -r line; do
    case "$line" in \#*|"") continue;; esac
    h="${line%% *}"; rel="${line#*  }"
    [ -f "$EVID/$rel" ] || { echo "    missing: $rel"; ebad=$((ebad+1)); continue; }
    have=$(sha256sum "$EVID/$rel" | cut -d' ' -f1)
    [ "$have" = "$h" ] || { echo "    mismatch: $rel"; ebad=$((ebad+1)); }
  done < "$PS"
  [ "$ebad" = "0" ] && ok "all Evidence-Pack checksums match" || fail "$ebad Evidence-Pack checksum issue(s)"
else fail "Evidence Pack PACK_SHA256SUMS.txt missing"; fi

echo "== 9. core pack links resolve =="
brk=0
for f in "$PACK"/00-START-HERE.md "$PACK"/MANIFEST.md "$PACK"/StayConnect-IAM-*.md; do
  [ -f "$f" ] || continue
  while IFS= read -r t; do
    case "$t" in http*|"") continue;; esac
    [ -f "$PACK/$t" ] || { echo "    BROKEN $(basename "$f") -> $t"; brk=$((brk+1)); }
  done < <(grep -oE '\]\([^)]+\)' "$f" | sed -E 's/^\]\(//;s/\)$//;s/#.*$//')
done
[ "$brk" = "0" ] && ok "core pack links resolve" || fail "$brk broken core pack link(s)"

echo "== 10. no secrets / guest PII / credential DSNs in the packs =="
sec=$(grep -rnE "BEGIN (RSA|OPENSSH) PRIVATE|ssh-ed25519 AAAA|sk_live|whsec_|POSTGRES_PASSWORD=[^ ]|postgres://[a-z_]+:[A-Za-z0-9]{6,}@|14215|262224|3c2ffe67|81a3edc5" "$PACK" "$EVID" --exclude=validate-project-state.sh 2>/dev/null | grep -viE "POSTGRES_PASSWORD assignments committed|redacted|«" | wc -l)
[ "$sec" = "0" ] && ok "no secrets/PII/credential-DSNs in the packs" || { grep -rnE "sk_live|whsec_|14215|262224" "$PACK" "$EVID" --exclude=validate-project-state.sh | head; fail "$sec secret/PII hit(s) in packs"; }

# ---------------------------------------------------------------------------------------------------
# 11. CLAIM-VERSUS-CODE PARITY (repository mode only -- it measures the tree, which a pack does not carry).
#
# Everything above asks whether the current-state surfaces agree with each other. A stale fact survives that
# happily: phase4_manual_review_frontend read `false` for ten transitions while the screen it denied was
# built, tested and gated, and every check here passed the whole time because none of them looked at the
# repository. This section closes that by comparing each build claim to the file, function or route that
# would have to exist for it to be true.
# ---------------------------------------------------------------------------------------------------
echo "== 11. current-state claims match the repository (parity) =="
if [ "$MODE" = "repository" ] && [ -f "$REPO_ROOT/tools/validate-state-parity.py" ]; then
  PYP=python3; python3 --version >/dev/null 2>&1 || PYP=python
  if PARITY_OUT="$("$PYP" "$REPO_ROOT/tools/validate-state-parity.py" 2>&1)"; then
    ok "every current-state build claim is supported by the tree"
  else
    printf '%s
' "$PARITY_OUT" | grep '  FAIL:' | sed 's/^/    /'
    fail "current-state claims disagree with the repository (tools/validate-state-parity.py)"
  fi
else
  echo "  SKIPPED (parity measures the repository tree)"; SKIP=$((SKIP+1))
fi

echo "=================================================="
echo "mode: $MODE   repository-only checks skipped: $SKIP"
if [ "$FAIL" = "0" ]; then echo "ZERO_STALE_LEFTOVERS = PASS"; exit 0; else echo "ZERO_STALE_LEFTOVERS = FAIL ($FAIL)"; exit 1; fi
