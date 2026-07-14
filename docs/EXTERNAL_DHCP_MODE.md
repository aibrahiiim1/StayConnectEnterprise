# External DHCP Mode (Phase 19)

> When the hotel keeps its own DHCP server. StayConnect is still the **gateway
> and captive portal** for the VLAN, but Kea does **not** serve the subnet.
> Field: `guest_networks.dhcp_mode`. Renderer:
> `data-plane/internal/netcfg/render_kea.go`. Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

## 1. When to use it

Some hotels run a central DHCP server (Windows Server, an IPAM appliance, a core
router) and want to keep addressing there — for reservations tied to their own
records, or for policy reasons. In that case set the guest network's
`dhcp_mode = external`:

- StayConnect **still** owns the VLAN gateway (`gateway_ip`), captive portal,
  NAT, walled garden, DNS (optionally), shaping and internet policy.
- StayConnect's **Kea does not answer** for the subnet. In `RenderKeaDhcp4` only
  `dhcp_mode = local` networks become `subnet4` entries; `external` networks are
  skipped entirely and their interface is excluded from Kea's bind list, so Kea
  will not even listen on that bridge. There is no risk of a rogue second DHCP
  server.

Because Kea isn't serving the subnet, the pool/reservation validators are also
skipped for the network (they run only for `local`) — pools you add are ignored
until you switch back to `local`.

## 2. The four DHCP modes

| Mode | Kea serves the subnet? | Gateway/portal by StayConnect? | Use when |
|---|---|---|---|
| `local` | **Yes** — full `subnet4` with pools, reservations, option 114 | Yes | default; StayConnect owns everything ([DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md)) |
| `external` | No | Yes | the hotel runs its own DHCP server on the VLAN |
| `relay` | No (relays elsewhere) | Yes | central DHCP on a different L3 segment; **supported in schema, minimal in this release** |
| `disabled` | No | Yes (no DHCP at all) | static-only / lab segments |

## 3. The checklist StayConnect shows the external-DHCP admin

Hotel Admin renders this for an `external` network so the hotel's DHCP admin can
configure their server to hand out addresses that route through StayConnect:

| Setting on the hotel's DHCP server | Value |
|---|---|
| **Scope / subnet** | the network's `subnet_cidr`, e.g. `10.20.0.0/22` |
| **Router / default gateway** | the StayConnect gateway `gateway_ip`, e.g. `10.20.0.1` |
| **DNS servers** | the StayConnect gateway (`10.20.0.1`) for appliance DNS + walled-garden resolution, or the hotel's resolver if it can reach the walled garden |
| **Option 114 (Captive-Portal, RFC 8910)** | `http://10.20.0.1:8380/` — **plain HTTP**, the gateway IP, port 8380 ([DHCP_OPTION_114.md](DHCP_OPTION_114.md)) |
| **Lease time** | the hotel's choice; short-ish (e.g. 1–2 h) suits transient guests |
| **Exclusions** | exclude `gateway_ip` from any dynamic range |

Critical: the **default gateway the external server hands out must be the
StayConnect gateway** — otherwise guest traffic never reaches the captive portal
or NAT, and clients bypass the whole system.

## 4. Relay mode (schema-supported, minimal)

`relay` models the case where DHCP lives on a different L3 segment and the
appliance must forward DHCP requests to it. The schema carries `relay_targets`
(JSON array of server IPs) and the validator checks them (`relay_required`,
`bad_relay_target`), but the DHCP-relay data path is **minimal in this release**
and is only surfaced in the UI where it can be tested. For most hotels, `external`
(their DHCP directly on the VLAN) is the right choice today; use `relay` only in
coordination with StayConnect support.

## 5. Local vs external — how to decide

- Want the simplest setup, pool + reservations managed in Hotel Admin, guaranteed
  option 114 → **`local`**.
- Hotel insists on central addressing / their own IPAM, DHCP already lives on the
  guest VLAN → **`external`** (and make sure they set gateway + option 114 as
  above).
- No DHCP wanted at all (static hosts) → **`disabled`**.

Switching a network between modes is a normal edit + apply; it goes through the
same validate → apply → confirm lifecycle
([NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md)), and Kea is
`config-set` online with the network added to or removed from `subnet4`.

## 6. Verifying an external-DHCP network

After applying an `external` network, confirm StayConnect is doing its half and
staying out of the hotel's DHCP:

| Check | Expected |
|---|---|
| StayConnect's Kea is **not** serving the subnet | the network is absent from Kea's `subnet4`; Kea does not bind `br-g<vlan>` |
| The VLAN gateway is up on StayConnect | `ip -br addr show br-g20` → `10.20.0.1/22` |
| Guests get leases from the hotel's server | client IP is in the hotel's scope, **default gateway = the StayConnect gateway** |
| Captive portal still intercepts | unauthenticated HTTP redirects to `http://10.20.0.1:8380/` (DNAT is independent of who serves DHCP) |
| Only one DHCP server answers | run a DHCP discover on the VLAN; exactly one offer, from the hotel's server |

If guests get an address but no portal and no internet, the hotel's server is
almost always handing out the **wrong default gateway** (its own router instead
of the StayConnect gateway) — fix that first. See
[NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md) §1–2.

## 7. What does not change in external mode

- nftables captive DNAT, masquerade, walled garden, inter-guest isolation and
  shaping are all still rendered for the network (they don't depend on `dhcp_mode`).
- Option 114 is **not** rendered by StayConnect (it renders option-data only for
  `local` subnets) — the hotel's server must carry it, which is why the checklist
  in §3 includes it explicitly.
