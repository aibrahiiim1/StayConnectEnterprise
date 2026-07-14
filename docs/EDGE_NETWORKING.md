# Edge Networking (Phase 19)

> How the appliance turns hotel intent — "give me a guest VLAN 20 on 10.20.0.0/22
> with DHCP and a captive portal" — into live netplan/Kea/nftables/Unbound
> configuration, transactionally and without losing management connectivity.
> Model & renderers: `data-plane/internal/netcfg/`. Schema:
> `data-plane/migrations/0002_edge_networking.up.sql`. Design rationale:
> [PHASE19_ASSESSMENT.md](PHASE19_ASSESSMENT.md).

Before Phase 19 the appliance hardcoded exactly one guest network (`br-lan`,
`10.10.0.0/24`) in every layer. Phase 19 makes the **site database the source of
truth** for any number of guest networks and VLANs, and renders the OS files
from it.

## 1. The network hierarchy

```
Site  (one row in `sites`)
 └─ Appliance  (`appliances`; the box or HA pair)
     ├─ Interfaces        (`network_interfaces` — ens160, ens192, bond0 …)
     └─ Guest Networks    (`guest_networks` — one L2/L3 guest domain each)
         ├─ DHCP Pools        (`dhcp_pools` — 1..N ranges per subnet)
         ├─ DHCP Reservations (`dhcp_reservations` — MAC → fixed IP)
         └─ (served by) Config Revisions (`network_config_revisions`)
```

Every mutation to the guest-network intent is captured and applied as a numbered
**revision** with a full rendered bundle on disk. Revisions carry the audit trail
(`network_apply_events`, `network_health_checks`).

## 2. Interface roles

`network_interfaces.role` (CHECK-constrained) classifies each discovered NIC:

| Role | Meaning | Editable in this release |
|---|---|---|
| `management` | Hotel Admin / SSH / sync interface | **protected** (read-only, guarded) |
| `wan` | internet uplink; masquerade egress | **protected** (read-only, guarded) |
| `guest_access` | untagged guest access port (single guest LAN) | yes |
| `guest_trunk` | 802.1Q trunk carrying tagged guest VLANs from the WLAN controller | yes |
| `ha_sync` | dedicated link between the HA pair (VRRP/conntrackd/replication) | protected |
| `unused` | discovered but unassigned (default) | yes |

`mode` is one of `auto | manual | trunk | bridge_slave`. `is_protected=true`
marks management/WAN so a guest-network apply can never touch them. netd
*observes* `link_state`, `speed_mbps`, `mtu`, `driver`, `ip_addresses`,
`last_seen_at` — these columns are never authoritative for intent, only
refreshed.

## 3. The `netd` privileged daemon

Guest-network apply requires root (create bridges/VLANs, drive nftables, write
Kea via the control socket). Rather than make scd/portald/acctd root, Phase 19
adds one new privileged surface:

- **`netd`** listens only on the Unix socket `/run/stayconnect/netd.sock` (root,
  group `stayconnect`, mode 0660). Never on a TCP port.
- **`edged`** (unprivileged, the Hotel Admin API) proxies the operator's
  `/edge/v1/network/*` calls to netd over that socket — exactly the `scdClient`
  Unix-socket proxy pattern edged already uses for live session control.
- netd owns render → validate → snapshot → apply → health-check → confirm/rollback.
  The pure "what to render" logic lives in `internal/netcfg` (no side effects, unit
  tested); netd owns the "how to apply" side effects.

## 4. Source-of-truth model

```
 Site DB (guest_networks + dhcp_pools + dhcp_reservations + network_interfaces)
        │  netd loads intent → []netcfg.GuestNetwork + netcfg.Topology
        ▼
 internal/netcfg renderers (pure)
        │  RenderNetplan / RenderKeaDhcp4 / RenderNftables / RenderUnbound
        ▼
 /etc/stayconnect/generated/network/revision-NNNNNN/   (the rendered bundle)
        │  netd applies each artifact through its native online path
        ▼
 netplan generate/apply · Kea control socket · nft -f · unbound-control reload
```

