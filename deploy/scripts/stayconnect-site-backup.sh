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
PGUSER_SITE="${STAYCONNECT_PGUSER:-stayconnect_site}"
PGDB_SITE="${STAYCONNECT_PGDB:-stayconnect_site}"
MARKER_NAME="financial-restore-generation.json"

STAMP="$(date -u +%Y%m%d-%H%M%S)"
DUMP="$OUT_DIR/site-$STAMP.dump"
ETC_TAR="$OUT_DIR/site-$STAMP-etc.tgz"

mkdir -p "$OUT_DIR"

echo "backup: pg_dump -> $DUMP"
pg_dump -Fc -U "$PGUSER_SITE" "$PGDB_SITE" -f "$DUMP"

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
