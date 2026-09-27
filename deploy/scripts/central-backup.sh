#!/usr/bin/env bash
# THE SCHEDULED LOCAL CENTRAL DATABASE BACKUP. Run daily by stayconnect-central-backup.timer.
#
# Writes   /opt/stayconnect/backups/db/central-<UTC stamp>-<reason>.dump   (pg_dump -Fc, 0600, dir 0700)
# where <reason> is "scheduled" here and "pre-<sha12>" for the dump central-deploy.sh takes before a deploy —
# one directory, one pattern, one retention policy.
#
# The dump is written to a temporary name, proven to be a readable archive (pg_restore -l), and only then
# renamed into place, so a half-written file can never be mistaken for the newest backup. Afterwards it runs
# stayconnect-backup-cleanup --apply, which applies KEEP_DB from /etc/stayconnect/backup-retention.conf (the
# newest dump is never deleted, pinned ones never) and rewrites /opt/stayconnect/backup-retention-status.json —
# the file the console's System → Backup health page reads through ctrlapi.
#
# LOCAL ONLY. A copy of this directory that leaves the host is the operator's responsibility: where it goes is
# a Product-Owner decision (docs/DEPLOYMENT_CLOUD.md §9). A backup on the same disk does not survive the host.
#
# Usage (as root on Central):  central-backup.sh [--reason <word>]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROG=central-backup
# shellcheck source=central-lib.sh
. "$HERE/central-lib.sh"

REASON=scheduled
while [ $# -gt 0 ]; do
  case "$1" in
    --reason) REASON="$2"; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
case "$REASON" in *[!a-zA-Z0-9._-]*|'') die "--reason must be a plain word" ;; esac
[ "$(id -u)" = 0 ] || die "run as root"
docker inspect "$CENTRAL_PG_CONTAINER" >/dev/null 2>&1 || die "no $CENTRAL_PG_CONTAINER container on this host"

install -d -m 0700 "$CENTRAL_DB_BACKUPS"
DUMP="$CENTRAL_DB_BACKUPS/central-$(date -u +%Y%m%dT%H%M%SZ)-$REASON.dump"
TMP="$DUMP.partial"
ERR="$(mktemp)"; trap 'rm -f "$TMP" "$ERR"' EXIT
umask 077

# pg_dump always warns about TimescaleDB's circular catalog FKs (harmless for a full dump); shown only on failure.
docker exec "$CENTRAL_PG_CONTAINER" pg_dump -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" -Fc > "$TMP" 2> "$ERR" \
  || { cat "$ERR" >&2; die "pg_dump failed — no backup written"; }
[ -s "$TMP" ] || die "pg_dump produced an empty file"
docker exec -i "$CENTRAL_PG_CONTAINER" pg_restore -l < "$TMP" > /dev/null 2>"$ERR" \
  || { cat "$ERR" >&2; die "the dump is not a readable archive — discarded"; }
chmod 0600 "$TMP"
mv -f "$TMP" "$DUMP"
say "backup $DUMP ($(du -h "$DUMP" | cut -f1))"
logger -t stayconnect-central-backup "backup $DUMP" 2>/dev/null || true

# Retention + the status the Backup health page shows. A retention problem does not un-make the backup.
if [ -x "$SC_OPT/bin/stayconnect-backup-cleanup" ]; then
  "$SC_OPT/bin/stayconnect-backup-cleanup" --apply >/dev/null 2>&1 \
    || warn "backup retention reported a problem — see /var/log/stayconnect/backup-cleanup.log and System → Backup health"
fi
