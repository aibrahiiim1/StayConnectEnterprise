#!/usr/bin/env bash
# SOCIAL SIGN-IN OFFERING ON THE HTTPS PORTAL, FROM A GUEST DEVICE (run as root on the appliance).
#
# Proves everything up to the provider's own login page, with a PLACEHOLDER Microsoft app (a made-up client id
# and secret that no provider will accept): the secret is sealed by scd and never stored in plaintext; the
# provider is offered only while HTTPS is active and the canonical hostname is a registered callback host; the
# authorize redirect carries https://<canonical>/auth/social/callback, PKCE S256 and a nonce; an Android WebView
# gets the open-in-browser page for Google/Facebook. A real round trip needs the hotel's own app (contract §5).
#
# Restores the site's sign-in methods exactly and deletes the placeholder provider on exit.
#   CA=<pem>   trust anchor for the portal certificate (a test CA); default: the system roots.
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
R='--resolve hotel.stayconnect.local:443:127.0.0.1'; B=https://hotel.stayconnect.local/api/edge/v1; CK=/tmp/ck.so
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
CAOPT=(); [ -n "${CA:-}" ] && CAOPT=(--cacert "$CA")
N=ogsoc; PID=""
AM0=$($PSQL "SELECT auth_methods::text FROM tenants LIMIT 1")
cleanup(){
  [ -n "$PID" ] && curl -sk $R -b $CK -o /dev/null -X DELETE "$B/social-providers/$PID"
  # The auth-methods API merges a partial update, so it cannot remove the "social" key this test added: the
  # site's original value is restored as it was read.
  $PSQL "UPDATE tenants SET auth_methods = \$am\$$AM0\$am\$::jsonb" >/dev/null
  [ "$($PSQL "SELECT auth_methods::text FROM tenants LIMIT 1")" = "$AM0" ] && echo "  (sign-in methods restored)" || echo "  WARNING: sign-in methods NOT restored: $AM0"
  ip netns exec $N dhclient -r -lf /var/lib/dhcp/dhclient-$N.leases -pf /run/dhclient-$N.pid ${N}c >/dev/null 2>&1
  ip netns del $N 2>/dev/null; ip link del ${N}a 2>/dev/null; rm -rf /etc/netns/$N /var/lib/dhcp/dhclient-$N.leases; }
trap cleanup EXIT
read -r ACTIVE HOST <<<"$($PSQL "SELECT https_active, coalesce(canonical_hostname,'-') FROM portal_tls_state" | tr '|' ' ')"
[ "$ACTIVE" = t ] || { echo "HTTPS is not active; nothing to test"; exit 2; }
GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
curl -sk $R -c $CK -o /dev/null -H 'Content-Type: application/json' -d '{"email":"admin","password":"admin"}' $B/auth/login

echo "== provider configuration (placeholder Microsoft app)"
SECRET="og-placeholder-$(head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' -d "{\"provider\":\"microsoft\",\"enabled\":true,\"display_name\":\"Microsoft (placeholder)\",
  \"client_id\":\"00000000-0000-4000-8000-000000000001\",\"client_secret\":\"$SECRET\",\"tenant\":\"common\",\"callback_hosts\":[\"$HOST\"]}" "$B/social-providers")
PID=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id") or (d.get("data") or {}).get("id",""))' 2>/dev/null)
[ -n "$PID" ] && ok "provider created through the Admin Console API" || { bad "create: $out"; exit 1; }
printf '%s' "$out" | grep -q "$SECRET" && bad "the API echoed the secret" || ok "the API never echoes the secret"
sleep 3
[ "$($PSQL "SELECT client_secret IS NULL FROM social_oauth_providers WHERE id='$PID'")" = t ] && ok "no plaintext secret in social_oauth_providers" || bad "plaintext secret stored"
G=$($PSQL "SELECT count(*)||'/'||bool_and(octet_length(ciphertext) > 0) FROM iam_v2.social_provider_secret_generations WHERE provider_id='$PID' AND superseded_at IS NULL")
[ "$G" = "1/true" ] && ok "one current sealed generation (AES-256-GCM)" || bad "sealed generations: $G"
$PSQL "SELECT encode(ciphertext,'escape') FROM iam_v2.social_provider_secret_generations WHERE provider_id='$PID'" | grep -q "$SECRET" \
  && bad "secret readable in the ciphertext" || ok "the secret does not appear in the ciphertext"

