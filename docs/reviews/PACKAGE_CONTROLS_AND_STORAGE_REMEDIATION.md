# Package controls, backup and storage remediation, continuity corrections (2026-10-31)

Branch `delivery/package-controls-backup-remediation` from master `f059af95`.

Implemented on 2026-10-31 with test execution suspended. On 2026-11-01 the Product Owner authorised completion, verification and the
protected merge (no deployment). Section 7 records what was run and what testing found; section 6 the decisions taken.

## 1. Staff End Package / Reset Package

**Contract.** Migration 0202 adds definer functions owned by `iam_v2_owner`. `svc_edged` may only EXECUTE them (Gate-P
`svc-package-controls-grants.sql`); no table privilege is added.

**End** (`iam_v2.admin_end_package`)
- Ends the whole entitlement with reason ADMIN, through the existing end-access primitive `p4_end_access_funded_by`. That closes every open device interval and ends every active or pending session.
- Returns the sessions it ended. edged then asks scd (`/v1/admin/sessions/withdraw`) to withdraw each address from the gate at once.
- scd acts only on a session of this site that is ended, and never on an address another live session now holds.
- The result reports how many withdrawals were confirmed. Enforcement also converges from the database, so an unconfirmed withdrawal is said to be unconfirmed, never "done".

**Reset** (`iam_v2.admin_reset_package`)
- Makes a new entitlement linked by `supersedes_entitlement_id`, funded by a purchase with trigger `ADMIN_GRANT`, `amount_minor 0`, no quote, no settlement and no currency.
- Copies:
  - the package and plan revisions (speed, device limit and policy, time mode);
  - the frozen data allowance;
  - the end mode;
  - the policy snapshot.
- Window: a duration window restarts at the reset with the original length. A fixed instant is kept, and the reset is refused once that instant has passed. No window stays no window.
- A live original is terminated SUPERSEDED in the same transaction. Its online devices and live sessions move through `rebind_session_entitlement`, so accounting follows the instant.
- The original's counters, purchase, payment and settlement are untouched. No row is reopened and no counter is zeroed.

**Refusals**, raised as `PKG_<CODE>` under the stay and entitlement locks:
- a subject that is suspended for an independent reason;
- an original ended REVOKED, TRANSFERRED, CONVERTED, CANCELLED or by CHECKOUT;
- a disabled Client Account;
- a voucher that is not redeemed (revoked or expired);
- a room package whose stay is not in house;
- After Check-out whose stay has not checked out;
- an original already superseded;
- a subject that holds other live access;
- a package not yet started;
- a fixed end time that has passed.

**Idempotency and concurrency**
- Every request carries a client-generated request id, unique in the append-only `iam_v2.package_admin_actions`.
- A repeated request returns the recorded outcome. A request id reused for another action is refused.
- The request id takes an advisory lock, then the stay and the entitlement are locked, in the order every writer uses.
- The partial unique indexes guarantee one live entitlement per subject.

**No automatic re-acquisition after an End** (Product Owner, 2026-11-01). `iam_v2.package_ended_by_staff` is the one rule:

- the same package (any revision) and the same subject; an anonymous subject is matched by the devices the ended package had;
- for a room package, for the rest of that Stay. A later, independent Stay is not affected;
- for any other subject, until the moment the ended package would have ended anyway. One without an end stays ended until staff reset it;
- it covers free self-service and automatic acquisitions only (`GUEST_SELECTION`, `ACCOUNT_AUTO_GRANT`, `OTP_SOCIAL_DEFAULT`,
  `VOUCHER_REDEMPTION` at price 0). A priced purchase, a staff grant, After Check-out, a transfer, a conversion and a renewal are never
  blocked;
- a staff Reset of the ended package lifts it.

It is enforced by the entitlement guard `staff_ended_benefit_guard`. The room-offer list (scd) and the account, Client and voucher
offer list and quote (commerce engine) read the same rule, so a guest is never offered what the grant would refuse. `svc_scd` holds
EXECUTE on it through Gate-P; `svc_edged` does not.

