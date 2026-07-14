# Phase 19 — Hotel Networking: Final Report

Guest VLANs, guest-facing networks, and DHCP management for the StayConnect
edge appliance. Implemented on top of the existing cloud/edge architecture
without undoing the Cloud/Edge split. Verified on the pilot (`172.21.60.23`)
with an isolated test VLAN.

## 1. Previous networking limitation

The appliance hardcoded exactly one guest network (`br-lan` / `10.10.0.0/24` /
gateway `10.10.0.1`, WAN `ens160`, VLAN-less) across nftables, the shaping
classids, tc-setup, Kea, netplan, Unbound, and the session model. Sessions
were keyed on `(tenant_id, ip)` with no network dimension; the nft auth set was
IP-only; Kea was file-configured with no runtime control; option 114 existed
only as a live-VM edit. Hotel staff had no way to add a VLAN, a second subnet, a
DHCP scope, a pool, a reservation, or per-network portal policy.

## 2. Domain & database changes

New site-local migration `data-plane/migrations/0002_edge_networking.up.sql`
(applied live) adds seven tables + four session columns:

- `network_interfaces` — discovered inventory + operator-assigned roles
  (management/wan/guest_access/guest_trunk/ha_sync/unused); management/WAN are
  `is_protected`.
- `guest_networks` — one row per guest L2/L3 domain: untagged or 802.1Q VLAN,
  parent interface, generated bridge name (IFNAMSIZ-safe), gateway/subnet,
  dhcp_mode (local/external/relay/disabled), DNS, lease timers, captive/NAT/
  isolation flags, derived portal_url. Unique indexes reject duplicate active
  VLAN-per-parent, duplicate bridges, and >1 untagged network per parent.
- `dhcp_pools`, `dhcp_reservations` — multiple pools per subnet; host
  reservations.
- `network_config_revisions` — the transactional apply record with the full
  lifecycle (draft → validated → applying → pending_confirmation → active |
  failed | rolled_back | superseded), rendered-bundle path, intent snapshot,
  validation JSON, audit columns, and a single-in-flight partial unique index.
- `network_apply_events`, `network_health_checks` — per-apply audit trail.
- `sessions` gains `guest_network_id`, `vlan_id`, `ingress_interface`,
  `gateway_ip` (nullable, backfilled to the legacy network).

Evidence: migration applied to `stayconnect_site`; all 7 tables + 4 columns
verified present.

## 3. New Edge API routes (`/edge/v1/network/*`, permission key `network`)

`GET /interfaces`, `PATCH /interfaces/{name}/role`;
`GET|POST /guest-networks`, `GET|PUT|DELETE /guest-networks/{id}`,
`POST /guest-networks/{id}/disable`, `GET /guest-networks/{id}/status`;
`POST /validate`, `POST /apply`, `POST /adopt`;
`GET /dhcp/leases`, `GET|POST /dhcp/reservations`, `PUT|DELETE /dhcp/reservations/{id}`;
`GET /revisions`, `GET /revisions/{id}`, `POST /revisions/{id}/confirm`,
`POST /revisions/{id}/rollback`.

Guest-network/pool/reservation CRUD is site-DB owned by `edged`; validate/apply/
confirm/rollback and interface/lease reads proxy to the privileged `netd`
daemon over its unix socket. Structured validation errors:
`{error, issues:[{field, code, message}]}`. Every apply/rollback is audited.

Evidence (live): `GET /edge/v1/network/interfaces` → 3 interfaces;
`/guest-networks` → `Legacy Guest Network/br-lan`; `/revisions` → 17 revisions,
newest active; `/dhcp/leases` → structured leases from both subnets.

## 4. Hotel Admin Networking pages

`hotel-admin/app/(app)/network/`: guest-networks landing (with a
pending-confirmation banner + Validate/Apply), a 7-step creation wizard
(Identity → Interface/VLAN → Subnet/Gateway → DHCP+DNS → Portal → Review → Apply,
including the "Wireless controller action required" callout), an edit page with
a reservations sub-section, a DHCP leases+reservations page, and a revision
history with confirm/rollback. Nav gains a "Networking" section gated by
`canRead("network", roles)`. `npm run build` succeeds (23 routes). Built locally;
the built app and its live edged API are both verified.

