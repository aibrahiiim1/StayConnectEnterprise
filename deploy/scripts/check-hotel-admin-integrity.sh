#!/usr/bin/env bash
# EXACTLY ONE SERVABLE HOTEL ADMIN RELEASE, AND IT IS THE ONE WE VERIFIED.
#
# This exists because the PRE-LIVE appliance served an operator surface with no Internet Packages, no Service
# Plans and no PMS Connection for five days, and every check anyone ran said it was healthy. The unit was
# active. The port answered. HTTP was 200. The routes were compiled in and answered by URL. What had happened
# was subtler than any of those questions could reach:
#
#   * the bundle was built with no NEXT_PUBLIC_PHASE*_ADMIN flags, so Next never substituted them and the
#     compiled navigation was permanently false in a browser -- proven by hash against an A/B build;
#   * /opt/stayconnect/hotel-admin was a plain DIRECTORY, not the verified release symlink, because a bundle
#     had been installed by copying rather than by switching a release;
#   * hotel-admin.previous still pointed at a release from five days earlier that predates those surfaces --
#     a rollback that would have made the outage worse and called it recovery.
#
# So this asks the questions that would have caught it, and it asks them of the RUNNING system:
#
#   1. is the runtime path a symlink to exactly one release under the releases dir?
#   2. does that release carry a manifest that proves its commit and BUILD_ID?
#   3. is the BUILD_ID the live service is actually SERVING the same one?
#   4. were the capability flags really inlined, or is the navigation compiled off?
#   5. do the required operator surfaces answer?
#   6. is `previous` either absent or a release that still satisfies today's contract?
#   7. is anything else reachable as a servable release?
#
# It changes NOTHING. It is safe to run at any time, and it is meant to run at deploy time and on demand.
#
# Usage: check-hotel-admin-integrity.sh [--json]
# Exit:  0 healthy · 1 integrity violation · 2 cannot check
set -uo pipefail

CURRENT_LINK="${HOTEL_ADMIN_CURRENT:-/opt/stayconnect/hotel-admin}"
PREVIOUS_LINK="${HOTEL_ADMIN_PREVIOUS:-$CURRENT_LINK.previous}"
RELEASES_DIR="${HOTEL_ADMIN_RELEASES:-/opt/stayconnect/releases/hotel-admin}"
PORT="${HOTEL_ADMIN_PORT:-3100}"
MANIFEST_NAME="hotel-admin-release.json"
MANIFEST_NAME_DEFAULT="$MANIFEST_NAME"
# THE OPERATOR ENDPOINT IS CONFIGURED, NOT REMEMBERED. Passing HOTEL_ADMIN_PUBLIC_* by hand means the external
# check runs when someone thinks to run it that way, which is not a guarantee. An appliance records the real
# endpoint here once and every invocation - by hand, by a deployment, by a timer - checks it.
HA_ENV="${HOTEL_ADMIN_INTEGRITY_ENV:-/etc/stayconnect/hotel-admin-integrity.env}"
# shellcheck disable=SC1090
[ -f "$HA_ENV" ] && . "$HA_ENV"
# shellcheck source=lib-hotel-admin-contract.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-hotel-admin-contract.sh"
JSON=0
[ "${1:-}" = "--json" ] && JSON=1

fails=0; notes=()
ok()  { notes+=("PASS|$1"); [ $JSON -eq 1 ] || echo "  [PASS] $1"; }
no()  { notes+=("FAIL|$1"); fails=$((fails+1)); [ $JSON -eq 1 ] || echo "  [FAIL] $1"; }

command -v python3 >/dev/null 2>&1 || { echo "CANNOT CHECK: python3 is required"; exit 2; }

# A carriage return, spelled so this file never CONTAINS one. The first version of these strips was
# written as $'<literal CR>' and the byte did not survive editing, so the strip silently did nothing
# and every route comparison failed on a bundle that contained every route.
CR=$''

