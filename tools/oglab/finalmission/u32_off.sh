journalctl -u stayconnect-netd --since "08:03:00" --until "08:05:00" --no-pager -o cat | grep -E "classifier forced|u32" | head -2 | cut -c1-250
B=br-g121-1e33
echo "before: classes=$(tc class show dev $B | grep -c '^class htb') u32=$(tc filter show dev $B parent 1: | grep -c ' u32 ') flower=$(tc filter show dev $B parent 1: | grep -c flower) set=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c expires)"
rm -f /etc/systemd/system/stayconnect-netd.service.d/lab-u32.conf; rmdir /etc/systemd/system/stayconnect-netd.service.d 2>/dev/null; systemctl daemon-reload
systemctl stop stayconnect-netd; systemctl reset-failed stayconnect-netd; systemctl start --no-block stayconnect-netd
sleep 75
echo "env: $(tr '\0' '\n' < /proc/$(pidof netd)/environ | grep -c NETD_SHAPE) vars"
echo "after:  classes=$(tc class show dev $B | grep -c '^class htb') u32=$(tc filter show dev $B parent 1: | grep -c ' u32 ') flower=$(tc filter show dev $B parent 1: | grep -c flower) set=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c expires)"
