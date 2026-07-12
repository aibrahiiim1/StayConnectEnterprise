#!/usr/bin/env bash
# Mint / renew the Hotel Admin TLS certificate.
#
# One leaf cert with BOTH a DNS SAN (hotel.stayconnect.local) and an IP SAN
# (the appliance management IP), signed by Caddy's existing local CA so the
# chain a workstation already trusts is preserved. Serves the canonical URL and
# the management-IP alias from the same Caddy vhost.
#
# Validity is 2 years (Caddy does NOT auto-manage this explicit cert). Re-run
# before expiry, then: systemctl reload stayconnect-caddy
#
# Env overrides: HA_DNS, HA_IP, CADDY_CA_DIR, HA_DIR
set -euo pipefail

HA_DNS="${HA_DNS:-hotel.stayconnect.local}"
HA_IP="${HA_IP:-172.21.60.23}"
CA="${CADDY_CA_DIR:-/var/lib/caddy/.local/share/caddy/pki/authorities/local}"
D="${HA_DIR:-/etc/caddy/hotel-admin}"
DAYS="${HA_DAYS:-730}"

[ -f "$CA/intermediate.crt" ] && [ -f "$CA/intermediate.key" ] || {
  echo "Caddy local CA not found at $CA" >&2; exit 1; }
mkdir -p "$D"

openssl ecparam -name prime256v1 -genkey -noout -out "$D/hotel-admin.key"
openssl req -new -key "$D/hotel-admin.key" -subj "/CN=$HA_DNS" -out /tmp/ha.csr

cat > /tmp/ha.ext <<EXT
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=critical,DNS:$HA_DNS,IP:$HA_IP
EXT

openssl x509 -req -in /tmp/ha.csr -CA "$CA/intermediate.crt" -CAkey "$CA/intermediate.key" \
  -CAcreateserial -days "$DAYS" -sha256 -extfile /tmp/ha.ext -out "$D/hotel-admin.crt"

# fullchain = leaf + intermediate so clients build leaf -> intermediate -> trusted root
cat "$D/hotel-admin.crt" "$CA/intermediate.crt" > "$D/hotel-admin.fullchain.crt"

chown -R caddy:caddy "$D"
chmod 700 "$D"; chmod 600 "$D/hotel-admin.key"; chmod 644 "$D/hotel-admin.crt" "$D/hotel-admin.fullchain.crt"
rm -f /tmp/ha.csr /tmp/ha.ext

echo "minted $HA_DNS + IP:$HA_IP -> $D/hotel-admin.fullchain.crt (valid ${DAYS}d)"
echo "now run: systemctl reload stayconnect-caddy"
