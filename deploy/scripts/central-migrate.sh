#!/usr/bin/env bash
# CENTRAL SCHEMA MIGRATIONS — the same end state from a fresh install and from an upgrade.
#
# Until now Central's schema was applied by hand from a runbook ("apply 0001..00NN"). That works exactly
# once, on the host where somebody did it. It cannot tell you what a given server actually has, it cannot
# tell an upgrade from a fresh install, and every feature that adds a table discovers the gap the same way:
# a runtime error on a server nobody remembers migrating. 0044 (offline activation requests) is the feature
# that made that unacceptable, so the ledger exists now and covers every migration, not just the new one.
#
# WHAT IT GUARANTEES
#
#   Fresh install     applies 0001..N in order -> the current schema.
#   Existing Central  applies only what is missing -> the same schema. Never re-runs anything.
#   Either way        `verify` says so, and says which files are pending, by name.
#   Tamper-evident    a migration whose bytes changed after being applied is REFUSED, not silently skipped.
#                     Editing applied history is how two servers end up "at 0044" with different schemas.
#
# It does not invent a rollback story: .down.sql files exist and are the operator's tool, not this script's.
# An automatic down-migration is a good way to lose a customer's data during an incident.
#
# Usage:
#   central-migrate.sh status                    what is applied, what is pending  (default)
#   central-migrate.sh up [--dry-run]            apply everything pending, in order
#   central-migrate.sh verify                    exit non-zero unless fully migrated and unmodified
#   central-migrate.sh adopt --through 0043 --yes
#                                                record 0001..0043 as already applied WITHOUT running them,
#                                                for the pre-ledger Central that really does have them
#
# Connection (either form):
#   CENTRAL_DB_URL=postgres://user:pw@host/db central-migrate.sh up
#   central-migrate.sh --pg-exec "docker exec -i central-pg" --db stayconnect up
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS="${CENTRAL_MIGRATIONS_DIR:-$(cd "$HERE/../.." && pwd)/control-plane/migrations}"
PG_EXEC=""
DB=""
DSN="${CENTRAL_DB_URL:-}"
DRY=0
THROUGH=""
YES=0

say() { echo "[central-migrate] $*"; }
die() { echo "[central-migrate] ABORT: $*" >&2; exit 1; }

cmd=""
while [ $# -gt 0 ]; do
  case "$1" in
    status|up|verify|adopt) cmd="$1"; shift ;;
    --pg-exec) PG_EXEC="$2"; shift 2 ;;
    --db) DB="$2"; shift 2 ;;
    --dsn) DSN="$2"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    --through) THROUGH="$2"; shift 2 ;;
    --yes) YES=1; shift ;;
    -h|--help) sed -n '1,40p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
cmd="${cmd:-status}"

[ -d "$MIGRATIONS" ] || die "migrations directory not found: $MIGRATIONS"

# psql invocation. ON_ERROR_STOP is what turns "half a migration applied" into a failure instead of a
# success message followed by a broken server.
if [ -n "$PG_EXEC" ]; then
  [ -n "$DB" ] || die "--pg-exec also needs --db <database>"
  PSQL() { $PG_EXEC psql -v ON_ERROR_STOP=1 -U "${PGUSER:-stayconnect}" -d "$DB" "$@"; }
else
  [ -n "$DSN" ] || die "set CENTRAL_DB_URL or pass --dsn (or use --pg-exec with --db)"
  command -v psql >/dev/null || die "psql not found; use --pg-exec to run it inside a container"
  PSQL() { psql -v ON_ERROR_STOP=1 "$DSN" "$@"; }
fi
Q() { PSQL -tA -c "$1"; }

checksum() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

ensure_ledger() {
  PSQL -q -c "
    CREATE TABLE IF NOT EXISTS schema_migrations (
      version     text PRIMARY KEY,
      name        text NOT NULL,
      checksum    text NOT NULL,
      applied_at  timestamptz NOT NULL DEFAULT now(),
      applied_by  text,
      adopted     boolean NOT NULL DEFAULT false
    );
    COMMENT ON TABLE schema_migrations IS
      'Applied Central migrations. adopted=true means the row was recorded for a schema that predates this ledger, not run by it.';
  " >/dev/null
}

