#!/bin/bash
# LOG REDACTION OVER THE WHOLE ACCEPTANCE RUN (read-only): the guest-facing and admin services' journals since
# SINCE carry none of the fixture passwords, the acceptance operator's password, the Stripe test card number,
# and portald logs query strings as ?[redacted].
. "$(dirname "$0")/lib.sh"
SINCE=${SINCE:-"-3h"}
L=$(journalctl -u 'stayconnect-*' --since "$SINCE" --no-pager 2>/dev/null); info "journal lines examined: $(printf '%s\n' "$L" | wc -l) (since $SINCE)"
n=$(printf '%s' "$L" | grep -c -- "Acc-pw-"); [ "$n" = 0 ] && ok "no fixture client password (Acc-pw-*) in the journal" || bad "$n lines carry a fixture client password"
printf '%s' "$L" | grep -qF -- "$ACC_PW" && bad "the acceptance operator's password is in the journal" || ok "the operator password is not in the journal"
n=$(printf '%s' "$L" | grep -c "4242424242424242"); [ "$n" = 0 ] && ok "no card number in the journal" || bad "$n lines carry a card number"
P=$(journalctl -u stayconnect-portald --since "$SINCE" --no-pager 2>/dev/null)
r=$(printf '%s' "$P" | grep -c '?\[redacted\]'); raw=$(printf '%s' "$P" | grep -cE '\?(p|c|k|t|code|session_id)=[^ &"]')
info "portald request lines with ?[redacted]: $r"; [ "$raw" = 0 ] && ok "portald logs no raw query strings (p= c= k= t= code= session_id=)" || bad "$raw portald lines carry a raw query string"
n=$(printf '%s' "$P" | grep -ciE 'ACCWRONG|ACCDAMP'); [ "$n" = 0 ] && ok "wrong voucher codes typed in the browser are not logged by portald" || bad "$n portald lines carry a typed voucher code"
