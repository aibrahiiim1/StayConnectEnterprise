# Deployment — Cloud

> Production layout for OneGate Central (the vendor half: licensing, activation
> and fleet status — [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md)).
> Central runs on its own host (`sc-central.echofusion.com`, CLAUDE.md §0D).
> Appliance counterpart: [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md).

## 1. Components

```
                 Internet
                    │ :443                                   │ :9443 (client-cert gated)
              ┌─────▼─────┐                                  │
              │   Caddy   │  TLS, security headers            │
              └──┬─────┬──┘                                  │
   everything else  /v1/* /cloud/* /healthz /readyz          │
              │           │                                  │
     ┌────────▼───┐   ┌───▼──────────────────────────────────▼─┐   ┌──────────────┐
     │ cloud-admin│   │ ctrlapi  :8080 /v1/auth, /cloud/v1,     │──▶│  Postgres +  │
     │ (Next.js,  │   │          appliance protocol             │   │ TimescaleDB  │
     │  :3000)    │   │          :9443 appliance mutual TLS     │   └──────────────┘
     └────────────┘   └─────────────────────────────────────────┘──▶ Redis (sessions)
     appliances dial in over HTTPS only (outbound from the hotel) — no message bus
     Prometheus / Grafana / Alertmanager · backup cron · secrets
```

