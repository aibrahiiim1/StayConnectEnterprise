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
BIN="${SC_BIN_DIR:-/opt/stayconnect/bin}"
# A prefix for absolute Exec paths that live OUTSIDE $BIN (/usr/local/sbin/..., /opt/stayconnect/deploy/...).
# Empty in production, where those paths are literal. The self-test sets it so it can exercise the real
# install-and-verify code against a sandbox instead of a parallel copy of the logic that could drift.
ROOT_PREFIX="${SC_ROOT_PREFIX:-}"
UNITS="${SC_UNIT_DIR:-/etc/systemd/system}"
# SC_SKIP_SYSTEMD lets the self-test drive the file-installation and verification logic on a machine with no
# systemd and no /opt. It skips ONLY the daemon-reload; every existence and executability check still runs,
# because those are the checks that failed to exist.
SKIP_SYSTEMD="${SC_SKIP_SYSTEMD:-0}"
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

# ---- 2. HELPERS, DERIVED FROM THE UNITS THAT NAME THEM ----------------------
#
# This list used to be hand-written, and stayconnect-backup-cleanup.service was not on it. Its
# ExecStart=/opt/stayconnect/bin/stayconnect-backup-cleanup was never installed by anything, so the daily
# retention timer failed with status=203/EXEC every night, silently, for weeks. Backup binaries and release
# directories accumulated until the appliance root filesystem hit 100%, PostgreSQL could not write
# postmaster.pid, and the site went down.
#
# So the list is no longer written by hand. Every /opt/stayconnect/bin path that ANY unit being installed
# names is extracted from the unit files themselves and installed here. A new unit that references a new
# helper installs it automatically; a unit that references a helper with no source REFUSES THE WHOLE INSTALL
# rather than shipping a timer that cannot run.
#
# The source file may be named with or without .sh: the units name the installed path, and the repository
# names the script. Both spellings are tried, in that order, so neither convention has to change.
# unit_exec_paths — every absolute program path any shipped unit declares, ignoring distribution programs.
# Used to VERIFY; helper INSTALLATION still targets $BIN, with non-$BIN destinations installed explicitly by
# install_external_helpers below so a path outside /opt/stayconnect/bin can never be silently skipped again.
unit_exec_paths() {
  grep -hoE '^Exec[A-Za-z]*=-?/[^ ]+' "$SRC"/systemd/stayconnect-*.service 2>/dev/null |
    sed -E 's/^Exec[A-Za-z]*=-?//' |
    grep -vE '^/(bin|sbin|usr/bin|usr/sbin|usr/local/bin/node)/' | sort -u
}

# ---- THE EXTERNAL-HELPER CONTRACT ------------------------------------------------------------------------
#
# A BASENAME MATCH IS NOT AUTHORITY TO MAKE A FILE EXECUTABLE ON AN APPLIANCE.
#
# The first version of this walked every absolute Exec path a unit declared and installed any repository
# script whose basename happened to match. That is how a RETIRED unit could be re-armed: stayconnect-tc-setup
# named /opt/stayconnect/deploy/scripts/tc-setup.sh, the repository still shipped a tc-setup.sh, and the
# installer would have written it out - turning a dormant, harmless failure into a script that runs and whose
# first act is `tc qdisc del dev <IF> root`. It would not have STARTED it; it did not need to, because
# stayconnect-acctd.service carried Wants=stayconnect-tc-setup.service and would pull it in on its next start.
#
# So the set of helpers that may be placed outside /opt/stayconnect/bin is ENUMERATED here, deliberately, one
# line per helper, naming the unit that needs it and the exact destination. Adding a helper is a visible,
# reviewable change to this list rather than a side effect of a filename.
#
# Format: <unit-basename>|<absolute destination>|<source under deploy/scripts>
EXTERNAL_HELPER_CONTRACT="stayconnect-hotel-admin-cert-renew|/usr/local/sbin/stayconnect-hotel-admin-cert-manager|hotel-admin-cert-manager.sh"

