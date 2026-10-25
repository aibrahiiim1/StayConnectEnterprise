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
| **Full client networking reset** | As factory defaults, plus which portal design each network shows and each network's sign-in options go back to the site's. Requires typing the hotel's name (or the word `RESET` where the site has no readable name). |

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
  (`iam_v2.profile_privacy_notice_stamp`, a trigger that runs after the 0130 stamp and replaces the version for a profile network; it changes nothing in `public`, because a live site applies migrations as `iam_v2_owner`, which owns no public function. Gate-P grants `iam_v2_owner` SELECT and INSERT on `public.privacy_notice_versions`. A failure leaves the site notice stamp and logs a warning; it never stops a sign-in).
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
`deploy/gatep/svc-edged-phase345-admin-grants.sql` (0154 adds `iam_v2.network_archive`), so a Gate-P reconcile after a factory-clean install reproduces them (the grant test replays that file).
No table holds guest data. `iam_v2.portal_profiles.name` is recorded in the personal-column list as staff configuration.

## 11. Permissions and audit

Recovery, profiles and per-network sign-in use the existing `network` and `portal-branding` resources and their roles; changes are audited
(`network.recovery.staged`, `portal_profile.*`, `network.signin_changed`). Publishing a design still requires the operator's password.

## 12. What has and has not been verified

**Verified in isolation** (unit tests; integration tests against a full-schema PostgreSQL with a fake network service; real-kernel container tests for bridge
ownership, adoption, a hostile network name against the real nft parser and the real `netd` binary starting with the database refusing connections,
`scripts/ci/netd-integrity-kernel.sh`; browser tests at desktop and 390 px): including the journey from a heavily reconfigured trunk with a converted prepared
network back to the approved Access network, and an apply that lapses or whose service stops before it is recorded. Each correction has a test that fails
without it.

**Verified on PRE-LIVE after the deployment (§13):** the migrations and software versions that are running; that Kea was not restarted and its live
configuration is unchanged; that the Kea file on disk now matches the confirmed configuration; that every new read endpoint answers; that the new pages render
without overflow at 1280 px and 390 px; that the recovery previews are correct. These are read-only observations.

