# Aruba (and other WLAN) SSID → VLAN Mapping

> Where the WLAN controller's job ends and StayConnect's begins. StayConnect does
> **not** configure your wireless controller — it consumes the VLANs the
> controller trunks to it. Overview: [EDGE_NETWORKING.md](EDGE_NETWORKING.md).
> Boundary rationale: [PHASE19_ASSESSMENT.md](PHASE19_ASSESSMENT.md) §4.

## 1. The responsibility boundary

| Owned by the WLAN controller (Aruba/Ruckus/Cisco/Extreme) | Owned by StayConnect |
|---|---|
| The **SSID** ("Coral Sea Guest") and its radio/security settings | — |
| **SSID → VLAN** assignment (which VLAN a client lands on) | — |
| Tagging guest traffic 802.1Q and **trunking the VLAN** to the StayConnect guest-trunk port | — |
| — | **VLAN → interface** (the `parent.<vlan>` sub-interface + bridge) |
| — | **Gateway** (`gateway_ip`/`subnet_cidr`), **DHCP**, **DNS** |
| — | **Captive portal** (option 114, DNAT interception, portald) |
| — | **Internet policy** (NAT, walled garden, shaping, client isolation) |

One sentence: **the controller decides which VLAN a guest is on; StayConnect
provides everything that VLAN needs to reach the internet through a sign-in
page.** `guest_networks.ssid_label` is descriptive only — a human label to help
operators match a StayConnect network to the controller's SSID. Setting it does
not touch the controller.

## 2. Step-by-step — Aruba

On the Aruba controller / Aruba Central (Instant AP flow is analogous):

1. **Create the SSID.** WLAN → *New*. Name it `Coral Sea Guest`. Set the security
   you want (typically Open or Enhanced Open for a captive-portal guest network;
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
   [EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md)).

Then in **StayConnect Hotel Admin**:

5. Assign the appliance interface (e.g. `ens192`) the **`guest_trunk`** role
   (Network → Interfaces).
6. Create the guest network: type **VLAN**, parent `ens192`, VLAN id **20**,
   gateway `10.20.0.1/22`, DHCP `local` with a pool, captive portal on. Set
   `ssid_label` to `Coral Sea Guest` for readability. Validate, apply, confirm
   ([GUEST_VLAN_CONFIGURATION.md](GUEST_VLAN_CONFIGURATION.md),
   [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md)).

The result: a guest joins `Coral Sea Guest` → the AP tags them VLAN 20 → the
trunk carries VLAN 20 to `ens192` → `ens192.20` → `br-g20` → StayConnect gives
them a `10.20.x.x` lease, option 114, and the captive portal.

## 3. The exact UI note text

Hotel Admin shows this on the VLAN step of the guest-network wizard, so operators
know the controller work is theirs:

> **StayConnect does not configure your WLAN controller.** On your Aruba (or
> Ruckus / Cisco / Extreme) controller, create the SSID, map it to VLAN 20, and
> trunk VLAN 20 (tagged) to this appliance's guest-trunk port. StayConnect then
> provides the VLAN 20 gateway, DHCP, DNS, captive portal and internet access.
> The SSID name below is a label only — it does not change your controller.

## 4. Other controllers (analogous)

The three-part controller task — **create SSID, assign VLAN, trunk the VLAN** —
is the same everywhere; only the menu names differ:

| Vendor | SSID → VLAN | Trunk the VLAN |
|---|---|---|
| **Aruba** (Central / ArubaOS) | WLAN → VLAN → static VLAN 20 | switch/uplink port: 802.1Q trunk, tag VLAN 20 |
| **Ruckus** (SmartZone / Unleashed) | WLAN → VLAN 20 | AP/switch port trunk, tag VLAN 20 |
| **Cisco** (Catalyst / 9800 WLC) | WLAN → Policy Profile → VLAN 20 | `switchport mode trunk` + `switchport trunk allowed vlan add 20` |
| **Extreme** (ExtremeCloud IQ) | Network Policy → SSID → VLAN 20 | port trunk, tag VLAN 20 |

## 5. What StayConnect never does

- It does not log into or push config to the controller.
- It does not create or rename SSIDs (the `ssid_label` is cosmetic).
- It does not choose which clients land on which VLAN — that is the controller's
  SSID→VLAN policy.

If VLAN 20 traffic never reaches StayConnect, the problem is the controller/switch
trunk, not StayConnect — see
[NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md) §"VLAN not passing
traffic".

## 6. Verifying end to end

Once both sides are configured, walk the path from the client inward:

| Step | Check on StayConnect | Expected |
|---|---|---|
| Tagged frames arrive | `ip -br link show ens192.20` | device exists, state UP |
| Bridge carries the VLAN | `bridge link show` | `ens192.20` master `br-g20` |
| Gateway is live | `ip -br addr show br-g20` | `10.20.0.1/22` |
| Guest gets a lease | leases page / Kea `lease4-get-all` | client in `10.20.x.x`, gateway `10.20.0.1` |
| Portal pops | join `Coral Sea Guest` on a phone | sign-in page at `http://10.20.0.1:8380/` |

If the VLAN device or bridge never shows traffic counters incrementing
(`ip -s link show ens192.20`), the tagged frames aren't arriving — recheck the
controller SSID→VLAN mapping and the switch/AP trunk. On ESXi, also confirm the
LAN portgroup allows Promiscuous / MAC-changes / Forged-transmits (needed for the
bridge to pass VLAN-tagged guest MACs).

## 7. Common mapping mistakes

- **Controller portal left on** — the AP shows its own splash page and guests
  never reach StayConnect's. Turn the controller's captive portal off for the
  SSID; StayConnect is the portal.
- **VLAN sent untagged** — if the controller puts VLAN 20 on the trunk untagged,
  create an `untagged` StayConnect network on that access port instead, or fix the
  trunk to tag it. Untagged frames can't be demuxed to `ens192.20`.
- **Access port instead of trunk** — a single-VLAN access port works only for one
  untagged network; to carry multiple guest VLANs the appliance port must be a
  tagged trunk with role `guest_trunk`.
