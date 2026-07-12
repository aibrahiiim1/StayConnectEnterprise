# Hotel Admin TLS Certificate — Managed Lifecycle

Production, self-managing lifecycle for the Hotel Admin dual-SAN leaf certificate
(`hotel.stayconnect.local` + the current Management/WAN IP). No operator has to
remember to renew anything. Scope is ONLY this local Hotel Admin HTTPS leaf — it
never touches the vendor appliance mTLS PKI, Root/Intermediate CA, API-client /
NATS certs, or assignment/license/command/update keys.

## Components

| Piece | Where | Role |
|-------|-------|------|
| `stayconnect-hotel-admin-cert-manager` | `/usr/local/sbin` (root) | the lifecycle engine: resolve IP → decide → mint → validate → atomic swap → reload → health-check → rollback; writes status + audit |
| `stayconnect-hotel-admin-cert-renew.{service,timer}` | systemd | runs the manager `renew` after boot and every 6h, jittered, `Persistent` |
| scd `/v1/hotel-admin-cert/{check,rotate,renew}` | data-plane (root) | executes the manager on behalf of the sandboxed edged |
| edged `/edge/v1/hotel-admin-cert[/check|/rotate]` | data-plane | status surface + manual controls (permission + step-up); proxies exec to scd |
| Hotel Admin → **Networking → TLS certificate** | hotel-admin UI | live status card + “Check certificate” + “Rotate Hotel Admin certificate” |
| Central **Fleet → TLS cert** column | cloud-admin UI | fleet-wide warning/critical/expired/renewal-failure from sanitized telemetry |
| `/etc/caddy/hotel-admin/vhost.caddy` | imported by main Caddyfile | managed vhost: site address (DNS + current IP), `tls`, reverse_proxy — rewritten on renewal |

## Renewal triggers & thresholds

The manager renews when ANY of: remaining validity ≤ **45 days**; the management
IP changed; the certificate SAN set no longer equals `{DNS:hotel.stayconnect.local,
IP:<current mgmt IP>}`; or the cert is missing/invalid. Otherwise it is a **no-op**
(idempotent). Status thresholds surfaced in both UIs and telemetry:

`healthy` > 45d · `renewal_due` ≤ 45d · `warning` ≤ 30d · `critical` ≤ 14d ·
`emergency` ≤ 7d · `expired` invalid.

## Management IP source (never hardcoded)

The management interface is read from netd's own config (`NETD_MGMT_IFACE`, else
`NETD_WAN_IFACE`); the IP is read **live** off that interface (single global-scope
IPv4). Renewal is **refused** if the IP is ambiguous/absent, and guest-LAN
(`10.10.0.x`), loopback, link-local and docker addresses are rejected — the guest
LAN address can never enter the management certificate.

## Safe minting & validation

A candidate is minted into a protected staging dir (`/run/stayconnect/ha-cert.*`,
root 0700) and validated BEFORE activation: signed by the expected Caddy local CA;
private key matches the cert; DNS SAN present; current IP SAN present; **no
unexpected SAN**; currently valid; ≥ freshness policy; `serverAuth` EKU; key type
EC `prime256v1`; chain includes the intermediate. Any failure aborts before the
active files are touched.

**Permissions.** Directory `/etc/caddy/hotel-admin` is root-owned `0755` (not
writable by the Hotel Admin app user `stayconnect`). The private key is `0600`
readable only by `caddy` (the TLS terminator); cert/chain are `0644`. Private key
material is never printed to logs or sent in telemetry.

## Atomic replacement & rollback

1. Mint + fully validate the candidate.
2. Preserve current cert **and** vhost as `.prev`.
3. Atomically replace the active key/cert/chain, rewrite `vhost.caddy` to the
   current IP.
4. `caddy validate`, then **reload** (never restart).
5. Live health checks through **both** `https://hotel.stayconnect.local` and
   `https://<current-mgmt-ip>`: same new serial, CA validates hostname + IP, and
   the app responds (static 200, redirect 307, login endpoint reachable).
6. On ANY failure: restore cert **and** vhost `.prev`, reload, verify the previous
   serial is serving again, record the failure and raise an alert. Only the
   current and previous generations are kept.

## Monitoring & audit

