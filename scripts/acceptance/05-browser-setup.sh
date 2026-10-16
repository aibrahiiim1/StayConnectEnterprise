#!/bin/bash
# 5 (appliance side). start: a guest-network namespace client "bw<R4>", a fixture Personal account, and the
# one-file proxy (nsproxy.py) inside the namespace on <ns-ip>:3128. Prints NSIP / PORTAL / USER for the workstation.
# The account password is written to /root/acceptance/browser.state (0600) and handed to Playwright over ssh.
# stop: ends the account's access, removes it, releases any sign-in restriction on the device, removes the namespace.
ST=/root/acceptance/browser.state
if [ "${1:-}" = start ]; then
  . "$(dirname "$0")/lib.sh"; trap - EXIT
  C=bw$R4; client $C; NS=()
  U="acc-browser-$R4"; A=$(mkacc "$U")
  ip netns exec $C nohup python3 /root/acceptance/nsproxy.py "$(nsip $C)" 3128 >/tmp/$C.proxy 2>&1 &
  sleep 1
  umask 077; printf 'C=%s\nA=%s\nU=%s\nUPW=%s\nNSIP=%s\nMAC=%s\nPORTAL=%s\n' "$C" "$A" "$U" "Acc-pw-$R4" "$(nsip $C)" "$(nsmac $C)" "$P" > $ST
  echo "NSIP=$(nsip $C) PORTAL=$P USER=$U online=$(online $C)"; rm -f $CK
elif [ "${1:-}" = online ]; then . $ST; echo "probe from the browser device: $(ip netns exec $C curl -s -o /dev/null -w '%{http_code}' -m 6 http://connectivitycheck.gstatic.com/generate_204)"
elif [ "${1:-}" = stop ]; then
  . $ST; . "$(dirname "$0")/lib.sh"
  ACCS+=("$A"); NS+=("$C")
  PSQL0="$PSQL"; for r in $($PSQL "SELECT id FROM iam_v2.guest_signin_restrictions WHERE device_mac='$MAC' AND released_at IS NULL AND expires_at>now()" 2>/dev/null); do
    c=$(code POST "/guest-signin-restrictions/$r/release" '{"reason":"acceptance: browser device retired"}'); info "released sign-in restriction ${r:0:8} -> $c"; done
  ip netns pids $C | xargs -r kill 2>/dev/null
  info "browser device ledger: $($PSQL "SELECT string_agg(e.status||'/'||coalesce(e.terminal_reason,'-'),', ') FROM iam_v2.entitlements e WHERE e.guest_account_id='$A'")"
  rm -f $ST
fi
