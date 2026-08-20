#!/usr/bin/env bash
# PROVISION A FRESH PRODUCTION APPLIANCE, END TO END.
#
# Everything this script does was learned by doing it by hand on a real Production bring-up. Each step that
# looks obvious is here because its absence produced a specific, confusing failure in the Hotel Admin UI:
#
#   "Could not load guest accounts / the appliance did not answer"  -> tenant-scoped screens before assignment
#   "branding load failed"                                          -> same cause
#   "kea dial: ... no such file or directory"                       -> Kea not installed, LAN not configured
#   silent audit_log write failures                                 -> hypertables not registered by the baseline
#   Caddy crash-loop on start                                       -> /var/log/caddy ownership
#   scd crash-loop on first boot                                    -> voucher DEK never bootstrapped
#
# It is idempotent: safe to re-run on a partially provisioned box.
#
# It does NOT invent identity or topology. It never sets a tenant or site, never writes a netplan file, and
# never starts Kea, because guest topology comes from the operator through Hotel Admin. It stops at the
# furthest state a factory-clean appliance can legitimately reach on its own.
#
# Usage (as root, on the appliance, with the repo's deploy/ tree and binaries already present):
#   provision-fresh-appliance.sh
set -euo pipefail

SC=/opt/stayconnect
DEPLOY="${SC}/deploy"
ETC=/etc/stayconnect
say() { echo "[provision] $*"; }
die() { echo "[provision] ABORT: $*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"
[ -d "$DEPLOY" ] || die "missing $DEPLOY"

# ---------------------------------------------------------------- 1. packages
say "packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl gnupg lsb-release nftables unbound jq python3 >/dev/null
if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin >/dev/null
  systemctl enable --now docker
fi
if ! command -v caddy >/dev/null; then
  curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy >/dev/null
  systemctl disable --now caddy >/dev/null 2>&1 || true   # the stock unit; we ship stayconnect-caddy
fi
# KEA IS INSTALLED BUT NOT STARTED. It binds the LAN bridge, which does not exist until guest networking is
# configured, so starting it here would only produce a crash-loop. Installing it means the DHCP screen can
# work the moment the operator configures the LAN, instead of failing with a missing-socket error.
command -v kea-dhcp4 >/dev/null || apt-get install -y -qq kea-dhcp4-server >/dev/null
systemctl disable --now kea-dhcp4-server >/dev/null 2>&1 || true
command -v node >/dev/null || { curl -fsSL https://deb.nodesource.com/setup_20.x | bash - >/dev/null; apt-get install -y -qq nodejs >/dev/null; }
say "packages ok"

# ---------------------------------------------------------------- 2. layout
say "layout"
mkdir -p "$SC"/{bin,releases} "$ETC"/{identity,license,assignment,certs,secrets} /var/log/stayconnect /run/kea /var/lib/kea
chmod 700 "$ETC/secrets"
id stayconnect >/dev/null 2>&1 || useradd --system --home "$SC" --shell /usr/sbin/nologin stayconnect
chown -R stayconnect:stayconnect /var/log/stayconnect
# CADDY'S LOG DIRECTORY *AND ITS FILES*. Caddy runs unprivileged; a root-owned log file inside a
# caddy-owned directory still fails to open, and Caddy then refuses the whole config on start AND on reload.
mkdir -p /var/log/caddy && chown -R caddy:caddy /var/log/caddy && chmod 755 /var/log/caddy
say "layout ok"

# ---------------------------------------------------------------- 3. infra containers
say "infra containers"
( cd "$DEPLOY/compose" && docker compose -f infra.yml up -d )
for _ in $(seq 1 60); do
  docker exec stayconnect-pg pg_isready -U stayconnect >/dev/null 2>&1 && break
  sleep 2
done
docker exec stayconnect-pg pg_isready -U stayconnect >/dev/null 2>&1 || die "postgres never became ready"
say "infra ok"

# ---------------------------------------------------------------- 4. database, from the FACTORY-CLEAN BASELINE
say "database"
PGX="docker exec -i stayconnect-pg psql -v ON_ERROR_STOP=1 -U stayconnect"
BASELINE="$SC/data-plane/migrations/baseline/0000_production_baseline.sql"
[ -f "$BASELINE" ] || die "missing $BASELINE (a new appliance is built from the baseline, never from 0001..0049)"
$PGX -d stayconnect -tAc "SELECT 1 FROM pg_database WHERE datname='stayconnect_site'" | grep -q 1 \
  || $PGX -d stayconnect -c "CREATE DATABASE stayconnect_site" >/dev/null
SITE="$PGX -d stayconnect_site"
docker cp "$DEPLOY/gatep" stayconnect-pg:/tmp/gatep >/dev/null
$SITE -f /tmp/gatep/gatep-roles.sql >/dev/null
# The IAM roles run BEFORE the baseline (its privileges name them) and again AFTER (its guarded REFERENCES
# grant needs public.guest_networks, which the baseline creates). The file is idempotent and order-tolerant.
$SITE -f /tmp/gatep/gatep-iam-roles.sql >/dev/null
docker cp "$BASELINE" stayconnect-pg:/tmp/baseline.sql >/dev/null
$SITE -f /tmp/baseline.sql >/dev/null
$SITE -f /tmp/gatep/gatep-iam-roles.sql >/dev/null
$SITE -f /tmp/gatep/gatep-iam-ownership.sql >/dev/null
$SITE -f /tmp/gatep/gatep-grants.sql >/dev/null
# HYPERTABLES MUST BE REGISTERED, NOT MERELY SHAPED. A schema-only dump carries the table and its
# ts_insert_blocker trigger but not the TimescaleDB registration, and the result refuses every INSERT into
# audit_log and accounting_records while looking perfect in the catalog. The baseline now re-registers them;
# this asserts it, because the failure is invisible until something tries to write.
hyper="$($SITE -tAc "SELECT count(*) FROM timescaledb_information.hypertables")"
[ "${hyper:-0}" -ge 2 ] || die "only ${hyper:-0} hypertables registered — audit_log/accounting_records would reject every write"
say "database ok (factory-clean baseline; $hyper hypertables)"

# ---------------------------------------------------------------- 5. credentials + local key material
say "credentials"
if [ ! -s "$ETC/.dsn" ]; then
  bash "$DEPLOY/gatep/gatep-set-passwords.sh" --pg-exec "docker exec -i stayconnect-pg" \
       --db stayconnect_site --dsn-out "$ETC/.dsn" >/dev/null
fi
# keybootstrap creates the durable-throttle key, the OTP generation-1 key AND the voucher code-encryption
# key. The last one is not optional on a production build: IAM-v2 VOUCHER cannot be configured off there, so
# scd refuses to start without it and crash-loops on first boot.
KEYBOOTSTRAP_DSN="postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect_site?sslmode=disable" \
  "$SC/bin/keybootstrap" >/dev/null
for k in throttle.key voucher_dek.key otp_hmac_1.key; do
  [ -s "$ETC/secrets/$k" ] || die "keybootstrap did not produce $k"
done
say "credentials ok"

# ---------------------------------------------------------------- 6. service environment (NO IDENTITY)
say "environment"
# NO TENANT OR SITE ID IS EVER WRITTEN HERE. Identity arrives only through enrollment -> claim -> signed
# assignment. Seeding it would be indistinguishable from cloning another appliance.
. "$ETC/.dsn"
WAN_IFACE="${WAN_IFACE:-$(ip -o -4 route show default | awk '{print $5}' | head -1)}"
LAN_IFACE="${LAN_IFACE:-$(ls /sys/class/net | grep -vE '^(lo|docker|br-|veth)' | grep -v "^${WAN_IFACE}$" | head -1)}"
[ -n "$WAN_IFACE" ] || die "cannot determine the WAN interface"
umask 027
[ -f "$ETC/scd.env" ] || cat > "$ETC/scd.env" <<EOF
SCD_DB_URL=${SCD_DB_URL}
SCD_SOCKET=/run/stayconnect/scd.sock
SCD_CTRLAPI_BASE=${CENTRAL_BASE:-https://150.0.0.252}
SCD_MTLS_BASE=${CENTRAL_MTLS_BASE:-https://150.0.0.252:8443}
SCD_AUTO_REGISTER=true
SCD_NATS_URL=nats://127.0.0.1:4222
SCD_METRICS_ADDR=127.0.0.1:9101
STAYCONNECT_IAMV2_MASTER=true
EOF
[ -f "$ETC/edged.env" ] || cat > "$ETC/edged.env" <<EOF
EDGED_DB_URL=${EDGED_DB_URL}
EDGED_SCD_SOCKET=/run/stayconnect/scd.sock
EDGED_COOKIE_SECURE=true
STAYCONNECT_IAMV2_MASTER=true
EOF
[ -f "$ETC/acctd.env" ] || printf 'ACCTD_DB_URL=%s\nACCTD_SCD_SOCKET=/run/stayconnect/scd.sock\n' "$ACCTD_DB_URL" > "$ETC/acctd.env"
# netd needs the interface names to exist before the Hotel Admin certificate can be minted for the
# management IP: the cert manager reads NETD_MGMT_IFACE/NETD_WAN_IFACE and refuses without them.
[ -f "$ETC/netd.env" ] || cat > "$ETC/netd.env" <<EOF
NETD_DB_URL=${NETD_DB_URL}
NETD_WAN_IFACE=${WAN_IFACE}
NETD_MGMT_IFACE=${WAN_IFACE}
NETD_LAN_PHYS=${LAN_IFACE}
EOF
[ -f "$ETC/portald.env" ] || printf 'PORTALD_SCD_SOCKET=/run/stayconnect/scd.sock\n' > "$ETC/portald.env"
[ -f "$ETC/pmsd.env" ] || cp "$DEPLOY/env/pmsd.env.dark" "$ETC/pmsd.env"
chmod 640 "$ETC"/*.env && chown root:stayconnect "$ETC"/*.env
grep -qE '^(SCD|EDGED|ACCTD)_(TENANT|SITE)_ID=' "$ETC"/*.env && die "an identity was seeded into the environment"
say "environment ok (WAN=$WAN_IFACE, LAN=$LAN_IFACE, no identity pinned)"

# ---------------------------------------------------------------- 7. units, proxy, certificate
say "units + proxy"
cp "$DEPLOY/tmpfiles"/*.conf /etc/tmpfiles.d/ 2>/dev/null || true
cp "$DEPLOY/sysctl"/*.conf /etc/sysctl.d/ 2>/dev/null || true
systemd-tmpfiles --create >/dev/null 2>&1 || true
sysctl --system >/dev/null 2>&1 || true
bash "$DEPLOY/scripts/install-service-units.sh" "$DEPLOY"
install -m 0644 "$DEPLOY/caddy/Caddyfile.edge" /etc/caddy/Caddyfile
install -m 0644 "$DEPLOY/caddy/stayconnect-caddy.edge.service" /etc/systemd/system/stayconnect-caddy.service
mkdir -p /etc/caddy/hotel-admin
# The Caddyfile imports a vhost the cert manager generates, and the cert manager needs Caddy's local CA,
# which only exists after Caddy has run once. An empty import breaks that cycle.
[ -f /etc/caddy/hotel-admin/vhost.caddy ] || : > /etc/caddy/hotel-admin/vhost.caddy
systemctl daemon-reload
systemctl enable --now stayconnect-caddy >/dev/null
for _ in $(seq 1 30); do [ -f /var/lib/caddy/.local/share/caddy/pki/authorities/local/intermediate.crt ] && break; sleep 2; done
bash "$DEPLOY/scripts/hotel-admin-cert-manager.sh" rotate >/dev/null 2>&1 || true
chown -R caddy:caddy /var/log/caddy   # the cert-manager health check can create a root-owned log file
systemctl reload stayconnect-caddy || systemctl restart stayconnect-caddy
say "units + proxy ok"

# ---------------------------------------------------------------- 8. services
say "services"
for u in stayconnect-scd stayconnect-edged stayconnect-acctd stayconnect-netd stayconnect-portald stayconnect-hotel-admin; do
  systemctl enable "$u" >/dev/null 2>&1 || true
done
systemctl restart stayconnect-scd
sleep 10
systemctl restart stayconnect-edged stayconnect-acctd stayconnect-netd stayconnect-portald || true
sleep 5
fail=0
for u in stayconnect-scd stayconnect-edged stayconnect-acctd stayconnect-netd stayconnect-portald stayconnect-caddy; do
  st="$(systemctl is-active "$u" || true)"
  printf '  %-32s %s\n' "$u" "$st"
  [ "$st" = active ] || fail=1
done
[ "$fail" = 0 ] || die "one or more services are not active"
# The production build must say so out loud; a development binary here is the one thing that silently
# reintroduces a configurable guest-IAM authority.
journalctl -u stayconnect-scd -n 200 --no-pager | grep -q 'build=production' \
  || die "scd is not a production build — rebuild with -tags stayconnect_production"
say "services ok (scd reports build=production)"

echo
say "PROVISIONED. Awaiting, in order:"
say "  1. enrollment (bootstrap token from Central) -> claim -> signed assignment"
say "  2. signed licence"
say "  3. LAN/guest networking through Hotel Admin (Kea starts once the LAN bridge exists)"
say "Hotel Admin: https://$(ip -o -4 addr show "$WAN_IFACE" | awk '{print $4}' | cut -d/ -f1)/"
