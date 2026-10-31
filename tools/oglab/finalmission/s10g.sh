. /root/cont-lib.sh
cs() { contstat | jq -c '{s:.sessions,a:.active}'; }
echo "== (d) direct removal during an outage, clean start"
sleep 60
systemctl stop stayconnect-acctd; T=$(date +%s); sleep 80
echo "$(date +%T) outage +80 s: $(cs) set=$(authset)"
ADDR=10.60.3.3
echo "present before: $(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ")"
nft delete element inet stayconnect phase3_auth_ipv4 "{ \"br-g-9252e124\" . $ADDR }" && echo "deleted $ADDR"; TD=$(date +%s)
for i in $(seq 1 30); do
  sleep 10
  echo "del+$(( $(date +%s)-TD ))s present=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ") set=$(authset)"
done
systemctl start stayconnect-acctd; sleep 25
echo "$(date +%T) acctd back: $(cs) set=$(authset) present=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c "\"br-g-9252e124\" . $ADDR ")"
