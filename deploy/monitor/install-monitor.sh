#!/usr/bin/env bash
# Install, roll back or remove the OneGate monitor on an appliance (root). It replaces the earlier ad-hoc ogcheck timer and
# keeps writing to /var/log/ogcheck.log. Changes nothing the product uses: no configuration, network, database or service.
#
#   install-monitor.sh            install or upgrade, disable the old ogcheck timer, run one quick sweep
#   install-monitor.sh --rollback stop the new timers and re-enable the old ogcheck timer (left installed on purpose)
#   install-monitor.sh --remove   stop and remove the new monitor; the log and the old timer are left as they are
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST=/opt/stayconnect/monitor
UNITS="onegate-monitor-quick.service onegate-monitor-quick.timer onegate-monitor-deep.service onegate-monitor-deep.timer"
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

case "${1:-install}" in
  --rollback)
    systemctl disable --now onegate-monitor-quick.timer onegate-monitor-deep.timer 2>/dev/null || true
    if [ -f /etc/systemd/system/ogcheck.timer ]; then systemctl enable --now ogcheck.timer; echo "rolled back: ogcheck.timer active"; else echo "rolled back: new timers stopped (no earlier ogcheck timer on this appliance)"; fi
    exit 0 ;;
  --remove)
    systemctl disable --now onegate-monitor-quick.timer onegate-monitor-deep.timer 2>/dev/null || true
    for u in $UNITS; do rm -f "/etc/systemd/system/$u"; done
    systemctl daemon-reload
    rm -rf -- "$DEST"
    echo "removed; state kept in /var/lib/onegate-monitor"
    exit 0 ;;
  install) ;;
  *) echo "usage: $0 [--rollback|--remove]" >&2; exit 2 ;;
esac

command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' || { echo "python3 3.9 or newer is required" >&2; exit 1; }
install -d -m 0755 "$DEST"
install -m 0755 "$HERE/onegate_monitor.py" "$DEST/onegate_monitor.py"
install -m 0644 "$HERE/README.md" "$DEST/README.md"
install -d -m 0700 /var/lib/onegate-monitor
for u in $UNITS; do install -m 0644 "$HERE/$u" "/etc/systemd/system/$u"; done
[ -f /etc/logrotate.d/ogcheck ] || install -m 0644 "$HERE/logrotate-ogcheck" /etc/logrotate.d/ogcheck
systemctl daemon-reload
if systemctl is-enabled -q ogcheck.timer 2>/dev/null || systemctl is-active -q ogcheck.timer 2>/dev/null; then
  systemctl disable --now ogcheck.timer
  echo "the earlier ogcheck timer is disabled (kept for --rollback)"
fi
python3 "$DEST/onegate_monitor.py" quick
tail -n 1 /var/log/ogcheck.log | cut -c1-200
systemctl enable --now onegate-monitor-quick.timer onegate-monitor-deep.timer
systemctl list-timers --no-pager onegate-monitor-quick.timer onegate-monitor-deep.timer | head -4
echo "installed: $(sha256sum "$DEST/onegate_monitor.py" | cut -c1-16)"
