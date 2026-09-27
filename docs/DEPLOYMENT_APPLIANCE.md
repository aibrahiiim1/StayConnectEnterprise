# Deployment — Appliance (Edge)

> Production layout for one hotel appliance (or HA pair). Everything the
> guest and the hotel staff touch runs here, against the site-local database.
> Cloud counterpart: [DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md).

> **⚠️ Topology correction (2026-07-16) — approved two-NIC rule governs.** The approved,
> permanent appliance topology is **exactly two physical NICs: WAN and LAN.** **WAN is
> also the management interface** (Hotel Admin, SSH, outbound sync, PMS reachability if the
> PMS is on the WAN-side hotel network); **LAN** carries guest connectivity and guest
> VLAN/trunk behavior. There is **no separate physical management NIC** and **no approved
> third HA-sync NIC.** The historical "separate `mgmt` at `172.21.15.30`" and "optional
> `hasync` third NIC" wording below is **superseded** — see `SYSTEM_OVERVIEW.md` (WAN=`ens160`,
> LAN=`ens192`) and `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` (WAN/LAN). Where this file
> still says "mgmt", read it as **"the WAN/management interface"**.

## 1. Interfaces & addressing (approved two-NIC model)

| Interface | Example | Role |
|---|---|---|
| **WAN = management** (`ens160`) | `the retired development reference appliance's address`, default route | uplink/masquerade **and** management: Hotel Admin (`https://<WAN-IP>`), SSH, outbound HTTPS to Central (licensing only), PMS reachability when the PMS is on the WAN-side hotel network, monitoring |
| **LAN = guest** (`ens192`, over `br-lan` / per-VLAN bridges) | `10.20.0.1/24` (and per-VLAN gateways) | guest gateway: DHCP/DNS/captive portal/shaping + 802.1Q guest VLAN trunk; option 114 → `http://10.20.0.1:8380/` (**keep the RFC 8910 stanza in the repo Kea config — it was VM-only drift once already**) |

Guest traffic masquerades out the **WAN** interface, never onto the guest LAN. ESXi installs:
the LAN portgroup needs Promiscuous/MAC-changes/Forged-transmits = Accept (SYSTEM_OVERVIEW §3).

**Superseded:** a third **`hasync`** NIC (e.g. `ens224`) for VRRP/conntrackd/Postgres
replication is **no longer part of the approved topology**. Under the two-NIC rule the HA
synchronization transport is an **OPEN architecture decision** (see §7 and
[TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md) §6) — do not assume a dedicated sync NIC.

## 2. Software stack (systemd)

