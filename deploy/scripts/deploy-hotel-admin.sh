#!/usr/bin/env bash
# Safe Hotel Admin deployment for the appliance.
#
# This script exists because a Next.js production build run in the wrong
# directory (/root) once exhausted the pilot VM's memory. It removes the need
# to build ON the appliance at all: the UI is built on a workstation/CI into a
# self-contained Next.js "standalone" bundle, shipped, and installed into an
# ATOMIC release directory that a symlink flips to. `node server.js` runs it —
# no npm install and no webpack build ever touch the appliance.
#
# Two phases:
#   deploy-hotel-admin.sh package            # run on the workstation/CI
#   deploy-hotel-admin.sh install <tarball>  # run on the appliance (as root)
#
# Guards:
#   - refuses to run its build/install from /root
#   - refuses to install if the tarball is missing or malformed
#   - keeps the previous release so a bad deploy can be rolled back
#   - restarts ONLY the hotel-admin unit
set -euo pipefail

RELEASES_DIR="${HOTEL_ADMIN_RELEASES:-/opt/stayconnect/releases/hotel-admin}"
CURRENT_LINK="${HOTEL_ADMIN_CURRENT:-/opt/stayconnect/hotel-admin}"
# Explicit "previous release" pointer. Rollback follows this, NOT directory
# mtime — extraction order, chown -R and pruning all perturb mtimes, so an
# mtime-based "second newest" is unreliable and can select the current release.
PREVIOUS_LINK="${HOTEL_ADMIN_PREVIOUS:-$CURRENT_LINK.previous}"
SERVICE="${HOTEL_ADMIN_SERVICE:-stayconnect-hotel-admin}"
RUN_USER="${HOTEL_ADMIN_USER:-stayconnect}"
# Releases that are retained as EVIDENCE but must never be served. Nothing here is reachable by the current or
# previous symlink, by systemd or by Caddy: the runtime only ever resolves through CURRENT_LINK.
ARCHIVE_DIR="${HOTEL_ADMIN_ARCHIVE:-/opt/stayconnect/releases/hotel-admin-archive}"
# The bundle's own machine-readable identity. Written at package time, verified before every switch.
MANIFEST_NAME="hotel-admin-release.json"

die() { echo "ERROR: $*" >&2; exit 1; }

# A carriage return, spelled so this file never CONTAINS one. The first version of these strips was
# written as $'<literal CR>' and the byte did not survive editing, so the strip silently did nothing
# and every route comparison failed on a bundle that contained every route.
CR=$''

# jqless JSON reader: this runs on an appliance where jq is not guaranteed, and python3 is.
jget() { # jget <file> <python-expression-on-d>
  python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
v=eval(sys.argv[2])
print("" if v is None else (v if isinstance(v,str) else json.dumps(v)))' "$1" "$2" 2>/dev/null
}

# atomic_link <target> <linkpath> — never rm+ln (a concurrent read never sees a
# missing target); a temp symlink + rename is atomic on the same filesystem.
atomic_link() { ln -sfn "$1" "$2.tmp"; mv -Tf "$2.tmp" "$2"; }

# `systemctl is-active` is NOT a readiness check. A unit with Restart=always
# reads "active" while it is crash-looping -- this appliance once showed
# "active" through 596 restarts. So health means: the port actually answers,
# AND the unit did not accumulate restarts while we were looking.
wait_healthy() {
  systemctl daemon-reload
  local before; before="$(systemctl show "$SERVICE" -p NRestarts --value 2>/dev/null || echo 0)"
  systemctl restart "$SERVICE"
  local i
  for i in $(seq 1 "${HOTEL_ADMIN_READY_TIMEOUT:-45}"); do
    if curl -fsS -o /dev/null --max-time 2 "http://127.0.0.1:${HOTEL_ADMIN_PORT:-3100}/" 2>/dev/null; then
      local after; after="$(systemctl show "$SERVICE" -p NRestarts --value 2>/dev/null || echo 0)"
      if [ "$after" != "$before" ]; then
        echo "WARN: $SERVICE served a request but restarted $before->$after during startup" >&2
      fi
      echo ">> $SERVICE serving on :${HOTEL_ADMIN_PORT:-3100} after ${i}s"
      return 0
    fi
    sleep 1
  done
  echo "ERROR: $SERVICE did not serve HTTP within ${HOTEL_ADMIN_READY_TIMEOUT:-45}s" >&2
  return 1
}

