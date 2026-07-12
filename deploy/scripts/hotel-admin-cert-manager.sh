#!/usr/bin/env bash
# StayConnect — Hotel Admin dual-SAN TLS certificate lifecycle manager.
#
# Manages ONLY the local Hotel Admin HTTPS leaf certificate (hotel.stayconnect.local
# + the current Management/WAN IP), issued from Caddy's existing local CA. It does
# NOT touch the vendor appliance mTLS PKI, Root/Intermediate CA, API client / NATS
# certs, or assignment/license/command/update keys.
#
# Subcommands:
#   renew    (default) idempotent: renew only if due (<=45d), IP changed, or SAN drift
#   rotate   force a renewal now (same safe lifecycle), for the manual Hotel-IT action
#   check    diagnostic-only: validate the ACTIVE cert, write status, change nothing
#   status   print the status JSON
#
# Exit: 0 ok/no-op, 2 renewed, 10 validation/health failure (rolled back), 20 config error.
set -uo pipefail

# ---- configuration -----------------------------------------------------------
DNS_SAN="hotel.stayconnect.local"
CERT_DIR="/etc/caddy/hotel-admin"
# Status lives beside the cert material: the dir is root-owned 0755 (traversable),
# status.json is 0644 (readable by edged/scd for the UI + Central telemetry) while
# the private key stays 0600 caddy-only. /var/lib/stayconnect is 0700 and NOT
# readable by the unprivileged Hotel Admin app, so it cannot host this.
STATE_DIR="$CERT_DIR"
STATUS_JSON="$STATE_DIR/status.json"
CA_DIR="/var/lib/caddy/.local/share/caddy/pki/authorities/local"
NETD_ENV="/etc/stayconnect/netd.env"
CADDY_SVC="stayconnect-caddy"
RENEW_DAYS=45           # renew when remaining validity <= this
LEAF_DAYS=730           # minted leaf validity
MIN_FRESH_DAYS=180      # a freshly minted cert must have at least this much life
RETRY_BASE=300          # backoff base seconds
RETRY_MAX=86400         # backoff cap (24h)
APP_USER="stayconnect"  # Hotel Admin application user (must NOT own/write cert material)
CADDY_USER="caddy"      # TLS terminator (reads the key)
SITE_DB="stayconnect_site"
PGC="stayconnect-pg"

KEY="$CERT_DIR/hotel-admin.key"
CRT="$CERT_DIR/hotel-admin.crt"
CHAIN="$CERT_DIR/hotel-admin.fullchain.crt"
# The Hotel Admin Caddy vhost lives in the cert dir (scd-writable) and is imported
# by the main Caddyfile. The manager rewrites its site-address IP so Caddy ROUTES
# the current management IP (not just serves a cert with that SAN).
VHOST="$CERT_DIR/vhost.caddy"

umask 077
mkdir -p "$STATE_DIR"; chmod 755 "$STATE_DIR"

log(){ printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }
now(){ date -u +%s; }