contract_dest_for() { # contract_dest_for <absolute-path> -> source filename, or nothing
  local path="$1" line
  printf '%s
' "$EXTERNAL_HELPER_CONTRACT" | while IFS='|' read -r _unit dest src; do
    [ -n "${dest:-}" ] || continue
    [ "$dest" = "$path" ] && { printf '%s
' "$src"; break; }
  done
}

# install_external_helpers — ONLY the helpers the contract above names.
install_external_helpers() {
  local line unit dest src
  printf '%s
' "$EXTERNAL_HELPER_CONTRACT" | while IFS='|' read -r unit dest src; do
    [ -n "${dest:-}" ] && [ -n "${src:-}" ] || continue
    # A contract entry for a unit this tree no longer ships is skipped, not installed: retiring a unit must
    # not leave its helper behind, and must not fail the install either.
    [ -f "$SRC/systemd/$unit.service" ] || { say "note: $unit is not shipped here; its helper is not installed"; continue; }
    [ -f "$SRC/scripts/$src" ] || die "the helper contract names $src for $unit, but $SRC/scripts/$src does not exist"
    local target="$ROOT_PREFIX$dest"
    mkdir -p "$(dirname "$target")"
    install -m 0755 "$SRC/scripts/$src" "$target.tmp" && mv -f "$target.tmp" "$target"
    say "installed external helper $src -> $target (contract: $unit)"
  done
}

unit_helpers() {
  # Every Exec* directive across the units in SRC, reduced to the bin paths they reference.
  grep -hoE '^Exec[A-Za-z]*=[^ ]*/opt/stayconnect/bin/[A-Za-z0-9._-]+' "$SRC"/systemd/stayconnect-*.service 2>/dev/null |
    sed 's#.*/opt/stayconnect/bin/##' | sort -u
}

HELPERS="$(unit_helpers)"
[ -n "$HELPERS" ] || die "no unit references a helper under /opt/stayconnect/bin; the units in $SRC/systemd look wrong"

# Copy to a temp name and mv, so a running ExecStartPre never reads a half-written script.
for want in $HELPERS; do
  srcfile=""
  for cand in "$want" "$want.sh"; do
    [ -f "$SRC/scripts/$cand" ] && { srcfile="$SRC/scripts/$cand"; break; }
  done
  if [ -z "$srcfile" ]; then
    # No repository script by that name, so this is a SERVICE BINARY (scd, netd, acctd, pmsd, portald,
    # edged, svc-run...). Those are produced by the build and are already on the appliance before this runs
    # -- that is the provisioning contract. This step does not install them; the verification pass below
    # refuses to finish if one of them is missing anyway, which is the same protection by a different route.
    continue
  fi
  install -m 0755 "$srcfile" "$BIN/.$want.tmp" || die "cannot write $BIN/.$want.tmp"
  mv -f "$BIN/.$want.tmp" "$BIN/$want" || die "cannot install $BIN/$want"
  say "installed $BIN/$want (from $(basename "$srcfile"))"
done

# ...and the helpers whose units name a path OUTSIDE $BIN. This call is the whole difference between a
# certificate manager that renews nightly and a timer that has failed 203/EXEC every night for two weeks.
install_external_helpers

# ---- 3. VERIFY BEFORE INSTALLING ANY UNIT ----------------------------------
#
# This runs BEFORE the units are copied, on purpose, and it is the same ordering principle as the config gate
# at the top: refuse while it is still just files. The check used to run at the END, so a unit whose helper
# was missing was installed and enabled first and the abort came afterwards -- leaving exactly the state this
# is meant to prevent, a timer systemd runs nightly and that fails 203/EXEC every time.
# EVERY unit, EVERY Exec directive. Not two units and not only ExecStartPre.
#
# The old check looked at ExecStartPre for stayconnect-scd and stayconnect-netd, took the FIRST path it found,
# and looked no further. stayconnect-backup-cleanup.service declares its helper on ExecStart, so nothing
# checked it, and the appliance ran for weeks with a timer whose executable did not exist. A check that only
# covers the units someone remembered is a check that will be wrong again the next time a unit is added.
#
# This reads the unit FILES rather than asking systemd, so it is exactly as valid on a machine with no
# systemd -- which is where the self-test runs.
# WHICH UNITS MUST BE HARD-FAILED, AND WHICH ARE MERELY REPORTED.
#
# A unit that systemd will actually RUN here and whose executable is missing is the outage: it fails 203/EXEC
# on every trigger, silently. A unit shipped for a host that is not this one -- stayconnect-ctrlapi belongs to
# the Central control plane and is deliberately disabled on an edge appliance -- names a binary this host has
# no reason to hold, and refusing the whole install over it would make the correct configuration
# uninstallable.
#
# So: enabled (directly, or through its timer, or currently running) is a hard failure; anything else is
# reported and does not stop the install. Under SC_SKIP_SYSTEMD every unit is treated as in service, which is
# the strict reading the self-test asserts against.
# A unit systemd does not know yet is IN SERVICE by default: that is a first install, where every unit being
# shipped is about to be enabled. Only an explicit "disabled" or "masked" from systemd downgrades a unit to
# report-only. Unknown must not mean lax, or the protection would evaporate on exactly the install that most
# needs it.
#
# SC_UNIT_ENABLED_CMD is the seam the self-test drives. It exists because the alternative is a test that
# depends on the enablement state of the machine it runs on, which is not a test.
ENABLED_CMD="${SC_UNIT_ENABLED_CMD:-systemctl is-enabled}"
in_service() { # in_service <unit-basename-without-suffix>
  local svc tmr
  svc="$($ENABLED_CMD "$1.service" 2>/dev/null)"
  tmr="$($ENABLED_CMD "$1.timer" 2>/dev/null)"
  case "$svc" in disabled|masked)
    case "$tmr" in enabled|enabled-runtime) return 0 ;; *) return 1 ;; esac ;;
  esac
  return 0
}

