# StayConnect — Testing Runbook (Control Plane + Appliance Edge)

Step‑by‑step checks for both planes and the link between them. Run everything from
a workstation that can SSH to both hosts.

```bash
# Set once per shell
C=root@150.0.0.252     # Central control plane
A=root@172.21.60.23    # Appliance edge (radius)
```

Host / service map:

| Plane | Host | Key services | Ingress |
|---|---|---|---|
| Central control plane | `150.0.0.252` | `stayconnect-ctrlapi` (:8080), `stayconnect-nats-authz`, `cloud-admin` (:3000), Postgres `sc-central-pg`, NATS mTLS `sc-central-nats-mtls` (:4223), legacy `sc-central-nats` (:4222, loopback‑only) | `admin.stayconnect.local` → :3000 · `api.stayconnect.local` → :8080 |
| Appliance edge | `172.21.60.23` | `stayconnect-scd`, `edged` (:8090), `netd`, `portald` (:8380), `caddy` (:80/:443), `hotel-admin` (:3100), `acctd`, site Postgres `stayconnect-pg` | `portal.stayconnect.local` (guest) · `hotel.stayconnect.local` (Hotel Admin) |

> **UI note:** the admin UIs redirect unauthenticated requests, so a protected
> route returns **307 → /login**, and **/login returns 200**. A `500` on a
> protected route is a bug (see the middleware note at the end), not normal SSR.

---

## Part A — Central control plane

**A1. Services**
```bash
ssh $C 'systemctl is-active stayconnect-ctrlapi stayconnect-nats-authz; \
        docker ps --format "{{.Names}}: {{.Status}}" | grep sc-central'
```
Expect: ctrlapi + authz `active`; `sc-central-pg`, `sc-central-nats`, `sc-central-nats-mtls`, `sc-central-redis` `Up`.

**A2. API health + auth endpoint**
```bash
ssh $C 'curl -s -o /dev/null -w "healthz: %{http_code}\n" http://127.0.0.1:8080/healthz
        curl -s -o /dev/null -w "auth(bad creds): %{http_code}\n" http://127.0.0.1:8080/v1/auth/login \
             -H "Content-Type: application/json" -d "{\"email\":\"x@x\",\"password\":\"x\"}"'
```
Expect: `healthz: 200`, `auth(bad creds): 401`.

**A3. Platform admin UI**
```bash
ssh $C 'for p in / /login /dashboard; do printf "%s -> " $p; \
  curl -s -o /dev/null -w "%{http_code}\n" -H "Host: admin.stayconnect.local" http://127.0.0.1:3000$p; done'
```
Expect: `/ -> 307`, `/login -> 200`, `/dashboard -> 307`.

**A4. Database**
```bash
ssh $C "docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc \
  \"SELECT serial, lifecycle_state, status FROM appliances ORDER BY serial\""
```
Expect: only the real appliances.

**A5. NATS buses**
```bash
ssh $A 'timeout 4 bash -c "echo>/dev/tcp/150.0.0.252/4223" && echo ":4223 OPEN (mTLS bus)" || echo ":4223 down"
        timeout 4 bash -c "echo>/dev/tcp/150.0.0.252/4222" && echo ":4222 OPEN (BAD)" || echo ":4222 REFUSED (old cred closed)"'
```
Expect: `:4223 OPEN`, `:4222 REFUSED`.

---

## Part B — Appliance edge

**B1. Daemons**
```bash
ssh $A 'for s in stayconnect-scd stayconnect-edged stayconnect-netd stayconnect-portald \
                 stayconnect-caddy stayconnect-hotel-admin stayconnect-acctd; do \
          printf "%s: %s\n" "${s#stayconnect-}" "$(systemctl is-active $s)"; done'
```
Expect: all `active`.

**B2. scd status (enrollment / license / mTLS)** — the single most useful edge check
```bash
ssh $A 'curl -s --unix-socket /run/stayconnect/scd.sock http://localhost/v1/setup/status | python3 -m json.tool'
```
Expect: `enrolled: true`, `locked: true`, `license.state: "Active"`, `api_mtls.mtls_ready: true`, a future `not_after`.

**B3. Sync outbox**
```bash
ssh $A 'curl -s --unix-socket /run/stayconnect/scd.sock http://localhost/v1/admin/outbox/stats'
```
Expect: `{"dead":0,...,"pending":0}`.

