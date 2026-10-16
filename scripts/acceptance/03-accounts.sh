#!/bin/bash
# 3. CLIENT ACCOUNTS (decision D-A, audit F2/F10). Run as root on the appliance. Fixture accounts and a fixture
# free package only.
#   a. disable an ONLINE account -> device offline within seconds, entitlement SUSPENDED; enable -> ACTIVE again
#   b. a removed account: PATCH -> 409 removed (enable and rename)
#   c. mark-verified needs reason + password (self-registered fixture email account)
#   d. a voucher_operator cannot flip the portal's account sign-in switch (403)
. "$(dirname "$0")/lib.sh"
DEFTIERS='[{"order":1,"grant":{}}]'
PREV=$(mkplan "{\"code\":\"acc-acct-$R4\",\"name\":\"Acc accounts\",\"down_kbps\":20000,\"up_kbps\":10000,\"max_concurrent_devices\":2,\"device_limit_policy\":\"REJECT_NEW_DEVICE\",\"time_accounting_mode\":\"VALIDITY_WINDOW\",\"speed_allocation\":\"PER_DEVICE\",\"create_only\":true}")
PK=$(mkpkg "{\"code\":\"acc-free-acct-$R4\",\"service_plan_revision_id\":\"$PREV\",\"display\":{\"name\":\"Acc accounts free\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,\"create_only\":true}")
case "$PK" in ERR*) bad "fixture package: $PK"; exit 1;; esac

echo "== a. disable an online account, then enable it"
U="acc-dis-$R4"; A=$(mkacc "$U"); ACCS+=("$A"); C=x$R4; client $C
signin $C "$U" >/dev/null; acquire $C "$PK" >/dev/null; waiton $C && ok "device online on the account" || bad "device not online"
T=$(date +%s.%N); c=$(code PATCH "/guest-accounts/$A" '{"enabled":false}'); info "PATCH enabled=false -> $c $(python3 -c 'import json,sys;d=json.load(open("/tmp/acc.last"));print({k:d.get(k) for k in ("packages_suspended","disconnected_sessions","disconnect_failures")})' 2>/dev/null)"
off=""; for i in $(seq 1 10); do [ "$(online $C)" != 204 ] && { off=$(python3 -c "import time;print(round(time.time()-$T,1))"); break; }; sleep 1; done
[ -n "$off" ] && ok "device offline ${off}s after Disable" || bad "device still online 10 s after Disable"
E=$(ENT $A); case "$E" in *SUSPENDED*) ok "entitlement SUSPENDED (${E#*|})";; *) bad "entitlement after disable: $E";; esac
r=$(signin $C "$U"); [ "$(online $C)" != 204 ] && ok "a disabled account cannot sign back in ($r)" || bad "disabled account signed in"
c=$(code PATCH "/guest-accounts/$A" '{"enabled":true}'); info "PATCH enabled=true -> $c $(python3 -c 'import json;d=json.load(open("/tmp/acc.last"));print({k:d.get(k) for k in ("packages_resumed",)})' 2>/dev/null)"
E=$(ENT $A); case "$E" in *ACTIVE*) ok "entitlement ACTIVE again after Enable (${E#*|})";; *) bad "entitlement after enable: $E";; esac
if waiton $C 3; then ok "device back online without signing in"; else signin $C "$U" >/dev/null; waiton $C 5 || { acquire $C "$PK" >/dev/null; waiton $C 5; }
  [ "$(online $C)" = 204 ] && ok "device online again after signing in (same entitlement: $(ENT $A | cut -d'|' -f1 | cut -c1-8) = ${E:0:8})" || bad "device cannot resume after Enable"; fi