| Component | Sizing / notes |
|---|---|
| **ctrlapi** | stateless Go binary (`stayconnect-ctrlapi.service`); 1 replica until the appliance-JWT replay cache moves to Redis ([SECURITY_HARDENING.md](SECURITY_HARDENING.md) §7); env from [`deploy/env/ctrlapi.env.example`](../deploy/env/ctrlapi.env.example): `CTRLAPI_DB_URL`, `CTRLAPI_REDIS_URL`, `CTRLAPI_VENDOR_KEY`, `CTRLAPI_ASSIGN_KEY`, `CTRLAPI_REGISTRY_ROOT_KEY`, `CTRLAPI_APPLIANCE_BASE`, `CTRLAPI_COOKIE_SECURE=true`, `CTRLAPI_ALLOW_ORIGINS=https://admin.<domain>` |
| **cloud-admin** | Next.js standalone release on `:3000`; `/api/v1/*` and `/api/cloud/*` proxy → ctrlapi |
| **Postgres + TimescaleDB** | the `stayconnect` DB ([CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md) §2); hypertable `audit_log`; loopback/VPC-only |
| **Redis** | operator sessions (`sc:sess:*`); later: shared JWT replay cache |
| **Caddy** | `deploy/caddy/Caddyfile.central`; HSTS etc.; only :443 exposed (plus ctrlapi's own :9443) |
| **Observability** | Prometheus (ctrlapi metrics, PG/Redis exporters), Grafana **behind Caddy**, Alertmanager → SendGrid (never the Gmail relay — [SECURITY_HARDENING.md](SECURITY_HARDENING.md) §1) |
| **Backups** | nightly `pg_dump -Fc` + off-host copy ([BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §2) |
| **Secrets** | vendor Ed25519 signing key (0600 file, CA-grade handling, encrypted escrow); assignment signing key and registry root key; appliance CA intermediate key (root kept offline); DB/Redis credentials; SendGrid API key. Env files 0600 root-owned, or a proper secrets manager |

There is **no NATS** on Central and no telemetry consumer (CLAUDE.md §0E).

## 2. Network exposure

| Port | Exposure |
|---|---|
| 443 (Caddy) | public — cloud-admin + `/v1/auth` + `/cloud/v1` + appliance protocol before a certificate is issued |
| **9443 (ctrlapi mTLS)** | **public but client-certificate-gated** — the appliance mutual-TLS surface (`CTRLAPI_MTLS_ADDR`). `RequireAndVerifyClientCert` against the appliance CA rejects anything without a certificate the CA signed, during the handshake. It must be reachable from wherever hotels are: the design is appliance-initiated outbound, so restricting it to a management subnet breaks the first appliance installed elsewhere. |
| 5432 / 6379 / 8080 / 3000 / 9090 / 3001 / 9093 | **never public** — loopback or private VPC only |

Apply with **`deploy/scripts/central-firewall.sh`**, which derives the mTLS port from
`deploy/config/central-endpoint.env` so it cannot drift from what appliances are told to dial. 9443 was
listening but firewalled on the live host: `ss` showed it, loopback tests passed, and every appliance got a
connection timeout — a port nothing on Central logs, because the connections never arrived.

The cloud initiates **no** connections toward hotels. Anything that looks like
"cloud dials appliance" is a design violation.

## 3. DNS / TLS

- **`sc-central.echofusion.com` → ctrlapi** — the appliance-facing endpoint, and the *only* Central address
  an appliance ever learns. It is defined once in
  [`deploy/config/central-endpoint.env`](../deploy/config/central-endpoint.env); appliance provisioning and
  Central both read that file, so the two cannot drift. Moving Central is a DNS change and nothing else —
  no appliance is edited, because none of them knows an IP. Real certificate, never `local_certs`.
- `admin.<domain>` → cloud-admin vhost. Deliberately a **different** name from the appliance endpoint, so
  the admin UI can later sit behind MFA / VPN / Zero-Trust without that becoming a runtime dependency of a
  hotel's connectivity.

An `/etc/hosts` entry on an appliance is a stopgap for the hours before DNS propagates. It is never the
product's dependency: it exists on one machine and the next deployment will not have it. Provisioning says
so out loud when it finds one.

## 4. Bring-up order

1. Postgres (+ timescaledb extension).
   **Schema: `deploy/scripts/central-migrate.sh up`** — never a hand-typed range of files. It keeps a
   `schema_migrations` ledger, so a fresh install and an upgrade reach the same schema and neither one can
   skip a migration a feature depends on. On a Central that predates the ledger, adopt its history once
   (`central-migrate.sh adopt --through <last-applied> --yes`) and then run `up`.
2. Redis.
3. **The vendor signing identity — created once, ever**:
   `deploy/scripts/vendor-signing-key.sh init`, then immediately `… backup <encrypted-file>` and publish the
   fingerprint. A *replacement* Central host runs `… restore <encrypted-file>` instead; running `init` there
   would mint a new identity and every appliance in the field is pinned to the old one's public half. The
   **public** key goes to appliances via `deploy/pki/vendor-license.pub` or
   `install-vendor-trust-key.sh`; the private half never leaves this host.
4. `/etc/stayconnect/ctrlapi.env` from
   [`deploy/env/ctrlapi.env.example`](../deploy/env/ctrlapi.env.example), then
   **`deploy/scripts/install-central-endpoint.sh`** — it installs the versioned endpoint to
   `/etc/stayconnect/central-endpoint.env`, which the unit reads last so the fleet-wide value wins over any
   hand-edit on this host. Without an appliance base in either place, offline first activation is silently
   disabled.
5. Assignment signing and registry keys (`ctrlapi gen-assignment-key`, `ctrlapi gen-registry-key`), then
   ctrlapi (`ctrlapi serve`), then `ctrlapi seed-admin` for the first platform admin.
6. Caddy vhosts; cloud-admin.
7. Observability stack; backup cron; alert-delivery test.
8. **`deploy/scripts/central-mint-tls.sh`** — issues Central's public certificate covering the
   appliance-facing FQDN. The certificate an appliance validates must carry the name it was told to dial;
   if it does not, the appliance's registration fails verification and Hotel Admin shows only *Not
   registered yet* with the last problem.
   Pass `CENTRAL_TLS_SANS` for any address still in use during a transition. Then restart Caddy.
9. **`deploy/scripts/central-preflight.sh`** — checks the four things that otherwise fail silently at a
   hotel: endpoint set and matching the versioned config, vendor key present with the right permissions,
   assignment key present and distinct, schema fully migrated.
10. Smoke: `GET /readyz` (reports the version), sign in to the console, activate a staging appliance and
   verify it reaches **Activated** with its licence.

## 5. Operational duties

- **Activation and licences**: a platform admin in the console; renewals
  before `valid_until` (appliances re-fetch on their own; GracePeriod covers
  late renewals — [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md)).
- **Fleet watch**: the console's **Overview** (*Needs attention*: waiting,
  licences expiring/in grace/expired/suspended, appliances offline, open
  security alerts, unconfirmed retirements) + Grafana for the host itself.
- **Signing keys**: state changes are host commands —
  `ctrlapi assignment-key verify-only|revoke --key-id <id> --reason <text> [--emergency]`.
- **Upgrades**: ctrlapi is stateless — deploy, migrate
  (`deploy/scripts/central-migrate.sh up`), restart; appliances are unaffected
  (they retry and keep serving guests).

## 6. Pilot topology (HISTORICAL)

The pilot ran cloud **and** one edge on a single VM with two databases
(`stayconnect`, `stayconnect_site`) and separate credentials. **Superseded:**
Central now runs on its own host (CLAUDE.md §0D) and the appliance on its own;
see §1.

## 7. Failure modes and their blast radius

| Failure | Effect on hotels | Effect on cloud users | Recovery |
|---|---|---|---|
| ctrlapi down | none (license fetch retries with backoff) | cloud-admin unusable | redeploy — stateless |
| Postgres down | none | everything cloud down | restore/replica failover; [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §2 |
| Redis down | none | operators logged out | restart — sessions are re-creatable |
| Vendor key lost | none until renewals are due | cannot issue licenses | restore from escrow, or rotate: ship new public key to appliances, re-issue |

The recurring answer in column two — "none" — is the acceptance test for the
whole refactor: no cloud failure may reach a guest.
