# Phase 19 — Current-State Assessment & Design (Hotel Networking)

## 1. Previous networking limitation (verified against the code)

The appliance hardcodes exactly **one** guest network everywhere. The single-network assumption is load-bearing in:

| Layer | Hardcoded artifact | Location |
|---|---|---|
| Firewall | `br-lan`, DNAT→`10.10.0.1:8380/8343`, masquerade `10.10.0.0/24`, WAN `ens160` | `deploy/nftables/stayconnect.nft` |
| nft auth set | `auth_ipv4` is **IP-only** (no interface/VLAN qualifier) | `data-plane/internal/nft/nft.go` |
| Shaping | `WANIface=ens160`, `LANIface=br-lan`, classid `0x1000+last_octet`, IP readback `10.10.0.x` | `internal/shape/shape.go` |
| tc root | primes/tears down `ens160`+`br-lan` only | `deploy/scripts/tc-setup.sh` |
| DHCP | single `subnet4 10.10.0.0/24` on `br-lan`, option-114 `http://10.10.0.1:8380/` | `deploy/kea/kea-dhcp4.conf` |
| L2/L3 | single `br-lan` bridge, `10.10.0.1/24`, slave `ens192` | `deploy/netplan/02-lan-bridge.yaml` |
| DNS | bind `10.10.0.1`, allow `10.10.0.0/24` | `deploy/unbound/…` |
| Session identity | `(tenant_id, ip)` — **no network column** | `sessions` table, `internal/session` |
| DHCP control | Kea is file-only; the declared unix control socket is unused | `deploy/kea/kea-dhcp4.conf` |

There is no way for hotel staff to add a VLAN, a second guest subnet, a DHCP scope, or a reservation, and no captive-portal option-114 management (it existed only as a live-VM edit until this phase).

## 2. Capabilities I can build on (discovered)

- **Kea 2.0.2 with a live unix control socket** (`/run/kea/kea4-ctrl-socket`) exposing `config-test` (validate without apply), `config-set`, `config-write` (persist), `config-reload`, `lease4-get-all`, `status-get`. This is the "supported structured interface" the spec wants — netd uses it instead of restarting Kea or parsing CSV.
- **802.1Q available** (`8021q` module) on `ens192` (vmxnet3). VLAN sub-interfaces create/destroy cleanly.
- edged's `mountResource` + `rolePerms` pattern and the `scdClient` unix-socket proxy give a clean way to add `/network/*`.

## 3. Design decisions (my "best scenario", where I deviate from the literal spec)

1. **Kea via control socket, not file-rewrite-and-restart.** netd renders a full `Dhcp4` object from the Site DB across all enabled *local* guest networks, validates it with `config-test`, applies with `config-set` + `config-write` (persists to `/etc/kea/kea-dhcp4.conf`), and never restarts Kea. Leases come from `lease4-get-all`; reservations are rendered as Kea host reservations in each subnet (no extra hook needed). This is more robust and truly online.

2. **Interface-qualified auth, subnet-derived.** Rather than require portald to detect the VLAN, scd derives the guest network from the **source IP's subnet** (netd enforces non-overlapping enabled subnets on one appliance, so IP→network is unambiguous). The nft auth set becomes a **concatenated `ifname . ipv4_addr`** set so the design is already correct if a future VRF permits overlaps. Every session records `guest_network_id`, `vlan_id`, `ingress_interface`, `gateway_ip`.

3. **A dedicated privileged `netd` daemon** on `/run/stayconnect/netd.sock` (root, socket group `stayconnect`, 0660) owns all render/validate/apply/rollback. `edged` (unprivileged) proxies to it. scd/portald/acctd are **not** made root — netd is the only new privileged surface, and it is never on a TCP port.

4. **Transactional apply with a watchdog.** Every change is a numbered revision with a rendered bundle under `/etc/stayconnect/generated/network/revision-NNNNNN/`. Lifecycle: draft → validated → applying → snapshot → apply → health-checks → **pending_confirmation** (120 s watchdog) → active, else automatic **rollback** to the previous known-good revision. Management/WAN interfaces are protected and never touched by guest-network applies. A management-reachability check is part of every apply.

5. **Legacy `br-lan` imported as the first Guest Network** (`Legacy Guest Network`, untagged, 10.10.0.0/24) at migration time, marked as already-active, so the live network is described by the DB without being re-applied (zero disruption).

6. **nftables generated per revision** from the DB: dynamic `guest_interfaces` / `guest_subnets` sets, per-network DNAT to that network's gateway portal, per-network masquerade, inter-guest isolation, and the concatenated `auth_ipv4` set. The static `stayconnect.nft` becomes a bootstrap skeleton; the live ruleset is the generated fragment.

7. **DHCP modes**: `local` (Kea, default), `external` (no Kea subnet — Hotel Admin shows the exact router/DNS/option-114 the hotel's DHCP admin must set), `disabled`. `relay` is modelled in the schema but only surfaced when it can be tested (not faked).

8. **Shaping stays correct for the legacy network and becomes network-aware** for new networks: download shaped on the network's own bridge, upload on WAN under a per-network band so classids never collide across networks. Existing accounting/quota behaviour is preserved.

## 4. Scope boundary (honest)

- StayConnect manages **VLAN → interface → gateway → DHCP → captive portal → internet policy**. It does **not** touch the WLAN controller's SSID→VLAN mapping; the UI shows the exact controller action required (see `docs/ARUBA_SSID_VLAN_MAPPING.md`).
- Management/WAN editing is **read-only** in this release (guarded); guest interfaces/VLANs are fully configurable.
- Pilot verification uses an **isolated test VLAN (219) in a network namespace** so the live guest network is never disrupted.