**B4. Live mTLS link to Central**
```bash
ssh $A 'ss -tnp | grep ":4223" | grep -q scd && echo "scd ESTABLISHED to :4223 mTLS" || echo "not connected"'
ssh $C "docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc \
  \"SELECT serial, round(extract(epoch from now()-last_seen_at))||'s ago' \
    FROM appliances WHERE last_seen_at IS NOT NULL ORDER BY last_seen_at DESC\""
```
Expect: ESTABLISHED + `last_seen` a few seconds ago.

**B5. Site DB**
```bash
ssh $A 'docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc "SELECT count(*) FROM sessions"'
```
Expect: a number, no error.

---

## Part C — Control‑plane ⇄ edge, end‑to‑end

Prefer the automated suite (Part E) for signed flows — it self‑cleans. Manual spot‑checks:

**C1. License lifecycle (edge reacts within ~60 s)** — issue/suspend/resume/revoke from the Platform UI, then:
```bash
watch -n5 "ssh $A 'curl -s --unix-socket /run/stayconnect/scd.sock http://localhost/v1/setup/status \
  | python3 -c \"import sys,json;print(json.load(sys.stdin)[\\\"license\\\"][\\\"state\\\"])\"'"
```
Expect state to follow the signed doc: `Active → Suspended → Active`, revoke → gated.

**C2. mTLS revocation** — revoke the appliance cert in the UI; `ss -tnp | grep 4223` on the edge drops within the JWT TTL (prod 600 s; ~15 s under a shortened TTL).

**C3. Signed command / update+rollback / offline activation** — run via the harness (Part E).

---

## Part D — Guest plane (edge)

**D1. Captive portal + HTTPS**
```bash
ssh $A 'curl -s -o /dev/null -w "generate_204: %{http_code}\n" http://127.0.0.1/generate_204
        curl -sk -o /dev/null -w "portal HTTPS: %{http_code}\n" \
             --resolve portal.stayconnect.local:443:127.0.0.1 https://portal.stayconnect.local/
        curl -sk -o /dev/null -w "hotel-admin /login: %{http_code}\n" \
             --resolve hotel.stayconnect.local:443:127.0.0.1 https://hotel.stayconnect.local/login'
```
Expect: `generate_204: 308`, `portal HTTPS: 200`, `hotel-admin /login: 200`.

**D2. Full guest journey** (real client on guest VLAN 219) — connect → DHCP lease → captive auto‑pop → redeem voucher → internet + a `sessions` row in the site DB. Touches the live VLAN; run only in a maintenance window.

---

## Part E — Automated 37‑point suite (fastest full test of both planes)

Self‑cleaning orchestrator (same one that produced `ACC‑… PASS=56 FAIL=0`):
```bash
bash <scratchpad>/acc_run.sh
```
Runs: cloud harness (tokens, enroll, CSR/cert, API mTLS, 5‑state license, replay/clone/replacement, no‑PII, audit) → NATS harness (mTLS‑only, per‑appliance isolation, cross‑tenant denial, missing/revoked cert, active revocation) → live appliance execution (command exactly‑once, update success + health‑fail rollback, offline activation + reconcile) → tenant‑isolation extras → cleanup to `0/0/0` with archive triggers restored. Expect: `RESULT: PASS=56 FAIL=0`.

Single slices:
```bash
ssh $C '/tmp/acceptance'      # cloud points 1–15, 20–29, 36–37
ssh $C '/tmp/nats-acctest'    # NATS points 11–19
# if "Permission denied": ssh $C 'chmod +x /tmp/acceptance /tmp/nats-acctest'
```

---

## Gotchas

- **Admin UI middleware** must redirect via `req.nextUrl.clone()` + `NextResponse.redirect(url)` (an **absolute** URL). A **relative** `Location` header makes current Next.js throw `ERR_INVALID_URL` → `500` on every protected route. The `hotel.*` Caddy vhost carries a `header_down Location "^https?://localhost:3100…"` rewrite so the absolute redirect is rewritten to a relative one for the browser.
- **Frontend `/` needs no cookie to redirect** — a fresh visitor should get `307 → /login`, never `500`. Test `/login` for a `200`.
- **NATS JWT TTL** — the suite uses `AUTHZ_USER_TTL_SECONDS=15` only for the revocation test and restores production **600**. If you run `nats-acctest` standalone with a short TTL, set it back to 600 afterward. Update delivery is core‑NATS (no redelivery), so keep TTL=600 during update tests.
- **Don’t hand‑enroll a serial that already exists** (`ACC‑A1` …) — the clone guard `403`s it. The suite pre‑cleans.
- **Manual enroll/license/update tests create fixtures.** Prefer Part E; cleanup SQL is `acc_cleanup.sql`.
