# Edge Appliance Architecture

> Everything that runs at the hotel. The appliance is self-sufficient: guests
> authenticate, browse, get shaped and accounted against a **site-local
> database**, with the cloud completely optional at runtime.
> Cloud counterpart: [CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md).

## 1. Daemons

| Daemon | Listens | Role |
|---|---|---|
| `scd` | Unix socket `/run/stayconnect/scd.sock` (+ loopback `/metrics`) | Session controller: owns nft `auth_ipv4` set, tc classes, sessions table; validates vouchers/OTP/social/PMS; PMS provider registry; reaper. **Also hosts the sync agent**: outbox drain to NATS, periodic license fetch, config-push subscriber, heartbeat |
| `portald` | `:8380` HTTP / `:8343` HTTPS on the guest interface | Captive portal front end; no DB, no business logic — proxies to scd over the socket |
| `acctd` | none (1s tick) | tc byte-counter snapshots → `accounting_records`, quota enforcement via scd |
| `edged` | loopback, fronted by Caddy on the **management IP** | The Hotel Admin API (`/edge/v1`): local operator auth, guest-domain CRUD, license status, reports, backups. Serves the `hotel-admin/` UI bundle |

All four read/write only the local DB. The single cloud-touching component is
the sync agent inside scd (outbound NATS + outbound HTTPS license fetch).

## 2. Site-local database (`stayconnect_site`)

One hotel = one isolated Postgres database, schema
`data-plane/migrations/0001_edge_init.up.sql` — intentionally shape-compatible
with the guest-domain subset of the central schema so scd/portald/acctd cut over
by changing only their DSN and `sitemigrate` copies rows 1:1.

