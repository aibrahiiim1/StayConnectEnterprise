#!/usr/bin/env bash
# validate-project-state.sh — enforces the Zero-Stale-Leftovers rule (docs/ZERO_STALE_LEFTOVERS_RULE.md).
# Exit non-zero on any contradiction/stale/hash/PII/provenance/link failure.
# Scans BOTH the repository source (docs/) AND the generated ChatGPT Project Pack (exports/...).
# A matching line is excused ONLY if that same line carries an explicit inline historical marker
# (HISTORICAL / SUPERSEDED / "does not describe current status" / "that gate is now satisfied" /
#  "at Phase-0 close" / "originally approved for scratch"). Broad file-level exceptions are NOT allowed.
set -uo pipefail
cd "$(dirname "$0")/.."
FAIL=0; fail(){ echo "  FAIL: $*"; FAIL=$((FAIL+1)); }; ok(){ echo "  ok: $*"; }
PACK="exports/chatgpt/stayconnectenterprise"
EVID="exports/chatgpt/phase-evidence"
RULE="ZERO_STALE_LEFTOVERS_RULE.md"
SCAN_DIRS=(docs "$PACK")
# per-line inline historical marker that excuses a stale match
HIST='HISTORICAL|SUPERSEDED|does \*\*not\*\* describe current|does not describe current status|that gate is now satisfied|that gate has since been satisfied|at Phase-0 close|originally approved for scratch|was originally|\[Historical|\(Historical:'

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

echo "== 1. no stale Phase-1A current-status phrases (docs + generated pack; per-line historical excused) =="
s1=0
for entry in "${RULES[@]}"; do
  pat="${entry%%::*}"; desc="${entry##*::}"
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    echo "$line" | grep -qiE "$HIST" && continue
    echo "    HIT [$desc]: $line"; s1=$((s1+1))
  done < <(grep -rniE "$pat" "${SCAN_DIRS[@]}" --include=*.md 2>/dev/null)
done
[ "$s1" = "0" ] && ok "no stale Phase-1A current-status phrases" || fail "$s1 stale Phase-1A current-status line(s)"

echo "== 2. single current maturity + next action (consistency) =="
MAT="PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED"
for f in docs/architecture/StayConnect-IAM-Phase1A-Plan.md docs/context/StayConnect-IAM-Handoff.md "$PACK/00-START-HERE.md" "$PACK/MANIFEST.md"; do
  grep -q "$MAT" "$f" && ok "maturity present in $(basename "$f")" || fail "maturity string missing in $f"
done
na=$(grep -rhoiE "next authorized (activity|action|step)[^.]*" docs/context/StayConnect-IAM-Handoff.md "$PACK/00-START-HERE.md" | grep -ciE "acceptance of Phase 1A|acceptance of the live-dark|review of the live-dark|review of the Phase 1A LIVE-DARK")
[ "$na" -ge 2 ] && ok "next-action consistent (PO acceptance of Phase 1A)" || fail "next-action inconsistent ($na)"

