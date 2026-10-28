#!/usr/bin/env bash
# Self-test for backfill-db-urls.sh (audit X3): runs the real script on a sandbox /etc/stayconnect and asserts
#   - a missing *_DB_URL is added from .dsn, exactly once (a re-run adds nothing);
#   - an existing value is never changed, including an empty one (reported instead);
#   - other lines, the file mode and an unterminated last line survive;
#   - an absent env file is not created; no .dsn = refused, nothing written;
#   - no secret value is printed.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }
E="$T/etc"; mkdir -p "$E"

printf 'SCD_DB_URL=postgres://svc_scd:SCDSECRET@127.0.0.1:5432/stayconnect_site?sslmode=disable\nEDGED_DB_URL=postgres://svc_edged:EDGEDSECRET@127.0.0.1:5432/stayconnect_site?sslmode=disable\nNETD_DB_URL=postgres://svc_netd:NETDSECRET@127.0.0.1:5432/stayconnect_site?sslmode=disable\nACCTD_DB_URL=postgres://svc_acctd:ACCTDSECRET@127.0.0.1:5432/stayconnect_site?sslmode=disable\n' > "$E/.dsn"
printf 'SCD_SOCKET=/run/stayconnect/scd.sock\nSCD_DB_URL=postgres://operator:CHOSEN@db/x\n' > "$E/scd.env"     # present: keep
printf 'EDGED_SCD_SOCKET=/run/stayconnect/scd.sock\nEDGED_COOKIE_SECURE=true' > "$E/edged.env"               # missing, no final newline
printf 'NETD_WAN_IFACE=ens160\nNETD_DB_URL=\n' > "$E/netd.env"                                                # present but empty
chmod 0640 "$E/edged.env"
# acctd.env deliberately absent

out="$(bash "$HERE/backfill-db-urls.sh" "$E" 2>&1)"; rc=$?
[ "$rc" = 0 ] && ok "runs (exit 0)" || bad "exit $rc: $out"
grep -qx 'SCD_DB_URL=postgres://operator:CHOSEN@db/x' "$E/scd.env" && [ "$(grep -c '^SCD_DB_URL=' "$E/scd.env")" = 1 ] \
  && ok "an existing value is kept, unchanged" || bad "scd.env changed: $(cat "$E/scd.env")"
grep -qx 'EDGED_COOKIE_SECURE=true' "$E/edged.env" && grep -q '^EDGED_DB_URL=postgres://svc_edged:EDGEDSECRET@' "$E/edged.env" \
  && grep -qx 'EDGED_SCD_SOCKET=/run/stayconnect/scd.sock' "$E/edged.env" \
  && ok "a missing key is added from .dsn on its own line; the other lines survive" || bad "edged.env: $(cat "$E/edged.env")"
[ "$(stat -c %a "$E/edged.env")" = 640 ] && ok "the file mode is kept" || bad "mode is $(stat -c %a "$E/edged.env")"
grep -qx 'NETD_DB_URL=' "$E/netd.env" && ! grep -q 'NETDSECRET' "$E/netd.env" && printf '%s' "$out" | grep -q 'netd.env sets NETD_DB_URL to an empty value' \
  && ok "an empty existing value is reported, not replaced" || bad "netd.env: $(cat "$E/netd.env") / $out"
[ ! -e "$E/acctd.env" ] && ok "an absent env file is not created" || bad "acctd.env was created"
printf '%s' "$out" | grep -q 'SECRET' && bad "a secret was printed" || ok "no secret is printed"
cp "$E/edged.env" "$T/edged.before"
bash "$HERE/backfill-db-urls.sh" "$E" >/dev/null 2>&1
cmp -s "$E/edged.env" "$T/edged.before" && ok "a re-run changes nothing" || bad "a re-run changed edged.env"
rm "$E/.dsn"; printf 'EDGED_SCD_SOCKET=x\n' > "$E/edged.env"
bash "$HERE/backfill-db-urls.sh" "$E" >/dev/null 2>&1 && bad "ran without .dsn"
grep -q DB_URL "$E/edged.env" && bad "wrote without .dsn" || ok "no .dsn: refused, nothing written"

# both install paths call it
grep -q 'backfill-db-urls.sh' "$HERE/provision-fresh-appliance.sh" && ok "provision-fresh-appliance.sh runs it" || bad "provisioning does not run it"
grep -q 'backfill-db-urls.sh' "$HERE/appliance-install.sh" && ok "appliance-install.sh runs it" || bad "the installer does not run it"
exit "$fail"
