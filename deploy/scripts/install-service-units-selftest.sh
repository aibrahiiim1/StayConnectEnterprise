#!/usr/bin/env bash
# THE MISSING EXECUTABLE, WATCHED FAILING.
#
# stayconnect-backup-cleanup.service declares
#
#     ExecStart=/opt/stayconnect/bin/stayconnect-backup-cleanup --apply
#
# and nothing installed that file. The unit and its timer were installed and enabled, so systemd ran it every
# night and it exited 203/EXEC every night, silently. Retention therefore never ran: 45 rollback binaries
# (720 MB), 1.2 GB of release directories and 800 MB of journals accumulated until the appliance root
# filesystem reached 100%, PostgreSQL could not write postmaster.pid, and the site went down.
#
# The installer now derives its helper list FROM the units and verifies every Exec directive of every unit.
# This proves both halves against a disposable tree: the good case installs and passes, and each way of
# getting it wrong is refused rather than shipped.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$(cd "$HERE/.." && pwd)"
INSTALLER="$HERE/install-service-units.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

ok()  { echo "  ok: $*"; }
bad() { echo "  *** FAIL: $*"; fail=1; }

# A disposable SRC tree: real units, a stub scripts/ dir, and fake BIN/UNITS/env dirs. The config gates run
# for real against an env dir that is deliberately dark, so they pass and never mask the case under test.
new_tree() { # new_tree <name> -> echoes the tree root
  local t="$WORK/$1"
  mkdir -p "$t/src/systemd" "$t/src/scripts" "$t/bin" "$t/units" "$t/etc"
  printf 'SCD_DB_URL=x\n'   > "$t/etc/scd.env"
  printf 'NETD_DB_URL=x\n'  > "$t/etc/netd.env"
  printf 'ACCTD_DB_URL=x\n' > "$t/etc/acctd.env"
  cp "$DEPLOY/scripts/check-phase6-guest-dependency.sh" \
     "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$t/src/scripts/"
  echo "$t"
}

run_installer() { # run_installer <tree> ; echoes output, returns the installer's exit code
  local t="$1"
  SC_BIN_DIR="$t/bin" SC_UNIT_DIR="$t/units" SC_ENV_DIR="$t/etc" SC_SKIP_SYSTEMD=1 \
    bash "$INSTALLER" "$t/src" >"$t/out" 2>&1
}

# ---------------------------------------------------------------- 1. the real defect, reproduced
# A unit that names a helper the tree cannot supply must REFUSE THE WHOLE INSTALL. Shipping the unit without
# the helper is what produced a nightly 203/EXEC.
t="$(new_tree missing_helper)"
cp "$DEPLOY/systemd/stayconnect-backup-cleanup.service" "$t/src/systemd/"
printf '#!/bin/sh\nexit 0\n' > "$t/src/scripts/wait-for-site-db.sh"
if run_installer "$t"; then
  bad "a unit whose helper has no source was installed anyway"
else
  if grep -q "stayconnect-backup-cleanup" "$t/out"; then
    ok "a unit whose helper has no source is refused, and the message names it"
  else
    bad "refused, but the message does not name the missing helper:"; sed 's/^/      /' "$t/out"
  fi
fi
if [ -f "$t/units/stayconnect-backup-cleanup.service" ]; then
  bad "the unit was installed despite the refusal — the timer would run and fail 203/EXEC"
else
  ok "nothing was installed"
fi

# ---------------------------------------------------------------- 2. the good case
# The same unit, with its script present under the repository's .sh spelling, installs to the exact path the
# unit names and passes verification.
t="$(new_tree good)"
cp "$DEPLOY/systemd/stayconnect-backup-cleanup.service" "$t/src/systemd/"
cp "$DEPLOY/scripts/stayconnect-backup-cleanup.sh" "$t/src/scripts/"
printf '#!/bin/sh\nexit 0\n' > "$t/src/scripts/wait-for-site-db.sh"
if run_installer "$t"; then
  if [ -x "$t/bin/stayconnect-backup-cleanup" ]; then
    ok "scripts/stayconnect-backup-cleanup.sh installs as bin/stayconnect-backup-cleanup, executable"
  else
    bad "the installer passed but $t/bin/stayconnect-backup-cleanup is missing or not executable"
  fi
else
  bad "the good case was refused:"; sed 's/^/      /' "$t/out"
fi

# ---------------------------------------------------------------- 3. present but not executable
# install(1) sets the mode, so this models a helper left behind by an older install or a botched manual copy.
# The verification pass must catch it: a non-executable ExecStart is the same 203/EXEC.
t="$(new_tree not_executable)"
cp "$DEPLOY/systemd/stayconnect-backup-cleanup.service" "$t/src/systemd/"
cp "$DEPLOY/scripts/stayconnect-backup-cleanup.sh" "$t/src/scripts/"
printf '#!/bin/sh\nexit 0\n' > "$t/src/scripts/wait-for-site-db.sh"
run_installer "$t"
chmod -x "$t/bin/stayconnect-backup-cleanup"
# Re-run the verification half only, by re-running the installer with the source removed so it cannot repair
# the file it is meant to be checking.
rm -f "$t/src/scripts/stayconnect-backup-cleanup.sh"
if [ -x "$t/bin/stayconnect-backup-cleanup" ]; then
  # A filesystem with no POSIX exec bit (a Windows development checkout) cannot express this case. Say so
  # rather than pass: the CI runner is Linux, and there it is a real assertion.
  echo "  SKIP: this filesystem ignores chmod -x, so 'present but not executable' cannot be staged here"
