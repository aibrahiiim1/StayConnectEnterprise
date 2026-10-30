#!/bin/bash
# netdkids.sh <seconds>: tally of the commands netd runs (children seen by polling ps at ~20 Hz) and their cumulative CPU time
P=$(pidof netd)
END=$(( $(date +%s) + ${1:-15} ))
: > /tmp/kids.txt
while [ $(date +%s) -lt $END ]; do
  ps --ppid $P -o pid=,args= 2>/dev/null | cut -c1-110 >> /tmp/kids.txt
  sleep 0.05
done
# one line per distinct pid (a child seen in several polls counts once)
sort -u -k1,1n /tmp/kids.txt | sed -E 's/^ *[0-9]+ //' | sed -E 's/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/IP/g; s/classid [0-9:a-f]+/classid X/g; s/ [0-9]+:[0-9a-f]+/ X/g' | cut -c1-70 | sort | uniq -c | sort -rn | head -20