# versions_files lists "version<TAB>path" for every up-migration, in numeric order.
versions_files() {
  local f base
  for f in "$MIGRATIONS"/*.up.sql; do
    base="$(basename "$f")"
    printf '%s\t%s\n' "${base%%_*}" "$f"
  done | sort
}

applied_checksum() { Q "SELECT checksum FROM schema_migrations WHERE version='$1'"; }

report() {
  ensure_ledger
  local version file cs have pending=0 applied=0 drift=0
  while IFS=$'\t' read -r version file; do
    cs="$(checksum "$file")"
    have="$(applied_checksum "$version")"
    if [ -z "$have" ]; then
      printf '  %-6s PENDING   %s\n' "$version" "$(basename "$file")"
      pending=$((pending+1))
    elif [ "$have" != "$cs" ]; then
      printf '  %-6s MODIFIED  %s  <-- applied bytes differ from the file on disk\n' "$version" "$(basename "$file")"
      drift=$((drift+1))
    else
      printf '  %-6s applied   %s\n' "$version" "$(basename "$file")"
      applied=$((applied+1))
    fi
  done < <(versions_files)
  say "$applied applied, $pending pending, $drift modified"
  [ "$drift" = 0 ] || return 2
  [ "$pending" = 0 ] || return 1
  return 0
}

apply_one() {
  local version="$1" file="$2" cs; cs="$(checksum "$file")"
  local name; name="$(basename "$file")"
  if [ "$DRY" = 1 ]; then
    say "would apply $version  $name"
    return 0
  fi
  say "applying $version  $name"
  # SOME MIGRATIONS MANAGE THEIR OWN TRANSACTION and some do not. Wrapping a file that already says BEGIN
  # in --single-transaction makes psql warn and, worse, lets the file's COMMIT close the wrapper early. So
  # the file's own choice is respected, and the ledger row is written immediately after it succeeds.
  if grep -qiE '^[[:space:]]*BEGIN[[:space:]]*;' "$file"; then
    PSQL -q -f - < "$file"
  else
    PSQL -q --single-transaction -f - < "$file"
  fi
  PSQL -q -c "INSERT INTO schema_migrations (version, name, checksum, applied_by)
              VALUES ('$version', '$name', '$cs', '$(id -un 2>/dev/null || echo unknown)')
              ON CONFLICT (version) DO UPDATE SET checksum=EXCLUDED.checksum, name=EXCLUDED.name" >/dev/null
  say "  ok"
}

case "$cmd" in

status)
  say "migrations: $MIGRATIONS"
  set +e; report; rc=$?; set -e
  [ "$rc" != 2 ] || die "some applied migrations no longer match the files on disk (see MODIFIED above).
    An applied migration must never be edited: two servers would then both report the same version with
    different schemas. Add a NEW migration instead."
  exit 0
  ;;

up)
  ensure_ledger
  say "migrations: $MIGRATIONS"
  applied_any=0
  while IFS=$'\t' read -r version file; do
    cs="$(checksum "$file")"
    have="$(applied_checksum "$version")"
    if [ -n "$have" ] && [ "$have" != "$cs" ]; then
      die "$version has already been applied, but $(basename "$file") has changed since.
    Refusing to continue: re-running it could conflict with what is already in the database, and skipping it
    would leave this server silently different from the next one. Add a new migration instead."
    fi
    [ -z "$have" ] || continue
    apply_one "$version" "$file"
    applied_any=1
  done < <(versions_files)
  if [ "$applied_any" = 0 ]; then
    say "already up to date — nothing to apply"
  elif [ "$DRY" = 1 ]; then
    say "dry run: nothing was applied"
  else
    say "Central schema is up to date"
  fi
  ;;

verify)
  set +e; report; rc=$?; set -e
  case "$rc" in
    0) say "VERIFIED: Central schema is fully migrated and unmodified"; exit 0 ;;
    1) die "Central is NOT fully migrated (see PENDING above). Run: $0 up" ;;
    *) die "applied migrations no longer match the files on disk (see MODIFIED above)" ;;
  esac
  ;;

adopt)
  # THE ONE-TIME BRIDGE for a Central that was migrated by hand before this ledger existed. It records
  # history; it never runs anything and never touches the schema. Getting it wrong is recoverable in one
  # direction only, so it names exactly what it will record and insists on --yes.
  [ -n "$THROUGH" ] || die "adopt needs --through <version>, e.g. --through 0043"
  ensure_ledger
  say "these migrations will be RECORDED AS ALREADY APPLIED, without being run:"
  n=0
  while IFS=$'\t' read -r version file; do
    if [ "$version" \> "$THROUGH" ]; then continue; fi
    if [ -n "$(applied_checksum "$version")" ]; then continue; fi
    printf '  %-6s %s\n' "$version" "$(basename "$file")"
    n=$((n+1))
  done < <(versions_files)
  [ "$n" != 0 ] || { say "nothing to adopt"; exit 0; }
  say ""
  say "Only do this if this database genuinely already has all of the above."
  say "Anything AFTER $THROUGH stays pending and will be applied normally by '$0 up'."
  [ "$YES" = 1 ] || die "re-run with --yes to confirm"
  while IFS=$'\t' read -r version file; do
    if [ "$version" \> "$THROUGH" ]; then continue; fi
    if [ -n "$(applied_checksum "$version")" ]; then continue; fi
    PSQL -q -c "INSERT INTO schema_migrations (version, name, checksum, applied_by, adopted)
                VALUES ('$version', '$(basename "$file")', '$(checksum "$file")',
                        '$(id -un 2>/dev/null || echo unknown)', true)
                ON CONFLICT (version) DO NOTHING" >/dev/null
  done < <(versions_files)
  say "adopted through $THROUGH — now run '$0 status' and apply the rest with '$0 up'"
  ;;

esac
