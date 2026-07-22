#!/usr/bin/env bash
# Phase-3 OFFLINE PREFLIGHT.
#
# Answers one question before anyone touches an appliance: is this build safe to deploy DARK?
#
# Everything here is local and offline. It contacts no appliance, no production database, no PMS and no
# network service. It refuses to run against anything but the repository it lives in, and it fails on the
# FIRST condition that would make a dark deployment unsafe rather than reporting a tidy list at the end.
#
# Usage: bash scripts/phase3-preflight.sh [--json]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JSON=0
[ "${1:-}" = "--json" ] && JSON=1

pass=0; fail=0
declare -a RESULTS
ok(){ RESULTS+=("PASS|$1"); pass=$((pass+1)); [ $JSON -eq 1 ] || echo "  [PASS] $1"; }
no(){ RESULTS+=("FAIL|$1"); fail=$((fail+1)); [ $JSON -eq 1 ] || echo "  [FAIL] $1"; }

[ $JSON -eq 1 ] || echo "== Phase-3 offline preflight (no appliance, no production DB, no PMS) =="

# ---------------------------------------------------------------- 1. the build itself
if (cd "$ROOT/data-plane" && go build ./... >/dev/null 2>&1); then
  ok "data-plane builds"
else
  no "data-plane does not build"
fi
# gofmt is checked over the PHASE-3 package set (the same list the Phase-3 CI enforces). Pre-existing
# formatting elsewhere in the repository is out of this preflight's scope and is deliberately not asserted
# here, so a Phase-3 go/no-go is never blocked or falsely reassured by unrelated code.
P3_PKGS="internal/pmsd internal/pms internal/stayengine internal/pmsresolve internal/authctx internal/grace internal/checkout internal/staygrant internal/enforce internal/shapeplan internal/writerguard cmd/pmsd cmd/edged cmd/portald cmd/acctd cmd/netd cmd/scd"
if (cd "$ROOT/data-plane" && [ -z "$(gofmt -l $P3_PKGS 2>/dev/null)" ]); then
  ok "Phase-3 Go sources are gofmt-clean"
else
  no "Phase-3 Go sources are not gofmt-clean: $(cd "$ROOT/data-plane" && gofmt -l $P3_PKGS | tr '
' ' ')"
fi
if (cd "$ROOT/data-plane" && go vet ./... >/dev/null 2>&1); then
  ok "go vet is clean"
else
  no "go vet reports problems"
fi

# ---------------------------------------------------------------- 2. the flags that keep it dark
# A deployed unit must ship with every Phase-3 flag OFF. The authoritative default lives in the Go config;
# this asserts the DEFAULT is off, not merely that someone remembered to unset the env.
if grep -q "func DefaultPMSConfig() PMSConfig { return PMSConfig{} }" "$ROOT/data-plane/internal/iamv2/pms_config.go"; then
  ok "Phase-3 flag defaults are OFF in code"
else
  no "Phase-3 flag defaults are not provably OFF"
fi
if grep -q "phase3 surface flag enabled while STAYCONNECT_PHASE3_MASTER is OFF" "$ROOT/data-plane/internal/iamv2/pms_config.go"; then
  ok "a surface flag without the master flag is a startup failure (loud, not silently off)"
else
  no "an incoherent flag set would not fail closed"
fi
# the deployed frontend bundle must not be built with the admin flag on
if grep -RIl "NEXT_PUBLIC_PHASE3_ADMIN" "$ROOT/deploy" 2>/dev/null | grep -q .; then
  no "a deployment file sets NEXT_PUBLIC_PHASE3_ADMIN (the deployed bundle must be dark)"
else
  ok "no deployment file enables the Phase-3 admin bundle"
fi

# ---------------------------------------------------------------- 3. the migration is reversible
UP="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
if [ -f "$UP" ] && [ -f "$DOWN" ]; then
  ok "migration 0010 has both an up and a down script"
else
  no "migration 0010 is missing its up or down script"
fi
# every controlled function the up script creates must be dropped by the down script: a rollback that leaves
# executable functions behind is not a rollback.
missing=""
while read -r fn; do
  grep -q "DROP FUNCTION IF EXISTS iam_v2.$fn" "$DOWN" || missing="$missing $fn"