echo "== 3. conflicting maturity WITHIN a single pack file =="
c3=0
for f in "$PACK"/*.md; do
  [ -f "$f" ] || continue
  has_live=$(grep -ciE "live-dark|LIVE-DARK|$MAT" "$f")
  # unlabeled stale assertion in the same file
  stale_in=$(grep -inE 'planning only|in scratch only|not created on live|implementation requires separate approval of the Phase 1A plan|Forbidden Until the Phase 1A Plan Is Approved' "$f" 2>/dev/null | grep -viE "$HIST" | grep -viE "nothing in this section is authorized to execute" | wc -l)
  if [ "$has_live" -gt 0 ] && [ "$stale_in" -gt 0 ]; then
    echo "    conflict in $(basename "$f"): live-dark maturity + $stale_in unlabeled stale line(s)"; c3=$((c3+1))
  fi
done
[ "$c3" = "0" ] && ok "no within-file maturity conflicts" || fail "$c3 file(s) with conflicting maturity"

echo "== 4. required acceptance record present + references V2 evidence =="
AR=docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md
[ -f "$AR" ] && ok "acceptance record present" || fail "acceptance record missing"
grep -q "PROD_LIVE_DARK_EVIDENCE_V2.txt" "$AR" && ok "acceptance references V2 evidence" || fail "acceptance does not reference V2"
grep -q "SUPERSEDED — EVIDENCE ERROR" iam_v2_scratch/review/prod/PROD_LIVE_DARK_EVIDENCE.txt && ok "broken V1 evidence marked superseded" || fail "V1 evidence not marked superseded"

echo "== 5. exact provenance in MANIFEST (no placeholder) =="
M="$PACK/MANIFEST.md"
sync_line=$(grep -E "SOURCE_DOCUMENTATION_SYNC_COMMIT" "$M" | head -1)
exp_line=$(grep -E "PROJECT_PACK_EXPORT_COMMIT" "$M" | head -1)
sync_hash=$(echo "$sync_line" | grep -oE '`[0-9a-f]{7,40}`' | head -1 | tr -d '`')
exp_hash=$(echo "$exp_line"  | grep -oE '`[0-9a-f]{7,40}`' | head -1 | tr -d '`')
[ -n "$sync_hash" ] && ok "SOURCE_DOCUMENTATION_SYNC_COMMIT = $sync_hash" || fail "SOURCE_DOCUMENTATION_SYNC_COMMIT missing/hex"
if echo "$exp_line" | grep -qiE "the commit that introduces|HEAD at export|verify with|created \*\*after\*\*"; then
  fail "PROJECT_PACK_EXPORT_COMMIT uses placeholder wording, not an exact hash"
elif [ -z "$exp_hash" ]; then
  fail "PROJECT_PACK_EXPORT_COMMIT missing an exact hex hash"
else
  ok "PROJECT_PACK_EXPORT_COMMIT = $exp_hash"
  git rev-parse --verify -q "$exp_hash^{commit}" >/dev/null 2>&1 && ok "export commit $exp_hash exists in git" || echo "  note: export commit $exp_hash not yet in git (pre-commit run)"
fi

echo "== 6. permanent rule bundled in the Project Pack + links resolve =="
[ -f "$PACK/$RULE" ] && ok "$RULE present in Project Pack" || fail "$RULE missing from Project Pack"
b=0
for f in "$PACK"/*.md; do
  [ -f "$f" ] || continue
  while IFS= read -r t; do
    base="${t##*/}"; base="${base%%#*}"
    [ "$base" = "$RULE" ] || continue
    if [ ! -f "$PACK/$RULE" ] || [ "$t" != "$RULE" ] && [ "$t" != "./$RULE" ]; then
      # link must be flattened to resolve inside the pack
      [ -f "$PACK/$t" ] || { echo "    broken/unflattened rule link in $(basename "$f"): $t"; b=$((b+1)); }
    fi
  done < <(grep -oE '\]\([^)]*'"$RULE"'[^)]*\)' "$f" | sed -E 's/^\]\(//;s/\)$//;s/#.*$//')
done
[ "$b" = "0" ] && ok "all permanent-rule links resolve inside the pack" || fail "$b unresolved permanent-rule link(s)"

echo "== 7. validator physically shipped in the Evidence Pack with a checksum =="
if [ -f "$EVID/tools/validate-project-state.sh" ]; then ok "validator present in Evidence Pack"; else fail "validator file missing from Evidence Pack"; fi
if grep -q "tools/validate-project-state.sh" "$EVID/PACK_SHA256SUMS.txt" 2>/dev/null; then ok "validator checksum in PACK_SHA256SUMS"; else fail "validator not checksummed in PACK_SHA256SUMS"; fi

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

echo "== 10. no secrets / guest PII / credential DSNs in exports =="
sec=$(grep -rnE "BEGIN (RSA|OPENSSH) PRIVATE|ssh-ed25519 AAAA|sk_live|whsec_|POSTGRES_PASSWORD=[^ ]|postgres://[a-z_]+:[A-Za-z0-9]{6,}@|14215|262224|3c2ffe67|81a3edc5" exports/ 2>/dev/null | grep -viE "POSTGRES_PASSWORD assignments committed|redacted|«" | wc -l)
[ "$sec" = "0" ] && ok "no secrets/PII/credential-DSNs in exports" || { grep -rnE "sk_live|whsec_|14215|262224" exports/ | head; fail "$sec secret/PII hit(s) in exports"; }

echo "======================================"
if [ "$FAIL" = "0" ]; then echo "ZERO_STALE_LEFTOVERS = PASS"; exit 0; else echo "ZERO_STALE_LEFTOVERS = FAIL ($FAIL)"; exit 1; fi
