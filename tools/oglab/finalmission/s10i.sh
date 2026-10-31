. /root/cont-lib.sh
cs() { contstat | jq -c '{s:.sessions,a:.active}'; }
echo "== (d) direct removal from BOTH authorization sets during an outage"
sleep 60
systemctl stop stayconnect-acctd; sleep 125
echo "$(date +%T) outage +125 s: $(cs) set=$(authset)"
ADDR=10.60.3.3
MAC=$(nft list set inet stayconnect phase3_auth_mac_ipv4 | grep -F "10.60.3.3 ." | head -n 1 | grep -o '[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]')
echo "mac=$MAC"
nft delete element inet stayconnect phase3_auth_ipv4 "{ \"br-g-9252e124\" . $ADDR }" && echo "deleted from ip set"
nft delete element inet stayconnect phase3_auth_mac_ipv4 "{ \"br-g-9252e124\" . $ADDR . $MAC }" && echo "deleted from mac set"; TD=$(date +%s)
for i in $(seq 1 30); do
  sleep 10
  echo "del+$(( $(date +%s)-TD ))s ip_set=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ") mac_set=$(nft list set inet stayconnect phase3_auth_mac_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ") set=$(authset)"
done
systemctl start stayconnect-acctd; sleep 25
echo "$(date +%T) acctd back: $(cs) set=$(authset) ip_set=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ")"
