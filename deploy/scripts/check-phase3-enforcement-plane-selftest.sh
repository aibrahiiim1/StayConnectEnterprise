#!/usr/bin/env bash
# A CHECK THAT CANNOT FAIL IS NOT A CHECK.
#
# check-phase3-enforcement-plane.sh exists because of one specific live state: a Phase-3 surface enabled in one
# service's env file while the enforcement plane was absent from the other two. This proves the checker
# actually refuses that state — not merely that it runs and prints OK, which is what a checker with a typo in
# its variable name would also do.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$HERE/check-phase3-enforcement-plane.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

case_dir() { # case_dir <name> -> echoes a fresh dir
  local d="$WORK/$1"; mkdir -p "$d"; echo "$d"
}
expect() { # expect <want-exit> <dir> <description>
  local want="$1" dir="$2" what="$3" got
  bash "$CHECK" "$dir" >"$dir/.out" 2>&1
  got=$?
  if [ "$got" != "$want" ]; then
    echo "  *** FAIL: $what — exit $got, want $want"
    sed 's/^/      /' "$dir/.out"
    fail=1
  else
    echo "  ok: $what"
  fi
}

# 1. THE LIVE FAILURE, reproduced exactly: Room authentication on in scd.env, netd and acctd carrying no
#    Phase-3 configuration at all. This is the state that granted two real guests unenforceable access.
d="$(case_dir live_failure)"
printf 'STAYCONNECT_PHASE3_MASTER=1\nSTAYCONNECT_PHASE3_PMS_AUTH=1\n' > "$d/scd.env"
printf 'NETD_DB_URL=x\n'  > "$d/netd.env"
printf 'ACCTD_DB_URL=x\n' > "$d/acctd.env"
expect 1 "$d" "a surface with no enforcement plane is REFUSED"

# 2. Half-applied: the flags are there but netd has no producer to authenticate, so it would exit at startup
#    and systemd would restart it forever.
d="$(case_dir no_producer)"
printf 'STAYCONNECT_PHASE3_MASTER=1\nSTAYCONNECT_PHASE3_PMS_AUTH=1\n' > "$d/scd.env"
printf 'NETD_DB_URL=x\nSTAYCONNECT_PHASE3_MASTER=true\n' > "$d/netd.env"
printf 'ACCTD_DB_URL=x\nSTAYCONNECT_PHASE3_MASTER=true\n' > "$d/acctd.env"
expect 1 "$d" "a plane with no authenticated producer is REFUSED"

# 3. Checkout Grace is a session-minting surface too, and it was the ONLY one the old gate looked at.
d="$(case_dir grace_only)"
printf 'STAYCONNECT_PHASE3_MASTER=1\nSTAYCONNECT_PHASE3_CHECKOUT_GRACE=1\n' > "$d/pmsd.env"
printf 'NETD_DB_URL=x\n'  > "$d/netd.env"
printf 'ACCTD_DB_URL=x\n' > "$d/acctd.env"
expect 1 "$d" "Checkout Grace without the plane is REFUSED"

# 4. A correctly enabled appliance passes.
d="$(case_dir coherent)"
printf 'STAYCONNECT_PHASE3_MASTER=1\nSTAYCONNECT_PHASE3_PMS_AUTH=1\n' > "$d/scd.env"
printf 'NETD_DB_URL=x\nSTAYCONNECT_PHASE3_MASTER=true\nNETD_PHASE3_PRODUCER_UID=0\n' > "$d/netd.env"
printf 'ACCTD_DB_URL=x\nSTAYCONNECT_PHASE3_MASTER=true\n' > "$d/acctd.env"
expect 0 "$d" "a coherent appliance is accepted"

# 5. A dark appliance passes: no surface mints sessions, so no plane is required and none is demanded.
d="$(case_dir dark)"
printf 'SCD_DB_URL=x\n'   > "$d/scd.env"
printf 'NETD_DB_URL=x\n'  > "$d/netd.env"
printf 'ACCTD_DB_URL=x\n' > "$d/acctd.env"
expect 0 "$d" "a dark appliance needs no enforcement plane"

if [ "$fail" = "0" ]; then
  echo "PHASE3_ENFORCEMENT_PLANE_CHECK_SELFTEST = PASS"
  exit 0
fi
echo "PHASE3_ENFORCEMENT_PLANE_CHECK_SELFTEST = FAIL"
exit 1
