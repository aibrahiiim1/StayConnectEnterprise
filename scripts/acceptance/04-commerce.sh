#!/bin/bash
# 4. COMMERCE (audit O-1, O-4, O-13, currency policy, abandoned checkouts). Run as root on the appliance.
# Fixture plan + fixture priced packages only; Stripe TEST; no room charge; the live payment account is never
# changed except a display-name change that is restored at once (and a merchant change that must be refused).
#   a. repin keeps price and methods (O-1): a revision without commercial terms is refused; the console's repin
#      payload moves the plan and keeps GBP 1.00 / ONLINE_PAYMENT
#   b. provider converts ON: a USD card package is offered by card to a Personal account
#   c. abandoned checkout: a USD card checkout is started and never paid (state is re-read by 04b later)
#   d. O-13 while that checkout is open: display-name change allowed (restored), merchant-ref change -> 409
#   e. O-4: a per-stay-night allowance package is not offered to an account
. "$(dirname "$0")/lib.sh"
DEFTIERS='[{"order":1,"grant":{}}]'
TS0=$($PSQL "SELECT now()")
cur(){ admin "$B/commercial-packages/$1/current"; }
PLANJ="{\"code\":\"acc-com-$R4\",\"name\":\"Acc commerce\",\"down_kbps\":20000,\"up_kbps\":10000,\"max_concurrent_devices\":2,\"device_limit_policy\":\"REJECT_NEW_DEVICE\",\"time_accounting_mode\":\"VALIDITY_WINDOW\",\"speed_allocation\":\"PER_DEVICE\""
REV1=$(mkplan "$PLANJ,\"create_only\":true}"); case "$REV1" in ERR*) bad "plan: $REV1"; exit 1;; esac
pk(){ # pk CODE NAME CURRENCY PRICE METHODS [EXTRA]
  mkpkg "{\"code\":\"$1-$R4\",\"service_plan_revision_id\":\"$REV1\",\"display\":{\"name\":\"$2\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,\"price_minor\":$4,\"currency\":\"$3\",\"currency_exponent\":2,\"acquisition_methods\":$5,\"create_only\":true${6:+,$6}}"; }

echo "== a. repin keeps price and methods (O-1)"
PG=$(pk acc-gbp "Acc GBP card" GBP 100 '["ONLINE_PAYMENT"]'); case "$PG" in ERR*) bad "GBP package: $PG"; exit 1;; esac; ok "GBP 1.00 card package (${PG:0:8})"
REV2=$(mkplan "$PLANJ,\"down_kbps\":15000,$STEP}"); case "$REV2" in ERR*) bad "plan revision: $REV2";; *) [ "$REV2" != "$REV1" ] && ok "plan revised (rev ${REV1:0:8} -> ${REV2:0:8})" || bad "same revision";; esac
CUR=$(cur $PG); echo "$CUR" > /tmp/acc.cur
c=$(code POST /commercial-packages "{\"code\":\"acc-gbp-$R4\",\"service_plan_revision_id\":\"$REV2\",\"display\":{\"name\":\"Acc GBP card\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,$STEP}")
[ "$c" = 400 ] && ok "a revision without the commercial terms is refused: 400 $(jget error < /tmp/acc.last)" || bad "revision without terms -> $c $(last)"
REPIN=$(python3 - "$REV2" "$ACC_PW" <<'PY'
import json,sys
cur=json.load(open('/tmp/acc.cur')); rev=sys.argv[1]
c=cur.get('package',cur) if isinstance(cur,dict) else cur
price=c.get('price_minor'); cy=(c.get('currency') or '').upper(); ex=c.get('currency_exponent')
m=[x for x in (c.get('settlement_methods') or []) if x]
out={'price_minor':price,'currency':cy,'currency_exponent':ex,'acquisition_methods':m,
     'room_charge_mappings':[{k:v for k,v in x.items() if k in('pms_interface_id','posting_code','tax_code','tax_rate_bp') and v not in (None,'')} for x in (c.get('room_charge_mappings') or [])],
     'code':c['code'],'service_plan_revision_id':rev,'display':c.get('display') or {'name':c['code']},
     'duration_policy':c.get('duration_policy') or {'end_mode':'MANUAL_END'},
     'eligibility_rules':[{'type':r.get('type',r.get('Type')),'value':r.get('value',r.get('Value')) or {}} for r in (c.get('eligibility_rules') or [])],
     'grant_tiers':[{'order':t.get('order',t.get('Order',10)),'grant':t.get('value',t.get('Value')) or {}} for t in (c.get('grant_tiers') or [])],
     'password':sys.argv[2]}
for k in ('package_type','plan_overrides','renewable','max_purchases_per_stay','visible_from','visible_until'):
  if c.get(k) not in (None,''): out[k]=c[k]
if c.get('data_allocation_policy'): out['data_allocation_policy']=c['data_allocation_policy']
print(json.dumps(out))
PY
)
c=$(code POST /commercial-packages "$REPIN"); [ "$c" = 200 ] || [ "$c" = 201 ] && ok "repin with the console's payload -> $c" || bad "repin -> $c $(last)"
cur $PG | python3 -c '
import json,sys;c=json.load(sys.stdin);c=c.get("package",c)
print("  info  after repin: plan_rev=%s price=%s %s methods=%s" % (str(c.get("service_plan_revision_id"))[:8], c.get("price_minor"), c.get("currency"), c.get("settlement_methods")))
sys.exit(0 if c.get("price_minor")==100 and c.get("currency")=="GBP" and c.get("settlement_methods")==["ONLINE_PAYMENT"] and c.get("service_plan_revision_id")==sys.argv[1] else 1)' "$REV2" && ok "repin moved the plan and kept GBP 1.00 / ONLINE_PAYMENT" || bad "repin lost price/methods or did not move the plan"

