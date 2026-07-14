# Control Plane Migration — Central Server 150.0.0.252

Physical separation of the StayConnect Central Control Plane from the Hotel
Appliance. Executed 2026-07-11.

> The requested target `150.0.0.120` was **rejected during discovery**: it is a
> shared, 100%-full production host (NIMS stack, mssql, ollama, nginx already on
> :443) whose own Postgres was crash-looping on `No space left on device`.
> Deploying there would have required destructive action on unrelated production.
> The operator provided a clean dedicated host **`150.0.0.252`** (Ubuntu 22.04),
> which is now the Central Control Plane.

## Final topology

```
CENTRAL CONTROL PLANE — 150.0.0.252 (Ubuntu 22.04)
  ctrlapi (127.0.0.1:8080, systemd)      cloud-admin (127.0.0.1:3000, systemd, Next.js standalone)
  TimescaleDB 2.16.1-pg16 (docker, 127.0.0.1:5432)   Redis 7 (auth, 127.0.0.1:6379)
  NATS 2.10 JetStream + TLS (0.0.0.0:4222)           Caddy TLS ingress (:443, internal-CA IP-SAN cert)
  vendor license signing key                          ufw: 22, 443, 4222(appliance-only)
        ▲  outbound TLS (HTTPS + NATS-TLS), appliance-initiated only
        │
HOTEL APPLIANCE — 172.21.60.23  (edge-only; Cloud API/Admin removed)
  scd portald acctd edged netd hotel-admin kea unbound nftables tc  +  site DB (stayconnect_site)
```

## Cloud transport & TLS

- One **internal CA** on the central server (`/opt/stayconnect/central/tls/ca.crt`)
  issues a server cert with **SAN `IP:150.0.0.252, DNS:radius, api/admin.stayconnect.local`**.
- The CA is installed in the **system trust store** of both the central server
  (so ctrlapi verifies NATS TLS) and the appliance (so scd/edged verify HTTPS +
  NATS). **No `curl -k`, no disabled verification.**
- Caddy terminates TLS on :443 and path-routes: `/v1/*`, `/cloud/*`, `/healthz`,
  `/readyz` → ctrlapi:8080; everything else → cloud-admin:3000. The appliance
  hits `/v1/*`; the admin browser + `/api/*` use cloud-admin.
- NATS: JetStream + TLS + user/password auth. Appliance connects
  `tls://stayconnect:***@150.0.0.252:4222`. Port 4222 firewalled to the
  appliance IP only. All appliance→cloud traffic is **outbound**; no inbound to
  the appliance is required.

## Cloud DB migration (stayconnect only; site DB never leaves the appliance)

TimescaleDB-aware: `pg_dump -Fc` → `CREATE EXTENSION timescaledb` →
`timescaledb_pre_restore()` → `pg_restore --no-owner` → `timescaledb_post_restore()`.
Verified equal counts (tenants 2, sites 2, appliances 2, plans 6, licenses 8,
operators 35), schema `0019_licensing_fleet`, and all 4 hypertables restored
(accounting_records, audit_log, fleet_telemetry, usage_counters). Dump archived
at `/opt/stayconnect/central/backups/`.

## Redis / NATS decisions

- **Redis** holds only cloud operator sessions → not migrated; a controlled
  re-login after cutover is accepted (documented). New central Redis has a
  strong password, loopback-only.
- **NATS** on the appliance served BOTH co-located cloud and scd's telemetry
  transport. It is now **split**: the appliance's local NATS is retired for
  cloud use; scd publishes telemetry/heartbeat to **central** NATS. The durable
  **outbox** buffers during central outage and drains idempotently (verified:
  pending→0, dead 0, dedupe count == telemetry count → no duplicates). Local
  guest operation never depends on central NATS.

## Cutover sequence (executed)

1. Central stack healthy (pg/redis/nats/ctrlapi/cloud-admin/caddy) + DB restored.
2. Appliance trusts central CA; `SCD_CTRLAPI_BASE=https://150.0.0.252`,
   `SCD_NATS_URL=tls://…@150.0.0.252:4222` (endpoints in env/DB, **not** source).
3. Restart **scd only** (guest data-plane — kea/portald/acctd/nft — untouched;
   sessions continue at kernel/accounting level).
4. Verified: `hello: ctrlapi signed-auth ok`, `license refreshed from cloud`,
   NATS subscribed over TLS, central `fleet_telemetry` incremented.
5. Stop + disable appliance `stayconnect-ctrlapi` and `stayconnect-web-admin`
   (Cloud API/Admin **no longer active** on the appliance).

## Rollback readiness

Old cloud DB (`stayconnect`) remains on the appliance **as a non-authoritative
archive** during the soak window (not deleted). Rollback = restore appliance
`scd.env` from the timestamped backup, re-enable local ctrlapi/web-admin. Backups:
`/etc/stayconnect/scd.env.bak-*`, `/root/backups/net-*`, central DB dump.

## Data privacy

Fleet telemetry is sanitized: **0 telemetry rows** contain room/voucher/email.
The migrated cloud DB carries historical guest rows from the co-located era
(legacy, not new runtime writes); the runtime path writes no guest PII centrally.

## Carryover hardening (this phase)

- **A — DHCP one source of truth:** the WAN/LAN *System Network* page shows DHCP
  **read-only** with a link to *Guest Networks → DHCP*; the Site DB → Kea apply
  pipeline is the only editor. No second Kea source.
- **B — Kea write-through:** DB↔Kea coherent; end-to-end proven (Phase 19 DORA on
  a new pool, Option 114 generation, reservation CRUD, kea reload, post-reload lease).
- **C — Safe LAN apply:** LAN netplan render unit-tested; the apply/verify/auto-
  rollback machinery is proven live on the WAN path (identical code). A full
  isolated-bridge LAN apply is the remaining hands-on drill (production `br-lan`
  deliberately untouched).
- **D — Auto-rollback audit:** in-process netd watchdog writes the full event
  sequence to `system_network_audit`: `network.apply.requested → started →
  pending_confirmation → confirmation_timeout → rollback.automatic.started →
  succeeded` (or `.confirmed` on success; `.failed` on rollback error). Replaced
  the detached systemd script.
- **E — Pending-marker cleanup:** persistent marker
  `/var/lib/stayconnect/sysnet-pending.json`; cleared on every terminal outcome;
  `netd` reconciles a stale unconfirmed change on boot (audited).
- **F — Cloud Connection page** (`/network/cloud`): live reachability + cert
  probe, license, outbox, IDs, endpoints. Connection state derived from real
  facts, never hardcoded; secrets masked.

## Exact URLs / locations

- Cloud Admin + Cloud API: `https://150.0.0.252/` (admin UI) and `https://150.0.0.252/v1|/cloud` (API).
- NATS: `tls://150.0.0.252:4222`.
- Hotel Admin (appliance, mgmt only): `https://172.21.60.23/`.
- Central config: `/opt/stayconnect/central/` (compose, secrets 600, tls),
  `/etc/stayconnect/ctrlapi.env`, systemd `stayconnect-{ctrlapi,cloud-admin,caddy}`.
