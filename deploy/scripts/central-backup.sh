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
ERR="$(mktemp)"
umask 077

# EVERY RUN IS RECORDED, SUCCESS OR FAILURE, in $SC_OPT/central-backup-status.json, which the console's Backup
# health page reads through ctrlapi. A failing daily dump used to be invisible there: the retention cleanup
# kept refreshing its own last_run whether or not a dump had been written. The previous success is kept when
# a run fails, so the page can say "failed; newest good backup is from <then>".
BACKUP_STATUS="${CENTRAL_BACKUP_STATUS:-$SC_OPT/central-backup-status.json}"
record_run() { # record_run ok|failed <file> <error>
  python3 - "$BACKUP_STATUS" "$1" "$2" "$3" <<'PY' || true
import json, os, sys, datetime
path, outcome, file, error = sys.argv[1:5]
now = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
try:
    d = json.load(open(path))
except Exception:
    d = {}
d['last_attempt_at'] = now
if outcome == 'ok':
    d['last_success_at'], d['last_file'], d['last_error'] = now, file, ''
else:
    d['last_failure_at'], d['last_error'] = now, error[:500]
tmp = path + '.tmp'
with open(tmp, 'w') as f:
    json.dump(d, f)
os.chmod(tmp, 0o644)
os.replace(tmp, path)
PY
}
on_exit() {
  rc=$?
  if [ "$rc" != 0 ]; then
    record_run failed "" "${FAIL_WHY:-backup exited with status $rc}: $(tail -c 300 "$ERR" 2>/dev/null | tr '\n' ' ')"
  fi
  rm -f "$TMP" "$ERR"
}
trap on_exit EXIT

# pg_dump always warns about TimescaleDB's circular catalog FKs (harmless for a full dump); shown only on failure.
docker exec "$CENTRAL_PG_CONTAINER" pg_dump -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" -Fc > "$TMP" 2> "$ERR" \
  || { cat "$ERR" >&2; FAIL_WHY="pg_dump failed"; die "pg_dump failed — no backup written"; }
[ -s "$TMP" ] || { FAIL_WHY="pg_dump produced an empty file"; die "pg_dump produced an empty file"; }
docker exec -i "$CENTRAL_PG_CONTAINER" pg_restore -l < "$TMP" > /dev/null 2>"$ERR" \
  || { cat "$ERR" >&2; FAIL_WHY="the dump is not a readable archive"; die "the dump is not a readable archive — discarded"; }
chmod 0600 "$TMP"
mv -f "$TMP" "$DUMP"
record_run ok "$DUMP" ""
say "backup $DUMP ($(du -h "$DUMP" | cut -f1))"
logger -t stayconnect-central-backup "backup $DUMP" 2>/dev/null || true

# Retention + the status the Backup health page shows. A retention problem does not un-make the backup.
if [ -x "$SC_OPT/bin/stayconnect-backup-cleanup" ]; then
  "$SC_OPT/bin/stayconnect-backup-cleanup" --apply >/dev/null 2>&1 \
    || warn "backup retention reported a problem — see /var/log/stayconnect/backup-cleanup.log and System → Backup health"
fi