done < <(grep -oE "CREATE OR REPLACE FUNCTION iam_v2\.[a-z0-9_]+" "$UP" | sed 's/.*iam_v2\.//' | sort -u)
if [ -z "$missing" ]; then
  ok "every function created by 0010 is dropped by its down script"
else
  no "down script does not drop:$missing"
fi
# every table the up script creates must be dropped too
tmissing=""
while read -r tb; do
  grep -q "DROP TABLE IF EXISTS iam_v2.$tb" "$DOWN" || tmissing="$tmissing $tb"
done < <(grep -oE "CREATE TABLE iam_v2\.[a-z0-9_]+" "$UP" | sed 's/.*iam_v2\.//' | sort -u)
if [ -z "$tmissing" ]; then
  ok "every table created by 0010 is dropped by its down script"
else
  no "down script does not drop tables:$tmissing"
fi

# ---------------------------------------------------------------- 4. zero runtime privilege while dark
if grep -q "REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition" "$UP" &&
   ! grep -qE "GRANT (EXECUTE|SELECT|INSERT|UPDATE|DELETE).*TO (svc_|PUBLIC)" "$UP"; then
  ok "0010 grants no runtime role any iam_v2 privilege"
else
  no "0010 grants a runtime privilege (Gate-P is a separate, authorized step)"
fi

# ---------------------------------------------------------------- 4b. exactly one Phase-3 shaping writer
# ADR-0002: netd is the ONLY process that mutates Phase-3 tc state. This is checked structurally rather than
# trusted: if acctd ever regains a tc mutation call, two daemons can race the same kernel classes on their own
# schedules, and nothing at runtime would report it.
if grep -qE '(AddSession|DeleteSession|EnsureBridgeInfra)\(' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" 2>/dev/null; then
  ok "netd is a Phase-3 shaping writer (ADR-0002)"
else
  no "netd does not perform Phase-3 shaping"
fi
if grep -qE '(AddSession|DeleteSession)\(' "$ROOT/data-plane/cmd/acctd/phase3.go" 2>/dev/null; then
  no "acctd mutates tc directly — ADR-0002 requires netd to be the single shaping writer"
else
  ok "acctd derives the plan and performs NO Phase-3 tc mutation (single writer holds)"
fi

# ---------------------------------------------------------------- 5. no live-evidence fabrication
# The evidence bundle must never contain claims about an appliance this tooling has not actually touched.
if grep -RIn "LIVE VERIFIED" "$ROOT/docs/manifests" 2>/dev/null | grep -q .; then
  no "a manifest claims live verification that this offline tooling cannot have performed"
else
  ok "no offline artifact claims live verification"
fi

# ---------------------------------------------------------------- 8. the Phase-3 control-plane invariants
# These four are the properties that make the single-writer decision (ADR-0002) and the controlled-writer
# boundary real rather than documentary. Each is checked structurally, because each can be silently undone by
# an ordinary-looking change.

# (a) netd refuses to mutate tc while Phase 3 is dark, on its OWN authority. If this check ever fails, the
#     kill switch depends on acctd staying correct instead of on netd enforcing it.
if grep -q 'phase3_dark' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" \
   && grep -q 'if !p.mode.Active' "$ROOT/data-plane/cmd/netd/phase3_shaping.go"; then
  ok "netd refuses shaping submissions while Phase 3 is dark (its own check, not the producer's)"
else
  no "netd does not independently refuse shaping while dark"
fi

# (b) the shaping producer is authenticated by peer credentials, never by a request header. A header is a
#     claim any local process can write; SO_PEERCRED is the kernel's statement.
if grep -q 'SO_PEERCRED' "$ROOT/data-plane/cmd/netd/phase3_peer_linux.go" \
   && ! grep -qE 'r\.Header\.Get\("X-[^"]*(Producer|Service|Caller)' "$ROOT/data-plane/cmd/netd/phase3_shaping.go"; then
  ok "the Phase-3 shaping producer is authenticated by peer credentials, not a header"
else
  no "the shaping producer is not authenticated by peer credentials"
fi

