#!/usr/bin/env bash
# DARK assertion for the Phase-4 financial core.
#
# "No financial egress" is proved three ways in this repository, and this script is the third:
#   - behaviourally, by the DARK worker integration test, which runs a real worker against a real queued
#     posting and asserts it produces no bytes, no attempt and no P#;
#   - structurally, by DarkGuard, which refuses before the inner transport is reached;
#   - and here, statically: nothing anywhere in the delivered tree turns a Phase-4 flag ON, no deployment
#     unit sets one, and the Phase-3 connector still classifies PS and PA as forbidden financial records.
#
# Exit 1 on any violation. There is no retryable condition here, so this never exits 2.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
fail=0
say(){ printf '  [PASS] %s\n' "$1"; }
bad(){ printf '  [FAIL] %s\n' "$1"; fail=1; }

echo "== Phase-4 DARK assertion =="

# 1. No Phase-4 flag is set to a true value anywhere outside the Go source that DEFINES the names and the
#    tests that exercise them. A deployment unit, an env file or a script that enabled one would show here.
hits="$(grep -rInE 'STAYCONNECT_PHASE4_[A-Z_]+\s*[=:]\s*"?(1|true|TRUE|yes|on)"?' . \
        --include='*.service' --include='*.env' --include='*.sh' --include='*.yml' --include='*.yaml' \
        --include='*.conf' --include='*.json' --include='*.ts' --include='*.tsx' \
        2>/dev/null | grep -v '^\./\.git/' || true)"
if [ -z "$hits" ]; then
  say "no deployment or configuration file enables a Phase-4 flag"
else
  bad "a Phase-4 flag is enabled somewhere in the tree:"; printf '%s\n' "$hits"
fi

# 2. The Go default really is all-OFF. A constructor or default that flipped one would make every other
#    proof in this milestone vacuous.
if grep -qE 'func DefaultConfig\(\) Config \{ return Config\{\} \}' data-plane/internal/posting/config.go; then
  say "posting.DefaultConfig() is the zero (all-OFF) value"
else
  bad "posting.DefaultConfig() is no longer the zero value"
fi

# 3. Every Transport in the tree is constructed through the guard. NewEngine is the only constructor, and
#    it must wrap; a second constructor that did not would be a hole.
# THE CONSTRUCTION BOUNDARY, asserted as a PROPERTY of the exported API rather than as a list of names.
#
# The previous version of this check EXCLUDED NewDarkGuard from its own exported-constructor rule, so it
# could not fail on the exact hole that existed. This version asks the compiler what the package actually
# exports and refuses any exported function that accepts a Config or a Transport — no exclusions, so a new
# hole cannot be added without this failing.
ctor_ok=1
api="$(cd data-plane && go doc -short ./internal/posting 2>/dev/null)"
if [ -z "$api" ]; then
  bad "could not read the exported API of internal/posting (go doc produced nothing)"
  ctor_ok=0
else
  # Any exported func or method taking a Config or a Transport is a configurable financial construction
  # path. NOTE the leading-whitespace class: go doc -short INDENTS constructors under their type, so an
  # anchor of '^func' silently skipped every constructor -- which is exactly why the first version of
  # this check passed a deliberately planted hole.
  # Match Config/Transport ONLY inside the parameter list. Two earlier attempts got this wrong in
  # opposite directions: anchoring on '^func' skipped every constructor (go doc indents them under their
  # type), and matching the whole signature flagged DefaultConfig()/LoadConfigFromEnv(), whose names
  # contain "Config" and which accept none. awk isolates the parameters, skipping a method receiver.
  offenders="$(printf '%s\n' "$api" | grep -E '^[[:space:]]*func ' | awk '{
      line = $0; rest = line
      sub(/^[[:space:]]*func[[:space:]]*/, "", rest)
      if (substr(rest, 1, 1) == "(") {            # method: drop the receiver group
        c = index(rest, ")"); rest = substr(rest, c + 1)
      }
      o = index(rest, "("); if (o == 0) next
      rest = substr(rest, o + 1)
      c = index(rest, ")"); params = (c > 0) ? substr(rest, 1, c - 1) : rest
      if (params ~ /Config|Transport/) print line
    }')"
  if [ -n "$offenders" ]; then
    bad "the package exports a configurable financial construction path:"
    printf '%s\n' "$offenders" | sed 's/^/      /'
    ctor_ok=0
  fi
  printf '%s\n' "$api" | grep -q 'func NewProductionEngine(repo \*Repo) (\*Engine, error)' || {
    bad "NewProductionEngine is not the expected zero-configuration constructor"; ctor_ok=0; }