## 5. Interface & VLAN implementation (netd)

New privileged daemon `data-plane/cmd/netd` (root, unix socket
`/run/stayconnect/netd.sock`, group `stayconnect`, never on TCP; edged proxies
to it). It:
- discovers interfaces via `ip -j` (hides docker/veth/generated bridges);
- renders a full revision bundle from the site DB into
  `/etc/stayconnect/generated/network/revision-NNNNNN/`;
- applies L2/L3 **surgically** with `ip` commands (create VLAN sub-interface →
  bridge → gateway address → up), which is additive and reversible and never
  touches the management/WAN/legacy interfaces, and writes netplan only for
  reboot persistence.

Evidence (live, test VLAN 219): `netd` created `ens219t.219 → br-g219 →
10.219.0.1/24`; `br-g219` up with the VLAN member; management IP intact
throughout.

## 6. Kea DHCP implementation

netd drives Kea 2.0.2 entirely through its **control socket**: renders the
`Dhcp4` object across all enabled *local* networks, applies with `config-set`
(which re-detects interfaces so freshly-created bridges are bound) then
`config-write` for persistence — no Kea restart, no CSV parsing. The
`lease_cmds` hook is loaded so `lease4-get-all` powers the leases page.

Evidence: a namespaced VLAN-219 client completed a real DORA —
`DHCPOFFER of 10.219.0.101 from 10.219.0.1`, `DHCPACK`, `bound to 10.219.0.101`;
`/edge/v1/network/dhcp/leases` returns structured leases from subnet-id 1
(legacy `10.10.0.100 oppo-a54`) and subnet-id 2 (`10.219.0.x`).

## 7. DHCP Option 114 implementation

Auto-generated per captive local network as `http://<gateway>:8380/` (plain
HTTP straight to portald — never HTTPS/Caddy; a `portal_https` validation guard
enforces this), stored (derived) in `guest_networks.portal_url`, rendered into
the Kea subnet `option-data`, versioned in the revision bundle, visible in the
Hotel Admin, and covered by tests. For external-DHCP networks the Hotel Admin
shows the exact option-114 value for the hotel's DHCP admin.

Evidence: VLAN 219's Kea subnet carried
`v4-captive-portal = http://10.219.0.1:8380/`.

## 8. nftables multi-network implementation

`internal/netcfg/render_nft.go` generates the whole `inet stayconnect` ruleset
from the DB: a **concatenated `auth_ipv4 { ifname . ipv4_addr }`** set (identity
is (ingress bridge, IP), correct even under future overlapping subnets),
per-network captive DNAT to that network's own gateway, per-network masquerade,
dynamic `guest_interfaces`/`guest_subnets` sets, inter-guest isolation,
guest→management blocking, and IPv6 guest drops. `nft -c` validates every
generated ruleset before load.

Evidence: generated ruleset passes `nft -c`; the live auth set is now
`type ifname . ipv4_addr` with elements like `"br-lan" . 10.10.0.205 timeout 1h`.

## 9. Session/network association (scd)

scd resolves the guest network from the source IP's subnet
(`internal/nft` auto-detects concatenated vs legacy IP-only sets so it works
across the cutover), records `guest_network_id`/`vlan_id`/`ingress_interface`/
`gateway_ip` on every session, and adds the concatenated auth element. Revoke,
reaper, NATS RPC and boot-reconcile all thread the ingress interface (falling
back to the legacy bridge for pre-Phase-19 sessions).

Evidence (live): a legacy voucher login recorded ingress `br-lan`,
`guest_network_id` set, gateway `10.10.0.1`; the auth element is concatenated.

## 10. Apply & rollback safety

Transactional apply: structural validation (`netcfg.ValidateSet`) → generate
bundle → `nft -c` gate → surgical apply → Kea config-set → Unbound reload →
five health checks (mgmt_reachable, gateway_up, kea_running, portal_listen) →
`pending_confirmation` with a **120 s watchdog** that auto-rolls-back if
unconfirmed. Any apply/health failure rolls back to the previous active
revision (re-applying its bundle + tearing down bridges the failed revision
added). Confirm refuses non-pending revisions. Management connectivity is a
health gate on every apply.