# ---- audit (best-effort, into the site-local audit_log edged reads) ----------
audit(){ # action  json_payload
  local action="$1" payload="${2:-{\}}"
  docker exec -i "$PGC" psql -U stayconnect -d "$SITE_DB" -v ON_ERROR_STOP=0 >/dev/null 2>&1 <<SQL || true
INSERT INTO audit_log (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
SELECT (SELECT tenant_id FROM operators LIMIT 1), 'system', NULL, '${action}', 'hotel_admin_cert', NULL, '${payload}'::jsonb;
SQL
}

# ---- authoritative Management/WAN IP -----------------------------------------
# Interface comes from netd's config (NETD_MGMT_IFACE, else NETD_WAN_IFACE); the IP
# is read LIVE off that interface. Never hardcoded. Refuse if ambiguous/invalid.
resolve_mgmt_ip(){
  local iface ips ip
  # Test-only override (acceptance 20, reversible controlled test): pretend the
  # management IP is a given value. Never set in production/systemd.
  if [ -n "${HA_TEST_MGMT_IP:-}" ]; then printf '%s' "$HA_TEST_MGMT_IP"; return 0; fi
  [ -r "$NETD_ENV" ] || { log "ERR: $NETD_ENV unreadable"; return 20; }
  # shellcheck disable=SC1090
  iface="$(. "$NETD_ENV"; printf '%s' "${NETD_MGMT_IFACE:-${NETD_WAN_IFACE:-}}")"
  [ -n "$iface" ] || { log "ERR: no NETD_MGMT_IFACE/NETD_WAN_IFACE configured"; return 20; }
  # global-scope IPv4 only: excludes loopback, link-local (169.254), and by
  # reading only the mgmt iface, excludes br-lan/docker/guest addresses.
  ips="$(ip -j -4 addr show "$iface" 2>/dev/null | python3 -c '
import sys,json
try: a=json.load(sys.stdin)
except Exception: sys.exit(0)
for i in a:
  for x in i.get("addr_info",[]):
    if x.get("scope")=="global" and x.get("family")=="inet":
      print(x["local"])
')"
  local n; n="$(printf '%s\n' "$ips" | grep -c .)"
  [ "$n" = "1" ] || { log "ERR: ambiguous/absent mgmt IP on $iface (found: ${ips//$'\n'/ })"; return 20; }
  ip="$(printf '%s' "$ips" | tr -d '[:space:]')"
  # defensive: never a guest/docker/loopback/link-local address
  case "$ip" in
    10.10.*|127.*|169.254.*|172.17.*|172.18.*|172.19.*|172.2[0-9].0.1) : ;; # 172.20.x.1 docker-ish guarded below
  esac
  case "$ip" in
    10.10.0.*) log "ERR: refusing guest-LAN IP $ip"; return 20;;
    127.*|169.254.*) log "ERR: refusing loopback/link-local $ip"; return 20;;
    172.1[789].*) log "ERR: refusing docker IP $ip"; return 20;;
  esac
  printf '%s' "$ip"
}

# ---- cert introspection ------------------------------------------------------
cert_serial(){ openssl x509 -in "$1" -noout -serial 2>/dev/null | cut -d= -f2; }
cert_enddate_epoch(){ date -u -d "$(openssl x509 -in "$1" -noout -enddate 2>/dev/null | cut -d= -f2)" +%s 2>/dev/null; }
cert_sans(){ openssl x509 -in "$1" -noout -ext subjectAltName 2>/dev/null | tr ',' '\n' | sed -n 's/.*\(DNS:[^ ]*\|IP Address:[0-9.]*\).*/\1/p' | sed 's/IP Address:/IP:/' | tr -d ' ' | sort; }

want_sans(){ printf 'DNS:%s\nIP:%s\n' "$DNS_SAN" "$1" | sort; }

# ---- candidate validation (all checks; echoes reason on failure) -------------
validate_candidate(){ # keyfile crtfile chainfile ip
  local k="$1" c="$2" ch="$3" ip="$4"
  openssl x509 -in "$c" -noout >/dev/null 2>&1 || { echo "unparseable cert"; return 1; }
  # signed by the expected Caddy local CA
  openssl verify -CAfile "$CA_DIR/root.crt" -untrusted "$CA_DIR/intermediate.crt" "$c" >/dev/null 2>&1 \
    || { echo "not signed by Caddy local CA"; return 1; }
  openssl x509 -in "$c" -noout -issuer 2>/dev/null | grep -q "Caddy Local Authority" \
    || { echo "unexpected issuer"; return 1; }
  # private key matches certificate
  local kp cp
  kp="$(openssl pkey -in "$k" -pubout 2>/dev/null | openssl sha256 2>/dev/null)"
  cp="$(openssl x509 -in "$c" -noout -pubkey 2>/dev/null | openssl sha256 2>/dev/null)"
  [ -n "$kp" ] && [ "$kp" = "$cp" ] || { echo "key does not match cert"; return 1; }
  # SAN set must be EXACTLY {DNS, current mgmt IP} — no more, no less
  local have want
  have="$(cert_sans "$c")"; want="$(want_sans "$ip")"
  printf '%s' "$have" | grep -qx "DNS:$DNS_SAN" || { echo "missing DNS SAN"; return 1; }
  printf '%s' "$have" | grep -qx "IP:$ip"       || { echo "missing/incorrect IP SAN ($ip)"; return 1; }
  [ "$have" = "$want" ] || { echo "unexpected SAN set: $(echo $have)"; return 1; }
  # currently valid + within freshness policy
  openssl x509 -in "$c" -noout -checkend 0 >/dev/null 2>&1 || { echo "cert not currently valid"; return 1; }
  openssl x509 -in "$c" -noout -checkend $((MIN_FRESH_DAYS*86400)) >/dev/null 2>&1 \
    || { echo "cert expires too soon (<${MIN_FRESH_DAYS}d)"; return 1; }
  # serverAuth EKU
  openssl x509 -in "$c" -noout -ext extendedKeyUsage 2>/dev/null | grep -q "TLS Web Server Authentication" \
    || { echo "missing serverAuth EKU"; return 1; }
  # key type/size policy: EC prime256v1 (matches approved existing policy)
  openssl pkey -in "$k" -noout -text 2>/dev/null | grep -q "prime256v1" \
    || { echo "key not EC prime256v1"; return 1; }
  # chain file = leaf + intermediate
  [ "$(grep -c "BEGIN CERTIFICATE" "$ch")" -ge 2 ] || { echo "chain missing intermediate"; return 1; }
  echo ""
  return 0
}

