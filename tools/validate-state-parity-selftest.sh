#!/usr/bin/env bash
# DOES THE PARITY VALIDATOR ACTUALLY CATCH THE STALENESS IT WAS WRITTEN FOR?
#
# The parity validator passes on the corrected state file, which is also the state a validator that does
# nothing would be in. So this drives the REAL validator -- tools/validate-state-parity.py, the file CI runs,
# not a copy -- against synthetic state documents carrying the EXACT stale values this milestone found, and
# asserts it refuses each one.
#
# The fixtures are the genuine historical values, reproduced verbatim from what project-state.json contained
# at T0041. A self-test written against invented values would prove the validator catches invented values.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
PY=python3
python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "INFRA: no usable python"; exit 2; }

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# mutate <label> <python-expression-on-facts> <expected-substring-of-the-refusal>
mutate(){
  local label="$1" mutation="$2" want="$3" out rc
  "$PY" - "$ROOT/governance/project-state.json" "$W/case.json" <<PYEOF
import json, sys, collections
src, dst = sys.argv[1], sys.argv[2]
d = json.load(open(src, encoding="utf-8"), object_pairs_hook=collections.OrderedDict)
f = d["current_state_facts"]
$mutation
json.dump(d, open(dst, "w", encoding="utf-8"), indent=2, ensure_ascii=False)
PYEOF
  out="$("$PY" "$ROOT/tools/validate-state-parity.py" "$W/case.json" 2>&1)"; rc=$?
  if [ "$rc" = 0 ]; then
    no "$label" "the validator returned PASS"
  elif printf '%s' "$out" | grep -qF -- "$want"; then
    ok "$label"
  else
    no "$label" "refused, but not for the stated reason: $(printf '%s' "$out" | grep FAIL | head -1)"
  fi
}

echo "== the exact stale values found at T0041 =="

mutate "a built frontend claimed NOT built is refused" \
  'f["phase4_manual_review_frontend"] = False' \
  "the claim is stale"

mutate "a built operator surface claimed NOT built is refused" \
  'f["phase4_operator_surface"] = False' \
  "the claim is stale"

mutate "built runtime integration claimed NOT built is refused" \
  'f["phase4_runtime_integration"] = False' \
  "the claim is stale"

mutate "a wired entitlement grant claimed unwired is refused" \
  'f["phase4_entitlement_grant_wired"] = False' \
  "the claim is stale"

mutate "a delivered FINANCIAL_RECOVERY_MODE claimed absent is refused" \
  'f["phase4_financial_recovery_mode"] = False' \
  "the claim is stale"

mutate "a status string still reading FRONTEND_NOT_BUILT is refused" \
  'f["phase4_manual_review_operator_workflow"] = "BACKEND_AND_API_COMPLETE_FRONTEND_NOT_BUILT"' \
  "the Manual Review screen exists"

mutate "a status string still reading GO_DOMAIN_NOT_BUILT is refused" \
  'f["phase4_payments"] = "DB_ENFORCEMENT_COMPLETE_AND_CONCURRENCY_PROVEN_GO_DOMAIN_NOT_BUILT"' \
  "the Go payment domain exists"

mutate "remaining_scope still listing delivered work is refused" \
  'f["phase4_remaining_scope"] = ["Manual Review operator workflow (RBAC + step-up)", "operator UI and observability (queue depth, oldest age, UNKNOWN count, backlog)"]' \
  "still lists work that is delivered"

mutate "a migration inventory missing a migration on disk is refused" \
  'f["phase4_migration"] = f["phase4_migration"].replace(" + 0026_phase4_c35_failclosed_and_operator_retry", "")' \
  "phase4_migration omits"

mutate "a migration inventory naming one that does not exist is refused" \
  'f["phase4_migration"] = f["phase4_migration"] + " + 0099_phase4_imaginary"' \
  "which is not in data-plane/migrations/"

mutate "a build claim that has been DELETED is refused" \
  'del f["phase4_operator_surface"]' \
  "is absent"

# ...and the honest opposite: an UNSUPPORTED claim must fail too, or the validator only enforces one
# direction and a future surface could be declared built before it exists. Proved by pointing the claim at a
# component that is genuinely absent from the tree.
echo "== the other direction =="
"$PY" - "$ROOT/tools/validate-state-parity.py" "$W/probe.py" <<'PYEOF'
import io, sys
src, dst = sys.argv[1], sys.argv[2]
s = io.open(src, encoding="utf-8").read()
s = s.replace('"hotel-admin/components/phase4/manual-review-view.tsx"),\n     "hotel-admin',
              '"hotel-admin/components/phase4/does-not-exist.tsx"),\n     "hotel-admin', 1)
io.open(dst, "w", encoding="utf-8").write(s)
PYEOF
out="$("$PY" "$W/probe.py" "$ROOT/governance/project-state.json" 2>&1)"
if [ $? = 0 ]; then
  no "a claim with no evidence is refused" "the validator returned PASS"
elif printf '%s' "$out" | grep -qF "the claim is unsupported"; then
  ok "a claim of something the tree does not contain is refused as unsupported"
else
  no "a claim with no evidence is refused" "wrong reason: $(printf '%s' "$out" | grep FAIL | head -1)"
fi

# ...and the corrected state itself must pass, or the validator is simply "always fail".
echo "== the current state =="
if "$PY" "$ROOT/tools/validate-state-parity.py" >/dev/null 2>&1; then
  ok "the corrected project-state.json passes"
else
  no "the corrected project-state.json passes" "$("$PY" "$ROOT/tools/validate-state-parity.py" 2>&1 | grep FAIL | head -3)"
fi

echo "===== STATE PARITY SELF-TEST: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
