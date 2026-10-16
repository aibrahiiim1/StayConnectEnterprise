#!/bin/bash
# 1. OPERATOR SECURITY (audit N-1, N-8, N-13, N-23, N-24, D-B). Run as root on the appliance.
#   a. login throttle: a throwaway fixture operator, wrong passwords until locked -> 429; audit rows
#      operator.login_failed / operator.login_locked; the right password is refused while locked.
#   b. step-up throttle: a second fixture (hotel_it_manager), wrong step-up passwords on the backup ticket until
#      429; audit operator.reauth_failed / operator.reauth_locked.
#   c. must-change: the seeded admin/admin signs in (correct password, no failure counted), whoami answers, and
#      GET /operators is refused 403 password_change_required. Its password is NOT changed.
#   d. backup download: site_viewer fixture sees the list but gets no ticket (403); the acceptance site_admin
#      gets 401 without a password, a ticket with it, 403 for the file without a ticket, 200 with the ticket.
#   e. logs: none of the passwords used here appear in the edged/scd/portald journal since the run started.
# Budget: at most 10 failed passwords from 127.0.0.1 (the per-address lock is 20 / 15 min by default).
. "$(dirname "$0")/lib.sh"
T0=$(date '+%Y-%m-%d %H:%M:%S')
FPW="Fixture-$(head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n')-Zq9"
mkop(){ local email=$1 role=$2 id; id=$(admin -d "{\"email\":\"$email\",\"display_name\":\"Acceptance $role\",\"password\":\"$FPW\",\"role\":\"$role\",\"require_change\":false}" $B/operators | jget id); echo "$id"; }
echo "== a. login throttle"
POL=$(admin $B/operators/signin-protection); info "policy: $(echo "$POL" | python3 -c 'import json,sys;d=json.load(sys.stdin);d=d.get("policy",d);print({k:d.get(k) for k in ("account_max_failures","ip_max_failures","lockout_seconds")})')"
E1="acc-throttle-$R4@onegate.invalid"; O1=$(mkop "$E1" site_viewer); [ -n "$O1" ] && OPS+=("$O1") && ok "fixture operator created (${O1:0:8}, must_change=false)" || bad "fixture operator create: $(last)"
fails=0; locked=""; codes=""
for i in $(seq 1 12); do
  c=$(login_as "$E1" "wrong-$i-$R4-xxxxxxxx" /tmp/acc.f1); codes="$codes $c"
  if [ "$c" = 429 ]; then ra=$(jget retry_after_seconds < /tmp/acc.login); if [ "${ra:-0}" -gt 60 ]; then locked=$ra; break; fi; sleep "$ra"; continue; fi
  [ "$c" = 401 ] && fails=$((fails+1))
done
info "answers:$codes"
[ -n "$locked" ] && ok "after $fails wrong passwords the account is locked: 429, Retry-After ${locked}s, '$(jget message < /tmp/acc.login)'" || bad "no lockout after $fails failures"
c=$(login_as "$E1" "$FPW" /tmp/acc.f1); [ "$c" = 429 ] && ok "the RIGHT password is refused while locked (429)" || bad "right password while locked -> $c"
nf=$($PSQL "SELECT count(*) FROM audit_log WHERE action='operator.login_failed' AND target_id='$O1' AND ts>'$T0'"); nl=$($PSQL "SELECT count(*) FROM audit_log WHERE action='operator.login_locked' AND target_id='$O1' AND ts>'$T0'")
[ "$nf" -ge 5 ] && [ "$nl" -ge 1 ] && ok "audit: $nf operator.login_failed + $nl operator.login_locked rows for the fixture" || bad "audit rows failed=$nf locked=$nl"
leak=$($PSQL "SELECT count(*) FROM audit_log WHERE ts>'$T0' AND payload::text ILIKE '%wrong-%-$R4%'"); [ "$leak" = 0 ] && ok "no attempted password is stored in the audit payloads" || bad "$leak audit payloads contain an attempted password"

