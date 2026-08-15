#!/usr/bin/env bash
# PHASE-6 FLAG COHERENCE ACROSS THE SERVICES THAT SHARE THE DECISION.
#
# scd decides whether a NEW aggregate entitlement may be created. acctd consumes the budgets. edged decides
# whether the mode may be published and whether the operator surface exists. Those three reading different
# flag states is not a cosmetic inconsistency -- it is how an appliance ends up creating access it cannot
# account for, or offering a screen for a capability the runtime refuses.
#
# The accrual side is already safe by construction: acctd accrues for any entitlement that EXISTS, whatever
# its own flag says, so a partially applied deployment can never turn finite access into unlimited access.
# This check exists for the other direction -- a runtime that is half-configured should be visible and
# refused BEFORE it is accepted as live-dark, not diagnosed afterwards from an entitlement nobody expected.
#
# Usage:
#   tools/phase6-flag-coherence.sh                     # read the running units on THIS host
#   tools/phase6-flag-coherence.sh --files a.env b.env # read environment files instead
#
# It reads only. It changes nothing, and it never enables a flag.
set -uo pipefail

FLAGS="STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME \
STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN"
SERVICES="${PHASE6_SERVICES:-stayconnect-scd stayconnect-acctd stayconnect-edged}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# norm turns an absent or empty value into the explicit word "off", so "unset" and "false" are not reported as
# a disagreement when they mean the same thing -- and so a TYPO is not silently read as off.
norm(){
  local v; v="$(printf '%s' "${1:-}" | tr -d '"' | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
  case "$v" in
    ""|false|0|no)  printf 'off';;
    true|1|yes)     printf 'on';;
    *)              printf 'INVALID(%s)' "$v";;
  esac
}

# read_env <service> -> prints "FLAG=value" lines for that unit's environment
read_env(){
  local svc="$1"
  if [ -n "${PHASE6_ENV_DIR:-}" ] && [ -f "$PHASE6_ENV_DIR/$svc.env" ]; then
    cat "$PHASE6_ENV_DIR/$svc.env"
    return
  fi
  systemctl show "$svc" -p Environment --value 2>/dev/null | tr ' ' '\n'
}

echo "== Phase-6 flag coherence =="
declare -A seen
for svc in $SERVICES; do
  env_text="$(read_env "$svc")"
  for f in $FLAGS; do
    raw="$(printf '%s\n' "$env_text" | grep -E "^${f}=" | tail -1 | cut -d= -f2-)"
    val="$(norm "$raw")"
    seen["$svc|$f"]="$val"
    case "$val" in
      INVALID*) no "$svc $f is a parseable value" "$val -- a flag nobody can parse must never be read as permission";;
    esac
  done
done

for f in $FLAGS; do
  vals=""
  for svc in $SERVICES; do
    vals="$vals ${seen["$svc|$f"]}"
  done
  distinct="$(printf '%s\n' $vals | sort -u | tr '\n' ' ')"
  count="$(printf '%s\n' $vals | sort -u | wc -l)"
  if [ "$count" = "1" ]; then
    ok "$f agrees across [$SERVICES]: $(printf '%s' $distinct)"
  else
    no "$f DISAGREES across services" "$(for svc in $SERVICES; do printf '%s=%s ' "$svc" "${seen["$svc|$f"]}"; done)"
  fi
done

# A child flag without its master is a deployment mistake in ANY single service, and the services fail closed
# on it at startup -- but it is worth naming here too, because this script is what runs before acceptance.
for svc in $SERVICES; do
  master="${seen["$svc|STAYCONNECT_PHASE6_MASTER"]}"
  for child in STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST \
               STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN; do
    if [ "${seen["$svc|$child"]}" = "on" ] && [ "$master" != "on" ]; then
      no "$svc $child without its master" "the service refuses to start in this state, by design"
    fi
  done
done
[ "$fail" -eq 0 ] && ok "no child flag is set without its master"

echo "------------------------------------------------------------"
printf 'PHASE6_FLAG_COHERENCE pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
