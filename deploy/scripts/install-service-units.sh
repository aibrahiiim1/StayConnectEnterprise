#!/usr/bin/env bash
# Install the StayConnect systemd units and their startup gate scripts.
#
# The gate runs FIRST, before anything is copied. An incompatible PMS-dependent
# guest configuration is therefore rejected while it is still just a config
# file -- not after a reboot has already started a restart loop. That ordering
# is the point of this script; do not move the check below the install step.
#
# Run on the appliance:  install-service-units.sh [SRC_DIR]
set -uo pipefail

SRC="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BIN=/opt/stayconnect/bin
UNITS=/etc/systemd/system
ENVDIR="${SC_ENV_DIR:-/etc/stayconnect}"

say() { echo "[install-units] $*"; }
die() { echo "[install-units] ABORT: $*" >&2; exit 1; }

# ---- 1. GATE: refuse an invalid configuration before touching the system ----
GUARD="$SRC/scripts/check-phase6-guest-dependency.sh"
[ -x "$GUARD" ] || chmod +x "$GUARD" 2>/dev/null
if ! bash "$GUARD" "$ENVDIR"; then
  die "PMS-dependent guest configuration is invalid (see REFUSED above). Nothing was installed."
fi

# ...and the ENFORCEMENT PLANE, for the same reason and with worse symptoms. A Phase-3 guest surface enabled
# without the plane does not crash anything: the appliance authenticates guests, grants entitlements and
# sessions, tells each guest the connection failed, and enforces nothing. It looks healthy from every angle
# except the guest's.
PLANE="$SRC/scripts/check-phase3-enforcement-plane.sh"
[ -x "$PLANE" ] || chmod +x "$PLANE" 2>/dev/null
if ! bash "$PLANE" "$ENVDIR"; then
  die "the Phase-3 enforcement plane is not configured (see REFUSED above). Nothing was installed."
fi

# ---- 2. gate scripts, installed before the units that reference them --------
# A unit whose ExecStartPre points at a missing file fails to start, so these
# must land first. Copy to a temp name and mv, so a running ExecStartPre never
# reads a half-written script.
for s in check-phase6-guest-dependency.sh check-phase3-enforcement-plane.sh wait-for-site-db.sh; do
  [ -f "$SRC/scripts/$s" ] || die "missing $SRC/scripts/$s"
  install -m 0755 "$SRC/scripts/$s" "$BIN/.$s.tmp" || die "cannot write $BIN/.$s.tmp"
  mv -f "$BIN/.$s.tmp" "$BIN/$s" || die "cannot install $BIN/$s"
  say "installed $BIN/$s"
done

# ---- 3. units --------------------------------------------------------------
changed=0
for u in "$SRC"/systemd/stayconnect-*.service "$SRC"/systemd/stayconnect-*.timer; do
  [ -f "$u" ] || continue
  b="$(basename "$u")"
  if [ -f "$UNITS/$b" ] && cmp -s "$u" "$UNITS/$b"; then continue; fi
  install -m 0644 "$u" "$UNITS/$b" || die "cannot install $UNITS/$b"
  say "updated $b"
  changed=1
done

# ---- 4. reload + verify ----------------------------------------------------
systemctl daemon-reload || die "daemon-reload failed"
say "daemon-reload done (units changed: $changed)"

# systemd-analyze verify catches a typo'd directive or a missing ExecStartPre
# path. It warns about units we intentionally do not ship here, so its exit
# code is advisory -- the ExecStartPre existence check below is the hard one.
for u in stayconnect-scd stayconnect-netd; do
  pre="$(systemctl show "$u" -p ExecStartPre --value 2>/dev/null | grep -oE '/opt/stayconnect/bin/[A-Za-z0-9._-]+' | head -1)"
  if [ -n "$pre" ] && [ ! -x "$pre" ]; then
    die "$u declares ExecStartPre=$pre but that file is not executable -- the unit would fail to start"
  fi
  [ -n "$pre" ] && say "$u ExecStartPre=$pre OK"
done

say "install complete"
