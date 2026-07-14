# DHCP Option 114 — Captive-Portal API (RFC 8910)

> The one DHCP option that makes captive-portal sign-in pop automatically across
> iOS, Android, macOS and Windows. StayConnect generates it per captive local
> network. Renderer: `data-plane/internal/netcfg/render_kea.go`; URL builder:
> `netcfg.PortalURLFor` (`validate.go`). Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

## 1. What it is

RFC 8910 defines DHCP option **114** (`v4-captive-portal`): the network hands the
client a **URL for the captive-portal API** in its DHCP ACK. Modern client OSes
read it and open the sign-in page proactively, instead of relying on probe
requests (`captive.apple.com`, `connectivitycheck.gstatic.com`, MSFT NCSI) being
intercepted. It is the single most reliable auto-pop mechanism across all OSes.

## 2. Why plain HTTP, straight to portald — never HTTPS/Caddy

The option-114 value is **always** `http://<gateway>:8380/`:

- The captive-portal endpoint must be reachable **before** the client trusts the
  network or has a working DNS/PKI path. A plain-HTTP URL to the gateway IP
  avoids any certificate-name / trust-anchor problem on the captive path.
- Caddy on the appliance terminates TLS **only on the management IP** for Hotel
  Admin — it is never bound to the guest bridge. The guest captive path goes
  **directly to portald** on `:8380` (`:8343` exists for the TLS portal but is
  not the option-114 target).
- The validator enforces this: if the derived URL ever came out `https://` it
  raises `portal_https` ("captive-portal option 114 must use plain HTTP, not
  HTTPS"). In practice `PortalURLFor` always builds HTTP, so this is a guard.

`netcfg.PortalURLFor(gatewayIP, httpPort)` returns exactly
`fmt.Sprintf("http://%s:%d/", gatewayIP, httpPort)` — HTTP, the network's own
gateway, the portal HTTP port (default **8380**), trailing slash.

## 3. Auto-generated per captive local network

The option is emitted **only** for networks that are both `dhcp_mode = local`
and `captive_portal_enabled = true`. Each network gets its **own** gateway in the
URL, so a client on VLAN 20 is pointed at `http://10.20.0.1:8380/` and a client
on VLAN 40 at `http://10.40.0.1:8380/`. In `RenderKeaDhcp4` the option is
appended to that subnet's `option-data`:

```json
{ "name": "v4-captive-portal", "data": "http://10.20.0.1:8380/" }
```

## 4. Stored in the DB (derived)

`guest_networks.portal_url` holds the generated URL. It is **derived**, not
hand-entered: netd computes it from `gateway_ip` + the portal HTTP port whenever
the network is `local` + captive, and it is regenerated on every render so it
can never drift from the gateway. (The column is `NULL` for non-captive or
non-local networks.)

## 5. Rendered into the Kea subnet option-data

Full option-data block for a captive local network (VLAN 20):

```json
"option-data": [
  { "name": "routers",             "data": "10.20.0.1" },
  { "name": "domain-name-servers", "data": "10.20.0.1" },
  { "name": "domain-name",         "data": "guest.local" },
  { "name": "v4-captive-portal",   "data": "http://10.20.0.1:8380/" }
]
```

Kea 2.0.2 ships the `v4-captive-portal` option definition, so no custom option
definition is needed — the name is used directly.

## 6. What to tell an external-DHCP admin

When the hotel runs its own DHCP server ([EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md)),
StayConnect does not serve the subnet — but the captive portal still needs
option 114 pointed at the StayConnect gateway. The Hotel Admin external-DHCP
checklist shows the admin the exact values to set on **their** server:

| Setting | Value |
|---|---|
| Router / default gateway | the StayConnect gateway for that VLAN, e.g. `10.20.0.1` |
| DNS | the StayConnect gateway (or the hotel's resolver, if it can reach the walled garden) |
| Option 114 (`Captive-Portal`, RFC 8910) | `http://10.20.0.1:8380/` — **plain HTTP**, the gateway IP, port 8380 |

Vendor syntax examples:

- **ISC dhcpd**: `option v4-captive-portal "http://10.20.0.1:8380/";` (define
  the option code 114, type text, first).
- **Windows DHCP**: add option 114 (String) with value `http://10.20.0.1:8380/`
  on the scope.
- **MikroTik / RouterOS**: DHCP option name `captive-portal`, code 114,
  value string `http://10.20.0.1:8380/`.

If the hotel's DHCP can't set option 114, captive sign-in still works via the
nftables DNAT interception (probe requests get redirected), but auto-pop is less
reliable — recommend they set the option.

## 7. Per-OS behaviour (why this matters)

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

## 8. Confirming it's live

- In the rendered bundle: `kea-dhcp4.json` → the subnet's `option-data` contains
  `{ "name": "v4-captive-portal", "data": "http://<gw>:8380/" }`.
- On a client after a lease: the DHCP ACK carries option 114 (visible in a packet
  capture or the client's network detail on some OSes).
- If it's absent, check `captive_portal_enabled` (it's only emitted for captive
  networks) and `dhcp_mode = local`; `external` networks rely on the hotel's
  server to set it ([EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md)).
