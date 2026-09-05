#!/usr/bin/env bash
# ONE DEFINITION OF "DOES THIS RELEASE SATISFY THE CONTRACT", SHARED BY EVERY GUARD.
#
# The deployment script and the standing integrity checker were asking overlapping but DIFFERENT questions,
# and the difference was the hole. Deployment held a rollback target to the full contract; the checker held it
# only to the flag-inlining scan, so a release with correctly inlined flags but missing an operator route was
# reported as "a legitimate rollback" by the very tool whose job is to say otherwise. Two implementations of
# one rule will always drift, and the drift is always in the direction of passing.
#
# Everything here FAILS CLOSED. A producer that yields nothing, a manifest that cannot be read, a BUILD_ID
# that cannot be extracted: each is a failure, never a silent pass. That is the specific defect these guards
# were written to prevent and then contained themselves -- a loop over an empty list reports success, and a
# check that cannot run is indistinguishable from a check that passed.

# ha_json_list <file> <python-expression-yielding-a-list> — one item per line, LF, or NOTHING on failure.
ha_json_list() {
  python3 -c 'import json, sys
sys.stdout.reconfigure(newline=chr(10))
d = json.load(open(sys.argv[1], encoding="utf-8"))
for item in eval(sys.argv[2]):
    print(item)' "$1" "$2" 2>/dev/null
}

# ha_contract_routes <contract>   — the required operator routes.
ha_contract_routes() { ha_json_list "$1" 'd["required_routes"]["routes"]'; }
# ha_contract_patterns <contract> — the residual-lookup fingerprints of a flagless build.
ha_contract_patterns() { ha_json_list "$1" 'd["forbidden_in_client_bundle"]["patterns"]'; }
# ha_contract_nav <contract>      — "label|route|flag" per required navigation entry.
ha_contract_nav() {
  ha_json_list "$1" '["%s|%s|%s" % (e["label"], e["route"], e.get("flag","")) for e in d["required_navigation"]["entries"]]'
}
# ha_contract_flags <contract>    — "NAME=VALUE" per required build flag.
ha_contract_flags() {
  ha_json_list "$1" '["%s=%s" % (k, v) for k, v in d["required_build_flags"].items() if not k.startswith("_")]'
}

# ha_count <text> — lines in a materialised producer result ("" -> 0).
ha_count() { [ -z "$1" ] && echo 0 || printf '%s\n' "$1" | grep -c . ; }

# ha_release_has_route <release-dir> <route> — is the route in the BUILT app route manifest?
#
# The route travels with its leading slash stripped and restored inside python: on a Git-Bash host MSYS
# rewrites anything that looks like a POSIX absolute path, in argv AND in environment values, so
# "/internet-packages" arrives as "C:/Program Files/Git/internet-packages" and every lookup fails on a bundle
# that contains every route.
ha_release_has_route() {
  SC_REL="$1" SC_ROUTE="${2#/}" python3 -c 'import json, os
m = json.load(open(os.environ["SC_REL"] + "/.next/app-path-routes-manifest.json", encoding="utf-8"))
want = "/" + os.environ["SC_ROUTE"]
raise SystemExit(0 if want in set(v for v in m.values() if isinstance(v, str)) else 1)' 2>/dev/null
}

# ha_served_build_id <url> — the BUILD_ID a running endpoint is serving, or NOTHING.
#
# Next stamps it into every rendered document as an HTML comment. Extraction uses sed and not
# `tr -d "<!->"`, because that set is the RANGE "!" to ">" (ASCII 33-62) and silently deletes every digit.
# An empty result here is NOT a pass anywhere: callers must treat it as failure.
ha_served_build_id() {
  # TLS AND VHOST NAMING ARE TRANSPORT, NOT IDENTITY. The operator endpoint is served over a local-CA
  # certificate on a vhost name that does not resolve from the appliance itself, so a bare curl fails for
  # reasons that say nothing about which bundle is being served. These are explicit, opt-in knobs rather than
  # silent tolerance: --cacert verifies properly where a trust anchor is available, HOST supplies the vhost
  # name for an IP URL, and INSECURE is a deliberate, recorded choice for an endpoint whose certificate is a
  # separate matter. What is NEVER tolerated is an unreadable identity: that still returns failure.
  local -a xopts=()
  [ -n "${HOTEL_ADMIN_PUBLIC_CACERT:-}" ] && xopts+=(--cacert "$HOTEL_ADMIN_PUBLIC_CACERT")
  [ "${HOTEL_ADMIN_PUBLIC_INSECURE:-0}" = "1" ] && xopts+=(-k)
  [ -n "${HOTEL_ADMIN_PUBLIC_HOST:-}" ] && xopts+=(-H "Host: ${HOTEL_ADMIN_PUBLIC_HOST}")
  local body; body="$(curl -sS "${xopts[@]}" --max-time "${HOTEL_ADMIN_HTTP_TIMEOUT:-5}" "$1" 2>/dev/null || true)"
  [ -n "$body" ] || return 1
  local id; id="$(grep -o '<!--[A-Za-z0-9_-]\{16,\}-->' <<<"$body" | head -1 | sed 's/^<!--//; s/-->$//')"
  # MALFORMED IS NOT MISSING, AND NEITHER IS ACCEPTABLE. A BUILD_ID is Next's own url-safe id; anything else
  # reaching a comparison would compare true against nonsense.
  case "$id" in
    "" ) return 1 ;;
    *[!A-Za-z0-9_-]* ) return 1 ;;
  esac
  [ "${#id}" -ge 16 ] || return 1
  printf '%s\n' "$id"
}