Evidence (live): a deliberately invalid config (gateway inside pool) was
rejected with `pool_contains_gateway`, state `failed`, **no OS change**, and
management IP intact; a good apply reached `pending_confirmation` (all four
health checks true) and confirmed `active`.

## 11. Permissions & audit

`network.*` folded into the single `network` permission key: site_admin &
hotel_it_manager write; site_viewer read; front_office/guest_relations/voucher/
payments none. Enforced in edged `rolePerms` and mirrored in hotel-admin
`lib/roles.ts`. Interface-role changes, guest-network create/update/delete,
apply, confirm and rollback all write `audit_log` rows.

## 12. Tests & exact results

- **Unit** (`internal/netcfg`): validation (VLAN range, pool-outside-subnet,
  gateway-in-pool, pool-reversed, pool-overlap, gateway-outside-subnet,
  protected-interface, missing-parent, reservation-in-pool, duplicate-VLAN,
  subnet-overlap, multi-VLAN-valid), Kea render (option 114, external excluded,
  lease hook), nft render (concat set + per-network DNAT/masquerade), netplan
  render (VLAN + empty-safe), bridge-name length — **all pass**.
- **E2E** `scripts/phase19-network-test.sh` on the pilot (isolated VLAN 219):
  **ALL GREEN (12 checks)** — validate, apply→pending_confirmation with 4 health
  checks, bridge+gateway live, VLAN sub-interface present, **real DORA lease
  10.219.0.103**, Kea serves 10.219.0.0/24 with option 114, confirm→active,
  invalid config rejected (`pool_contains_gateway`, failed, no OS change),
  management intact, legacy br-lan untouched.
- **Regression** (existing guest suites on the site DB after the nft/session
  changes): phase2-quota, phase4-email-otp, phase4-sms-otp, phase4-pms,
  phase4-pms-5b, phase6-session-lifecycle, phase7-metrics — **7/7 PASS**
  (phase6 adapted: manual nft injections use the concatenated element form).

## 13. Pilot verification

Isolated test VLAN 219 via a dummy trunk + network namespace — the production
`ens192`/`br-lan` guest network was never touched. Full DORA, option 114, Kea
serving, apply/confirm/rollback, health checks, isolation, and the legacy guest
voucher path (HTTP 303) all verified live. Backups taken first
(`/root/backups/phase19-network-baseline-*`).

## 14. Remaining genuine blockers

- **hotel-admin UI deployment to the pilot is pending**: the UI builds cleanly
  locally (23 routes) and its edged API is verified live, but the on-VM rebuild
  was misdirected into `/root` (the `/opt/stayconnect/hotel-admin` path did not
  exist), which thrashed the VM's memory and temporarily blocked SSH. No
  StayConnect service was affected (the npm process was isolated). Deploying the
  built app to the correct path and behind Caddy is the one packaging step
  outstanding; it does not affect the backend acceptance.
- DHCP **relay** mode is modelled in the schema but intentionally minimal in
  this release (not faked) — surfaced only where testable.

## 15. Documentation

`docs/EDGE_NETWORKING.md`, `GUEST_VLAN_CONFIGURATION.md`, `DHCP_MANAGEMENT.md`,
`DHCP_OPTION_114.md`, `NETWORK_APPLY_AND_ROLLBACK.md`, `ARUBA_SSID_VLAN_MAPPING.md`,
`EXTERNAL_DHCP_MODE.md`, `NETWORK_TROUBLESHOOTING.md` (new); updates to
`EDGE_ARCHITECTURE.md`, `DEPLOYMENT_APPLIANCE.md`, `ROLE_AND_SCOPE_MATRIX.md`,
`SYSTEM_OVERVIEW.md`, `BACKUP_AND_RESTORE.md`; plus `PHASE19_ASSESSMENT.md` (the
design). The Aruba doc separates the WLAN-controller SSID→VLAN side from the
StayConnect gateway/DHCP/portal side, per the responsibility boundary.