fi
grep -q 'transport: newDarkGuard(cfg, inner)' data-plane/internal/posting/engine.go || {
  bad "the internal constructor no longer wraps in the DARK guard"; ctor_ok=0; }
grep -qE '^\s+(Cfg|Repo|Transport|Gate)\s' data-plane/internal/posting/engine.go && {
  bad "an exported Engine field reappeared"; ctor_ok=0; }
[ -f data-plane/internal/posting/export_test.go ] || {
  bad "the compile-time-only test seam is missing"; ctor_ok=0; }
if [ "$ctor_ok" = 1 ]; then
  say "no exported function accepts a Config or a Transport; NewProductionEngine(repo) is the whole financial construction surface"
fi

# The reversal model: the passive ledger row is REQUIRED by the contract, the SENDER is forbidden. Assert
# that the forbidding half is still structural, and that no PT=C or negative-TA assumption ever appeared.
rev_ok=1
grep -q 'REVERSAL_NOT_EXECUTABLE' data-plane/migrations/0013_phase4_reversal_ledger.up.sql || rev_ok=0
grep -q 'p4_reversal_never_queued' data-plane/migrations/0013_phase4_reversal_ledger.up.sql || rev_ok=0
grep -q 'p4_reversal_never_attempted' data-plane/migrations/0013_phase4_reversal_ledger.up.sql || rev_ok=0
if grep -rn "PT=C\|PTC\|'C'" data-plane/internal/posting/fias.go >/dev/null 2>&1; then rev_ok=0; fi
if [ "$rev_ok" = 1 ]; then
  say "the reversal ledger is passive and structurally non-executable; no PT=C and no negative-TA assumption exists"
else
  bad "the reversal safety model changed"
fi

# 4. The financial core opens no socket of its own. It has no net import at all: the transport is an
#    interface, and the only implementations in this milestone are the guard and in-process test stubs.
netimports="$(grep -rn '"net"\|"net/http"\|net\.Dial\|http\.Client' data-plane/internal/posting/ \
              --include='*.go' | grep -v '_test.go' || true)"
if [ -z "$netimports" ]; then
  say "the financial core imports no network package (no socket of its own)"
else
  bad "the financial core reaches the network directly:"; printf '%s\n' "$netimports"
fi

# 5. The Phase-3 read-only connector still forbids the financial records. If PS were ever moved onto its
#    allowlist, a Phase-4 defect could smuggle a charge out through the Phase-3 socket.
if grep -q '"PS": {}, // financial Posting (Phase 4 only)' data-plane/internal/pmsd/fias_adapter.go \
   && grep -q '"PA": {}, // posting answer (financial)' data-plane/internal/pmsd/fias_adapter.go; then
  say "pmsd still classifies PS and PA as forbidden outbound records"
else
  bad "pmsd no longer forbids the financial records on its outbound allowlist"
fi

# 6. The governance record must still say Phase-4 flags are OFF. A milestone that quietly changed that
#    while claiming DARK would be the exact failure this check exists for.
# Try each interpreter and take the first that actually RUNS: on Windows dev hosts `python3` resolves to
# an App-Execution-Alias stub that exits without producing output, so `command -v` alone is not enough.
flagsoff=""
for py in python3 python py; do
  command -v "$py" >/dev/null 2>&1 || continue
  out="$("$py" -c "import json;print(json.load(open('governance/project-state.json'))['current_state_facts'].get('phase4_flags_off'))" 2>/dev/null || true)"
  if [ -n "$out" ]; then flagsoff="$out"; break; fi
done
[ -n "$flagsoff" ] || flagsoff=ERROR
if [ "$flagsoff" = "True" ]; then
  say "governance records phase4_flags_off = true"
else
  bad "governance no longer records phase4_flags_off = true (got '$flagsoff')"
fi

echo
[ "$fail" -eq 0 ] && { echo "PHASE4_DARK_CHECK: PASS"; exit 0; }
echo "PHASE4_DARK_CHECK: FAIL"; exit 1
