cd /root
chk() { echo "[$1] $(date +%T) bridges: $(ip -br link | awk '/^br-g/{print $1}' | tr '\n' ' ') | kea subnets: $(python3 /root/keaq.py | grep -o '10\.[0-9]*\.0\.0/[0-9]*' | tr '\n' ' ')"; }
echo "=== Rollback path B: operator rollback while pending"
OUT=$(python3 s19_apply.py apply "rollback B, operator"); echo "$OUT" | cut -c1-200
RID=$(echo "$OUT" | grep -o '"revision_id": "[^"]*"' | cut -d'"' -f4)
chk "pending"
python3 s19_apply.py rollback $RID | cut -c1-200
sleep 5; chk "after operator rollback"
python3 s19_apply.py status | head -n 2
echo "=== Failed health check: Kea stopped before the apply"
systemctl stop kea-dhcp4-server; sleep 2
OUT=$(python3 s19_apply.py apply "failed health check"); echo "$OUT" | cut -c1-420
sleep 8
systemctl is-active kea-dhcp4-server
chk "after failed apply"
python3 s19_apply.py status | head -n 2
systemctl start kea-dhcp4-server; sleep 5; systemctl is-active kea-dhcp4-server
chk "kea restarted"