**The staff reason is a staff note.** `package_admin_actions.reason` is registered in the 0147 scrub registry: the staff-notes period
removes it, and the three erasure functions (`anonymise_stay`, `anonymise_guest_account`, `anonymise_guest_principal`, re-declared from
their exact 0147 text with one statement added) remove it with the guest's other staff notes. Outside the scrub the table stays
append-only.

**After Check-out**
- A reset carries only the devices the checkout carried over (grandfathered rows), and none that left by a deliberate act.
- A carried device that is offline is recorded `DISCONNECTED / PACKAGE_RESET_COHORT`. `after_checkout_device_guard` lets exactly such a row return; a new device or an unverified rotated MAC is still refused.
- A reset is not a `CHECKOUT_GRACE` purchase, so the checkout event still makes at most one automatic grant.

**Admin Console.** The shared `components/package-controls.tsx` is on:
- the Stay drawer;
- the session drawer (active access);
- the Client Account editor;
- the device detail.

The dialog names:
- the subject kind and id;
- the PMS connection, room, arrival and departure, and check-out time (never the room alone);
- the package, plan, speed and device limit;
- usage against the allowance;
- the end, the devices online and the live sessions;
- the original payment;
- the impact;
- earlier staff actions.

It requires the new `package-controls` permission (write: IT manager, client services and client relations operators; read: site viewer), the password step-up, a 4 to 500 character reason, and explicit confirmation. Everything is audited (`package.ended`, `package.reset`).

**Related fix.** The room-package offer list hid a package whose live access came from a reset, so a second device could not join it. The predicate now follows the live entitlement's package revision as well as its purchase (`cmd/scd/phase3_offers.go`).

## 2. After Check-out Access and outage continuity

The final decisions are recorded in `docs/architecture/ONEGATE_OUTAGE_CONTINUITY.md` section 1a.

Corrections:

- **F2, recently admitted sessions.** A class created after the last 30 s counter sample had no baseline, so a budgeted session got no continuity until the next sample. Each sample now records which classes existed, and a class created after it is metered from zero. That is conservative. What remains is inherent: a session admitted inside the last plan interval is not in the record.
- **F5, fewer elements than the record.** The cause is not established and cannot be found by reading code. The governor now reports, per reason, which held sessions it did not renew (status `not_renewed`, plus a log line), so the next occurrence is explained.
- **The production database-role gap.** This is the root cause of last mission's missing grant reaching TEST. `validate-migration-grant-durability.py` followed only `svc_*` grantees. Migration 0200's grant to `iam_v2_owner` on `public.guest_networks` could not take effect when applied as `iam_v2_owner` (PostgreSQL warns, grants nothing, and the migration succeeds). Owner grants on objects outside `iam_v2` are now checked against Gate-P. The role-based integration tests are skipped without `EDGED_ROLE_TEST_DSN`; the golden lab requires it, and the End/Reset suite has a full-journey test as `svc_edged`.
- **Reboot with the database down.** It fails closed: elements are never created, and a record from another boot is refused. Unchanged, and now a stated decision; a lab scenario is prepared.
- **Documentation.** The continuity contract said "not merged, not deployed"; corrected. The disposition record said the grant validator could not read DO blocks; corrected.

The 90-second lease and every fail-closed boundary are unchanged.

## 3. Backups, Erasure Ledger and secondary storage: root causes and corrections