# ---- mint into a protected staging dir ---------------------------------------
mint_candidate(){ # dir ip
  local d="$1" ip="$2"
  openssl ecparam -name prime256v1 -genkey -noout -out "$d/key" 2>/dev/null || return 1
  openssl req -new -key "$d/key" -subj "/CN=$DNS_SAN" -out "$d/csr" 2>/dev/null || return 1
  local san="DNS:$DNS_SAN,IP:$ip"
  # Test-only injection (acceptance 15): add an unexpected SAN so the candidate is
  # rejected by validate_candidate BEFORE activation. Never set in production.
  [ -n "${HA_TEST_EXTRA_SAN:-}" ] && san="$san,DNS:${HA_TEST_EXTRA_SAN}"
  cat > "$d/ext" <<EXT
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=critical,$san
EXT
  openssl x509 -req -in "$d/csr" -CA "$CA_DIR/intermediate.crt" -CAkey "$CA_DIR/intermediate.key" \
    -CAcreateserial -days "$LEAF_DAYS" -sha256 -extfile "$d/ext" -out "$d/crt" 2>/dev/null || return 1
  cat "$d/crt" "$CA_DIR/intermediate.crt" > "$d/chain"
  rm -f "$d/csr" "$d/ext"
}

apply_perms(){
  # directory root-owned, not writable by the Hotel Admin app user
  chown root:root "$CERT_DIR"; chmod 755 "$CERT_DIR"
  # key: 0600, owned by the TLS terminator (caddy) so Caddy can read it; the app
  # user (stayconnect) cannot. certs: 0644 (public material).
  chown "$CADDY_USER":"$CADDY_USER" "$KEY" 2>/dev/null || true; chmod 600 "$KEY"
  chown root:root "$CRT" "$CHAIN" 2>/dev/null || true; chmod 644 "$CRT" "$CHAIN"
  for f in "$KEY.prev" "$CRT.prev" "$CHAIN.prev"; do
    [ -e "$f" ] || continue
    case "$f" in *key.prev) chown "$CADDY_USER":"$CADDY_USER" "$f"; chmod 600 "$f";;
                 *) chown root:root "$f"; chmod 644 "$f";; esac
  done
}

# write_vhost renders the Hotel Admin Caddy vhost with the given management IP as a
# second site address (alongside the DNS name), referencing the stable cert paths.
write_vhost(){ # ip
	local ip="$1"
	cat > "$VHOST" <<VH
# Managed by stayconnect-hotel-admin-cert-manager — do not edit by hand.
# Site address tracks the current Management/WAN IP; imported by /etc/caddy/Caddyfile.
hotel.stayconnect.local, ${ip} {
	tls ${CHAIN} ${KEY}
	import security_headers
	header Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'"
	encode zstd gzip
	reverse_proxy 127.0.0.1:3100 {
		header_down Location "^https?://localhost:3100(/.*)\$" "\$1"
		header_up X-Forwarded-Host  {host}
		header_up X-Forwarded-Proto {scheme}
		header_up X-Real-IP         {remote_host}
	}
	log {
		output file /var/log/caddy/hotel-admin.log
		format json
	}
}
VH
	chown root:root "$VHOST"; chmod 644 "$VHOST"
}

