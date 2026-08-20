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

die() { echo "ERROR: $*" >&2; exit 1; }

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
    echo ">> npm ci"
    npm ci --no-fund --no-audit
    echo ">> next build (standalone)"
    npm run build
    [ -f .next/standalone/server.js ] || die "standalone build missing — is output:'standalone' set in next.config?"
    # Assemble the runnable tree: standalone server + static assets + public.
    rm -rf .deploy && mkdir -p .deploy/app
    cp -a .next/standalone/. .deploy/app/
    mkdir -p .deploy/app/.next
    cp -a .next/static .deploy/app/.next/static
    [ -d public ] && cp -a public .deploy/app/public || true
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
    echo ">> wrote $(pwd)/hotel-admin-deploy.tgz (BUILD_ID=$dst_id)"
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

  # Record the outgoing release as "previous" BEFORE flipping, so a later
  # rollback returns to exactly this release regardless of mtimes.
  if [ -L "$CURRENT_LINK" ]; then
    local outgoing; outgoing="$(readlink -f "$CURRENT_LINK")"
    [ -n "$outgoing" ] && [ "$outgoing" != "$rel" ] && atomic_link "$outgoing" "$PREVIOUS_LINK"
  fi
  # Atomic flip of the stable path to the new release.
  atomic_link "$rel" "$CURRENT_LINK"

  wait_healthy || { echo "service failed to start; rolling back"; rollback; die "hotel-admin failed to start"; }

  echo ">> deployed release $stamp -> $CURRENT_LINK"
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
