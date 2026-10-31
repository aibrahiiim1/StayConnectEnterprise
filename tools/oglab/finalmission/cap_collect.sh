#!/bin/bash
export MSYS_NO_PATHCONV=1
EV=/d/tmp/fm/ev/cap
A() { docker exec fm-appl sh -c "$1"; }
G() { docker exec fm-gen sh -c "cd /root; $1"; }
T0=$(cat $EV/cap.T0)
A 'pkill -f sampler.sh' >/dev/null 2>&1
docker cp /d/tmp/fm/lab/cap_remote.sh fm-appl:/root/cap_remote.sh
A "bash /root/cap_remote.sh $T0" > $EV/cap_remote.out 2>&1
for f in post-integrity-t0.txt cap.activations.csv pg_stat_statements_top.txt pg_deadlock_lines.txt pg_lock_timeout_fatal_lines.txt pg_lock_wait_log_lines.txt journal_warn_error_counts.txt netd_pass_summary.txt netd_panics.txt memory_events.txt cap.cpuproc.log cap.sampler.csv; do
  docker cp fm-appl:/root/capout/$f $EV/$f
done
G "cat cap.signin.log" > $EV/cap.signin.log
G "tail -n 3 cap.churn.out" > $EV/cap.churn.out.tail
G "tail -n 2 cap.movers.out" > $EV/cap.movers.out.tail
docker cp fm-gen:/root/cap.movers.json $EV/cap.movers.json 2>/dev/null
echo collected
