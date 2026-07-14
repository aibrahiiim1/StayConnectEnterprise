#!/usr/bin/env bash
# Phase 3 bootstrap — write ctrlapi env, build & install the systemd unit,
# seed a platform_admin operator for UI login.
set -euo pipefail

CFG=/etc/stayconnect
mkdir -p "$CFG" /opt/stayconnect/bin

ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-adminadmin01}

cat > "$CFG/ctrlapi.env" <<EOF
CTRLAPI_ADDR=:8080
CTRLAPI_DB_URL=postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable
CTRLAPI_REDIS_URL=redis://127.0.0.1:6379/0
CTRLAPI_NATS_URL=nats://127.0.0.1:4222
CTRLAPI_LOG_LEVEL=info
CTRLAPI_ENV=dev
CTRLAPI_COOKIE_SECURE=false
CTRLAPI_ALLOW_ORIGINS=http://localhost:3000,http://127.0.0.1:3000,http://172.21.60.23:3000
EOF
chmod 640 "$CFG/ctrlapi.env"

# Seed the super admin (idempotent — password is re-set every run).
set -a; . "$CFG/ctrlapi.env"; set +a
/opt/stayconnect/bin/ctrlapi seed-admin --email "$ADMIN_EMAIL" --password "$ADMIN_PASSWORD" --name "Platform Admin"

# Seed the dev tenant's stub OIDC provider (Phase 4.4) so the SSO flow works
# out-of-the-box. groups_to_role maps "sc-admins" → tenant_admin so the auto-
# provisioned operator gets a meaningful role.
docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
WITH dev AS (SELECT id FROM tenants WHERE slug = 'dev')
INSERT INTO idp_providers (tenant_id, name, display_name, kind, claims_map)
SELECT dev.id, 'stub', 'Stub IdP (dev)', 'stub',
       '{"default_role":"tenant_operator","groups_to_role":{"sc-admins":"tenant_admin","sc-billing":"billing"}}'::jsonb
  FROM dev
ON CONFLICT (tenant_id, name) DO UPDATE
  SET display_name = EXCLUDED.display_name,
      claims_map   = EXCLUDED.claims_map,
      enabled      = true,
      updated_at   = now();
SQL

echo "Phase 3 bootstrap complete."
echo "  login: $ADMIN_EMAIL / $ADMIN_PASSWORD"
echo "  SSO:   stub provider enabled for tenant 'dev'"
