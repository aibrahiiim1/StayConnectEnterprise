#!/bin/bash
# pgsample.sh <seconds> <outfile>: 2 samples/second of pg_stat_activity (non-idle) for the diagnosis
N=${1:-60}; OUT=${2:-/root/pgsample.tsv}
: > $OUT
END=$(( $(date +%s) + N ))
while [ $(date +%s) -lt $END ]; do
  docker exec stayconnect-pg psql -X -At -F $'\t' -U stayconnect -d stayconnect_site -c "select now()::time(2), a.usename, a.state, coalesce(a.wait_event_type,''), coalesce(a.wait_event,''), extract(epoch from now()-a.state_change)::numeric(8,2), coalesce((select b.usename||':'||b.state||':'||left(regexp_replace(b.query,'\s+',' ','g'),70) from pg_stat_activity b where b.pid = any(pg_blocking_pids(a.pid)) limit 1),'-'), left(regexp_replace(a.query,'\s+',' ','g'),110) from pg_stat_activity a where a.datname='stayconnect_site' and a.pid<>pg_backend_pid() and (a.state<>'idle')" >> $OUT 2>/dev/null
  sleep 0.5
done
echo done
