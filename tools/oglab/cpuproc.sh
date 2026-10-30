#!/bin/bash
# cpuproc.sh <seconds>: CPU used per systemd service cgroup over the window (percent of ONE cpu), top first
CG=/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup | sed -E 's#(/docker/[0-9a-f]+).*#\1#')
W=${1:-60}
declare -A A
for d in $CG/system.slice/*.service $CG/system.slice/*.scope; do
  [ -f $d/cpu.stat ] && A[$d]=$(awk '/^usage_usec/{print $2}' $d/cpu.stat)
done
T0=$(awk '/^usage_usec/{print $2}' $CG/cpu.stat)
sleep $W
T1=$(awk '/^usage_usec/{print $2}' $CG/cpu.stat)
for d in "${!A[@]}"; do
  n=$(awk '/^usage_usec/{print $2}' $d/cpu.stat 2>/dev/null)
  echo "$(( (n-${A[$d]})*100/(W*1000000) )) $(basename $d)"
done | sort -rn | head -12
echo "TOTAL $(( (T1-T0)*100/(W*1000000) ))% of one cpu (200% = both)"
