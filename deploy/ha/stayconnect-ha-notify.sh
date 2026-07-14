#!/bin/sh
# keepalived state-change hook. Copy to /usr/local/bin/stayconnect-ha-notify
# and chmod 755. Called by keepalived as `notify_master/backup/fault` with
# a single arg: the new role.
#
# Responsibilities:
#   - drive conntrackd: primary-role when we become master, backup-role
#     when we become backup, bulk-resync on either transition
#   - log to journald so operators can correlate with scd / VRRP logs

set -e

role="$1"
logger -t stayconnect-ha -p daemon.info "transition → $role"

case "$role" in
  master)
    conntrackd -c        # commit remote cache into local kernel
    conntrackd -B        # bulk send local state to backups
    conntrackd -n        # primary role
    ;;
  backup|fault)
    conntrackd -t        # shift to backup role (catch-up)
    conntrackd -B        # broadcast our state one more time
    conntrackd -s        # flush external cache
    ;;
esac