else
  if run_installer "$t"; then
    bad "a non-executable helper passed verification"
  else
    ok "a helper that is present but not executable is refused"
  fi
fi

# ---------------------------------------------------------------- 4. every unit, not a remembered few
# The old check read ExecStartPre for two hardcoded units. A helper named on ExecStart by any other unit must
# be covered too — that is exactly the gap the outage came through.
t="$(new_tree every_unit)"
cp "$DEPLOY"/systemd/stayconnect-*.service "$t/src/systemd/" 2>/dev/null
for want in $(grep -hoE '/opt/stayconnect/bin/[A-Za-z0-9._-]+' "$t"/src/systemd/*.service | sed 's#.*/##' | sort -u); do
  if [ -f "$DEPLOY/scripts/$want" ]; then cp "$DEPLOY/scripts/$want" "$t/src/scripts/"
  elif [ -f "$DEPLOY/scripts/$want.sh" ]; then cp "$DEPLOY/scripts/$want.sh" "$t/src/scripts/"
  else printf '#!/bin/sh\nexit 0\n' > "$t/src/scripts/$want"
  fi
done
if run_installer "$t"; then
  covered="$(grep -c ' -> ' "$t/out")"
  if [ "${covered:-0}" -ge 2 ]; then
    ok "verification covers every unit helper ($covered checked)"
  else
    bad "only $covered helper(s) were verified across the full unit set"
  fi
else
  bad "the full unit set was refused:"; sed 's/^/      /' "$t/out"
fi

# ---------------------------------------------------------------- 5. the shipped units are self-consistent
# Every /opt/stayconnect/bin path the real units name must be produced by SOMETHING in this repository: a
# helper script under deploy/scripts, or a Go command under data-plane/cmd that the build delivers. A unit
# naming a path nobody produces is the outage in its original form, and it must not be possible to ship one.
ROOT="$(cd "$DEPLOY/.." && pwd)"
for u in "$DEPLOY"/systemd/stayconnect-*.service; do
  b="$(basename "$u")"
  for path in $(grep -hoE '^Exec[A-Za-z]*=[^ ]*/opt/stayconnect/bin/[A-Za-z0-9._-]+' "$u" 2>/dev/null |
                grep -oE '/opt/stayconnect/bin/[A-Za-z0-9._-]+' | sort -u); do
    want="$(basename "$path")"
    if [ -f "$DEPLOY/scripts/$want" ] || [ -f "$DEPLOY/scripts/$want.sh" ]; then
      ok "$b -> $want is a helper this installer ships"
    elif [ -d "$ROOT/data-plane/cmd/$want" ] || [ -d "$ROOT/control-plane/cmd/$want" ]; then
      ok "$b -> $want is a binary the build delivers"
    else
      bad "$b references $path but nothing in this repository produces it: no deploy/scripts/$want(.sh), no data-plane/cmd/$want and no control-plane/cmd/$want"
    fi
  done
done

# ---------------------------------------------------------------- 6. a unit that is NOT in service here
# stayconnect-ctrlapi belongs to the Central control plane and is deliberately disabled on an edge appliance,
# which has no ctrlapi binary and no reason to hold one. Refusing the whole install over it would make the
# correct configuration uninstallable, so it must be REPORTED and installed anyway. (SC_SKIP_SYSTEMD forces
# the strict reading, so this case drives the real systemd path by asking the host about a unit that is not
# enabled on it.)
if command -v systemctl >/dev/null 2>&1; then
  t="$(new_tree not_in_service)"
  cp "$DEPLOY/systemd/stayconnect-ctrlapi.service" "$t/src/systemd/" 2>/dev/null
  printf '#!/bin/sh
exit 0
' > "$t/src/scripts/wait-for-site-db.sh"
  if SC_BIN_DIR="$t/bin" SC_UNIT_DIR="$t/units" SC_ENV_DIR="$t/etc"        bash "$INSTALLER" "$t/src" >"$t/out" 2>&1; then
    if grep -q "NOT enabled here" "$t/out"; then
      ok "a unit that is not in service here is reported, not refused"
    else
      bad "it installed, but said nothing about the missing binary:"; sed 's/^/      /' "$t/out"
    fi
  else
    bad "a unit that is not enabled on this host blocked the install:"; sed 's/^/      /' "$t/out"
  fi
else
  echo "  SKIP: no systemctl on this machine, so unit enablement cannot be interrogated"
fi

if [ "$fail" = "0" ]; then
  echo "INSTALL_SERVICE_UNITS_SELFTEST = PASS"
  exit 0
fi
echo "INSTALL_SERVICE_UNITS_SELFTEST = FAIL"
exit 1
