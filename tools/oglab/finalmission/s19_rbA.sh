cd /root
chk() { echo "[$1] $(date +%T) bridges: $(ip -br link | awk '/^br-g/{print $1}' | tr '\n' ' ') | b19 addr: $(ip -4 -br addr show br-g120 2>&1 | head -c 60) | kea subnets: $(python3 /root/keaq.py | grep -o '10\.[0-9]*\.0\.0/[0-9]*' | tr '\n' ' ')"; }
echo "=== Rollback path A: unconfirmed apply of a second /19 (watchdog)"
python3 s19_apply.py create b19
OUT=$(python3 s19_apply.py apply "rollback A, not confirmed"); echo "$OUT" | cut -c1-260
RID=$(echo "$OUT" | grep -o '"revision_id": "[^"]*"' | cut -d'"' -f4)
chk "pending"
sleep 140
chk "after 140 s"
python3 s19_apply.py status | head -n 3
nft list table inet stayconnect > /root/ev/s19-after-rbA.nft
bash -c 'norm() { grep -vE "elements = |^\s+\"br-g|expires|packets|bytes" $1 | sed "s/timeout [0-9a-z]*//"; }; diff <(norm /root/ev/s19-after-confirm.nft) <(norm /root/ev/s19-after-rbA.nft) | grep "^[<>]" | grep -v "^. *$" | head -10; echo "nft diff lines above (none = equal to the confirmed /19 state)"'
