#!/usr/bin/env bash
# PORTAL TLS INTEGRATION TEST — Central's whole certificate chain against a real ACME server, with no DNS
# credential and nothing on the internet: Pebble (Let's Encrypt's test CA) validates DNS-01 through
# pebble-challtestsrv, which ctrlapi drives exactly as it would drive Cloudflare; a throwaway TimescaleDB
# carries every Central migration.
#
# It proves, end to end: hostname allocation and its A record; an order for an appliance CSR becoming an
# ISSUED chain for that CSR's key; a renewal (same hostname: not "new", the previous one SUPERSEDED); the
# weekly cap holding a new order back; operator revocation (the CA confirms); a hotel domain verified
# through two DNS-over-HTTPS resolvers and issued through its delegated challenge name; and the lifecycle
# release revoking and un-publishing a retired appliance.
#
#   scripts/portaltls-pebble-test.sh            run (containers are removed afterwards)
#   KEEP=1 scripts/portaltls-pebble-test.sh     leave the containers running for a look
#
# Needs Docker and Go. Works from Git Bash on Windows (MSYS path conversion is switched off where Docker
# needs a container path).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
P=ptls-$$
NET="$P-net"
PG="$P-pg"
CTS="$P-cts"
PEB="$P-pebble"
PGPORT="${PGPORT:-25432}"
CTS_MGMT="${CTS_MGMT:-18055}"
CTS_DNS="${CTS_DNS:-18053}"
PEB_PORT="${PEB_PORT:-24000}"
PEB_MGMT="${PEB_MGMT:-25000}"
WORK="$(mktemp -d)"
# The host path as Docker and Go see it (C:/... under Git Bash; unchanged elsewhere).
HWORK="$(cygpath -m "$WORK" 2>/dev/null || echo "$WORK")"

say() { echo "[portaltls-pebble] $*"; }

cleanup() {
  if [ "${KEEP:-0}" = 1 ]; then
    say "KEEP=1: containers $PG $CTS $PEB left running (network $NET)"
    return
  fi
  docker rm -f "$PG" "$CTS" "$PEB" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

docker network create "$NET" >/dev/null

say "starting TimescaleDB"
docker run -d --name "$PG" --network "$NET" -p "127.0.0.1:$PGPORT:5432" \
  -e POSTGRES_USER=stayconnect -e POSTGRES_PASSWORD=ptls -e POSTGRES_DB=stayconnect \
  timescale/timescaledb:2.16.1-pg16 >/dev/null

say "starting pebble-challtestsrv (DNS for the CA, driven through its management API)"
# Every name answers A 10.255.255.1 unless told otherwise; no HTTP/TLS challenge servers are needed.
docker run -d --name "$CTS" --network "$NET" \
  -p "127.0.0.1:$CTS_MGMT:8055" -p "127.0.0.1:$CTS_DNS:8053/udp" -p "127.0.0.1:$CTS_DNS:8053/tcp" \
  ghcr.io/letsencrypt/pebble-challtestsrv:latest \
  -defaultIPv4 10.255.255.1 -defaultIPv6 "" -dnsserver :8053 -management :8055 \
  -http01 "" -https01 "" -tlsalpn01 "" -doh "" >/dev/null

say "starting Pebble"
docker run -d --name "$PEB" --network "$NET" -p "127.0.0.1:$PEB_PORT:14000" -p "127.0.0.1:$PEB_MGMT:15000" \
  -e PEBBLE_VA_NOSLEEP=1 -e PEBBLE_WFE_NONCEREJECT=0 \
  ghcr.io/letsencrypt/pebble:latest -config test/config/pebble-config.json -dnsserver "$CTS:8053" >/dev/null
MSYS_NO_PATHCONV=1 docker cp "$PEB:/test/certs/pebble.minica.pem" "$HWORK/pebble.minica.pem"

for i in $(seq 1 60); do
  docker exec "$PG" pg_isready -U stayconnect -d stayconnect >/dev/null 2>&1 && \
    docker exec "$PG" psql -U stayconnect -d stayconnect -tAc 'select 1' >/dev/null 2>&1 && break
  sleep 1
done

say "applying every Central migration"
PGUSER=stayconnect "$ROOT/deploy/scripts/central-migrate.sh" --pg-exec "docker exec -i $PG" --db stayconnect up >"$WORK/migrate.log" 2>&1 || {
  tail -20 "$WORK/migrate.log"; exit 1; }
tail -2 "$WORK/migrate.log"

# Wait for Pebble's directory to answer.
for i in $(seq 1 30); do
  curl -fsS --cacert "$WORK/pebble.minica.pem" "https://localhost:$PEB_PORT/dir" >/dev/null 2>&1 && break
  sleep 1
done

say "running the Go integration test"
cd "$ROOT/control-plane"
set +e
PORTALTLS_PEBBLE=1 \
PORTALTLS_PG_URL="postgres://stayconnect:ptls@127.0.0.1:$PGPORT/stayconnect?sslmode=disable" \
PORTALTLS_ACME_DIR="https://localhost:$PEB_PORT/dir" \
PORTALTLS_ACME_CA="$HWORK/pebble.minica.pem" \
PORTALTLS_PEBBLE_MGMT="https://localhost:$PEB_MGMT" \
PORTALTLS_CTS_URL="http://127.0.0.1:$CTS_MGMT" \
PORTALTLS_DNS="127.0.0.1:$CTS_DNS" \
  go test -count=1 -v -run 'TestPebble' ./internal/portaltls/ 2>&1 | tee "$WORK/test.log"
status=${PIPESTATUS[0]}
[ "$status" = 0 ] && say "PASS" || say "FAIL"
exit "$status"
