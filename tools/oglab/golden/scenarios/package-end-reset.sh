#!/usr/bin/env bash
# Staff End Package / Reset Package on the real services: End removes the package's kernel authorisation (scd withdraw) and only
# its own; Reset of the ended package makes a zero-price ADMIN_GRANT cycle the guest can sign in to again; nothing is posted or charged.
. "$(dirname "$0")/lib.sh"

signin 0 2
wait_for 60 "2 guests authorised" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -ge 2 ]"
read -r ent other <<<"$(sql "SELECT string_agg(e.id::text, ' ') FROM (SELECT DISTINCT s.entitlement_id AS id FROM iam_v2.sessions s
  WHERE s.state='active' AND s.ended IS NULL LIMIT 2) e")"
[ -n "${ent:-}" ] && [ -n "${other:-}" ] || fail "two live packages were expected"
before=$(authcount)
rid() { cat /proc/sys/kernel/random/uuid 2>/dev/null || python3 -c 'import uuid;print(uuid.uuid4())'; }
out="$(api POST "/package-controls/$ent/end" "{\"password\":\"@PW\",\"reason\":\"lab end\",\"request_id\":\"$(rid)\"}")"
echo "$out" | head -c 400; echo
echo "$out" | grep -q '"outcome": "ENDED"' || fail "End did not report ENDED"
wait_for 15 "the ended package's authorisation is gone and only it" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -eq $((before-1)) ]"
[ "$(sql "SELECT status FROM iam_v2.entitlements WHERE id='$other'")" = ACTIVE ] || fail "another guest's package was touched"
out="$(api POST "/package-controls/$ent/reset" "{\"password\":\"@PW\",\"reason\":\"lab reset\",\"request_id\":\"$(rid)\"}")"
echo "$out" | head -c 400; echo
new="$(echo "$out" | sed -n 's/.*"new_entitlement_id": "\([0-9a-f-]*\)".*/\1/p' | head -1)"
[ -n "$new" ] || fail "Reset returned no new package"
[ "$(sql "SELECT p.trigger||':'||p.amount_minor FROM iam_v2.entitlements e JOIN iam_v2.purchases p ON p.id=e.purchase_id WHERE e.id='$new'")" = "ADMIN_GRANT:0" ] \
  || fail "the reset is not a zero-price administrative grant"
[ "$(sql "SELECT count(*) FROM iam_v2.posting_outbox po JOIN iam_v2.entitlements e ON e.purchase_id=po.purchase_id WHERE e.id='$new'" 2>/dev/null || echo 0)" = 0 ] \
  || fail "a reset was posted to the PMS"
signin 0 2
wait_for 60 "the guest signed in again on the reset package" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -ge 2 ]"