| Domain | Tables |
|---|---|
| Site identity & portal config | `tenants` (exactly ONE row: auth_methods, `branding` jsonb), `sites` (one row), `appliances` (this site's box/pair) |
| Local operators | `operators`, `operator_roles` — the seven site roles: `site_admin`, `hotel_it_manager`, `front_office_operator`, `guest_relations_operator`, `voucher_operator`, `payments_operator`, `site_viewer` (see [ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md)) |
| Guest access | `ticket_templates` (**GuestAccessPlan**), `voucher_batches`, `vouchers`, `guests`, `sessions`, `accounting_records`, `auth_otps`, `social_oauth_states` |
| PMS | `pms_providers`, `pms_attempts` |
| Policy | `walled_garden_rules` |
| Providers & payments | `notification_providers`, `social_oauth_providers`, `stripe_accounts`, `payments`, `stripe_events` |
| Compliance | `audit_log` (local; hotel actions stay at the hotel) |
| Entitlements bridge | `tenant_effective_limits` — a **plain table** (the cloud version is a view) rewritten by scd/edged from the verified signed license. Existing limit queries (`session.CheckConcurrency`, provisioning caps) work unchanged; source of truth is the license file, never a cloud DB |
| Sync & ops | `sync_outbox` (seq identity, attempts, next_attempt_at, dead, last_error), `sync_checkpoints` (named jsonb checkpoints), `backup_records` |

`accounting_records` and `audit_log` become hypertables when TimescaleDB is
installed (pilot), plain indexed tables otherwise.

## 3. `/edge/v1` API (edged)

Authentication: local operator session (argon2id + cookie, same model as
ctrlapi). Role gates per [ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md).
Provisioning writes are additionally gated by license state
([LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md)).

| Resource | Routes | Purpose |
|---|---|---|
| health | `GET /edge/v1/health` | daemon + DB + license-state summary |
| license | `GET /edge/v1/license` (status/evaluation), `POST /edge/v1/license` (manual envelope upload — offline renewal path) | license page |
| operators | CRUD + set-password/roles | local hotel staff |
| guest-access-plans | CRUD (`ticket_templates`) | duration/caps/bandwidth/price |
| voucher-batches | list/create/detail/codes/CSV/revoke | |
| vouchers | get/revoke | |
| sessions | list/get/disconnect | live disconnect via scd socket |
| pms-providers | CRUD + test/cache/health | live against local scd |
| auth-methods | GET/PUT | portal tab switchboard (local `tenants.auth_methods`) |
| walled-garden | CRUD + effective | |
| portal-branding | GET/PUT | `tenants.branding` (logo, T&C, languages) |
| payments | list, refund | Stripe history |
| stripe-accounts | CRUD | secrets write-only |
| notification-providers | CRUD | email/SMS |
| social-providers | CRUD | OAuth creds |
| audit | GET | local audit_log |
| reports | GET | usage/auth/revenue summaries from local data |
| backups | list/trigger (`backup_records`) | see [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) |

## 4. Network interfaces

| Interface | Example | Carries |
|---|---|---|
| **Management** | `172.21.15.30/24` on the hotel's IT/management VLAN | Hotel Admin (`https://172.21.15.30` via Caddy), SSH, outbound sync (NATS + license HTTPS), monitoring |
| **Guest gateway** | `10.20.0.1/24` on the guest bridge | Kea DHCP (+ RFC 8910 option 114 → `http://10.20.0.1:8380/`), Unbound DNS, nftables captive DNAT :80→8380/:443→8343, portald, tc shaping, masquerade to uplink |
| **HA sync** (optional) | dedicated link/VLAN between the pair | VRRP adverts, conntrackd FTFW, Postgres streaming replication |

nftables retains the `inet stayconnect` table (auth_ipv4 / walled_garden_ip
sets, drop-by-default input/forward). Refactor tightening: the dev-era WAN
accepts for :8080/:3000 are removed, and — because the DNAT captive redirect is
**IPv4-only** while the inet table would otherwise forward authenticated v6 —
**IPv6 is dropped on the guest LAN** until v6 capture is implemented
([SECURITY_HARDENING.md](SECURITY_HARDENING.md)).

## 5. Caddy exposure rules

Caddy on the appliance is the only TLS terminator:

| vhost / bind | Upstream | Rule |
|---|---|---|
| `https://<mgmt-ip>` (e.g. `https://172.21.15.30`) | hotel-admin UI + `/edge/v1` → edged | **Management interface only.** Never bound to the WAN/uplink or the guest bridge. Internal CA (`local_certs`) until sites have real names |
| guest portal | portald `:8380/:8343` | Guest interface only; portal HTTP stays direct-to-portald for RFC 8910 (no HTTPS redirect on the captive path) |

Hotel Admin is therefore reachable exclusively from the hotel's management
network — not from guest devices and not from the internet. Cloud operators who
need site data see it through fleet telemetry, never through a direct connection
to the appliance.

## 6. Sync agent (inside scd)

- **Outbox writer**: every reportable local event (heartbeat, health snapshot,
  usage rollup, auth counters, PMS health, license ack, backup result, sync
  stats, update progress) is a row in `sync_outbox`.
- **Drain loop**: publishes pending rows to `telemetry.<applianceID>` in `seq`
  order via NATS request/reply; marks `sent_at` only on `Nats-Status: 200`;
  otherwise exponential backoff via `next_attempt_at`, `dead=true` after the
  retry budget. Details: [SYNC_PROTOCOL.md](SYNC_PROTOCOL.md).
- **License refresh**: periodic `GET /v1/appliance/license` (Ed25519 appliance
  JWT); on success installs the envelope (rollback-protected) and calls
  `MarkCloudValidated`.
- **Inbound**: `config.<tenantID>.pms` reload events and revocation data
  (embedded in license fetch responses). Both tolerate arbitrarily long outages.

## 7. HA pair specifics

Data path HA is unchanged from phase 5.5 (keepalived VRRP on the guest VIP,
conntrackd, nft `auth_ipv4` replication over `nft.<siteID>`, boot reconcile from
`sessions`). New: the site DB runs on the primary with **streaming replication**
to the secondary; edged and scd on the secondary point at the local replica and
promote it on failover. Split-brain risk and the recommended cloud-heartbeat
witness are documented in [TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md) §6 —
a known limitation, witness not yet implemented.

## 8. Phase 19 — Networking

Phase 19 makes the site DB the source of truth for the appliance's guest
networks and VLANs; the OS files become rendered artifacts. Full suite:
[EDGE_NETWORKING.md](EDGE_NETWORKING.md).

### `netd` — the privileged network daemon

| Daemon | Listens | Role |
|---|---|---|
| `netd` | Unix socket `/run/stayconnect/netd.sock` (root, group `stayconnect`, 0660; **never a TCP port**) | Renders netplan/Kea/nftables/Unbound from the site DB, validates, applies transactionally, health-checks and rolls back. The only new privileged surface — scd/portald/acctd stay unprivileged. `edged` proxies `/edge/v1/network/*` to it (same Unix-socket proxy pattern as scd). |

Pure render/validate logic lives in `data-plane/internal/netcfg/` (model,
validator, `RenderNetplan`/`RenderKeaDhcp4`/`RenderNftables`/`RenderUnbound`);
netd owns the side effects.

### Networking data flow

```
Site DB (guest_networks + dhcp_pools + dhcp_reservations + network_interfaces)
   → netd loads intent → internal/netcfg renders a revision bundle under
     /etc/stayconnect/generated/network/revision-NNNNNN/
   → apply: netplan generate/apply · Kea config-test/set/write (control socket,
     no restart) · nft -c/-f · unbound-control reload
   → health checks (mgmt_reachable/gateway_up/kea_running/portal_listen/dns)
   → pending_confirmation (120 s watchdog) → active | rolled_back
```

nftables becomes generated per revision: concatenated `auth_ipv4`
(`ifname . ipv4_addr`), dynamic `guest_interfaces`/`guest_subnets` sets,
per-network captive DNAT and masquerade, inter-guest isolation. Sessions gain
`guest_network_id` / `vlan_id` / `ingress_interface` / `gateway_ip`; scd derives
the guest network from the source IP's subnet.

### New site tables (`0002_edge_networking`)

| Domain | Tables |
|---|---|
| Interface inventory | `network_interfaces` (role `management`/`wan`/`guest_access`/`guest_trunk`/`ha_sync`/`unused`; observed link/speed/mtu) |
| Guest networks | `guest_networks` (untagged or 802.1Q; gateway/subnet; dhcp_mode `local`/`external`/`relay`/`disabled`; portal/NAT/isolation flags) |
| DHCP | `dhcp_pools` (N ranges per subnet), `dhcp_reservations` (MAC → fixed IP) |
| Transactional apply | `network_config_revisions` (draft→validated→applying→pending_confirmation→active/failed/rolled_back/superseded), `network_apply_events`, `network_health_checks` |
| Session association | `sessions` gains `guest_network_id`/`vlan_id`/`ingress_interface`/`gateway_ip` |

Management/WAN interfaces are `is_protected` and read-only in this release; guest
interfaces/VLANs are fully configurable. DHCP `relay` is supported in the schema
but minimal in this release.
