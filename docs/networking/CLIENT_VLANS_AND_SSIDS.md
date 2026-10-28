# Client VLANs, and mapping SSIDs to them

> Consolidated on 2026-10-28 (T0265) from `GUEST_VLAN_CONFIGURATION.md`, `ARUBA_SSID_VLAN_MAPPING.md`; the parts are unchanged in substance. The client-network model as a whole: [EDGE_NETWORKING.md](EDGE_NETWORKING.md); the current behaviour: [CLIENT_NETWORKING.md](../CLIENT_NETWORKING.md) and the [system handbook](../architecture/ONEGATE_SYSTEM_HANDBOOK.md) §10.

| Part | Topic |
|---|---|
| Part A | Client VLAN and network configuration |
| Part B | WLAN controller (Aruba and others): SSID → VLAN mapping |

## Part A — Client VLAN and network configuration

> How to add a client network — untagged (a bridge straight over a parent port)
> or a tagged 802.1Q VLAN — and what the appliance renders for it. Model:
> `data-plane/internal/netcfg/model.go`; netplan renderer:
> `render_netplan.go`. Overview: [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

A client network (a `guest_networks` row) is one L2/L3 domain the appliance serves: its own bridge, L3
gateway, DHCP scope, DNS, firewall zone and captive-portal policy. Two shapes:

- **untagged** — the bridge enslaves the parent interface directly. Only **one**
  untagged network per parent interface (untagged frames aren't demuxable; the DB
  enforces this with a partial unique index `guest_networks_untagged_parent_uniq`).
- **vlan** — a `parent.<vlan>` 802.1Q sub-interface feeds the bridge. Many VLANs
  can share one trunk parent. `(parent_interface, vlan_id)` is unique among
  enabled networks (`guest_networks_vlan_parent_uniq`).

The `gn_vlan_consistency` CHECK enforces the rule both ways: a `vlan` network
must set `vlan_id`; an `untagged` network must not.

### 1. Bridge & VLAN device naming

netd derives names deterministically (`netcfg.BridgeNameFor`, `VLANIfaceName`),
kept within the Linux `IFNAMSIZ` limit of **15 characters**:

| Kind | Bridge name | Example | VLAN device |
|---|---|---|---|
| VLAN | `br-g<vlan>` | VLAN 20 → `br-g20` (max `br-g4094`, 7 chars) | `parent.<vlan>`, e.g. `ens192.20` |
| untagged | `br-g-<8hex of id>` | `br-g-1a2b3c4d` (13 chars) | none (parent enslaved directly) |

`VLANIfaceName` caps the parent so `parent.<vlan>` stays ≤ 15 (a long parent name
is truncated before the `.vlan` suffix).

### 2. Worked example — untagged client network

Parent `ens192` is a plain client access port (role `guest_access`, *Client access*). One untagged
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

### 3. Worked example — tagged VLAN 20 (Main)

Parent `ens192` is a `guest_trunk` (*Client trunk*) carrying tagged VLANs from the WLAN
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

### 4. Worked example — multiple VLANs on one trunk

VLAN 20 "Main" + VLAN 40 "Conference", both on `ens192`:

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

<a id="5-the-7-step-create-wizard-hotel-admin"></a>
### 5. The 7-step create wizard (Admin Console)

The Admin Console (**Networking → Client networks → New client network**) walks an
operator through a client network (a `guest_networks` row) and its apply:

1. **Identity** — name, description, `ssid_label` (descriptive only; the WLAN
   controller owns the real SSID — see
   [CLIENT_VLANS_AND_SSIDS.md, Part B](CLIENT_VLANS_AND_SSIDS.md)).
2. **Interface / VLAN** — pick the parent interface (role `guest_access` or
   `guest_trunk`, shown as *Client access* / *Client trunk*, or `unused`) and
   untagged vs VLAN + id.
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
   [DHCP.md, Part A](DHCP.md) for the validation-code table.
7. **Apply** — `POST /edge/v1/network/apply` renders a numbered revision,
   snapshots the current config, applies, health-checks, and enters
   `pending_confirmation` under the 120 s watchdog. Confirm to keep it, or it
   auto-rolls-back. See
   [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md).

### 6. Constraints to know

- One untagged network per parent; no duplicate enabled VLAN on a parent.
- Bridge names are globally unique (`guest_networks_bridge_uniq`).
- Enabled client subnets on one appliance must **not overlap** (no VRF yet) —
  `ValidateSet` returns `subnet_overlap`; the DB keeps IP→network unambiguous so
  sessions map cleanly.
- The parent interface may not be the management or WAN interface — the
  validator returns `protected_interface`.


## Part B — WLAN controller (Aruba and others): SSID → VLAN mapping

> Where the WLAN controller's job ends and StayConnect's begins. StayConnect does
> **not** configure your wireless controller — it consumes the VLANs the
> controller trunks to it. Overview: [EDGE_NETWORKING.md](EDGE_NETWORKING.md).
> Boundary rationale: [PHASE19_ASSESSMENT.md (retired; see the archive)](../ARCHIVE.md) §4.

### 1. The responsibility boundary

| Owned by the WLAN controller (Aruba/Ruckus/Cisco/Extreme) | Owned by StayConnect |
|---|---|
| The **SSID** ("OneGate Wi-Fi") and its radio/security settings | — |
| **SSID → VLAN** assignment (which VLAN a client lands on) | — |
| Tagging client traffic 802.1Q and **trunking the VLAN** to the StayConnect client-trunk port (`guest_trunk` role) | — |
| — | **VLAN → interface** (the `parent.<vlan>` sub-interface + bridge) |
| — | **Gateway** (`gateway_ip`/`subnet_cidr`), **DHCP**, **DNS** |
| — | **Captive portal** (option 114, DNAT interception, portald) |
| — | **Internet policy** (NAT, walled garden, shaping, client isolation) |

One sentence: **the controller decides which VLAN a client is on; StayConnect
provides everything that VLAN needs to reach the internet through a sign-in
page.** `guest_networks.ssid_label` is descriptive only — a human label to help
operators match a StayConnect network to the controller's SSID. Setting it does
not touch the controller.

### 2. Step-by-step — Aruba

On the Aruba controller / Aruba Central (Instant AP flow is analogous):

1. **Create the SSID.** WLAN → *New*. Name it `OneGate Wi-Fi`. Set the security
   you want (typically Open or Enhanced Open for a captive-portal client network;
   leave the controller's own captive portal **off** — StayConnect is the
   portal).
2. **Map the SSID to VLAN 20.** In the SSID's network/VLAN settings, set the
   client VLAN to **20** (static VLAN assignment). Do not enable the controller's
   built-in portal, MAC auth, or its own DHCP for this SSID.
3. **Trunk VLAN 20 to the StayConnect uplink.** On the switch port (or the AP
   uplink / controller uplink) that connects toward the StayConnect appliance,
   configure an 802.1Q **trunk** and **tag VLAN 20** on it. That port must reach
   the appliance interface you've assigned the `guest_trunk` role.
4. **Confirm no other DHCP/gateway serves VLAN 20** — StayConnect is the gateway
   and DHCP server for it (unless you deliberately chose external DHCP, see
   [DHCP.md, Part C](DHCP.md)).

Then in the **OneGate Admin Console**:

5. Make sure the appliance interface (e.g. `ens192`) has the **`guest_trunk`**
   role — shown as *Client trunk* on the interface step of the new-network wizard.
6. Create the client network (**Networking → Client networks → New client
   network**): type **VLAN**, parent `ens192`, VLAN id **20**,
   gateway `10.20.0.1/22`, DHCP `local` with a pool, captive portal on. Set
   `ssid_label` to `OneGate Wi-Fi` for readability. Validate, apply, confirm
   ([CLIENT_VLANS_AND_SSIDS.md, Part A](CLIENT_VLANS_AND_SSIDS.md),
   [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md)).

The result: a client joins `OneGate Wi-Fi` → the AP tags them VLAN 20 → the
trunk carries VLAN 20 to `ens192` → `ens192.20` → `br-g20` → StayConnect gives
them a `10.20.x.x` lease, option 114, and the captive portal.

### 3. The exact UI note text

The Admin Console shows this on the **Review** step of the **New client network**
wizard, so operators know the controller work is theirs:

> **Wireless controller action required** — Map the '‹SSID label›' SSID to VLAN 20
> on your wireless controller. OneGate manages the gateway, DHCP and captive
> portal; it does not broadcast Wi-Fi.

### 4. Other controllers (analogous)

The three-part controller task — **create SSID, assign VLAN, trunk the VLAN** —
is the same everywhere; only the menu names differ:

| Vendor | SSID → VLAN | Trunk the VLAN |
|---|---|---|
| **Aruba** (Central / ArubaOS) | WLAN → VLAN → static VLAN 20 | switch/uplink port: 802.1Q trunk, tag VLAN 20 |
| **Ruckus** (SmartZone / Unleashed) | WLAN → VLAN 20 | AP/switch port trunk, tag VLAN 20 |
| **Cisco** (Catalyst / 9800 WLC) | WLAN → Policy Profile → VLAN 20 | `switchport mode trunk` + `switchport trunk allowed vlan add 20` |
| **Extreme** (ExtremeCloud IQ) | Network Policy → SSID → VLAN 20 | port trunk, tag VLAN 20 |

### 5. What StayConnect never does

- It does not log into or push config to the controller.
- It does not create or rename SSIDs (the `ssid_label` is cosmetic).
- It does not choose which clients land on which VLAN — that is the controller's
  SSID→VLAN policy.

If VLAN 20 traffic never reaches StayConnect, the problem is the controller/switch
trunk, not StayConnect — see
[NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md) §"VLAN not passing
traffic".

### 6. Verifying end to end

Once both sides are configured, walk the path from the client inward:

| Step | Check on StayConnect | Expected |
|---|---|---|
| Tagged frames arrive | `ip -br link show ens192.20` | device exists, state UP |
| Bridge carries the VLAN | `bridge link show` | `ens192.20` master `br-g20` |
| Gateway is live | `ip -br addr show br-g20` | `10.20.0.1/22` |
| Client gets a lease | leases page / Kea `lease4-get-all` | client in `10.20.x.x`, gateway `10.20.0.1` |
| Portal pops | join `OneGate Wi-Fi` on a phone | sign-in page at `http://10.20.0.1:8380/` |

If the VLAN device or bridge never shows traffic counters incrementing
(`ip -s link show ens192.20`), the tagged frames aren't arriving — recheck the
controller SSID→VLAN mapping and the switch/AP trunk. On ESXi, also confirm the
LAN portgroup allows Promiscuous / MAC-changes / Forged-transmits (needed for the
bridge to pass VLAN-tagged client MACs).

### 7. Common mapping mistakes

- **Controller portal left on** — the AP shows its own splash page and clients
  never reach StayConnect's. Turn the controller's captive portal off for the
  SSID; StayConnect is the portal.
- **VLAN sent untagged** — if the controller puts VLAN 20 on the trunk untagged,
  create an `untagged` StayConnect network on that access port instead, or fix the
  trunk to tag it. Untagged frames can't be demuxed to `ens192.20`.
- **Access port instead of trunk** — a single-VLAN access port works only for one
  untagged network; to carry multiple client VLANs the appliance port must be a
  tagged trunk with role `guest_trunk`.
