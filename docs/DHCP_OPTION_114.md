# DHCP Option 114 — Captive-Portal API (RFC 8910)

> The one DHCP option that makes captive-portal sign-in pop automatically across
> iOS, Android, macOS and Windows. StayConnect generates it per captive local
> network. Renderer: `data-plane/internal/netcfg/render_kea.go`; URL builder:
> `netcfg.PortalURLFor` (`validate.go`). Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

## 1. What it is

RFC 8910 defines DHCP option **114**: the network hands the
client a **URL for the captive-portal API** in its DHCP ACK. Modern client OSes
read it and open the sign-in page proactively, instead of relying on probe
requests (`captive.apple.com`, `connectivitycheck.gstatic.com`, MSFT NCSI) being
intercepted. It is the single most reliable auto-pop mechanism across all OSes.

## 2. What the value is: the HTTPS captive API, or the HTTP portal in fallback

Since the HTTPS Client Portal ([contract](architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md) §4, §6) the
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

## 3. Auto-generated per captive local network

The option is emitted **only** for networks that are both `dhcp_mode = local`
and `captive_portal_enabled = true`. Each network gets its **own** gateway in the
URL, so a client on VLAN 20 is pointed at `http://10.20.0.1:8380/` and a client
on VLAN 40 at `http://10.40.0.1:8380/`. In `RenderKeaDhcp4` the option is
appended to that subnet's `option-data`:

```json
{ "code": 114, "space": "dhcp4", "data": "http://10.20.0.1:8380/" }
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
  { "code": 114, "space": "dhcp4",  "data": "http://10.20.0.1:8380/" }
]
```

Code 114 has a standard string definition in Kea 2.0.2 and later, so no custom option definition is needed.
The NAME must not be used on Kea 2.0 (it means code 160; see §2).

## 6. What to tell an external-DHCP admin

When the site runs its own DHCP server ([EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md)),
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

- In Kea's running configuration (`config-get`): the subnet's `option-data` has an entry with `"code": 114`
  and the value from §2. An entry named `v4-captive-portal` with code 160 is the old, never-delivered form.
- `scripts/portal-https-e2e-test.sh` leases a guest-network client and asserts the value it actually received.
- On a client after a lease: the DHCP ACK carries option 114 (visible in a packet
  capture or the client's network detail on some OSes).
- If it's absent, check `captive_portal_enabled` (it's only emitted for captive
  networks) and `dhcp_mode = local`; `external` networks rely on the site's
  server to set it ([EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md)).
