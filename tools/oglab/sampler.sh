#!/bin/bash
# Appliance-side sampler for the sign-in capacity acceptance. One CSV line every INTERVAL seconds.
# usage: sampler.sh <outfile> [interval=5] [licence_limit=2500]
OUT="${1:-/root/sample.csv}"; INT="${2:-5}"; LIMIT="${3:-2500}"
PGX='docker exec -i stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site -F ,'
echo "ts,cpu_usec,mem_cur,mem_peak,load1,rss_scd,rss_edged,rss_netd,rss_portald,rss_acctd,rss_pg_all,pg_total,pg_scd,pg_acctd,pg_edged,pg_netd,pg_idle_in_tx,pg_waiting_lock,pg_waiting_other,locks_notgranted,acct_raw,acct_minute,acct_hour,sess_active,sess_pending,sess_ended,dup_live_dev,online_count,lic_limit,auth_set,tc_classes,ct_count" > "$OUT"
CG=/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup | sed -E 's#(/docker/[0-9a-f]+).*#\1#')
rss() { ps -eo rss,comm | awk -v p="$1" '$2 ~ p {s+=$1} END{print s+0}'; }
while true; do
  TS=$(date +%s)
  CPU=$(awk '/^usage_usec/{print $2}' $CG/cpu.stat)
  MC=$(cat $CG/memory.current); MP=$(cat $CG/memory.peak 2>/dev/null || echo 0)
  L1=$(cut -d' ' -f1 /proc/loadavg)
  PG=$($PGX -c "select (select count(*) from pg_stat_activity where datname='stayconnect_site'),
    (select count(*) from pg_stat_activity where usename='svc_scd'),(select count(*) from pg_stat_activity where usename='svc_acctd'),
    (select count(*) from pg_stat_activity where usename='svc_edged'),(select count(*) from pg_stat_activity where usename='svc_netd'),
    (select count(*) from pg_stat_activity where datname='stayconnect_site' and state='idle in transaction'),
    (select count(*) from pg_stat_activity where datname='stayconnect_site' and wait_event_type='Lock'),
    (select count(*) from pg_stat_activity where datname='stayconnect_site' and wait_event_type is not null and wait_event_type not in ('Lock','Client','Activity')),
    (select count(*) from pg_locks where not granted),
    (select count(*) from iam_v2.accounting_records),(select count(*) from iam_v2.accounting_usage_minute),
    coalesce((select count(*) from iam_v2.accounting_usage_hour),0),
    (select count(*) from iam_v2.sessions where state='active' and ended is null),
    (select count(*) from iam_v2.sessions where state='PENDING_ENFORCEMENT' and ended is null),
    (select count(*) from iam_v2.sessions where ended is not null),
    (select count(*) from (select device_id from iam_v2.sessions where ended is null and state in ('active','PENDING_ENFORCEMENT') group by device_id having count(*)>1) x),
    (select count(*) from iam_v2.sessions se join iam_v2.devices d on d.id=se.device_id where se.ended is null and se.state in ('active','PENDING_ENFORCEMENT'))" 2>/dev/null | tr -d '\n')
  # fields: total,scd,acctd,edged,netd,idletx,waitlock,waitother,notgranted,raw,minute,hour,active,pending,ended,dup,online
  TCC=$(for d in $(ls /sys/class/net | grep '^br-g'); do tc class show dev $d 2>/dev/null; done | wc -l)
  AS=$(nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null | grep -o 'br-g[^ ]* \. [0-9.]*' | wc -l)
  CT=$(cat /proc/sys/net/netfilter/nf_conntrack_count 2>/dev/null)
  IFS=, read -r f_total f_scd f_acct f_edged f_netd f_idle f_wl f_wo f_ng f_raw f_min f_hour f_act f_pend f_end f_dup f_online <<< "$PG"
  echo "$TS,$CPU,$MC,$MP,$L1,$(rss '^scd$'),$(rss '^edged$'),$(rss '^netd$'),$(rss '^portald$'),$(rss '^acctd$'),$(rss '^postgres$'),$f_total,$f_scd,$f_acct,$f_edged,$f_netd,$f_idle,$f_wl,$f_wo,$f_ng,$f_raw,$f_min,$f_hour,$f_act,$f_pend,$f_end,$f_dup,$f_online,$LIMIT,$AS,$TCC,$CT" >> "$OUT"
  sleep "$INT"
done
