# Deployment — Appliance (Edge)

> Production layout for one hotel appliance (or HA pair). Everything the
> guest and the hotel staff touch runs here, against the site-local database.
> Cloud counterpart: [DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md).

## 1. Interfaces & addressing (reference example)

| Interface | Example | Role |
|---|---|---|
| **mgmt** (`ens160`) | `172.21.15.30/24`, hotel IT VLAN, default route | Hotel Admin (`https://172.21.15.30`), SSH, outbound sync (NATS + license HTTPS), PMS reachability if the PMS lives on the hotel LAN, monitoring |
| **guest** (`br-lan` over `ens192`) | `10.20.0.1/24` | guest gateway: DHCP/DNS/captive portal/shaping; DHCP pool 10.20.0.100–250; option 114 → `http://10.20.0.1:8380/` (**keep the RFC 8910 stanza in the repo Kea config — it was VM-only drift once already**) |
| **hasync** (optional, `ens224`) | `169.254.7.1/30` p2p | VRRP, conntrackd FTFW, Postgres streaming replication |

Uplink note: WAN/masquerade may share mgmt or be a separate interface per the
hotel's topology; guest traffic masquerades out the uplink, never into the
mgmt VLAN. ESXi installs: LAN portgroup needs Promiscuous/MAC-changes/Forged-
transmits = Accept (SYSTEM_OVERVIEW §3).

## 2. Software stack (systemd)

| Unit | Component | Notes |
|---|---|---|
| `postgresql` | local Postgres 16 (+TimescaleDB where available) | database `stayconnect_site`, site-only credentials; loopback |
| `stayconnect-tc-setup` | HTB roots (oneshot) | before scd |
| `stayconnect-scd` | session controller **+ sync agent** (outbox drain, license fetch, config subscriber, heartbeat) | root (CAP_NET_ADMIN); `SCD_DB_URL` → site DSN; `SCD_CTRLAPI_BASE=https://api.<domain>`; `SCD_NATS_URL` with per-appliance creds |
| `stayconnect-portald` | captive portal | user `stayconnect`, guest iface :8380/:8343 |
| `stayconnect-acctd` | accounting/quotas | root (tc); site DSN |
| `stayconnect-edged` | Hotel Admin API `/edge/v1` + serves `hotel-admin/` | loopback listener, fronted by Caddy on mgmt; site DSN; reads license store |
| `kea-dhcp4` / `unbound` | guest DHCP/DNS | bound to 10.20.0.1 |
| `nftables` | `inet stayconnect` ruleset | see §3 |
| `stayconnect-caddy` | TLS for Hotel Admin on the **mgmt IP only** | internal CA (`local_certs`) unless the site has real names |
| backup agent (timer) | nightly `pg_dump` → `backup_records` | [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §1 |
| monitoring | scd/edged Prometheus endpoints, loopback | scraped locally; fleet-level health goes up as telemetry, not scrapes |
| update agent | Roadmap — update orchestration not yet implemented | until then: staged binary rollout via ops procedure |

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
| `nats.<domain>:4222` | NATS/TLS, per-appliance creds | telemetry drain, heartbeat, config events, RPC subscription |
| `api.<domain>:443` | HTTPS | enrollment (first boot), license fetch |
| Twilio / SendGrid / Google / Stripe / Mews / Apaleo | HTTPS | only if the respective feature is enabled |
| hotel PMS (FIAS) | TCP on the hotel LAN | local — not internet |

No inbound rule from the internet exists at all. The hotel firewall needs only
these outbound allowances; a hotel that blocks them still has working guest
WiFi ([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md)).

## 6. Bring-up order (new site)

1. OS, netplan (mgmt/guest/hasync), sysctl, nftables, tc-setup, Kea (incl.
   option 114), Unbound.
2. Local Postgres → create `stayconnect_site` + role → apply
   `data-plane/migrations/0001_edge_init.up.sql`.
3. Install binaries + env files; **enroll**: mint a bootstrap token in
   cloud-admin, set `SCD_BOOTSTRAP_TOKEN`/`SCD_SERIAL`, start scd — identity
   keypair is generated and registered.
4. Issue the site license in cloud-admin; scd fetches and installs it
   (populates `tenant_effective_limits`); or upload the envelope manually via
   Hotel Admin for dark sites.
5. Start portald, acctd, edged, Caddy; seed the first `site_admin` operator.
6. Verify: phase 1/2 suites (guest path), Hotel Admin login on the mgmt IP,
   `GET /edge/v1/license` = Active, telemetry visible in `/cloud/v1/fleet`.
7. Run the offline drill and one reboot drill before handing the site over.

## 7. HA pair

Second appliance: same stack; keepalived VRRP on the guest VIP (10.20.0.1),
conntrackd over hasync, nft `auth_ipv4` replication via `nft.<siteID>`.
Site DB: primary runs Postgres with **streaming replication** over hasync to
the secondary; failover promotes the replica (VRRP notify hook), edged/scd on
the survivor keep their loopback DSN. Both nodes appear in the license's
`appliance_ids` and each keeps its own cloud identity/heartbeat. Split-brain:
two nodes cannot arbitrate on their own — recommended cloud-heartbeat witness
is documented in [TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md) §6 (known
limitation, not yet implemented); until then, alert loudly on dual-master
(both nodes reporting VRRP MASTER in telemetry) and fence manually.

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

Alongside mgmt/guest/hasync (§1), a guest-facing NIC assigned the **`guest_trunk`**
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
