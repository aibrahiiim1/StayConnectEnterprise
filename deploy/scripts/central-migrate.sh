#!/usr/bin/env bash
# CENTRAL SCHEMA MIGRATIONS — the same end state from a fresh install and from an upgrade.
#
# Central's schema used to be applied from a runbook ("apply 0001..00NN"). That works exactly once, on the
# host where somebody did it. It cannot tell you what a given server actually has, it cannot tell an upgrade
# from a fresh install, and every feature that adds a table discovers the gap the same way: a runtime error
# on a server nobody remembers migrating. 0044 (offline activation requests) is the feature that made that
# unacceptable, so the ledger covers every migration, not just the new one.
#
# IT ADOPTS THE LEDGER THAT IS ALREADY IN PRODUCTION rather than inventing a second one. The live Central
# keeps `schema_migrations(version, applied_at)` keyed by the FULL migration filename, maintained by hand
# and incomplete. This script keys by the same full filename -- a second convention keyed on the numeric
# prefix would have seen every existing row as a different migration and cheerfully re-applied all of them --
# and adds the columns it needs with ADD COLUMN IF NOT EXISTS, so the real history is preserved in place.
#
# WHAT IT GUARANTEES
#
#   Fresh install     applies 0001..N in order -> the current schema.
#   Existing Central  applies only what is missing -> the same schema. Never re-runs anything.
#   Hand-kept rows    a row with no checksum is treated as APPLIED (it is real history) and its checksum is
#                     backfilled, marked adopted so it is never mistaken for something this tool ran.
#   Either way        `verify` says so, and names what is pending.
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
#                                                record everything up to 0043 as already applied WITHOUT
#                                                running it — for a schema that predates this ledger.
#                                                VERIFY THE SCHEMA FIRST; this tool records history, it
#                                                cannot check that history is true.
#
# Connection (either form):
#   CENTRAL_DB_URL=postgres://user:pw@host/db central-migrate.sh up
#   central-migrate.sh --pg-exec "docker exec -i sc-central-pg" --db stayconnect up
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
# PSQL NEVER READS THIS SCRIPT'S STDIN. PSQLIN is the one that does.
#
# Every enumeration here is a `while read ... done < <(versions_files)` loop, and `docker exec -i psql`
# reads stdin whether or not it has anything to do with it. A query issued inside the loop therefore ate the
# remaining migration list, and the loop ended after its first iteration -- reporting "1 applied, 0 pending"
# on a database that was missing a migration. It looked like a clean, fully-migrated server.
if [ -n "$PG_EXEC" ]; then
  [ -n "$DB" ] || die "--pg-exec also needs --db <database>"
  PSQL()   { $PG_EXEC psql -v ON_ERROR_STOP=1 -U "${PGUSER:-stayconnect}" -d "$DB" "$@" </dev/null; }
  PSQLIN() { $PG_EXEC psql -v ON_ERROR_STOP=1 -U "${PGUSER:-stayconnect}" -d "$DB" "$@"; }
else
  [ -n "$DSN" ] || die "set CENTRAL_DB_URL or pass --dsn (or use --pg-exec with --db)"
  command -v psql >/dev/null || die "psql not found; use --pg-exec to run it inside a container"
  PSQL()   { psql -v ON_ERROR_STOP=1 "$DSN" "$@" </dev/null; }
  PSQLIN() { psql -v ON_ERROR_STOP=1 "$DSN" "$@"; }
fi
Q() { PSQL -tA -c "$1"; }

checksum() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# ensure_ledger creates the ledger on a fresh database and UPGRADES an existing one in place. The live
# Central's table has only (version, applied_at); the extra columns are added, never recreated, so its 32
# rows of genuine history survive.
ensure_ledger() {
  PSQL -q -c "
    CREATE TABLE IF NOT EXISTS schema_migrations (
      version     text PRIMARY KEY,
      applied_at  timestamptz NOT NULL DEFAULT now()
    );
    ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS name       text;
    ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum   text;
    ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS applied_by text;
    ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS adopted    boolean NOT NULL DEFAULT false;
    COMMENT ON TABLE schema_migrations IS
      'Applied Central migrations, keyed by migration filename. adopted=true means the row records a schema this ledger did not itself apply.';
  " >/dev/null
}

