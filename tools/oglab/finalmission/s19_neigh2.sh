cd /root
cat /proc/net/stat/arp_cache | head -1 | tr -s ' ' | cut -c1-200
tot() { awk 'NR>1{for(i=1;i<=NF;i++) s[i]+=strtonum("0x"$i)} END{printf "entries=%d allocs=%d destroys=%d hash_grows=%d lookups=%d hits=%d res_failed=%d rcv_probes_mcast=%d periodic_gc=%d forced_gc=%d forced_gc_goal_miss=%d unres_discards=%d table_fulls=%d\n", s[1],s[2],s[3],s[4],s[5],s[6],s[7],s[8],s[9],s[10],s[11],s[12],s[13]}' /proc/net/stat/arp_cache; }
echo "before: $(tot)"
python3 /root/sweep.py
sleep 1; echo "after 1 s: $(tot)"; echo "br-g119 entries: $(ip -4 neigh show dev br-g119 | wc -l) total: $(ip -4 neigh show | wc -l)"
sleep 30; echo "after 30 s: $(tot)"; echo "br-g119 entries: $(ip -4 neigh show dev br-g119 | wc -l)"
