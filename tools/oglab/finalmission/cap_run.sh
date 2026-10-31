#!/bin/bash
# Capacity run: 300 OFFERED sign-ins/min of 60 s vouchers for 60 min, 1,700 long sessions, 10 percent of them moving 256 KB every 5 s.
export MSYS_NO_PATHCONV=1
EV=/d/tmp/fm/ev/cap
mkdir -p $EV
A() { docker exec fm-appl sh -c "$1"; }
G() { docker exec fm-gen sh -c "ulimit -n 1048576; cd /root; $1"; }
DUR=${1:-60m}
say() { echo "$(date -u +%T) $*" | tee -a $EV/cap_run.log; }
say "capacity run start, duration $DUR"
python3 D:/tmp/fm/lab/ensure_pop.py cap | tee -a $EV/cap_run.log
A ". /root/cont-lib.sh; setwin 60 capacity-default" >> $EV/cap_run.log 2>&1
cd /d/tmp/fm/lab
docker cp cap_trigger.sql fm-appl:/root/cap_trigger.sql; docker cp cap_integrity.sql fm-appl:/root/cap_integrity.sql
A 'docker exec -i stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site < /root/cap_trigger.sql' | tee -a $EV/cap_run.log
A 'docker exec -i stayconnect-pg psql -X -At -F "|" -U stayconnect -d stayconnect_site < /root/cap_integrity.sql' > $EV/pre-integrity.txt 2>&1
A 'bash /root/pgreset.sh' | tee -a $EV/cap_run.log
A 'mkdir -p /root/cap; rm -f /root/cap/*; nohup /root/sampler.sh /root/cap/sampler.csv 5 2500 >/dev/null 2>&1 &'
A 'nohup sh -c "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do bash /root/cpuproc.sh 60 >> /root/cap/cpuproc.log; echo ---- >> /root/cap/cpuproc.log; sleep 240; done" >/dev/null 2>&1 &'
A 'date +%s > /root/cap/sampler.started'
sleep 45
G 'rm -f /root/cap.signin.log /root/cap.churn.out /root/cap.movers.out /root/cap.movers.json; cp /root/v-short2.txt /root/v-short.cap.txt'
T0=$(date +%s)
echo $T0 > $EV/cap.T0
say "generator start T0=$T0"
docker exec -d fm-gen sh -c "ulimit -n 1048576; cd /root; ./ogload signin -from 2000 -to 2400 -loop-for $DUR -rate 300 -vouchers /root/v-short.cap.txt -log /root/cap.signin.log > /root/cap.churn.out 2>&1"
docker exec -d fm-gen sh -c "ulimit -n 1048576; cd /root; ./ogload traffic -from 0 -to 1700 -sink http://172.30.10.50:8080 -pct 10 -bytes 262144 -every 5s -spread 4s -duration $DUR -perclient /root/cap.movers.json > /root/cap.movers.out 2>&1"
END=$((T0 + 3600 + 120))
[ "$DUR" != "60m" ] && END=$((T0 + 120 + 60 * ${DUR%m}))
while [ $(date +%s) -lt $END ]; do sleep 20; done
say "generator window over"
echo done > $EV/cap_run.done
