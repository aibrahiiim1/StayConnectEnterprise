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

A network's room sign-in follows its **room sign-in policy** (Hotel → PMS routing): none, one dedicated PMS, or an explicitly chosen shared strict set (§16). The
network's sign-in card shows which interface(s) it is routed to, or says room sign-in is not available there. Routing itself is configured only under PMS routing (or
from the Default Network card and the post-reset step, which use the same preview and confirmation).

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

**Verified on PRE-LIVE after the Product Owner's first Full Reset (§14, observed read-only):** the Product Owner applied the reset at 14:58:53 UTC and confirmed it at
14:59:06 UTC on 2026-10-25, and revision 98 is the active, confirmed revision. Only *Client Wi-Fi* is enabled (untagged on `ens192`, `10.20.0.0/20`); its bridge is up;
the old bridges are gone; Kea's live configuration holds exactly that one subnet; the management address and default route are unchanged; the three retired networks are
switched off and, since the archive was deployed (§15), archived. This replaces an earlier wording of this section that described that reset as staged and not applied: it
was written before the Product Owner applied it and is corrected here, not rewritten in the records that were true when they were made.

**Not verified anywhere:** a real client signing in through the portal and reaching the internet over the prepared `10.20.0.0/20` network (Kea reports one active lease
in that range, counted once and recorded as Kea's own observation; no guest session was active and nothing beyond the lease is claimed); an Access or Trunk conversion on
the real port; real Kea behaviour while a network change is applied; any real switch or hypervisor trunk configuration; a real client reaching the portal over a guest
network.

**VLAN 60.** An earlier reading on PRE-LIVE showed 0 frames received on the `ens192.60` VLAN device while frames were sent on it. That is an observation of that
counter and nothing more: it does not show where frames were lost. The switch port, the virtual-switch port group, the access point or controller mapping, VLAN
tagging on the virtual NIC and the appliance itself have all been left unexamined, and no cause has been established. Do not read this document as locating it.

## 13. PRE-LIVE deployment record (2026-10-25, appliance `172.21.60.25`, retired 2026-10-28)

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

### Second deployment record (2026-10-25: archive and Default Network)

* **Deployed.** edged `40621f8e` (master after PR #249) and the Admin Console release `20261025-160357` (`f524dc70`, BUILD_ID `zwvpA0D2wcjcKkF-TSldy`), which carries the Recovery-page fix.
  netd, scd, portald, pmsd and acctd were not changed. Kea was not restarted. Installed bytes read back: edged sha256 `d10ce4ec...d9c9`, `vcs.modified=false`; `sha256sum -c DEPLOYED_BINARIES.sha256` 7 of 7 OK.
* **Migration.** 0154 applied to `stayconnect_site` with `scripts/edge-migrate.sh --apply-role iam_v2_owner` after an encrypted, proven-restorable backup. It creates `iam_v2.network_archive` and backfills it from
  confirmed reset changes only. Result: three rows.
* **Measured on PRE-LIVE (read-only).** `/network/archive` counts 3; `/network/default` reports Client Wi-Fi ready for DHCP, DNS, Portal, sign-in and packages, and PMS routing unresolved;
  `/network/recovery` reports the latest recovery as the Full Reset, Confirmed, revision 98. In a real browser the main list shows only the Default Network, "Archived networks (3)" is collapsed and expands,
  and the Recovery page, Client networks and Portal settings show no overflow at 1280 px and 390 px. The PMS routing preview was opened and not confirmed.
* **Rollback.** `edged.bak-pre-40621f8e-20261025T163027Z`; `hotel-admin.previous` -> `20261025-115528`; `0154.down.sql`.
* **Not verified.** A real client signing in through the portal on `10.20.0.0/20`, the switch and ESXi side, and a confirmed PMS route.

## 14. The Product Owner's first Full Reset on PRE-LIVE (observed 2026-10-25)

**Timeline recorded by the appliance (UTC).** 12:11:56 the Product Owner staged a Full Client Networking Reset (`network.recovery.staged`). At 12:26 it was still
staged: three changes `PENDING`, active revision 97, VLAN 60 and VLAN 90 still enabled, Kea unchanged. **At 14:58:53 the reset was applied (`network.apply`,
revision 98) and at 14:59:06 it was confirmed (`network.revision.confirmed`); revision 98 is active.** All three changes are `CONFIRMED`.

**State read afterwards (15:53 UTC, read-only).**
* Only *Client Wi-Fi* is enabled: untagged on `ens192`, `10.20.0.0/20`, gateway `10.20.0.1`, local DHCP pool `10.20.0.10`–`10.20.15.254`. The bridge `br-g-52fd7a28`
  is up with `10.20.0.1/20`; `br-g60` and `br-g90` are gone.
* Kea's live configuration has exactly one subnet, `10.20.0.0/20`, and the Kea file on disk holds the confirmed configuration (`kea_disk.holds_confirmed_configuration`
  is true). netd is healthy. The WAN/management address `172.21.60.25/24` (historical) and the default route are unchanged.
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

* **When it is written.** Only when a Restore Factory Defaults or Full Client Networking Reset is **confirmed**, in the same transaction that settles the change
  (`settleConfirmedChanges`). It archives every network the reset left switched off: the ones it switched off and the ones that were already off. Staged, applied,
  cancelled, rolled back and lapsed resets never archive anything, so a failed change cannot leave an incorrect archive. Restore Last Working archives nothing.
* **Catch-up.** A reset confirmed before this existed is caught up from the recorded facts: migration 0154 archives the networks it switched off, and the reads that
  show the archive archive the ones already off before it. Only networks untouched since that reset are archived: a network the operator edited, switched on,
  or restored from the archive afterwards is not archived again (restoring touches the network's row for exactly that reason).
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
| Room sign-in | The network's own policy (§16): none, one dedicated PMS, or a shared strict set. The routes of the retired networks stay on them and nothing is copied. An undecided Default Network is shown as needing a decision, with a **Configure room sign-in** action that previews the effect first. No PMS traffic or posting is started. |
| Package eligibility | A package whose current revision limits it to named networks, none of them the Default Network, is listed as *not offered here*. Revisions are immutable and were not widened; offering one here is a new revision of the package. |

**Dependencies on retired network ids, investigated** (PRE-LIVE, 2026-10-25): two of the three old networks carry sign-in history (419 and 251 sign-ins; 139 and 174
device appearances; 187 and 23 resolutions); both old PRELIVE test networks are routed to the one PMS interface (Protel); no package rule names a network
(`SITE_NETWORK` rules: none); entitlements are not tied to a network (no entitlement column references one); sign-in restrictions and pauses are by network id and stay with their networks. A
replacement creates lineage so pinned package rules keep matching; a reset creates a new network without lineage, so nothing is widened.

**Still a Product-Owner matter.** Removing history, or a network that has any, is not offered anywhere and would need its own authorisation.

## 16. Room sign-in routing, shared networks and readiness (D74, 2026-10-26)

*Record of the mission "Complete enterprise client networking and multi-PMS delivery". Contract: [Phase-0 Amendment A2](architecture/StayConnect-IAM-Phase0-Amendment-A2.md).*

**Three concepts, kept apart.** The *physical LAN port* (Access or Trunk, with an optional untagged native network), the *Default Network* (the prepared network serving
clients; an administrative idea that never reuses another network's identity or database id), and each *client network* (its own subnet, DHCP, DNS, portal, sign-in
methods, isolation, package eligibility and room sign-in policy). A tagged VLAN network is configured independently of the Default Network and of every other VLAN.

**The policy of one network.**

| Policy | Meaning | Interfaces |
|---|---|---|
| None | No room sign-in on this network, by choice. Never a fault; the room tab is not offered. | 0 |
| One dedicated PMS | The room is checked against exactly one interface. | 1 |
| Shared strict set | Two or three independent interfaces; every one is asked; a guest is admitted only when exactly one finds them and all others clearly do not. | 2 to 3 |
| (undecided) | Nobody has chosen. Room sign-in is not offered. | — |

Many networks may use one PMS; one network may use several only as a shared strict set. **There is no "every active PMS"**: `ALL_ACTIVE_INTERFACES` is not offered, not
implemented and not a fallback, and a PMS added to the site is never silently added to any network.

**How a sign-in resolves** (`internal/pmsresolve`, `cmd/scd`). The network's policy and its mapped interfaces must agree or the attempt is refused as a routing failure
before anything is looked up. The vector is every selected interface, asked in parallel, decided only on the complete outcome (no first responder, no fastest answer).
A room number is only ever looked up inside one interface's own stays, so the same room number in two PMS never collides and a PMS is never inferred from the room; a
guest who is in the same room number with the same surname in two selected PMS is *ambiguous* and is asked for the reservation number, never matched by picking. A
selected interface that is out of service is UNAVAILABLE (never dropped); a candidate whose feed cannot authorise is STALE (never NO_MATCH); either makes the whole
network fail closed, because "no PMS has this guest" can no longer be proven. All non-success outcomes remain one uniform guest response; the operator-facing reason
(`auth_resolutions`, sign-in attempts) is recorded without any guest-visible interface or property detail. The cap of three is enforced at save and at run time.

**Changing a network's policy** (PMS routing page, Default Network card, post-reset step). Choose a policy and the interfaces, *preview*, then *confirm*:

* The preview (`POST /pms-routing/{network}/preview`) changes nothing. It lists the interfaces added and removed, each interface's ability to answer now and guests in
  house, other networks that use the same interface, room numbers present in more than one selected PMS, clients and room-based entitlements online on the network, and
  blockers (cap, wrong count for the policy, an interface that does not exist, is out of service or has no published configuration, an archived network, a critical
  duplicate-source conflict between two selected interfaces) and warnings.
* A preview that can be applied mints a **proposal**: an unguessable, single-use, 15-minute approval held by the server and bound to the network, to the operator who
  previewed it, to the exact mode and interfaces previewed, and to the network's change counter and routing at that moment. Ordinary reads never carry it. The apply
  (`PUT /pms-routing/{network}` with `proposal_id`, and the mode and interfaces) is refused unless every one of those still holds: another operator (`proposal_other_operator`),
  other content (`proposal_mismatch`), a network changed or previewed-again since, or an expired or used proposal (`routing_changed` / `preview_required`). Any applied change
  to a network retires every outstanding proposal of that network, so changing a network away and back cannot revive an old approval. The policy and the interfaces are written
  in one transaction. Both the preview (`pms_routing.previewed`) and the apply (`pms_routing.policy_set`) are audited with the proposal id and the previous and new policy and
  interfaces, and a refused confirmation is audited (`pms_routing.proposal_refused`, with the reason). **There is no other way to change routing.** The earlier
  single-interface request and any request without a mode and a proposal are refused (`preview_required`, nothing written). Returning a network to *not decided* is previewed the
  same way (mode `UNDECIDED`) and confirmed with `DELETE /pms-routing/{network}?proposal=<id>`. Proposals live in the server's memory: a restart of edged invalidates them all,
  which fails closed.
* It starts and stops no PMS traffic, posts nothing, changes no package eligibility, copies nothing from another network and leaves online guests with their access
  (their entitlements belong to the stay that earned them).

**Access ↔ Trunk, VLAN changes, replacement.** Before anything is staged, `GET /network/guest-networks/{id}/impact` and the port-mode preview list, for each network
affected, its online clients and sessions, how many hold room-based access, its room sign-in policy and interfaces, its switched-off sign-in methods and the packages
limited to it, and the requirements *outside* OneGate for the resulting topology (switch trunk or access configuration, the hypervisor port group's VLAN setting, the
access-point profile). OneGate configures none of them. A replacement keeps its own network's policy and routing; nothing is merged with another network's, and no
guest moves to a different PMS.

**Reset and recovery.** No recovery operation changes PMS routing. Restore Last Working compares the policy as well as the interfaces; a difference pauses room sign-in
on that network only until reviewed (D72, unchanged). After a **confirmed** Full or Factory reset the Recovery page offers an optional, separate *Configure room sign-in*
step for the resulting network (with the same preview and confirmation); it is not offered while the reset is staged or applied, nor after a cancelled, lapsed or rolled-back
one, and the confirmation of the network and the choice of routing are two different actions.

**One readiness assessment** (`GET /pms-routing/readiness`). Dashboard Needs attention, PMS connection, PMS routing, the Default Network and Client networks all read
it. Per network: working, not offered (by choice), not working, not known, needs a decision, not set up, needs correcting, or network off. A PMS that is not ready but
serves no active network raises nothing; a PMS that also keeps a route to an archived network is not a problem on its own; an undecided network matters only where a PMS in
service could serve it; PMS transport health is separate (a disconnected transport with an intact mirror still answers); unknown stays unknown. A warning names the
network and the interface, links to PMS routing or PMS connection, and clears by itself when corrected.

**Database.** Migration 0155 adds `iam_v2.guest_network_pms_policy` (one row per network; `NONE`, `DEDICATED`, `SHARED_STRICT`), a deferred constraint trigger that keeps the
policy and the mapped interfaces consistent whenever either is written, and `iam_v2.p3_interface_can_authorise`. Existing single routes are backfilled as dedicated; nothing is
inferred, mapped or merged. Grants are in `deploy/gatep` (`svc_edged` on the table; `svc_scd` select and execute). `.down` removes the objects and leaves every mapping.

**Rollback after a shared policy exists** (drilled on a disposable database as `iam_v2_owner`, 34 shared, 179 dedicated and 12 none networks).
* **The safe boundary.** While any Shared STRICT policy exists, nothing may be rolled back to a release that predates the policy: `scripts/binary-rollback.sh` refuses to install
  such an scd or edged (it asks the artifact for the policy table name and the database for the number of shared networks; an unreadable count is a refusal; there is no override
  flag) and `0155.down.sql` refuses to run (a guard at its top). Both leave everything unchanged and say the operator action: change each shared network to one dedicated PMS or
  to no room sign-in (previewed, audited), then roll back. Tested: `scripts/ci/binary-rollback-tests.sh` (47 cases) and
  `TestIntegration_Routing_TheDownMigrationRefusesWhileASharedSetIsInUse`; on a disposable database the down migration was refused with 26 shared networks (nothing changed),
  and after they were converted it ran, saved all 68 policies and the re-apply restored them. Only a hand-run `install` or `psql` can bypass it, and that is outside the tool.
* Roll the software back first (scd, edged, console), then the schema. If the schema goes first, scd treats a missing policy table as no policy: a network with one mapped
  interface keeps working, a network with several refuses room sign-in (fail closed), and every other sign-in method is unaffected.
* `0155.down.sql` keeps every mapped interface (269 of 269 rows) and copies the policies to `iam_v2.guest_network_pms_policy_rolled_back` before dropping the table. Applying
  0155 again restores each policy that still agrees with the mapping (34, 179 and 12 came back exactly) and drops the copy; one whose mapping changed meanwhile stays undecided,
  fail closed, until an operator chooses again. Networks holding more interfaces than the cap (four, in the fixture) cannot be restored as shared and stay undecided.
* An older release (before 0155) given a database that still has 0155: its single-interface route replace is **refused by the database** on a shared network (a deferred
  trigger), so it cannot downgrade a shared set; its resolver reads every mapped interface strictly but drops an out-of-service one and has no cap and no per-candidate
  feed check. If that difference matters, change shared networks to a dedicated or none policy (previewed) before rolling the software back.
* 0155 was amended after its live application, only to add the save-and-restore above: the live database already has the earlier text and is unaffected; the live ledger row is
  unchanged.

**Tests.** Unit: the assessment (PMS-free is never broken; archived-only routes do not warn; strict shared blocks on any candidate; unknown stays unknown; transport is
separate; corrected warnings clear), the resolver (out-of-service candidate, cap, ambiguity), the routing refusal table. Integration against a full-schema PostgreSQL, also
as the real `svc_edged` role: preview/apply/stale/blockers/cap/permissions, readiness and the dashboard attention agreeing, no routing change through stage, cancel, apply,
lapse, rollback and confirm, the post-reset step offered only after confirmation, impact and external requirements. scd through its real handlers: duplicate room numbers,
ambiguity across interfaces, an unavailable or out-of-service candidate failing the network closed while a dedicated network is unaffected, NONE, a mismatching policy, the
cap. Console: 1019 unit tests and browser tests at 1280 px and 390 px.

**Limits and open items.** The cap of three is a contract constant, not a setting. Sharing a network between two PMS makes it depend on both being able to answer; that is
the strict contract's price and the readiness assessment says so. Real-client sign-in on a shared network, real Protel behaviour for two interfaces on one network, and any switch
or hypervisor configuration are **not verified** and need the Product Owner's manual acceptance on PRE-LIVE.

## 17. PRE-LIVE deployment record, third deployment (2026-10-25, appliance `172.21.60.25`, retired 2026-10-28)

Measured, not claimed. A read-only snapshot was taken before and after (networks, active revision, archive and routing rows, Kea file hash, addresses, routes, services,
binaries, console release); only the intended objects differ.

| | Before | After |
|---|---|---|
| Schema | 0154 | 0155 (applied by `scripts/edge-migrate.sh` as `iam_v2_owner`, after the mandatory encrypted, proven-restorable backup `db-20261025T195514Z.sql.gz.age`) |
| edged | `40621f8e` | `67808d7a` (a first install of `58abb494` was replaced by it after the two review findings below) |
| scd | `4a192268` | `58abb494` |
| netd, portald, pmsd, acctd | `4a192268`, `4a192268`, `ae4f85d2`, `9394eb5f` | unchanged (netd was not touched, Kea was not restarted) |
| Admin Console | release `20261025-160357` (`f524dc70`) | release `20261025-201251`, built from `67808d7a` (`20261025-195619` was the intermediate `ab15b67e`) |
| Routing | two rows, both on archived networks, no policy table | the same two rows, each backfilled as one dedicated PMS by the migration; nothing mapped, copied or merged |
| Kea file hash, addresses, routes, management address, active revision (98), archive (3), sessions (0) | as running | identical |

`DEPLOYED_BINARIES.{sha256,txt}` were rewritten from the installed bytes and `sha256sum -c` reports 7 of 7; `check-appliance-units.sh` passes; every `stayconnect-*` unit is
active; neither edged nor scd logged an error. Rollback: `edged.bak-pre-67808d7a-20261025T201245Z`, `edged.bak-pre-58abb494-20261025T195604Z`,
`scd.bak-pre-58abb494-20261025T195609Z`, `hotel-admin.previous` -> `20261025-195619` (`20261025-160357` is retained), `0155.down.sql` after the recorded backup.
(nftables' hash is not a valid probe: it changes between two identical reads because of packet counters.)

**Live checks as the verification operator (read-only, at the time of deployment):** `/pms-routing`, `/pms-routing/readiness`, `/network/default`, `/network/overview`,
`/network/recovery`, `/network/archive`, `/reports/overview` and the impact endpoint all answered; the Default Network was undecided and the only warning was the decision for it
(not a PMS fault, and nothing for Protel, which then served no active network and kept its two archived routes); the three previews (none, one dedicated PMS, a shared set of
one, which is refused with its exact reason) changed nothing; a real browser rendered the Client networks, PMS routing, PMS connection, Dashboard, Room sign-in and Recovery
pages without overflow at 1280 px and 390 px and the editor previewed (and was not confirmed) at both. The migration was also rehearsed beforehand on a disposable database as
`iam_v2_owner`.

**Observed after the deployment (not performed by it).** Within minutes the Product Owner's own `admin` account tested the features live, as the audit records: it restored a
retired network from the archive (`network.unarchived`, 20:13:55 UTC), staged Access-to-Trunk changes on `ens192` (`network.port.mode_staged`, 20:15:34 and 20:16:54), applied
and kept them (revisions 99 and 100), created networks *VLAN 90* and *VLAN 60* by that route, and set and cleared room sign-in routing for them through the new preview-and-confirm
call (`pms_routing.policy_set`, `pms_routing.cleared`, 20:18 UTC, with the previous policy and interfaces recorded). None of this was done by the deployment or by the agent. It is
the first live use of the new previews; it is not recorded here as an acceptance, and what those changes did to real clients is theirs to report.

**Review findings fixed before the final deployment (PR #251, both P2, both resolved):** the routing preview did not use the fingerprint-based duplicate-source detection that
the source-conflicts page uses, so two interfaces for one property could be approved as a shared set; and a selected interface that had since gone out of service could not
be unchecked, blocking the repair. A production lint rule (a form label needs accessible text) that the local checks do not run also failed the first console build; the label is
now tied to its radio by id.

**Not verified, and not claimed:** a real client signing in with a room number on a dedicated or shared network; real Protel behaviour with two interfaces on one network (only
one PMS Interface exists on PRE-LIVE, so a shared set cannot be exercised live); the effect of the Product Owner's trunk conversions on real clients; any switch, access-point or
hypervisor configuration.

## 18. Follow-up under the same mission (2026-10-26): closed write path, rollback drill, reconciliation after revisions 99 and 100

**Closed write path.** Every routing mutation needs its preview. (Superseded in 2026-10-26's second follow-up, section 19: the state fingerprint was replaced by server-held,
single-use proposals.) A routing request without a mode, the interfaces and the preview's reference is refused (`preview_required`),
the earlier single-interface request included; removing a network's routing needs it too and records the previous policy. Regression evidence
(`TestIntegration_Routing_NoRequestCanReplaceOrRemoveAnApprovedRouteWithoutItsPreview`): a shared set survives, unchanged, the legacy request, the legacy request with a routing mode,
a dedicated replacement with no, empty or stale fingerprint, a removal with none or a stale one, and a fingerprint taken from an earlier preview. Live (deployment, against a
network id that does not exist, so nothing could change): all refused, and every network's policy and fingerprint identical before and after.

**Rollback.** Drilled on a disposable database as `iam_v2_owner` with 34 shared, 179 dedicated and 12 none networks: the down migration keeps all 269 mapped rows and saves the
policies; applying 0155 again restored 34, 179 and 12 exactly (the four over-cap fixtures stay undecided, fail closed). Software-first order, the older-release behaviour and
the amendment of 0155 (a save-and-restore added after its live application; the live database is unaffected) are in §16. scd now treats an absent policy table as no policy
(one mapped interface keeps working, several refuse), tested by renaming the table under a running resolver.

**Guided editor.** The room sign-in editor shows its step (choose, then review and confirm), marks the fitting option as recommended, disables an option with the reason
(for example a shared set when only one PMS is in service), counts the shared selection, states in a sentence what the choice means for guests, returns to step 1 with a clear
message and the reloaded state when the routing changed after the preview, and confirms with a message that repeats the effect.

**Read-only reconciliation of PRE-LIVE after revisions 99 and 100** (21:34 UTC, 2026-10-25; no network or PMS change was made by the agent, and re-read after the deployment below:
identical apart from one `backup.created` by `admin`).
* Revisions: 100 active (applied 20:17:07, confirmed 20:17:15 by `admin`), 99 and 98 superseded (98 is your Full Reset, 14:58:53 / 14:59:06). Nothing staged or in flight.
* Client port `ens192` is a trunk: *Client Wi-Fi* untagged (native, `10.20.0.0/20`), *VLAN 60* (`10.60.0.0/24`) and *VLAN 90* (`10.90.0.0/24`) tagged, all enabled and
  confirmed running; the VLAN devices and bridges are up with their gateway addresses and routes; Kea's file lists exactly those three subnets, written at the revision-100
  confirmation; the management address `172.21.60.25/24` (historical) and the default route are unchanged; no service is in error.
* Archive: two (*PRELIVE Test Guest* untagged and the earlier *VLAN 60*); the other *PRELIVE Test Guest* (VLAN 90) was restored from the archive by you and is switched off. Nothing
  archived is enabled. VLAN 60 and VLAN 90 were created by the Access-to-Trunk changes.
* Room sign-in: *VLAN 90* is dedicated to Protel and ready (set at 20:18:25); *VLAN 60* (set, then returned to not decided) and *Client Wi-Fi* are undecided and each raises one
  decision warning; the two restored/archived test networks keep their migrated dedicated routes; no mapping without a policy, no policy without a network.
* Diagnostics: no failing check on any of the three running networks. **`ens192.60` has received 0 frames (17 sent) and `ens192.90` 0 (14 sent)** while `ens192` itself carries
  traffic: the same signature as the earlier VLAN 60 observation, now on both VLANs. It shows that no tagged frame has reached this appliance's network stack on those VLANs; it
  does not show where they are lost. The switch trunk, the ESXi port group (VLAN ID 4095 is needed to receive tagged frames), the access-point mapping and the virtual NIC are
  unexamined. Active guest sessions: 0.

**Deployment of this follow-up (software only; no schema change).** edged and scd `1500865c` (sha256 `852373f7...` and `ce292ebd...`, `vcs.modified=false`, production tag),
Admin Console release `20261025-215640` built from `1500865c` (BUILD_ID `lshkkUlm4k3eLnLbQnfjd`). Backups `edged.bak-pre-1500865c-20261025T215629Z` and
`scd.bak-pre-1500865c-20261025T215634Z`; console `hotel-admin.previous` -> `20261025-201251`. `DEPLOYED_BINARIES` rewritten from the installed bytes (7 of 7), every unit active, no
error logged. A real browser check at 1280 px and 390 px found no overflow and the guided editor behaving as described (previewed, not confirmed).

## 19. Second follow-up (2026-10-26): the approval is a proposal, not a state; the rollback boundary is enforced

**Why the fingerprint was not enough.** The previous check compared a fingerprint of the *current* routing, which ordinary reads returned. It proved the state had not moved, not that
*this exact change* had been previewed and accepted by *this operator*, and a change away and back (shared, dedicated, shared) restored the same fingerprint, so an approval
older than both changes would have been accepted again.

**What replaced it** (details in section 16). A preview mints a server-held, single-use, 15-minute proposal bound to the network, the previewing operator, the exact mode and
interfaces, and the network's change counter. Reads never expose it; every applied change to a network retires all of that network's proposals; removal is previewed (mode
`UNDECIDED`) and confirmed the same way (`DELETE ?proposal=`). Authorisation (`pms-routing` write), the server-side validation (cap, service state, duplicate source, archived
network) and the audit run on the apply exactly as before, and every doubt refuses.

Regression evidence (integration, full-schema PostgreSQL; unit tests on the store): an ordinary read exposes nothing that authorises a change; an invented, empty or old-style
reference changes nothing; a proposal confirmed for other interfaces, another policy, or as a removal (and the reverse) is refused; a proposal works once; an approval made before
two intervening changes that restored the identical state is refused; a removal approval from before an intervening change removes nothing; the removal preview lists what goes
and is audited with its proposal; another operator, another network, an expired proposal and a bounded store are covered at the unit level.

**Rollback boundary** (section 16): `scripts/binary-rollback.sh` and `0155.down.sql` both refuse while a Shared STRICT policy exists. Review found the tool's count query failed on a
database without the policy table (PostgreSQL resolves the table before the `CASE` runs), which would have refused a harmless rollback; the existence check is now a separate query,
tested through a stand-in `docker` (50 cases) including an absent table.

**A candidate's negative answer is only as fresh as its roster (migration 0156).** Review also found that the per-candidate feed check passed `now()` as the evidence time, which makes
the revision's `max_auth_cache_age` ceiling trivially true. A shared network could therefore take a disconnected PMS's expired roster as a trustworthy "no such guest" and admit another
interface's guest. `p3_interface_can_authorise` now uses the roster's own age as the evidence (`now()` while the feed is live and in sync, the last complete synchronisation otherwise), so an
expired candidate is STALE and the network fails closed; the health the console shows applies the same ceiling (`ROSTER_EXPIRED`), so the readiness assessment and the resolver agree.
Tests: scd (a candidate disconnected for an hour still answers; at ten days it cannot clear another interface's guest; a dedicated network is unaffected) and edged (readiness is READY
at one hour and BLOCKED with `ROSTER_EXPIRED` at ten days); confirmed to fail against the 0155 function.

No schema change: the proposals are not stored. Deployed to PRE-LIVE as software only (edged and the Admin Console).

**Deployment of this follow-up (software only; no schema change).** edged `5726db14` (sha256 `6a25228e...`, `vcs.modified=false`, production tag) and Admin Console release
`20261025-232056` (BUILD_ID `8cgt9uRsC5SDpgelCMyHC`); scd stays `1500865c`. Backup `edged.bak-pre-5726db14-20261025T232050Z`; console `hotel-admin.previous` -> `20261025-215640`.
`DEPLOYED_BINARIES` rewritten from the installed bytes, units pass, no error logged.

**Live checks** (as the verification operator; invalid requests aimed at a network that does not exist): the routing and readiness reads carry no proposal or fingerprint; the earlier
single-interface request, a replacement with no, an invented or the old-style reference, and a removal with no, an old-style or an invented one are all refused; a valid preview
returns its own proposal and is not applied; invalid previews mint none; every policy and interface set is identical before and after. The rollback tool's `--dry-run` against a real
pre-policy edged build read the live database and replaced nothing; it found no Shared STRICT network at that moment (the Product Owner's live networks are all dedicated), so its
refusal path is proven by the 47-case test suite and the disposable-database drill above, not on PRE-LIVE. PRE-LIVE changed continuously under the Product Owner's own testing
during this work (a second PMS interface now exists); that is their activity, recorded in the audit, and none of it was done by the agent.

**Deployment of the review fixes** (migration 0156 and software): schema `0156` applied after backup `db-20261025T235730Z.sql.gz.age`; edged and scd `3db9ed56` (sha256 `f7bf5285...`
and `d53f91eb...`); Admin Console release `20261025-235825` (BUILD_ID `m6vMRcAo0xTwXxIgLVMgG`); backups `edged.bak-pre-3db9ed56-20261025T235814Z` and `scd.bak-pre-3db9ed56-20261025T235820Z`.
Snapshots before and after differ only in those; units pass; `DEPLOYED_BINARIES` 7 of 7; the live function carries the roster-age evidence and `svc_scd` keeps `EXECUTE`. Not exercised live:
a stale roster with a real disconnected PMS, and the rollback tool's refusal against a live shared network (the Product Owner's networks are dedicated); both are proven by tests.

## 20. Several PMS Interfaces of one vendor (2026-10-26)

**Defect.** Creating a Protel FIAS Interface was refused (`protel_fias_exists`) whenever the property already had one, so a second, independent Protel system could not be configured. The
premise ("a second link is a second writer") is true only for the same PMS server, not for the same vendor.

**Rule now.** The vendor is not the unit of ownership; the physical source is. Every Interface keeps its own identity, revisions, sealed credentials, Stay and guest namespaces, health, lifecycle,
audit, routing and posting target, and any number of Interfaces of one connector kind may exist. One source (connector kind plus lowercased, trimmed endpoint, the existing `source_fingerprint`) has one
owner:

- **Publishing** a revision, and **activating** an Interface, are refused with `409 pms_source_in_use` while another Interface that is not decommissioned has that source in its published revision. The refusal
  names the other connection's label, never the address, and changes nothing. Two claims racing for one source are serialised by a transaction advisory lock on the source.
- A decommissioned Interface releases its source. Drafts and unpublished Interfaces claim nothing.
- **Runtime backstop.** pmsd starts one worker per source: if rows written any other way give two live Interfaces the same fingerprint, the one already running keeps it (otherwise the lowest id) and the
  other gets no connection, so there is never a second writer on one socket. `GET /pms-source-conflicts` still reports such a pair.
- Routing is unchanged: a Client network maps to Interfaces by the policy in section 16, and a Shared STRICT set still needs every candidate to answer.

**Limit.** The fingerprint compares the configured address text. The same server reached once by name and once by IP is not recognised as one source; the operator is the check there.

**Tests.** edged integration: three independent Protel Interfaces created, published and activated with distinct fingerprints; a second claim on one address (case and spacing varied) is refused at publish and at
activation, leaves the interface unpublished, and succeeds once the owner is decommissioned. pmsd: one worker per source, a running worker keeps it. Console: the refusal is explained in words.

**Review fixes and live acceptance (PR #252 follow-up).** A REST connection whose identity cannot name one property (a Mews token without an enterprise id) claims no exclusive source, so two independent properties
on one platform can both be published; and the pmsd reconcile stops every obsolete or displaced worker before starting any, so a source handed from one Interface to another has one writer throughout.
On PRE-LIVE, pmsd was restarted with no pending postings (the existing Protel link reconnected and resynchronised in about 37 s, identity, revision, stays and financial counters unchanged), and a second real
Protel Interface (Coral Sea Aqua Club, read-only, posting UNSET) ran concurrently with the first. Authentication-only room sign-in on the live Client Wi-Fi network gave the expected result in every routing scenario
(dedicated to each hotel, shared strict, and shared strict with one candidate off, which failed closed), a duplicate-source publish was refused, and the network's routing was restored. Aqua was left AUTH_DISABLED.

## 21. The administrator's journey across several PMS connections and several networks (2026-10-26)

This section records the decisions behind the Hotel Admin changes that make a site with more than one PMS connection and more than one client network readable.

**A room belongs to its PMS connection.** A room number identifies a room only together with the connection that reported it; the same number at two connections is two rooms, never a duplicate.
Stays, PMS reconciliation (unresolved departures, rooms with several stays, stays past departure), roster reconciliation runs, sessions, devices, post-stay access, settlements and the
cross-PMS transfer now name the connection (only where the site has more than one, so a single-PMS site is not cluttered) and key their rows by connection and room. The Stays page has connection
tabs with counts under the current filters and a combined "All connections" view; the API takes `pms_interface_id` and answers `by_interface`. The guest-facing rule is unchanged: a guest is never
asked which PMS they belong to, and identity stays behind the same permission.

**Removing a connection (migrations 0157 and 0158).** `GET /pms-interfaces/{id}/removal` assesses, `POST` acts, both under a row lock and the same transaction logic, so what the administrator confirmed is what
happens (`removal_changed` otherwise). Outcomes:

| Outcome | When | What happens |
|---|---|---|
| `DELETE` | switched off, never switched on (no audit record), never connected to its PMS, and nothing refers to it: no stay, event, posting, answer, routing, package mapping, sign-in record, conflict, or log of changes to its settings (migration 0158: that log is history, so a connection whose settings were ever changed is retired) | `iam_v2.delete_unused_pms_interface` removes the connection, its draft revisions, sealed secrets and runtime row. It is the only path that deletes one, and it fails (rolling back) on any record that refers to the connection. |
| `RETIRE` | the connection was ever used | lifecycle `DECOMMISSIONED` (terminal). Hidden from ordinary lists (`?include=retired` shows it). Every stay, event, entitlement, posting, sign-in record and the audit trail is untouched; open financial work is refused by the existing decommission gate. |
| `BLOCKED` | switched on or draining; a serving network is routed to it; financial work is open | nothing changes; the reasons are listed, with the networks named. Nothing is reassigned for the administrator. |

Revisions and secret generations stay immutable: the guards allow a delete only inside that function, for that connection (a transaction-local setting the function sets after it has verified the connection
is unused). A deletion or retirement needs the operator's password, a reason code and the connection's name typed, and is audited.

**One reading of where a client network stands.** `service_state` is `serving` (enabled, not archived), `off` or `archived`, produced once on the server (`networkServiceState`) and returned by the network list, the network
overview and the portal design list. Selectors offer only serving networks as places to do something (assign a design, route room sign-in); off and archived networks are shown, marked, where their history or
settings still matter. The dashboard and overview no longer list archived networks as current ones.

**A port is a wire, a network is a purpose.** The Client ports card lists, per port, the untagged (native) network and each tagged VLAN with its own state, subnet and connected clients, and says in words that a
trunk can carry a native network and tagged VLANs together. The "Change VLAN or port" dialog now opens showing the network's current topology (it used to open on the opposite one). Replacing a network's topology
carries its sign-in narrowing and portal design to the successor (and removes them again if the replacement is reverted); it used to carry only addressing, reservations, routing and policy.

**What a network actually offers.** `GET /network/sign-in-overrides` returns every network's narrowing in one read. Sign-in methods shows, for each serving network, which methods are offered, which are off here, off
site-wide or need setup, with the reason, from `lib/sign-in-effective.ts` (a mirror of what scd decides: a network can only narrow what the site offers; a network set to no room sign-in offers none; room sign-in needs a
PMS connection that can answer). The packages list shows who can get each package (the networks and PMS connections a package is tied to); a package's PMS rule is picked by name, not by pasting an id.

**Where each setting lives.** Site-wide: the switches on Sign-in methods. Network-specific: a network's own narrowing, design and addressing. PMS-specific: room sign-in routing under PMS routing and each connection's
own configuration. Package eligibility: Internet packages. None of them changes another; the screens say so.