| # | Defect (root cause) | Correction |
|---|---|---|
| L1 | **False success on Save.** `ledgerDestinationPut` saved the setting, ran a reconciliation and answered `ok: true` whatever the reconciliation found. The console then always said "copied and read back". `ledger_destination_set` also stamped `verified_at` before anything was copied. | Safe handover: the whole ledger is written to the NEW storage and read back (ledger id, last entry, hash, object checksum against its note) under the ledger lock. Only then is the setting saved, with the previous one in use until that moment. The answer is 200 only when the following reconciliation confirms "in step". Otherwise: "saved, not in step" (502), "recovery needed" (409), or "not saved" (502/422). The console never shows a success for those. |
| L2 | **"Test" looked like replication.** The test writes, reads back and *removes* a probe, so the folder is empty afterwards. It was labelled "Works". | Labelled **Connectivity test**; the answer says nothing of the ledger was copied (`replicated: false`). |
| L3 | **SFTP path confusion and chroot.** Absolute paths only. The folder written to was never shown, so a chrooted account or a different login root made "the folder" on the server differ from what the administrator looked at. | Relative folders are allowed (relative to the login folder). `..` and backslashes are refused. The folder is created and checked to be a folder. The **resolved path the server reports** (`RealPath`) and its free space (`statvfs`, where offered) are shown after a test and in the status. |
| L4 | **Temporary-file hazards.** A fixed `.partial` name could collide; a failed open could leave state behind. | A unique `O_EXCL` temporary per write, always removed on failure, then an atomic rename. Without posix-rename, the two-step replace is still proven by read-back. |
| L5 | **No edit.** The form started empty and an edit had to re-enter every credential. | The saved non-secret settings and which credentials are saved are returned and prefilled. An empty credential keeps the saved one only for the same account on the same server or bucket. |
| L6 | **No removal.** No API or function existed. | `ledger_destination_clear` (0203), password and typed confirmation, audited. The remote copy is not deleted. Erasure is refused again. |
| L7 | **No change history.** | Append-only `public.ledger_destination_changes` (trigger on every set, replace and removal; non-secret settings only), shown on the card. |
| L8 | **"In step" had no visible proof.** | The status carries the entry, hash and object the copy was read back at. |
| L9 | **Existing entries.** The push always wrote the whole primary copy, but a primary copy behind the database was pushed as it was. | Save first brings the primary copy in step with the database (the reconciliation heals and extends it), refuses if they still differ, then writes every entry. Ten local entries mean ten at the new storage, read back. An ahead or forked remote ledger is never overwritten. Another appliance's ledger is refused. |
| B1 | **Retention 7 + 3.** | At most five at rest: 3 regular plus 2 protective by default, validated together. Older files resolve to the cap. The console shows the cap and the earlier backups it does not remove. |
| B2 | **Rotation could remove the only good backup.** A newer broken or unverified set counted as kept. | A good set (files, marker, no checksum problem) is removed only while a newer good set of its pool is kept. Sets are always removed whole. |
| B3 | **No off-device backup copy.** | Optional `BACKUP_REPLICATE` (off by default) copies every retained set to the secondary storage. It streams (S3 signs with a precomputed SHA-256, never unsigned), reads every file back in full and compares SHA-256. Copies are marked complete only when all files verified. It removes only copies it made, of sets rotated locally, after every retained set is copied, and never touches a local backup. A replaced storage receives every retained set. |
| B4 | **Client account Delete/Remove looked like an erasure** but never wrote the ledger. | The console says it is not an erasure and points to Guest data → Erase; the API answer and the audit entry carry `erasure: false`. Actual erasure keeps the mandatory ledger. |
| B5 | **Every restore failed once 0203 was applied** (found by the restore drill). The restore replays the ownership it captured, including `ALTER SEQUENCE … OWNER` for a sequence owned by a column, which PostgreSQL refuses whenever that owner differs from the restoring role. The restore rolled back safely each time. | Column-owned (serial, identity) sequences are no longer re-owned on their own; their table's `ALTER TABLE … OWNER` carries them. The test harness now applies a platform migration as the platform role, as the appliance does. |
| M1 | **Migration 0203 could not be applied by a live upgrade**: it creates a table in `public`, and the upgrade's least-privilege role has no CREATE there (found by building the schema the way an upgrade applies it). | Marked `-- edge-migrate: platform`, like 0198. |

