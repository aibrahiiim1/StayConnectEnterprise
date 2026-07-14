# Sync Protocol — Edge ⇄ Cloud

> How an appliance talks to the cloud: a durable outbox drained over
> appliance-initiated channels, deduplicated cloud-side for exactly-once
> landing. The appliance opens **outbound connections only** — NATS over the
> existing channel, plus HTTPS for the license fetch.

## 1. Channels

| Direction | Channel | Content |
|---|---|---|
| Edge → Cloud | NATS publish (request/reply) on `telemetry.<applianceID>` | outbox telemetry messages |
| Edge → Cloud | NATS `hb.<applianceID>` (10s) | legacy liveness heartbeat (retained) |
| Edge → Cloud | HTTPS `GET /v1/appliance/license` | license fetch (doubles as cloud validation) |
| Cloud → Edge | NATS `config.<tenantID>.pms` | config-change events (queue group — one HA node reloads) |
| Cloud → Edge | license fetch **response** | signed envelope, `revoked[]` license ids, `server_time` |
| Cloud → Edge | NATS `scd.<applianceID>.>` | admin RPC (revoke session, PMS test/cache/health) — pre-existing, unchanged |

The NATS client connection is opened *from* the site; the cloud never dials in.

## 2. Outbox pattern (edge side)

Every reportable event is written to the site DB's `sync_outbox` in the same
transaction as (or immediately after) the local state change it reports:

```sql
sync_outbox(seq IDENTITY PK, kind, payload jsonb, created_at,
            sent_at, attempts, next_attempt_at, dead, last_error)
-- partial index on next_attempt_at WHERE sent_at IS NULL AND dead = false
```

`seq` is a gapless-enough monotonic identity and is **the idempotency key** —
the cloud dedupes on `(appliance_id, seq)`. The drain loop in scd:

1. Select pending rows (`sent_at IS NULL AND dead=false AND next_attempt_at <= now()`)
   in `seq` order.
2. Publish each as a NATS **request** on `telemetry.<applianceID>`; wait for the
   reply's `Nats-Status` header.
3. `200` → set `sent_at` (a replayed duplicate also gets `200`, so retries
   converge). `400`/`404` → permanent: mark `dead=true` with `last_error`
   (malformed or unknown appliance — retrying cannot help). Timeout / `500` →
   increment `attempts`, set `next_attempt_at = now() + backoff`.
4. **Backoff**: exponential, `min(2^attempts × 5s, 15min)`, with jitter. After
   the retry budget (attempts ≥ 20 ≈ several hours at cap) the row is flagged
   `dead=true` for operator attention — dead rows are visible in Hotel Admin
   and in the `sync` telemetry kind, and can be re-queued manually.
5. Sent rows are pruned after a retention window; `sync_checkpoints` records
   the last drained seq (`last_drain`) and last successful license fetch
   (`last_license_fetch`).

An unreachable cloud simply grows the outbox; nothing guest-facing waits on it.

## 3. Message format

```json
{
  "appliance_id": "a1b2c3d4-...-appliance-uuid",
  "seq": 4711,
  "kind": "health",
  "ts": "2026-07-11T10:15:00Z",
  "payload": { "scd": "ok", "portald": "ok", "db": "ok", "disk_free_pct": 71 }
}
```

- `kind` ∈ `heartbeat | health | usage | auth_counts | pms_health |
  license_ack | backup | sync | update_progress` (cloud rejects others).
- `payload` is flat-by-contract aggregate data — **no guest PII**
  ([DATA_OWNERSHIP.md](DATA_OWNERSHIP.md) §4).
- `ts` is the edge event time; the cloud replaces missing/far-future (>24h)
  timestamps with its own clock so skewed appliances cannot poison the
  hypertable index.

## 4. Idempotency / dedupe (cloud side)

`fleet.Consumer` processes each message as:

```
subject identity check ─▶ kind/seq validation ─▶ registry lookup (tenant/site)
  ─▶ INSERT fleet_telemetry_dedupe(appliance_id, seq) ON CONFLICT DO NOTHING
        ├─ conflict  → ack 200 (already landed — replay swallowed)
        └─ inserted  → sanitize payload → INSERT fleet_telemetry
                           ├─ ok   → ack 200
                           └─ fail → DELETE dedupe row, ack 500 (retry can land)
```

Properties:

- **Exactly-once landing**: dedupe-first insert plus compensating delete on
  telemetry-insert failure means a row lands once or the edge keeps retrying.
- **Identity**: `appliance_id` must match the subject suffix — an appliance can
  only speak for itself; tenant/site come from the cloud registry, never the
  payload.
- **Replay safety**: after any cloud outage the edge re-sends everything
  unacked; duplicates are dropped at the dedupe gate. Old seqs re-published
  maliciously are equally inert.

## 5. Ordering and clock skew

- The edge drains in `seq` order but the cloud does **not** require ordering —
  dedupe is per-seq, and consumers of `fleet_telemetry` sort by `ts`.
- Cross-machine clock skew is tolerated: edge `ts` is advisory, the far-future
  guard caps it, and license evaluation uses only the edge's own
  rollback-protected clock (plus `server_time` in the license response as an
  advisory reference — see [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) §7).

## 6. License fetch flow

```
scd sync agent (periodic, e.g. every 6h, and on push notice / operator request)
  │  GET https://<cloud>/v1/appliance/license
  │  Authorization: Bearer <Ed25519 JWT, alg=EdDSA, jti nonce, ≤60s lifetime>
  ▼
ctrlapi: RequireAppliance (signature vs enrolled public key, replay cache)
  → licensing.CurrentEnvelopeForAppliance (site's current active/suspended license)
  → 200 {license_id, envelope, revoked: [...], server_time}
  │        404 if the site has no license issued yet
  ▼
edge: Store.Install(envelope, now)      — verify signature, issued_at monotonic
      Store.AddRevocation(each revoked) — persists revoked.json
      Store.MarkCloudValidated(now)     — resets CloudStale, bumps high-water
      rewrite tenant_effective_limits from the verified document
      enqueue outbox kind=license_ack {license_id, state}
```

Failure handling: any fetch failure is logged and retried with backoff; the
currently installed license keeps governing behavior. `CloudStale` trips only
when the last successful validation is older than `offline_grace_days`, and is
a warning only.

## 7. Config push (cloud → edge)

`config.<tenantID>.pms` remains the only push subject today: the cloud
publishes a change event after PMS provider mutations; scd (queue group
`scd-reload-pms`) reloads its provider registry from the **local** DB. The
event is a hint, not data — the 10-minute safety reload loop guarantees
eventual consistency if the event is missed. Post-cutover, PMS mutations happen
in Hotel Admin directly against the site DB, and this subject is only used for
cloud-initiated config scenarios. Additional push kinds (walled garden,
branding) follow the same pattern when needed.

## 8. Observability

- Edge: outbox depth, oldest pending age, dead count and last drain time are
  exported as scd metrics and reported in the `sync` telemetry kind.
- Cloud: ingest counters (accepted / duplicate / rejected / failed) in
  `ctrlapi_*` metrics; per-appliance last-seen via `hb.*` plus latest
  telemetry rows in `/cloud/v1/fleet`.
- Alerting: an appliance with growing dead-letter count or `CloudStale` set
  warrants a support look — see the fleet dashboard.
