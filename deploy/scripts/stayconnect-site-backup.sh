#!/usr/bin/env bash
# stayconnect-site-backup.sh — the documented site backup, implemented.
#
# docs/BACKUP_AND_RESTORE.md §1 has always described this as two commands: a pg_dump of stayconnect_site and
# a tar of /etc/stayconnect. It was a snippet in a document, which meant the ONE exclusion that matters was
# nowhere enforced.
#
# THE EXCLUSION. /etc/stayconnect now holds the financial restore marker
# (financial-restore-generation.json). The marker's whole purpose is to survive a database restore and keep
# counting forward, so that a restored database can be recognised as older than the appliance knows it
# should be. A tar of /etc that CONTAINS the marker destroys that property: restoring the tar rolls the
# marker back to whatever it was when the backup was taken, and the restored database then matches it
# perfectly. The rollback detector would go quiet at exactly the moment it is needed.
#
# So the marker is excluded here, and the restore path below never writes it. It is deliberately NOT backed
# up: it is not data about the hotel, it is a counter describing THIS appliance's restore history, and the
# only correct value for it is the current one.
#
# Migration 0025 also detects the opposite direction -- a marker BEHIND the database -- and holds money
# movement, so an /etc restore taken before this script existed still fails safe rather than silently.
set -euo pipefail

OUT_DIR="${STAYCONNECT_BACKUP_DIR:-/var/backups/stayconnect}"
ETC_DIR="${STAYCONNECT_ETC_DIR:-/etc/stayconnect}"
PGDB_SITE="${STAYCONNECT_PGDB:-stayconnect_site}"
PG_CONTAINER="${STAYCONNECT_PG_CONTAINER:-stayconnect-pg}"
MARKER_NAME="financial-restore-generation.json"

# WHERE pg_dump COMES FROM, and why this is not a detail.
#
# MEASURED ON THE DEVELOPMENT APPLIANCE (WS-L, 2026-08-13): PostgreSQL runs in the `stayconnect-pg`
# container at 16.3, while the host carries the distribution client at 14.23. The earlier version of this
# script called the host `pg_dump` directly and would have aborted:
#
#     pg_dump: error: server version: 16.3; pg_dump version: 14.23
#     pg_dump: error: aborting because of server version mismatch
#
# A backup procedure that cannot run is worse than none, because the runbook says a backup was taken. It
# also defaulted PGUSER to `stayconnect_site`, which is the DATABASE name on this appliance and not a role
# that exists.
#
# So the dump is taken by the client that SHIPS WITH THE SERVER whenever the server is containerised, and
# the host client is used only when there is no container. Either way the major versions are compared
# before anything is written, because "the backup file exists" and "the backup file is a backup" are not
# the same claim.
# WHICH ROLE. A full backup has to read every schema, so a least-privilege SERVICE role is the wrong
# credential: measured on the development appliance, dumping as svc_edged fails with
# "permission denied for table schema_migrations" -- correctly, because that role is not supposed to read
# the whole database. The DSN is therefore taken from the administrative entry that already points at the
# site database, and an explicit STAYCONNECT_PGUSER always wins.
PGUSER_SITE="${STAYCONNECT_PGUSER:-}"
if [ -z "$PGUSER_SITE" ]; then
  for envf in /etc/stayconnect/ctrlapi.env /etc/stayconnect/edged.env /etc/stayconnect/scd.env; do
    [ -f "$envf" ] || continue
    dsn="$(grep -oE "postgres://[^ ]*/$PGDB_SITE(\?[^ ]*)?" "$envf" | head -1)" || true
    [ -n "${dsn:-}" ] || continue
    PGUSER_SITE="$(printf '%s' "$dsn" | sed -E 's#postgres://([^:]+):.*#\1#')"
    [ -n "${PGPASSWORD:-}" ] || PGPASSWORD="$(printf '%s' "$dsn" | sed -E 's#postgres://[^:]+:([^@]*)@.*#\1#')"
    export PGPASSWORD
    break
  done
fi
[ -n "$PGUSER_SITE" ] || { echo "backup: FAILED — no database role to dump as (set STAYCONNECT_PGUSER)" >&2; exit 1; }

