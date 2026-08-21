#!/usr/bin/env bash
# ISSUE CENTRAL'S PUBLIC TLS CERTIFICATE, COVERING EVERY NAME APPLIANCES ACTUALLY DIAL.
#
# This is the certificate Caddy presents on 443 -- the one an appliance validates when it registers, fetches
# a licence or reconciles. It is NOT the mTLS certificate (ctrlapi manages that one itself, on 9443).
#
# WHY THIS SCRIPT EXISTS
#
# The live certificate was minted by hand with openssl and a san.cnf, once, in July. Nothing recorded which
# names it covered or how to re-make it, so moving Central to a new name meant an undocumented sequence of
# openssl commands on one server -- and getting it wrong is not visible until an appliance somewhere refuses
# to connect. The name set now comes from the same versioned endpoint configuration everything else uses.
#
# THE FAILURE IT PREVENTS
#
# A certificate that does not carry the name an appliance was told to dial produces a TLS verification
# failure, and the appliance's own registration path swallows that into "awaiting enrollment" -- which reads
# as "this appliance has not been set up" and sends the operator to the setup wizard instead of the real
# problem. So the certificate must cover the endpoint BEFORE any appliance is pointed at it.
#
# Idempotent: re-running when the certificate already covers every configured name does nothing.
#
# Usage (as root on Central):
#   central-mint-tls.sh              issue if the current certificate is missing a name (or is absent)
#   central-mint-tls.sh --show       print the current certificate's names and expiry
#   central-mint-tls.sh --force      re-issue regardless
#
# Extra names (transition, split-horizon, legacy):
#   CENTRAL_TLS_SANS="150.0.0.252,admin.stayconnect.local,api.stayconnect.local"
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${DEPLOY:-$(cd "$HERE/.." && pwd)}"
TLSDIR="${CENTRAL_TLS_DIR:-/opt/stayconnect/central/tls}"
CADDY_CRT="${CADDY_TLS_CRT:-/etc/caddy/tls/server.crt}"
CADDY_KEY="${CADDY_TLS_KEY:-/etc/caddy/tls/server.key}"
DAYS="${CENTRAL_TLS_DAYS:-825}"

say() { echo "[central-tls] $*"; }
die() { echo "[central-tls] ABORT: $*" >&2; exit 1; }

names_of() { openssl x509 -in "$1" -noout -text 2>/dev/null | grep -A1 "Subject Alternative Name" | tail -1 | tr -d ' '; }

if [ "${1:-}" = "--show" ]; then
  [ -s "$CADDY_CRT" ] || die "no certificate at $CADDY_CRT"
  say "certificate: $CADDY_CRT"
  say "subject: $(openssl x509 -in "$CADDY_CRT" -noout -subject | sed 's/^subject=//')"
  say "issuer:  $(openssl x509 -in "$CADDY_CRT" -noout -issuer  | sed 's/^issuer=//')"
  say "expires: $(openssl x509 -in "$CADDY_CRT" -noout -enddate | sed 's/^notAfter=//')"
  say "names:   $(names_of "$CADDY_CRT")"
  exit 0
fi

FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1

[ "$(id -u)" = 0 ] || die "run as root"
[ -s "$TLSDIR/ca.crt" ] || die "no Central CA certificate at $TLSDIR/ca.crt"
[ -s "$TLSDIR/ca.key" ] || die "no Central CA key at $TLSDIR/ca.key"

# shellcheck source=lib-central-endpoint.sh
. "$HERE/lib-central-endpoint.sh"
sc_load_central_endpoint "$DEPLOY"
sc_validate_central_base "$CENTRAL_BASE"

primary="${CTRLAPI_APPLIANCE_BASE:-$CENTRAL_BASE}"
primary="${primary#https://}"; primary="${primary%%/*}"; primary="${primary%%:*}"

# Build the SAN list: the appliance-facing name first, then anything still in use during a transition.
dns_names=("$primary")
ip_names=()
IFS=',' read -r -a extras <<< "${CENTRAL_TLS_SANS:-}"
for e in "${extras[@]:-}"; do
  e="$(printf '%s' "$e" | tr -d '[:space:]')"
  [ -n "$e" ] || continue
  case "$e" in
    [0-9]*.[0-9]*.[0-9]*.[0-9]*) ip_names+=("$e") ;;
    *) dns_names+=("$e") ;;
  esac
