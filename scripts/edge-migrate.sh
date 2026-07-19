#!/usr/bin/env bash
# Authoritative edge-DB (site-local) migration runner for data-plane/migrations/NNNN_name.up.sql.
#
# ATOMIC + FAIL-CLOSED:
#   * requires --only <exact-version> (single migration) or the explicit --all mode.
#   * migration names must match ^[0-9]{4}_[a-z0-9_]+$; ambiguous/absent selections are rejected.
#   * the ledger decision happens ONLY under the advisory lock (no pre-lock decision): one dedicated psql
#     session takes a bounded advisory lock, re-reads public.schema_migrations under the lock, applies the
#     migration or reports SKIP_AFTER_LOCK, records exactly one ledger row, unlocks — all in one session.
#   * target identity is verified before any write: expected database (--expect-db), the iam_v2 schema, and
#     the schema_migrations ledger must exist. The ledger is NOT silently created in normal mode; a separate
#     explicit --bootstrap-ledger mode creates it.
#   * the selected file + its SHA-256 are printed (and belong in the deployment evidence); the version is
#     pattern-validated before any use in SQL (never interpolate an unvalidated name).
#   * the ONLY public-schema write these migrations perform is the schema_migrations metadata row.
#
# Usage:
#   EDGE_PSQL='docker exec -i <c> psql -U <role> -d <db> -v ON_ERROR_STOP=1' \
#     scripts/edge-migrate.sh --only 0010_phase3_stay_resolution --expect-db <db>
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
MIG_DIR="$HERE/data-plane/migrations"
ONLY=""; ALL=0; EXPECT_DB=""; BOOTSTRAP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) MIG_DIR="$2"; shift 2;;
    --only) ONLY="$2"; shift 2;;
    --all) ALL=1; shift;;
    --expect-db) EXPECT_DB="$2"; shift 2;;
    --bootstrap-ledger) BOOTSTRAP=1; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "${EDGE_PSQL:-}" ] || { echo "EDGE_PSQL not set"; exit 2; }
if [ -z "$ONLY" ] && [ "$ALL" -ne 1 ]; then echo "REFUSED: specify --only <exact-version> or --all" >&2; exit 2; fi
NAME_RE='^[0-9]{4}_[a-z0-9_]+$'
[ -z "$ONLY" ] || echo "$ONLY" | grep -Eq "$NAME_RE" || { echo "REFUSED: --only '$ONLY' does not match $NAME_RE" >&2; exit 2; }

q(){ $EDGE_PSQL -tAqc "$1"; }
LOCKNS='9223372036'  # fixed advisory-lock namespace-ish key for the whole edge-migrate runner

# --- target identity fail-closed ------------------------------------------------------------------
curdb="$(q 'SELECT current_database()')"
if [ -n "$EXPECT_DB" ] && [ "$curdb" != "$EXPECT_DB" ]; then
  echo "REFUSED: connected to database '$curdb' but --expect-db '$EXPECT_DB'" >&2; exit 3
fi
case "$curdb" in stayconnect|stayconnect_site_b) echo "REFUSED: refusing a non-target live database '$curdb'" >&2; exit 3;; esac
iamok="$(q "SELECT count(*) FROM information_schema.schemata WHERE schema_name='iam_v2'")"
[ "$iamok" = "1" ] || { echo "REFUSED: iam_v2 schema not present (baseline not built) in '$curdb'" >&2; exit 3; }
ledger="$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations'")"
if [ "$ledger" != "1" ]; then
  if [ "$BOOTSTRAP" -eq 1 ]; then
    q "CREATE TABLE public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
    echo "  ledger bootstrapped (explicit --bootstrap-ledger)"
  else
    echo "REFUSED: public.schema_migrations ledger absent; re-run once with --bootstrap-ledger" >&2; exit 3
  fi
fi

# --- resolve file set --------------------------------------------------------------------------------
files=()
if [ -n "$ONLY" ]; then
  n=0; for g in "$MIG_DIR/$ONLY".up.sql; do [ -e "$g" ] && n=$((n+1)); done
  [ "$n" -eq 1 ] || { echo "REFUSED: --only '$ONLY' resolves to $n files (need exactly 1)" >&2; exit 2; }
  files=("$MIG_DIR/$ONLY.up.sql")
else
  for f in "$MIG_DIR"/*.up.sql; do files+=("$f"); done
fi

applied=0; skipped=0
for f in "${files[@]}"; do
  [ -f "$f" ] || { echo "REFUSED: migration file not found: $f" >&2; exit 2; }
  base="$(basename "$f")"; ver="${base%.up.sql}"
  echo "$ver" | grep -Eq "$NAME_RE" || { echo "REFUSED: version '$ver' does not match $NAME_RE" >&2; exit 2; }
  sha="$(sha256sum "$f" | awk '{print $1}')"
  # per-version lock key derived from the runner namespace + a fold of the version (documented, deterministic)
  key="$(q "SELECT (hashtextextended('$LOCKNS:$ver', 0))")"
  echo "  select $ver  file=$base  sha256=$sha  lock_key=$key"
  # ATOMIC: bounded advisory lock -> re-read ledger UNDER lock -> apply-or-skip -> record -> unlock, ONE session.
  out="$(
    { printf "SET statement_timeout='60s';\nSELECT pg_advisory_lock(%s);\nSET statement_timeout=0;\n" "$key"
      printf "SELECT (NOT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='%s')) AS need \\\\gset\n" "$ver"
      printf "\\\\if :need\n\\\\echo APPLYING_UNDER_LOCK\n"
      cat "$f"
      printf "\nINSERT INTO public.schema_migrations(version) VALUES ('%s') ON CONFLICT DO NOTHING;\n" "$ver"
      printf "\\\\else\n\\\\echo SKIP_AFTER_LOCK\n\\\\endif\n"
      printf "SELECT pg_advisory_unlock(%s);\n" "$key"
    } | $EDGE_PSQL 2>&1
  )"
  if echo "$out" | grep -q "APPLYING_UNDER_LOCK"; then
    echo "  apply $ver (under lock)"; applied=$((applied+1))
  elif echo "$out" | grep -q "SKIP_AFTER_LOCK"; then
    echo "  skip-after-lock $ver (already applied)"; skipped=$((skipped+1))
  else
    echo "RUNNER ERROR for $ver:"; echo "$out" | tail -5 >&2; exit 4
  fi
done
echo "EDGE_MIGRATE_OK applied=$applied skipped=$skipped"
