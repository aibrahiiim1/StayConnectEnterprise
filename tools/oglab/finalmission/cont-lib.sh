#!/bin/sh
# Appliance-side helpers (plan section 0). Source with: . /root/cont-lib.sh
PGX='docker exec -i stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site'
psql_c() { $PGX -c "$1"; }
authset()  { nft list set inet stayconnect phase3_auth_ipv4 | grep -c 'expires'; }
contstat() { curl -s --unix-socket /run/stayconnect/netd.sock http://netd/v1/phase3/continuity; }
setwin()   { psql_c "SELECT public.enforcement_policy_set('outage_continuity_minutes','$1','lab-operator','lab run $2')"; }
nowms()    { date +%s%3N; }