# ha_release_satisfies_contract <release-dir> <contract> — THE WHOLE CONTRACT, one implementation.
#
# Provenance, inlined flags, required routes and required navigation. Prints reasons on stderr and returns
# non-zero on the first failure. Used to accept a candidate, to accept a rollback target, and to audit what is
# live -- so those three questions can never drift apart again.
ha_release_satisfies_contract() {
  local rel="$1" contract="$2" quiet="${3:-}"
  local say=1; [ "$quiet" = "quiet" ] && say=0
  _r() { [ "$say" = "1" ] && echo "$1" >&2; return 1; }

  [ -d "$rel" ]      || { _r "release $rel does not exist"; return 1; }
  [ -f "$contract" ] || { _r "no capability contract at $contract"; return 1; }

  # (1) PROVENANCE. A release that cannot say which commit built it can never be verified later.
  local mf="$rel/$MANIFEST_NAME_DEFAULT"
  [ -f "$mf" ] || { _r "release $rel carries no $MANIFEST_NAME_DEFAULT — it cannot prove its provenance"; return 1; }
  local commit bid disk_bid
  commit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1],encoding="utf-8")).get("source_commit") or "")' "$mf" 2>/dev/null)"
  bid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1],encoding="utf-8")).get("build_id") or "")' "$mf" 2>/dev/null)"
  [ -n "$commit" ] || { _r "$mf records no source_commit"; return 1; }
  [ -n "$bid" ]    || { _r "$mf records no build_id"; return 1; }
  disk_bid="$(cat "$rel/.next/BUILD_ID" 2>/dev/null || true)"
  [ "$bid" = "$disk_bid" ] || { _r "$mf says BUILD_ID=$bid but the release contains '$disk_bid'"; return 1; }

  # (2) THE FLAGS WERE REALLY INLINED. An empty pattern list means the check could not run, which is a
  #     failure and not a pass.
  local pats; pats="$(ha_contract_patterns "$contract")"
  [ -n "$pats" ] || { _r "the contract yielded no forbidden-pattern list; the flag-inlining check cannot run"; return 1; }
  local pat
  while IFS= read -r pat; do
    [ -n "$pat" ] || continue
    if grep -rql -- "$pat" "$rel/.next/static" 2>/dev/null; then
      _r "release $rel was built WITHOUT its capability flags: the client bundle still resolves '$pat' at runtime, so the navigation it gates is compiled OFF"
      return 1
    fi
  done <<< "$pats"

  # (3) EVERY REQUIRED OPERATOR ROUTE. Same rule for a candidate and for a rollback target: a release that
  #     cannot serve today's operator surfaces is archive evidence, not something to put in front of staff.
  local routes; routes="$(ha_contract_routes "$contract")"
  [ -n "$routes" ] || { _r "the contract yielded no required routes; the operator-surface check cannot run"; return 1; }
  local route
  while IFS= read -r route; do
    [ -n "$route" ] || continue
    ha_release_has_route "$rel" "$route" \
      || { _r "release $rel is missing required operator route $route"; return 1; }
  done <<< "$routes"

  # (4) AND THE NAVIGATION THOSE SURFACES HANG OFF, named as an operator would name it.
  local navs; navs="$(ha_contract_nav "$contract")"
  [ -n "$navs" ] || { _r "the contract yielded no required navigation; the operator-navigation check cannot run"; return 1; }
  local label nroute nflag
  while IFS='|' read -r label nroute nflag; do
    [ -n "$nroute" ] || continue
    ha_release_has_route "$rel" "$nroute" \
      || { _r "release $rel cannot serve '$label' ($nroute)"; return 1; }
  done <<< "$navs"

  return 0
}
