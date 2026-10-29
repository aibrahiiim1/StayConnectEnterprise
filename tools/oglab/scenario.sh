#!/bin/sh
# Capacity scenario, run inside the generator container. Timestamps go to /root/scenario.log (one JSON object per line).
LOG=/root/scenario.log
SINK=http://172.30.10.50:8080
O=/root/ogload
say() { echo "{\"t\":$(date +%s),\"event\":\"$1\"}" >> $LOG; }
run() { phase="$1"; shift; say "start:$phase"; "$@" >> $LOG 2>&1; say "end:$phase"; }

say "scenario-start"
# P1 ramp: 1,700 long-lived clients sign in at 300 a minute
run ramp $O signin -from 0 -to 1700 -vouchers /root/v-long.txt -rate 300
sleep 20
# P2 design load for 10 minutes: 10% of the 1,700 move 256 KB every 5 s while 300 clients cycle 60-second vouchers at 300 sign-ins a minute
say "start:steady"
$O traffic -from 0 -to 1700 -sink $SINK -pct 10 -bytes 262144 -every 5s -duration 10m >> $LOG 2>&1 &
T1=$!
$O signin -from 1700 -to 2000 -loop-for 10m -rate 300 -vouchers /root/v-short.txt >> $LOG 2>&1 &
T2=$!
wait $T1; wait $T2
say "end:steady"
sleep 20
# P3 stress: every one of the 1,700 sessions moves counters every 5 s (the heaviest accounting write rate: one row per session per pass)
run stress $O traffic -from 0 -to 1700 -sink $SINK -pct 100 -bytes 16384 -every 5s -duration 5m
sleep 30
say "scenario-end"