# verify_release <dir> — everything that can be checked WITHOUT touching the
# live symlink. Structure first, then a real boot of the candidate on a scratch
# port, because a tree can be structurally perfect and still fail to start.
verify_release() {
  local rel="$1"
  [ -f "$rel/server.js" ]        || die "release $rel has no server.js"
  [ -f "$rel/.next/BUILD_ID" ]   || die "release $rel has no .next/BUILD_ID — the hidden .next dir did not ship"
  [ -d "$rel/.next/static" ]     || die "release $rel has no .next/static"
  local bid; bid="$(cat "$rel/.next/BUILD_ID")"
  [ -n "$bid" ]                  || die "release $rel has an empty .next/BUILD_ID"

  # Boot the candidate on a port nothing else uses. This is the check that
  # would have caught the "Could not find a production build" crash BEFORE the
  # live symlink moved, instead of after.
  #
  # Two traps, both of which bit this function the first time it ran:
  #   * `[ -n "$pid" ] && kill ...` under `set -e` aborts the SCRIPT when the
  #     kill fails, so a perfectly healthy release reported failure. Every
  #     cleanup line below therefore ends in `|| true`.
  #   * `setsid` puts the child in a new process group whose id is NOT $!, so
  #     `kill -$pid` matched nothing and left the candidate listening — which
  #     also holds an ssh session open forever. We track the real pid instead.
  # stdin/stdout are detached from the caller so this never wedges a remote shell.
  local port="${HOTEL_ADMIN_VERIFY_PORT:-31999}"
  local log; log="$(mktemp)"
  local pid=""
  ( cd "$rel" && exec env NODE_ENV=production PORT="$port" HOSTNAME=127.0.0.1 \
      node server.js ) </dev/null >"$log" 2>&1 &
  pid=$!

  local ok=1 i
  for i in $(seq 1 "${HOTEL_ADMIN_VERIFY_TIMEOUT:-30}"); do
    kill -0 "$pid" 2>/dev/null || break          # candidate died; stop waiting
    # -f is deliberately absent: the app answers / with a 307 to /login, which
    # is a correct, serving response. Any HTTP status proves Next booted and
    # found its build; a missing .next produces a connection failure instead.
    if curl -sS -o /dev/null --max-time 2 "http://127.0.0.1:$port/" 2>/dev/null; then ok=0; break; fi
    sleep 1
  done

  # Clean up the candidate and anything it spawned, tolerating every failure.
  if [ -n "$pid" ]; then
    pkill -TERM -P "$pid" 2>/dev/null || true
    kill -TERM "$pid" 2>/dev/null || true
    sleep 1
    pkill -KILL -P "$pid" 2>/dev/null || true
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  # Belt and braces: never leave anything holding the verify port.
  fuser -k -TERM "$port"/tcp 2>/dev/null || true

  if [ "$ok" != "0" ]; then
    echo "---- candidate release output ----" >&2
    tail -20 "$log" >&2 || true
    rm -f "$log" || true
    die "candidate release $rel did not serve HTTP on :$port — NOT switching the live symlink"
  fi
  rm -f "$log" || true
  echo ">> candidate release verified (BUILD_ID=$bid) — safe to switch"
}
# ---- the capability contract -----------------------------------------------------------------------------
#
# WHY THIS EXISTS, precisely. Next.js substitutes NEXT_PUBLIC_* at BUILD time. A flag that is absent when
# `next build` runs is not substituted at all: the compiled client keeps a property access on an env shim that
# is empty in a browser, so `"1" === process.env.NEXT_PUBLIC_PHASE2_ADMIN` is permanently false. The routes
# still compile and still answer by URL. Nothing 404s, nothing crashes, every structural check passes -- and
# the operator loses the navigation to Internet Packages, Service Plans and PMS Connection.
#
# That is not a hypothesis. The bundle serving the PRE-LIVE appliance was proven by hash to be byte-identical
# to a local build made with no flags, and different from the same source built with them.
#
# So the contract is DATA, the build is given the flags from it, and the result is then PROVEN -- because
# supplying a variable and having it inlined are two different facts, and only the second one reaches a guest.
contract_file() {
  local src="${1:-.}"
  echo "${HOTEL_ADMIN_CONTRACT:-$src/capability-contract.json}"
}

# contract_flags <contract> — prints "NAME=VALUE" per required flag.
contract_flags() {
  python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
sys.stdout.reconfigure(newline=chr(10))
for k,v in d["required_build_flags"].items():
    if not k.startswith("_"): print("%s=%s" % (k,v))' "$1"
}