echo "== b. provider converts: USD package offered by card to an account"
ACCT=$(admin $B/payment-providers/accounts | python3 -c 'import json,sys;d=json.load(sys.stdin);a=d.get("accounts",d);a=[x for x in a if x.get("is_default")][0];print(json.dumps(a))')
ACCT0=$(echo "$ACCT" | python3 -c 'import json,sys;a=json.load(sys.stdin);print("|".join(str(a.get(k)) for k in ("provider","merchant_account_ref","display_name","currency","mode","status","is_default","provider_converts")))')
info "default account: $(echo "$ACCT0" | awk -F'|' '{print $1, $4, $5, $6, "provider_converts="$8}')"
[ "$(echo "$ACCT0" | cut -d'|' -f8)" = True ] && ok "provider converts is ON on the default (GBP) Stripe TEST account" || blk "provider converts is OFF; not switched by this harness"
PU=$(pk acc-usd "Acc USD card" USD 100 '["ONLINE_PAYMENT"]'); case "$PU" in ERR*) bad "USD package: $PU"; exit 1;; esac
U="acc-com-$R4"; A=$(mkacc "$U"); ACCS+=("$A"); C=k$R4; client $C; signin $C "$U" >/dev/null
gget $C /packages > /tmp/$C.pk
r=$(python3 - /tmp/$C.pk "$PU" "$PG" <<'PY'
import re,sys
t=open(sys.argv[1],encoding='utf-8',errors='replace').read()
ok=lambda pid: any(pid in f and 'ONLINE_PAYMENT' in f for f in re.findall(r'<form[^>]*>.*?</form>',t,flags=re.S))
print(f"{ok(sys.argv[2])} {ok(sys.argv[3])}")
PY
)
[ "${r%% *}" = True ] && ok "the USD package is offered BY CARD to the account (provider converts)" || bad "USD package not offered by card: $(grep -c 'Acc USD card' /tmp/$C.pk) name hits"
[ "${r#* }" = True ] && ok "the GBP package is offered by card as well" || bad "GBP package not offered by card"