STAMP="$(date -u +%Y%m%d-%H%M%S)"
DUMP="$OUT_DIR/site-$STAMP.dump"
ETC_TAR="$OUT_DIR/site-$STAMP-etc.tgz"

mkdir -p "$OUT_DIR"

USE_CONTAINER=0
if command -v docker >/dev/null 2>&1 && docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
  USE_CONTAINER=1
fi

if [ "$USE_CONTAINER" = 1 ]; then
  SRV_MAJOR="$(docker exec "$PG_CONTAINER" psql -U "$PGUSER_SITE" -d "$PGDB_SITE" -qAt                  -c 'SHOW server_version' </dev/null 2>/dev/null | cut -d. -f1)"
  CLI_MAJOR="$(docker exec "$PG_CONTAINER" pg_dump --version </dev/null 2>/dev/null                  | grep -oE '[0-9]+' | head -1)"
else
  SRV_MAJOR="$(psql -U "$PGUSER_SITE" -d "$PGDB_SITE" -qAt -c 'SHOW server_version' 2>/dev/null | cut -d. -f1)"
  CLI_MAJOR="$(pg_dump --version 2>/dev/null | grep -oE '[0-9]+' | head -1)"
fi
if [ -z "$SRV_MAJOR" ] || [ -z "$CLI_MAJOR" ]; then
  echo "backup: FAILED — could not establish the server and client versions" >&2; exit 1
fi
if [ "$CLI_MAJOR" -lt "$SRV_MAJOR" ]; then
  echo "backup: FAILED — pg_dump $CLI_MAJOR cannot dump a PostgreSQL $SRV_MAJOR server." >&2
  echo "backup: run this where the server's own client is available, or set STAYCONNECT_PG_CONTAINER." >&2
  exit 1
fi
echo "backup: pg_dump $CLI_MAJOR -> server $SRV_MAJOR (container=$USE_CONTAINER) -> $DUMP"

if [ "$USE_CONTAINER" = 1 ]; then
  # Streamed to the host over stdout so the artefact lands in OUT_DIR and nothing is left inside the
  # container to be forgotten about.
  docker exec -e PGPASSWORD="${PGPASSWORD:-}" "$PG_CONTAINER"     pg_dump -Fc -U "$PGUSER_SITE" "$PGDB_SITE" </dev/null > "$DUMP"
else
  pg_dump -Fc -U "$PGUSER_SITE" "$PGDB_SITE" -f "$DUMP"
fi
[ -s "$DUMP" ] || { echo "backup: FAILED — the dump is empty" >&2; rm -f "$DUMP"; exit 1; }

echo "backup: tar $ETC_DIR -> $ETC_TAR (EXCLUDING $MARKER_NAME)"
tar czf "$ETC_TAR" --exclude="$MARKER_NAME" -C "$(dirname "$ETC_DIR")" "$(basename "$ETC_DIR")"

# Prove the exclusion rather than trusting the flag. A tar that quietly included the marker would be a
# backup that disarms the rollback detector, and that is not something to discover during a restore.
if tar tzf "$ETC_TAR" | grep -q "$MARKER_NAME"; then
  echo "backup: FAILED — the financial restore marker is inside the /etc archive." >&2
  echo "backup: restoring that archive would roll the marker back and silence rollback detection." >&2
  rm -f "$ETC_TAR"
  exit 1
fi
echo "backup: verified — the financial restore marker is not in the archive"

DUMP_SHA="$(sha256sum "$DUMP" | awk '{print $1}')"
cat > "$DUMP.meta.json" <<JSON
{
  "dump_sha256": "$DUMP_SHA",
  "backup_taken_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "database": "$PGDB_SITE",
  "etc_archive": "$(basename "$ETC_TAR")",
  "marker_excluded": true,
  "note": "This metadata is NOT a restore manifest. A supported restore needs a manifest SIGNED by the appliance's pinned registry root; see stayconnect-financial-restore.sh."
}
JSON

echo "backup: complete"
echo "backup:   dump      $DUMP  ($DUMP_SHA)"
echo "backup:   /etc      $ETC_TAR"
echo "backup:   metadata  $DUMP.meta.json"
echo
echo "To restore this artefact, a signed restore manifest is required. The manifest is signed off-appliance"
echo "with the registry root key and verified against the appliance's own pinned anchor; the restore tool"
echo "will not accept a key supplied on its command line."
