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

- `api.<domain>` → ctrlapi vhost (appliances need this reachable: license
  fetch is HTTPS with a real certificate — no `local_certs` in production).
- `admin.<domain>` → cloud-admin vhost.
- NATS endpoint (`nats.<domain>:4222`) with TLS; appliance config pins it.

## 4. Bring-up order

1. Postgres (+ timescaledb extension) → apply `control-plane/migrations/0001..0019`.
2. Redis, NATS cluster.
3. Generate the vendor key once: store per `CTRLAPI_VENDOR_KEY`, escrow the
   encrypted copy, distribute the **public** key into the appliance image.
4. ctrlapi (`ctrlapi serve`), then `ctrlapi seed-admin` for the first
   platform_admin.
5. Caddy vhosts; cloud-admin.
6. Observability stack; backup cron; alert-delivery test.
7. Smoke: `readyz`, `GET /cloud/v1/version`, issue a test license against a
   staging tenant, verify an appliance can fetch it.

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
