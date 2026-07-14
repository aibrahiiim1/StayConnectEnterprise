#!/usr/bin/env bash
# Phase 13 — observability stack E2E.
#
# Starts Prometheus + Grafana via docker compose, waits for scrape, and
# asserts:
#   1. scd's TCP metrics listener responds
#   2. Prometheus has both targets UP
#   3. Alert rule groups loaded (no syntax errors)
#   4. Key metric samples landed in Prometheus (not just on /metrics)
#   5. Grafana is alive and has the 4 provisioned dashboards
set -euo pipefail

OBS_DIR=/opt/stayconnect/deploy/observability
PROM_URL=${PROM_URL:-http://127.0.0.1:9090}
GRAFANA_URL=${GRAFANA_URL:-http://127.0.0.1:3001}
GRAFANA_USER=${GRAFANA_USER:-admin}
GRAFANA_PASS=${GRAFANA_PASS:-admin}

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

# ---- 0. scd must have its TCP metrics listener up ----
scd_metrics_addr=$(grep '^SCD_METRICS_ADDR=' /etc/stayconnect/scd.env | cut -d= -f2 || true)
[[ -n "$scd_metrics_addr" ]] || fail "SCD_METRICS_ADDR not set in /etc/stayconnect/scd.env" \
    "add: SCD_METRICS_ADDR=127.0.0.1:9101 and restart stayconnect-scd"
got=$(curl -s -o /dev/null -w '%{http_code}' "http://$scd_metrics_addr/metrics")
[[ "$got" == "200" ]] && pass "scd TCP /metrics up on $scd_metrics_addr" \
                      || fail "scd TCP /metrics not reachable" "code=$got"

# ---- 1. Start the observability stack ----
docker compose -f "$OBS_DIR/docker-compose.yml" up -d >/dev/null 2>&1
pass "prometheus + grafana containers up"

# Poll until Prometheus is ready (up to 30s).
ready=no
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '%{http_code}' "$PROM_URL/-/ready" | grep -q 200; then
        ready=yes; break
    fi
    sleep 1
done
[[ "$ready" == "yes" ]] && pass "prometheus responds to /-/ready" \
                        || fail "prometheus not ready within 30s"

# ---- 2. Both scrape targets should be UP after ~2 scrape intervals ----
# Prometheus needs a couple scrape cycles to mark targets up.
sleep 15
targets_json=$(curl -s "$PROM_URL/api/v1/targets")
down=$(echo "$targets_json" | jq -r '.data.activeTargets[] | select(.health != "up") | .scrapeUrl' | sort -u)
if [[ -n "$down" ]]; then
    fail "some scrape targets are not up" "$down"
fi
up_count=$(echo "$targets_json" | jq '[.data.activeTargets[] | select(.health == "up")] | length')
[[ "$up_count" -ge 2 ]] && pass "prometheus has $up_count scrape targets UP" \
                        || fail "fewer than 2 targets up" "count=$up_count"

# ---- 3. Alert rule groups load without parse errors ----
rules_json=$(curl -s "$PROM_URL/api/v1/rules")
parse_errs=$(echo "$rules_json" | jq -r '.data.groups[] | select(.rules[]?.health == "err") | .name' | head -n1 || true)
[[ -z "$parse_errs" ]] && pass "no rule-eval errors" \
                      || fail "rule group has eval errors" "$parse_errs"
# Confirm expected alert names exist.
for alert in ScrapeTargetDown ApplianceOffline PMSProviderDown \
             StripeSignatureFailures CtrlapiHigh5xxRate; do
    hit=$(echo "$rules_json" | jq -r --arg n "$alert" '.data.groups[].rules[] | select(.name == $n) | .name' | head -n1)
    [[ "$hit" == "$alert" ]] || fail "alert rule missing" "expected=$alert"
done
pass "all expected alert rules loaded"

# ---- 4. Key metrics have landed in Prometheus storage ----
for expr in \
    'up{job="ctrlapi"}' \
    'up{job="scd"}' \
    'ctrlapi_build_info' \
    'scd_build_info' \
    'ctrlapi_heartbeats_received_total' \
    'scd_sessions_started_total'; do
    q=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$expr")
    result=$(curl -s "$PROM_URL/api/v1/query?query=$q" | jq -r '.data.result | length')
    [[ "$result" -ge 1 ]] && pass "prometheus has samples for: $expr" \
                          || fail "no samples for expr" "$expr"
done

# ---- 5. Grafana alive + dashboards provisioned ----
g_ready=no
for i in $(seq 1 60); do
    if curl -s -o /dev/null -w '%{http_code}' "$GRAFANA_URL/api/health" | grep -q 200; then
        g_ready=yes; break
    fi
    sleep 1
done
[[ "$g_ready" == "yes" ]] && pass "grafana responds to /api/health" \
                          || fail "grafana not ready within 60s"

for uid in stayconnect-overview stayconnect-payments stayconnect-auth stayconnect-system; do
    code=$(curl -s -o /dev/null -w '%{http_code}' -u "$GRAFANA_USER:$GRAFANA_PASS" \
        "$GRAFANA_URL/api/dashboards/uid/$uid")
    [[ "$code" == "200" ]] && pass "dashboard provisioned: $uid" \
                           || fail "dashboard not found: $uid" "code=$code"
done

# Datasource provisioned.
ds=$(curl -s -u "$GRAFANA_USER:$GRAFANA_PASS" "$GRAFANA_URL/api/datasources")
name=$(echo "$ds" | jq -r '.[0].name')
[[ "$name" == "Prometheus" ]] && pass "Prometheus datasource provisioned" \
                              || fail "datasource missing/misnamed" "got=$name"

echo
echo "ALL GREEN"
