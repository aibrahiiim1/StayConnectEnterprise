#!/usr/bin/env bash
# RETIRED (GDPR final mission: one backup system). This used to take a nightly pg_dump -Fc set of the site database and an archive of
# /etc/stayconnect, run by stayconnect-site-backup.timer. There is now ONE backup system, in scd, used from Hotel Admin -> Backups
# (encrypted to the backup key holders, proven restorable, noted, rotated by count; scheduled backups are off until an administrator
# switches them on). This file remains ONLY because immutable governance receipts cite it as evidence of what was once delivered; it
# takes no backup, installs nowhere (no unit references it) and refuses to run.
#
# What the script left behind (/var/backups/stayconnect/site-*) are "earlier backups": kept, listed in Hotel Admin, removed only by an
# administrator. See docs/BACKUP_AND_RESTORE.md.
echo "stayconnect-site-backup is retired. Use Hotel Admin -> Backups, or: /opt/stayconnect/bin/scd backup-now --reason \"<why>\"" >&2
exit 2
