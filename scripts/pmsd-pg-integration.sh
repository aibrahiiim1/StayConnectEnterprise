#!/usr/bin/env bash
# Build a disposable PostgreSQL 16 carrying the FULL appliance schema, then run the pmsd / Phase-3 PG16
# integration suites (build tag `integration`) against it. Self-contained: creates + tears down its own
# container. No Production/appliance access.
#
# THE SCHEMA IS THE ONE AN APPLIANCE HAS. This harness used to build the iam_v2 scratch fixture plus 0009, 0010
# and a hand-maintained list of later migrations (0050-0079, 0092, 0093). That list fell behind more than once
# -- a suite that SELECTs a column the fixture lacks does not fail loudly, it scans an EMPTY result that reads
# as "this site has none" -- and it can never carry migration 0100, which the product now reads. So the
# database is now built exactly as a factory-clean appliance builds it (Gate-P roles, the production baseline,
# Gate-P ownership, every migration above the baseline, Gate-P grants); see scripts/lib-fullschema-testdb.sh.
# There is no list to fall behind any more.
#
# EXIT CODES (the CI retry policy depends on these):
#   0  every test passed
#   1  a TEST failed, or the schema did not apply — deterministic. CI must NOT retry: a second run that passes
#      would be hiding a defect.
#   2  the disposable infrastructure could not be built (container, image, readiness). That IS transient under
#      runner load, and is the only condition CI may retry.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C=iamv2-scratch; DB=iam_scratch; PORT="${PMSD_INTEG_PORT:-55432}"
# shellcheck source=lib-fullschema-testdb.sh
. "$ROOT/scripts/lib-fullschema-testdb.sh"

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
echo "== disposable PG16, FULL appliance schema, for pmsd integration (container=$C port=$PORT) =="
fullschema_build "$C" "$PORT" "$DB"; rc=$?
[ "$rc" = 0 ] || exit "$rc"

# THE KEY IS THE ASSERTION. 0079 re-keys the recovery bounds onto the interface; a schema that kept the old
# two-column key would let the isolation suite pass against a schema where it cannot hold.
key_cols="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.key_column_usage WHERE table_schema='iam_v2' AND constraint_name='pms_connection_settings_pkey';")"
if [ "${key_cols:-0}" != "3" ]; then
  echo "0079 NOT IN EFFECT (pms_connection_settings primary key has ${key_cols:-0} columns, expected 3)"
  exit 1
fi
runtime_cols="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_runtime' AND column_name='pinned_secret_generation_id';")"
if [ "${runtime_cols:-0}" != "1" ]; then
  # Deterministic: the schema itself is wrong. Exit 1 so CI does NOT retry.
  echo "0010 NOT IN EFFECT (pinned_secret_generation_id missing) -- this is a defect, not a flake"
  exit 1
fi
built="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")"
echo "  full schema built: iam_v2 tables=$built"

export PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/$DB"
# ---------------------------------------------------------------------------------------------------------
# TEST OWNERSHIP GUARD.
#
# Phase-4 integration tests live in the SAME ./cmd/edged package as the Phase-3 suites. While they carried only
# the `integration` tag they compiled into this run -- which, when this gate built a database that stopped at
# migration 0010, produced seven red tests that said nothing about Phase 3 and would have hidden anything that
# did. The database is now the full appliance schema, so the objects exist; the guard stays because WHICH
# suites a gate runs is still a scope decision: the financial matrix belongs to scripts/phase4-pg-integration.sh
# and carries `//go:build integration && phase4`. A future test dropped into these packages without the tag
# fails here, loudly, instead of silently widening this gate.
PKGS="./internal/pmsd/ ./internal/stayengine/ ./internal/authctx/ ./internal/checkout/ ./internal/staygrant/ ./internal/pmsresolve/ ./internal/enforce/ ./internal/writerguard/ ./cmd/edged/ ./cmd/acctd/ ./cmd/scd/"
echo "== test-ownership guard: no Phase-4-schema test may compile into the Phase-3 gate =="
leak=0
while IFS= read -r gofile; do
  [ -n "$gofile" ] || continue
  # Phase-4-only database objects. A Phase-3 test cannot legitimately reference these: they do not exist
  # until migration 0011 or later.
  if grep -qE 'iam_v2\.p4_|financial_base_currency|p4_declare_financial_recovery|p4_entitlement_grant_kernel' "$gofile"; then
    echo "  LEAK: $gofile compiles into the Phase-3 gate but references Phase-4-only schema"
    leak=1
  fi
done <<EOF
$( cd "$ROOT/data-plane" && go list -tags integration -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}' $PKGS 2>/dev/null )
EOF
if [ "$leak" != 0 ]; then
  echo "  A Phase-4 test must carry //go:build integration && phase4 so this gate does not build it."
  exit 1
fi
echo "  ok: every test compiled into this gate is Phase-3-owned"

echo "== go test -tags integration ./internal/pmsd ./internal/stayengine ./internal/authctx ./internal/checkout ./internal/staygrant ./internal/pmsresolve ./internal/enforce ./internal/writerguard ./cmd/edged ./cmd/acctd ./cmd/netd ./cmd/scd (Integration) =="
( cd "$ROOT/data-plane" && go test -tags integration -run Integration ./internal/pmsd/ ./internal/stayengine/ ./internal/authctx/ ./internal/checkout/ ./internal/staygrant/ ./internal/pmsresolve/ ./internal/enforce/ ./internal/writerguard/ ./cmd/edged/ ./cmd/acctd/ ./cmd/netd/ ./cmd/scd/ -count=1 )
rc=$?
echo "PMSD_PG_INTEGRATION rc=$rc"
exit $rc
