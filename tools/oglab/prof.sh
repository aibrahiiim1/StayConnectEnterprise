#!/bin/bash
# prof.sh <delay> <seconds> <tag>: CPU profiles of netd (6062) and acctd (6063) taken together
sleep ${1:-40}
S=${2:-30}
curl -s "localhost:6062/debug/pprof/profile?seconds=$S" > /rel/netd-$3.prof &
curl -s "localhost:6063/debug/pprof/profile?seconds=$S" > /rel/acctd-$3.prof &
wait
echo done > /rel/prof-$3-done
