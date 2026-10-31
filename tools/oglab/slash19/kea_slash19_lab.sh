#!/bin/sh
# Runs INSIDE the c19-kea:1 container (privileged). /lab holds kea-fixture.json, dhcpclient.py, kea_check.py.
#   docker run --rm --privileged --name c19-kea-run -v <dir>:/lab c19-kea:1 sh /lab/kea_slash19_lab.sh
# Proves a real kea-dhcp4 serves the product's /19 render: config-test, leases across the WHOLE pool including the top of the range,
# a reservation at the top, pool exhaustion, the lease file / lease_cmds view, a Kea restart that keeps every lease, INIT-REBOOT renewals.
set -u
FIX=/lab/kea-fixture.json
POOL_FIRST=10      # 10.65.0.10
POOL_LAST=8186     # 10.65.31.250
POOL=$((POOL_LAST - POOL_FIRST + 1))
CLIENTS=$((POOL + 1 + 23))   # the pool, the one reserved client, and 23 more that must get nothing
mkdir -p /run/kea /var/lib/kea /var/log/kea
chmod 750 /run/kea
ip link add br-k19 type bridge && ip link set br-k19 up && ip addr add 10.65.0.1/19 dev br-k19
# The clients live in their own network namespace: in one namespace the server's own address would arrive as a martian source and be dropped.
ip netns add cl && ip link add kc0 type veth peer name kc1 && ip link set kc1 master br-k19 && ip link set kc1 up && ip link set kc0 netns cl
ip netns exec cl ip link set lo up && ip netns exec cl ip link set kc0 up
kea-dhcp4 -V | head -1
if kea-dhcp4 -t "$FIX" >/tmp/configtest.log 2>&1; then echo "PASS kea config-test accepts the rendered /19 file"; else echo "FAIL kea config-test"; cat /tmp/configtest.log; exit 1; fi
start_kea() {
  kea-dhcp4 -c "$FIX" >/var/log/kea/stdout.log 2>&1 &
  echo $! >/run/kea/kea.pid
  for i in 1 2 3 4 5 6 7 8 9 10; do [ -S /run/kea/kea4-ctrl.sock ] && break; sleep 1; done
  sleep 1
}
start_kea
echo "INFO kea RSS at start: $(grep VmRSS /proc/$(cat /run/kea/kea.pid)/status)"
ip netns exec cl python3 /lab/dhcpclient.py kc0 "$CLIENTS" 1000 dora
python3 /lab/kea_check.py /tmp/dhcp-results.txt "$POOL_FIRST" "$POOL_LAST" 10.65.0.0 1
echo "INFO kea RSS with the whole /19 pool leased: $(grep VmRSS /proc/$(cat /run/kea/kea.pid)/status)"
ls -la /var/lib/kea/kea-leases4.csv
FIRST=$(head -1 /tmp/dhcp-results.txt)
echo "INFO first client (reserved MAC 02:00:00:00:03:e8 -> 10.65.31.254): $FIRST"
case "$FIRST" in *" 10.65.31.254 "*) echo "PASS reservation at the top of the /19 honoured" ;; *) echo "FAIL reservation at the top of the /19 not honoured" ;; esac
# restart recovery
kill "$(cat /run/kea/kea.pid)"; sleep 2
start_kea
ip netns exec cl python3 /lab/dhcpclient.py kc0 "$CLIENTS" 1000 reboot >/tmp/reboot.txt
ACK=$(grep -c " ACK" /tmp/reboot.txt); NOACK=$(grep -c "NO-ACK" /tmp/reboot.txt)
echo "INFO after a Kea restart: $ACK INIT-REBOOT renewals ACKed, $NOACK not"
[ "$NOACK" = "0" ] && [ "$ACK" -gt 0 ] && echo "PASS every lease survived the Kea restart (renewed with the same address)" || echo "FAIL restart recovery"
tail -3 /var/log/kea/kea-dhcp4.log 2>/dev/null | cut -c1-200
