# Deployment — Cloud

> Production layout for the StayConnect cloud (the vendor/commercial half).
> Pilot exception: cloud and one edge share a single VM with separate Postgres
> databases and credentials — see the note in §6. Appliance counterpart:
> [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md).

## 1. Components

```
                 Internet
                    │ :443
              ┌─────▼─────┐
              │   Caddy   │  TLS (ACME), security headers
              └──┬─────┬──┘
      admin.stayconnect.example   api.stayconnect.example
              │           │
     ┌────────▼───┐   ┌───▼──────────┐        ┌──────────────┐
     │ cloud-admin│   │   ctrlapi    │───────▶│  Postgres +  │
     │  (Next.js) │   │ /cloud/v1 +  │        │ TimescaleDB  │
     └────────────┘   │ appliance    │        └──────────────┘
                      │ protocol /v1 │───────▶ Redis (sessions)
                      └───────▲──────┘
                              │ consume telemetry.>, hb.*
                      ┌───────┴──────┐
     appliances ─────▶│ NATS cluster │ :4222 (TLS, per-appliance creds)
     (outbound only)  │  (3 nodes)   │
                      └──────────────┘
     Prometheus / Grafana / Alertmanager · backup cron · secrets
```

| Component | Sizing / notes |
|---|---|
| **ctrlapi** | stateless Go binary; 1 replica until the appliance-JWT replay cache moves to Redis ([SECURITY_HARDENING.md](SECURITY_HARDENING.md) §7); env: `CTRLAPI_DB_URL`, `CTRLAPI_REDIS_URL`, `CTRLAPI_NATS_URL`, `CTRLAPI_VENDOR_KEY`, `CTRLAPI_COOKIE_SECURE=true`, `CTRLAPI_ALLOW_ORIGINS=https://admin.<domain>` |
| **cloud-admin** | Next.js served centrally; `/api` proxy → ctrlapi |
| **Postgres + TimescaleDB** | the `stayconnect` DB ([DATA_OWNERSHIP.md](DATA_OWNERSHIP.md) §2); hypertables: fleet_telemetry (7d chunks), audit_log; loopback/VPC-only |
| **Redis** | operator sessions (`sc:sess:*`, 12h sliding); later: shared JWT replay cache |
| **NATS cluster** | 3 nodes, JetStream on, TLS, **per-appliance credentials** scoped to `telemetry.<id>`, `hb.<id>`, `scd.<id>.>`, plus the ctrlapi account for `config.*` publishes. NATS was already a SPOF note in the HA review — the cluster fixes that |
| **Caddy** | ACME (public DNS), HSTS/CSP as in `deploy/caddy/`; only :443 (and :80 for ACME) exposed |
| **Observability** | Prometheus (ctrlapi metrics, NATS/PG/Redis exporters), Grafana **behind Caddy**, Alertmanager → SendGrid (never the Gmail relay — [SECURITY_HARDENING.md](SECURITY_HARDENING.md) §1) |
| **Backups** | nightly `pg_dump -Fc` + off-host copy ([BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §2) |
| **Secrets** | vendor Ed25519 signing key (0600 file, CA-grade handling, encrypted escrow); DB/Redis/NATS credentials; SendGrid API key. Env files 0600 root-owned, or a proper secrets manager |

## 2. Network exposure

| Port | Exposure |
|---|---|
| 443 (Caddy) | public — cloud-admin + `/cloud/v1` + appliance license fetch |
| 4222 (NATS TLS) | public but credentialed — appliance outbound connections terminate here |
| 5432 / 6379 / 8080 / 3000 / 9090 / 3001 / 9093 | **never public** — loopback or private VPC only |

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
- NATS endpoint (`nats.<domain>:4222`) with TLS; appliance config pins it.

An `/etc/hosts` entry on an appliance is a stopgap for the hours before DNS propagates. It is never the
product's dependency: it exists on one machine and the next deployment will not have it. Provisioning says
so out loud when it finds one.

## 4. Bring-up order

1. Postgres (+ timescaledb extension).
   **Schema: `deploy/scripts/central-migrate.sh up`** — never a hand-typed range of files. It keeps a
   `schema_migrations` ledger, so a fresh install and an upgrade reach the same schema and neither one can
   skip a migration a feature depends on. On a Central that predates the ledger, adopt its history once
   (`central-migrate.sh adopt --through <last-applied> --yes`) and then run `up`.
2. Redis, NATS cluster.
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
5. ctrlapi (`ctrlapi serve`), then `ctrlapi seed-admin` for the first platform_admin.
6. Caddy vhosts; cloud-admin.
7. Observability stack; backup cron; alert-delivery test.
8. **`deploy/scripts/central-preflight.sh`** — checks the four things that otherwise fail silently at a
   hotel: endpoint set and matching the versioned config, vendor key present with the right permissions,
   assignment key present and distinct, schema fully migrated.
9. Smoke: `readyz`, `GET /cloud/v1/version`, issue a test license against a staging tenant, verify an
   appliance can fetch it.

## 5. Operational duties

- **License issuance/renewal**: platform_admin via cloud-admin; renewals
  before `valid_until` (appliances re-fetch on their own; GracePeriod covers
  late renewals — [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md)).
- **Fleet watch**: `/cloud/v1/fleet` + Grafana; alert on missing heartbeats,
  dead-letter growth (`sync` kind), `license_ack` states ≠ Active.
- **Telemetry hygiene**: retention job on `fleet_telemetry` chunks;
  `fleet_telemetry_dedupe` pruned in step (keep dedupe ≥ telemetry retention
  to preserve idempotency).
- **Upgrades**: ctrlapi is stateless — deploy, migrate, restart; appliances
  are unaffected (outbox buffers through the blip).

## 6. Pilot topology (accepted deviation)

One VM hosts cloud **and** one edge: a single Postgres instance with two
databases (`stayconnect`, `stayconnect_site`), **separate DSNs and
credentials**, ctrlapi + NATS + Redis + observability alongside the edge
daemons. Isolation is per-database; moving the cloud to its own host later is
a topology change (new DSN/NATS endpoints in appliance config), not a code
change. All §2 exposure rules still apply on the VM — see the open items in
[SECURITY_HARDENING.md](SECURITY_HARDENING.md) §2/§3/§6.

## 7. Failure modes and their blast radius

| Failure | Effect on hotels | Effect on cloud users | Recovery |
|---|---|---|---|
| ctrlapi down | none (license fetch retries with backoff) | cloud-admin unusable | redeploy — stateless |
| NATS cluster degraded | none guest-facing; telemetry queues in outboxes | fleet view goes stale | restore quorum; edges re-drain, dedupe absorbs replays |
| Postgres down | none | everything cloud down | restore/replica failover; [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §2 |
| Redis down | none | operators logged out | restart — sessions are re-creatable |
| Vendor key lost | none until renewals are due | cannot issue licenses | restore from escrow, or rotate: ship new public key to appliances, re-issue |

The recurring answer in column two — "none" — is the acceptance test for the
whole refactor: no cloud failure may reach a guest.
