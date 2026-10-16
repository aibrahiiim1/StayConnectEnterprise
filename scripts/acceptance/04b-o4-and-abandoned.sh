#!/bin/bash
# 4b. O-4 with a fresh account whose catalogue is visible, and the state of the abandoned checkout(s) recorded
# by 04-commerce.sh (re-run after checkout_expiry_minutes + reconcile_grace_minutes to see the reconciler's end).
. "$(dirname "$0")/lib.sh"
DEFTIERS='[{"order":1,"grant":{}}]'
if [ "${1:-}" != abandoned-only ]; then
echo "== O-4: a per-stay-night allowance package is not offered to an account"
REV=$(mkplan "{\"code\":\"acc-o4-$R4\",\"name\":\"Acc O4\",\"down_kbps\":20000,\"up_kbps\":10000,\"max_concurrent_devices\":1,\"device_limit_policy\":\"REJECT_NEW_DEVICE\",\"time_accounting_mode\":\"VALIDITY_WINDOW\",\"speed_allocation\":\"PER_DEVICE\",\"create_only\":true}")
PN=$(mkpkg "{\"code\":\"acc-night2-$R4\",\"service_plan_revision_id\":\"$REV\",\"display\":{\"name\":\"Acc per night two\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,\"data_allocation_policy\":{\"mode\":\"PER_STAY_NIGHT\",\"gb_per_night\":1,\"min_gb\":1,\"max_gb\":5},\"create_only\":true}")
PF=$(mkpkg "{\"code\":\"acc-flat2-$R4\",\"service_plan_revision_id\":\"$REV\",\"display\":{\"name\":\"Acc flat two\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,\"create_only\":true}")
U="acc-o4-$R4"; A=$(mkacc "$U"); ACCS+=("$A"); C=n$R4; client $C; signin $C "$U" >/dev/null; gget $C /packages > /tmp/$C.pk
grep -q "Acc flat two" /tmp/$C.pk && ok "the catalogue is visible to the account (control package 'Acc flat two' listed)" || bad "control package not listed: $(pagetext /tmp/$C.pk 120)"
grep -q "Acc per night two\|$PN" /tmp/$C.pk && bad "O-4: the per-night package IS offered to an account" || ok "the per-night package is NOT offered to the account"
r=$(acquire $C "$PN"); [ "$(online $C)" != 204 ] && ok "acquiring it by id is refused ($(pagetext /tmp/$C.html 120 | sed 's/.*Wi-Fi //' | cut -c1-80))" || bad "per-night package acquired by an account"
fi
echo "== abandoned checkout(s) from 04-commerce.sh"
ST=$(admin $B/payment-providers/settings | python3 -c 'import json,sys;s=json.load(sys.stdin)["settings"];print(s["checkout_expiry_minutes"],s["reconcile_grace_minutes"])'); info "checkout expiry / grace (minutes): $ST"
while read -r id st cy amt; do [ -n "$id" ] || continue
  row=$($PSQL "SELECT pt.status||' age='||round(extract(epoch from now()-pt.intent_created_at)/60)||'min settlement='||coalesce(s.status,'-')||' purchase='||coalesce(pu.state,'-') FROM iam_v2.payment_transactions pt LEFT JOIN iam_v2.settlements s ON s.id=pt.settlement_id LEFT JOIN iam_v2.purchases pu ON pu.id=s.purchase_id WHERE pt.id='$id'")
  info "txn ${id:0:8}: $row"
  case "$row" in FAILED*|UNKNOWN*) ok "abandoned checkout resolved by the reconciler: $row";; *) info "still open (resolution due after expiry+grace)";; esac
done < /root/acceptance/abandoned.txn
info "stranded-purchase sweeps recorded: $($PSQL "SELECT count(*) FROM iam_v2.card_purchase_sweeps" 2>/dev/null)"
