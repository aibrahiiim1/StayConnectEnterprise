#!/usr/bin/env bash
# NOTHING IN THE TREE ENABLES A PHASE-5 FLAG.
#
# The delivered posture is dark, and "dark" is only true if no committed file turns it on. This scans the
# deployment surface — unit files, env files, compose files, deploy scripts — for a Phase-5 flag set to
# anything true-ish. Test harnesses are excluded by path, deliberately and visibly: the Playwright config
# sets NEXT_PUBLIC_PHASE5_ADMIN=1 for a TEST-only server that is never the deployed bundle.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

hits=0
scan(){
  local pattern="$1" label="$2"
  local found
  found="$(grep -rniE "$pattern" \
      --include='*.env' --include='*.service' --include='*.yml' --include='*.yaml' \
      --include='*.sh' --include='*.conf' --include='*.ini' --include='Dockerfile*' \
      deploy/ scripts/ .github/ 2>/dev/null \
    | grep -viE 'phase5-dark-guard|phase5-pg-integration' || true)"
  if [ -n "$found" ]; then
    echo "FAIL: $label"
    printf '%s\n' "$found"
    hits=$((hits+1))
  else
    echo "  ok: $label"
  fi
}

echo "===== PHASE-5 DARK GUARD ====="
scan 'STAYCONNECT_PHASE5_[A-Z_]*[[:space:]]*[:=][[:space:]]*"?(1|true|yes|on)"?' \
     "no deployment file enables a Phase-5 flag"
scan 'NEXT_PUBLIC_PHASE5_ADMIN[[:space:]]*[:=][[:space:]]*"?1"?' \
     "no deployment file enables the Phase-5 admin nav"

# ...and the defaults really are off, which is a property of the code rather than of the files above.
if (cd data-plane && go test -count=1 -run 'TestPhase5DefaultIsDark|TestPhase5ChildFlagWithoutMasterIsAnError' ./internal/iamv2/ >/dev/null 2>&1); then
  echo "  ok: the flag loader defaults to dark and refuses a child without its master"
else
  echo "FAIL: the flag loader's dark default is not proven"
  hits=$((hits+1))
fi

echo "===== RESULT: $hits failure(s) ====="
[ "$hits" -eq 0 ] || exit 1
exit 0
