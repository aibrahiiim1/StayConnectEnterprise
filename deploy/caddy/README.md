# StayConnect - production exposure (Caddy + TLS)

Caddy terminates TLS in front of the StayConnect services. There is **one Caddyfile per ROLE**, and the
roles do not overlap:

| File | Deployed on | Sites it terminates |
|---|---|---|
| `Caddyfile.edge` | Hotel Appliance (Edge) | `portal.stayconnect.local` -> portald `127.0.0.1:8380`; `hotel.stayconnect.local` + management IP -> hotel-admin `127.0.0.1:3100` (imported from `/etc/caddy/hotel-admin/vhost.caddy`) |
| `Caddyfile.central` | Central Control Plane | one site -> ctrlapi `127.0.0.1:8080` for `/v1/* /cloud/* /healthz /readyz /metrics`, cloud-admin `127.0.0.1:3000` for everything else |
| `Caddyfile.dev` | single-box dev VM only | all three sites on one host - **never deploy this to a real Edge or Central** |

**Why the split exists.** A single all-in-one template used to serve all three sites, written when Central
and the Edge ran on the same box. When Central moved to its own server the Edge kept the template, so the
appliance advertised `api.stayconnect.local` and `admin.stayconnect.local` and returned 502 on both - routes
pointing at services that deliberately do not run there. The combined file has been removed so a redeploy
cannot reintroduce them.

**Rule:** if a route 502s because its upstream "does not run on this box", the route does not belong on
this box. Add it to the other role's file instead.

**One systemd unit per role, too.**

| Unit source | Install as | Reload |
|---|---|---|
| `stayconnect-caddy.edge.service` | `/etc/systemd/system/stayconnect-caddy.service` on the appliance | **supported** — the Edge Caddyfile leaves the admin API on `127.0.0.1:2019`, so `systemctl reload` genuinely applies the new config |
| `stayconnect-caddy.central.service` | `/etc/systemd/system/stayconnect-caddy.service` on Central | **not supported, deliberately** — no `ExecReload` at all |

**The Central reload contract.** Central runs Caddy with `admin off`, which disables the API `caddy reload`
drives. Three options exist and two of them lie to the operator:

- `ExecReload=caddy reload …` — fails every time, and fails *quietly*: the old config keeps serving and the
  endpoint still answers 200, so "reload then curl" looks like success.
- `ExecReload=caddy validate …` — worse. `systemctl reload` reports **success** while the running
  configuration is untouched. A green result for a no-op is the most dangerous of the three.
- **no `ExecReload`** — what Central ships. `systemctl reload stayconnect-caddy` fails immediately with
  *"Job type reload is not applicable"*, which is the truth.

To apply a config change on Central:

    caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
    systemctl restart stayconnect-caddy

Then verify the **active** configuration (an HTTP check, or `caddy adapt`) rather than assuming the restart
applied it. On the Edge, `caddy validate` followed by `systemctl reload` is correct and sufficient.

**Central-only names on the Edge return 404.** `api.stayconnect.local` and `admin.stayconnect.local` are
declared in `Caddyfile.edge` purely to be refused with a 404 and a short explanation. They are not proxied
anywhere. Without those blocks Caddy has no matching route and its default is an **empty HTTP 200**, which an
uptime check reads as "this host serves that name and it is healthy" — worse than the 502 it replaced.

## What this gives you

| Public host           | Terminates at Caddy → forwards to |
|-----------------------|-----------------------------------|
| `portal.example.com`  | `127.0.0.1:8380` (portald)        |
| `api.example.com`     | `127.0.0.1:8080` (ctrlapi)        |
| `admin.example.com`   | `127.0.0.1:3000` (web-admin)      |

