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
CTRLAPI_LOG_LEVEL=info
CTRLAPI_ENV=dev
CTRLAPI_COOKIE_SECURE=false
CTRLAPI_ALLOW_ORIGINS=http://localhost:3000,http://127.0.0.1:3000
EOF
chmod 640 "$CFG/ctrlapi.env"

# Seed the super admin (idempotent — password is re-set every run).
set -a; . "$CFG/ctrlapi.env"; set +a
/opt/stayconnect/bin/ctrlapi seed-admin --email "$ADMIN_EMAIL" --password "$ADMIN_PASSWORD" --name "Platform Admin"

echo "Phase 3 bootstrap complete."
echo "  login: $ADMIN_EMAIL / $ADMIN_PASSWORD"
