. /root/cont-lib.sh
cs() { contstat | jq -c '{s:.sessions,a:.active,w:.window_seconds,r:.refused}'; }
echo "== (d) direct removal during an outage"
systemctl stop stayconnect-acctd; sleep 75
echo "$(date +%T) outage +75 s: $(cs) set=$(authset)"
ADDR=10.60.3.3
nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR " 
nft delete element inet stayconnect phase3_auth_ipv4 "{ \"br-g-9252e124\" . $ADDR }" && echo "deleted $ADDR"
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16; do
  sleep 20
  present=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ")
  echo "$(date +%T) removed element present=$present  set=$(authset)"
done
systemctl start stayconnect-acctd; sleep 30
echo "$(date +%T) acctd back: $(cs) set=$(authset) present=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ")"