# assert_contract_satisfied <release-dir> <contract> — every check that can be made against a BUILT tree.
# Used at package time and again at install time, on the appliance, against the artefact that actually shipped.
assert_contract_satisfied() {
  local rel="$1" contract="$2"
  [ -f "$contract" ] || die "capability contract not found: $contract"

  # (1) REQUIRED ROUTES. A build that cannot serve the operator surfaces is not a candidate for this appliance.
  local missing
  missing="$(python3 -c 'import json,sys
rel, contract = sys.argv[1], sys.argv[2]
want = json.load(open(contract, encoding="utf-8"))["required_routes"]["routes"]
try:
    m = json.load(open(rel + "/.next/app-path-routes-manifest.json", encoding="utf-8"))
except Exception as exc:
    print("MANIFEST_UNREADABLE:%s" % exc); raise SystemExit(0)
have = set(v for v in m.values() if isinstance(v, str))
print(" ".join(r for r in want if r not in have))' "$rel" "$contract")"
  case "$missing" in
    MANIFEST_UNREADABLE:*) die "release $rel has no readable app route manifest (${missing#MANIFEST_UNREADABLE:})" ;;
  esac
  [ -z "$missing" ] || die "release $rel is missing required operator route(s): $missing"

  # (2) THE FINGERPRINT OF A FLAGLESS BUILD. A residual runtime lookup in a CLIENT chunk means the flag was
  #     never substituted, whatever the build environment claimed. This is the check that would have caught
  #     the bundle now serving the appliance.
  # A LOOP FED BY A PRODUCER THAT RETURNED NOTHING IS NOT A PASSING CHECK.
  #
  # This is not a hypothetical either: while this file was being written, an emitter acquired a syntax error,
  # produced nothing, and every check below it "passed" on a bundle that was demonstrably built without its
  # flags -- and the manifest recorded build_flags:{} while the script reported the contract satisfied. So the
  # producer's output is MATERIALISED and asserted non-empty before anything iterates over it.
  local pats pat hits
  pats="$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
sys.stdout.reconfigure(newline=chr(10))
for p in d["forbidden_in_client_bundle"]["patterns"]: print(p)' "$contract")"
  [ -n "$pats" ] || die "the capability contract yielded no forbidden-pattern list; the flag-inlining check cannot run and must not be reported as passed"
  while IFS= read -r pat; do
    pat="${pat%$CR}"   # a packaging host may be Windows; a trailing CR would poison every comparison below
    [ -n "$pat" ] || continue
    hits="$(grep -rl -- "$pat" "$rel/.next/static" 2>/dev/null | head -3 || true)"
    if [ -n "$hits" ]; then
      echo "$hits" | sed 's/^/    /' >&2
      die "release $rel was built WITHOUT its capability flags: the client bundle still resolves '$pat' at runtime, so the navigation it gates is compiled OFF"
    fi
  done <<< "$pats"

  # (3) THE NAVIGATION ITSELF, entry by entry, named in the failure so an operator reads a surface and not a
  #     variable. Each entry needs its route present; (2) has already proved its flag was inlined.
  local navs label route
  navs="$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
sys.stdout.reconfigure(newline=chr(10))
for e in d["required_navigation"]["entries"]: print("%s|%s" % (e["label"], e["route"]))' "$contract")"
  [ -n "$navs" ] || die "the capability contract yielded no required navigation; the operator-surface check cannot run and must not be reported as passed"
  while IFS='|' read -r label route; do
    route="${route%$CR}"
    [ -n "$route" ] || continue
    # THE ROUTE TRAVELS IN THE ENVIRONMENT, NOT IN ARGV. On a Git-Bash packaging host, an argument that begins
    # with "/" is rewritten into a Windows path before a native python ever sees it: "/internet-packages"
    # arrives as "C:/Program Files/Git/internet-packages" and every route lookup fails on a bundle that
    # contains every route. A verification step that fails for a reason unrelated to what it verifies is worse
    # than no step at all, because the next person deletes it.
    # THE LEADING SLASH IS REMOVED IN TRANSIT AND PUT BACK INSIDE PYTHON. On a Git-Bash packaging host, MSYS
    # rewrites anything that looks like a POSIX absolute path -- in argv AND in environment values -- before a
    # native python ever sees it, so "/internet-packages" arrives as "C:/Program Files/Git/internet-packages"
    # and every route lookup fails on a bundle that contains every route. A verification step that fails for a
    # reason unrelated to what it verifies is worse than no step at all, because the next person deletes it.
    SC_REL="$rel" SC_ROUTE="${route#/}" python3 -c 'import json, os
m = json.load(open(os.environ["SC_REL"] + "/.next/app-path-routes-manifest.json", encoding="utf-8"))
want = "/" + os.environ["SC_ROUTE"]
raise SystemExit(0 if want in set(v for v in m.values() if isinstance(v, str)) else 1)'       || die "release $rel cannot serve '$label' ($route) — refusing to make it live"
  done <<< "$navs"

  echo ">> capability contract satisfied (routes, inlined flags, operator navigation)"
}

