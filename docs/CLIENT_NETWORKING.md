# Client networking

How a client network is prepared, switched on, changed, recovered and given its own portal and sign-in. It describes what is implemented
and tested; **PRE-LIVE acceptance on real Kea and a real switch has not been performed** (see §12).

Related: [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md) (the apply/confirm/rollback engine),
[DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md), [EDGE_NETWORKING.md](EDGE_NETWORKING.md) (the original model),
[NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md).

## 1. The prepared Access network

A new appliance offers one **prepared, not active** client network. It is a definition (`internal/netcfg/factory.go`), not a row: nothing exists
and nothing serves clients until a signed site assignment is in place and an administrator activates it.

| | |
|---|---|
| Name | Client Wi-Fi |
| Mode | LAN Access (untagged) on the client port netd reports (never assumed) |
| Subnet / gateway | `10.20.0.0/20`, `10.20.0.1` |
| DHCP | local, pool `10.20.0.10` – `10.20.15.254` |
| Services | appliance DNS, sign-in page, NAT, internet, the site's default portal profile |
| PMS | none: room sign-in is not offered until a PMS is routed to the network |

The address is **never forced**. If an enabled network already uses addresses in `10.20.0.0/20`, a network is already called "Client Wi-Fi", or the WAN /
management network overlaps it, the prepared network is **blocked** and says why. If the client port already carries networks they are **adopted**
unchanged; nothing is created.

**Activate** (Admin Console → Client networks → Factory network) requires the acknowledgement that no other DHCP server serves the segment. It creates the
row switched off and stages a switch-on (§2); the network serves clients only after the change is applied *and confirmed*.

## 2. Staged changes

Activation, Access/Trunk conversion and the three recovery operations switch networks on and off, and change their settings, **together or not at all**.
Each is recorded in `iam_v2.guest_network_changes` (migrations 0151, 0153):

```
PENDING  → asked for; nothing anywhere has changed; can be cancelled
APPLIED  → the apply wrote it, inside the confirmation window (what it replaced is kept in `before`)
CONFIRMED→ the operator kept it
REVERTED → the apply failed, was rolled back or timed out: everything is put back and the change is PENDING again
```

`kind` is `ENABLE`, `DISABLE` or `SETTINGS` (settings and layers without a switch). `group_id` ties one operation's changes together; Client networks lists
and cancels them as one. A network has at most one live change. The advisory lock per site serialises staging.

## 3. Access and Trunk

Each non-protected port is **Access** (one untagged network) or **Trunk** (tagged VLANs, optionally a native untagged network). Converting is previewed
first: networks that would be created (switched on when applied), networks that would be switched off with their connected-client count, and what the
appliance can **not** verify — switch trunk allow-lists, virtual-switch port-group VLANs, access-point mapping — listed separately as *check outside the
appliance*. The WAN/management port is protected and never offered.

## 4. Status and diagnostics

A network's status is derived from what was confirmed, not what was saved (`internal/netstatus`): *Not activated, Activating, Active, Active – needs
attention, Disabled, Change not applied, Last change failed*. A disabled network that was once confirmed needs **no new activation** to come back.

Diagnostics report what the appliance **observes** (link, bridge, address, DHCP socket, counters, the Kea file) and separately what must be checked
outside it. Zero frames received on a VLAN device does not prove where frames were lost.

## 5. Recovery

Admin Console → Client networks → **Recovery**. Each operation is **previewed** (what is switched on/off, settings put back, devices that lose access, the
layered review, the outside-the-appliance checklist, what is not touched), then **staged** (§2), then applied, kept or rolled back like any change.

| Operation | What it does |
|---|---|
| **Restore last working** | Source: the intent of the last *active* (confirmed) revision. Settings that drifted are put back, networks created or switched on since are switched off. A network moved to another port/VLAN since, or deleted, is reported and left to a replacement. |
| **Restore factory defaults** | Brings the LAN back to the approved Access/untagged `10.20.0.0/20` network with local DHCP and switches every other client network off. If the prepared network is still the untagged network on the client port it is put back in place (name, addresses, pool, DHCP). If it was turned into a VLAN, moved, deleted, or never activated, a **new** one with the prepared settings is created and switched on in the same change; the old row stays, switched off, with its settings and history. A trunk becomes Access because every tagged network is switched off. Refused, with the reason, if the WAN overlaps `10.20.0.0/20`, if the client port is the WAN/management port, or if a recovery is already staged. |
| **Full client networking reset** | As factory defaults, plus which portal design each network shows and each network's sign-in options go back to the site's. Requires typing the site name. |

**All or nothing.** A preview says whether the recovery is *stageable*. If any part of it cannot be done — for Restore last working, a network that was
deleted or moved to another port or VLAN since the last confirmation; for either factory operation, a conflict — **nothing is staged**
(`409 cannot_restore` or `conflict`); the preview still shows what could have been restored. A partial restore would be a configuration nobody confirmed.
Cancelling a staged reset removes the prepared network it created (it was never applied); everything else stays. Switching the external switch, virtual
switch or access point is never done implicitly: the preview lists what must be checked there.

**Never touched by any of them:** the networks themselves (nothing is deleted: identity and history stay, switched-off networks remain listed), guest
records, sessions, devices, packages, licensing and the signed assignment, the WAN/management port, the PMS configuration and its routing, and the portal
designs themselves.

### Version-aware review

At every confirmation the appliance records, per network, its portal profile, its sign-in options and its PMS routing
(`iam_v2.network_layer_snapshots`). Restore last working compares the present with that record layer by layer:

