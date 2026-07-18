#!/usr/bin/env bash
# Authoritative edge-DB (site-local) migration runner for data-plane/migrations/NNNN_*.up.sql.
# IDEMPOTENT: applies, in filename order, only the migrations whose version is not already recorded in
# public.schema_migrations; re-running is a deterministic no-op. Each .up.sql also self-records its
# version (ON CONFLICT DO NOTHING), so a raw double-apply is prevented AND the runner skips cleanly.
#
# The runner never invents behaviour: it reads real files and the real ledger. It does not touch
# Production unless EDGE_PSQL is pointed at Production (the caller owns that; the CI/test callers point
# it at a disposable container only).
#
# Usage:
#   EDGE_PSQL='docker exec -i <container> psql -U postgres -d <db> -v ON_ERROR_STOP=1' \
#     scripts/edge-migrate.sh [--dir data-plane/migrations] [--only <version-or-prefix>]
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
MIG_DIR="$HERE/data-plane/migrations"
ONLY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dir)  MIG_DIR="$2"; shift 2;;
    --only) ONLY="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "${EDGE_PSQL:-}" ] || { echo "EDGE_PSQL not set (the psql command prefix reading SQL from stdin)"; exit 2; }

q(){ $EDGE_PSQL -tAqc "$1"; }
applyf(){ $EDGE_PSQL < "$1"; }

# ledger must exist (prod bootstraps it; create-if-absent is safe and idempotent)
q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null

applied=0; skipped=0
for f in "$MIG_DIR"/*.up.sql; do
  base="$(basename "$f")"; ver="${base%.up.sql}"
  [ -n "$ONLY" ] && case "$ver" in "$ONLY"*) : ;; *) continue;; esac
  present="$(q "SELECT 1 FROM public.schema_migrations WHERE version='$ver';")"
  if [ "$present" = "1" ]; then
    echo "  skip  $ver (already applied)"; skipped=$((skipped+1)); continue
  fi
  echo "  apply $ver"
  applyf "$f" >/dev/null
  # belt-and-suspenders: record even if the .sql did not self-record
  q "INSERT INTO public.schema_migrations(version) VALUES ('$ver') ON CONFLICT DO NOTHING;" >/dev/null
  applied=$((applied+1))
done
echo "EDGE_MIGRATE_OK applied=$applied skipped=$skipped"
