. /root/cont-lib.sh
cs() { contstat | jq -c '{s:.sessions,a:.active,w:.window_seconds}'; }
echo "== repro 2: acctd flap, then a second outage 80 s later"
systemctl stop stayconnect-acctd; sleep 20; systemctl start stayconnect-acctd; T0=$(date +%s)
for i in $(seq 1 16); do sleep 5; echo "restart+$(( $(date +%s)-T0 ))s: $(cs) set=$(authset)"; done
systemctl stop stayconnect-acctd; T=$(date +%s)
for i in $(seq 1 22); do sleep 5; echo "stop+$(( $(date +%s)-T ))s: $(cs) set=$(authset)"; done
systemctl start stayconnect-acctd; sleep 20; echo "$(date +%T) acctd back: $(cs) set=$(authset)"
journalctl -u stayconnect-acctd --since -4min --no-pager | sed 's/^Oct 31 //' | grep -iE 'warn|error|stale|vouch|submit' | cut -c1-220 | head -8
