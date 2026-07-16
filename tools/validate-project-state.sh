#!/usr/bin/env bash
# validate-project-state.sh — enforces the Zero-Stale-Leftovers rule (docs/ZERO_STALE_LEFTOVERS_RULE.md).
# Exit non-zero on any contradiction/stale/hash/PII failure. Run before every doc/impl/export commit.
set -uo pipefail
cd "$(dirname "$0")/.."
FAIL=0; fail(){ echo "  FAIL: $*"; FAIL=$((FAIL+1)); }; ok(){ echo "  ok: $*"; }
PACK="exports/chatgpt/stayconnectenterprise"

echo "== 1. no stale current-status phrases in docs (outside HISTORICAL/SUPERSEDED markers) =="
STALE='Phase 1A is NOT started|Phase 1A is \*\*not\*\* implemented|implementation is still NOT approved|plan awaiting Product-Owner implementation approval|Implementation begins only after the product owner|CONDITIONALLY FROZEN|STANDBY DB|BLUE/GREEN SWAP|READY_FOR_PRODUCT_OWNER_IMPLEMENTATION_APPROVAL'
hits=$(grep -rniE "$STALE" docs/ --include=*.md 2>/dev/null | grep -viE "HISTORICAL|Historical|SUPERSEDED|previously|prior|originally|was CONDITIONALLY" | wc -l)
[ "$hits" = "0" ] && ok "no stale current-status phrases" || { grep -rniE "$STALE" docs/ --include=*.md | grep -viE "HISTORICAL|Historical|SUPERSEDED|previously|prior|originally|was CONDITIONALLY" | head; fail "$hits stale current-status phrase(s)"; }

echo "== 2. single current maturity + next action (consistency) =="
MAT="PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED"
for f in docs/architecture/StayConnect-IAM-Phase1A-Plan.md docs/context/StayConnect-IAM-Handoff.md "$PACK/00-START-HERE.md" "$PACK/MANIFEST.md"; do
  grep -q "$MAT" "$f" && ok "maturity present in $(basename "$f")" || fail "maturity string missing in $f"
done
na=$(grep -rhoiE "next authorized (activity|action|step)[^.]*" docs/context/StayConnect-IAM-Handoff.md "$PACK/00-START-HERE.md" | grep -ciE "acceptance of Phase 1A|acceptance of the live-dark|review of the live-dark|review of the Phase 1A LIVE-DARK")
[ "$na" -ge 2 ] && ok "next-action consistent (PO acceptance of Phase 1A)" || fail "next-action inconsistent ($na)"

echo "== 3. required acceptance record present + references V2 evidence =="
AR=docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md
[ -f "$AR" ] && ok "acceptance record present" || fail "acceptance record missing"
grep -q "PROD_LIVE_DARK_EVIDENCE_V2.txt" "$AR" && ok "acceptance references V2 evidence" || fail "acceptance does not reference V2"
grep -q "SUPERSEDED — EVIDENCE ERROR" iam_v2_scratch/review/prod/PROD_LIVE_DARK_EVIDENCE.txt && ok "broken V1 evidence marked superseded" || fail "V1 evidence not marked superseded"

echo "== 4. Project Pack MANIFEST checksums match packaged files =="
if [ -f "$PACK/MANIFEST.md" ]; then
  bad=0
  while IFS= read -r line; do
    fn=$(echo "$line" | grep -oE '`[A-Za-z0-9._-]+\.md`' | head -1 | tr -d '`')
    h=$(echo "$line" | grep -oE '[0-9a-f]{64}' | tail -1)
    [ -n "$fn" ] && [ -f "$PACK/$fn" ] || continue
    have=$(sha256sum "$PACK/$fn" | cut -d' ' -f1)
    [ "$have" = "$h" ] || { echo "    mismatch: $fn"; bad=$((bad+1)); }
  done < <(grep -E '^\| [0-9]+ \|' "$PACK/MANIFEST.md")
  [ "$bad" = "0" ] && ok "all MANIFEST checksums match" || fail "$bad MANIFEST checksum mismatch(es)"
else fail "pack MANIFEST missing"; fi

echo "== 5. core pack links resolve =="
b=0
for f in "$PACK"/00-START-HERE.md "$PACK"/MANIFEST.md "$PACK"/StayConnect-IAM-*.md; do
  [ -f "$f" ] || continue
  grep -oE '\]\([^)]+\)' "$f" | sed -E 's/^\]\(//;s/\)$//;s/#.*$//' | while read -r t; do
    case "$t" in http*|"") continue;; esac
    [ -f "$PACK/$t" ] || echo "BROKEN $f -> $t"
  done
done | grep -q BROKEN && { fail "broken core pack link(s)"; } || ok "core pack links resolve"

echo "== 6. no secrets / guest PII / credential DSNs in exports =="
sec=$(grep -rnE "BEGIN (RSA|OPENSSH) PRIVATE|ssh-ed25519 AAAA|sk_live|whsec_|POSTGRES_PASSWORD=[^ ]|postgres://[a-z_]+:[A-Za-z0-9]{6,}@|14215|262224|3c2ffe67|81a3edc5" exports/ 2>/dev/null | grep -viE "POSTGRES_PASSWORD assignments committed|redacted|«" | wc -l)
[ "$sec" = "0" ] && ok "no secrets/PII/credential-DSNs in exports" || { grep -rnE "sk_live|whsec_|14215|262224" exports/ | head; fail "$sec secret/PII hit(s) in exports"; }

echo "======================================"
if [ "$FAIL" = "0" ]; then echo "ZERO_STALE_LEFTOVERS = PASS"; exit 0; else echo "ZERO_STALE_LEFTOVERS = FAIL ($FAIL)"; exit 1; fi
