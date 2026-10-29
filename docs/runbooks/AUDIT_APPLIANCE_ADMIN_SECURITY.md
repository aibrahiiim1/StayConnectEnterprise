# Appliance administration security: decisions, owner actions and upgrade plans

Records the Product-Owner decisions AD-3, AD-7, AD-8, AD-9, AD-10, AD-12, AD-13, AD-14 and AD-67 and the restore checks (P9 M3, P4 CC4),
as implemented on the appliance. The audit text is in `docs/AUDIT_DECISIONS.md`; the findings are in `docs/AUDIT_BACKLOG.md`.

## 1. Accepted positions (nothing changed in code)

These are choices to keep the behaviour, not fixes. They stay open as accepted risks until the Product Owner changes them.

| Finding | Position | Why |
|---|---|---|
| P9 L4: anyone who reaches the console can lock out a named operator | **Kept.** Repeated wrong passwords for one account slow and then lock that account (`scopeAccount`), and repeated failures from one address are throttled on their own (`scopeIP`). | An address-scoped lock lets an attacker who rotates addresses guess without limit; the account lock is the standard trade-off. The console is meant to be reachable only on the management network, and a locked account clears after the lockout time (the setting `lockout_seconds`, default 900 seconds, in Sign-in protection) or when an administrator resets that operator's password. |
| P4 L8: scd keeps no MFA failure count of its own | **Rely on edged's limit.** | scd's authenticator routes live under `/v1/admin/` and `peerGate` admits edged alone. edged authenticates the password, counts and throttles failures and audits them before it asks scd to check a code. A second counter in scd would have to agree with the first and would only add a way for them to disagree. |
| P9 L6: confirming a system-network change has no password step-up | **No step-up on confirm.** | Apply and rollback already need the operator's password (`TestClientNetworkChangesRequireStepUp`). Confirm only keeps a change that is already applied and would otherwise roll itself back, so it is the safe direction; a step-up on it would make the rollback timer harder to stop. |

## 2. Owner-action checklist: SH-1 and SH-3 (AD-14)

These cannot be done from the code. Both are **EVIDENCE-DEPENDENT**: the repository cannot show them done.

**SH-1: the Gmail app password that was once committed.**
1. Sign in to the Google account that owns the app password and open *Security, 2-Step Verification, App passwords*.
2. Delete the app password that Alertmanager used. If you are not sure which one, delete every app password named for the appliance or Alertmanager.
3. If Alertmanager is still to send mail, make a new app password and place it only in the file named by `smtp_auth_password_file` (never in the repository, never in `alertmanager.yml`).
4. Record the date and who revoked it in the Product Owner's own register. The old value stays in Git history, which is never rewritten, so the revocation is the only fix.
5. Evidence to keep: a screenshot or export of the account's app-password list after the deletion (without the secrets).

**SH-3: older appliances keep the database password they were initialised with.** The Product-Owner decision of **2026-10-19 stands**: nothing rotates the password automatically, and Hotel Admin's Overview warns "The database still has its default password" to roles that can read diagnostics (edged re-tries the default login every six hours and clears the warning when it is refused).
- To clear it on one appliance, follow *Migrating an existing appliance* in `docs/DEPLOYMENT_APPLIANCE.md` (generate a password, write `pg-superuser.env`, `ALTER ROLE`, check nothing connects with the old one, export before any later `compose up`).
- Fresh installs already generate the password. An upgrade path that rotates it is a future decision; until then the warning is the control.
- Evidence to keep: the Overview no longer shows the warning after the next six-hourly probe, and `grep -l 'stayconnect:stayconnect@' /etc/stayconnect/*.env` returns nothing.

## 3. Backup downloads (AD-9)

- Only `db-*` database dumps can be downloaded, and only when they are **encrypted** (`.sql.gz.age`). Deployment, privilege-configuration and every other artefact is listed but not downloadable, and an unencrypted `.sql.gz` is refused (403 `download_refused`) at the ticket request and again at the download, even for a ticket issued earlier.
- The backup engine always encrypts: a backup that cannot be encrypted to a key holder is not taken (`errNoBackupKeyHolder`), so every backup taken since the key register exists is downloadable. An older unencrypted dump keeps its place in the list, marked **Not encrypted**, and can still be verified and restored; take a new backup to get one that can be downloaded. Nothing is deleted.
- The sealed copy of the ledger key (section 4) is not a `db-*` file and is never offered for download.

## 4. The erasure ledger is authenticated (AD-3, migration 0220)

