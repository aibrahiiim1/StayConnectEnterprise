#!/bin/bash
# gen-churn.sh <duration> <vouchers-file> [from] [to]
DUR=${1:-5m}; VF=${2:-v-short1.txt}; FROM=${3:-1700}; TO=${4:-2000}
cp /rel/$VF /root/$VF
ulimit -n 1048576
nohup /root/ogload signin -from $FROM -to $TO -loop-for $DUR -rate 300 -vouchers /root/$VF > /root/churn.log 2>&1 &
nohup /root/ogload traffic -from 0 -to 1700 -sink http://172.30.10.50:8080 -pct 10 -bytes 262144 -every 5s -spread 4s -duration $DUR > /root/traffic.log 2>&1 &
echo churn started $DUR