missing=0
for u in "$SRC"/systemd/stayconnect-*.service; do
  [ -f "$u" ] || continue
  b="$(basename "$u")"
  unit="${b%.service}"
  # EVERY ABSOLUTE Exec PATH, WHATEVER DIRECTORY IT NAMES.
  #
  # This scanned only /opt/stayconnect/bin, which is where most helpers live -- and that is precisely why it
  # could not see the one that broke. stayconnect-hotel-admin-cert-renew.service declares
  # ExecStart=/usr/local/sbin/stayconnect-hotel-admin-cert-manager; nothing ever installed that file, the
  # timer fired daily for two weeks, every firing failed 203/EXEC, the Hotel Admin certificate chain was never
  # re-minted, and HTTPS broke for every operator. The guard written to prevent exactly this outcome was
  # pinned to one directory and reported OK throughout.
  #
  # A guard against "the unit names a program that is not there" must not care WHERE the program was supposed
  # to be. Anything not under a system path we deliberately ignore (/bin, /usr/bin, /usr/sbin -- distribution
  # programs like /usr/bin/node) is checked at the path the unit actually declares.
  # not `local`: this loop runs at file scope, not inside a function
  contract_src=""
  for path in $(grep -hoE '^Exec[A-Za-z]*=-?/[^ ]+' "$u" 2>/dev/null |
                sed -E 's/^Exec[A-Za-z]*=-?//' |
                grep -vE '^/(bin|sbin|usr/bin|usr/sbin|usr/local/bin/node)/' | sort -u); do
    want="$(basename "$path")"
    # The unit's OWN path is what systemd will exec. A helper that lives somewhere else entirely is still
    # missing as far as that unit is concerned.
    case "$path" in
      /opt/stayconnect/bin/*) installed="$BIN/$want" ;;
      *)                      installed="$ROOT_PREFIX$path" ;;
    esac
    # The two ways this can be wrong read very differently to whoever has to fix it: a helper the repository
    # ships and this script failed to install, or a service binary the build was supposed to deliver.
    # WHAT IS THIS PATH, according to the contract rather than according to a filename?
    #
    # Three distinct answers, and they need three different behaviours. A helper the CONTRACT owns must be
    # present or the install is refused. A service binary the build delivers must be present for a unit that
    # is in service. Anything else is a path this installer does not own - and the presence of a similarly
    # named script in the repository does NOT make it ours.
    contract_src="$(contract_dest_for "$path")"
    if [ -n "$contract_src" ]; then
      origin="a helper this installer ships under the external-helper contract"
    else
      origin="a service binary the build delivers (provisioning expects binaries to be present already)"
    fi
    problem=""
    [ -f "$installed" ] || problem="does not exist"
    # A path that is neither contract-owned nor under $BIN belongs to something this installer does not
    # manage. It is reported so it cannot hide, but a unit this tree does not ship its helper for must not
    # abort an unrelated deployment - which is what a retired unit would otherwise do forever.
    case "$path" in
      /opt/stayconnect/bin/*) : ;;
      *) if [ -z "$contract_src" ]; then
           say "note: $b references $path, which no helper contract owns; not installed and not required here"
           continue
         fi ;;
    esac
    if [ -z "$problem" ] && [ ! -x "$installed" ]; then problem="is not executable"; fi
    if [ -z "$problem" ]; then
      say "$b -> $want OK"
    elif in_service "$unit"; then
      echo "[install-units] ABORT: $b is in service here and references $path, but $installed $problem" >&2
      echo "[install-units]        -- $origin. systemd would run it and it would fail 203/EXEC." >&2
      missing=1
    else
      say "note: $b references $path ($installed $problem) -- $origin, and this unit is NOT enabled here, so it is reported rather than refused"
    fi
  done
done
[ "$missing" = "0" ] || die "one or more units reference a helper that is missing or not executable; those services would fail with status=203/EXEC"

# ---- 4. units --------------------------------------------------------------
changed=0
for u in "$SRC"/systemd/stayconnect-*.service "$SRC"/systemd/stayconnect-*.timer; do
  [ -f "$u" ] || continue
  b="$(basename "$u")"
  if [ -f "$UNITS/$b" ] && cmp -s "$u" "$UNITS/$b"; then continue; fi
  install -m 0644 "$u" "$UNITS/$b" || die "cannot install $UNITS/$b"
  say "updated $b"
  changed=1
done

# ---- 5. reload -------------------------------------------------------------
if [ "$SKIP_SYSTEMD" = "1" ]; then
  say "SC_SKIP_SYSTEMD=1: not reloading systemd (units changed: $changed)"
else
  systemctl daemon-reload || die "daemon-reload failed"
  say "daemon-reload done (units changed: $changed)"
fi

say "install complete"