echo "== offering"
curl -sk $R -b $CK -o /dev/null -X PUT -H 'Content-Type: application/json' -d '{"social":{"microsoft":{"enabled":true}}}' "$B/auth-methods"
curl -s --unix-socket /run/stayconnect/scd.sock -X POST http://unix/v1/social/reload >/dev/null; sleep 2
ST=$(curl -s --unix-socket /run/stayconnect/scd.sock http://unix/v1/social/status)
printf '%s' "$ST" | python3 -c 'import sys,json;d=json.load(sys.stdin);p=[x for x in d["providers"] if x["provider"]=="microsoft"][0];sys.exit(0 if p["offered"] else 1)' \
  && ok "Microsoft offered (HTTPS active, $HOST registered)" || bad "not offered: $ST"

ip link add ${N}a type veth peer name ${N}c; ip link set ${N}a master "$GB"; ip link set ${N}a up
ip netns add $N; ip link set ${N}c netns $N; ip netns exec $N ip link set lo up; ip netns exec $N ip link set ${N}c up
mkdir -p /etc/netns/$N; echo "nameserver $GW" > /etc/netns/$N/resolv.conf
sleep 1; ip netns exec $N timeout 25 dhclient -1 -lf /var/lib/dhcp/dhclient-$N.leases -pf /run/dhclient-$N.pid ${N}c >/dev/null 2>&1
ip netns exec $N ping -c1 -W2 "$GW" >/dev/null 2>&1
# The page draws its buttons from /api/auth-methods (templates.go), so that is what the device is asked.
CFG=$(ip netns exec $N curl -s "${CAOPT[@]}" "https://$HOST/api/auth-methods")
printf '%s' "$CFG" | python3 -c 'import sys,json;d=json.load(sys.stdin);sys.exit(0 if (d.get("social") or {}).get("microsoft",{}).get("enabled") else 1)'   && ok "the portal offers the Microsoft button to the device" || bad "Microsoft not in the portal's sign-in methods: $CFG"
LOC=$(ip netns exec $N curl -s "${CAOPT[@]}" -o /dev/null -w '%{redirect_url}' "https://$HOST/auth/social/start?provider=microsoft")
echo "     authorize: ${LOC:0:110}..."
case "$LOC" in https://login.microsoftonline.com/common/oauth2/v2.0/authorize*) ok "redirects to Microsoft's authorize endpoint";; *) bad "start -> '$LOC'";; esac
python3 - "$LOC" "$HOST" <<'EOF' && ok "redirect_uri https://$HOST/auth/social/callback, PKCE S256, nonce, state" || bad "authorize parameters"
import sys, urllib.parse as u
q = dict(u.parse_qsl(u.urlparse(sys.argv[1]).query)); host = sys.argv[2]
assert q.get("redirect_uri") == f"https://{host}/auth/social/callback", q.get("redirect_uri")
assert q.get("code_challenge_method") == "S256" and len(q.get("code_challenge", "")) == 43
assert len(q.get("nonce", "")) >= 32 and len(q.get("state", "")) >= 32
EOF
STATE=$(python3 -c 'import sys,urllib.parse as u;print(dict(u.parse_qsl(u.urlparse(sys.argv[1]).query)).get("state",""))' "$LOC")
[ "$($PSQL "SELECT (code_verifier IS NOT NULL AND nonce IS NOT NULL)::text FROM social_oauth_states WHERE state='$STATE'")" = true ] \
  && ok "the attempt row holds the PKCE verifier and nonce server-side" || bad "attempt row"
CB=$(ip netns exec $N curl -s "${CAOPT[@]}" -o /dev/null -w '%{http_code}' "https://$HOST/auth/social/callback?provider=microsoft&state=forged&code=x")
[ "$CB" != 302 ] && ok "a callback with a forged state is refused ($CB)" || bad "forged callback accepted"
W=$(ip netns exec $N curl -s "${CAOPT[@]}" -A 'Mozilla/5.0 (Linux; Android 10; wv) AppleWebKit/537.36 Version/4.0 Chrome/120 Mobile Safari/537.36' -H 'Accept: text/html' -o /tmp/$N.wv -w '%{http_code}' "https://$HOST/auth/social/start?provider=google")
grep -q "https://$HOST/" /tmp/$N.wv && ok "Android WebView + Google: the open-in-browser page with the portal address ($W)" || bad "WebView page ($W)"
GARDEN=$(nft list set inet stayconnect walled_garden_tcp 2>/dev/null | grep -c ' . 443')
[ "${GARDEN:-0}" -gt 0 ] && ok "pre-sign-in walled garden carries provider pairs on 443 ($GARDEN)" || bad "no 443 garden pairs"

echo "== not offered when the canonical hostname is not registered"
curl -sk $R -b $CK -o /dev/null -X PATCH -H 'Content-Type: application/json' -d '{"callback_hosts":[]}' "$B/social-providers/$PID"
curl -s --unix-socket /run/stayconnect/scd.sock -X POST http://unix/v1/social/reload >/dev/null; sleep 2
C=$(ip netns exec $N curl -s "${CAOPT[@]}" -o /dev/null -w '%{http_code}' "https://$HOST/auth/social/start?provider=microsoft")
[ "$C" != 302 ] && ok "start refused when the callback host is not registered ($C)" || bad "started without a registered callback"
CFG=$(ip netns exec $N curl -s "${CAOPT[@]}" "https://$HOST/api/auth-methods")
printf '%s' "$CFG" | python3 -c 'import sys,json;d=json.load(sys.stdin);sys.exit(1 if (d.get("social") or {}).get("microsoft",{}).get("enabled") else 0)'   && ok "and the button is withdrawn from the portal" || bad "button still offered: $CFG"
echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" = 0 ]
