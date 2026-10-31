echo "== pending window checks $(date +%T)"
ip -4 -br addr show | grep -E 'br-g119|br-g-9252|br-g102'
nft list table inet stayconnect | grep -nE '10\.66\.0\.0/19' | head -5
echo "-- admin 443 accept rules:"
nft list chain inet stayconnect input | grep -E 'dport 443' | head -4
echo "-- Kea rendered config has the subnet and pool:"
KC=$(ls /etc/kea/kea-dhcp4.conf /etc/stayconnect/generated/*/kea* 2>/dev/null | head -n 3); for f in $KC; do grep -c '10.66.0.0/19' $f | sed "s#^#$f subnet matches: #"; done
grep -rho '"pool": *"10.66.0.10 - 10.66.31.250"' /etc/kea /etc/stayconnect/generated 2>/dev/null | head -n 1
echo "-- live kea control socket subnet4-list:"
ls /run/kea/ 2>/dev/null | head -n 3
echo "-- WAN:"; ip -4 addr show eth0 | grep inet; ip route | grep default
echo "-- netd warn/error last 2 min:"; journalctl -u stayconnect-netd --since -2min --no-pager | grep -E 'level":"(WARN|ERROR)' | sed 's/.*"msg":"\([^"]*\)".*/\1/' | cut -c1-140 | sort | uniq -c | head -5
for d in br-g-9252e124 br-g102 br-g119; do echo "$d classes=$(tc class show dev $d 2>/dev/null | grep -c '^class htb')"; done
