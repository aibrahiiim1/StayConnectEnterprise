#!/bin/bash
# Appliance container restart (the closest the container lab gets to a reboot). Output: s8.txt in the evidence folder.
export MSYS_NO_PATHCONV=1
OUT=/d/tmp/fm/ev/s8
mkdir -p $OUT
A() { docker exec fm-appl sh -c "$1"; }
{
echo "== before $(date -u +%T)"
A '. /root/cont-lib.sh; echo "set=$(authset) $(contstat | jq -c "{s:.sessions,a:.active,r:.refused}") boot_id=$(cat /proc/sys/kernel/random/boot_id)"; for d in $(ip -br link | awk "/^br-g/{print \$1}"); do echo "$d $(ip -4 -br addr show $d | awk "{print \$3}") classes=$(tc class show dev $d | grep -c "^class htb") flower=$(tc filter show dev $d parent 1: | grep -c flower)"; done; python3 /root/keaq.py | cut -c1-300; nft list table inet stayconnect | grep -o "netd-render-fp=[0-9a-f]*"; nft list table inet stayconnect | sha256sum | cut -c1-16'
T=$(date +%s)
echo "== docker restart fm-appl at $(date -u +%T)"
docker restart fm-appl > /dev/null
echo "container restart returned after $(( $(date +%s) - T )) s"
# the lab plumbing a restart loses: Docker regenerates /etc/hosts and /etc/resolv.conf; the lab veth is gone
for i in $(seq 1 30); do docker exec fm-appl true 2>/dev/null && break; sleep 1; done
docker exec fm-appl sh -c 'printf "nameserver 192.168.65.7\n" > /etc/resolv.conf; grep -q central.oglab.test /etc/hosts || echo "172.30.10.10 central.oglab.test" >> /etc/hosts; sysctl -w net.bridge.bridge-nf-call-iptables=0 net.bridge.bridge-nf-call-ip6tables=0 net.bridge.bridge-nf-call-arptables=0 >/dev/null 2>&1'
echo "plumbing restored after $(( $(date +%s) - T )) s"
for i in $(seq 1 120); do
  t=$(( $(date +%s) - T ))
  line=$(docker exec fm-appl sh -c '
    pg=$(docker exec stayconnect-pg pg_isready -q 2>/dev/null && echo up || echo down)
    svc=$(for s in stayconnect-scd stayconnect-netd stayconnect-acctd stayconnect-edged stayconnect-portald kea-dhcp4-server; do systemctl is-active $s 2>/dev/null | cut -c1-3; done | tr "\n" "/")
    set=$(nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null | grep -c expires)
    cs=$(curl -s -m 2 --unix-socket /run/stayconnect/netd.sock http://netd/v1/phase3/continuity 2>/dev/null | jq -c "{s:.sessions,a:.active,r:.refused}" 2>/dev/null)
    cl=$(for d in br-g-9252e124 br-g102 br-g119 br-g121-1e33; do tc class show dev $d 2>/dev/null | grep -c "^class htb"; done | tr "\n" ",")
    echo "pg=$pg svc=$svc set=$set classes=$cl cont=$cs"' 2>/dev/null)
  echo "+${t}s $line"
  sleep 5
done
echo "== after $(date -u +%T)"
A '. /root/cont-lib.sh; echo "set=$(authset) $(contstat | jq -c "{s:.sessions,a:.active,r:.refused}") boot_id=$(cat /proc/sys/kernel/random/boot_id)"; for d in $(ip -br link | awk "/^br-g/{print \$1}"); do echo "$d $(ip -4 -br addr show $d | awk "{print \$3}") classes=$(tc class show dev $d | grep -c "^class htb") flower=$(tc filter show dev $d parent 1: | grep -c flower)"; done; python3 /root/keaq.py | cut -c1-300; nft list table inet stayconnect | grep -o "netd-render-fp=[0-9a-f]*"; ip -br link | grep -E "eth1|eth0"'
} > $OUT/s8.txt 2>&1
echo S8_DONE >> $OUT/s8.txt