echo "== c. abandoned checkout (USD, never paid)"
r=$(acquire $C "$PU" --data-urlencode "method=ONLINE_PAYMENT"); URL=${r#* }
case "$URL" in https://checkout.stripe.com/*) ok "plain form post -> 303 to Stripe hosted checkout";; *) bad "acquire -> $r $(pagetext /tmp/$C.html 160)";; esac
r2=$(ip netns exec $C curl -s -o /tmp/$C.nav -c /tmp/$C.jar -b /tmp/$C.jar -w '%{http_code}' -H 'X-OneGate-Nav: 1' -X POST --data-urlencode "package_id=$PU" --data-urlencode "method=ONLINE_PAYMENT" "$P/packages/acquire")
info "the page's own (JS navigation) post -> $r2 $(head -c 120 /tmp/$C.nav | sed 's#cs_test_[A-Za-z0-9]*#cs_test_…#g')"
$PSQL "SELECT pt.id||' '||pt.status||' '||pt.currency||' '||pt.amount_minor FROM iam_v2.payment_transactions pt WHERE pt.intent_created_at >= '$TS0' ORDER BY pt.intent_created_at" > /root/acceptance/abandoned.txn
TX=$(head -1 /root/acceptance/abandoned.txn | tr ' ' '|'); info "checkout transactions started: $(wc -l < /root/acceptance/abandoned.txn); first ${TX:0:8} ${TX#*|} (the account settles GBP; the package price is USD 1.00)"
[ "$(echo "$TX" | cut -d'|' -f2)" = CREATED ] || [ "$(echo "$TX" | cut -d'|' -f2)" = PENDING ] && ok "the checkout is open (CREATED/PENDING) and will be abandoned" || info "transaction state: $TX"
OPEN=$($PSQL "SELECT count(*) FROM iam_v2.payment_transactions WHERE status IN ('CREATED','PENDING')"); info "open checkouts on the site: $OPEN"

echo "== d. O-13 while a checkout is open"
save(){ python3 - "$ACCT" "$1" "$2" "$ACC_PW" <<'PY'
import json,sys
a=json.loads(sys.argv[1]); f=sys.argv[2]; v=sys.argv[3]
b={k:a.get(k) for k in ("id","provider","merchant_account_ref","display_name","currency","mode","status","is_default")}
b[f]=v; b["credentials"]={}; b["reason"]="acceptance: O-13 check while a checkout is open"; b["password"]=sys.argv[4]
print(json.dumps(b))
PY
}
DN0=$(echo "$ACCT" | jget display_name); MR0=$(echo "$ACCT" | jget merchant_account_ref)
c=$(code POST /payment-providers/accounts "$(save display_name "$DN0 (acceptance)")"); [ "$c" = 200 ] && ok "display-name change allowed while a checkout is open (200)" || bad "display-name change -> $c $(last)"
c=$(code POST /payment-providers/accounts "$(save display_name "$DN0")"); [ "$c" = 200 ] && ok "display name restored (200)" || bad "display-name restore -> $c $(last)"
c=$(code POST /payment-providers/accounts "$(save merchant_account_ref "acct_acceptanceO13x")"); er=$(jget error < /tmp/acc.last)
if [ "$c" = 409 ] && [ "$er" = checkouts_open ]; then ok "merchant-ref change refused: 409 checkouts_open"; else bad "merchant-ref change -> $c $(last)"
  [ "$c" = 200 ] && { code POST /payment-providers/accounts "$(save merchant_account_ref "$MR0")" >/dev/null; info "merchant ref RESTORED immediately"; }; fi
ACCT1=$(admin $B/payment-providers/accounts | python3 -c 'import json,sys;d=json.load(sys.stdin);a=d.get("accounts",d);a=[x for x in a if x.get("is_default")][0];print("|".join(str(a.get(k)) for k in ("provider","merchant_account_ref","display_name","currency","mode","status","is_default","provider_converts")))')
[ "$ACCT1" = "$ACCT0" ] && ok "the payment account is exactly as before (provider, merchant, name, currency, mode, status, default, converts)" || bad "payment account differs: before $ACCT0 after $ACCT1"

echo "== e. O-4 per-stay-night allowance package"
PN=$(mkpkg "{\"code\":\"acc-night-$R4\",\"service_plan_revision_id\":\"$REV2\",\"display\":{\"name\":\"Acc per night\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,\"data_allocation_policy\":{\"mode\":\"PER_STAY_NIGHT\",\"gb_per_night\":1,\"min_gb\":1,\"max_gb\":5},\"create_only\":true}")
case "$PN" in ERR*) blk "per-night package could not be published: $PN";;
  *) ok "per-night allowance package published (${PN:0:8})"; gget $C /packages > /tmp/$C.pk2
     grep -q "$PN\|Acc per night" /tmp/$C.pk2 && bad "O-4: the per-night package is offered to an account" || ok "the per-night package is NOT offered to an account (others are: $(grep -c 'Acc USD card' /tmp/$C.pk2) USD hit)"
     r=$(acquire $C "$PN"); [ "$(online $C)" != 204 ] && ok "acquiring it directly by id is refused ($(pagetext /tmp/$C.html 90))" || bad "per-night package acquired by an account";;
esac
echo "$PU" > /root/acceptance/usd.pkg; echo "$PG" > /root/acceptance/gbp.pkg
# keep the USD/GBP card packages for the browser and room harnesses: drop them from this run's cleanup list
KEEP=" $PU $PG "; P2=(); for p in "${PKGS[@]}"; do [[ "$KEEP" == *" $p "* ]] || P2+=("$p"); done; PKGS=("${P2[@]}")
info "kept for 05/06: USD ${PU:0:8}, GBP ${PG:0:8} (retired by 99-cleanup.sh)"
