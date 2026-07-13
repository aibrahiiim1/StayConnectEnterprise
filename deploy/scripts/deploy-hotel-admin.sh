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

wait_healthy() {
  systemctl daemon-reload
  systemctl restart "$SERVICE"
  sleep 2
  systemctl is-active --quiet "$SERVICE"
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
    tar czf hotel-admin-deploy.tgz -C .deploy/app .
    echo ">> wrote $(pwd)/hotel-admin-deploy.tgz"
  )
}

# ---- install (appliance, root) ---------------------------------------------
install() {
  local tarball="${1:?usage: install <tarball>}"
  [ -f "$tarball" ] || die "tarball not found: $tarball"
  [ "$(id -u)" = "0" ] || die "install must run as root"

  # A runnable bundle must contain server.js at its top level.
  tar tzf "$tarball" | grep -q '^\./server.js$' || die "tarball does not look like a standalone bundle (no ./server.js)"

  local stamp; stamp="$(date -u +%Y%m%d-%H%M%S)"
  local rel="$RELEASES_DIR/$stamp"
  mkdir -p "$rel"
  tar xzf "$tarball" -C "$rel"
  [ -f "$rel/server.js" ] || die "extracted release has no server.js"
  chown -R "$RUN_USER":"$RUN_USER" "$rel" 2>/dev/null || true

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
  *) echo "usage: $0 {package [srcdir] | install <tarball> | rollback}"; exit 2 ;;
esac