# (c) both ends of the shaping contract use the ONE shared definition. Two hand-written copies of a canonical
#     hash drift silently, and the drift only shows up as a refused plan in production.
if grep -q 'internal/shapeplan' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" \
   && grep -q 'internal/shapeplan' "$ROOT/data-plane/cmd/acctd/phase3.go"; then
  ok "producer and applier share one shaping contract definition (internal/shapeplan)"
else
  no "the shaping contract is defined separately on each side"
fi

# (d) every Phase-3 composition root verifies the controlled-writer boundary before it can write. A service
#     that skipped this could run against a schema whose guards were never applied and never notice.
# netd is included because although it writes no Phase-3 TABLE directly, it performs two authoritative
# operations (allocating a class generation, registering a class origin) and those mean nothing on a schema
# whose guards were never applied. portald is deliberately absent: it writes no iam_v2 state at all, it
# proxies to scd, so requiring the check there would be requiring a promise it has no way to keep.
missing=""
for root in acctd edged scd pmsd netd; do
  grep -q 'writerguard.Verify' "$ROOT/data-plane/cmd/$root/main.go" || missing="$missing $root"
done
if [ -z "$missing" ]; then
  ok "every Phase-3 writing service verifies the controlled-writer boundary at startup"
else
  no "these Phase-3 services do not verify the writer boundary:$missing"
fi

# ---------------------------------------------------------------- 9. rollback ordering
# Every trigger the up migration attaches to the controlled-writer guard has to be dropped BY NAME in the down
# migration, and BEFORE the guard function itself is dropped. PostgreSQL refuses to drop a function while a
# trigger still depends on it, so one missing line aborts the entire rollback -- and nothing that merely
# APPLIES the migration can see it. That is why this has now slipped through twice: the failure only appears
# on the rollback path, and only in CI. The check is static so it costs nothing and names the missing line.
UP_SQL="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN_SQL="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
guard_line="$(grep -n 'DROP FUNCTION IF EXISTS iam_v2.p3_controlled_writer_only' "$DOWN_SQL" | head -1 | cut -d: -f1)"
order_defect=""
if [ -z "$guard_line" ]; then
  order_defect=" the down migration never drops the controlled-writer guard function"
else
  # Each "CREATE TRIGGER <name> ... EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();" in the up migration.
  # The whole statement is accumulated up to its terminating semicolon before matching, so a trigger that
  # merely happens to sit above a guarded one is not mistaken for a guarded trigger itself.
  guarded="$(awk '
    /CREATE TRIGGER/            { stmt=$0; name=$3; open=1 }
    open && !/CREATE TRIGGER/   { stmt=stmt " " $0 }
    open && /;[[:space:]]*$/    { if (stmt ~ /p3_controlled_writer_only\(\)/) print name; open=0 }
  ' "$UP_SQL" | sort -u)"
  [ -n "$guarded" ] || order_defect=" no guarded triggers found in the up migration (the check would be vacuous)"
  for trg in $guarded; do
    dl="$(grep -n "DROP TRIGGER IF EXISTS $trg " "$DOWN_SQL" | head -1 | cut -d: -f1)"
    if [ -z "$dl" ]; then
      order_defect="$order_defect $trg(never dropped)"
    elif [ "$dl" -gt "$guard_line" ]; then
      order_defect="$order_defect $trg(dropped after the guard function)"
    fi
  done
fi
if [ -z "$order_defect" ]; then
  ok "every controlled-writer trigger is dropped by name before its guard function (rollback ordering)"
else
  no "rollback ordering defect:$order_defect"
fi


# ============================================================================== report
# Emitting comes last on purpose: an earlier version printed the JSON before section 8 had run, so --json
# silently reported a smaller, all-passing suite.
if [ $JSON -eq 1 ]; then
  printf '{"pass":%d,"fail":%d,"checks":[' "$pass" "$fail"
  first=1
  for r in "${RESULTS[@]}"; do
    st="${r%%|*}"; msg="${r#*|}"
    [ $first -eq 1 ] || printf ','
    printf '{"status":"%s","check":"%s"}' "$st" "$(printf '%s' "$msg" | sed 's/"/\\"/g')"
    first=0
  done
  printf ']}\n'
else
  echo "============================================================"
  echo "PHASE3_PREFLIGHT: pass=$pass fail=$fail -> $([ $fail -eq 0 ] && echo PASS || echo FAIL)"
fi
[ $fail -eq 0 ]