caddy_validate_reload(){
  caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 || { echo "caddy validate failed"; return 1; }
  systemctl reload "$CADDY_SVC" >/dev/null 2>&1 || { echo "caddy reload failed"; return 1; }
  sleep 2; echo ""
}

# ---- live health checks through BOTH addresses -------------------------------
health_check(){ # ip expected_serial
  local ip="$1" want="$2" root="$CA_DIR/root.crt" fail=""
  # Test-only injection (acceptance 16): force a post-switch health failure so the
  # transaction rolls back to .prev. Never set in production.
  [ -n "${HA_TEST_FAIL_HEALTH:-}" ] && { echo " forced-test-health-failure"; return 0; }
  # DNS: serves expected serial + TLS validates hostname
  local ds is
  ds="$(echo | openssl s_client -connect 127.0.0.1:443 -servername "$DNS_SAN" 2>/dev/null | openssl x509 -noout -serial 2>/dev/null | cut -d= -f2)"
  is="$(echo | openssl s_client -connect "$ip:443" 2>/dev/null | openssl x509 -noout -serial 2>/dev/null | cut -d= -f2)"
  [ "$ds" = "$want" ] || fail="$fail dns-serial($ds!=$want)"
  [ "$is" = "$want" ] || fail="$fail ip-serial($is!=$want)"
  echo | openssl s_client -connect 127.0.0.1:443 -servername "$DNS_SAN" -verify_hostname "$DNS_SAN" -CAfile "$root" 2>/dev/null | grep -q "Verify return code: 0" || fail="$fail dns-verify"
  echo | openssl s_client -connect "$ip:443" -verify_ip "$ip" -CAfile "$root" 2>/dev/null | grep -q "Verify return code: 0" || fail="$fail ip-verify"
  # app reachable via both (static 200, login endpoint up = 401 for empty creds, redirect 307)
  for pair in "--resolve $DNS_SAN:443:127.0.0.1|https://$DNS_SAN" "|https://$ip"; do
    local rz="${pair%%|*}" b="${pair##*|}"
    [ "$(curl -s $rz --cacert "$root" -o /dev/null -w '%{http_code}' "$b/icon.svg")" = "200" ] || fail="$fail static($b)"
    [ "$(curl -s $rz --cacert "$root" -o /dev/null -w '%{http_code}' "$b/")" = "307" ] || fail="$fail redirect($b)"
    local lc; lc="$(curl -s $rz --cacert "$root" -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$b/api/edge/v1/auth/login")"
    [ "$lc" = "401" ] || [ "$lc" = "400" ] || fail="$fail loginproxy($b=$lc)"
  done
  echo "$fail"
}

# ---- status JSON (consumed by edged + scd telemetry) -------------------------
threshold_for(){ # days_remaining
  local d="$1"
  if   [ "$d" -lt 0 ];  then echo expired
  elif [ "$d" -le 7 ];  then echo emergency
  elif [ "$d" -le 14 ]; then echo critical
  elif [ "$d" -le 30 ]; then echo warning
  elif [ "$d" -le 45 ]; then echo renewal_due
  else echo healthy; fi
}

