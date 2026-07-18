#!/usr/bin/env bash
# Authoritative edge-DB (site-local) migration runner for data-plane/migrations/NNNN_name.up.sql.
# IDEMPOTENT: applies, in filename order, only migrations whose version is not already recorded in
# public.schema_migrations; re-running is a deterministic no-op. Scope-restricted and concurrency-guarded.
#
# SAFETY / SCOPE:
#   * requires an explicit --only <exact-version> (a single migration), or the explicit --all mode.
#   * migration names must match ^[0-9]{4}_[a-z0-9_]+$; ambiguous / absent selections are rejected.
#   * the selected file + its SHA-256 are printed before applying (and belong in the deployment evidence).
#   * the version string is pattern-validated before any use in SQL (never interpolate an unvalidated name).
#   * a session advisory lock (pg_try_advisory_lock) serialises concurrent runners so two cannot apply the
#     same migration simultaneously; PostgreSQL DDL locking + the schema_migrations PK are the hard backstop.
#   * the ONLY public-schema write performed by these migrations is the schema_migrations metadata row; no
#     public business/IAM table is altered by 0010.
#
# Usage:
#   EDGE_PSQL='docker exec -i <container> psql -U postgres -d <db> -v ON_ERROR_STOP=1' \
#     scripts/edge-migrate.sh --only 0010_phase3_stay_resolution
#   EDGE_PSQL='psql "$DSN" -v ON_ERROR_STOP=1' scripts/edge-migrate.sh --all
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
MIG_DIR="$HERE/data-plane/migrations"
ONLY=""; ALL=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dir)  MIG_DIR="$2"; shift 2;;
    --only) ONLY="$2"; shift 2;;
    --all)  ALL=1; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "${EDGE_PSQL:-}" ] || { echo "EDGE_PSQL not set (the psql command prefix reading SQL from stdin)"; exit 2; }
if [ -z "$ONLY" ] && [ "$ALL" -ne 1 ]; then
  echo "REFUSED: specify --only <exact-version> or the explicit --all mode" >&2; exit 2
fi
NAME_RE='^[0-9]{4}_[a-z0-9_]+$'
if [ -n "$ONLY" ]; then
  echo "$ONLY" | grep -Eq "$NAME_RE" || { echo "REFUSED: --only '$ONLY' does not match $NAME_RE" >&2; exit 2; }
fi

q(){ $EDGE_PSQL -tAqc "$1"; }

# ledger must exist (prod bootstraps it; create-if-absent is safe and idempotent)
q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null

# resolve the file set (exact match for --only; the whole ordered set for --all)
files=()
if [ -n "$ONLY" ]; then
  cand=("$MIG_DIR/$ONLY.up.sql")
  # reject ambiguity: an exact version must resolve to exactly one existing file
  n=0; for g in "$MIG_DIR/$ONLY".up.sql; do [ -e "$g" ] && n=$((n+1)); done
  [ "$n" -eq 1 ] || { echo "REFUSED: --only '$ONLY' resolves to $n files (need exactly 1)" >&2; exit 2; }
  files=("${cand[@]}")
else
  for f in "$MIG_DIR"/*.up.sql; do files+=("$f"); done
fi

applied=0; skipped=0
for f in "${files[@]}"; do
  [ -f "$f" ] || { echo "REFUSED: migration file not found: $f" >&2; exit 2; }
  base="$(basename "$f")"; ver="${base%.up.sql}"
  echo "$ver" | grep -Eq "$NAME_RE" || { echo "REFUSED: migration version '$ver' does not match $NAME_RE" >&2; exit 2; }
  sha="$(sha256sum "$f" | awk '{print $1}')"
  present="$(q "SELECT 1 FROM public.schema_migrations WHERE version='$ver';")"
  if [ "$present" = "1" ]; then
    echo "  skip  $ver (already applied)"; skipped=$((skipped+1)); continue
  fi
  echo "  apply $ver  file=$base  sha256=$sha"
  # concurrency guard + apply in ONE session: acquire a session-level advisory lock (fail fast with a
  # clean RAISE if a competing runner holds it), then apply the migration (its own BEGIN/COMMIT). The
  # session lock persists across the DO block's implicit commit and is released when psql exits.
  { printf "DO \$guard\$ BEGIN IF NOT pg_try_advisory_lock(hashtext('stayconnect_edge_migrate')) THEN RAISE EXCEPTION 'another edge-migrate runner is active'; END IF; END \$guard\$;\n"; cat "$f"; } | $EDGE_PSQL >/dev/null
  # belt-and-suspenders ledger record (no-op if the .sql self-recorded)
  q "INSERT INTO public.schema_migrations(version) VALUES ('$ver') ON CONFLICT DO NOTHING;" >/dev/null
  applied=$((applied+1))
done
echo "EDGE_MIGRATE_OK applied=$applied skipped=$skipped"