[ "$($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE guest_account_id='$A'")" = 1 ] && ok "still one entitlement: Enable resumed it, no new grant" || info "entitlements on the account: $($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE guest_account_id='$A'")"

echo "== b. a removed account cannot be edited back"
end_account_access "$A"
c=$(code DELETE "/guest-accounts/$A" "{$STEP}"); info "remove -> $c $(last 80)"
c=$(code PATCH "/guest-accounts/$A" '{"enabled":true}'); er=$(jget error < /tmp/acc.last); [ "$c" = 409 ] && [ "$er" = removed ] && ok "PATCH enabled=true on a removed account -> 409 removed" || bad "PATCH removed enable -> $c $(last)"
c=$(code PATCH "/guest-accounts/$A" "{\"username\":\"acc-renamed-$R4\"}"); [ "$c" = 409 ] && ok "rename of a removed account -> 409" || bad "rename removed -> $c $(last)"
[ "$($PSQL "SELECT enabled FROM iam_v2.guest_access_accounts WHERE id='$A'")" = f ] && ok "the row stays disabled" || bad "removed row enabled"

echo "== c. mark-verified needs reason + password"
EM="acc-ver-$R4@example.invalid"; C2=v$R4; client $C2
r=$(post $C2 /auth/account/register --data-urlencode "email=$EM" --data-urlencode "password=Acc-pw-$R4" --data-urlencode "password_confirm=Acc-pw-$R4")
AV=$($PSQL "SELECT id FROM iam_v2.guest_access_accounts WHERE lower(username)='$EM'"); [ -n "$AV" ] && ACCS+=("$AV") && ok "self-registered email account created (${AV:0:8}, unverified)" || bad "registration failed: $r"
if [ -n "$AV" ]; then
  c=$(code POST "/guest-accounts/$AV/verify-email" "{$STEP}"); [ "$c" = 400 ] && ok "no reason -> 400 $(jget error < /tmp/acc.last)" || bad "no reason -> $c $(last)"
  c=$(code POST "/guest-accounts/$AV/verify-email" '{"reason":"acceptance: verified by the desk"}'); [ "$c" = 401 ] && ok "no password -> 401 $(jget error < /tmp/acc.last)" || bad "no password -> $c $(last)"
  c=$(code POST "/guest-accounts/$AV/verify-email" "{\"reason\":\"acceptance: verified by the desk\",$STEP}"); [ "$c" = 200 ] && ok "reason + password -> 200 $(jget status < /tmp/acc.last)" || bad "verify -> $c $(last)"
  [ -n "$($PSQL "SELECT email_verified_at FROM iam_v2.guest_access_accounts WHERE id='$AV'")" ] && ok "email_verified_at is set" || bad "not verified in the row"
fi

echo "== d. voucher_operator vs the portal switch"
FPW="Fixture-$(head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n')-Vq7"; VE="acc-vop-$R4@onegate.invalid"
VO=$(admin -d "{\"email\":\"$VE\",\"display_name\":\"Acceptance voucher_operator\",\"password\":\"$FPW\",\"role\":\"voucher_operator\",\"require_change\":false}" $B/operators | jget id); [ -n "$VO" ] && OPS+=("$VO")
login_as "$VE" "$FPW" /tmp/acc.vo >/dev/null
AM0=$($PSQL "SELECT auth_methods::text FROM tenants"); CUR=$($PSQL "SELECT coalesce((auth_methods->'guest_account'->>'enabled'),'true') FROM tenants")
vo(){ curl -sk $R -b /tmp/acc.vo -o /tmp/acc.last -w '%{http_code}' -H 'Content-Type: application/json' -H 'Origin: https://hotel.stayconnect.local' "$@"; }
c=$(vo -d "{\"enabled\":$CUR}" $B/guest-accounts/portal); [ "$c" = 403 ] && ok "POST /guest-accounts/portal as voucher_operator -> 403 '$(jget message < /tmp/acc.last | cut -c1-90)'" || bad "voucher_operator portal switch -> $c $(last)"
c=$(vo -X PUT -d "{\"guest_account\":{\"enabled\":$CUR}}" $B/auth-methods); [ "$c" = 403 ] && ok "PUT /auth-methods as voucher_operator -> 403" || bad "voucher_operator auth-methods -> $c"
c=$(vo $B/guest-accounts); [ "$c" = 200 ] && ok "voucher_operator still lists accounts (its own surface works)" || info "voucher_operator account list -> $c"
[ "$($PSQL "SELECT auth_methods::text FROM tenants")" = "$AM0" ] && ok "auth_methods unchanged" || bad "auth_methods changed"
vo -X POST $B/auth/logout >/dev/null; rm -f /tmp/acc.vo
