#!/usr/bin/env bash
# Phase 18 — multi-site isolation (edge-first refactor).
#
# Proves the core isolation guarantees with a second, fully separate site
# database (site B):
#   - the same voucher code string can exist in both sites without conflict;
#   - a voucher from site B can NEVER authenticate on site A's appliance;
#   - site B's PMS/operator rows are invisible to site A;
#   - cloud-side tenant scoping: tenant B's operator cannot read tenant A's
#     licenses through the Cloud API.
set -euo pipefail

API=http://127.0.0.1:8080
SCD_SOCK=/run/stayconnect/scd.sock
PSQLC="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
PSQLA="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site -At -q -v ON_ERROR_STOP=1"
PSQLB="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site_b -At -q -v ON_ERROR_STOP=1"
CJ=$(mktemp); CJB=$(mktemp)
trap 'rm -f $CJ $CJB' EXIT
PASS=0; FAIL=0
ok()  { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad() { echo "  ✗ $1"; FAIL=$((FAIL+1)); }

echo "== 18.1 create site-B database (isolated schema instance) =="
if ! docker exec stayconnect-pg psql -U stayconnect -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname='stayconnect_site_b'" | grep -q 1; then
  docker exec stayconnect-pg psql -U stayconnect -d postgres -q -c "CREATE DATABASE stayconnect_site_b"
fi
docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site_b -v ON_ERROR_STOP=1 -q \
  < /opt/stayconnect/data-plane/migrations/0001_edge_init.up.sql 2>/dev/null || true
T=$(echo "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE';" | $PSQLB)
[ "$T" -ge 25 ] && ok "site-B schema present ($T tables)" || bad "site-B schema incomplete ($T tables)"

echo "== 18.2 seed site B (its own tenant/site/plan/voucher) =="
TENB=$(echo "SELECT id FROM tenants LIMIT 1;" | $PSQLB)
if [ -z "$TENB" ]; then
  TENB=$(echo "INSERT INTO tenants (slug, name, status) VALUES ('hotel-b', 'Hotel B', 'active') RETURNING id;" | $PSQLB)
fi
TPLB=$(echo "SELECT id FROM ticket_templates LIMIT 1;" | $PSQLB)
if [ -z "$TPLB" ]; then
  TPLB=$(echo "INSERT INTO ticket_templates (tenant_id, code, name, duration_seconds) VALUES ('$TENB','B-DAY','Hotel B Day Pass',86400) RETURNING id;" | $PSQLB)
fi
# The SAME code string that exists (or will exist) in site A:
SHARED_CODE="SHAREDVXYZ23"
echo "INSERT INTO vouchers (tenant_id, template_id, code, state) VALUES ('$TENB','$TPLB','$SHARED_CODE','unused') ON CONFLICT DO NOTHING;" | $PSQLB
ok "site B seeded (tenant=$(echo $TENB | cut -c1-8)…, voucher $SHARED_CODE)"

echo "== 18.3 same code string in site A — no cross-DB conflict =="
TENA=$(echo "SELECT id FROM tenants LIMIT 1;" | $PSQLA)
TPLA=$(echo "SELECT id FROM ticket_templates WHERE is_active LIMIT 1;" | $PSQLA)
echo "INSERT INTO vouchers (tenant_id, template_id, code, state) VALUES ('$TENA','$TPLA','$SHARED_CODE','unused') ON CONFLICT DO NOTHING;" | $PSQLA
A=$(echo "SELECT count(*) FROM vouchers WHERE code='$SHARED_CODE';" | $PSQLA)
B=$(echo "SELECT count(*) FROM vouchers WHERE code='$SHARED_CODE';" | $PSQLB)
[ "$A" = "1" ] && [ "$B" = "1" ] && ok "code '$SHARED_CODE' exists independently in BOTH sites" || bad "shared code counts A=$A B=$B"

echo "== 18.4 site-B-only voucher cannot authenticate on site A =="
BONLY="HABVXYZ99WK"
echo "INSERT INTO vouchers (tenant_id, template_id, code, state) VALUES ('$TENB','$TPLB','$BONLY','unused') ON CONFLICT DO NOTHING;" | $PSQLB
resp=$(curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/authorize \
  -H 'Content-Type: application/json' \
  -d "{\"ip\":\"10.10.0.197\",\"mac\":\"02:18:00:00:00:97\",\"voucher\":\"$BONLY\"}" -o /tmp/iso.json -w '%{http_code}')
[ "$resp" = "404" ] && ok "site-B voucher rejected on site A (404 not found)" || bad "site-B voucher on site A: HTTP $resp $(cat /tmp/iso.json)"

echo "== 18.5 site A's copy of the shared code DOES work on site A =="
resp=$(curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/authorize \
  -H 'Content-Type: application/json' \
  -d "{\"ip\":\"10.10.0.197\",\"mac\":\"02:18:00:00:00:97\",\"voucher\":\"$SHARED_CODE\"}" -o /tmp/iso2.json -w '%{http_code}')
[ "$resp" = "200" ] && ok "site A's own copy authenticates" || bad "site A shared code: HTTP $resp"
curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/revoke -H 'Content-Type: application/json' -d '{"ip":"10.10.0.197","reason":"admin"}' -o /dev/null || true
BSTATE=$(echo "SELECT state FROM vouchers WHERE code='$SHARED_CODE';" | $PSQLB)
[ "$BSTATE" = "unused" ] && ok "site B's copy untouched by site A redemption" || bad "site B copy state=$BSTATE (want unused)"

echo "== 18.6 PMS config isolation =="
echo "INSERT INTO pms_providers (tenant_id, name, kind, enabled, host, port) VALUES ('$TENB','hotel-b-pms','stub',true,'',0) ON CONFLICT DO NOTHING;" | $PSQLB
INA=$(echo "SELECT count(*) FROM pms_providers WHERE name='hotel-b-pms';" | $PSQLA)
[ "$INA" = "0" ] && ok "site B PMS provider invisible in site A" || bad "site B PMS provider leaked into site A"

echo "== 18.7 cloud tenant scoping: tenant B operator cannot read tenant A licenses =="
TENA_CLOUD=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQLC)
TENB_CLOUD=$(echo "SELECT id FROM tenants WHERE slug='acme';" | $PSQLC)
curl -s -c "$CJ" -X POST $API/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@stayconnect.local","password":"adminadmin01"}' -o /dev/null
# ensure an acme-scoped operator exists
curl -s -b "$CJ" -X POST "$API/v1/operators?tenant_id=$TENB_CLOUD" -H 'Content-Type: application/json' \
  -d '{"email":"iso-acme@test.local","display_name":"Iso Acme","password":"isolationpass1","role":"tenant_admin"}' -o /dev/null || true
curl -s -c "$CJB" -X POST $API/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"iso-acme@test.local","password":"isolationpass1"}' -o /dev/null
# acme operator asks for dev tenant's licenses: tenant_id override must be ignored
N=$(curl -s -b "$CJB" "$API/cloud/v1/licenses?tenant_id=$TENA_CLOUD" | python3 -c '
import sys, json
d = json.load(sys.stdin)
rows = d.get("data") or []
print(sum(1 for r in rows if r.get("tenant_id") == "'"$TENA_CLOUD"'"))' 2>/dev/null || echo "parse-fail")
[ "$N" = "0" ] && ok "tenant B operator sees zero tenant-A licenses" || bad "tenant A licenses visible to tenant B: $N"

echo
if [ $FAIL -eq 0 ]; then echo "ALL GREEN ($PASS checks)"; else echo "$FAIL FAILED / $PASS passed"; exit 1; fi
