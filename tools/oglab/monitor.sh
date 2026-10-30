#!/bin/sh
# Appliance-side sampler: one CSV line every INTERVAL seconds. Usage: monitor.sh <outfile> [interval=15]
OUT="${1:-/root/monitor.csv}"; INT="${2:-15}"
PGX='docker exec -i stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site -F ,'
echo "ts,cpu_usec,mem_cur,mem_peak,load1,rss_scd,rss_edged,rss_netd,rss_portald,rss_acctd,rss_caddy,rss_kea,rss_unbound,rss_node,rss_pg_all,rss_dockerd,pg_conns,pg_conns_scd,pg_conns_acctd,pg_conns_edged,pg_conns_netd,acct_raw,acct_minute,sess_active,sess_ended,ct_count,tc_classes,auth_set" > "$OUT"
rss() { ps -eo rss,comm,args | awk -v p="$1" '$2 ~ p {s+=$1} END{print s+0}'; }
while true; do
  TS=$(date +%s)
  CPU=$(awk '/^usage_usec/{print $2}' /sys/fs/cgroup/cpu.stat)
  MC=$(cat /sys/fs/cgroup/memory.current); MP=$(cat /sys/fs/cgroup/memory.peak 2>/dev/null || echo 0)
  L1=$(cut -d' ' -f1 /proc/loadavg)
  PG=$($PGX -c "select (select count(*) from pg_stat_activity where datname=\$\$stayconnect_site\$\$), (select count(*) from pg_stat_activity where usename=\$\$svc_scd\$\$), (select count(*) from pg_stat_activity where usename=\$\$svc_acctd\$\$), (select count(*) from pg_stat_activity where usename=\$\$svc_edged\$\$), (select count(*) from pg_stat_activity where usename=\$\$svc_netd\$\$), (select count(*) from iam_v2.accounting_records), (select count(*) from iam_v2.accounting_usage_minute), (select count(*) from iam_v2.sessions where state=\$\$active\$\$), (select count(*) from iam_v2.sessions where state=\$\$ended\$\$)" 2>/dev/null | tr -d '\n')
  CT=$(cat /proc/sys/net/netfilter/nf_conntrack_count 2>/dev/null)
  TCC=$(for d in $(ls /sys/class/net | grep '^br-g'); do tc class show dev $d 2>/dev/null; done | wc -l)
  AS=$(nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null | grep -c "br-g")
  echo "$TS,$CPU,$MC,$MP,$L1,$(rss '^scd$'),$(rss '^edged$'),$(rss '^netd$'),$(rss '^portald$'),$(rss '^acctd$'),$(rss '^caddy$'),$(rss 'kea-dhcp4'),$(rss '^unbound$'),$(rss '^node'),$(rss '^postgres$'),$(rss '^dockerd$'),$PG,$CT,$TCC,$AS" >> "$OUT"
  sleep "$INT"
done
