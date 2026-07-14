# Network Apply & Rollback (Phase 19)

> Every guest-network change is a **numbered revision** applied transactionally,
> with pre-apply gates, a snapshot, health checks, and a 120 s watchdog that
> auto-rolls-back if you don't confirm — so a bad change can never lock you out.
> Schema: `network_config_revisions` / `network_apply_events` /
> `network_health_checks` in `0002_edge_networking.up.sql`. Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

## 1. The lifecycle

`network_config_revisions.state` (CHECK-constrained) moves through:

```
draft ──validate──▶ validated ──apply──▶ applying ──health ok──▶ pending_confirmation
                                             │                          │
                                        health fail                confirm │ 120s watchdog
                                             ▼                     ┌───────┴────────┐
                                          failed                 active         rolled_back
                                                                    │
                                     a later apply supersedes ──▶ superseded
```

| State | Meaning |
|---|---|
| `draft` | intent captured, not yet validated |
| `validated` | passed all pre-apply gates (§3) |
| `applying` | artifacts being applied; snapshot taken |
| `pending_confirmation` | applied + healthy; **awaiting operator confirm** under the watchdog |
| `active` | confirmed and live (the known-good revision) |
| `failed` | a gate or apply step failed; nothing changed / rolled back |
| `rolled_back` | applied but reverted (health fail or watchdog timeout) |
| `superseded` | replaced by a newer active revision |

A partial unique index (`ncr_single_inflight`) guarantees **at most one**
revision is `applying`/`pending_confirmation` at a time — applies can't race.

## 2. The revision bundle

Each revision renders a full bundle under
`/etc/stayconnect/generated/network/revision-NNNNNN/` (NNNNNN = the `seq`
identity). It contains the four rendered artifacts and their inputs:

| File | Produced by | Applied via |
|---|---|---|
| `netplan.yaml` | `netcfg.RenderNetplan` | `netplan generate` + `netplan apply` |
| `kea-dhcp4.json` | `netcfg.RenderKeaFile` / `RenderKeaDhcp4` | Kea `config-test` → `config-set` → `config-write` |
| `stayconnect.nft` | `netcfg.RenderNftables` | `nft -c` (check) → `nft -f` |
| `unbound.conf` | `netcfg.RenderUnbound` | `unbound-control reload` |

The revision row also stores `intent` (a JSON snapshot of `guest_networks` +
pools at generation time), `validation` (structured results), `bundle_path`,
`previous_seq` (the revision this supersedes / rolls back to), and the actor
columns (`created_by` / `validated_by` / `applied_by` / `confirmed_by`).

## 3. Pre-apply validation gates

Apply refuses to proceed unless **all** gates pass (each recorded as a
`network_apply_events` row with `phase = validate`):

1. **`netcfg.ValidateSet`** — model validation: CIDRs, pools, reservations,
   timers, VLAN ranges, protected interfaces, cross-network subnet overlap and
   duplicate VLAN/bridge. Structured `{field, code, message}` issues; see the
   code table in [DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md) §5.
2. **`kea config-test`** — the rendered `Dhcp4` object is validated by Kea
   itself, no apply.
3. **`nft -c`** — the generated ruleset is syntax/consistency checked before load.
4. **`netplan generate`** — netplan renders backend config without applying, so a
   malformed device definition is caught early.

## 4. The snapshot

Before touching anything, netd captures a rollback point (`phase = snapshot`):
the previous known-good revision's bundle, the current running Kea config
(`config-get`), the live nftables ruleset, and current netplan. The
`previous_seq` link records exactly which revision to restore. **Management and
WAN interfaces are never part of the applied set** — the netplan renderer never
emits them, so a guest-network apply structurally cannot reconfigure the
management link.

## 5. Health checks

After apply (`phase = health`), netd runs the checks in
`network_health_checks.check_name`:

| Check | Verifies |
|---|---|
| `mgmt_reachable` | the management interface/route is still up and Hotel Admin is reachable — **the connectivity-protection check on every apply** |
| `gateway_up` | each new/changed guest gateway address is present and the bridge is up |
| `kea_running` | Kea answers `status-get` after `config-set` |
| `portal_listen` | portald is listening on the gateway `:8380`/`:8343` |
| `dns` | Unbound answers on each gateway after reload |

Any failed check → automatic rollback to `previous_seq`, state `rolled_back`,
`failure_reason` recorded. `mgmt_reachable` failing is the hard stop that
protects against lockout.

## 6. The 120 s watchdog + "confirm or it rolls back"

Even when every health check passes, the revision enters
**`pending_confirmation`**, not `active`. netd sets
`confirm_deadline = now() + 120s` and starts a watchdog:

- The operator must call `POST /edge/v1/network/confirm` within 120 s. On confirm
  the revision becomes `active`, the previous one `superseded`, and the watchdog
  stops.
- If the operator has **lost connectivity** because of the change (e.g. they were
  on a device that the change disrupted), they simply can't confirm — the
  watchdog fires at the deadline, restores `previous_seq`, and marks the revision
  `rolled_back`. The appliance returns to the last known-good state on its own.

This is the same "commit-confirmed" safety used by carrier routers: a change you
can't confirm is a change that undoes itself. The Hotel Admin UI shows a live
countdown and a big **Confirm** button after apply.

## 7. Manual rollback

`POST /edge/v1/network/rollback` re-applies the `previous_seq` bundle at any time
after a revision is `active`, following the same apply → health → confirm path in
reverse. Revision history and every apply event/health check are visible at
`GET /edge/v1/network/revisions` and `/revisions/{seq}`.

## 8. Where to look when it rolls back

- `network_apply_events` — the phase timeline (`validate|snapshot|generate|apply|
  health|commit|rollback`) with `ok` and a JSON `detail`.
- `network_health_checks` — which named check failed and why.
- `network_config_revisions.failure_reason` — the summary cause.

See [NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md) §"apply rolled back".
