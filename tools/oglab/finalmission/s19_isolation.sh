cd /root
python3 - <<'PYEOF' > /tmp/pick.txt
import json
c=[x for x in json.load(open('/var/lib/ogload/clients.json')) if 2400 <= x['idx'] < 3100 and x['signed_in']]
c.sort(key=lambda x: tuple(int(p) for p in x['ip'].split('.')))
print(c[0]['idx'], c[0]['ip'], c[-1]['idx'], c[-1]['ip'])
PYEOF
read LI LIP HI HIP < /tmp/pick.txt
LN=$(printf %04d $LI); HN=$(printf %04d $HI)
echo "lower-half client idx=$LI $LIP ; upper-half client idx=$HI $HIP"
echo "-- lower -> upper (isolation, expected: no reply):"; ip netns exec cl$LN ping -c2 -W1 $HIP 2>&1 | tail -n 2
echo "-- upper -> lower (expected: no reply):"; ip netns exec cl$HN ping -c2 -W1 $LIP 2>&1 | tail -n 2
echo "-- both reach the portal on the gateway 10.66.0.1:8380:"
for n in $LN $HN; do ip netns exec cl$n curl -s -o /dev/null -m 4 -w "cl$n portal http=%{http_code}\n" http://10.66.0.1:8380/ ; done
echo "-- DNS to the gateway:"
for n in $LN $HN; do ip netns exec cl$n sh -c 'timeout 4 nslookup -timeout=2 example.org 10.66.0.1 2>&1 | grep -E "Address: |can.t|timed out" | head -n 2 | tr "\n" " "'; echo " (cl$n)"; done
echo "-- management / WAN address from a client (expected: fail):"
for n in $LN $HN; do ip netns exec cl$n curl -sk -o /dev/null -m 4 -w "cl$n https://172.30.10.20/ http=%{http_code} (000 = no answer)\n" https://172.30.10.20/ ; done
for n in $HN; do ip netns exec cl$n curl -sk -o /dev/null -m 4 -w "cl$n https://10.66.0.1/ http=%{http_code}\n" https://10.66.0.1/ ; done
