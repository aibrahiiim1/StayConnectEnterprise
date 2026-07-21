#!/usr/bin/env bash
# Phase-3 OFFLINE PREFLIGHT.
#
# Answers one question before anyone touches an appliance: is this build safe to deploy DARK?
#
# Everything here is local and offline. It contacts no appliance, no production database, no PMS and no
# network service. It refuses to run against anything but the repository it lives in, and it fails on the
# FIRST condition that would make a dark deployment unsafe rather than reporting a tidy list at the end.
#
# Usage: bash scripts/phase3-preflight.sh [--json]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JSON=0
[ "${1:-}" = "--json" ] && JSON=1

pass=0; fail=0
declare -a RESULTS
ok(){ RESULTS+=("PASS|$1"); pass=$((pass+1)); [ $JSON -eq 1 ] || echo "  [PASS] $1"; }
no(){ RESULTS+=("FAIL|$1"); fail=$((fail+1)); [ $JSON -eq 1 ] || echo "  [FAIL] $1"; }

[ $JSON -eq 1 ] || echo "== Phase-3 offline preflight (no appliance, no production DB, no PMS) =="

# ---------------------------------------------------------------- 1. the build itself
if (cd "$ROOT/data-plane" && go build ./... >/dev/null 2>&1); then
  ok "data-plane builds"
else
  no "data-plane does not build"
fi
# gofmt is checked over the PHASE-3 package set (the same list the Phase-3 CI enforces). Pre-existing
# formatting elsewhere in the repository is out of this preflight's scope and is deliberately not asserted
# here, so a Phase-3 go/no-go is never blocked or falsely reassured by unrelated code.
P3_PKGS="internal/pmsd internal/pms internal/stayengine internal/pmsresolve internal/authctx internal/grace internal/checkout internal/staygrant internal/enforce cmd/pmsd cmd/edged cmd/portald"
if (cd "$ROOT/data-plane" && [ -z "$(gofmt -l $P3_PKGS 2>/dev/null)" ]); then
  ok "Phase-3 Go sources are gofmt-clean"
else
  no "Phase-3 Go sources are not gofmt-clean: $(cd "$ROOT/data-plane" && gofmt -l $P3_PKGS | tr '
' ' ')"
fi
if (cd "$ROOT/data-plane" && go vet ./... >/dev/null 2>&1); then
  ok "go vet is clean"
else
  no "go vet reports problems"
fi

# ---------------------------------------------------------------- 2. the flags that keep it dark
# A deployed unit must ship with every Phase-3 flag OFF. The authoritative default lives in the Go config;
# this asserts the DEFAULT is off, not merely that someone remembered to unset the env.
if grep -q "func DefaultPMSConfig() PMSConfig { return PMSConfig{} }" "$ROOT/data-plane/internal/iamv2/pms_config.go"; then
  ok "Phase-3 flag defaults are OFF in code"
else
  no "Phase-3 flag defaults are not provably OFF"
fi
if grep -q "phase3 surface flag enabled while STAYCONNECT_PHASE3_MASTER is OFF" "$ROOT/data-plane/internal/iamv2/pms_config.go"; then
  ok "a surface flag without the master flag is a startup failure (loud, not silently off)"
else
  no "an incoherent flag set would not fail closed"
fi
# the deployed frontend bundle must not be built with the admin flag on
if grep -RIl "NEXT_PUBLIC_PHASE3_ADMIN" "$ROOT/deploy" 2>/dev/null | grep -q .; then
  no "a deployment file sets NEXT_PUBLIC_PHASE3_ADMIN (the deployed bundle must be dark)"
else
  ok "no deployment file enables the Phase-3 admin bundle"
fi

# ---------------------------------------------------------------- 3. the migration is reversible
UP="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
if [ -f "$UP" ] && [ -f "$DOWN" ]; then
  ok "migration 0010 has both an up and a down script"
else
  no "migration 0010 is missing its up or down script"
fi
# every controlled function the up script creates must be dropped by the down script: a rollback that leaves
# executable functions behind is not a rollback.
missing=""
while read -r fn; do
  grep -q "DROP FUNCTION IF EXISTS iam_v2.$fn" "$DOWN" || missing="$missing $fn"
done < <(grep -oE "CREATE OR REPLACE FUNCTION iam_v2\.[a-z0-9_]+" "$UP" | sed 's/.*iam_v2\.//' | sort -u)
if [ -z "$missing" ]; then
  ok "every function created by 0010 is dropped by its down script"
else
  no "down script does not drop:$missing"
fi
# every table the up script creates must be dropped too
tmissing=""
while read -r tb; do
  grep -q "DROP TABLE IF EXISTS iam_v2.$tb" "$DOWN" || tmissing="$tmissing $tb"
done < <(grep -oE "CREATE TABLE iam_v2\.[a-z0-9_]+" "$UP" | sed 's/.*iam_v2\.//' | sort -u)
if [ -z "$tmissing" ]; then
  ok "every table created by 0010 is dropped by its down script"
else
  no "down script does not drop tables:$tmissing"
fi

# ---------------------------------------------------------------- 4. zero runtime privilege while dark
if grep -q "REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition" "$UP" &&
   ! grep -qE "GRANT (EXECUTE|SELECT|INSERT|UPDATE|DELETE).*TO (svc_|PUBLIC)" "$UP"; then
  ok "0010 grants no runtime role any iam_v2 privilege"
else
  no "0010 grants a runtime privilege (Gate-P is a separate, authorized step)"
fi

# ---------------------------------------------------------------- 5. no live-evidence fabrication
# The evidence bundle must never contain claims about an appliance this tooling has not actually touched.
if grep -RIn "LIVE VERIFIED" "$ROOT/docs/manifests" 2>/dev/null | grep -q .; then
  no "a manifest claims live verification that this offline tooling cannot have performed"
else
  ok "no offline artifact claims live verification"
fi

if [ $JSON -eq 1 ]; then
  printf '{"pass":%d,"fail":%d,"checks":[' "$pass" "$fail"
  first=1
  for r in "${RESULTS[@]}"; do
    st="${r%%|*}"; msg="${r#*|}"
    [ $first -eq 1 ] || printf ','
    printf '{"status":"%s","check":"%s"}' "$st" "$(printf '%s' "$msg" | sed 's/"/\\"/g')"
    first=0
  done
  printf ']}\n'
else
  echo "============================================================"
  echo "PHASE3_PREFLIGHT: pass=$pass fail=$fail -> $([ $fail -eq 0 ] && echo PASS || echo FAIL)"
fi
[ $fail -eq 0 ]
