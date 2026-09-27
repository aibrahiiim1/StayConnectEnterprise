# StayConnect — Testing Runbook (Control Plane + Appliance Edge)

Step‑by‑step checks for both planes and the link between them. Run everything from
a workstation that can SSH to both hosts.

```bash
# Set once per shell
C=root@150.0.0.252     # Central control plane
A=root@172.21.60.25    # PRE-LIVE appliance (the only appliance — CLAUDE.md §0D)
```

Host / service map:

| Plane | Host | Key services | Ingress |
|---|---|---|---|
| Central control plane | `150.0.0.252` | `stayconnect-ctrlapi` (:8080; appliance mutual TLS :9443), `cloud-admin` (:3000), Caddy, Postgres `sc-central-pg`, Redis `sc-central-redis`. No NATS (CLAUDE.md §0E) | `sc-central.echofusion.com` / `admin.stayconnect.local` → Caddy (`/v1`, `/cloud` → :8080, the rest → :3000) |
| Appliance edge | `172.21.60.25` | `stayconnect-scd`, `edged` (:8090), `netd`, `portald` (:8380), `caddy` (:80/:443), `hotel-admin` (:3100), `acctd`, site Postgres `stayconnect-pg` | `portal.stayconnect.local` (guest) · `hotel.stayconnect.local` (Hotel Admin) |

> **UI note:** the admin UIs redirect unauthenticated requests, so a protected
> route returns **307 → /login**, and **/login returns 200**. A `500` on a
> protected route is a bug (see the middleware note at the end), not normal SSR.

---

## Part A — Central control plane

**A1. Services**
```bash
ssh $C 'systemctl is-active stayconnect-ctrlapi; \
        docker ps --format "{{.Names}}: {{.Status}}" | grep sc-central'
```
Expect: ctrlapi `active`; `sc-central-pg` and `sc-central-redis` `Up`.

**A2. API health + auth endpoint**
```bash
ssh $C 'curl -s -o /dev/null -w "healthz: %{http_code}\n" http://127.0.0.1:8080/healthz
        curl -s -o /dev/null -w "auth(bad creds): %{http_code}\n" http://127.0.0.1:8080/v1/auth/login \
             -H "Content-Type: application/json" -d "{\"email\":\"x@x\",\"password\":\"x\"}"'
```
Expect: `healthz: 200`, `auth(bad creds): 401`.

**A3. Platform admin UI**
```bash
ssh $C 'for p in / /login /overview /dashboard; do printf "%s -> " $p; \
  curl -s -o /dev/null -w "%{http_code}\n" -H "Host: admin.stayconnect.local" http://127.0.0.1:3000$p; done'
```
Expect: `/ -> 307` (to `/overview`), `/login -> 200`, `/overview -> 307` (to `/login` when signed out),
`/dashboard -> 308` (old address, permanent redirect to `/overview`).

**A4. Database**
```bash
ssh $C "docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc \
  \"SELECT serial, lifecycle_state, activated_at FROM appliances ORDER BY serial\""
```
Expect: only the real appliances; `lifecycle_state` is one of `pending_approval`, `assigned`, `revoked`,
`decommissioned` (migration 0046 — there is no `status` column any more).

---

## Part B — Appliance edge

**B1. Daemons**
```bash
ssh $A 'for s in stayconnect-scd stayconnect-edged stayconnect-netd stayconnect-portald \
                 stayconnect-caddy stayconnect-hotel-admin stayconnect-acctd; do \
          printf "%s: %s\n" "${s#stayconnect-}" "$(systemctl is-active $s)"; done'
```
Expect: all `active`.

**B2. scd Central status (activation / licence / link)** — the single most useful edge check
```bash
ssh $A 'curl -s --unix-socket /run/stayconnect/scd.sock http://localhost/v1/central/status | python3 -m json.tool'
```
Expect: `activation: "activated"`, `license.state: "active"`, `central.state: "connected"`, a recent
`central.last_contact_at`, and a future `details.cert_not_after` ([CENTRAL_CONTROL_PLANE.md §8](CENTRAL_CONTROL_PLANE.md#8-hotel-admin--central)).

**B3. (removed)** The telemetry outbox is static by decision (CLAUDE.md §0E); there is nothing to drain.

**B4. Link to Central, seen from Central**
```bash
ssh $C "docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc \
  \"SELECT serial, round(extract(epoch from now()-last_seen_at))||'s ago' \
    FROM appliances WHERE last_seen_at IS NOT NULL ORDER BY last_seen_at DESC\""
```
Expect: `last_seen` within the last minute or so (the assignment poll runs every 30 s).

**B5. Site DB**
```bash
ssh $A 'docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc "SELECT count(*) FROM sessions"'
```
Expect: a number, no error.

---

## Part C — Control‑plane ⇄ edge, end‑to‑end

Prefer the automated suite (Part E) for signed flows — it self‑cleans. Manual spot‑checks:

**C1. License lifecycle** — renew/suspend/resume/revoke on the appliance's page in Central, press **Check now**
in Hotel Admin (or `POST /v1/central/refresh` on the scd socket; otherwise the licence is fetched every 6 h),
then:
```bash
ssh $A 'curl -s --unix-socket /run/stayconnect/scd.sock http://localhost/v1/central/status \
  | python3 -c "import sys,json;print(json.load(sys.stdin)[\"license\"][\"state\"])"'
```
Expect state to follow the signed doc: `active → suspended → active`, revoke → `revoked`. The appliance's
activation stays `activated` throughout — licence actions never touch the appliance lifecycle.

**C2. Offline activation** — see [APPLIANCE_ACTIVATION_AND_LICENSING.md](APPLIANCE_ACTIVATION_AND_LICENSING.md)
(activation request → Import activation request → Activation package → upload).

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

## Part E — Automated 37‑point suite (HISTORICAL — pre-redesign)

> **HISTORICAL.** This harness exercised enrollment tokens, the NATS buses, the signed command channel and
> the update agent — all removed or switched off (CLAUDE.md §0E; [API_DEPRECATIONS.md](API_DEPRECATIONS.md)).
> It does not run against the current Central. Kept as the record of what it proved at the time. Current
> lifecycle coverage for Central is `scripts/lifecycle-regression.sh` and the ctrlapi/cloud-admin test
> suites.

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
- **Don’t hand‑register a serial that is already activated** — the clone / hardware-reuse guard `403`s it
  and raises a security alert. Retire the old appliance first.
