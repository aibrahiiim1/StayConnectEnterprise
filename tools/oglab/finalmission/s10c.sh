. /root/cont-lib.sh
J=/var/lib/stayconnect/netd-phase3-continuity-revocations.json
nrestart() { systemctl stop stayconnect-netd; systemctl reset-failed stayconnect-netd; systemctl start --no-block stayconnect-netd; }
cs() { contstat | jq -c '{s:.sessions,a:.active,w:.window_seconds,r:.refused,j:.journalled_revocations}'; }
echo "== clean state"; rm -f $J; nrestart; sleep 60; echo "$(date +%T) $(cs) set=$(authset)"
echo "== (b) journal corrupt during an outage"
systemctl stop stayconnect-acctd; T=$(date +%s); sleep 75
echo "$(date +%T) outage +75 s: $(cs) set=$(authset)"
systemctl stop stayconnect-netd
echo '{"version":1,"entries":[],"sum":"bad"}' > $J
systemctl reset-failed stayconnect-netd; systemctl start --no-block stayconnect-netd; sleep 20
echo "$(date +%T) netd restarted with a corrupt journal: $(cs) set=$(authset)"
journalctl -u stayconnect-netd --since -30s --no-pager | grep -iE 'journal|continuity' | sed 's/^.*"msg"/msg/' | cut -c1-240 | head -3
sleep 100
echo "$(date +%T) +100 s later: $(cs) set=$(authset)   (no renewal expected: set should be ~0)"
systemctl start stayconnect-acctd; sleep 40
echo "$(date +%T) acctd back: $(cs) set=$(authset)"
ls -l $J | cut -c1-80
echo "== reset for (c)/(d)"; rm -f $J; nrestart; sleep 70; echo "$(date +%T) $(cs) set=$(authset)"
echo "== (c)+(d) outage: setting to 0 and a direct nft removal"
systemctl stop stayconnect-acctd; sleep 75
echo "$(date +%T) outage +75 s: $(cs) set=$(authset) record_mtime=$(stat -c %y /var/lib/stayconnect/netd-phase3-continuity.json | cut -c1-19)"
setwin 0 s10-setting-zero-during-outage >/dev/null
sleep 20
echo "$(date +%T) setting=0 written during the outage: $(cs) set=$(authset)  (netd cannot learn it: window unchanged expected)"
IP=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -o 'br-g-9252e124 . 10\.60\.[0-9]*\.[0-9]*' | head -n 1)
echo "removing element: $IP"
BR=$(echo $IP | cut -d' ' -f1); ADDR=$(echo $IP | awk '{print $3}')
nft delete element inet stayconnect phase3_auth_ipv4 "{ $BR . $ADDR }"
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  sleep 20
  present=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "$ADDR")
  echo "$(date +%T) removed element present=$present  set=$(authset)"
done
systemctl start stayconnect-acctd; sleep 20
echo "$(date +%T) acctd back (setting 0 in force): $(cs) set=$(authset)"
sleep 20
echo "$(date +%T) +40 s: $(cs) set=$(authset) record=$(ls /var/lib/stayconnect/netd-phase3-continuity.json 2>&1 | tail -n 1)"
setwin 60 s10-restore >/dev/null; sleep 30
echo "$(date +%T) setting back to 60: $(cs) set=$(authset)"
