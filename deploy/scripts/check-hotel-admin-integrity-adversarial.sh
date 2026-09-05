#!/usr/bin/env bash
# THE GUARDS THEMSELVES, ATTACKED.
#
# The first version of these guards passed on every good input and on several bad ones, because a check that
# cannot run is indistinguishable from a check that passed:
#
#   * the live-identity comparison was `[ -n "$served" ] && [ "$served" != "$want" ]`, which is FALSE when
#     nothing could be extracted -- so an endpoint that served no BUILD_ID at all reported its identity as
#     verified;
#   * the required-route loop iterated a producer that was never asserted non-empty, so a producer returning
#     nothing completed without making a single request and reported success;
#   * and the standing checker held a ROLLBACK target only to the flag-inlining scan, so a release with
#     correctly inlined flags and a missing operator route -- exactly what an older UI looks like -- was
#     reported as "a legitimate rollback".
#
# Each case below drives the REAL function against a controlled endpoint or a staged release and asserts it
# REFUSES. A case that passes for the wrong reason is reported as a failure, because it would leave the real
# condition unguarded.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CONTRACT="$ROOT/hotel-admin/capability-contract.json"
WORK="$(mktemp -d)"
trap 'stop_server 2>/dev/null; rm -rf "$WORK"' EXIT
fail=0
pass=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
no()  { echo "  *** FAIL: $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/        /' | head -8; fail=1; }

[ -f "$CONTRACT" ] || { echo "SKIP: no capability contract at $CONTRACT"; echo "HOTEL_ADMIN_ADVERSARIAL = SKIP"; exit 0; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 is required"; echo "HOTEL_ADMIN_ADVERSARIAL = SKIP"; exit 0; }

# Sourcing the real deployment script gives us the real smoke_live and the real shared contract library.
# shellcheck source=deploy-hotel-admin.sh
. "$HERE/deploy-hotel-admin.sh"

ROUTE_COUNT="$(ha_count "$(ha_contract_routes "$CONTRACT")")"
[ "$ROUTE_COUNT" -ge 1 ] || { echo "SKIP: the contract declares no routes"; echo "HOTEL_ADMIN_ADVERSARIAL = SKIP"; exit 0; }

# ---- a controlled Hotel Admin ------------------------------------------------------------------------------
# Serves a chosen BUILD_ID stamp and records every path it is asked for, so "was every route exercised" is
# answered by what the SERVER saw and not by what the checker claims it did.
SERVER_PID=""
stop_server() { [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null; SERVER_PID=""; return 0; }

serve() { # serve <build-id-or-empty> -> echoes port; writes request log to $WORK/requests
  stop_server
  : > "$WORK/requests"
  : > "$WORK/port"
  SC_BID="$1" SC_LOG="$WORK/requests" SC_PORT_FILE="$WORK/port" python3 - >/dev/null 2>&1 <<'PY' &
import http.server, os, threading, socketserver

bid = os.environ["SC_BID"]
log = os.environ["SC_LOG"]

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log, "a", encoding="utf-8") as f:
            f.write(self.path + "\n")
        body = "<!DOCTYPE html>"
        if bid:
            body += "<!--%s-->" % bid
        body += "<html><body>hotel admin</body></html>"
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body.encode())
    def log_message(self, *a):
        pass

srv = socketserver.TCPServer(("127.0.0.1", 0), H)
with open(os.environ["SC_PORT_FILE"], "w", encoding="utf-8") as f:
    f.write(str(srv.server_address[1]))
threading.Thread(target=srv.serve_forever, daemon=True).start()
import time
time.sleep(45)
PY
  # The redirect is not tidiness: serve() is called inside a command substitution, and a background child that
  # inherits the captured stdout keeps that substitution waiting until the child exits - so the port would be
  # handed back long after the case had given up. The port travels through a file for the same reason.
  SERVER_PID=$!
  for _ in $(seq 1 40); do [ -s "$WORK/port" ] && break; sleep 0.2; done
  cat "$WORK/port"
}

# stage <name> <inlined:yes|no> <manifest:yes|no> <routes-json> [nav-route-to-drop] -> release dir
stage() {
  local name="$1" inlined="$2" manifest="$3" routes="$4"
  local rel="$WORK/releases/$name"
  mkdir -p "$rel/.next/static/chunks/app"
  # Long enough to be a plausible Next BUILD_ID: the extractor requires >= 16 url-safe characters, and a
  # short fixture id would be refused as malformed before any comparison happened.
  echo "BID${name}0000000000000000" > "$rel/.next/BUILD_ID"
  : > "$rel/server.js"
  cp "$CONTRACT" "$rel/capability-contract.json"
  if [ "$inlined" = "no" ]; then
    printf 'let B="1"===L.env.NEXT_PUBLIC_PHASE2_ADMIN;\n' > "$rel/.next/static/chunks/app/layout.js"
  else
    printf 'let B=!0;/* inlined */\n' > "$rel/.next/static/chunks/app/layout.js"
  fi
  SC_REL="$rel" SC_ROUTES="$routes" python3 -c 'import json, os
routes = json.loads(os.environ["SC_ROUTES"])
m = {r.rstrip("/") + "/page": r for r in routes}
json.dump(m, open(os.environ["SC_REL"] + "/.next/app-path-routes-manifest.json", "w", encoding="utf-8"))'
  if [ "$manifest" = "yes" ]; then
    SC_REL="$rel" SC_NAME="$name" python3 -c 'import json, os
json.dump({"source_commit": "0123456789abcdef0123456789abcdef01234567", "source_state": "clean",
           "build_id": "BID" + os.environ["SC_NAME"] + "0000000000000000", "released_at": "2026-09-05T00:00:00Z",
           "contract_version": "1",
           "build_flags": {"NEXT_PUBLIC_PHASE2_ADMIN": "1", "NEXT_PUBLIC_PHASE3_ADMIN": "1"}},
          open(os.environ["SC_REL"] + "/hotel-admin-release.json", "w", encoding="utf-8"), indent=2)'
  fi
  echo "$rel"
}

ALL_ROUTES="$(ha_contract_routes "$CONTRACT" | python3 -c 'import sys, json; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))')"

echo "== smoke_live: live identity =="

# 1. THE FAIL-OPEN THAT SHIPPED: an endpoint that serves no BUILD_ID at all.
PORT="$(serve "")"
GOOD="$(stage good yes yes "$ALL_ROUTES")"
if HOTEL_ADMIN_PORT="$PORT" smoke_live "BIDgood0000000000000000" "$GOOD" >"$WORK/o1" 2>&1; then
  no "an endpoint serving NO BUILD_ID was accepted" "$(cat "$WORK/o1")"
else
  grep -q "could not extract a BUILD_ID" "$WORK/o1" \
    && ok "an endpoint serving no BUILD_ID is REFUSED" \
    || no "refused, but not for the missing identity" "$(cat "$WORK/o1")"
fi
stop_server

# 2. A DIFFERENT bundle answering: the mismatch must be caught, not tolerated.
PORT="$(serve "BIDsomethingelse0000000000000000")"
if HOTEL_ADMIN_PORT="$PORT" smoke_live "BIDgood0000000000000000" "$GOOD" >"$WORK/o2" 2>&1; then
  no "an endpoint serving a DIFFERENT BUILD_ID was accepted" "$(cat "$WORK/o2")"
else
  grep -q "is serving BUILD_ID" "$WORK/o2" \
    && ok "an endpoint serving a different BUILD_ID is REFUSED" \
    || no "refused, but not for the identity mismatch" "$(cat "$WORK/o2")"
fi
stop_server

echo "== smoke_live: required routes =="

# 3. AN EMPTY ROUTE PRODUCER. A `while read` over nothing makes no request and used to report success.
PORT="$(serve "BIDgood0000000000000000")"
printf '{ "required_routes": { "routes": [] }, "required_navigation": { "entries": [] }, "forbidden_in_client_bundle": { "patterns": ["env.NEXT_PUBLIC_PHASE2_ADMIN"] }, "required_build_flags": {} }\n' > "$WORK/empty-contract.json"
if HOTEL_ADMIN_PORT="$PORT" HOTEL_ADMIN_CONTRACT="$WORK/empty-contract.json" smoke_live "BIDgood0000000000000000" "$GOOD" >"$WORK/o3" 2>&1; then
  no "an empty route producer was accepted as a passing surface check" "$(cat "$WORK/o3")"
else
  grep -q "yielded no required routes" "$WORK/o3" \
    && ok "an empty route producer is REFUSED" \
    || no "refused, but not for the empty producer" "$(cat "$WORK/o3")"
fi
stop_server

# 4. EVERY REQUIRED ROUTE IS INDIVIDUALLY EXERCISED — asserted from the SERVER's request log.
PORT="$(serve "BIDgood0000000000000000")"
HOTEL_ADMIN_PORT="$PORT" smoke_live "BIDgood0000000000000000" "$GOOD" >"$WORK/o4" 2>&1
rc=$?
missing=""
while IFS= read -r r; do
  [ -n "$r" ] || continue
  grep -qx -- "$r" "$WORK/requests" || missing="$missing $r"
done < <(ha_contract_routes "$CONTRACT")
if [ "$rc" != "0" ]; then
  no "a correct endpoint and release did not pass the smoke test" "$(cat "$WORK/o4")"
elif [ -n "$missing" ]; then
  no "these required routes were never requested:$missing"
elif ! grep -q "checked $ROUTE_COUNT/$ROUTE_COUNT" "$WORK/o4"; then
  no "the smoke test did not report an exact route count of $ROUTE_COUNT/$ROUTE_COUNT" "$(cat "$WORK/o4")"
else
  ok "all $ROUTE_COUNT required routes were individually requested and counted $ROUTE_COUNT/$ROUTE_COUNT"
fi
stop_server

echo "== rollback eligibility: the FULL contract =="

# 5. CORRECT FLAGS, MISSING ROUTE. This is what an older UI looks like, and it was reported as a legitimate
#    rollback by the standing checker.
SHORT="$(python3 -c 'import json,sys; r=json.loads(sys.argv[1]); print(json.dumps([x for x in r if x != "/internet-packages"]))' "$ALL_ROUTES")"
REL_SHORT="$(stage shortroutes yes yes "$SHORT")"
if ha_release_satisfies_contract "$REL_SHORT" "$CONTRACT" >"$WORK/o5" 2>&1; then
  no "a release missing a required operator route was accepted as a rollback target"
else
  grep -q "missing required operator route /internet-packages" "$WORK/o5" \
    && ok "a release with correct flags but a MISSING required route is REFUSED" \
    || no "refused, but not for the missing route" "$(cat "$WORK/o5")"
fi

# 6. MISSING REQUIRED NAVIGATION. The routes the contract lists are present, but a NAVIGATION entry's route is
#    not — the operator would have the page and no way to reach it.
NAVDROP="$(python3 -c 'import json,sys
routes=json.loads(sys.argv[1]); contract=json.load(open(sys.argv[2],encoding="utf-8"))
nav={e["route"] for e in contract["required_navigation"]["entries"]}
print(json.dumps([r for r in routes if r not in nav] + [r for r in routes if r in nav][1:]))' "$ALL_ROUTES" "$CONTRACT")"
REL_NAV="$(stage navdrop yes yes "$NAVDROP")"
if ha_release_satisfies_contract "$REL_NAV" "$CONTRACT" >"$WORK/o6" 2>&1; then
  no "a release missing a required NAVIGATION target was accepted as a rollback target"
else
  grep -qE "missing required operator route|cannot serve '" "$WORK/o6" \
    && ok "a release missing a required navigation target is REFUSED" \
    || no "refused, but not for the missing navigation" "$(cat "$WORK/o6")"
fi

# 7. AND THE OTHER HALF OF THE CONTRACT: correct releases still pass. A guard that refuses everything is as
#    useless as one that accepts everything, and far more likely to be switched off.
if ha_release_satisfies_contract "$GOOD" "$CONTRACT" >"$WORK/o7" 2>&1; then
  ok "a correct release passes the full contract"
else
  no "a correct release was refused" "$(cat "$WORK/o7")"
fi
REL_PREV="$(stage compatprev yes yes "$ALL_ROUTES")"
if ha_release_satisfies_contract "$REL_PREV" "$CONTRACT" >"$WORK/o8" 2>&1; then
  ok "a compatible previous release is accepted as a legitimate rollback"
else
  no "a compatible previous release was refused" "$(cat "$WORK/o8")"
fi

# 8. Provenance is still mandatory.
REL_ANON="$(stage anon yes no "$ALL_ROUTES")"
if ha_release_satisfies_contract "$REL_ANON" "$CONTRACT" >"$WORK/o9" 2>&1; then
  no "a release with no manifest was accepted"
else
  ok "a release that cannot prove its provenance is REFUSED"
fi

echo "============================================================"
if [ "$fail" = "0" ]; then
  echo "HOTEL_ADMIN_ADVERSARIAL = PASS ($pass cases)"
  exit 0
fi
echo "HOTEL_ADMIN_ADVERSARIAL = FAIL"
exit 1