| Unit | Component | Notes |
|---|---|---|
| `postgresql` | local Postgres 16 (+TimescaleDB where available) | database `stayconnect_site`, site-only credentials; loopback |
| `stayconnect-scd` | session controller **+ Central agent** (token-less registration, assignment, certificate, licence fetch, hello — [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md) §6) | root (CAP_NET_ADMIN); `SCD_DB_URL` → site DSN; `SCD_CTRLAPI_BASE` / `SCD_MTLS_BASE` from `deploy/config/central-endpoint.env` (`https://sc-central.echofusion.com`, mTLS `:9443`). scd has no message-bus client and no telemetry, command or update settings (`SCD_NATS_URL`, `SCD_NATS_MTLS_URL`, `SCD_COMMAND_PUB`, `SCD_UPDATE_PUB` are removed — CLAUDE.md §0E). `SCD_REMOVED_MARKER` (default `/etc/stayconnect/removed-from-central.json`) is the removed-from-Central marker |
| `stayconnect-portald` | captive portal | user `stayconnect`, guest iface :8380/:8343 |
| `stayconnect-acctd` | accounting/quotas | root (tc); site DSN |
| `stayconnect-edged` | Hotel Admin API `/edge/v1` + serves `hotel-admin/` | loopback listener, fronted by Caddy on mgmt; site DSN; reads license store |
| `kea-dhcp4` / `unbound` | guest DHCP/DNS | bound to 10.20.0.1 |
| `nftables` | `inet stayconnect` ruleset | see §3 |
| `stayconnect-caddy` | TLS for Hotel Admin on the **mgmt IP only** | internal CA (`local_certs`) unless the site has real names |
| backup agent (timer) | nightly `pg_dump` → `backup_records` | [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §1 |
| monitoring | scd/edged Prometheus endpoints, loopback | scraped locally; nothing is sent to Central as telemetry |
| software updates | no on-appliance update agent | staged binary rollout via the deployment procedure |

`deploy/scripts/install-service-units.sh` installs the appliance units from `deploy/systemd/` and **skips
the OneGate Central units** that share that directory (`stayconnect-ctrlapi`, `stayconnect-cloud-admin`,
`stayconnect-central-backup`); Central's own installer is `deploy/scripts/central-install.sh`
([DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md)).

On-disk state that must survive reinstalls: `/etc/stayconnect/identity/`
(Ed25519 keypair), `/etc/stayconnect/license/` (current.json, state.json,
revoked.json), env files, and the Postgres data dir.

## 3. nftables policy (deltas vs the pilot ruleset)

- input (drop default): mgmt allows SSH 22 + Caddy 443 (Hotel Admin) **from the
  mgmt VLAN only**; guest allows DHCP/DNS/8380/8343/ICMP. **No 8080/3000
  accepts anywhere** ([SECURITY_HARDENING.md](SECURITY_HARDENING.md) §2).
- forward: guest→uplink iff `saddr @auth_ipv4` or `daddr @walled_garden_ip`;
  guest→mgmt VLAN explicitly dropped.
- **IPv6: dropped on the guest LAN** (no RAs, no v6 forwarding from br-lan)
  until dual-stack capture exists ([SECURITY_HARDENING.md](SECURITY_HARDENING.md) §4).
- prerouting DNAT :80→10.20.0.1:8380, :443→10.20.0.1:8343 for unauthenticated
  guests; masquerade guest subnet out the uplink.

## 4. Caddy exposure

One vhost: `https://172.21.15.30` → hotel-admin static bundle + `/edge/v1/*`
reverse-proxy to edged (loopback). Bind the listener to the mgmt address —
never `:443` on all interfaces. Guest portal traffic does **not** pass Caddy
(portald serves the captive path directly; plain HTTP is required for
RFC 8910/probe flows). Hotel staff import the appliance's internal CA root
once, or the site installs a real cert.

## 5. Outbound connectivity (all appliance-initiated)

| Destination | Protocol | Purpose |
|---|---|---|
| `sc-central.echofusion.com:443` | HTTPS | token-less registration, and all appliance calls before a certificate exists |
| `sc-central.echofusion.com:9443` | HTTPS, mutual TLS | assignment, licence, certificate renewal, hello once a certificate is issued |
| Twilio / SendGrid / Google / Stripe / Mews / Apaleo | HTTPS | only if the respective feature is enabled |
| hotel PMS (FIAS) | TCP on the hotel LAN | local — not internet |

No inbound rule from the internet exists at all. The hotel firewall needs only
these outbound allowances; a hotel that blocks them still has working guest
WiFi ([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md)).

## 6. Bring-up order (new site)

1. OS, netplan (**WAN/management + LAN/guest** — two NICs; no dedicated hasync NIC),
   sysctl, nftables, Kea (incl. option 114), Unbound. (No tc priming unit: netd owns the HTB roots,
   the guest IFB and the ingress redirect, and rebuilds them from zero on every pass.)
2. Local Postgres → create `stayconnect_site` + role → apply
   `data-plane/migrations/0001_edge_init.up.sql`.
3. Install binaries + env files (`deploy/scripts/install-central-endpoint.sh`,
   vendor trust key, assignment root anchor); start scd — the identity keypair
   is generated and the appliance **registers itself** (no token) and shows
   *Waiting for activation* in Central.
4. **Activate** it in Central (customer, site, licence terms); scd collects the
   signed assignment, certificate and licence and installs the licence
   (populates `tenant_effective_limits`). For a site without internet, use
   offline activation (activation request → activation package) via Hotel
   Admin → **Appliance & licence**.
5. Start portald, acctd, edged, Caddy; seed the first `site_admin` operator.
6. Verify: phase 1/2 suites (guest path), Hotel Admin login on the mgmt IP,
   **Appliance & licence** shows *Activated*, licence *Active*, OneGate Central
   *Connected* (`GET /edge/v1/central/status`), and Central shows the appliance
   **Activated** and **Connected**.
7. Run the offline drill and one reboot drill before handing the site over.

## 7. HA pair

> **Superseded transport / OPEN decision:** the HA design below was written for a **third
> dedicated `hasync` NIC**, which the approved **two-NIC (WAN+LAN)** rule removes. The HA
> *behaviors* (VRRP on the guest VIP, connection-tracking sync, nft-set replication,
> Postgres streaming replication, manual split-brain fencing) are **preserved as intent**,
> but the **synchronization transport over a two-NIC appliance is an OPEN architecture
> decision — not yet defined or implemented.** Do **not** claim any WAN/LAN HA transport is
> implemented. HA overall remains a documented, not-yet-implemented limitation.

Second appliance: same stack; keepalived VRRP on the guest VIP (10.20.0.1),
conntrackd connection-tracking sync, nft `auth_ipv4` replication (the earlier message-bus
replication over `nft.<siteID>` was removed with scd's NATS transport; a new transport is part of
the OPEN decision).
Site DB: primary runs Postgres with **streaming replication** to the secondary; failover
promotes the replica (VRRP notify hook), edged/scd on the survivor keep their loopback DSN.
Both nodes appear in the license's `appliance_ids` and each keeps its own cloud
identity. **HA-sync transport (which link carries VRRP/conntrackd/replication
under two NICs) is the OPEN decision above.** Split-brain: two nodes cannot arbitrate on
their own — a recommended cloud witness is documented in
[TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md) §6 (known limitation, not yet
implemented); until then, alert loudly on dual-master (both nodes reporting VRRP MASTER
locally) and fence manually.

## 8. Appliance sizing (guidance)

Pilot-verified on a modest VM: 2 vCPU / 4 GB / 40 GB serves a mid-size hotel
(hundreds of concurrent devices; scd's nft/tc ops are O(1) per session).
Postgres and accounting growth are bounded by license retention limits;
nightly backups need headroom for one extra dump generation.

## 9. Phase 19 — Networking

Guest networks/VLANs/DHCP are now DB-driven and applied by `netd`. Full suite:
[EDGE_NETWORKING.md](EDGE_NETWORKING.md).

### `netd` systemd unit

| Unit | Component | Notes |
|---|---|---|
| `stayconnect-netd` | privileged network config daemon | root; listens **only** on `/run/stayconnect/netd.sock` (group `stayconnect`, 0660, no TCP); renders + applies netplan/Kea/nftables/Unbound from the site DB; owns validate/apply/health/rollback. edged proxies `/edge/v1/network/*` to it. Ordered before `kea-dhcp4`/`nftables`/`unbound` reconcile. |

### Guest-trunk interface

On the **LAN** interface (§1), the guest-facing NIC assigned the **`guest_trunk`**
role carries **tagged** 802.1Q guest VLANs from the WLAN controller (e.g. `ens192`
as a trunk; VLAN 20 → `ens192.20` → `br-g20` → `10.20.0.1/22`). The trunk parent
is address-less (an L2 trunk); StayConnect owns the per-VLAN gateway. A plain
untagged guest port uses role `guest_access` instead. See
[ARUBA_SSID_VLAN_MAPPING.md](ARUBA_SSID_VLAN_MAPPING.md).

### Generated config directory

Each apply renders a numbered bundle under
`/etc/stayconnect/generated/network/revision-NNNNNN/` (netplan.yaml,
kea-dhcp4.json, stayconnect.nft, unbound.conf). The **active** revision's bundle
is the live config; the static `deploy/…` files are bootstrap skeletons. This
directory must survive reinstalls along with `/etc/stayconnect/identity` and
`/etc/stayconnect/license` (§2).

### Kea control socket

`kea-dhcp4` runs with the Unix control socket `/run/kea/kea4-ctrl-socket`. netd
drives DHCP online via `config-test` → `config-set` → `config-write` (persists to
`/etc/kea/kea-dhcp4.conf`) and reads leases via `lease4-get-all` — **Kea is never
restarted** to change DHCP, and leases are never read from the memfile CSV
([DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md)).

Bring-up (§6) is unchanged except that after the base netplan/Kea/nftables/Unbound
skeletons, netd imports the legacy `br-lan` as the first guest network (marked
already-active, zero disruption) and thereafter owns guest-network changes.