- Each appliance has its own ledger key (32 random bytes made by the migration, held only in `iam_v2.ledger_mac_key`). Every entry carries a MAC of its hash; the head note kept beside the secondary copy is MACed too. Whoever can write the secondary storage cannot make an entry or a note that checks.
- `ledger_apply` applies only checked entries: a valid MAC, or coverage by a valid seal (below). A MAC that is present and wrong is a forgery and stops the replay at that entry; restore and recovery refuse and replace nothing.
- `ledger_confirm` is no longer granted to edged. edged confirms with `ledger_confirm_checked(hashes, receipts)`, where a receipt is a MAC only scd can make after it has written both copies.
- A copy of the key is sealed to the backup key holders in `/opt/stayconnect/backups/ledger-mac-key.age` (rewritten hourly while a key holder exists). A replacement appliance restoring from the secondary copy gives a key holder's key; scd opens the sealed key and installs it in the restored database. The key also travels inside every database backup, which is encrypted.

**Existing copies that carry no MAC (every ledger written before 0220).** Their entries are real but unauthenticated. The plan, which is automatic and safe:
1. After the upgrade scd waits until the database, the appliance file and the secondary storage are all in step (state `ok`) and then appends one MACed **seal** entry that names the last unauthenticated entry and its hash (`iam_v2.ledger_seal_legacy`). The chain ties everything before it to that hash.
2. Until the seal exists, unauthenticated entries are never applied to a restored database (a restore of an older backup is refused with a plain reason). Nothing is erased on ambiguity.
3. If the three copies do **not** agree, no seal is written and the ledger reports the disagreement exactly as before (state `blocked` or `recovery_needed`). Resolve it with the existing recovery path; the seal follows on the next hourly run.
4. Check: `SELECT iam_v2.ledger_head()` shows `"unsealed": 0` once sealed.

**EVIDENCE-DEPENDENT (needs the Linux restore drills):** restore from an older backup with both copies, from the secondary copy alone on a replacement appliance (key from the sealed copy), and from a backup that predates 0220. Run `scripts/ledger-restore-drill.sh` and `scripts/ledger-linux-checks.sh` on a Linux host and keep their output.

## 5. Other implemented decisions

- **AD-7.** Every guest list page view is recorded as `guest_data.listed` with its filter (allow-listed parameters with plain values) and never with search text, once per person and list per ten minutes. A search is still `guest_data.searched`.
- **AD-8 (P9 L2).** The desk must give a reason of 4 to 500 characters to replace a client account's password; it is recorded with the audit entry. No step-up.
- **AD-10.** A reassignment to another customer needs a backup key holder before the departing customer's archive is written; the archive is never written unencrypted. If there is none, nothing is removed and the error says to add one under Backups, Backup keys, then restart the service. Adding a key holder does not depend on the move, so this cannot deadlock a legitimate move and no override exists.
- **AD-12.** `internal/iamv2/tenant_isolation_catalog_test.go` holds two checks. The catalogue check (needs a database) requires every `iam_v2` table to have `tenant_id` and `site_id` or a stated reason it does not. The query-path check lists every SQL literal that reads or writes a tenant-keyed table without `tenant_id`; 150 existing ones are recorded in `testdata/iamv2_unfiltered_queries.txt` as a **ratchet**: a new one fails the build, and entries may only be removed. Those 150 are not claimed to be safe: most reach a row by a key an earlier tenant-filtered step produced, and each should be reviewed and given the filter. This is the accepted state until that review is done; RLS (AD-12 option b) is not enabled.
- **AD-13.** An SMTP server's own words (greeting, refusal reason, banner) are never shown to an operator; `last_error` and the test answer carry a generic instruction for the failed step, and the server's text goes to the journal (`notifyloader: mail server refused the send`). Ports are not restricted.
- **AD-67.** Before a retention period is shortened the console shows what the new periods remove, by category, counted by the retention functions themselves in a rolled-back sub-transaction (`public.data_retention_preview`, migration 0221). The change is saved only when the request repeats the number shown (`confirm_records`) after the password; a shortening that removes nothing more needs no second confirmation. System logs (`log_days`) are removed by scd from the journal and are not in the count. If the preview cannot be worked out, the shortening is refused.
- **P9 M2 / P9 L16.** Adding, changing or removing an email, SMS or WhatsApp sender, or a social sign-in provider, and changing the client device self-service switch, need the operator's own password. A DELETE carries it in the body. (`scripts/portal-social-offering-test.sh` is a live harness that calls these routes without a password and needs one added before it is run again.)
- **P9 M3.** A restore compares the backup's schema (tables, columns, types, functions) with the live database, which is what the installed software expects, before anything is replaced. A backup from older software, from newer software, or from neither is refused and the reason says which. This is the same rule the retention check already applied to the retention functions. Restoring an older backup after a deployment therefore needs the deployment rolled back, not the screen. Running the forward migrations during a restore (the audit's other option) is not implemented. **EVIDENCE-DEPENDENT:** the restore drills on Linux.
- **P4 CC4.** The two places that formatted values into root SQL now pass them as psql variables (`-v name=value`, used as `:'name'`) or, for the ledger document, inside a per-call random dollar-quote. The comment-volume part of CC4 is not done.