echo "== b. step-up throttle (backup ticket with wrong passwords)"
E2="acc-stepup-$R4@onegate.invalid"; O2=$(mkop "$E2" hotel_it_manager); [ -n "$O2" ] && OPS+=("$O2")
c=$(login_as "$E2" "$FPW" /tmp/acc.f2); [ "$c" = 200 ] && ok "hotel_it_manager fixture signs in" || bad "fixture login -> $c $(head -c 120 /tmp/acc.login)"
ART=$(admin $B/backups/artifacts | python3 -c 'import json,sys;d=json.load(sys.stdin);a=d.get("artifacts") or d.get("data") or d
a=[x for x in a if isinstance(x,dict)];a=[x for x in a if (x.get("kind") or x.get("type") or "").lower().find("db")>=0 or str(x.get("name","")).endswith((".dump",".sql.gz",".gz",".dump.gz"))] or a
print(sorted(a,key=lambda x:x.get("size_bytes",x.get("size",0)))[0]["name"] if a else "")' 2>/dev/null)
info "backup artifact used: ${ART:-none}"
f2(){ curl -sk $R -b /tmp/acc.f2 -c /tmp/acc.f2 -o /tmp/acc.last -w '%{http_code}' -H 'Content-Type: application/json' -H 'Origin: https://hotel.stayconnect.local' "$@"; }
sfails=0; slocked=""; codes=""
for i in $(seq 1 12); do
  c=$(f2 -d "{\"password\":\"wrong-step-$i-$R4\"}" "$B/backup-download/$ART/ticket"); codes="$codes $c"
  if [ "$c" = 429 ]; then ra=$(jget retry_after_seconds < /tmp/acc.last); if [ "${ra:-0}" -gt 60 ]; then slocked=$ra; break; fi; sleep "$ra"; continue; fi
  [ "$c" = 401 ] && sfails=$((sfails+1))
done
info "answers:$codes"
[ -n "$slocked" ] && ok "step-up locked after $sfails wrong passwords: 429 '$(jget message < /tmp/acc.last)'" || bad "step-up not throttled after $sfails"
c=$(f2 -d "{\"password\":\"$FPW\"}" "$B/backup-download/$ART/ticket"); [ "$c" = 429 ] && ok "the right step-up password is refused while locked (429)" || bad "right step-up while locked -> $c"
nf=$($PSQL "SELECT count(*) FROM audit_log WHERE action='operator.reauth_failed' AND target_id='$O2' AND ts>'$T0'"); nl=$($PSQL "SELECT count(*) FROM audit_log WHERE action='operator.reauth_locked' AND target_id='$O2' AND ts>'$T0'")
[ "$nf" -ge 5 ] && [ "$nl" -ge 1 ] && ok "audit: $nf operator.reauth_failed + $nl operator.reauth_locked" || bad "reauth audit rows failed=$nf locked=$nl"
f2 -o /dev/null -X POST $B/auth/logout >/dev/null

echo "== c. must-change state (admin/admin; read-only, its password is not changed)"
MC0=$($PSQL "SELECT must_change_password FROM operators WHERE lower(email)='admin'")
c=$(login_as admin admin /tmp/acc.mc); mc=$(jget must_change_password < /tmp/acc.login)
[ "$c" = 200 ] && [ "$mc" = True ] && ok "admin/admin signs in with must_change_password=true" || bad "admin/admin login -> $c must_change=$mc"
mcc(){ curl -sk $R -b /tmp/acc.mc -o /tmp/acc.last -w '%{http_code}' -H 'Origin: https://hotel.stayconnect.local' "$@"; }
c=$(mcc $B/operators); er=$(jget error < /tmp/acc.last); [ "$c" = 403 ] && [ "$er" = password_change_required ] && ok "GET /operators -> 403 password_change_required" || bad "GET /operators -> $c $er"
c=$(mcc $B/sessions); [ "$c" = 403 ] && ok "GET /sessions -> 403 as well" || bad "GET /sessions -> $c"
c=$(mcc $B/auth/whoami); [ "$c" = 200 ] && ok "whoami still answers (200)" || bad "whoami -> $c"
mcc -X POST $B/auth/logout >/dev/null
[ "$($PSQL "SELECT must_change_password FROM operators WHERE lower(email)='admin'")" = "$MC0" ] && ok "admin's must-change flag unchanged ($MC0); password untouched" || bad "admin must-change flag changed"

echo "== d. backup download: role + password"
E3="acc-viewer-$R4@onegate.invalid"; O3=$(mkop "$E3" site_viewer); [ -n "$O3" ] && OPS+=("$O3"); login_as "$E3" "$FPW" /tmp/acc.f3 >/dev/null
f3(){ curl -sk $R -b /tmp/acc.f3 -o /tmp/acc.last -w '%{http_code}' -H 'Content-Type: application/json' -H 'Origin: https://hotel.stayconnect.local' "$@"; }
c=$(f3 $B/backups/artifacts); [ "$c" = 200 ] && ok "site_viewer sees the backup list (200)" || info "site_viewer list -> $c"
c=$(f3 -d "{\"password\":\"$FPW\"}" "$B/backup-download/$ART/ticket"); [ "$c" = 403 ] && ok "site_viewer with the right password gets NO ticket (403 $(jget error < /tmp/acc.last))" || bad "site_viewer ticket -> $c $(last)"
c=$(f3 -X POST -d "ticket=x" -H 'Content-Type: application/x-www-form-urlencoded' "$B/backup-download/$ART"); [ "$c" = 403 ] && ok "site_viewer file download refused (403)" || bad "site_viewer file -> $c"
f3 -X POST $B/auth/logout >/dev/null
c=$(code POST "/backup-download/$ART/ticket" '{}'); [ "$c" = 401 ] && ok "site_admin without a password -> 401 $(jget error < /tmp/acc.last)" || bad "site_admin no password -> $c $(last)"
c=$(admin -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/x-www-form-urlencoded' -d "ticket=forged" "$B/backup-download/$ART"); [ "$c" = 403 ] && ok "file without a valid ticket -> 403" || bad "forged ticket -> $c"
c=$(code POST "/backup-download/$ART/ticket" "{$STEP}"); TK=$(jget ticket < /tmp/acc.last)
[ "$c" = 200 ] && [ -n "$TK" ] && ok "site_admin with password -> one-time ticket" || bad "ticket with password -> $c $(last)"
r=$(curl -sk $R -b $CK -H 'Origin: https://hotel.stayconnect.local' -o /dev/null -w '%{http_code} %{size_download}' -X POST --data-urlencode "ticket=$TK" "$B/backup-download/$ART")
[ "${r%% *}" = 200 ] && ok "download with the ticket -> 200 (${r#* } bytes, discarded)" || bad "download with ticket -> $r"
r=$(curl -sk $R -b $CK -H 'Origin: https://hotel.stayconnect.local' -o /dev/null -w '%{http_code}' -X POST --data-urlencode "ticket=$TK" "$B/backup-download/$ART"); [ "$r" = 403 ] && ok "the ticket is single-use (second use 403)" || bad "ticket reuse -> $r"
na=$($PSQL "SELECT count(*) FROM audit_log WHERE action='backup.download_authorized' AND ts>'$T0'"); [ "$na" -ge 1 ] && ok "audit backup.download_authorized ($na)" || bad "no download audit row"

echo "== e. logs carry no passwords"
L=$(journalctl -u stayconnect-edged -u stayconnect-scd -u stayconnect-portald -u stayconnect-caddy --since "$T0" --no-pager 2>/dev/null)
n=0; for s in "$FPW" "$ACC_PW" "wrong-1-$R4" "wrong-step-1-$R4"; do printf '%s' "$L" | grep -qF -- "$s" && n=$((n+1)); done
[ "$n" = 0 ] && ok "none of 4 passwords used appear in $(printf '%s\n' "$L" | wc -l) journal lines since $T0" || bad "$n passwords found in the journal"
printf '%s' "$L" | grep -qi '"password"' && bad "a journal line carries a password field" || ok "no journal line carries a password field"
rm -f /tmp/acc.f1 /tmp/acc.f2 /tmp/acc.f3 /tmp/acc.mc
