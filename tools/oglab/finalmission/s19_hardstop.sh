cd /root
echo "=== hard stop during a pending-confirmation window (revision afaf450d pending since 07:16:52)"
python3 s19_apply.py status | head -n 1
systemctl stop stayconnect-netd; echo "netd stopped $(date +%T)"
sleep 140
systemctl reset-failed stayconnect-netd; systemctl start --no-block stayconnect-netd; echo "netd started $(date +%T)"
sleep 25
python3 s19_apply.py status | head -n 2
echo "bridges: $(ip -br link | awk '/^br-g/{print $1}' | tr '\n' ' ') | kea subnets: $(python3 /root/keaq.py | grep -o '10\.[0-9]*\.0\.0/[0-9]*' | tr '\n' ' ')"