The manager writes `/etc/caddy/hotel-admin/status.json` (subject, issuer, serial,
SHA-256 fingerprint, DNS/IP SANs, issued/expires, days remaining, threshold,
current mgmt IP, SAN match, last attempt/success/result/error). edged serves it at
`GET /edge/v1/hotel-admin-cert`; scd folds the **sanitized** subset into its
health telemetry (`hotel_admin_cert`) → Central Fleet. Local audit events (site
`audit_log`): `renewal_started/succeeded/failed`, `rollback_succeeded/failed`,
`management_ip_changed`, `certificate_san_changed`; edged additionally records
`hotel_admin_cert.rotate_requested` with the operator + reason.

## Manual controls (Hotel IT)

- **Rotate Hotel Admin certificate** — Hotel-IT (`network`) role + password
  step-up + reason + typed `ROTATE` confirmation. Runs the exact same safe
  lifecycle; cannot upload a key or bypass validation.
- **Check certificate** — diagnostic only, validates the active cert, changes
  nothing.

## Failure behavior

Renewal is a background maintenance job with **no** dependency from
caddy/edged/scd/netd/portald/acctd — a renewal failure never stops DHCP, DNS, the
captive portal, guest auth, sessions, PMS, accounting or the data plane. If renewal
fails while the current cert is still valid, that cert keeps serving, alerts are
raised, and the manager backs off (bounded exponential, capped at 24h) so the 6h
timer never becomes a rapid loop.

---

## Operations runbook

**Check status:** `stayconnect-hotel-admin-cert-manager status` (or Hotel Admin →
Networking → TLS certificate, or Central Fleet).
**Force a rotation:** UI “Rotate”, or `systemctl start
stayconnect-hotel-admin-cert-renew.service` for a due-only run, or (root)
`stayconnect-hotel-admin-cert-manager rotate`.
**Diagnose without changing anything:** `stayconnect-hotel-admin-cert-manager check`.
**Logs:** `journalctl -u stayconnect-hotel-admin-cert-renew.service`; audit in the
site `audit_log` (`target_type='hotel_admin_cert'`).
**Timer:** `systemctl list-timers stayconnect-hotel-admin-cert-renew.timer`.

## Backup / rollback

The active generation and its predecessor live in `/etc/caddy/hotel-admin/` as
`hotel-admin.{key,crt,fullchain.crt}` and `…​.prev` (+ `vhost.caddy[.prev]`). The
manager rolls back automatically on a failed renewal. **Manual rollback** (rare):
```
cd /etc/caddy/hotel-admin
cp -a hotel-admin.key.prev hotel-admin.key
cp -a hotel-admin.crt.prev hotel-admin.crt
cp -a hotel-admin.fullchain.crt.prev hotel-admin.fullchain.crt
cp -a vhost.caddy.prev vhost.caddy
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile && systemctl reload stayconnect-caddy
```
The CA the certs chain to (Caddy local CA) is backed up per the standing Caddy PKI
backup; losing `/etc/caddy/hotel-admin` is fully recoverable — the next timer run
re-mints from the CA.

## Certificate incident procedure

- **`warning`/`critical`/`emergency`/renewal-failure** on Central Fleet or Hotel
  Admin: open `journalctl -u stayconnect-hotel-admin-cert-renew`, read
  `status.json` `last_error`. Common causes: ambiguous mgmt IP (fix the interface
  config), CA unavailable. Force a `rotate` once resolved.
- **`expired`:** the box keeps running (guest plane unaffected); Hotel Admin HTTPS
  shows a browser warning. Run a manual `rotate`; if it fails, check the CA and the
  management IP, then re-run.
- The cert lifecycle is isolated: a certificate incident is **never** a guest-service
  incident.

## Management IP change workflow

When Hotel IT changes the WAN/Management IP via **Networking → WAN/LAN settings**
(apply → confirm), edged fires an idempotent renewal after confirm. The manager
resolves the new IP, mints a cert with the new IP SAN, **removes the old IP SAN**,
keeps the DNS SAN, rewrites `vhost.caddy` so Caddy routes the new IP, atomically
switches, health-checks both URLs, and audits `management_ip_changed` /
`certificate_san_changed`. No manual certificate step is required.
