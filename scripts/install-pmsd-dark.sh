#!/usr/bin/env bash
# INSTALL THE PMS CONNECTOR AS A DARK SERVICE.
#
# Live Increment 9 found the pmsd BINARY on the appliance and no unit: the daemon was deployed in the sense that
# a file existed, and undeployed in every sense that matters. This completes the contract, and it does it from a
# reviewed script rather than by typing systemctl on the box, so what gets installed is the thing that was read.
#
# It installs three things and nothing else:
#
#   1. a dedicated system user/group `stayconnect-pmsd` — the unit runs as it, and the unit is not weakened to
#      run as root just to make it start;
#   2. /etc/stayconnect/pmsd.env from deploy/env/pmsd.env.dark — deliberately empty of settings, so every
#      Phase-3 flag resolves OFF;
#   3. deploy/systemd/stayconnect-pmsd.service, enabled and started.
#
# DARK is not "the service is stopped". It is: the unit is installed and enabled, it starts on boot, it runs,
# it discovers that every flag it owns is OFF, it opens no PMS socket and touches no database, it exits 0, and
# `Restart=on-failure` leaves it at rest instead of storming. This script verifies exactly that and fails if the
# daemon does anything more.
#
# It contacts no PMS, enables no flag, and creates no PMS state.
#
# Usage:  sudo bash scripts/install-pmsd-dark.sh [--verify-only]
set -uo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd -P)"
UNIT_SRC="$HERE/deploy/systemd/stayconnect-pmsd.service"
ENV_SRC="$HERE/deploy/env/pmsd.env.dark"
UNIT_DST=/etc/systemd/system/stayconnect-pmsd.service
ENV_DST=/etc/stayconnect/pmsd.env
SVC_USER=stayconnect-pmsd
BIN=/opt/stayconnect/bin/pmsd

VERIFY_ONLY=0
[ "${1:-}" = "--verify-only" ] && VERIFY_ONLY=1

fail=0
ok()  { printf '  [PASS] %s\n' "$1"; }
no()  { printf '  [FAIL] %s\n' "$1"; fail=$((fail+1)); }
die() { printf 'install-pmsd-dark: FATAL: %s\n' "$*" >&2; exit 1; }

[ -f "$UNIT_SRC" ] || die "reviewed unit not found: $UNIT_SRC"
[ -f "$ENV_SRC" ]  || die "reviewed dark env not found: $ENV_SRC"

if [ "$VERIFY_ONLY" -eq 0 ]; then
  [ "$(id -u)" -eq 0 ] || die "must run as root (it creates a system user and installs a unit)"
  [ -x "$BIN" ] || die "the pmsd binary is not present at $BIN; deploy the candidate binaries first"

  echo "== 1. least-privilege service identity =="
  if getent group "$SVC_USER" >/dev/null; then
    echo "  group $SVC_USER already exists"
  else
    groupadd --system "$SVC_USER" || die "could not create group $SVC_USER"
    echo "  created system group $SVC_USER"
  fi
  if getent passwd "$SVC_USER" >/dev/null; then
    echo "  user $SVC_USER already exists"
  else
    # No login, no home, no shell: this account exists to own a process and nothing else.
    useradd --system --gid "$SVC_USER" --no-create-home --home-dir /nonexistent \
            --shell /usr/sbin/nologin --comment "StayConnect PMS connector (dark)" "$SVC_USER" \
      || die "could not create user $SVC_USER"
    echo "  created system user $SVC_USER (nologin, no home)"
  fi

  echo "== 2. the DARK environment file =="
  install -o root -g "$SVC_USER" -m 0640 "$ENV_SRC" "$ENV_DST" || die "could not install $ENV_DST"
  echo "  installed $ENV_DST (root:$SVC_USER 0640)"

  echo "== 3. the reviewed unit =="
  install -o root -g root -m 0644 "$UNIT_SRC" "$UNIT_DST" || die "could not install $UNIT_DST"
  systemctl daemon-reload || die "daemon-reload failed"
  systemctl enable stayconnect-pmsd.service >/dev/null 2>&1 || die "could not enable the unit"
  echo "  installed and enabled stayconnect-pmsd.service"
  systemctl start stayconnect-pmsd.service >/dev/null 2>&1 || true
  sleep 3
fi

echo "== VERIFY THE DARK CONTRACT =="

# (a) installed and enabled
[ -f "$UNIT_DST" ] && ok "unit installed at $UNIT_DST" || no "unit is not installed"
[ "$(systemctl is-enabled stayconnect-pmsd.service 2>/dev/null)" = "enabled" ] \
  && ok "unit is enabled (it will start on boot)" || no "unit is not enabled"

# (b) it runs as the dedicated account, not root
uid="$(systemctl show -p User --value stayconnect-pmsd.service 2>/dev/null)"
[ "$uid" = "$SVC_USER" ] && ok "runs as $SVC_USER, not root" || no "unit User= is '$uid', expected $SVC_USER"
getent passwd "$SVC_USER" >/dev/null && ok "the service account exists" || no "the service account is missing"

# (c) no Phase-3 flag anywhere in its configuration
if grep -qE '^[[:space:]]*STAYCONNECT_PHASE3_' "$ENV_DST" 2>/dev/null; then
  no "the dark env file sets a Phase-3 flag"
else
  ok "no Phase-3 flag is set in $ENV_DST"
fi

# (d) DARK behaviour: it ran, said it had nothing to do, and exited cleanly
res="$(systemctl show -p Result --value stayconnect-pmsd.service 2>/dev/null)"
act="$(systemctl is-active stayconnect-pmsd.service 2>/dev/null)"
code="$(systemctl show -p ExecMainStatus --value stayconnect-pmsd.service 2>/dev/null)"
printf '  observed: active=%s result=%s exit-status=%s\n' "$act" "$res" "$code"
if [ "$res" = "success" ] && [ "${code:-0}" = "0" ]; then
  ok "the daemon exited CLEANLY (flags OFF: nothing to do)"
else
  no "the daemon did not exit cleanly (result=$res status=$code)"
fi
if journalctl -u stayconnect-pmsd.service -n 50 --no-pager 2>/dev/null | grep -q 'connector and ingest flags OFF'; then
  ok "it logged the flags-OFF path: no assignment, DB, secret, worker or PMS socket"
else
  no "the flags-OFF log line is absent; the daemon may have taken another path"
fi

# (e) it is not restart-looping
nr="$(systemctl show -p NRestarts --value stayconnect-pmsd.service 2>/dev/null)"
[ "${nr:-0}" -le 1 ] && ok "not restart-looping (NRestarts=${nr:-0})" || no "restart loop detected (NRestarts=$nr)"

# (f) no PMS socket, no process left behind
conns="$(ss -tanp 2>/dev/null | grep -c 'pmsd' || true)"
[ "${conns:-0}" -eq 0 ] && ok "no socket owned by pmsd" || no "pmsd holds $conns socket(s)"
procs="$(pgrep -c -x pmsd 2>/dev/null || true)"
[ "${procs:-0}" -eq 0 ] && ok "no pmsd process is running (it exited, as DARK requires)" || no "$procs pmsd process(es) still running"

echo "=============================================="
if [ "$fail" -eq 0 ]; then
  echo "PMSD_DARK_CONTRACT = PASS"
  exit 0
fi
echo "PMSD_DARK_CONTRACT = FAIL ($fail)"
exit 1
