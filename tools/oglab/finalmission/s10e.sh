. /root/cont-lib.sh
cs() { contstat | jq -c '{s:.sessions,a:.active,w:.window_seconds}'; }
echo "== repro: outage soon after the setting was switched on again"
setwin 0 repro >/dev/null; sleep 40; echo "$(date +%T) setting 0: $(cs) set=$(authset)"
setwin 60 repro >/dev/null
for i in $(seq 1 12); do sleep 5; echo "$(date +%T) after setwin 60 +$((i*5))s: $(cs) set=$(authset)"; done
systemctl stop stayconnect-acctd; T=$(date +%s)
for i in $(seq 1 24); do sleep 5; echo "stop+$(( $(date +%s)-T ))s: $(cs) set=$(authset)"; done
systemctl start stayconnect-acctd; sleep 20; echo "$(date +%T) acctd back: $(cs) set=$(authset)"