**Storage destinations.** One abstraction (`ledger.Destination`, plus the new `Locator` and `FileDest`) serves:

- the appliance's own disk (the primary copy);
- an external USB/SATA disk, and SMB/CIFS or NFS NAS shares mounted on Linux. These are the `dir` kind, independence checked from the kernel mount table, with the mount type, source and free space reported;
- SFTP, with a password or an SSH private key and a pinned host key;
- S3-compatible buckets (AWS, B2, Wasabi, and self-hosted MinIO, Ceph or NAS over https).

No FTP, and no TLS or SSH verification bypass. Nothing goes to OneGate Central.

**Erasure stays fail-closed.** Backups never need the secondary storage. An irreversible erasure still needs a verified independent copy.

## 4. Tests prepared (not run)

- `cmd/edged/package_controls_integration_test.go`:
  - End: whole package only, idempotent, ALREADY_ENDED, request id reuse refused, audit;
  - Reset: free, linked, original untouched, device moved, no settlement or posting, a second reset refused, two concurrent resets with exactly one winner, one live entitlement;
  - After Check-out cohort, with a new device refused and no `CHECKOUT_GRACE` purchase;
  - refusals: checked out, role, reason;
  - the whole journey as `svc_edged`.
- `cmd/scd/backup_mirror_test.go`: copy verified by read-back, follows rotation, foreign files untouched, a failure never touches local and is never shown copied, off by default, a replaced storage gets every set.
- `cmd/scd/backups_test.go` (three plus two policy; the only good set is never removed; no orphans) and `backup_settings_test.go` (five-set cap; old seven-plus-three files resolve to it).
- `cmd/netd/phase3_continuity_test.go`: a recently admitted session is covered, metered from zero, and still ended by its allowance.
- `hotel-admin/test/ledger-card.test.tsx`: updated labels; connectivity wording; saved-but-not-in-step shown as a warning; prefill with a kept credential; removal with typed confirmation.
- Golden-lab scenarios `tools/oglab/golden/scenarios/` (each at most 300 s): `continuity-1min`, `reboot-db-down`, `package-end-reset`, `ledger-sftp` (chrooted SFTP, relative folder, server-side SHA-256), `backup-five-sets`.

**Known to need attention when testing is authorized:**

- The console role matrix JSON was edited by hand. The generator test (`TestConsoleRolePermissionsAreGeneratedFromTheServerTable`) must agree.
- Existing console tests that assert exact dialog text may need the new lines.
- The continuity test's newcomer case assumes the plan walk proves a newly created class renewable in the same submission.

## 5. Not verified

Nothing in this branch has run. In particular, none of the following is claimed:
- an End or Reset on a real appliance;
- an SFTP server other than the prepared lab container;
- an S3 streaming upload;
- the backup copy on real storage;
- 60- or 120-minute real outages;
- the F5 cause;
- licence interaction under load;
- the role-path proofs.

## 6. Decisions

Taken by the Product Owner on 2026-11-01:

- **Free packages after End:** they are not acquired again automatically (section 1).
- **Client Account removal and erasure:** they are distinct; removal is not an erasure, and actual erasure keeps the mandatory ledger
  (section 3, B4).

Still open:

1. **Erasure with only a local copy.** Not implemented. On the appliance alone no record exists that is as independent as the secondary copy: a restore can replace every local disk the appliance controls. Allowing it would weaken the anti-resurrection guarantee. It needs an explicit security decision; the recommendation is to keep it fail-closed.
2. **Earlier backups above the five-set cap.** Rotation never deletes them. They are removed only by the existing confirmed "Remove the earlier backups" action, which requires a verified new backup. The number at rest is shown on the console. An absolute physical maximum of five needs that action, taken by the hotel or explicitly authorized for the test appliance.
3. **Hard deletion of an unused Client Account** removes a row without a ledger entry, so an old restore would bring the account back. Under the decision above this is account removal, not an erasure, and it is labelled so everywhere; an erasure of that person is Guest data → Erase.
