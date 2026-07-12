#!/usr/bin/env bash
# Phase 1 bootstrap — run on the appliance.
# Creates dev tenant/site/appliance, seeds a test voucher, writes env files,
# mints a self-signed cert, builds & installs the binaries.
set -euo pipefail

BASE=/opt/stayconnect
BIN="$BASE/bin"
CFG=/etc/stayconnect
TLS="$CFG/tls"
LOG=/var/log/stayconnect
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
pgq() { $PSQL | head -n1; }

mkdir -p "$BIN" "$CFG" "$TLS" "$LOG"
id stayconnect >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin stayconnect
chown -R stayconnect:stayconnect "$LOG"
chown -R root:stayconnect "$TLS"
chmod 750 "$TLS"

# ---- Seed dev entities ------------------------------------------------------
TENANT_ID=$(echo "
INSERT INTO tenants (slug, name, contact_email)
VALUES ('dev', 'Dev Tenant', 'ops@example.com')
ON CONFLICT (slug) DO UPDATE SET name=EXCLUDED.name
RETURNING id;" | pgq)

SITE_ID=$(echo "
INSERT INTO sites (tenant_id, code, name)
VALUES ('$TENANT_ID', 'hq', 'Headquarters')
ON CONFLICT (tenant_id, code) DO UPDATE SET name=EXCLUDED.name
RETURNING id;" | pgq)

APPLIANCE_ID=$(echo "
INSERT INTO appliances (tenant_id, site_id, serial, name, status, enrolled_at, last_seen_at)
VALUES ('$TENANT_ID', '$SITE_ID', 'APP-DEV-0001', 'dev-appliance', 'online', now(), now())
ON CONFLICT (serial) DO UPDATE SET status='online', last_seen_at=now()
RETURNING id;" | pgq)

TEMPLATE_ID=$(echo "
INSERT INTO ticket_templates (tenant_id, code, name, duration_seconds, data_cap_bytes,
                              down_kbps, up_kbps, max_concurrent_devices)
VALUES ('$TENANT_ID', 'hour1', '1 Hour Pass', 3600, NULL, 10000, 10000, 1)
ON CONFLICT (tenant_id, code) DO UPDATE SET name=EXCLUDED.name
RETURNING id;" | pgq)

echo "
INSERT INTO vouchers (tenant_id, template_id, code, state)
VALUES ('$TENANT_ID', '$TEMPLATE_ID', 'TESTCODE', 'unused')
ON CONFLICT (tenant_id, code) DO UPDATE
  SET state='unused', bytes_used=0, seconds_used=0, activated_at=NULL, expires_at=NULL;
" | $PSQL

echo "
WITH plan AS (SELECT id FROM plans WHERE code='enterprise-yearly')
INSERT INTO tenant_subscriptions
  (tenant_id, plan_id, status, billing_cycle,
   current_period_start, current_period_end, trial_end)
SELECT '$TENANT_ID', plan.id, 'trialing', 'yearly',
       now(), now() + interval '1 year', now() + interval '30 days'
FROM plan
WHERE NOT EXISTS (
  SELECT 1 FROM tenant_subscriptions
   WHERE tenant_id = '$TENANT_ID'
     AND status IN ('trialing','active','past_due','paused')
);
" | $PSQL

echo "TENANT_ID=$TENANT_ID"
echo "SITE_ID=$SITE_ID"
echo "APPLIANCE_ID=$APPLIANCE_ID"

# ---- Env files --------------------------------------------------------------
# A factory appliance ships with NO customer identity. tenant_id / site_id /
# appliance_id are deliberately ABSENT below — they are not configuration:
#   * appliance_id comes from ENROLLMENT (identity.json), and
#   * tenant_id/site_id come ONLY from the vendor-signed ASSIGNMENT document that
#     Central issues on assign, persisted to /etc/stayconnect/assignment/.
# Hard-wiring them here would pin the box to one customer forever and make it
# uninstallable at a new hotel without manual surgery.
cat > "$CFG/scd.env" <<EOF
SCD_DB_URL=postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable
SCD_SOCKET=/run/stayconnect/scd.sock
SCD_MAIL_LOG=/var/log/stayconnect/otp-mail.log
SCD_SMS_LOG=/var/log/stayconnect/otp-sms.log
SCD_PMS_STUB_SEED=true
EOF
chmod 640 "$CFG/scd.env"

cat > "$CFG/portald.env" <<EOF
PORTALD_HTTP_ADDR=:8380
PORTALD_HTTPS_ADDR=:8343
PORTALD_SCD_SOCKET=/run/stayconnect/scd.sock
PORTALD_CERT=$TLS/portal.crt
PORTALD_KEY=$TLS/portal.key
PORTALD_FQDN=portal.stayconnect.local
EOF
chmod 640 "$CFG/portald.env"
chown root:stayconnect "$CFG/portald.env"

# Stamp the dev tenant with voucher + email + SMS + social.google enabled,
# all pointed at the hour1 template. UI-driven config will replace this later.
echo "
UPDATE tenants
   SET auth_methods = jsonb_build_object(
       'voucher', jsonb_build_object('enabled', true,  'template_id', null),
       'email',   jsonb_build_object('enabled', true,  'template_id', '$TEMPLATE_ID'),
       'sms',     jsonb_build_object('enabled', true,  'template_id', '$TEMPLATE_ID'),
       'social',  jsonb_build_object(
           'google', jsonb_build_object('enabled', true, 'template_id', '$TEMPLATE_ID')
       ),
       'pms',     jsonb_build_object(
           'enabled',     true,
           'template_id', '$TEMPLATE_ID',
           'provider',    'stub-dev',
           'mode',        'either'
       )
   )
 WHERE id = '$TENANT_ID';
" | $PSQL >/dev/null

# Phase 4.5.5a: insert a pms_providers row for the dev tenant pointed at the
# Stub provider. Production tenants would set kind='protel-fias' (or similar)
# and host/port pointing at their PMS.
echo "
INSERT INTO pms_providers (tenant_id, name, kind, display_name, enabled)
VALUES ('$TENANT_ID', 'stub-dev', 'stub', 'Stub PMS (dev)', true)
-- pms_providers has a *partial* unique index on (tenant_id, name) WHERE
-- site_id IS NULL (see migration 0014). ON CONFLICT must name that
-- predicate explicitly for PostgreSQL to pick the right index.
ON CONFLICT (tenant_id, name) WHERE site_id IS NULL DO UPDATE
  SET enabled      = true,
      display_name = EXCLUDED.display_name,
      updated_at   = now();
" | $PSQL >/dev/null

# Ensure OTP-stub log files exist with right perms.
mkdir -p /var/log/stayconnect
for f in otp-mail.log otp-sms.log; do
    touch "/var/log/stayconnect/$f"
    chown root:root "/var/log/stayconnect/$f"
    chmod 640 "/var/log/stayconnect/$f"
done

cat > "$CFG/acctd.env" <<EOF
ACCTD_DB_URL=postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable
ACCTD_SCD_SOCKET=/run/stayconnect/scd.sock
ACCTD_TICK_SECONDS=1
ACCTD_TENANT_ID=$TENANT_ID
ACCTD_APPLIANCE_ID=$APPLIANCE_ID
EOF
chmod 640 "$CFG/acctd.env"

# ---- Self-signed TLS cert for the portal -----------------------------------
if [ ! -f "$TLS/portal.crt" ]; then
  openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
    -keyout "$TLS/portal.key" -out "$TLS/portal.crt" \
    -subj "/CN=portal.stayconnect.local/O=StayConnect/OU=Dev" \
    -addext "subjectAltName=DNS:portal.stayconnect.local,DNS:captive.stayconnect.local,IP:10.10.0.1" \
    >/dev/null 2>&1
fi
chown root:stayconnect "$TLS/portal.key" "$TLS/portal.crt"
chmod 640 "$TLS/portal.key"
chmod 644 "$TLS/portal.crt"

# ---- scd needs to talk to nft, and portald needs to read the socket --------
mkdir -p /run/stayconnect
chgrp stayconnect /run/stayconnect
chmod 750 /run/stayconnect

echo "Phase 1 bootstrap complete."
