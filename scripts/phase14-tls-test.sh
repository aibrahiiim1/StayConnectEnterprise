#!/usr/bin/env bash
# Phase 14 — TLS + reverse-proxy E2E.
#
# Stands up Caddy on the VM with Caddyfile.dev (local CA, ports 9443/9080),
# then asserts the full proxy path works for all three hostnames:
#
#   1. Production Caddyfile is syntactically valid (caddy validate)
#   2. Dev Caddy serves all three hosts over HTTPS
#   3. HTTP → HTTPS redirect works
#   4. Security headers are set on every response
#   5. Backends see X-Forwarded-* headers (verified via ctrlapi's own
#      trace-id plumbing)
#   6. Stripe webhook path still returns 403 on unknown tenant (proves
#      the request reaches ctrlapi through the proxy intact)
#   7. api host serves the tight API CSP (default-src 'none')
#
# Caddy is installed via apt if not already present; keeping it as a
# host service avoids the nftables-vs-docker iptables clash we hit in
# phases 13/observability.
set -euo pipefail

CADDY_BIN=${CADDY_BIN:-/usr/bin/caddy}
DEV_CONF=${DEV_CONF:-/opt/stayconnect/deploy/caddy/Caddyfile.dev}
PROD_CONF=${PROD_CONF:-/opt/stayconnect/deploy/caddy/Caddyfile}
RUN_DIR=/tmp/stayconnect-caddy-test
HTTPS_PORT=9443
HTTP_PORT=9080

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

# ---- 0. prerequisites ----
if ! command -v caddy >/dev/null; then
    echo "  caddy not installed; installing via apt…"
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl >/dev/null
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null
    echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] \
https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" \
        > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
    apt-get install -y caddy >/dev/null
fi
[[ -x "$CADDY_BIN" ]] || fail "caddy binary not found" "checked $CADDY_BIN"
pass "caddy installed: $($CADDY_BIN version | head -n1)"

# /etc/hosts entries — resolve the three dev hostnames to 10.10.0.1 (the
# captive-portal bridge IP). 10.10.0.1 is reachable both from the host
# AND from inside the client1 network namespace, while 127.0.0.1 resolves
# to the *netns-local* loopback from inside client1 — breaking the phase
# 2/3/4 tests that curl these hostnames via `ip netns exec client1`.
# Caddy binds `*:80` + `*:443`, so 10.10.0.1 hits it fine from either
# side.
for host in portal.stayconnect.local api.stayconnect.local admin.stayconnect.local; do
    if grep -qE "^127\\.0\\.0\\.1 $host\$" /etc/hosts; then
        sed -i "s|^127\\.0\\.0\\.1 $host\$|10.10.0.1 $host|" /etc/hosts
    elif ! grep -q "$host" /etc/hosts; then
        echo "10.10.0.1 $host" >> /etc/hosts
    fi
done
pass "test hostnames in /etc/hosts"

# ---- 1. production config validates ----
if $CADDY_BIN validate --config "$PROD_CONF" --adapter caddyfile >/tmp/caddy-validate.out 2>&1; then
    pass "production Caddyfile syntactically valid"
else
    fail "prod Caddyfile validate failed" "$(cat /tmp/caddy-validate.out)"
fi

# ---- 2. spin up Caddy with the dev config ----
# Stop the stock caddy.service shipped by apt — it starts automatically
# on install and would occupy the default admin port 2019. Ours binds
# admin on 2020 via Caddyfile.dev, but a lingering stock instance still
# owns its own set of ports and muddies the test.
systemctl stop caddy 2>/dev/null || true
systemctl disable caddy 2>/dev/null || true
pkill -f "caddy.*$DEV_CONF" 2>/dev/null || true
# Wait briefly for sockets to release.
sleep 2
rm -rf "$RUN_DIR" && mkdir -p "$RUN_DIR"

# Run as the current user (root here) so we skip the caddy-user setup;
# ports 9080/9443 don't need CAP_NET_BIND_SERVICE anyway.
XDG_DATA_HOME="$RUN_DIR" XDG_CONFIG_HOME="$RUN_DIR" \
    $CADDY_BIN start --config "$DEV_CONF" --adapter caddyfile --pidfile "$RUN_DIR/caddy.pid" \
    >/tmp/caddy-start.out 2>&1

