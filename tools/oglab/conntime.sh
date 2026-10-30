#!/bin/bash
# conntime.sh: TCP connect latency to the published port (docker-proxy) and straight to the container, 30 samples each
t() { local s=$(date +%s%N); ( exec 3<>/dev/tcp/$1/5432 ) 2>/dev/null; local e=$(date +%s%N); echo $(( (e-s)/1000 )); }
for h in 127.0.0.1 172.18.0.2; do
  v=""
  for i in $(seq 1 30); do v="$v $(t $h)"; sleep 0.2; done
  echo "$h connect us: $v" | tr ' ' '\n' | grep -E '^[0-9]+$' | sort -n | awk -v h=$h '{a[NR]=$1} END{print h, "n="NR, "p50="a[int(NR/2)+1]"us", "p90="a[int(NR*0.9)]"us", "max="a[NR]"us"}'
done
