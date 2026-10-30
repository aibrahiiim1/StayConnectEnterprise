# DHCP on client networks

> Consolidated on 2026-10-28 (T0265) from `DHCP_MANAGEMENT.md`, `DHCP_OPTION_114.md`, `EXTERNAL_DHCP_MODE.md`; the parts are unchanged in substance. The client-network model as a whole: [EDGE_NETWORKING.md](EDGE_NETWORKING.md); the current behaviour: [CLIENT_NETWORKING.md](../CLIENT_NETWORKING.md) and the [system handbook](../architecture/ONEGATE_SYSTEM_HANDBOOK.md) §10.

| Part | Topic |
|---|---|
| Part A | Local DHCP management (Kea) |
| Part B | DHCP option 114, the captive-portal API (RFC 8910) |
| Part C | External DHCP mode |

## Part A — Local DHCP management (Kea)

> Local DHCP for client networks with Kea, driven **online through the control
> socket** — no restart, no CSV parsing. Renderer:
> `data-plane/internal/netcfg/render_kea.go`. Validator: `validate.go`.
> Overview: [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

### 1. Kea via the control socket

The appliance runs **Kea DHCP4 2.0.2** with a live Unix control socket
(`/run/kea/kea4-ctrl-socket`). netd renders the full `Dhcp4` object from the
enabled **local** client networks and drives Kea online:

| Step | Kea command | Effect |
|---|---|---|
| Validate | `config-test` | check the rendered config, no apply |
| Apply | `config-set` | swap the running config atomically (no restart) |
| Persist | *(netd's own atomic write, not Kea's `config-write`)* | `/etc/kea/kea-dhcp4.conf` holds the **confirmed** configuration only, written at a confirm, at boot reconcile and after a rollback to the previous revision; a failed write is reported by `/v1/health` (`kea_disk`) and retried |
| Leases | `lease4-get-all` | read live leases for the leases page |
| Status | `status-get` | liveness for the `kea_running` health check |

Kea is **not restarted** to change DHCP (the one exception: when it accepts a configuration but does not bind a gateway, netd restarts it once, waits for
its control socket and pushes the configuration again, because a restart loads the confirmed file, not the live one). Leases are **never** read by
parsing the memfile CSV — the socket is the structured interface. `RenderKeaFile`
produces the same `{ "Dhcp4": {…} }` document for the on-disk file so a cold
start matches what was `config-set`.

Only `dhcp_mode = local` networks are rendered into `subnet4`. `external`,
`relay` and `disabled` networks are intentionally **not** served (Kea must not
answer for a subnet the site runs itself) and their interfaces are excluded
from the bind list.

### 2. What a rendered subnet looks like

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
  `captive_portal_enabled` — see [DHCP.md, Part B](DHCP.md).
- `user-context` carries `guest_network_id` + `vlan_id` so `lease4-get-all`
  results attribute back to a client network.

### 3. Scopes, pools and reservations

- **Scope** = one client network's subnet (`subnet_cidr` + `gateway_ip`).
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

### 4. The leases page

`GET /edge/v1/dhcp/{networkId}/leases` calls Kea `lease4-get-all` and shows
active leases (IP, MAC, hostname, expiry) for the network, using `user-context`
to filter to the selected client network. Nothing is read from the memfile CSV.

### 5. Validation rules (from `validate.go`)

`netcfg.ValidateOne` / `ValidateSet` return structured `{field, code, message}`
issues. Pool/reservation checks run only for `dhcp_mode = local`. Every code:

| Code | Meaning | Fix |
|---|---|---|
| `required` (name/parent) | name or parent interface missing | supply it |
| `interface_not_found` | parent interface not present on the appliance | pick a discovered interface |
| `protected_interface` | parent is the management/WAN interface | choose a client-network interface |
| `bad_network_type` | `network_type` not `untagged`/`vlan` | fix the type |
| `vlan_out_of_range` | VLAN id not in 1–4094 | use a valid 802.1Q id |
| `subnet_too_large` | subnet wider than `/19` (8,190 usable addresses) | use `/19` or narrower, or split the site into several client networks |
| `subnet_overlaps_host` | the subnet overlaps the appliance's own WAN/management network | choose a range outside the WAN/management subnet |
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
| `subnet_overlap` | this subnet overlaps another enabled client subnet (no VRF yet) |

Validation is a hard gate before apply — see
[NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md) §3.

### 6. DHCP modes

`local` (this document), `external`, `relay`, `disabled`. `external` — the site
runs DHCP; StayConnect is still gateway + captive portal, see
[DHCP.md, Part C](DHCP.md). `relay` is supported in the schema
(`relay_targets`) but minimal in this release. `disabled` serves no DHCP.

The prepared Access network uses the pool `10.20.0.10` – `10.20.15.254` on `10.20.0.0/20` ([CLIENT_NETWORKING.md](../CLIENT_NETWORKING.md) §1); its address is never
forced onto a conflicting network.


## Part B — DHCP option 114, the captive-portal API (RFC 8910)

> The one DHCP option that makes captive-portal sign-in pop automatically across
> iOS, Android, macOS and Windows. StayConnect generates it per captive local
> network. Renderer: `data-plane/internal/netcfg/render_kea.go`; URL builder:
> `netcfg.PortalURLFor` (`validate.go`). Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

### 1. What it is

RFC 8910 defines DHCP option **114**: the network hands the
client a **URL for the captive-portal API** in its DHCP ACK. Modern client OSes
read it and open the sign-in page proactively, instead of relying on probe
requests (`captive.apple.com`, `connectivitycheck.gstatic.com`, MSFT NCSI) being
intercepted. It is the single most reliable auto-pop mechanism across all OSes.

### 2. What the value is: the HTTPS captive API, or the HTTP portal in fallback

Since the HTTPS Client Portal ([contract](../architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md) §4, §6) the
value follows the portal's HTTPS state, chosen by `netcfg.CaptivePortalURL` and pushed to Kea by netd's
15-second refresh without a network revision:

| Portal state | Option 114 |
|---|---|
| HTTPS active | `https://<canonical hostname>/captive-portal/api` (the RFC 8908 API, a valid public certificate) |
| HTTP fallback | `http://<gateway>:8380/` (unchanged from before) |

RFC 8908 requires the API to be served over TLS; a device whose TLS check fails falls back to its ordinary
probes, which the appliance still answers.

**Emitted by code 114, never by name.** Kea 2.0 (the appliance runs 2.0.2) maps the name `v4-captive-portal`
to code **160**, the obsolete RFC 7710 assignment; only Kea 2.2+ maps it to 114. Until 2026-10-13 this renderer
used the name, so **no StayConnect appliance on Kea 2.0 ever delivered option 114** — found on PRE-LIVE by
capturing a real DHCP exchange (the client requested 114; the ACK carried none). The earlier claims in this
document that option 114 drove the auto-pop were therefore never true on this appliance: the probes did.

### 3. Auto-generated per captive local network

The option is emitted **only** for networks that are both `dhcp_mode = local`
and `captive_portal_enabled = true`. Each network gets its **own** gateway in the
URL, so a client on VLAN 20 is pointed at `http://10.20.0.1:8380/` and a client
on VLAN 40 at `http://10.40.0.1:8380/`. In `RenderKeaDhcp4` the option is
appended to that subnet's `option-data`:

```json
{ "code": 114, "space": "dhcp4", "data": "http://10.20.0.1:8380/" }
```

### 4. Stored in the DB (derived)

`guest_networks.portal_url` holds the generated URL. It is **derived**, not
hand-entered: netd computes it from `gateway_ip` + the portal HTTP port whenever
the network is `local` + captive, and it is regenerated on every render so it
can never drift from the gateway. (The column is `NULL` for non-captive or
non-local networks.)

### 5. Rendered into the Kea subnet option-data

Full option-data block for a captive local network (VLAN 20):

```json
"option-data": [
  { "name": "routers",             "data": "10.20.0.1" },
  { "name": "domain-name-servers", "data": "10.20.0.1" },
  { "name": "domain-name",         "data": "guest.local" },
  { "code": 114, "space": "dhcp4",  "data": "http://10.20.0.1:8380/" }
]
```

Code 114 has a standard string definition in Kea 2.0.2 and later, so no custom option definition is needed.
The NAME must not be used on Kea 2.0 (it means code 160; see §2).

### 6. What to tell an external-DHCP admin

When the site runs its own DHCP server ([DHCP.md, Part C](DHCP.md)),
StayConnect does not serve the subnet — but the captive portal still needs
option 114 pointed at the StayConnect gateway. The Admin Console external-DHCP
checklist shows the admin the exact values to set on **their** server:

| Setting | Value |
|---|---|
| Router / default gateway | the StayConnect gateway for that VLAN, e.g. `10.20.0.1` |
| DNS | the StayConnect gateway (or the site's resolver, if it can reach the walled garden) |
| Option 114 (`Captive-Portal`, RFC 8910) | while the portal is on HTTPS: `https://<canonical hostname>/captive-portal/api` (shown on Admin Console → Network → Portal HTTPS); otherwise `http://10.20.0.1:8380/` |

Vendor syntax examples:

- **ISC dhcpd**: `option v4-captive-portal "http://10.20.0.1:8380/";` (define
  the option code 114, type text, first).
- **Windows DHCP**: add option 114 (String) with value `http://10.20.0.1:8380/`
  on the scope.
- **MikroTik / RouterOS**: DHCP option name `captive-portal`, code 114,
  value string `http://10.20.0.1:8380/`.

If the site's DHCP can't set option 114, captive sign-in still works via the
nftables DNAT interception (probe requests get redirected), but auto-pop is less
reliable — recommend they set the option.

### 7. Per-OS behaviour (why this matters)

Option 114 is the only mechanism that pops the sign-in sheet reliably across all
platforms; the fallbacks differ per OS:

| OS | With option 114 | Without (probe fallback) |
|---|---|---|
| iOS / iPadOS | opens the Captive Network Assistant sheet from the ACK | relies on `captive.apple.com` probe being DNATed |
| macOS | same CNA sheet | same probe path |
| Android | opens the sign-in notification / activity | relies on `connectivitycheck.gstatic.com` interception |
| Windows | NCSI marks the network as needing sign-in and offers the portal | relies on `www.msftconnecttest.com` interception |

The DNAT fallback (nftables redirecting :80/:443 to the gateway portal) always
runs, so a client that ignores option 114 still can't browse until it signs in —
but it may not auto-pop. Setting option 114 is what turns "open a browser and try
to load a page" into "the sign-in sheet appears on its own."

### 8. Confirming it's live

- In Kea's running configuration (`config-get`): the subnet's `option-data` has an entry with `"code": 114`
  and the value from §2. An entry named `v4-captive-portal` with code 160 is the old, never-delivered form.
- `scripts/portal-https-e2e-test.sh` leases a guest-network client and asserts the value it actually received.
- On a client after a lease: the DHCP ACK carries option 114 (visible in a packet
  capture or the client's network detail on some OSes).
- If it's absent, check `captive_portal_enabled` (it's only emitted for captive
  networks) and `dhcp_mode = local`; `external` networks rely on the site's
  server to set it ([DHCP.md, Part C](DHCP.md)).


## Part C — External DHCP mode

> When the site keeps its own DHCP server. StayConnect is still the **gateway
> and captive portal** for the VLAN, but Kea does **not** serve the subnet.
> Field: `guest_networks.dhcp_mode`. Renderer:
> `data-plane/internal/netcfg/render_kea.go`. Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

### 1. When to use it

Some sites run a central DHCP server (Windows Server, an IPAM appliance, a core
router) and want to keep addressing there — for reservations tied to their own
records, or for policy reasons. In that case set the client network's
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

### 2. The four DHCP modes

| Mode | Kea serves the subnet? | Gateway/portal by StayConnect? | Use when |
|---|---|---|---|
| `local` | **Yes** — full `subnet4` with pools, reservations, option 114 | Yes | default; StayConnect owns everything ([DHCP.md, Part A](DHCP.md)) |
| `external` | No | Yes | the site runs its own DHCP server on the VLAN |
| `relay` | No (relays elsewhere) | Yes | central DHCP on a different L3 segment; **supported in schema, minimal in this release** |
| `disabled` | No | Yes (no DHCP at all) | static-only / lab segments |

### 3. The checklist StayConnect shows the external-DHCP admin

Admin Console renders this for an `external` network so the site's DHCP admin can
configure their server to hand out addresses that route through StayConnect:

| Setting on the site's DHCP server | Value |
|---|---|
| **Scope / subnet** | the network's `subnet_cidr`, e.g. `10.20.0.0/22` |
| **Router / default gateway** | the StayConnect gateway `gateway_ip`, e.g. `10.20.0.1` |
| **DNS servers** | the StayConnect gateway (`10.20.0.1`) for appliance DNS + walled-garden resolution, or the site's resolver if it can reach the walled garden |
| **Option 114 (Captive-Portal, RFC 8910)** | `http://10.20.0.1:8380/` — **plain HTTP**, the gateway IP, port 8380 ([DHCP.md, Part B](DHCP.md)) |
| **Lease time** | the site's choice; short-ish (e.g. 1–2 h) suits transient clients |
| **Exclusions** | exclude `gateway_ip` from any dynamic range |

Critical: the **default gateway the external server hands out must be the
StayConnect gateway** — otherwise client traffic never reaches the captive portal
or NAT, and clients bypass the whole system.

### 4. Relay mode (schema-supported, minimal)

`relay` models the case where DHCP lives on a different L3 segment and the
appliance must forward DHCP requests to it. The schema carries `relay_targets`
(JSON array of server IPs) and the validator checks them (`relay_required`,
`bad_relay_target`), but the DHCP-relay data path is **minimal in this release**
and is only surfaced in the UI where it can be tested. For most sites, `external`
(their DHCP directly on the VLAN) is the right choice today; use `relay` only in
coordination with StayConnect support.

### 5. Local vs external — how to decide

- Want the simplest setup, pool + reservations managed in Admin Console, guaranteed
  option 114 → **`local`**.
- The site insists on central addressing / its own IPAM, DHCP already lives on the
  client VLAN → **`external`** (and make sure they set gateway + option 114 as
  above).
- No DHCP wanted at all (static hosts) → **`disabled`**.

Switching a network between modes is a normal edit + apply; it goes through the
same validate → apply → confirm lifecycle
([NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md)), and Kea is
`config-set` online with the network added to or removed from `subnet4`.

### 6. Verifying an external-DHCP network

After applying an `external` network, confirm StayConnect is doing its half and
staying out of the site's DHCP:

| Check | Expected |
|---|---|
| StayConnect's Kea is **not** serving the subnet | the network is absent from Kea's `subnet4`; Kea does not bind `br-g<vlan>` |
| The VLAN gateway is up on StayConnect | `ip -br addr show br-g20` → `10.20.0.1/22` |
| Clients get leases from the site's server | client IP is in the site's scope, **default gateway = the StayConnect gateway** |
| Captive portal still intercepts | unauthenticated HTTP redirects to `http://10.20.0.1:8380/` (DNAT is independent of who serves DHCP) |
| Only one DHCP server answers | run a DHCP discover on the VLAN; exactly one offer, from the site's server |

If clients get an address but no portal and no internet, the site's server is
almost always handing out the **wrong default gateway** (its own router instead
of the StayConnect gateway) — fix that first. See
[NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md) §1–2.

### 7. What does not change in external mode

- nftables captive DNAT, masquerade, walled garden, inter-client isolation and
  shaping are all still rendered for the network (they don't depend on `dhcp_mode`).
- Option 114 is **not** rendered by StayConnect (it renders option-data only for
  `local` subnets) — the site's server must carry it, which is why the checklist
  in §3 includes it explicitly.
