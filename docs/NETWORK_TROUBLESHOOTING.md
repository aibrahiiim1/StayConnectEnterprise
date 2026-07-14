# Network Troubleshooting (Phase 19)

> Symptom → check → fix runbook for guest networks, VLANs, DHCP and captive
> portal. Generated artifacts live under
> `/etc/stayconnect/generated/network/revision-NNNNNN/`; audit lives in
> `network_apply_events` / `network_health_checks`. Overview:
> [EDGE_NETWORKING.md](EDGE_NETWORKING.md).

Quick orientation: the **active** revision's bundle is the live config. Compare
what's on disk (`/etc/stayconnect/generated/network/revision-NNNNNN/`) with
what's actually running (`nft list ruleset`, `ip -br addr`, Kea `status-get`).

## 1. No DHCP lease

| Check | How | Fix |
|---|---|---|
| Is the network `dhcp_mode = local`? | Hotel Admin, or `guest_networks.dhcp_mode` | `external`/`disabled` means Kea won't serve it — see [EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md) |
| Does the subnet have a pool? | `dhcp_pools` for the network; validator raises `no_pool` | add a pool inside the subnet, excluding gateway/network/broadcast |
| Is Kea running & bound to the bridge? | `kea` `status-get`; `interfaces-config.interfaces` should list `br-g<vlan>/<gateway>` | if not bound, re-apply; check the bridge exists (`ip -br link`) |
| Is the bridge up with the gateway IP? | `ip -br addr show br-g20` → `10.20.0.1/22` | if missing, netplan wasn't applied — re-apply the revision |
| Do requests reach the bridge? | `iifname "br-g20" udp dport {67,68} accept` present in `nft list ruleset` | present by default in the generated input chain; if absent, re-render |
| Client on the right VLAN? | see §3 — trunk/tagging on the switch/AP | fix the controller trunk |

Leases page empty but clients have IPs → the leases view uses Kea
`lease4-get-all` filtered by `user-context.guest_network_id`; confirm the subnet
carries the right `user-context`.

## 2. Portal doesn't open

| Check | How | Fix |
|---|---|---|
| Option 114 present & plain HTTP? | subnet `option-data` has `v4-captive-portal` = `http://<gw>:8380/` | must be HTTP not HTTPS ([DHCP_OPTION_114.md](DHCP_OPTION_114.md)); validator code `portal_https` |
| Is DNAT interception in place? | `nft list ruleset` prerouting_nat: `iifname "br-g20" … tcp dport 80 dnat ip to 10.20.0.1:8380` (and 443→8343) | only emitted when `captive_portal_enabled`; enable it and re-apply |
| Is portald listening on the gateway? | health check `portal_listen`; `ss -ltnp | grep 8380` | restart portald; confirm it binds the guest interface |
| Ingress interface correct? | client's traffic must arrive on `br-g20` | wrong VLAN/trunk → §3 |
| Client already authenticated? | auth set `auth_ipv4` element `br-g20 . <ip>` | authenticated clients skip the portal by design; deauth to retest |

## 3. VLAN not passing traffic

| Check | How | Fix |
|---|---|---|
| Is the VLAN trunked/tagged to the appliance? | switch/AP uplink port must be an 802.1Q trunk with the VLAN tagged | fix the controller/switch — **StayConnect does not configure it** ([ARUBA_SSID_VLAN_MAPPING.md](ARUBA_SSID_VLAN_MAPPING.md)) |
| Did netplan apply? | `ip -br link show ens192.20` exists; `ip -br link show br-g20` UP | re-apply the revision; check `netplan generate` gate passed |
| Is the parent a trunk with the VLAN device? | rendered netplan `vlans: ens192.20 {id:20, link:ens192}` | present in the bundle; if the parent has an IP it's mis-roled — parent should be `guest_trunk`, address-less |
| Is the bridge carrying the VLAN device? | `bridge link show` → `ens192.20` master `br-g20` | if not enslaved, re-apply |
| ESXi vSwitch dropping tags? | LAN portgroup needs Promiscuous / MAC-changes / Forged-transmits = Accept | set them on the portgroup (SYSTEM_OVERVIEW §3) |

## 4. Apply rolled back

An apply that reverts leaves a `rolled_back` revision. Find out why:

| Where | What it tells you |
|---|---|
| `network_config_revisions.failure_reason` | one-line cause |
| `network_apply_events` (by `revision_id`, ordered by `at`) | the phase that failed: `validate|snapshot|generate|apply|health|commit|rollback` |
| `network_health_checks` (by `revision_id`) | which named check failed: `mgmt_reachable|gateway_up|kea_running|portal_listen|dns` |

Common causes:

- `mgmt_reachable` failed → the change disrupted management; **this is the
  lockout guard** — nothing to fix on the guest side, review what touched the
  management path (it shouldn't; the netplan renderer never emits mgmt/WAN).
- `kea_running` failed → the rendered Kea config was accepted by `config-test`
  but Kea didn't come healthy; check `/var/log/kea/kea-dhcp4.log`.
- Watchdog timeout → nobody confirmed within 120 s. Re-apply and click
  **Confirm** ([NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md) §6).
- Validation gate → structured issues by field/code; fix per the table in
  [DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md) §5.

## 5. Inter-guest isolation (traffic leaking between guest networks)

By design, guests on one network cannot reach another guest subnet:
`iifname @guest_interfaces ip daddr @guest_subnets drop` in the forward chain.
If cross-network traffic is getting through:

| Check | Fix |
|---|---|
| Is the target subnet in `@guest_subnets`? | it's populated from all enabled `subnet_cidr`s; re-apply so the set is current |
| Are both bridges in `@guest_interfaces`? | populated from enabled `bridge_name`s; re-apply |
| `client_isolation_enabled` for intra-network isolation | that's a separate per-network flag (clients on the *same* network); enable and re-apply |

## 6. Management lockout recovery

Management/WAN are protected and never in the applied set, and every apply
health-checks `mgmt_reachable` with a 120 s auto-rollback — so a guest-network
change should not lock you out. If you are still locked out of Hotel Admin:

1. **Wait for the watchdog.** If an apply is `pending_confirmation`, it
   auto-reverts at `confirm_deadline` (≤ 120 s) and management returns.
2. **Console / SSH to the appliance** on the management interface (unaffected by
   guest applies).
3. Inspect the in-flight revision:
   `SELECT seq,state,failure_reason FROM network_config_revisions
   WHERE state IN ('applying','pending_confirmation');`
4. Force a rollback to the last good revision via netd (the same path
   `POST /edge/v1/network/rollback` uses), which re-applies `previous_seq`'s
   bundle.
5. As a last resort, re-apply the bootstrap skeletons
   (`deploy/netplan`, `deploy/nftables/stayconnect.nft`) to restore a known
   baseline, then reconcile the DB intent.

Because the site DB is the source of truth and each revision keeps its full
bundle + `previous_seq`, recovery is always "re-apply a known-good revision,"
never "reconstruct config by hand."
