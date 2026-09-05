#!/usr/bin/env bash
# EVERY UNIT THIS APPLIANCE COULD ACTUALLY RUN MUST BE ABLE TO RUN.
#
# This exists because two units sat broken on the PRE-LIVE appliance for weeks and nothing asked:
#
#   * stayconnect-backup-cleanup.service named a helper nothing had installed. It failed 203/EXEC on every
#     nightly trigger, retention never ran, the root filesystem reached 100% and PostgreSQL restart-looped.
#   * stayconnect-tc-setup.service ran an obsolete script against a bridge that does not exist, exited 1 on
#     every start, and was pulled in by acctd's Wants= edge on every acctd restart.
#
# Both were visible in `systemctl --failed` the whole time. Nobody was looking, because nothing looked.
#
# THE DISTINCTION THAT MATTERS. An appliance legitimately carries units it must never run: this is an EDGE
# appliance, and stayconnect-ctrlapi / stayconnect-web-admin belong to the Central control plane. Those are
# disabled, inactive, unreachable by any dependency edge, and their absent binaries are correct rather than
# broken. A check that failed on them would be switched off within a week. So dormancy is PROVEN, not assumed:
# a unit is excused only when it is disabled AND inactive AND nothing pulls it in.
#
# It changes NOTHING. Read-only, safe at any time.
#
# Usage: check-appliance-units.sh [--json]
# Exit:  0 healthy · 1 a unit that could run cannot · 2 cannot check
set -uo pipefail

JSON=0
[ "${1:-}" = "--json" ] && JSON=1
SYSTEMD_DIR="${SC_UNIT_DIR:-/etc/systemd/system}"
ENABLED_CMD="${SC_UNIT_ENABLED_CMD:-systemctl is-enabled}"
ACTIVE_CMD="${SC_UNIT_ACTIVE_CMD:-systemctl is-active}"
FAILED_CMD="${SC_UNIT_FAILED_CMD:-systemctl --failed --no-legend --plain}"
PREFIX="${SC_EXEC_PREFIX:-}"

fails=0; dormant=0; checked=0
ok() { [ $JSON -eq 1 ] || echo "  [PASS] $1"; }
no() { [ $JSON -eq 1 ] || echo "  [FAIL] $1"; fails=$((fails+1)); }
note() { [ $JSON -eq 1 ] || echo "  [ ok ] $1"; }

command -v systemctl >/dev/null 2>&1 || [ -n "${SC_UNIT_ENABLED_CMD:-}" ] || {
  echo "CANNOT CHECK: systemctl is required"; exit 2; }

# ---- 1. nothing StayConnect may be in a failed state --------------------------------------------------------
failed="$($FAILED_CMD 2>/dev/null | awk '{print $1}' | grep '^stayconnect-' || true)"
if [ -n "$failed" ]; then
  while IFS= read -r u; do
    [ -n "$u" ] || continue
    no "$u is in a FAILED state"
  done <<< "$failed"
else
  ok "no StayConnect unit is in a failed state"
fi

# ---- 2. every unit that could run must have every Exec path it declares -------------------------------------
#
# "Could run" is the operative phrase. A unit that is disabled, inactive and pulled in by nothing cannot run,
# and its missing program is a fact about a role this appliance does not have rather than a defect.
for u in "$SYSTEMD_DIR"/stayconnect-*.service; do
  [ -f "$u" ] || continue
  b="$(basename "$u")"; unit="${b%.service}"
  # OUTPUT ONLY, NEVER THE EXIT STATUS. `systemctl is-enabled` exits 1 for a disabled unit and `is-active`
  # exits 3 for an inactive one, so `$(cmd || echo unknown)` appends "unknown" to a perfectly good answer and
  # every comparison below silently stops matching — which is how a correctly dormant Central-only unit was
  # reported as a broken one.
  en="$($ENABLED_CMD "$b" 2>/dev/null | head -1)"; en="${en:-unknown}"
  ac="$($ACTIVE_CMD "$b" 2>/dev/null | head -1)"; ac="${ac:-unknown}"

  # Is anything able to pull it in? A Wants=/Requires= from another shipped unit is enough: that is exactly
  # how the retired tc-setup unit kept being started despite being disabled.
  pulled=""
  if grep -lE "^(Wants|Requires|BindsTo|Requisite)=.*\b$unit\.service\b" "$SYSTEMD_DIR"/stayconnect-*.service 2>/dev/null \
     | grep -qv "/$b$"; then
    pulled="yes"
  fi

  if [ "$en" = "disabled" ] || [ "$en" = "masked" ]; then
    if [ "$ac" = "inactive" ] || [ "$ac" = "failed" ]; then
      if [ -z "$pulled" ]; then
        dormant=$((dormant+1))
        note "$b is deliberately dormant here (disabled, $ac, pulled in by nothing) — not checked"
        continue
      fi
      no "$b is disabled but another unit still pulls it in; it WILL run and must be maintained or the edge removed"
    fi
  fi

  checked=$((checked+1))
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    case "$p" in /bin/*|/sbin/*|/usr/bin/*|/usr/sbin/*) continue ;; esac
    target="$PREFIX$p"
    if [ ! -e "$target" ]; then
      no "$b declares $p, which does not exist — systemd would fail it 203/EXEC"
    elif [ ! -x "$target" ]; then
      no "$b declares $p, which is not executable — systemd would fail it 203/EXEC"
    fi
  done <<< "$(grep -hoE '^Exec[A-Za-z]*=-?/[^ ]+' "$u" 2>/dev/null | sed -E 's/^Exec[A-Za-z]*=-?//' | sort -u)"
done
[ "$fails" = "0" ] && ok "every unit that could run has all of its programs ($checked checked, $dormant deliberately dormant)"

if [ $JSON -eq 1 ]; then
  printf '{"healthy":%s,"failures":%d,"checked":%d,"dormant":%d}\n' \
    "$([ $fails -eq 0 ] && echo true || echo false)" "$fails" "$checked" "$dormant"
else
  echo "============================================================"
  echo "APPLIANCE_UNITS = $([ $fails -eq 0 ] && echo PASS || echo "FAIL ($fails)")"
fi
[ $fails -eq 0 ]
