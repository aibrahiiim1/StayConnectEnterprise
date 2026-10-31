#!/bin/sh
# Runs in fm-sink (WAN side). One line per second: ping, https and ssh banner on the appliance MANAGEMENT (WAN) address. Any DOWN/000/none is a gap.
A=${1:-172.30.10.20}
while true; do
  t=$(date +%T)
  if ping -c1 -W1 $A >/dev/null 2>&1; then p=up; else p=DOWN; fi
  h=$(curl -sk -o /dev/null -m 2 -w '%{http_code}' https://$A/ 2>/dev/null || echo 000)
  s=$(python3 -c "
import socket
try:
    s=socket.create_connection(('$A',22),2); print(s.recv(4).decode(errors='replace').strip()); s.close()
except Exception:
    print('none')
" 2>/dev/null)
  echo "$t ping=$p https=$h ssh=$s"
  sleep 1
done
