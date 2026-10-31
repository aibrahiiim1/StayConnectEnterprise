cd /root
. /root/cont-lib.sh
snap() {
  echo "  classes: $(for d in br-g-9252e124 br-g102 br-g119 br-g121-1e33; do echo -n "$d=$(tc class show dev $d 2>/dev/null | grep -c '^class htb') "; done)"
  echo "  set=$(authset) render_fp=$(nft list table inet stayconnect | grep -o 'netd-render-fp=[0-9a-f]*' | head -n 1) kea_leases=$(python3 - <<'PYEOF'
import json, socket
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect("/run/kea/kea4-ctrl-socket")
s.sendall(json.dumps({"command": "lease4-get-all"}).encode())
b = b""
while True:
    d = s.recv(1 << 22)
    if not d: break
    b += d
    try:
        j = json.loads(b); break
    except Exception: continue
print(len(j["arguments"]["leases"]))
PYEOF
)"
}
for svc in stayconnect-netd stayconnect-acctd kea-dhcp4-server; do
  echo "=== $svc stop/reset-failed/start --no-block  $(date +%T)"
  snap
  systemctl stop $svc; systemctl reset-failed $svc; systemctl start --no-block $svc
  sleep 45
  snap
  echo "  netd class-change/stray lines since: $(journalctl -u stayconnect-netd --since -50sec --no-pager -o cat | grep -ciE 'class change|stray')"
done
