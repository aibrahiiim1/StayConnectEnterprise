# DHCP Management (Phase 19)

> Local DHCP for guest networks with Kea, driven **online through the control
> socket** — no restart, no CSV parsing. Renderer:
> `data-plane/internal/netcfg/render_kea.go`. Validator: `validate.go`.
> Overview: [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

## 1. Kea via the control socket

The appliance runs **Kea DHCP4 2.0.2** with a live Unix control socket
(`/run/kea/kea4-ctrl-socket`). netd renders the full `Dhcp4` object from the
enabled **local** guest networks and drives Kea online:

| Step | Kea command | Effect |
|---|---|---|
| Validate | `config-test` | check the rendered config, no apply |
| Apply | `config-set` | swap the running config atomically (no restart) |
| Persist | `config-write` | write to `/etc/kea/kea-dhcp4.conf` for cold start |
| Leases | `lease4-get-all` | read live leases for the leases page |
| Status | `status-get` | liveness for the `kea_running` health check |

Kea is **never restarted** to change DHCP, and leases are **never** read by
parsing the memfile CSV — the socket is the structured interface. `RenderKeaFile`
produces the same `{ "Dhcp4": {…} }` document for the on-disk file so a cold
start matches what was `config-set`.

Only `dhcp_mode = local` networks are rendered into `subnet4`. `external`,
`relay` and `disabled` networks are intentionally **not** served (Kea must not
answer for a subnet the hotel runs itself) and their interfaces are excluded
from the bind list.

## 2. What a rendered subnet looks like

For VLAN 20 (`10.20.0.0/22`, gateway `10.20.0.1`, one pool, one reservation):

```json
{
  "id": 1,
  "subnet": "10.20.0.0/22",
  "interface": "br-g20",
  "pools": [ { "pool": "10.20.0.100 - 10.20.3.250" } ],
  "option-data": [
    { "name": "routers", "data": "10.20.0.1" },
    { "name": "domain-name-servers", "data": "10.20.0.1" },
    { "name": "domain-name", "data": "guest.local" },
    { "name": "v4-captive-portal", "data": "http://10.20.0.1:8380/" }
  ],
  "valid-lifetime": 3600,
  "min-valid-lifetime": 900,
  "max-valid-lifetime": 7200,
  "reservations": [
    { "hw-address": "aa:bb:cc:dd:ee:ff", "ip-address": "10.20.0.10", "hostname": "lobby-printer" }
  ],
  "user-context": { "guest_network_id": "…uuid…", "vlan_id": 20 }
}
```

Notes:

- Kea binds the network's **bridge** (`interface: br-g20`), and the top-level
  `interfaces-config` lists `br-g20/10.20.0.1` with `dhcp-socket-type: raw`.
- `domain-name-servers` defaults to the gateway (appliance DNS). With
  `dns_mode = custom` it becomes the comma-joined `dns_servers`.
- `v4-captive-portal` (RFC 8910 option 114) is emitted only when
  `captive_portal_enabled` — see [DHCP_OPTION_114.md](DHCP_OPTION_114.md).
- `user-context` carries `guest_network_id` + `vlan_id` so `lease4-get-all`
  results attribute back to a guest network.

## 3. Scopes, pools and reservations

- **Scope** = one guest network's subnet (`subnet_cidr` + `gateway_ip`).
- **Pools** (`dhcp_pools`): a subnet may have **several** ranges, each
  `start_ip … end_ip`, ordered by `sort_order`. Rendered as
  `{ "pool": "start - end" }` entries. The `dhcp_pool_order` CHECK enforces
  `start_ip <= end_ip` at the DB.
- **Reservations** (`dhcp_reservations`): MAC → fixed IP, rendered as Kea **host
  reservations** inside the subnet (`hw-address` / `ip-address` / optional
  `hostname`). No extra hook is needed. `UNIQUE (guest_network_id, mac)` and
  `UNIQUE (guest_network_id, reserved_ip)` prevent duplicates at the DB;
  disabled reservations are skipped.
- **Lease timers**: `lease_default_seconds` / `lease_min_seconds` /
  `lease_max_seconds` → `valid-lifetime` / `min-valid-lifetime` /
  `max-valid-lifetime` (defaults 3600 / 900 / 7200).

## 4. The leases page

`GET /edge/v1/dhcp/{networkId}/leases` calls Kea `lease4-get-all` and shows
active leases (IP, MAC, hostname, expiry) for the network, using `user-context`
to filter to the selected guest network. Nothing is read from the memfile CSV.

## 5. Validation rules (from `validate.go`)

`netcfg.ValidateOne` / `ValidateSet` return structured `{field, code, message}`
issues. Pool/reservation checks run only for `dhcp_mode = local`. Every code:

| Code | Meaning | Fix |
|---|---|---|
| `required` (name/parent) | name or parent interface missing | supply it |
| `interface_not_found` | parent interface not present on the appliance | pick a discovered interface |
| `protected_interface` | parent is the management/WAN interface | choose a guest interface |
| `bad_network_type` | `network_type` not `untagged`/`vlan` | fix the type |
| `vlan_out_of_range` | VLAN id not in 1–4094 | use a valid 802.1Q id |
| `vlan_on_untagged` | untagged network set a VLAN id | clear `vlan_id` |
| `bad_gateway` | `gateway_ip` not valid IPv4 | correct the address |
| `bad_subnet` | `subnet_cidr` not valid IPv4 CIDR | correct the CIDR |
| `gateway_outside_subnet` | gateway not inside the subnet | move gateway into the subnet |
| `gateway_is_network` | gateway equals the network address | pick a host address |
| `gateway_is_broadcast` | gateway equals the broadcast address | pick a host address |
| `subnet_too_small` | subnet can't hold a gateway + pool (use /30 or larger) | widen the subnet |
| `bad_lease_timers` | min > default, or max < default | order the timers min ≤ default ≤ max |
| `dns_required` | custom DNS mode with no servers | add ≥ 1 DNS server |
| `bad_dns` | a DNS server isn't valid IPv4 | correct it |
| `portal_https` | option-114 URL came out HTTPS | must be plain HTTP (see option-114 doc) |
| `relay_required` | relay mode with no targets | add ≥ 1 relay target |
| `bad_relay_target` | relay target not valid IPv4 | correct it |
| `no_pool` | local DHCP with no pool | add at least one pool |
| `bad_ip` (pool/res) | a pool/reservation IP isn't valid IPv4 | correct it |
| `pool_reversed` | pool start > end | swap/fix the range |
| `pool_outside_subnet` | pool range not inside the subnet | move the pool into the subnet |
| `pool_contains_network` | pool includes the network address | shrink the pool |
| `pool_contains_broadcast` | pool includes the broadcast address | shrink the pool |
| `pool_contains_gateway` | pool includes the gateway | exclude the gateway |
| `pool_overlap` | pool overlaps another pool in the subnet | separate the ranges |
| `bad_mac` | reservation MAC invalid | correct the MAC |
| `dup_reservation_mac` | duplicate reservation MAC in the network | remove the duplicate |
| `dup_reservation_ip` | duplicate reserved IP in the network | remove the duplicate |
| `reservation_outside_subnet` | reserved IP outside the subnet | move it into the subnet |
| `reservation_in_pool` | reserved IP falls inside a dynamic pool | move it outside the pool or shrink the pool |

Cross-network checks from `ValidateSet`:

| Code | Meaning |
|---|---|
| `duplicate_vlan` | same VLAN id already used on that parent interface |
| `duplicate_bridge` | bridge name used by more than one network |
| `subnet_overlap` | this subnet overlaps another enabled guest subnet (no VRF yet) |

Validation is a hard gate before apply — see
[NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md) §3.

## 6. DHCP modes

`local` (this document), `external`, `relay`, `disabled`. `external` — the hotel
runs DHCP; StayConnect is still gateway + captive portal, see
[EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md). `relay` is supported in the schema
(`relay_targets`) but minimal in this release. `disabled` serves no DHCP.
