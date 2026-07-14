#!/usr/bin/env bash
# Phase 15 — Alertmanager routing + silence E2E.
#
# Asserts:
#   1. Both configs (prod + dev) pass `amtool check-config`
#   2. Alertmanager starts with the dev config + config is loaded
#   3. Prometheus is forwarding to it (AM shows as active discovered)
#   4. Firing a critical alert via API reaches the `webhook-critical`
#      sink within group_wait (~5s)
#   5. Firing a warning alert → only the fallback receives it
#   6. Inhibit rule: firing critical + warning on same (alertname, tenant_id)
#      → warning doesn't reach fallback
#   7. A silence matching alertname suppresses future dispatch
#
# Uses a tiny Python webhook sink on :9099 that buckets POSTs by path
# (/critical, /fallback). No dependencies beyond python3's http.server.
set -euo pipefail

OBS_DIR=/opt/stayconnect/deploy/observability
AM_URL=${AM_URL:-http://127.0.0.1:9093}
PROM_URL=${PROM_URL:-http://127.0.0.1:9090}
SINK_PORT=9099
SINK_DIR=/tmp/stayconnect-am-sink
mkdir -p "$SINK_DIR" && rm -f "$SINK_DIR"/*.log

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

# ---- 0. start the webhook sink in the background ----
cat > "$SINK_DIR/sink.py" <<'PY'
import http.server, sys, json, os
LOG_DIR = "/tmp/stayconnect-am-sink"
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n).decode("utf-8", errors="replace")
        target = self.path.strip("/").replace("/", "_") or "root"
        with open(f"{LOG_DIR}/{target}.log", "a") as f:
            f.write(body + "\n")
        self.send_response(200); self.end_headers()
    def log_message(self, *a, **k): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
pkill -f "stayconnect-am-sink/sink.py" 2>/dev/null || true
python3 "$SINK_DIR/sink.py" "$SINK_PORT" >/dev/null 2>&1 &
SINK_PID=$!
cleanup() {
    kill $SINK_PID 2>/dev/null || true
    rm -rf "$SINK_DIR"
    ALERTMANAGER_CONFIG=alertmanager.yml docker compose \
        -f "$OBS_DIR/docker-compose.yml" up -d stayconnect-alertmanager >/dev/null 2>&1 || true
}
trap cleanup EXIT
sleep 0.5
# Sink smoke check.
curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${SINK_PORT}/ping" | grep -q 200 \
    && pass "webhook sink up on :${SINK_PORT}" \
    || fail "sink not accepting posts"

# ---- 1. both configs pass amtool check-config ----
for f in alertmanager.yml alertmanager-dev.yml; do
    # `--` is not needed; amtool just takes the file path.
    docker run --rm -v "$OBS_DIR/alertmanager:/etc/alertmanager:ro" \
        --entrypoint amtool prom/alertmanager:v0.27.0 \
        check-config "/etc/alertmanager/$f" >/tmp/am-check.out 2>&1 \
        && pass "$f passes amtool check-config" \
        || fail "amtool check-config failed for $f" "$(cat /tmp/am-check.out)"
done

# ---- 2. boot alertmanager with the dev config ----
cd "$OBS_DIR"
ALERTMANAGER_CONFIG=alertmanager-dev.yml docker compose \
    -f "$OBS_DIR/docker-compose.yml" up -d stayconnect-alertmanager >/dev/null 2>&1
cd - >/dev/null

ready=no
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '%{http_code}' "$AM_URL/-/ready" | grep -q 200; then
        ready=yes; break
    fi
    sleep 1
done
[[ "$ready" == "yes" ]] && pass "alertmanager /-/ready" || fail "AM not ready in 30s"

# Config file name visible via status endpoint — confirm we loaded the dev one.
status=$(curl -s "$AM_URL/api/v2/status")
# Look for a dev-specific marker. The dev config uses group_wait:5s.
echo "$status" | jq -e '.config.original' | grep -q "group_wait: 5s" \
    && pass "alertmanager loaded the dev config" \
    || fail "dev config not loaded" "$(echo "$status" | jq '.config.original' | head -c 300)"

# ---- 3. Prometheus sees the AM target ----
# Reload Prom so it picks up the alerting: section if it wasn't reloaded
# since the config change. Discovery runs on the evaluation interval;
# give it a few cycles before asserting.
curl -s -X POST "$PROM_URL/-/reload" >/dev/null || true
am_active=0
for i in $(seq 1 30); do
    am_active=$(curl -s "$PROM_URL/api/v1/alertmanagers" | jq -r '.data.activeAlertmanagers | length')
    [[ "$am_active" -ge 1 ]] && break
    sleep 1
done
[[ "$am_active" -ge 1 ]] && pass "prometheus forwarding to $am_active alertmanager(s)" \
                        || fail "prometheus has no active alertmanager after 30s"

# ---- 4. Fire a critical alert via Alertmanager's own /api/v2/alerts ----
# Alertmanager accepts alert creation directly (same path as Prometheus
# uses). starts_at in the past; ends_at in the future so it stays firing.
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
FUTURE=$(date -u -d '+10 minutes' +%Y-%m-%dT%H:%M:%SZ)
post_alert() {
    local alertname="$1" severity="$2" tenant="$3"
    curl -s -o /dev/null -w '%{http_code}' -X POST "$AM_URL/api/v2/alerts" \
        -H 'Content-Type: application/json' \
        --data "[{\"labels\":{\"alertname\":\"$alertname\",\"severity\":\"$severity\",\"tenant_id\":\"$tenant\",\"service\":\"e2e\"},\"annotations\":{\"summary\":\"e2e test\"},\"startsAt\":\"$NOW\",\"endsAt\":\"$FUTURE\"}]"
}

rm -f "$SINK_DIR"/critical.log "$SINK_DIR"/fallback.log
code=$(post_alert "CritAlert1" "critical" "tenant-1")
[[ "$code" == "200" ]] || fail "POST alert rejected" "code=$code"

# Dev config has group_wait=2s for critical; give it 10s.
landed=no
for i in $(seq 1 15); do
    if [[ -s "$SINK_DIR/critical.log" ]]; then landed=yes; break; fi
    sleep 1
done
[[ "$landed" == "yes" ]] && pass "critical alert reached webhook-critical" \
                         || fail "webhook-critical never received the alert" \
                              "state: $(ls -la $SINK_DIR/)"

# The critical route has `continue: true` so fallback ALSO gets it.
for i in $(seq 1 10); do
    if [[ -s "$SINK_DIR/fallback.log" ]]; then break; fi
    sleep 1
done
[[ -s "$SINK_DIR/fallback.log" ]] && pass "critical alert also reached webhook-fallback (continue)" \
                                  || fail "fallback didn't receive critical"

# ---- 5. Warning alert → only fallback, not critical ----
rm -f "$SINK_DIR"/critical.log "$SINK_DIR"/fallback.log
post_alert "WarnAlert1" "warning" "tenant-2" >/dev/null
for i in $(seq 1 15); do
    if [[ -s "$SINK_DIR/fallback.log" ]]; then break; fi
    sleep 1
done
[[ -s "$SINK_DIR/fallback.log" ]] && pass "warning reached fallback" || fail "warning missed fallback"
[[ ! -s "$SINK_DIR/critical.log" ]] && pass "warning did NOT reach webhook-critical" \
                                    || fail "warning leaked to critical receiver"

# ---- 6. Inhibit rule ----
# Fire a critical AND a warning with the same (alertname, tenant_id).
# The warning should be inhibited → only critical reaches fallback.
rm -f "$SINK_DIR"/critical.log "$SINK_DIR"/fallback.log
post_alert "InhibTest" "critical" "tenant-3" >/dev/null
post_alert "InhibTest" "warning"  "tenant-3" >/dev/null
sleep 12
# critical.log should exist (the critical fired).
# fallback.log: alertmanager groups critical + warning-would-have → but
# the warning is inhibited, so fallback receives only the critical copy.
# Easiest assertion: the fallback log body mentions "severity":"critical"
# but not any line where severity is "warning" against the same tenant.
# `grep -c` prints "0" to stdout AND exits 1 on no-match. `|| echo 0`
# would stack a second "0", giving "0\n0" which trips both the
# string-equals and numeric comparisons below. `|| true` keeps grep's
# own output as the sole value.
got_crit=$(grep -c '"severity":"critical"' "$SINK_DIR/fallback.log" 2>/dev/null || true)
got_warn=$(grep -c 'InhibTest.*"severity":"warning"' "$SINK_DIR/fallback.log" 2>/dev/null || true)
got_crit=${got_crit:-0}
got_warn=${got_warn:-0}
[[ "$got_crit" -ge 1 ]] && pass "inhibit: critical still dispatched ($got_crit)" \
                        || fail "critical missing after inhibit test" "$(cat $SINK_DIR/fallback.log | head -c 300)"
[[ "$got_warn" == "0" ]] && pass "inhibit: warning suppressed by same-pair critical" \
                         || fail "warning leaked through inhibit" "got=$got_warn"

# ---- 7. Silence suppresses a fresh alert ----
SILENCE_BODY=$(cat <<EOF
{
  "matchers": [
    {"name": "alertname", "value": "SilencedAlert", "isRegex": false, "isEqual": true}
  ],
  "startsAt": "$(date -u +%Y-%m-%dT%H:%M:%S.000Z)",
  "endsAt":   "$(date -u -d '+5 minutes' +%Y-%m-%dT%H:%M:%S.000Z)",
  "createdBy": "e2e",
  "comment": "phase15 test"
}
EOF
)
sid=$(curl -s -X POST "$AM_URL/api/v2/silences" \
    -H 'Content-Type: application/json' --data "$SILENCE_BODY" | jq -r '.silenceID')
[[ -n "$sid" && "$sid" != "null" ]] && pass "silence created (id=$sid)" || fail "silence create failed"

rm -f "$SINK_DIR"/critical.log "$SINK_DIR"/fallback.log
post_alert "SilencedAlert" "critical" "tenant-4" >/dev/null
sleep 10
if [[ -s "$SINK_DIR/critical.log" || -s "$SINK_DIR/fallback.log" ]]; then
    fail "silenced alert leaked to receivers" \
        "crit=$(wc -l < $SINK_DIR/critical.log 2>/dev/null || echo 0) fb=$(wc -l < $SINK_DIR/fallback.log 2>/dev/null || echo 0)"
fi
pass "silenced alert did NOT reach any receiver"

# Delete silence so we don't leave state.
curl -s -X DELETE "$AM_URL/api/v2/silence/$sid" >/dev/null

echo
echo "ALL GREEN"
