echo "== baseline $(date +%T)"
ip -4 -br addr
ip -4 route
nft list ruleset | sha256sum
nft list table inet stayconnect > /root/ev/s19-before.nft
for d in $(ip -br link | awk '/^br-g/{print $1}'); do echo "$d classes=$(tc class show dev $d | grep -c '^class htb') flower=$(tc filter show dev $d parent 1: 2>/dev/null | grep -c flower)"; done
systemctl is-active stayconnect-netd stayconnect-acctd stayconnect-edged stayconnect-scd kea-dhcp4-server stayconnect-caddy | tr '\n' ' '; echo
ls /sys/class/net | tr '\n' ' '; echo
curl -s --unix-socket /run/stayconnect/netd.sock http://netd/v1/phase3/continuity | head -c 120; echo
