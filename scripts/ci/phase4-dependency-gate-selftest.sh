#!/usr/bin/env bash
# DOES THE DEPENDENCY GATE ACTUALLY FAIL?
#
# The gate currently reports PASS because the production tree has no advisories. A gate that has only ever
# been observed passing on a clean tree has not been shown to do anything at all -- and the version this
# replaced passed on a tree with high-severity production advisories in it, which is exactly the failure a
# green run hides.
#
# So this runs the REAL judgement (scripts/ci/dependency-judgement.py -- the same file CI runs, not a copy)
# against synthetic audit documents, and asserts the verdict each one deserves.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
PY=python3
python3 --version >/dev/null 2>&1 || PY=python
"$PY" --version >/dev/null 2>&1 || { echo "INFRA: no usable python"; exit 2; }

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

echo '{}' > "$W/empty.json"
printf 'next\npostcss\nsharp\n' > "$W/triage.md"

# A production advisory, exactly as npm audit reports one.
cat > "$W/dirty.json" <<'JSON'
{"vulnerabilities":{"next":{"name":"next","severity":"high","via":[
  {"title":"Next.js has a Denial of Service with Server Components",
   "url":"https://github.com/advisories/GHSA-q4gf-8mx6-v5v3","severity":"high"}]}}}
JSON
cat > "$W/clean.json" <<'JSON'
{"vulnerabilities":{}}
JSON

judge(){ # judge <prod> <acceptances> -> exit code, output in $OUTTXT
  OUTTXT="$("$PY" "$ROOT/scripts/ci/dependency-judgement.py" "$1" "$W/clean.json" "$2" "$W/triage.md" 2>&1)"
  return $?
}

# 1. the finding that started this: a triaged-but-unaccepted production advisory must NOT pass
echo '{"acceptances":[]}' > "$W/none.json"
if judge "$W/dirty.json" "$W/none.json"; then
  no "a triaged, unaccepted production advisory is refused" "the gate returned PASS"
else
  case "$OUTTXT" in
    *"triaged, NOT accepted"*) ok "a triaged, unaccepted production advisory is refused, and named as triaged" ;;
    *) no "a triaged, unaccepted production advisory is refused" "wrong wording: $OUTTXT" ;;
  esac
fi

# 2. a self-written acceptance missing the decision is not an acceptance
cat > "$W/nameless.json" <<'JSON'
{"acceptances":[{"package":"next","advisories":["GHSA-q4gf-8mx6-v5v3"],"severity":"high",
  "rationale":"it seemed fine"}]}
JSON
if judge "$W/dirty.json" "$W/nameless.json"; then
  no "an acceptance with no decider is refused" "the gate returned PASS"
else ok "an acceptance with no decider, reference or expiry is refused"; fi

# 3. an expired acceptance is no acceptance
cat > "$W/expired.json" <<'JSON'
{"acceptances":[{"package":"next","advisories":["GHSA-q4gf-8mx6-v5v3"],"severity":"high",
  "rationale":"assessed","decided_by":"Product Owner","decision_ref":"T0029",
  "decided_on":"2026-01-01","expires_on":"2026-02-01"}]}
JSON
if judge "$W/dirty.json" "$W/expired.json"; then
  no "an expired acceptance is refused" "the gate returned PASS"
else ok "an expired acceptance is refused"; fi

# 4. an acceptance for a DIFFERENT advisory in the same package does not cover this one
cat > "$W/wrong.json" <<'JSON'
{"acceptances":[{"package":"next","advisories":["GHSA-0000-0000-0000"],"severity":"high",
  "rationale":"assessed","decided_by":"Product Owner","decision_ref":"T0029",
  "decided_on":"2026-08-01","expires_on":"2099-01-01"}]}
JSON
if judge "$W/dirty.json" "$W/wrong.json"; then
  no "a package-level acceptance does not absorb a new advisory" "the gate returned PASS"
else ok "an acceptance of one advisory does not absorb a different one in the same package"; fi

# 5. a complete, current Product-Owner acceptance DOES pass -- otherwise the gate is just "always fail"
cat > "$W/good.json" <<'JSON'
{"acceptances":[{"package":"next","advisories":["GHSA-q4gf-8mx6-v5v3"],"severity":"high",
  "rationale":"assessed and accepted for the pilot window","decided_by":"Product Owner",
  "decision_ref":"T0029","decided_on":"2026-08-01","expires_on":"2099-01-01"}]}
JSON
if judge "$W/dirty.json" "$W/good.json"; then ok "a complete, current Product-Owner acceptance passes"
else no "a complete, current Product-Owner acceptance passes" "$OUTTXT"; fi

# 6. an acceptance for a risk that is no longer present is reported rather than left to rot
if judge "$W/clean.json" "$W/good.json"; then
  no "a stale acceptance is refused" "the gate returned PASS"
else
  case "$OUTTXT" in
    *stale*) ok "an acceptance for a risk that is no longer reported is refused as stale" ;;
    *) no "a stale acceptance is refused" "wrong wording: $OUTTXT" ;;
  esac
fi

# 7. and the clean case, which is what the repository is in today
if judge "$W/clean.json" "$W/none.json"; then ok "a clean production tree with no acceptances passes"
else no "a clean production tree with no acceptances passes" "$OUTTXT"; fi

echo "===== DEPENDENCY GATE SELF-TEST: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