write_status(){ # result last_error mgmt_ip attempted(0/1)
  local result="$1" lasterr="$2" ip="$3" attempted="${4:-0}"
  local subj iss ser fp sans dns_sans ip_sans issued exp days thr sanmatch
  if [ -f "$CHAIN" ] && openssl x509 -in "$CHAIN" -noout >/dev/null 2>&1; then
    subj="$(openssl x509 -in "$CHAIN" -noout -subject 2>/dev/null | sed 's/^subject=//')"
    iss="$(openssl x509 -in "$CHAIN" -noout -issuer 2>/dev/null | sed 's/^issuer=//')"
    ser="$(cert_serial "$CHAIN")"
    fp="$(openssl x509 -in "$CHAIN" -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2)"
    dns_sans="$(cert_sans "$CHAIN" | grep '^DNS:' | sed 's/DNS://' | paste -sd, -)"
    ip_sans="$(cert_sans "$CHAIN" | grep '^IP:' | sed 's/IP://' | paste -sd, -)"
    issued="$(openssl x509 -in "$CHAIN" -noout -startdate 2>/dev/null | cut -d= -f2)"
    local ep; ep="$(cert_enddate_epoch "$CHAIN")"; exp="$(date -u -d "@$ep" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)"
    days=$(( (ep - $(now)) / 86400 ))
    thr="$(threshold_for "$days")"
    if [ "$(cert_sans "$CHAIN")" = "$(want_sans "$ip")" ]; then sanmatch=true; else sanmatch=false; fi
  else
    thr="expired"; days=-1; sanmatch=false
  fi
  local ts; ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local prev_attempt prev_success rc
  prev_success="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("last_successful_renewal",""))' "$STATUS_JSON" 2>/dev/null || echo '')"
  rc="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("retry_count",0))' "$STATUS_JSON" 2>/dev/null || echo 0)"
  [ "$attempted" = "1" ] && prev_attempt="$ts" || prev_attempt="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("last_renewal_attempt",""))' "$STATUS_JSON" 2>/dev/null || echo '')"
  [ "$result" = "renewal_succeeded" ] && prev_success="$ts"
  # Pass everything via env (no shell->python literal interpolation, quote-safe).
  HS_SUBJECT="${subj:-}" HS_ISSUER="${iss:-}" HS_SERIAL="${ser:-}" HS_FP="${fp:-}" \
  HS_DNS="${dns_sans:-}" HS_IP="${ip_sans:-}" HS_ISSUED="${issued:-}" HS_EXP="${exp:-}" \
  HS_DAYS="${days:-0}" HS_THR="${thr}" HS_MGMT="${ip:-}" HS_SANMATCH="${sanmatch:-false}" \
  HS_ATTEMPT="${prev_attempt:-}" HS_SUCCESS="${prev_success:-}" HS_RESULT="${result}" \
  HS_ERR="${lasterr:-}" HS_RC="${rc:-0}" HS_TS="${ts}" HS_STATUS="$STATUS_JSON" \
  python3 <<'PY'
import json,os
def s(k): return os.environ.get(k,"")
d=dict(
 subject=s("HS_SUBJECT"),issuer=s("HS_ISSUER"),serial=s("HS_SERIAL"),fingerprint_sha256=s("HS_FP"),
 dns_sans=[x for x in s("HS_DNS").split(",") if x],
 ip_sans=[x for x in s("HS_IP").split(",") if x],
 issued_at=s("HS_ISSUED"),expires_at=s("HS_EXP"),
 days_remaining=int(s("HS_DAYS") or 0),
 status_threshold=s("HS_THR"),current_management_ip=s("HS_MGMT"),
 san_config_match=(s("HS_SANMATCH")=="true"),
 last_renewal_attempt=s("HS_ATTEMPT"),last_successful_renewal=s("HS_SUCCESS"),
 last_renewal_result=s("HS_RESULT"),last_error=s("HS_ERR"),
 retry_count=int(s("HS_RC") or 0),updated_at=s("HS_TS"))
try:
 prev=json.load(open(s("HS_STATUS")))
 if "next_retry_epoch" in prev: d["next_retry_epoch"]=prev["next_retry_epoch"]
except Exception: pass
json.dump(d,open(s("HS_STATUS"),"w"),indent=2)
PY
  chmod 644 "$STATUS_JSON"
}

set_retry(){ # inc|reset
  local rc next
  rc="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("retry_count",0))' "$STATUS_JSON" 2>/dev/null || echo 0)"
  if [ "$1" = reset ]; then rc=0; next=0; else
    rc=$((rc+1)); local back=$((RETRY_BASE * (1<<(rc>10?10:rc)))); [ "$back" -gt "$RETRY_MAX" ] && back=$RETRY_MAX
    next=$(( $(now) + back ))
  fi
  python3 - "$STATUS_JSON" "$rc" "$next" <<'PY'
import json,sys
p,rc,nx=sys.argv[1],int(sys.argv[2]),int(sys.argv[3])
try: d=json.load(open(p))
except Exception: d={}
d["retry_count"]=rc; d["next_retry_epoch"]=nx
json.dump(d,open(p,"w"),indent=2)
PY
}

