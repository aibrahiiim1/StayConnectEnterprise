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
  'not created on live|not yet created (on|in) (live|production)|live-dark .{0,20}(is|remains) (future|to be created|not yet)|separate approval to create dark .?iam_v2.? in live::live-dark creation described as still-future'
  'READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL::stale approval-status label'
)
ARCH='blue/green|standby site db|whole-database swap|whole-db swap|swap-back'  # superseded standby/whole-DB terms

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

echo "== 2. single current maturity + consistent next action =="
for f in "$PACK/StayConnect-IAM-Phase1A-Plan.md" "$PACK/StayConnect-IAM-Handoff.md" "$PACK/00-START-HERE.md" "$PACK/MANIFEST.md"; do
  [ -f "$f" ] && grep -q "$MAT" "$f" && ok "maturity present in $(basename "$f")" || fail "maturity string missing in $(basename "$f")"
done
if have_repo; then
  for f in "$DOCS/architecture/StayConnect-IAM-Phase1A-Plan.md" "$DOCS/context/StayConnect-IAM-Handoff.md"; do
    [ -f "$f" ] && grep -q "$MAT" "$f" && ok "maturity present in repo $(basename "$f")" || fail "maturity missing in repo $(basename "$f")"
  done
else skipped "repo docs maturity presence"; fi
na=$(grep -rhoiE "next authorized (activity|action|step)[^.]*" "$PACK/StayConnect-IAM-Handoff.md" "$PACK/00-START-HERE.md" 2>/dev/null | grep -ciE "acceptance of Phase 1A|acceptance of the live-dark|review of the live-dark|review of the Phase 1A LIVE-DARK|approval or rejection of the .{0,30}Phase 1B plan|approval of the .{0,30}Phase 1B (implementation )?plan")
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

echo "== 5. exact provenance in MANIFEST (no placeholder) =="
M="$PACK/MANIFEST.md"
sync_hash=$(grep -E "SOURCE_DOCUMENTATION_SYNC_COMMIT" "$M" 2>/dev/null | head -1 | grep -oE '`[0-9a-f]{7,40}`' | head -1 | tr -d '`')
exp_line=$(grep -E "PROJECT_PACK_EXPORT_COMMIT" "$M" 2>/dev/null | head -1)
exp_hash=$(echo "$exp_line" | grep -oE '`[0-9a-f]{7,40}`' | head -1 | tr -d '`')
[ -n "$sync_hash" ] && ok "SOURCE_DOCUMENTATION_SYNC_COMMIT = $sync_hash" || fail "SOURCE_DOCUMENTATION_SYNC_COMMIT missing/hex"
if echo "$exp_line" | grep -qiE "the commit that introduces|HEAD at export|verify with|created \*\*after\*\*"; then
  fail "PROJECT_PACK_EXPORT_COMMIT uses placeholder wording, not an exact hash"
elif [ -z "$exp_hash" ]; then fail "PROJECT_PACK_EXPORT_COMMIT missing an exact hex hash"
else
  ok "PROJECT_PACK_EXPORT_COMMIT = $exp_hash"
  if have_repo && command -v git >/dev/null 2>&1 && git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
    git -C "$REPO_ROOT" rev-parse --verify -q "$exp_hash^{commit}" >/dev/null 2>&1 && ok "export commit $exp_hash exists in git" || fail "export commit $exp_hash not found in git"
  else skipped "git existence of export commit $exp_hash"; fi
fi

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

echo "=================================================="
echo "mode: $MODE   repository-only checks skipped: $SKIP"
if [ "$FAIL" = "0" ]; then echo "ZERO_STALE_LEFTOVERS = PASS"; exit 0; else echo "ZERO_STALE_LEFTOVERS = FAIL ($FAIL)"; exit 1; fi
