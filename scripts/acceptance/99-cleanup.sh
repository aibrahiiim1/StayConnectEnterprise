#!/bin/bash
# FINAL CLEANUP AND PROOF (run as root on the appliance): every acceptance fixture is retired and counted.
#   accounts acc-*   : live access ended, removed (history kept) or deleted
#   packages acc-*   : deactivated (catalogue history keeps them; they cannot be deleted)
#   operators acc-*@onegate.invalid : disabled
#   namespaces, sign-in restrictions on fixture devices, walled-garden fixture rules: none left
. "$(dirname "$0")/lib.sh"
echo "== retire fixtures"
for a in $($PSQL "SELECT id FROM iam_v2.guest_access_accounts WHERE (username LIKE 'acc-%' OR username LIKE 'removed:%acc-%') AND (enabled OR username NOT LIKE 'removed:%')"); do
  end_account_access "$a"; admin -o /dev/null -X DELETE -d "{$STEP}" "$B/guest-accounts/$a"; done
for p in $($PSQL "SELECT id FROM iam_v2.internet_packages WHERE code LIKE 'acc-%' AND active"); do
  admin -o /dev/null -d "{\"active\":false,\"reason\":\"acceptance fixture retired\",$STEP}" "$B/commercial-packages/$p/active"; done
for o in $($PSQL "SELECT id FROM operators WHERE email LIKE 'acc-%@onegate.invalid' AND status <> 'disabled'"); do admin -o /dev/null -X DELETE "$B/operators/$o"; done
for n in $(ip netns list | awk '{print $1}' | grep -E '^[a-z]+[0-9]*[0-9a-f]{4}$'); do rmclient "$n"; done
echo "== proof"
info "fixture accounts still enabled: $($PSQL "SELECT count(*) FROM iam_v2.guest_access_accounts WHERE username ILIKE '%acc-%' AND enabled")"
info "fixture accounts (any state): $($PSQL "SELECT count(*) FROM iam_v2.guest_access_accounts WHERE username ILIKE '%acc-%'")"
info "live entitlements of fixture accounts: $($PSQL "SELECT count(*) FROM iam_v2.entitlements e JOIN iam_v2.guest_access_accounts a ON a.id=e.guest_account_id WHERE a.username ILIKE '%acc-%' AND e.status IN ('ACTIVE','PENDING','SUSPENDED')")"
info "live entitlements on fixture packages: $($PSQL "SELECT count(*) FROM iam_v2.entitlements e JOIN iam_v2.internet_package_revisions r ON r.id=e.package_revision_id JOIN iam_v2.internet_packages p ON p.id=r.package_id WHERE p.code LIKE 'acc-%' AND e.status IN ('ACTIVE','PENDING','SUSPENDED')" 2>/dev/null)"
info "fixture packages active: $($PSQL "SELECT count(*) FROM iam_v2.internet_packages WHERE code LIKE 'acc-%' AND active") of $($PSQL "SELECT count(*) FROM iam_v2.internet_packages WHERE code LIKE 'acc-%'")"
info "fixture operators not disabled: $($PSQL "SELECT count(*) FROM operators WHERE email LIKE 'acc-%@onegate.invalid' AND status <> 'disabled'") of $($PSQL "SELECT count(*) FROM operators WHERE email LIKE 'acc-%@onegate.invalid'")"
info "open sessions on fixture accounts: $($PSQL "SELECT count(*) FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id=s.entitlement_id JOIN iam_v2.guest_access_accounts a ON a.id=e.guest_account_id WHERE a.username ILIKE '%acc-%' AND s.ended IS NULL")"
info "namespaces left: $(ip netns list | wc -l)"
info "walled-garden fixture rules: $(admin $B/walled-garden | grep -c 'acceptance')"
info "auth_methods: $($PSQL "SELECT auth_methods::text FROM tenants")"
info "operators by status: $($PSQL "SELECT string_agg(status||'='||n,' ') FROM (SELECT status, count(*) n FROM operators GROUP BY status) x")"
