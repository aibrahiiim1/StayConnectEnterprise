#!/bin/bash
# One real sign-in from a client namespace; prints status, redirect, latency and the rendered portal message (never the code).
NS=${1:-cl1950}; LINE=${2:-1900}; FILE=${3:-/root/v-long.txt}
C=$(sed -n ${LINE}p $FILE)
S=$(date +%s.%N)
ip netns exec $NS curl -s -m 30 -D /tmp/h.txt -o /tmp/b.html --data-urlencode "code=$C" http://10.60.0.1:8380/auth/voucher
E=$(date +%s.%N)
echo "latency: $(echo "$E - $S" | bc 2>/dev/null || python3 -c "print($E-$S)")"
head -3 /tmp/h.txt | tr -d '\r'
python3 - <<'PY'
import re, html
t = open("/tmp/b.html", encoding="utf-8", errors="replace").read()
m = re.search(r'id="server-error"[^>]*>(.*?)</div>', t, re.S)
print("server-error:", html.unescape(m.group(1)).strip()[:300] if m else "(none)")
print("body bytes:", len(t))
PY
