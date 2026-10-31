mkdir -p /etc/systemd/system/stayconnect-netd.service.d
printf '[Service]\nEnvironment=NETD_SHAPE_CLASSIFIER=u32\n' > /etc/systemd/system/stayconnect-netd.service.d/lab-u32.conf
systemctl daemon-reload
systemctl stop stayconnect-netd; systemctl reset-failed stayconnect-netd; systemctl start --no-block stayconnect-netd
sleep 30
echo "env in the running netd: $(tr '\0' '\n' < /proc/$(pidof netd)/environ | grep NETD_SHAPE)"
echo "br-g119 (flower sessions from before) classes=$(tc class show dev br-g119 | grep -c '^class htb') flower=$(tc filter show dev br-g119 parent 1: | grep -c flower) set=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c expires)"
journalctl -u stayconnect-netd --since -40sec --no-pager -o cat | grep -iE 'classifier|u32|flower' | head -3 | cut -c1-300