* **Unchanged** — nothing to do.
* **Restored – review needed** — a portal profile or sign-in option changed since is put back; a profile that no longer exists is replaced by the site
  default. The change is listed under *Needs your review*.
* **PMS routing** that differs is **not changed** (PMS configuration is not this feature's to alter). Room sign-in is **paused on that network only**
  until the item is acknowledged. If the operator had already switched room sign-in off there, acknowledging never lifts it.

Review items and the pause are written inside the apply and removed again by a rollback.

### Local recovery

`netd recover status` and `netd recover reassert-confirmed` (as root, on the appliance) work when the Admin Console cannot be reached. Reassert runs the
same reconcile and health checks as a restart and records it; it exits non-zero if the appliance did not converge. Factory defaults and the full reset need
the database and the Console's review and are deliberately not offered locally.

## 6. The last-confirmed snapshot

After every confirmation (and whenever a boot reconciles from the database) netd writes `last-confirmed.json` atomically (temp file, fsync, rename) beside
the bundles. It holds network intent and a digest — **no sessions, devices or credentials**, so restoring from it cannot reinstate revoked access.

* The database commit comes first and the snapshot after, so the snapshot can lag the database but never lead it. **When the database answers it decides**,
  and the snapshot is rewritten from it.
* If the database is **unreachable at boot** (connection failure or timeout — not an answer the database gave), netd no longer exits: it brings the network
  up from the snapshot, **refuses all changes** (reported as an unfinished reconcile) and retries until the database answers, then reconciles from it.
  Phase-3 shaping stays dark until its writer boundary can be verified.
* A torn or altered snapshot is never used. No candidate or in-flight revision is ever persisted as a boot source.

## 7. Portal profiles

The site's portal design stays in `tenants.branding` and is the **default profile**. A named profile (`iam_v2.portal_profiles`) is the same document
(design, draft, history) assigned to any number of networks (`portal_profile_assignments`). Portal settings has a *Portal design* selector with the
accepted editor (Save changes, Live Preview, History). A profile is appearance and wording only: it never changes which sign-in methods, PMS interface or
packages a network has.

* The client's profile is decided by the **connection's source address** on the appliance — `portald` passes it to `scd`, which resolves the network — never
  by a header, cookie or parameter the client controls. The administrator preview of a profile is honoured only from the appliance itself, with no
  forwarding header. `portald` caches designs per client address, bounded (512 entries, 3 s).
* The **privacy notice** is part of the design, so a sign-in on a profile network is recorded under that profile's notice
  (`public.privacy_notice_in_force(tenant, network)`, stamped by the trigger of 0130).
* An uploaded image used by **any** design (default or profile) cannot be deleted; an unreadable scan refuses the deletion.
* Deleting a profile returns its networks to the default.

## 8. Sign-in per network

A network can only **narrow** what the site offers (`iam_v2.network_signin_overrides`): switch methods off (room sign-in, voucher, staff account, email
account, text code, WhatsApp code, social, open selection), choose which leads, and remember devices for **fewer** days. It can never switch on what the
licence, the deployment or the site does not. `scd` applies it at every entry point — the guest authority (voucher, accounts, codes, social, resume),
code issue, social start, open selection, room sign-in and the method listing the portal shows — and **fails closed** when the override cannot be read.
A code is judged by the channel it arrived by (a text code on a network that switched text codes off is refused even if WhatsApp is on). The social
callback is bound to the address that started the flow and is checked again, for the network that address belongs to *now*, when it completes; a flow whose
network switched social sign-in off in between is refused. Registration, resend and forgot-password for email accounts are not network-scoped; signing in is.
The network is always derived from the connection's source address on the appliance, never from a field the client supplies.

Package rules of type *only on certain client networks* pick networks by name.

## 9. PMS routing

A network's room sign-in follows its PMS routing (Hotel → PMS routing). The network's sign-in card shows which interface(s) it is routed to, or says room
sign-in is not available there. Routing itself is configured only under PMS routing.

## 10. Database objects and grants

Migrations 0151 (staged changes), 0152 (profiles, assignments, sign-in overrides, the notice stamp) and 0153 (recovery snapshots and review items) create
`iam_v2` tables owned by `iam_v2_owner`. `svc_edged` writes them and `svc_scd` reads the profile and sign-in tables; the grants are plain statements in
`deploy/gatep/svc-edged-phase345-admin-grants.sql`, so a Gate-P reconcile after a factory-clean install reproduces them (the grant test replays that file).
No table holds guest data. `iam_v2.portal_profiles.name` is recorded in the personal-column list as staff configuration.

## 11. Permissions and audit

Recovery, profiles and per-network sign-in use the existing `network` and `portal-branding` resources and their roles; changes are audited
(`network.recovery.staged`, `portal_profile.*`, `network.signin_changed`). Publishing a design still requires the operator's password.

## 12. What has and has not been verified

Verified in isolation (including the journey from a heavily reconfigured trunk with a converted prepared network back to the approved Access network, and an apply that lapses or whose service stops before it is recorded): unit tests; integration tests against a full-schema PostgreSQL with a fake network service; real-kernel container tests for bridge
ownership, adoption, a hostile network name against the real nft parser and the **real `netd` binary starting with the database refusing connections**
(`scripts/ci/netd-integrity-kernel.sh`); browser tests at desktop and 390 px. Each correction has a test that fails without it.

**Not verified:** a real Kea, a real switch or hypervisor trunk, PRE-LIVE. Nothing here was deployed. On PRE-LIVE, VLAN 60 showed zero frames received on
its VLAN device, which locates the loss outside the appliance; the switch port and the virtual-switch port group still need checking. PRE-LIVE's on-disk Kea
file stays stale until netd is deployed.
