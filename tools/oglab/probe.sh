#!/bin/bash
# probe.sh <seconds> <out>: one tiny write transaction every 250 ms, its latency from psql \timing
docker exec stayconnect-pg psql -X -U stayconnect -d stayconnect_site -c "create table if not exists public.zz_probe(t timestamptz)" >/dev/null 2>&1
printf '\\timing on\ninsert into public.zz_probe values (now());\n\\watch 0.25\n' > /root/probe.sql
docker cp /root/probe.sql stayconnect-pg:/tmp/probe.sql
timeout ${1:-60} docker exec stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site -f /tmp/probe.sql 2>&1 | grep -E '^Time' > ${2:-/root/probe.txt}
echo probe done