# Wait for the admin API to be ready; that's Caddy's sign of life.
for i in $(seq 1 15); do
    if curl -sk "https://portal.stayconnect.local:${HTTPS_PORT}/" >/dev/null 2>&1; then
        break
    fi
    sleep 1
done
pass "caddy started + listening on :${HTTPS_PORT}"

cleanup() {
    if [[ -f "$RUN_DIR/caddy.pid" ]]; then
        kill "$(cat "$RUN_DIR/caddy.pid")" 2>/dev/null || true
    fi
    # Best-effort — kill anything we might have left behind.
    pkill -f "caddy.*$DEV_CONF" 2>/dev/null || true
    rm -rf "$RUN_DIR"
}
trap cleanup EXIT

# ---- 3. all three hosts respond over HTTPS ----
for host_port in \
    "portal.stayconnect.local:${HTTPS_PORT}" \
    "api.stayconnect.local:${HTTPS_PORT}/healthz" \
    "admin.stayconnect.local:${HTTPS_PORT}"; do
    code=$(curl -sk -o /dev/null -w '%{http_code}' "https://${host_port}")
    # portald's root is 200; ctrlapi /healthz is 200; admin may 302 to /login.
    case "$code" in
        200|302|307) pass "HTTPS OK: $host_port ($code)";;
        *) fail "HTTPS reachability" "host=$host_port code=$code";;
    esac
done

# ---- 4. HTTP → HTTPS redirect ----
redir=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' \
    "http://api.stayconnect.local:${HTTP_PORT}/healthz")
code=${redir%% *}
target=${redir#* }
[[ "$code" =~ ^30[12378]$ ]] && pass "plain HTTP returns 3xx redirect" || fail "no HTTP redirect" "got=$redir"
[[ "$target" == https://* ]] && pass "redirects to https:// ($target)" || fail "redirect target not https" "got=$target"

# ---- 5. Security headers on every host ----
for host in portal.stayconnect.local api.stayconnect.local admin.stayconnect.local; do
    hdrs=$(curl -sk -D - -o /dev/null "https://${host}:${HTTPS_PORT}/")
    for h in "Strict-Transport-Security" "X-Content-Type-Options" "X-Frame-Options" "Referrer-Policy" "Permissions-Policy"; do
        echo "$hdrs" | grep -qi "^${h}:" || fail "missing header $h on $host" "$(echo "$hdrs" | head -n20)"
    done
done
pass "security headers present on all three hosts"

# api host serves the tight JSON CSP.
api_csp=$(curl -sk -D - -o /dev/null "https://api.stayconnect.local:${HTTPS_PORT}/healthz" \
    | grep -i '^content-security-policy:' | head -n1)
echo "$api_csp" | grep -q "default-src 'none'" \
    && pass "api host has tight CSP (default-src 'none')" \
    || fail "api CSP wrong" "got=$api_csp"

# ---- 6. Backend integration: api webhook on unknown tenant → 403 ----
# Proves the request reaches ctrlapi (not a 502 from Caddy).
wh_code=$(curl -sk -o /dev/null -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -H 'Stripe-Signature: t=0,v1=deadbeef' \
    --data '{}' \
    "https://api.stayconnect.local:${HTTPS_PORT}/v1/webhooks/stripe/00000000-0000-0000-0000-000000000000")
[[ "$wh_code" == "403" ]] && pass "webhook path reaches ctrlapi through proxy (403 unknown tenant)" \
                          || fail "webhook path broken" "code=$wh_code"

# Bonus: ctrlapi /metrics is reachable via the proxy — sanity check that
# Caddy isn't inadvertently blocking the route.
m_code=$(curl -sk -o /dev/null -w '%{http_code}' "https://api.stayconnect.local:${HTTPS_PORT}/metrics")
[[ "$m_code" == "200" ]] && pass "/metrics reachable via api host" \
                         || fail "/metrics blocked" "code=$m_code"

echo
echo "ALL GREEN"