done
ip_names+=("127.0.0.1")

# Already covering everything? Then this is a no-op, and saying so beats re-issuing a certificate that every
# appliance would have to re-validate for no reason.
if [ "$FORCE" = 0 ] && [ -s "$CADDY_CRT" ]; then
  missing=0
  cur_names="$(names_of "$CADDY_CRT")"
  for n in "${dns_names[@]}" "${ip_names[@]}"; do
    printf '%s' "$cur_names" | grep -E "(DNS|IPAddress):${n}(,|$)" >/dev/null 2>&1 || { say "missing name: $n"; missing=1; }
  done
  if [ "$missing" = 0 ]; then
    say "current certificate already covers every configured name — nothing to do"
    say "names: $(names_of "$CADDY_CRT")"
    exit 0
  fi
fi

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
{
  echo "[req]"
  echo "distinguished_name = dn"
  echo "req_extensions = v3"
  echo "prompt = no"
  echo "[dn]"
  echo "O = StayConnect"
  echo "CN = $primary"
  echo "[v3]"
  echo "basicConstraints = CA:FALSE"
  echo "keyUsage = digitalSignature, keyEncipherment"
  echo "extendedKeyUsage = serverAuth"
  echo "subjectAltName = @alt"
  echo "[alt]"
  i=1; for n in "${dns_names[@]}"; do echo "DNS.$i = $n"; i=$((i+1)); done
  i=1; for n in "${ip_names[@]}";  do echo "IP.$i = $n";  i=$((i+1)); done
} > "$TMP/san.cnf"

say "issuing for: DNS=${dns_names[*]}  IP=${ip_names[*]}"
openssl req -new -newkey rsa:2048 -nodes -keyout "$TMP/server.key" -out "$TMP/server.csr" \
  -config "$TMP/san.cnf" 2>/dev/null
openssl x509 -req -in "$TMP/server.csr" -CA "$TLSDIR/ca.crt" -CAkey "$TLSDIR/ca.key" -CAcreateserial \
  -out "$TMP/server.crt" -days "$DAYS" -sha256 -extfile "$TMP/san.cnf" -extensions v3 2>/dev/null

# VERIFY BEFORE SWITCHING. A certificate that does not chain, or that is missing a name, must never reach
# the live path -- the old one is strictly better than a broken new one.
openssl verify -CAfile "$TLSDIR/ca.crt" "$TMP/server.crt" >/dev/null || die "issued certificate does not chain to the Central CA"
new_names="$(names_of "$TMP/server.crt")"
for n in "${dns_names[@]}" "${ip_names[@]}"; do
  printf '%s' "$new_names" | grep -E "(DNS|IPAddress):${n}(,|$)" >/dev/null 2>&1 || die "issued certificate is missing $n"
done
say "verified: chains to the Central CA and carries every configured name"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
for f in "$CADDY_CRT" "$CADDY_KEY" "$TLSDIR/server.crt" "$TLSDIR/server.key"; do
  [ -f "$f" ] && cp -a "$f" "$f.prev-$STAMP"
done
install -m 0644 "$TMP/server.crt" "$TLSDIR/server.crt"
install -m 0600 "$TMP/server.key" "$TLSDIR/server.key"
install -m 0644 "$TMP/san.cnf"    "$TLSDIR/san.cnf"
mkdir -p "$(dirname "$CADDY_CRT")"
install -m 0644 "$TMP/server.crt" "$CADDY_CRT"
install -m 0640 "$TMP/server.key" "$CADDY_KEY"
chgrp caddy "$CADDY_KEY" 2>/dev/null || true

say "installed. names: $(names_of "$CADDY_CRT")"
say "expires: $(openssl x509 -in "$CADDY_CRT" -noout -enddate | sed 's/^notAfter=//')"
say ""
say "Apply it:  systemctl restart stayconnect-caddy"
say "Appliances must trust $TLSDIR/ca.crt — install it with deploy/scripts/install-central-trust.sh"