The static `deploy/nftables/stayconnect.nft`, `deploy/kea/…`, `deploy/netplan/…`
files become **bootstrap skeletons**; the live configuration is the generated
bundle for the currently `active` revision. The legacy `br-lan` network is
imported at migration time as the first guest network (`Legacy Guest Network`,
untagged, 10.10.0.0/24), marked already-active, so nothing re-applies.

### Component diagram

```
 operator (Hotel Admin UI, mgmt IP)
        │ HTTPS (Caddy, mgmt only)
        ▼
   edged  ──proxy──▶  netd  (/run/stayconnect/netd.sock, root)
                        │
        ┌───────────────┼──────────────────┬─────────────────┐
        ▼               ▼                  ▼                 ▼
   netplan          Kea control        nftables           unbound
  generate/apply    socket (config-    (nft -f generated  (-control
  (vlans+bridges)   test/set/write)    ruleset)           reload)
```

## 5. `/edge/v1` route surface (Phase 19)

edged mounts these under the existing per-resource permission model (see
[ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md) §3). All writes proxy to
netd; reads may be served from the site DB.

**Interfaces & topology** (`network.interfaces`)

| Method & path | Purpose |
|---|---|
| `GET /edge/v1/network/interfaces` | list discovered NICs + observed state |
| `GET /edge/v1/network/interfaces/{name}` | one interface |
| `PUT /edge/v1/network/interfaces/{name}/role` | assign role (guest_access/guest_trunk/unused); protected roles refused |

**Guest networks** (`network.guest`)

| Method & path | Purpose |
|---|---|
| `GET /edge/v1/guest-networks` | list guest networks |
| `POST /edge/v1/guest-networks` | create (draft) |
| `GET /edge/v1/guest-networks/{id}` | detail incl. pools + reservations |
| `PUT /edge/v1/guest-networks/{id}` | edit |
| `DELETE /edge/v1/guest-networks/{id}` | remove |
| `POST /edge/v1/network/validate` | `netcfg.ValidateSet` + `kea config-test` + `nft -c` + `netplan generate`; returns structured issues, no apply |
| `POST /edge/v1/network/apply` | render a revision, snapshot, apply, health-check → `pending_confirmation` |
| `POST /edge/v1/network/confirm` | confirm the in-flight revision → `active` (stops the watchdog) |
| `POST /edge/v1/network/rollback` | roll back to the previous known-good revision |
| `GET /edge/v1/network/revisions` | revision history + lifecycle |
| `GET /edge/v1/network/revisions/{seq}` | one revision incl. events + health checks |
| `GET /edge/v1/network/health` | current health-check summary |

**DHCP** (`network.dhcp`)

| Method & path | Purpose |
|---|---|
| `GET /edge/v1/dhcp/{networkId}/pools` · `POST` · `PUT` · `DELETE` | pools |
| `GET /edge/v1/dhcp/{networkId}/reservations` · `POST` · `PUT` · `DELETE` | reservations |
| `GET /edge/v1/dhcp/{networkId}/leases` | live leases via Kea `lease4-get-all` |

> Implementation note: `internal/netcfg` (model, validator, all four renderers)
> and the migration schema are implemented and unit-tested. The netd daemon and
> the edged proxy routes above are the wiring delivered in this phase; DHCP
> **relay** mode is supported in the schema but minimal in this release
> (surfaced only where it can be tested).

## 6. Related documents

- [GUEST_VLAN_CONFIGURATION.md](GUEST_VLAN_CONFIGURATION.md) — create untagged & VLAN networks
- [DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md) — pools, reservations, validation codes
- [DHCP_OPTION_114.md](DHCP_OPTION_114.md) — RFC 8910 captive-portal option
- [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md) — the transactional lifecycle
- [ARUBA_SSID_VLAN_MAPPING.md](ARUBA_SSID_VLAN_MAPPING.md) — the WLAN-controller boundary
- [EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md) — when the hotel runs its own DHCP
- [NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md) — symptom → check → fix
