# Guest VLAN & Network Configuration (Phase 19)

> How to add a guest network — untagged (a bridge straight over a parent port)
> or a tagged 802.1Q VLAN — and what the appliance renders for it. Model:
> `data-plane/internal/netcfg/model.go`; netplan renderer:
> `render_netplan.go`. Overview: [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

A guest network is one L2/L3 domain the appliance serves: its own bridge, L3
gateway, DHCP scope, DNS, firewall zone and captive-portal policy. Two shapes:

- **untagged** — the bridge enslaves the parent interface directly. Only **one**
  untagged network per parent interface (untagged frames aren't demuxable; the DB
  enforces this with a partial unique index `guest_networks_untagged_parent_uniq`).
- **vlan** — a `parent.<vlan>` 802.1Q sub-interface feeds the bridge. Many VLANs
  can share one trunk parent. `(parent_interface, vlan_id)` is unique among
  enabled networks (`guest_networks_vlan_parent_uniq`).

The `gn_vlan_consistency` CHECK enforces the rule both ways: a `vlan` network
must set `vlan_id`; an `untagged` network must not.

## 1. Bridge & VLAN device naming

netd derives names deterministically (`netcfg.BridgeNameFor`, `VLANIfaceName`),
kept within the Linux `IFNAMSIZ` limit of **15 characters**:

| Kind | Bridge name | Example | VLAN device |
|---|---|---|---|
| VLAN | `br-g<vlan>` | VLAN 20 → `br-g20` (max `br-g4094`, 7 chars) | `parent.<vlan>`, e.g. `ens192.20` |
| untagged | `br-g-<8hex of id>` | `br-g-1a2b3c4d` (13 chars) | none (parent enslaved directly) |

`VLANIfaceName` caps the parent so `parent.<vlan>` stays ≤ 15 (a long parent name
is truncated before the `.vlan` suffix).

## 2. Worked example — untagged guest network

Parent `ens192` is a plain guest access port (role `guest_access`). One untagged
network, gateway `10.30.0.1/24`:

| Field | Value |
|---|---|
| `network_type` | `untagged` |
| `parent_interface` | `ens192` |
| `vlan_id` | *(null)* |
| `bridge_name` | `br-g-<hash>` (generated) |
| `gateway_ip` / `subnet_cidr` | `10.30.0.1` / `10.30.0.0/24` |

Rendered netplan (bridge enslaves the parent directly; parent stays L2, no IP):

```yaml
network:
  version: 2
  renderer: networkd
  bridges:
    br-g-1a2b3c4d:
      interfaces: [ens192]
      addresses:
        - 10.30.0.1/24
      dhcp4: no
      dhcp6: no
      parameters:
        stp: false
        forward-delay: 0
```

## 3. Worked example — tagged VLAN 20 (Rooms)

Parent `ens192` is a `guest_trunk` carrying tagged VLANs from the WLAN
controller. Create VLAN 20 → `ens192.20` → `br-g20` → `10.20.0.1/22`, pool
`10.20.0.100 – 10.20.3.250`:

| Field | Value |
|---|---|
| `network_type` | `vlan` |
| `parent_interface` | `ens192` |
| `vlan_id` | `20` |
| `bridge_name` | `br-g20` |
| `gateway_ip` / `subnet_cidr` | `10.20.0.1` / `10.20.0.0/22` |
| pool | `10.20.0.100 – 10.20.3.250` |

Rendered netplan. The trunk parent is emitted as an address-less ethernet set
UP; the VLAN device links to it; the bridge carries the gateway:

```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    ens192:
      dhcp4: no
      dhcp6: no
      optional: true
  vlans:
    ens192.20:
      id: 20
      link: ens192
      dhcp4: no
      dhcp6: no
  bridges:
    br-g20:
      interfaces: [ens192.20]
      addresses:
        - 10.20.0.1/22
      dhcp4: no
      dhcp6: no
      parameters:
        stp: false
        forward-delay: 0
```

The gateway address is `gateway_ip` combined with the `subnet_cidr` prefix
length (`10.20.0.1` + `/22` → `10.20.0.1/22`).

## 4. Worked example — multiple VLANs on one trunk

VLAN 20 "Rooms" + VLAN 40 "Conference", both on `ens192`:

```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    ens192:
      dhcp4: no
      dhcp6: no
      optional: true
  vlans:
    ens192.20: { id: 20, link: ens192, dhcp4: no, dhcp6: no }
    ens192.40: { id: 40, link: ens192, dhcp4: no, dhcp6: no }
  bridges:
    br-g20:
      interfaces: [ens192.20]
      addresses: [10.20.0.1/22]
      ...
    br-g40:
      interfaces: [ens192.40]
      addresses: [10.40.0.1/24]
      ...
```

Networks are rendered in a stable order (VLAN id, then name) so the bundle is
deterministic. The renderer **never** emits the management or WAN interface, so
applying this file cannot disturb management connectivity. The parent's own
address is never touched — parents are expected to be L2 trunks (no IP), or the
base netplan already owns the legacy untagged case.

## 5. The 7-step create wizard (Hotel Admin)

The UI walks an operator through a guest network and its apply:

1. **Identity** — name, description, `ssid_label` (descriptive only; the WLAN
   controller owns the real SSID — see
   [ARUBA_SSID_VLAN_MAPPING.md](ARUBA_SSID_VLAN_MAPPING.md)).
2. **Interface / VLAN** — pick the parent interface and untagged vs VLAN + id.
   The bridge name is generated for you.
3. **Subnet / Gateway** — `subnet_cidr` and `gateway_ip` (must be inside the
   subnet, not the network/broadcast address).
4. **DHCP / DNS** — `dhcp_mode` (`local`/`external`/`relay`/`disabled`), pools,
   lease timers, DNS mode (`appliance` or `custom` servers), `domain_name`.
5. **Portal** — `captive_portal_enabled`, `internet_access_enabled`,
   `nat_enabled`, `client_isolation_enabled`.
6. **Review / Validate** — runs `POST /edge/v1/network/validate`
   (`netcfg.ValidateSet` + `kea config-test` + `nft -c` + `netplan generate`);
   shows structured issues by field. See
   [DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md) for the validation-code table.
7. **Apply** — `POST /edge/v1/network/apply` renders a numbered revision,
   snapshots the current config, applies, health-checks, and enters
   `pending_confirmation` under the 120 s watchdog. Confirm to keep it, or it
   auto-rolls-back. See
   [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md).

## 6. Constraints to know

- One untagged network per parent; no duplicate enabled VLAN on a parent.
- Bridge names are globally unique (`guest_networks_bridge_uniq`).
- Enabled guest subnets on one appliance must **not overlap** (no VRF yet) —
  `ValidateSet` returns `subnet_overlap`; the DB keeps IP→network unambiguous so
  sessions map cleanly.
- The parent interface may not be the management or WAN interface — the
  validator returns `protected_interface`.