# assert_manifest <release-dir> — the bundle must be able to prove what it is.
assert_manifest() {
  local rel="$1" mf="$1/$MANIFEST_NAME"
  [ -f "$mf" ] || die "release $rel carries no $MANIFEST_NAME — it cannot prove its provenance and will not be served"
  local commit bid
  commit="$(jget "$mf" 'd["source_commit"]' | tr -d '')"
  bid="$(jget "$mf" 'd["build_id"]')"
  [ -n "$commit" ] || die "$mf records no source_commit"
  [ -n "$bid" ]    || die "$mf records no build_id"
  [ "$bid" = "$(cat "$rel/.next/BUILD_ID")" ]     || die "$mf says BUILD_ID=$bid but the release contains $(cat "$rel/.next/BUILD_ID") — the manifest does not describe this bundle"
  echo ">> manifest verified: commit=${commit:0:12} BUILD_ID=$bid"
}

# smoke_live <expected-build-id> <release-dir> — PROVE the live endpoint is serving THIS bundle.
#
# `systemctl is-active` and HTTP 200 are both satisfied by the wrong bundle, by the previous bundle, and by a
# bundle whose operator navigation is compiled off. What distinguishes them is the BUILD_ID Next stamps into
# every rendered document, and whether the surfaces the contract requires actually answer. Both are checked
# here, through the port the service really listens on, after the switch.
smoke_live() {
  local want_bid="$1" rel="$2"
  local port="${HOTEL_ADMIN_PORT:-3100}" base="http://127.0.0.1:${HOTEL_ADMIN_PORT:-3100}"
  local body; body="$(curl -sS --max-time 5 "$base/login" 2>/dev/null || true)"
  [ -n "$body" ] || { echo "SMOKE FAIL: $base/login returned nothing" >&2; return 1; }

  # Next writes the build id into the served document as an HTML comment. This is the identity check that
  # "HTTP 200" cannot make.
  # sed, NOT `tr -d "<!->"`. That set is a RANGE: "!" to ">" spans ASCII 33-62, which includes every digit,
  # so a BUILD_ID came back with its digits silently removed and the smoke test reported a mismatch against
  # the release it had just installed. A verification step that fails for a reason unrelated to what it
  # verifies is worse than no step at all.
  local served; served="$(grep -o "<!--[A-Za-z0-9_-]\{16,\}-->" <<<"$body" | head -1 | sed 's/^<!--//; s/-->$//')"
  if [ -n "$served" ] && [ "$served" != "$want_bid" ]; then
    echo "SMOKE FAIL: the live endpoint is serving BUILD_ID '$served' but the release we installed is '$want_bid'" >&2
    return 1
  fi
  echo ">> smoke: live endpoint serves BUILD_ID ${served:-$want_bid}"

  # Every required operator surface must ANSWER. Unauthenticated these redirect to /login, which is a correct
  # serving response; what must never happen is a 404, which is what a bundle missing the route returns.
  local route code bad=0
  while IFS= read -r route; do
    route="${route%$CR}"
    [ -n "$route" ] || continue
    code="$(curl -sS -o /dev/null -w "%{http_code}" --max-time 5 "$base$route" 2>/dev/null || echo 000)"
    case "$code" in
      200|302|303|307|308) echo "   ok  $route -> $code" ;;
      *) echo "   BAD $route -> $code" >&2; bad=1 ;;
    esac
  done < <(jget "$rel/$MANIFEST_NAME" 'd["required_routes"]' | python3 -c 'import json,sys
sys.stdout.reconfigure(newline=chr(10))
try: [print(r) for r in json.load(sys.stdin)]
except Exception: pass' | tr -d '')
  [ "$bad" = "0" ] || { echo "SMOKE FAIL: a required operator surface is not being served" >&2; return 1; }

  # And the navigation those surfaces hang off must be COMPILED IN, not merely routable. This re-reads the
  # live tree because the whole defect was a bundle whose routes worked and whose navigation was gone.
  assert_contract_satisfied "$rel" "${HOTEL_ADMIN_CONTRACT:-$rel/capability-contract.json}" >/dev/null     || { echo "SMOKE FAIL: the live release does not satisfy the capability contract" >&2; return 1; }
  echo ">> smoke: required operator surfaces served and their navigation is compiled in"
  return 0
}