jget() { python3 -c 'import json,sys
try: d=json.load(open(sys.argv[1], encoding="utf-8"))
except Exception: raise SystemExit(0)
v=eval(sys.argv[2])
print("" if v is None else (v if isinstance(v,str) else json.dumps(v)))' "$1" "$2" 2>/dev/null; }

# ---- 1. one servable release, reached through a symlink ---------------------------------------------------
if [ -L "$CURRENT_LINK" ]; then
  LIVE="$(readlink -f "$CURRENT_LINK")"
  ok "runtime path is a release symlink -> $LIVE"
elif [ -d "$CURRENT_LINK" ]; then
  LIVE="$CURRENT_LINK"
  no "runtime path $CURRENT_LINK is a PLAIN DIRECTORY, not a verified release symlink — this is how an unmanaged bundle became live without provenance"
else
  no "runtime path $CURRENT_LINK does not exist"; LIVE=""
fi

if [ -n "$LIVE" ] && [ -L "$CURRENT_LINK" ]; then
  case "$LIVE" in
    "$RELEASES_DIR"/*) ok "the live release lives under the managed releases directory" ;;
    *) no "the live release $LIVE is OUTSIDE $RELEASES_DIR — it is not a managed release" ;;
  esac
fi

# ---- 2. the release can prove what it is ------------------------------------------------------------------
MF="$LIVE/$MANIFEST_NAME"
COMMIT=""; BID=""
if [ -n "$LIVE" ] && [ -f "$MF" ]; then
  COMMIT="$(jget "$MF" 'd["source_commit"]')"
  BID="$(jget "$MF" 'd["build_id"]')"
  [ -n "$COMMIT" ] && ok "live release records its source commit: ${COMMIT:0:12}" \
                   || no "live release manifest records no source_commit"
  disk_bid="$(cat "$LIVE/.next/BUILD_ID" 2>/dev/null || true)"
  if [ -n "$BID" ] && [ "$BID" = "$disk_bid" ]; then
    ok "manifest BUILD_ID matches the release on disk ($BID)"
  else
    no "manifest BUILD_ID '$BID' does not match the release on disk '$disk_bid'"
  fi
else
  no "the live Hotel Admin release carries NO $MANIFEST_NAME — it cannot prove which commit built it"
fi

# ---- 3. what the service is actually SERVING, FAIL-CLOSED --------------------------------------------------
#
# This used to report a pass whenever a BUILD_ID could not be extracted: `[ -n "$SERVED" ] && ... != ...` is
# false when SERVED is empty, and the else-branch said nothing at all. So a page rendered without the stamp, an
# endpoint answering with something that is not this application, or an extraction that quietly broke, all left
# the identity unverified and the checker green. An identity check that cannot obtain an identity has FAILED.
if SERVED="$(ha_served_build_id "http://127.0.0.1:$PORT/login")"; then
  if [ -z "$BID" ]; then
    no "the service serves BUILD_ID '$SERVED' but the release on disk records none to compare it with"
  elif [ "$SERVED" != "$BID" ]; then
    no "the RUNNING service serves BUILD_ID '$SERVED' but the recorded live release is '$BID' — the process is running a different bundle than the one on disk"
  else
    ok "the running service serves the recorded BUILD_ID ($SERVED)"
  fi
else
  no "no usable BUILD_ID could be read from http://127.0.0.1:$PORT/login (no answer, no stamp, or a malformed one) — what the service is serving is unverifiable, which is a failure and not a pass"
fi

# ---- 3b. THE ENDPOINT AN OPERATOR ACTUALLY USES ------------------------------------------------------------
#
# Localhost process health says nothing about what staff reach. A stale Caddy vhost, a proxy aimed at another
# port or a cached upstream would all leave 127.0.0.1 perfect while the operator is on an old bundle - and the
# operator's browser is the only place this defect was ever visible. Where a real endpoint is configured it is
# checked, and it must serve the SAME BUILD_ID as the managed release.
if [ -n "${HOTEL_ADMIN_PUBLIC_URL:-}" ]; then
  if PUBLIC_ID="$(ha_served_build_id "${HOTEL_ADMIN_PUBLIC_URL%/}/login")"; then
    if [ -n "$BID" ] && [ "$PUBLIC_ID" = "$BID" ]; then
      ok "the operator endpoint ${HOTEL_ADMIN_PUBLIC_URL} serves the managed release ($PUBLIC_ID)"
    else
      no "the operator endpoint ${HOTEL_ADMIN_PUBLIC_URL} serves BUILD_ID '$PUBLIC_ID' but the managed release is '$BID'"
    fi
  else
    no "the operator endpoint ${HOTEL_ADMIN_PUBLIC_URL} served no usable BUILD_ID — what staff actually reach is unverifiable"
  fi
else
  ok "no external operator endpoint configured for this check (HOTEL_ADMIN_PUBLIC_URL unset)"
fi

# ---- 4. the capability flags were really inlined ----------------------------------------------------------
CONTRACT="${HOTEL_ADMIN_CONTRACT:-$LIVE/capability-contract.json}"
if [ -n "$LIVE" ] && [ -f "$CONTRACT" ]; then
  residual=0
  while IFS= read -r pat; do
    pat="${pat%$CR}"   # a CR from a Windows-built stream would silently match nothing
    [ -n "$pat" ] || continue
    if grep -rql -- "$pat" "$LIVE/.next/static" 2>/dev/null; then
      no "the live client bundle still resolves '$pat' at runtime — it was built WITHOUT that capability flag, so the navigation it gates is compiled OFF"
      residual=1
    fi
  done < <(python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
sys.stdout.reconfigure(newline=chr(10))
for p in d["forbidden_in_client_bundle"]["patterns"]: print(p)' "$CONTRACT" 2>/dev/null)
  [ "$residual" = "0" ] && ok "every capability flag was inlined at build time (no runtime lookups remain)"

  # ---- 5. the required operator surfaces answer ------------------------------------------------------------
  bad=0
  while IFS= read -r route; do
    route="${route%$CR}"
    [ -n "$route" ] || continue
    code="$(curl -sS -o /dev/null -w "%{http_code}" --max-time 5 "http://127.0.0.1:$PORT$route" 2>/dev/null || echo 000)"
    case "$code" in
      200|302|303|307|308) : ;;
      *) no "required operator surface $route is not served (HTTP $code)"; bad=1 ;;
    esac
  done < <(python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
sys.stdout.reconfigure(newline=chr(10))
for r in d["required_routes"]["routes"]: print(r)' "$CONTRACT" 2>/dev/null)
  [ "$bad" = "0" ] && ok "every required operator surface is served"
else
  no "no capability contract available for the live release, so the operator surface cannot be verified"
fi

# ---- 6. the rollback pointer is not a trap ----------------------------------------------------------------
#
# This checked ONLY the flag-inlining fingerprint, so a release with correctly inlined flags but missing a
# current operator route - exactly what an older UI looks like - was reported as "a legitimate rollback" by the
# tool whose job is to say otherwise. A rollback target is now held to the SAME full contract as a deployment
# candidate, through the same shared implementation: provenance, inlined flags, every required route and every
# required navigation entry.
if [ -L "$PREVIOUS_LINK" ]; then
  PREV="$(readlink -f "$PREVIOUS_LINK")"
  if [ ! -d "$PREV" ]; then
    no "previous release pointer is dangling: $PREV"
  else
    PREV_CONTRACT="${HOTEL_ADMIN_CONTRACT:-$CONTRACT}"
    if PREV_WHY="$(ha_release_satisfies_contract "$PREV" "$PREV_CONTRACT" 2>&1)"; then
      ok "previous release satisfies the FULL capability contract and is a legitimate rollback"
    else
      no "previous release $PREV is NOT a legitimate rollback: ${PREV_WHY:-it does not satisfy the current capability contract}. It must be archived, not wired as an executable rollback"
    fi
  fi
else
  ok "no previous-release pointer is wired (nothing obsolete can be rolled back into service)"
fi

# ---- 7. nothing else is servable --------------------------------------------------------------------------
# Extra release directories are fine as evidence; what is NOT fine is a second one being REACHABLE as the
# runtime. Only the two pointers can make a release live, so those are what get counted.
reachable=0
for link in "$CURRENT_LINK" "$PREVIOUS_LINK"; do
  [ -L "$link" ] && reachable=$((reachable+1))
done
[ "$reachable" -le 2 ] && ok "only the managed pointers can select a release ($reachable in use)" \
                       || no "more than the managed pointers can select a release"

if [ $JSON -eq 1 ]; then
  printf '{"healthy":%s,"failures":%d,"live_release":"%s","source_commit":"%s","build_id":"%s"}\n' \
    "$([ $fails -eq 0 ] && echo true || echo false | tr -d '')" "$fails" "$LIVE" "$COMMIT" "$BID"
else
  echo "============================================================"
  echo "HOTEL_ADMIN_INTEGRITY = $([ $fails -eq 0 ] && echo PASS || echo "FAIL ($fails)")"
fi
[ $fails -eq 0 ]