**Not verified anywhere:** a real client obtaining a DHCP lease on the prepared `10.20.0.0/20` network; the live result of applying a Full Reset (the Product
Owner's first Full Reset is staged and not applied, §14); an Access or Trunk conversion on the real port; real Kea behaviour while a network change is applied;
any real switch or hypervisor trunk configuration; a real client reaching the portal over a guest network.

**VLAN 60.** An earlier reading on PRE-LIVE showed 0 frames received on the `ens192.60` VLAN device while frames were sent on it. That is an observation of that
counter and nothing more: it does not show where frames were lost. The switch port, the virtual-switch port group, the access point or controller mapping, VLAN
tagging on the virtual NIC and the appliance itself have all been left unexamined, and no cause has been established. Do not read this document as locating it.

## 13. PRE-LIVE deployment record (2026-10-25, appliance `172.21.60.25`)

Measured, not claimed. The appliance was inspected first and re-inspected after every step.

| | Before | After |
|---|---|---|
| Schema | 0150 | 0153 (0151, 0152, 0153 applied by `scripts/edge-migrate.sh` as `iam_v2_owner`, each after a mandatory encrypted, proven-restorable backup) |
| netd, scd, portald | `9394eb5f` (netd), `b5775652` (scd, portald) | `4a192268` (master after #243) |
| edged | `b5775652` | `82faee4f` (master after #244) |
| Admin Console | release `20261024-170919` | release `20261025-115528`, built from `82faee4f` |
| acctd, pmsd | `9394eb5f`, `ae4f85d2` | unchanged |
| Kea live configuration | subnets 192.168.77.0/24 (br-g90) and 10.60.0.0/24 (br-g60); hash `75dc2ea6` | identical hash: Kea was never restarted |
| Kea file on disk | stale since 21 Aug (one subnet, a bridge that no longer exists) | holds the confirmed configuration (both subnets) |
| nftables | structure as running | no change except the walled-garden's DNS-resolved address sets |
| Unbound fragments, interfaces, default route, management address | as running | identical |
| Guest sessions at the time | 0 | 0 |

**Two defects found only by deploying, both fixed through the protected path before the final state:**
1. Migration 0152 replaced a function in `public`. A live site applies migrations as `iam_v2_owner`, which owns nothing in `public`, so the apply was refused (`permission denied for schema public`) and nothing changed. 0152 now touches nothing in `public` (§7); Gate-P's grant of `SELECT, INSERT` on `public.privacy_notice_versions` to `iam_v2_owner` was applied once, by hand, as the platform owner (it is in `gatep-grants.sql`). (#243)
2. The Full Reset confirmation name was read from `public.sites`, which the Admin Console's database role cannot read, so the field never appeared. It is now read from `tenants`, `RESET` when the site has no readable name, and an integration test pins the privileges the recovery code uses. (#244)

**Live checks that passed:** every new read endpoint answers 200 as the verification operator; `/network`, `/network/recovery`, the network page and Portal settings render with no horizontal overflow at 1280 px and 390 px; the Restore Last Working and Full Reset previews (read-only) show the right plan, the "check outside the appliance" list and what is not touched, and the Stage button stays disabled until the confirmation is typed; no 4xx/5xx from the Admin Console API during the browser run; `check-appliance-units.sh` passes; the Client Portal answers on 8380 and 8343 with the site's design and sign-in methods; `sha256sum -c DEPLOYED_BINARIES.sha256` is all OK.

**Not done, deliberately:** the prepared network was not activated, no port was converted, no VLAN was changed, no Full Reset was run, and nothing external (switch, ESXi) was touched. Those are the Product Owner's manual tests. Rollback: previous binaries are kept as `*.bak-pre-<sha>-<time>` in `/opt/stayconnect/bin`, the previous Admin Console release is `hotel-admin.previous`, and migrations 0151–0153 each have a `.down.sql` (run with `scripts/edge-migrate.sh --down`, newest first).

### Merge provenance

| | |
|---|---|
| Consolidated PR #242 | The Product Owner's mission named reviewed head `c568373446c8`. It was merged at head `090f92c9` (merge commit `fa24a913`). |
| Why the head differs | Two unresolved Codex review threads blocked the protected merge. They were fixed, replied to and resolved, and the new head was labelled `po-merge-approved` by the delivery agent. |
| The fixes | The account sign-in route judges the network by the account's kind (a refusal looks like a wrong password); backing out a swap of two untagged networks switches the new one off before the old one returns. Each has a test that fails on the previous code. |
| Authorization | The mission authorised the protected merge of #242 and "routine related fixes". It did **not** name head `090f92c9`; the Product Owner did not approve that head separately, and nothing in this record states that they did. |
| Follow-ups merged the same way | #243 (migration 0152 changes nothing in `public`, master `4a192268`), #244 (Full Reset confirmation name readable by the Admin Console role, `82faee4f`), #245 (this record, `d870bf80`). Each was reviewed by Codex with no findings before the label was applied. |
| Superseded | #234–#241 were closed with a comment pointing at #242. |

## 14. The Product Owner's first Full Reset on PRE-LIVE (observed 2026-10-25)

**Timeline recorded by the appliance (UTC).** 12:11:56 the Product Owner staged a Full Client Networking Reset (`network.recovery.staged`). At 12:26 it was still
staged: three changes `PENDING`, active revision 97, VLAN 60 and VLAN 90 still enabled, Kea unchanged. **At 14:58:53 the reset was applied (`network.apply`,
revision 98) and at 14:59:06 it was confirmed (`network.revision.confirmed`); revision 98 is active.** All three changes are `CONFIRMED`.

**State read afterwards (15:53 UTC, read-only).**
* Only *Client Wi-Fi* is enabled: untagged on `ens192`, `10.20.0.0/20`, gateway `10.20.0.1`, local DHCP pool `10.20.0.10`–`10.20.15.254`. The bridge `br-g-52fd7a28`
  is up with `10.20.0.1/20`; `br-g60` and `br-g90` are gone.
* Kea's live configuration has exactly one subnet, `10.20.0.0/20`, and the Kea file on disk holds the confirmed configuration (`kea_disk.holds_confirmed_configuration`
  is true). netd is healthy. The WAN/management address `172.21.60.25/24` and the default route are unchanged.
* Kea reports one active lease in `10.20.x.x`: a client obtained an address from the new network's DHCP. That is Kea's own record, counted once; this document does
  not claim a client test beyond it. No guest session was active.
* The three old networks are all switched off and still listed: *PRELIVE Test Guest* (untagged), *PRELIVE Test Guest* (VLAN 90) and *VLAN 60*. No review item exists;
  the one PMS interface is still routed to the two old test networks, so room sign-in is not offered on the new network until the operator routes it (section 15).
* **Not verified:** a real client's sign-in and internet access through the portal on the new network, the switch and ESXi side (the untagged VLAN must reach
  `ens192`), and anything about what the access point or controller maps.

**Why old networks are still listed.** Two carry sign-in history that foreign keys reference: *PRELIVE Test Guest* (untagged) has 419 sign-ins, 139 device appearances
and 187 resolutions; *PRELIVE Test Guest* (VLAN 90) has 251, 174 and 23. *VLAN 60* has none. A network with history cannot be removed (the existing delete answers
`kept_as_history`), and a rollback and the audit trail need the rows. That is intended. This reset was confirmed before the archive existed, so nothing was archived
at the time; migration 0154 archives networks retired by an already-confirmed reset (section 15).

**Defect found while investigating, and fixed:** after staging, nothing on the Recovery page showed that the recovery was still waiting, so a staged reset could be
taken for a finished one. Staging now sends the operator to Client networks, the Recovery page shows the latest recovery as Staged, Applied or Confirmed, and the
staged list marks it *Not applied yet*.

## 15. Archived networks and the Default Network

**Decision (Product Owner, 2026-10-25).** Non-destructive archiving: after a successful Full Reset the main screen shows one functional Default Network, the old
networks are disabled and hidden in an optional archive, and nothing is deleted. A truly factory-clean reset that removes history is not part of this and would
need its own, explicitly authorised operation.

**Why a retired network keeps its row.** Its identity is referenced by sign-ins, device appearances, resolutions, verification grants, the PMS routing, package
rules, sign-in restrictions and pauses, the audit trail, the last-confirmed snapshot and a rollback. Deleting is refused for any network with history
(`kept_as_history`); the existing delete is for a network never used.

**What archived means.** A marker in `iam_v2.network_archive` (migration 0154, additive; nothing in `public`). It is a presentation state: the network is still
switched off, still has its history, settings, PMS routing and audit trail, and nothing a guest, an entitlement, a package rule or the PMS sees changes.

* **Resets confirmed before the feature.** Migration 0154 archives, once and idempotently, the networks that an already-confirmed reset switched off and that nothing has touched since (from the recorded changes alone; a network that is serving is never shown as archived).
* **When it is written.** Only when a Restore Factory Defaults or Full Client Networking Reset is **confirmed**, in the same transaction that settles the change
  (`settleConfirmedChanges`), and only for the networks that reset switched off. Staged, applied, cancelled, rolled back and lapsed resets never archive anything,
  so a failed change cannot leave an incorrect archive. Restore Last Working archives nothing.
* **Staged, applied, confirmed.** The Recovery page shows the latest recovery as three different states: *Staged* (nothing has changed), *Applied* (live, affecting
  clients, rolls back by itself unless kept) and *Confirmed* (kept; the retired networks are archived). Client networks lists a staged or applied recovery with
  *Not applied yet* or *Awaiting confirmation*.
* **Where it shows.** Client networks lists the Default Network (badged *Default*) and every other network that is not archived. **Archived networks (n)** sits
  below, collapsed, with its count; expanding it lists each network with when and by which operation it was retired, whether it has client history and whether it
  keeps a PMS route, and **Restore from archive** (which only removes the marker; the network stays switched off). Switching an archived network on also removes the
  marker. Package-rule pickers mark archived networks; PMS routing keeps showing their routes and does not list them as unrouted networks.
* **Audit.** `network.unarchived`; the reset itself is audited as before.

**The Default Network** is the prepared network a reset put in place (or activation created) while it is switched on. `GET /network/default` and the card on Client
networks say what is ready and what needs the operator, and change nothing:

| Check | Meaning |
|---|---|
| DHCP, DNS | The prepared settings: local DHCP and its pool, appliance DNS. |
| Client Portal | The portal design the network shows (the site's default unless a profile is assigned). |
| Sign-in | The site's methods, narrowed by the network if it has an override. |
| PMS routing | A route is made only by the operator. The routes of the retired networks stay on them. If a PMS interface is routed only to archived or switched-off networks it is offered as a **candidate**, one interface at a time, with a preview; routing it uses the existing, explicit PMS-routing call. Two independent interfaces are two candidates, never merged; none is chosen for the operator. No PMS traffic or posting is started. |
| Package eligibility | A package whose current revision limits it to named networks, none of them the Default Network, is listed as *not offered here*. Revisions are immutable and were not widened; offering one here is a new revision of the package. |

**Dependencies on retired network ids, investigated** (PRE-LIVE, 2026-10-25): two of the three old networks carry sign-in history (419 and 251 sign-ins; 139 and 174
device appearances; 187 and 23 resolutions); both old PRELIVE test networks are routed to the one PMS interface (Protel); no package rule names a network
(`SITE_NETWORK` rules: none); entitlements are not tied to a network (no entitlement column references one); sign-in restrictions and pauses are by network id and stay with their networks. A
replacement creates lineage so pinned package rules keep matching; a reset creates a new network without lineage, so nothing is widened.

**Still a Product-Owner matter.** Removing history, or a network that has any, is not offered anywhere and would need its own authorisation.