Every response carries:
- `Strict-Transport-Security: max-age=31536000; includeSubDomains`
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Permissions-Policy: geolocation=(), microphone=(), camera=()`
- Content-Security-Policy (tuned per host — tight for the JSON API,
  looser for Next.js which ships inline runtime)

HTTP → HTTPS is automatic (Caddy's default redirect).

## Prerequisites

1. DNS A/AAAA records for `portal.example.com`, `api.example.com`,
   `admin.example.com` pointing at the appliance's public IP.
2. Ports `80` and `443` reachable from the public internet (ACME needs
   `:80`; `:443` is the whole point).
3. A real email address in the Caddyfile global block (used by Let's
   Encrypt for renewal warnings).
4. portald/ctrlapi/web-admin running and bound to `127.0.0.1` — NEVER
   expose their raw ports to the internet.

## Install

```sh
# Debian/Ubuntu: https://caddyserver.com/docs/install
apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | sudo tee /etc/apt/sources.list.d/caddy-stable.list
apt update && apt install -y caddy
```

Then drop the config in:

```sh
install -o caddy -g caddy -m 0644 \
    deploy/caddy/Caddyfile.edge /etc/caddy/Caddyfile      # on the appliance
    deploy/caddy/Caddyfile.central /etc/caddy/Caddyfile   # on Central
sed -i "s/example.com/yourdomain.com/g" /etc/caddy/Caddyfile
sed -i "s/ops@example.com/you@yourdomain.com/" /etc/caddy/Caddyfile
mkdir -p /var/log/caddy && chown caddy:caddy /var/log/caddy

# Validate and reload.
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

You can use the stock `caddy.service` shipped by the apt package — it
already binds `CAP_NET_BIND_SERVICE` and runs as the `caddy` user. The
`stayconnect-caddy.service` template in this dir is just for reference
if you ever need to diverge.

## First-boot cert issuance

On first start Caddy will attempt ACME HTTP-01 for each site. Watch the
log:

```sh
journalctl -u caddy -f
```

Look for `certificate obtained successfully`. Common failures:

- **DNS not propagated** — ACME can't resolve the hostname. Re-check
  with `dig portal.example.com` and wait.
- **Port 80 blocked** — the appliance's nftables must allow inbound
  `:80` and `:443` from the public side. The default `stayconnect.nft`
  accepts them on `wan0`; double-check if you customised.
- **Rate limit hit** — while iterating on DNS, switch the Caddyfile's
  `acme_ca` line to the staging endpoint (commented-in example in the
  template) to avoid burning real certs.

## Downstream config changes

After Caddy is fronting everything, a few upstream services need their
public URLs updated:

1. **ctrlapi**: set `CTRLAPI_COOKIE_SECURE=true` in `/etc/stayconnect/ctrlapi.env`
   and add your admin hostname to `CTRLAPI_ALLOW_ORIGINS`. The cookie
   must be secure-flagged once it travels over real HTTPS or browsers
   will refuse it.
2. **web-admin**: no change needed — it already uses relative URLs and
   honours `X-Forwarded-*`.
3. **Google OAuth**: in the Google Cloud console, update the authorised
   redirect URI of each `social_oauth_providers` row to
   `https://portal.example.com/auth/social/callback`.
4. **Stripe**: in the Stripe dashboard, set the webhook endpoint to
   `https://api.example.com/v1/webhooks/stripe/{tenant_id}` (one per
   tenant). The webhook_secret stays identical; only the URL changes.
5. **scd**: no change needed — the appliance's RPC path is still NATS.

## Dev-mode: `tls internal`

On a laptop / VM without public DNS you can still exercise the full
proxy path by swapping the global block's `email …` directive for:

```caddy
{
    local_certs    # Caddy uses its own internal CA
}
```

and the test-domain equivalents for each site (see the E2E test in
`scripts/phase14-tls-test.sh`). Add the local CA to your trust store
with `caddy trust`.

## Known limitations

- **Alertmanager** for alert routing is not fronted here — run it
  behind its own Caddy block if you expose it.
- **Prometheus / Grafana** from phase 13 stay on `127.0.0.1`. Put them
  behind `metrics.example.com` with BasicAuth if you need remote ops
  access.
- **Rate limits** at the edge are not configured; Caddy has
  `caddy-ratelimit` modules but they're not bundled in the stock apt
  build. Revisit when there's a real abuse signal.
