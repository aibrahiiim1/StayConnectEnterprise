#!/usr/bin/env bash
# Self-test for the nightly site-backup retention in stayconnect-backup-cleanup.sh (audit N-9 / decision D-H).
#
# Runs the REAL script against a sandbox (SC_CLEANUP_* overrides) and asserts:
#   - sets beyond the newest SITE_BACKUP_KEEP that are older than SITE_BACKUP_DAYS are deleted, all three files;
#   - the newest SITE_BACKUP_KEEP sets are kept however old they are;
#   - sets younger than SITE_BACKUP_DAYS are kept;
#   - a pinned set is kept;
#   - nothing else in the directory is touched: subdirectories, look-alike names, other files;
#   - every site-backup artefact ends up 0600 and the directory 0700;
#   - a policy file asking for fewer than 7 kept sets is treated as 7;
#   - sets encrypted to the hotel's backup keys (.age, GDPR plan C5) follow the same rule;
#   - the appliance's own database dumps, clear or encrypted, are removed past DB_BACKUP_DAYS, the newest kept;
#   - dry-run deletes nothing.
# Needs bash, GNU coreutils and GNU findutils. Touches nothing outside its temporary directory.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/stayconnect-backup-cleanup.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

ROOT="$T/opt"; SITE="$T/backups"; mkdir -p "$ROOT/backups/db" "$ROOT/bin" "$ROOT/releases/hotel-admin/r1" "$ROOT/releases/hotel-admin/r2" "$SITE/compliance" "$T/log"
ln -s "$ROOT/releases/hotel-admin/r2" "$ROOT/hotel-admin"
ln -s "$ROOT/releases/hotel-admin/r1" "$ROOT/hotel-admin.previous"

# 12 nightly sets, newest first. The newest seven include four OLDER than 30 days: they are kept by the floor.
days=(0 1 2 35 36 37 38 40 41 44 50 60)
for d in "${days[@]}"; do
  stamp="$(date -u -d "-$d days" +%Y%m%d-%H%M%S)"
  # Days 1 and 44 are encrypted sets: the dump and the /etc archive carry .age.
  e=""; case "$d" in 1|44) e=".age";; esac
  for f in "site-$stamp.dump$e" "site-$stamp.dump.meta.json" "site-$stamp-etc.tgz$e"; do
    echo x > "$SITE/$f"; chmod 0644 "$SITE/$f"
    # The 60-day set is COPIED BACK: its stamp says 60 days, its mtime says now. Age comes from the stamp.
    if [ "$d" = 60 ]; then touch "$SITE/$f"; else touch -d "-$d days" "$SITE/$f"; fi
  done
  echo "$d $stamp" >> "$T/stamps"
done
# never touched: a subdirectory, look-alikes, and an unrelated old file
echo keep > "$SITE/compliance/old.json"; touch -d "-400 days" "$SITE/compliance/old.json"
echo keep > "$SITE/site-latest.dump";     touch -d "-400 days" "$SITE/site-latest.dump"
echo keep > "$SITE/phase1b.dump";         touch -d "-400 days" "$SITE/phase1b.dump"
# pin the 41-day set
pinned_stamp="$(awk '$1==41{print $2}' "$T/stamps")"
echo "$SITE/site-$pinned_stamp.dump" > "$T/pins"

# The appliance's own database dumps: the newest (encrypted, from today) must stay; a recent clear dump stays;
# a 40-day clear dump and a 45-day encrypted safety dump go.
dbstamp() { date -u -d "-$1 days" +%Y%m%dT%H%M%SZ; }
DB="$ROOT/backups/db"
echo x > "$DB/db-$(dbstamp 3).sql.gz";             touch -d "-3 days"  "$DB/db-$(dbstamp 3).sql.gz"
echo x > "$DB/db-$(dbstamp 40).sql.gz";            touch -d "-40 days" "$DB/db-$(dbstamp 40).sql.gz"
echo x > "$DB/db-safety-$(dbstamp 45).sql.gz.age"; touch -d "-45 days" "$DB/db-safety-$(dbstamp 45).sql.gz.age"
NEWEST_DB="$DB/db-$(dbstamp 2).sql.gz.age"; echo x > "$NEWEST_DB"; touch "$NEWEST_DB"
# (the newest by time is NEWEST_DB; it is kept whatever DB_BACKUP_DAYS says)

run() {
  SC_CLEANUP_CONF="$T/conf" SC_CLEANUP_ROOT="$ROOT" SC_CLEANUP_PINS="$T/pins" SC_CLEANUP_LOGDIR="$T/log" \
    SC_CLEANUP_SITE_DIR="$SITE" bash "$SCRIPT" "$@" >"$T/out" 2>&1
}

# a policy that tries to keep fewer than seven: clamped to seven
printf 'SITE_BACKUP_DAYS=30\nSITE_BACKUP_KEEP=2\n' > "$T/conf"

run   # dry-run
n_before="$(ls "$SITE" | grep -c '^site-[0-9]')"
[ "$n_before" = 36 ] && ok "dry-run deleted nothing" || bad "dry-run changed the directory ($n_before files)"

run --apply
present() { { [ -f "$SITE/site-$1.dump" ] || [ -f "$SITE/site-$1.dump.age" ]; } && [ -f "$SITE/site-$1.dump.meta.json" ]             && { [ -f "$SITE/site-$1-etc.tgz" ] || [ -f "$SITE/site-$1-etc.tgz.age" ]; }; }
gone()    { ! ls "$SITE" | grep -q "site-$1"; }
while read -r d stamp; do
  case "$d" in
    0|1|2|35|36|37|38) present "$stamp" && ok "one of the newest seven (day $d) kept, whatever its age" || bad "one of the newest seven (day $d) was removed" ;;
    41)            present "$stamp" && ok "pinned set kept" || bad "the pinned set was removed" ;;
    *)             gone "$stamp" && ok "set $d days old removed (all three files)" || bad "set $d days old survived: $(ls "$SITE" | grep "$stamp")" ;;
  esac
done < "$T/stamps"
for f in compliance/old.json site-latest.dump phase1b.dump; do
  [ -f "$SITE/$f" ] && ok "untouched: $f" || bad "deleted something that is not a nightly set: $f"
done
perm_bad="$(find "$SITE" -maxdepth 1 -type f -name 'site-2*' ! -perm 0600 | wc -l)"
[ "$perm_bad" = 0 ] && ok "every site-backup artefact is 0600" || bad "$perm_bad artefact(s) are not 0600"
[ "$(stat -c %a "$SITE")" = 700 ] && ok "the directory is 0700" || bad "the directory is $(stat -c %a "$SITE")"
[ "$(stat -c %a "$SITE/phase1b.dump")" = 644 ] && ok "permissions of other files untouched" || bad "another file's mode was changed"

[ -f "$NEWEST_DB" ] && ok "the newest database dump kept (encrypted)" || bad "the newest database dump was removed"
[ -f "$DB/db-$(dbstamp 3).sql.gz" ] && ok "a 3-day database dump kept" || bad "a 3-day database dump was removed"
[ ! -e "$DB/db-$(dbstamp 40).sql.gz" ] && ok "a 40-day database dump removed (DB_BACKUP_DAYS)" || bad "a 40-day database dump survived"
[ ! -e "$DB/db-safety-$(dbstamp 45).sql.gz.age" ] && ok "a 45-day encrypted safety dump removed" || bad "a 45-day encrypted safety dump survived"

[ "$fail" = 0 ] && { echo "ALL PASS"; exit 0; }
echo "--- script output ---"; cat "$T/out"; exit 1
