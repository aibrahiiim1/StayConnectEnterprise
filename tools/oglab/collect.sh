#!/bin/bash
# collect.sh <since e.g. -5min> <tag> : journals of the appliance services into /rel/<tag>-*.txt
S=${1:--5min}; T=${2:-x}
journalctl -u stayconnect-scd --since "$S" --no-pager -o cat > /rel/$T-scd.txt
journalctl -u stayconnect-portald --since "$S" --no-pager -o cat > /rel/$T-portald.txt
journalctl -u stayconnect-netd --since "$S" --no-pager -o cat > /rel/$T-netd.txt
journalctl -u stayconnect-acctd --since "$S" --no-pager -o cat > /rel/$T-acctd.txt
docker logs --since "${S#-}" stayconnect-pg > /rel/$T-pg.txt 2>&1
wc -l /rel/$T-*.txt