in_backoff(){
  local nx; nx="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("next_retry_epoch",0))' "$STATUS_JSON" 2>/dev/null || echo 0)"
  [ "$(now)" -lt "${nx:-0}" ]
}

# ---- decide whether renewal is needed ----------------------------------------
needs_renewal(){ # ip  -> 0 yes, 1 no; echoes reason
  local ip="$1"
  [ -f "$CHAIN" ] && [ -f "$KEY" ] || { echo "missing cert"; return 0; }
  openssl x509 -in "$CHAIN" -noout -checkend 0 >/dev/null 2>&1 || { echo "cert invalid/expired"; return 0; }
  openssl x509 -in "$CHAIN" -noout -checkend $((RENEW_DAYS*86400)) >/dev/null 2>&1 || { echo "within ${RENEW_DAYS}d of expiry"; return 0; }
  [ "$(cert_sans "$CHAIN")" = "$(want_sans "$ip")" ] || { echo "SAN drift (mgmt IP changed or SAN mismatch)"; return 0; }
  echo "valid and correct"; return 1
}

# ---- the safe renewal transaction --------------------------------------------
do_renew(){ # ip reason
  local ip="$1" reason="$2"
  audit renewal_started "{\"reason\":\"${reason//\"/}\",\"mgmt_ip\":\"$ip\"}"
  write_status renewal_started "" "$ip" 1
  local stg; stg="$(mktemp -d /run/stayconnect/ha-cert.XXXXXX)"; chmod 700 "$stg"
  trap 'rm -rf "${stg:-/nonexistent}" 2>/dev/null || true' RETURN
  if ! mint_candidate "$stg" "$ip"; then
    log "ERR: mint failed"; audit renewal_failed "{\"stage\":\"mint\"}"; write_status renewal_failed "mint failed" "$ip" 1; set_retry inc; return 10
  fi
  local vr; vr="$(validate_candidate "$stg/key" "$stg/crt" "$stg/chain" "$ip")"
  if [ -n "$vr" ]; then
    log "ERR: candidate invalid: $vr"; audit renewal_failed "{\"stage\":\"validate\",\"reason\":\"$vr\"}"
    write_status renewal_failed "candidate rejected: $vr" "$ip" 1; set_retry inc; return 10
  fi
  local newser; newser="$(cert_serial "$stg/crt")"
  # preserve current cert + vhost as .prev
  [ -f "$KEY" ]   && cp -a "$KEY"   "$KEY.prev"
  [ -f "$CRT" ]   && cp -a "$CRT"   "$CRT.prev"
  [ -f "$CHAIN" ] && cp -a "$CHAIN" "$CHAIN.prev"
  [ -f "$VHOST" ] && cp -a "$VHOST" "$VHOST.prev"
  # atomic replace (same filesystem: /etc)
  install -m600 "$stg/key"   "$KEY.new";   mv -f "$KEY.new"   "$KEY"
  install -m644 "$stg/crt"   "$CRT.new";   mv -f "$CRT.new"   "$CRT"
  install -m644 "$stg/chain" "$CHAIN.new"; mv -f "$CHAIN.new" "$CHAIN"
  apply_perms
  write_vhost "$ip"   # route the current management IP to the Hotel Admin backend
  local cr; cr="$(caddy_validate_reload)"
  if [ -n "$cr" ]; then
    log "ERR: $cr — rolling back"; rollback "$ip" "$cr"; return 10
  fi
  local hc; hc="$(health_check "$ip" "$newser")"
  if [ -n "$hc" ]; then
    log "ERR: health check failed:$hc — rolling back"; rollback "$ip" "health:$hc"; return 10
  fi
  # success
  audit renewal_succeeded "{\"serial\":\"$newser\",\"mgmt_ip\":\"$ip\"}"
  write_status renewal_succeeded "" "$ip" 1
  set_retry reset
  log "OK: renewed, serial $newser"
  return 2
}