# rollback_eligible <release-dir> — may this release be SERVED if the current one is withdrawn?
#
# An obsolete UI is not a rollback. Returning the appliance to a build that predates PMS, Service Plan and
# Package management would take those surfaces away from the operator at the worst possible moment, which is
# the moment something else has already gone wrong. Such a release is archive evidence and nothing more.
rollback_eligible() {
  local rel="$1"
  [ -d "$rel" ] || return 1
  [ -f "$rel/$MANIFEST_NAME" ] || return 1
  assert_contract_satisfied "$rel" "${HOTEL_ADMIN_CONTRACT:-$rel/capability-contract.json}" >/dev/null 2>&1
}

guard_not_root_cwd() {
  case "$(pwd)" in
    /root|/root/*) die "refusing to run from /root ($(pwd)); use a project checkout" ;;
  esac
}

# ---- package (workstation / CI) --------------------------------------------
# Builds the standalone bundle and produces a single tarball to ship.
package() {
  guard_not_root_cwd
  local src="${1:-.}"
  [ -f "$src/package.json" ] || die "no package.json in $src (run from the hotel-admin dir or pass its path)"
  ( cd "$src"
    # ---- PROVENANCE FIRST. A bundle that cannot say which commit built it can never be verified later, and
    # "which commit is the UI at" then becomes a question answered from memory. Packaging fails rather than
    # shipping an anonymous artefact.
    local commit dirty="clean"
    commit="$(git rev-parse HEAD 2>/dev/null || true)"
    [ -n "$commit" ] || die "cannot identify the source commit (not a git checkout?) — refusing to package an artefact that cannot prove its provenance"
    if [ -n "$(git status --porcelain -- . 2>/dev/null)" ]; then
      dirty="dirty"
      [ "${HOTEL_ADMIN_ALLOW_DIRTY:-0}" = "1" ]         || die "the hotel-admin tree has uncommitted changes; the bundle would not correspond to $commit. Commit them, or set HOTEL_ADMIN_ALLOW_DIRTY=1 to record the build as dirty on purpose"
    fi

    # ---- THE CAPABILITY CONTRACT SUPPLIES THE BUILD ENVIRONMENT. Never a bare `npm run build`: that is the
    # exact command that produced the flagless bundle now serving the appliance, and it succeeded quietly.
    local contract; contract="$(contract_file .)"
    [ -f "$contract" ] || die "no capability contract at $contract — refusing to build an unconstrained UI"
    local flaglist flagline flags_json=""
    flaglist="$(contract_flags "$contract")"
    [ -n "$flaglist" ] || die "the capability contract yielded no build flags; a build would silently produce a reduced UI"
    while IFS= read -r flagline; do
      flagline="${flagline%$CR}"
      [ -n "$flagline" ] || continue
      export "${flagline?}"
      flags_json="$flags_json${flags_json:+, }\"${flagline%%=*}\": \"${flagline#*=}\""
      echo ">> build flag ${flagline}"
    done <<< "$flaglist"

    echo ">> npm ci"
    npm ci --no-fund --no-audit
    echo ">> next build (standalone, under the capability contract)"
    npm run build
    [ -f .next/standalone/server.js ] || die "standalone build missing — is output:'standalone' set in next.config?"
    # Assemble the runnable tree: standalone server + static assets + public.
    rm -rf .deploy && mkdir -p .deploy/app
    cp -a .next/standalone/. .deploy/app/
    mkdir -p .deploy/app/.next
    cp -a .next/static .deploy/app/.next/static
    [ -d public ] && cp -a public .deploy/app/public || true
    # The contract travels WITH the bundle, so the appliance verifies against the same rules the build was
    # held to instead of against whatever happens to be on the deployment host.
    cp -a "$contract" .deploy/app/capability-contract.json
    printf '%s\n' "$(date -u +%Y%m%dT%H%M%SZ)" > .deploy/app/RELEASE_STAMP

    # ---- the omission this build step is most likely to make, asserted -----
    # `cp -a .next/standalone/* dst` silently drops the HIDDEN .next directory
    # inside the standalone tree, because the glob does not match dotfiles. The
    # bundle then looks complete -- server.js is there, static/ is there -- and
    # Next dies at runtime with "Could not find a production build", which is
    # what crash-looped this appliance. The trailing-dot form above copies
    # dotfiles; these assertions make a regression LOUD at package time instead
    # of at 3am on the appliance, and they are cheap enough to always run.
    [ -f .deploy/app/.next/BUILD_ID ] || die "packaged bundle has no .next/BUILD_ID — the hidden .next dir was not copied (use 'cp -a .next/standalone/.', not '/*')"
    [ -d .deploy/app/.next/static ]   || die "packaged bundle has no .next/static"
    [ -f .deploy/app/server.js ]      || die "packaged bundle has no server.js"
    local src_id dst_id
    src_id="$(cat .next/BUILD_ID)"; dst_id="$(cat .deploy/app/.next/BUILD_ID)"
    [ "$src_id" = "$dst_id" ] || die "packaged BUILD_ID ($dst_id) != built BUILD_ID ($src_id)"
    echo ">> bundle verified: BUILD_ID=$dst_id"

    # ---- THE CONTRACT, PROVEN ON THE BUILT TREE. Everything above proves the bundle is STRUCTURALLY whole;
    # this proves it is the RIGHT bundle -- that the flags were actually substituted and the operator surfaces
    # are reachable. A build that fails here never becomes a tarball.
    assert_contract_satisfied .deploy/app "$contract"

    # ---- THE MANIFEST. Written last, so it describes what was actually produced.
    python3 -c 'import json, sys, os
rel, commit, dirty, bid, stamp, flags, contract, name = sys.argv[1:9]
c = json.load(open(contract, encoding="utf-8"))
mf = {
  "_note": ("Machine-readable identity of this Hotel Admin bundle. Installation refuses any release that "
            "cannot present one, and the appliance guard compares the RUNNING UI against it. A backend "
            "deployment that does not rebuild this UI leaves this file untouched, which is the point: the UI "
            "has its own identity and never inherits the identity of the service binaries."),
  "source_commit": commit,
  "source_state": dirty,
  "build_id": bid,
  "released_at": stamp,
  "contract_version": c.get("contract_version"),
  "build_flags": json.loads(flags),
  "required_routes": c["required_routes"]["routes"],
  "required_navigation": c["required_navigation"]["entries"],
}
open(os.path.join(rel, name), "w", encoding="utf-8").write(json.dumps(mf, indent=2) + "\n")
print(">> manifest written: %s" % name)' \
      .deploy/app "$commit" "$dirty" "$dst_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "{$flags_json}" "$contract" "$MANIFEST_NAME"

    tar czf hotel-admin-deploy.tgz -C .deploy/app .
    # Verify the TARBALL too, not just the staging dir: tar has its own ways to
    # lose a path, and the tarball is the only artefact that actually ships.
    # MATERIALISE THE LISTING ONCE, for the same reason install() does it below: under `set -o pipefail`,
    # `tar tzf ... | grep -q PATTERN` reports a PIPELINE failure when grep matches early and exits, because
    # the closed pipe kills tar with SIGPIPE. ./.next/static/ sits near the front of this archive, so the
    # check failed on a bundle that was demonstrably complete and blocked the deployment.
    local listing; listing="$(tar tzf hotel-admin-deploy.tgz)"
    grep -q '^\./\.next/BUILD_ID$' <<<"$listing" || die "tarball is missing ./.next/BUILD_ID"
    grep -q '^\./\.next/static/'   <<<"$listing" || die "tarball is missing ./.next/static/"
    grep -q "^\./$MANIFEST_NAME\$" <<<"$listing" || die "tarball is missing ./$MANIFEST_NAME"
    grep -q '^\./capability-contract\.json$' <<<"$listing" || die "tarball is missing ./capability-contract.json"

    # ARTEFACT CHECKSUM. Of the tarball, because that is the thing that travels; recorded beside it and
    # re-derived on the appliance before anything is extracted.
    local sum; sum="$(sha256sum hotel-admin-deploy.tgz | cut -d" " -f1)"
    printf '%s  %s\n' "$sum" "hotel-admin-deploy.tgz" > hotel-admin-deploy.tgz.sha256
    echo ">> wrote $(pwd)/hotel-admin-deploy.tgz"
    echo "   commit=${commit:0:12} ($dirty)  BUILD_ID=$dst_id  sha256=$sum"
  )
}

# ---- install (appliance, root) ---------------------------------------------
install() {
  local tarball="${1:?usage: install <tarball>}"
  [ -f "$tarball" ] || die "tarball not found: $tarball"
  [ "$(id -u)" = "0" ] || die "install must run as root"

  # Reject a malformed bundle before anything is extracted. ./.next/BUILD_ID is
  # the specific path that goes missing when the hidden .next dir is dropped.
  #
  # The listing is materialised ONCE instead of being piped into three `grep -q`s.
  #
  # `tar tzf ... | grep -q PATTERN` is not the harmless idiom it looks like under `set -o pipefail`. grep -q
  # exits the instant it matches, the write end of the pipe closes, tar dies of SIGPIPE, and pipefail reports
  # the PIPELINE as failed -- so a check that SUCCEEDED takes the `|| die` branch. Whether it bites depends on
  # WHERE the match sits in the listing: ./server.js is near the end, so grep drains the stream and tar exits
  # cleanly, while ./.next/BUILD_ID is the sixth entry, so grep leaves almost the whole archive unread. That is
  # why this bundle was rejected with "the hidden .next dir is missing" while the file was demonstrably there.
  # ARTEFACT INTEGRITY, before a single byte is extracted. If the packager wrote a checksum, the thing that
  # arrived must be the thing that was built and verified.
  if [ -f "$tarball.sha256" ]; then
    local want got
    want="$(cut -d" " -f1 < "$tarball.sha256")"
    got="$(sha256sum "$tarball" | cut -d" " -f1)"
    [ "$want" = "$got" ] || die "artifact checksum mismatch: expected $want, got $got — this is not the bundle that was verified at package time"
    echo ">> artifact checksum verified: $got"
  fi

  local listing; listing="$(tar tzf "$tarball")"
  grep -q '^\./server.js$'       <<<"$listing" || die "tarball does not look like a standalone bundle (no ./server.js)"
  grep -q '^\./\.next/BUILD_ID$' <<<"$listing" || die "tarball has no ./.next/BUILD_ID — the hidden .next dir is missing; repackage with 'cp -a .next/standalone/.'"
  grep -q '^\./\.next/static/'   <<<"$listing" || die "tarball has no ./.next/static/"

  local stamp; stamp="$(date -u +%Y%m%d-%H%M%S)"
  local rel="$RELEASES_DIR/$stamp"
  mkdir -p "$rel"
  tar xzf "$tarball" -C "$rel"
  chown -R "$RUN_USER":"$RUN_USER" "$rel" 2>/dev/null || true

  # FULL verification of the candidate while the live symlink still points at
  # the old release. Anything wrong here aborts with the running site untouched.
  verify_release "$rel"
  # ...and the two questions structure alone cannot answer: WHAT is this, and does it carry the operator
  # surfaces this appliance requires. Both are checked here, on the appliance, against the artefact that
  # actually arrived -- not against what the build host believed it had sent.
  assert_manifest "$rel"
  # The bundle's own contract by default; an appliance may PIN a stricter one through HOTEL_ADMIN_CONTRACT, in
  # which case the candidate is held to the appliance's rules rather than to its own.
  assert_contract_satisfied "$rel" "${HOTEL_ADMIN_CONTRACT:-$rel/capability-contract.json}"

  local cand_commit cand_bid
  cand_commit="$(jget "$rel/$MANIFEST_NAME" 'd["source_commit"]')"
  cand_bid="$(jget "$rel/$MANIFEST_NAME" 'd["build_id"]')"
  # An EXPECTED commit may be pinned by the caller. A deployment that silently installs something other than
  # what was asked for is the failure mode this whole file exists to prevent.
  if [ -n "${HOTEL_ADMIN_EXPECT_COMMIT:-}" ]; then
    case "$cand_commit" in
      "$HOTEL_ADMIN_EXPECT_COMMIT"*) : ;;
      *) die "candidate was built from $cand_commit but $HOTEL_ADMIN_EXPECT_COMMIT was expected — refusing to switch" ;;
    esac
  fi

  # Record the outgoing release as "previous" BEFORE flipping, so a later
  # rollback returns to exactly this release regardless of mtimes.
  #
  # ...AND ONLY IF IT IS A LEGITIMATE ROLLBACK TARGET. A previous pointer is a loaded gun aimed at production:
  # whatever it references is what an operator gets under pressure. Wiring an obsolete UI there means a
  # rollback silently removes PMS, Service Plan and Package management. If the outgoing release cannot satisfy
  # today's contract it is archived as evidence and the pointer is CLEARED rather than left lying.
  local outgoing=""
  if [ -L "$CURRENT_LINK" ]; then
    outgoing="$(readlink -f "$CURRENT_LINK")"
  elif [ -d "$CURRENT_LINK" ] && [ ! -L "$CURRENT_LINK" ]; then
    # A plain DIRECTORY at the runtime path means someone deployed by copying instead of by switching a
    # release. It is not a release, it has no provenance, and it must not become "previous".
    echo ">> WARN: $CURRENT_LINK is a plain directory, not a release symlink — it will be archived, not made rollback-able" >&2
    mkdir -p "$ARCHIVE_DIR"
    mv -T "$CURRENT_LINK" "$ARCHIVE_DIR/unmanaged-$(date -u +%Y%m%d-%H%M%S)"
  fi
  if [ -n "$outgoing" ] && [ "$outgoing" != "$rel" ]; then
    if rollback_eligible "$outgoing"; then
      atomic_link "$outgoing" "$PREVIOUS_LINK"
      echo ">> previous release recorded: $outgoing"
    else
      echo ">> WARN: outgoing release $outgoing does not satisfy the current capability contract;" >&2
      echo "   it is retained as ARCHIVE EVIDENCE and is NOT wired as an executable rollback." >&2
      rm -f "$PREVIOUS_LINK"
    fi
  fi
  # AND AN INHERITED POINTER IS AUDITED TOO. The branch above only reaches a previous pointer we ourselves are
  # about to set. A pointer left by an earlier deployment - or by an install-by-copying that never touched it -
  # survives untouched, and on this appliance that meant an obsolete release stayed wired as the rollback
  # through the very deployment that was fixing the problem.
  if [ -L "$PREVIOUS_LINK" ] && ! rollback_eligible "$(readlink -f "$PREVIOUS_LINK")"; then
    echo ">> WARN: the inherited previous-release pointer references $(readlink -f "$PREVIOUS_LINK")," >&2
    echo "   which does not satisfy the current capability contract. Clearing it: that release remains on disk" >&2
    echo "   as archive evidence and is no longer reachable as an executable rollback." >&2
    rm -f "$PREVIOUS_LINK"
  fi

  # Atomic flip of the stable path to the new release.
  atomic_link "$rel" "$CURRENT_LINK"

  wait_healthy || { echo "service failed to start; rolling back"; rollback; die "hotel-admin failed to start"; }

  # THE PROOF, after the switch, against the endpoint a browser actually reaches. Everything before this
  # verified a directory; this verifies the SERVICE. If it fails, the deployment is not "mostly fine": the
  # appliance is serving something other than what was verified, and we go back.
  if ! smoke_live "$cand_bid" "$rel"; then
    echo ">> smoke test FAILED — restoring the previous release" >&2
    if [ -L "$PREVIOUS_LINK" ]; then rollback || true; fi
    die "hotel-admin smoke test failed for release $stamp (commit ${cand_commit:0:12}); the live symlink has been returned to the previous verified release"
  fi

  echo ">> deployed release $stamp -> $CURRENT_LINK"
  echo "   commit=${cand_commit:0:12}  BUILD_ID=$cand_bid"
  # Post-deploy retention: hand off to the centralized, fail-safe cleanup tool so
  # ALL artifact types (releases, binaries, config, DB) are pruned by one policy
  # that never touches current/previous/PKI/pinned material. The daily timer is
  # the safety net if this deploy path is not used.
  if [ -x /opt/stayconnect/bin/stayconnect-backup-cleanup ]; then
    /opt/stayconnect/bin/stayconnect-backup-cleanup --apply || echo ">> WARN: backup cleanup reported issues (see /var/log/stayconnect/backup-cleanup.log)"
  fi
}

# ---- rollback (appliance, root) --------------------------------------------
rollback() {
  [ "$(id -u)" = "0" ] || die "rollback must run as root"
  local prev; prev="$(readlink -f "$PREVIOUS_LINK" 2>/dev/null || true)"
  [ -n "$prev" ] && [ -d "$prev" ] || die "no previous release recorded to roll back to"
  local cur; cur="$(readlink -f "$CURRENT_LINK" 2>/dev/null || true)"
  [ "$prev" != "$cur" ] || die "previous release equals current ($prev); nothing to roll back to"
  # THE ROLLBACK TARGET IS HELD TO THE SAME CONTRACT AS A DEPLOYMENT. Going backwards is not permission to
  # serve a UI this appliance has outgrown.
  rollback_eligible "$prev"     || die "previous release $prev does not satisfy the current Hotel Admin capability contract — it is archive evidence, not an executable rollback. Deploy a compatible release instead."
  # Swap: current becomes previous, and we flip to the recorded previous.
  [ -n "$cur" ] && atomic_link "$cur" "$PREVIOUS_LINK"
  atomic_link "$prev" "$CURRENT_LINK"
  wait_healthy || die "hotel-admin failed to start after rollback"
  echo ">> rolled back to $prev"
}

case "${1:-}" in
  package) shift; package "${1:-.}" ;;
  install) shift; install "${1:-}" ;;
  rollback) rollback ;;
  # verify [dir] — check a release tree (default: whatever is live) without
  # changing anything. Safe to run any time, including after a reboot.
  verify) shift; verify_release "$(readlink -f "${1:-$CURRENT_LINK}")" ;;
  *) echo "usage: $0 {package [srcdir] | install <tarball> | rollback | verify [releasedir]}"; exit 2 ;;
esac
