#!/usr/bin/env bash
# A CHECK THAT CANNOT FAIL IS NOT A CHECK.
#
# check-appliance-units.sh exists because two units sat broken on the appliance for weeks while
# `systemctl --failed` listed them the whole time. These cases prove it refuses each of those states, and — the
# half that decides whether anyone leaves it switched on — that it does NOT complain about the Central-only
# units an edge appliance legitimately carries and must never run.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$HERE/check-appliance-units.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0; pass=0
ok() { echo "  ok: $1"; pass=$((pass+1)); }
no() { echo "  *** FAIL: $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/        /' | head -8; fail=1; }

# A disposable unit dir plus stubbed systemctl answers, so no real service is consulted or touched.
newdir() { local d="$WORK/$1"; mkdir -p "$d/units" "$d/root/opt/stayconnect/bin"; echo "$d"; }

stub() { # stub <dir> <enabled> <active> <failed-list>
  local d="$1"
  printf '#!/bin/sh\necho %s\n' "$2" > "$d/is-enabled"; chmod +x "$d/is-enabled"
  printf '#!/bin/sh\necho %s\n' "$3" > "$d/is-active"; chmod +x "$d/is-active"
  printf '#!/bin/sh\nprintf "%%s" "%s"\n' "$4" > "$d/failed"; chmod +x "$d/failed"
}

run() { # run <dir>
  local d="$1"
  SC_UNIT_DIR="$d/units" SC_EXEC_PREFIX="$d/root" \
  SC_UNIT_ENABLED_CMD="$d/is-enabled" SC_UNIT_ACTIVE_CMD="$d/is-active" SC_UNIT_FAILED_CMD="$d/failed" \
    bash "$CHECK" >"$d/out" 2>&1
}

# 1. A FAILED UNIT. Exactly what both live outages looked like from the outside, for weeks.
d="$(newdir failed_unit)"; stub "$d" enabled active "stayconnect-tc-setup.service loaded failed failed"
printf '[Service]\nExecStart=/opt/stayconnect/bin/acctd\n' > "$d/units/stayconnect-acctd.service"
printf '#!/bin/sh\n' > "$d/root/opt/stayconnect/bin/acctd"; chmod +x "$d/root/opt/stayconnect/bin/acctd"
if run "$d"; then no "a failed StayConnect unit was accepted" "$(cat "$d/out")"
else grep -q "is in a FAILED state" "$d/out" && ok "a failed StayConnect unit is REFUSED" \
     || no "refused, but not for the failed state" "$(cat "$d/out")"; fi

# 2. A MISSING PROGRAM on an enabled unit — the 203/EXEC that emptied the disk.
d="$(newdir missing_exec)"; stub "$d" enabled active ""
printf '[Service]\nExecStart=/opt/stayconnect/bin/stayconnect-backup-cleanup\n' > "$d/units/stayconnect-backup-cleanup.service"
if run "$d"; then no "an enabled unit with a missing program was accepted" "$(cat "$d/out")"
else grep -q "does not exist" "$d/out" && ok "an enabled unit whose program is missing is REFUSED" \
     || no "refused, but not for the missing program" "$(cat "$d/out")"; fi

# 3. A NON-EXECUTABLE program is the same failure wearing a different hat.
d="$(newdir not_exec)"; stub "$d" enabled active ""
printf '[Service]\nExecStart=/opt/stayconnect/bin/helper.sh\n' > "$d/units/stayconnect-x.service"
printf '#!/bin/sh\n' > "$d/root/opt/stayconnect/bin/helper.sh"; chmod -x "$d/root/opt/stayconnect/bin/helper.sh" 2>/dev/null
if [ -x "$d/root/opt/stayconnect/bin/helper.sh" ]; then
  echo "  skip: this filesystem cannot drop the execute bit, so the non-executable case is unstageable"
else
  if run "$d"; then no "a non-executable program was accepted" "$(cat "$d/out")"
  else grep -q "not executable" "$d/out" && ok "a non-executable program is REFUSED" \
       || no "refused, but not for the execute bit" "$(cat "$d/out")"; fi
fi

# 4. THE OTHER HALF: a Central-only unit an EDGE appliance carries and must never run. Disabled, inactive,
#    pulled in by nothing, program absent — and that is CORRECT. A check that failed here would be disabled.
d="$(newdir dormant_central)"; stub "$d" disabled inactive ""
printf '[Service]\nExecStart=/opt/stayconnect/bin/ctrlapi\n' > "$d/units/stayconnect-ctrlapi.service"
if run "$d"; then
  grep -q "deliberately dormant" "$d/out" && ok "a disabled, unreachable Central-only unit is excused, not failed" \
    || no "the dormant unit passed but was not reported as dormant" "$(cat "$d/out")"
else
  no "a legitimately dormant Central-only unit was reported as broken" "$(cat "$d/out")"
fi

# 5. AND THE TRAP THAT CAUGHT tc-setup: disabled means nothing if another unit pulls it in.
d="$(newdir disabled_but_pulled)"; stub "$d" disabled inactive ""
printf '[Unit]\nWants=stayconnect-tc-setup.service\n[Service]\nExecStart=/opt/stayconnect/bin/acctd\n' > "$d/units/stayconnect-acctd.service"
printf '[Service]\nExecStart=/opt/stayconnect/deploy/scripts/tc-setup.sh\n' > "$d/units/stayconnect-tc-setup.service"
printf '#!/bin/sh\n' > "$d/root/opt/stayconnect/bin/acctd"; chmod +x "$d/root/opt/stayconnect/bin/acctd"
if run "$d"; then no "a disabled unit that another unit still pulls in was excused as dormant" "$(cat "$d/out")"
else grep -q "still pulls it in" "$d/out" && ok "a disabled unit that something else pulls in is REFUSED" \
     || no "refused, but not for the dependency edge" "$(cat "$d/out")"; fi

# 6. A healthy appliance passes, or the check is useless.
d="$(newdir healthy)"; stub "$d" enabled active ""
printf '[Service]\nExecStart=/opt/stayconnect/bin/netd\n' > "$d/units/stayconnect-netd.service"
printf '#!/bin/sh\n' > "$d/root/opt/stayconnect/bin/netd"; chmod +x "$d/root/opt/stayconnect/bin/netd"
if run "$d"; then ok "a healthy appliance passes"; else no "a healthy appliance was refused" "$(cat "$d/out")"; fi

echo "============================================================"
if [ "$fail" = "0" ]; then echo "APPLIANCE_UNITS_SELFTEST = PASS ($pass cases)"; exit 0; fi
echo "APPLIANCE_UNITS_SELFTEST = FAIL"; exit 1