rollback(){ # ip reason
  local ip="$1" reason="$2"
  if [ -f "$KEY.prev" ] && [ -f "$CHAIN.prev" ]; then
    cp -a "$KEY.prev" "$KEY"; cp -a "$CRT.prev" "$CRT"; cp -a "$CHAIN.prev" "$CHAIN"; apply_perms
    [ -f "$VHOST.prev" ] && cp -a "$VHOST.prev" "$VHOST"   # restore vhost routing too
    local cr; cr="$(caddy_validate_reload)"
    local ps; ps="$(echo | openssl s_client -connect 127.0.0.1:443 -servername "$DNS_SAN" 2>/dev/null | openssl x509 -noout -serial 2>/dev/null | cut -d= -f2)"
    local prevser; prevser="$(cert_serial "$CHAIN")"
    if [ -z "$cr" ] && [ "$ps" = "$prevser" ]; then
      audit rollback_succeeded "{\"reason\":\"${reason//\"/}\",\"restored_serial\":\"$prevser\"}"
      write_status renewal_failed "rolled back: $reason" "$ip" 1; set_retry inc
      log "OK: rolled back to previous serial $prevser"
    else
      audit rollback_failed "{\"reason\":\"${reason//\"/}\",\"caddy\":\"$cr\"}"
      write_status renewal_failed "ROLLBACK FAILED: $reason / $cr" "$ip" 1; set_retry inc
      log "FATAL: rollback failed ($cr)"
    fi
  else
    audit rollback_failed "{\"reason\":\"no .prev to restore\"}"
    write_status renewal_failed "rollback impossible (no .prev): $reason" "$ip" 1; set_retry inc
    log "FATAL: no .prev to roll back to"
  fi
}

# ---- entrypoints -------------------------------------------------------------
cmd_status(){ [ -f "$STATUS_JSON" ] && cat "$STATUS_JSON" || echo '{}'; }

cmd_check(){
  local ip; ip="$(resolve_mgmt_ip)" || { write_status check_failed "mgmt IP unresolved" "" 0; echo "mgmt IP unresolved" >&2; return 20; }
  local vr; vr="$(validate_candidate "$KEY" "$CRT" "$CHAIN" "$ip" 2>/dev/null)"
  write_status "$([ -z "$vr" ] && echo check_ok || echo check_failed)" "$vr" "$ip" 0
  if [ -n "$vr" ]; then echo "INVALID: $vr" >&2; return 10; fi
  echo "OK"; return 0
}

cmd_renew(){ # force(0/1)
  local force="${1:-0}" ip reason
  ip="$(resolve_mgmt_ip)" || { audit renewal_failed '{"stage":"resolve_ip"}'; write_status renewal_failed "mgmt IP unresolved/ambiguous" "" 1; return 20; }
  # detect management IP change vs last recorded
  local lastip; lastip="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("current_management_ip",""))' "$STATUS_JSON" 2>/dev/null || echo '')"
  if [ -n "$lastip" ] && [ "$lastip" != "$ip" ]; then
    audit management_ip_changed "{\"old\":\"$lastip\",\"new\":\"$ip\"}"; log "mgmt IP changed $lastip -> $ip"
  fi
  reason="$(needs_renewal "$ip")"; local need=$?
  if [ "$force" = "1" ]; then reason="manual rotate"; need=0; fi
  if [ "$need" != "0" ]; then
    write_status no_op "" "$ip" 0
    log "no-op: $reason (serial $(cert_serial "$CHAIN"))"; return 0
  fi
  # SAN drift → note the specific event
  [ "$(cert_sans "$CHAIN" 2>/dev/null)" != "$(want_sans "$ip")" ] && [ -f "$CHAIN" ] && audit certificate_san_changed "{\"mgmt_ip\":\"$ip\"}"
  if [ "$force" != "1" ] && in_backoff; then
    log "in retry backoff; existing cert still valid — skipping mint"; write_status backoff "" "$ip" 0; return 0
  fi
  do_renew "$ip" "$reason"; return $?
}

case "${1:-renew}" in
  renew)  cmd_renew 0 ;;
  rotate) cmd_renew 1 ;;
  check)  cmd_check ;;
  status) cmd_status ;;
  *) echo "usage: $0 {renew|rotate|check|status}" >&2; exit 20 ;;
esac