# versions_files lists "version<TAB>path" in order. VERSION IS THE FULL FILENAME STEM, matching the
# convention already in production.
versions_files() {
  local f base
  for f in "$MIGRATIONS"/*.up.sql; do
    base="$(basename "$f")"
    printf '%s\t%s\n' "${base%.up.sql}" "$f"
  done | sort
}

# row_state prints MISSING (never applied), NOCHECKSUM (recorded by hand before this ledger existed), or
# the stored checksum.
row_state() {
  local out
  out="$(Q "SELECT COALESCE(NULLIF(checksum,''),'NOCHECKSUM') FROM schema_migrations WHERE version='$1'")"
  [ -n "$out" ] || out="MISSING"
  printf '%s' "$out"
}

backfill_checksum() {
  PSQL -q -c "UPDATE schema_migrations SET checksum='$2', name=COALESCE(name,'$1.up.sql'), adopted=true
              WHERE version='$1' AND COALESCE(checksum,'')=''" >/dev/null
}

report() {
  ensure_ledger
  local version file cs st pending=0 applied=0 drift=0 nocs=0
  while IFS=$'\t' read -r version file; do
    cs="$(checksum "$file")"
    st="$(row_state "$version")"
    case "$st" in
      MISSING)    printf '  PENDING     %s\n' "$version"; pending=$((pending+1)) ;;
      NOCHECKSUM) printf '  recorded    %s   (pre-ledger row, checksum not yet backfilled)\n' "$version"
                  applied=$((applied+1)); nocs=$((nocs+1)) ;;
      "$cs")      printf '  applied     %s\n' "$version"; applied=$((applied+1)) ;;
      *)          printf '  MODIFIED    %s   <-- applied bytes differ from the file on disk\n' "$version"
                  drift=$((drift+1)) ;;
    esac
  done < <(versions_files)
  say "$applied applied, $pending pending, $drift modified${nocs:+, $nocs awaiting checksum backfill}"
  [ "$drift" = 0 ] || return 2
  [ "$pending" = 0 ] || return 1
  return 0
}

apply_one() {
  local version="$1" file="$2" cs; cs="$(checksum "$file")"
  if [ "$DRY" = 1 ]; then
    say "would apply $version"
    return 0
  fi
  say "applying $version"
  # SOME MIGRATIONS MANAGE THEIR OWN TRANSACTION and some do not. Wrapping a file that already says BEGIN
  # in --single-transaction makes psql warn and, worse, lets the file's COMMIT close the wrapper early. So
  # the file's own choice is respected, and the ledger row is written immediately after it succeeds.
  if grep -qiE '^[[:space:]]*BEGIN[[:space:]]*;' "$file"; then
    PSQLIN -q -f - < "$file"
  else
    PSQLIN -q --single-transaction -f - < "$file"
  fi
  PSQL -q -c "INSERT INTO schema_migrations (version, name, checksum, applied_by, adopted)
              VALUES ('$version', '$version.up.sql', '$cs', '$(id -un 2>/dev/null || echo unknown)', false)
              ON CONFLICT (version) DO UPDATE SET checksum=EXCLUDED.checksum, name=EXCLUDED.name,
                                                 applied_by=EXCLUDED.applied_by, adopted=false" >/dev/null
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
    st="$(row_state "$version")"
    case "$st" in
      MISSING)
        apply_one "$version" "$file"
        applied_any=1 ;;
      NOCHECKSUM)
        # Real history kept by hand before this ledger existed. It is applied; record what it was applied
        # with so the tamper check has something to compare against from here on.
        if [ "$DRY" = 0 ]; then backfill_checksum "$version" "$cs"; fi
        say "recorded (pre-ledger): $version — checksum backfilled" ;;
      "$cs")
        : ;;
      *)
        die "$version has already been applied, but $version.up.sql has changed since.
    Refusing to continue: re-running it could conflict with what is already in the database, and skipping it
    would leave this server silently different from the next one. Add a new migration instead." ;;
    esac
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
  # THE ONE-TIME BRIDGE for a schema that was migrated before this ledger tracked it. It records history; it
  # never runs anything and never touches the schema. IT CANNOT CHECK THAT THE HISTORY IS TRUE -- verify the
  # schema yourself first, object by object. Getting it wrong is recoverable in one direction only, so it
  # names exactly what it will record and insists on --yes.
  [ -n "$THROUGH" ] || die "adopt needs --through <version>, e.g. --through 0043"
  ensure_ledger
  say "these migrations will be RECORDED AS ALREADY APPLIED, without being run:"
  n=0
  while IFS=$'\t' read -r version file; do
    if [ "${version%%_*}" \> "${THROUGH%%_*}" ]; then continue; fi
    if [ "$(row_state "$version")" != "MISSING" ]; then continue; fi
    printf '  %s\n' "$version"
    n=$((n+1))
  done < <(versions_files)
  if [ "$n" = 0 ]; then say "nothing to adopt"; exit 0; fi
  say ""
  say "Only do this if this database genuinely already has all of the above."
  say "Anything after ${THROUGH%%_*} stays pending and will be applied normally by '$0 up'."
  [ "$YES" = 1 ] || die "re-run with --yes to confirm"
  while IFS=$'\t' read -r version file; do
    if [ "${version%%_*}" \> "${THROUGH%%_*}" ]; then continue; fi
    if [ "$(row_state "$version")" != "MISSING" ]; then continue; fi
    PSQL -q -c "INSERT INTO schema_migrations (version, name, checksum, applied_by, adopted)
                VALUES ('$version', '$version.up.sql', '$(checksum "$file")',
                        '$(id -un 2>/dev/null || echo unknown)', true)
                ON CONFLICT (version) DO NOTHING" >/dev/null
  done < <(versions_files)
  say "adopted through ${THROUGH%%_*} — now run '$0 status' and apply the rest with '$0 up'"
  ;;

esac
