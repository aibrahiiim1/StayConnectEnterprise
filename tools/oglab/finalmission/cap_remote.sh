#!/bin/bash
# runs ON the appliance: collects the capacity-run measurements into /root/capout. usage: cap_remote.sh <T0 epoch>
T0=$1
O=/root/capout
mkdir -p $O
PG="docker exec -i stayconnect-pg psql -X -At -F | -U stayconnect -d stayconnect_site"
$PG < /root/cap_integrity.sql > $O/post-integrity-t0.txt 2>&1
docker exec stayconnect-pg psql -X -At -F ',' -U stayconnect -d stayconnect_site -c "copy (select extract(epoch from at), session_id, host(ip) from lab.session_activation order by at) to stdout csv" > $O/cap.activations.csv
cat > /tmp/top.sql <<'EOF'
select r.rolname, calls, round(total_exec_time::numeric) as total_ms, round(mean_exec_time::numeric,2) as mean_ms, round(max_exec_time::numeric) as max_ms, left(regexp_replace(query, '\s+', ' ', 'g'), 110) as q from pg_stat_statements s join pg_roles r on r.oid=s.userid order by total_exec_time desc limit 18;
EOF
docker exec -i stayconnect-pg psql -X -U stayconnect -d stayconnect_site < /tmp/top.sql > $O/pg_stat_statements_top.txt 2>&1
docker logs --since 80m stayconnect-pg 2>&1 | grep -ciE 'deadlock' > $O/pg_deadlock_lines.txt
docker logs --since 80m stayconnect-pg 2>&1 | grep -ciE 'lock timeout|canceling statement due to lock|FATAL' > $O/pg_lock_timeout_fatal_lines.txt
docker logs --since 80m stayconnect-pg 2>&1 | grep -cE 'still waiting for' > $O/pg_lock_wait_log_lines.txt
journalctl -u stayconnect-scd -u stayconnect-netd -u stayconnect-acctd -u stayconnect-portald -u stayconnect-edged --since @$T0 --no-pager -o cat | grep -E 'level":"(ERROR|WARN)' | sed 's/.*"msg":"\([^"]*\)".*/\1/' | cut -c1-110 | sort | uniq -c | sort -rn | head -15 > $O/journal_warn_error_counts.txt
journalctl -u stayconnect-netd --since @$T0 --no-pager -o cat > /root/cap/netd.log
python3 /root/passes.py /root/cap/netd.log 0 | tail -n 3 > $O/netd_pass_summary.txt
journalctl -u stayconnect-netd --since @$T0 --no-pager | grep -ciE 'panic|goroutine ' > $O/netd_panics.txt
CG=/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup | sed -E 's#(/docker/[0-9a-f]+).*#\1#')
cat $CG/memory.events > $O/memory_events.txt
cat /root/cap/cpuproc.log > $O/cap.cpuproc.log
cat /root/cap/sampler.csv > $O/cap.sampler.csv
echo remote_done
