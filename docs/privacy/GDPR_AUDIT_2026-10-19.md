# GDPR audit of OneGate: 2026-10-19

> Read-only audit of `delivery/gdpr` at `a04127a1`. Six parallel reviews covered:
>
> - the data inventory;
> - retention and guest rights;
> - appliance security;
> - transparency and outside companies;
> - Central;
> - the accuracy of the documents.
>
> The main findings were re-checked in the code: marked **✔ confirmed**. The rest are reported as the reviews
> found them, with their evidence. Nothing was changed. Known, already-recorded items are not repeated: SSH
> (D-SSH), disk encryption (C11), FIAS TLS off by default, backups unencrypted until a key holder, and the
> Central database superuser.

## Summary

The foundations are sound:

- no card data reaches the appliance;
- no guest data goes to Central;
- outside companies receive the minimum;
- retention, export and erasure work and are hard to misuse;
- the anonymisation tokens cannot be reversed;
- staff access is role-based, with step-up and audit;
- the portal loads nothing from outside by default.

The gaps fall into four groups:

1. **Personal data reaches logs that the documents say it does not reach** (staff searches, pre-sign-in web
   requests).
2. **A few guest-facing protections are weak by default:** the HTTP fallback, client isolation off, the room
   sign-in throttle, and the privacy notice can be left empty.
3. **Coverage gaps:** some stores are outside retention, export or erasure.
4. **Documents that overstate or misstate the code.** This includes errors in today's Central retention and
   notice work.

| Priority | Count |
|---|---|
| A: fix before launch | 8 |
| B: fix soon | 12 |
| C: low / wording | 10 |

---

## A. Fix before launch

**Status, 2026-10-19 (same day):**

| Item | Status |
|---|---|
| A1 | **Fixed** (`internal/reqlog`, Caddy log filters) |
| A2 | **Fixed** (`internal/reqlog`) |
| A3 | **Fixed:** isolation on for new networks, and a warning while HTTPS is not active, as the Product Owner chose |
| A4 | **Fixed** (migration 0136): a network pause with staff confirmation, as the Product Owner chose (below) |
| A5 | **Fixed:** a built-in notice and a warning |
| A6 | **Fixed by retention** (migration 0135); the table is kept (Product-Owner decision, 2026-10-19) |
| A7 | **Documented** |
| A8 | **Fixed** (0054 extended, HMAC counter key, notice and record corrected) |

**A4, how it was decided.** The device limit replaced a per-room and per-address throttle by an earlier
Product-Owner ruling (`cmd/scd/phase3_auth.go`). The Product Owner chose a per-network pause, with staff able to
confirm genuine guests (2026-10-19):

- **The pause.** 30 wrong room sign-ins on one network within 10 minutes, from any devices, pause Room sign-in
  there for 15 minutes. These are settings.
- **What staff see.** Hotel Admin shows the pause on Sign-in methods and Guest sign-in attempts.
- **What staff can do:**
  - let a genuine guest's device try again, picked by the room it typed;
  - end a false-alarm pause.

  Both need a reason and are audited. Neither signs anyone in.

**A1. Guest search terms are written to the request logs. ✔ confirmed**

- **Evidence:**
  - edged logs every full request URI (`data-plane/cmd/edged/main.go:367`, `middleware.Logger`, no
    redaction). Hotel Admin puts guest data in URLs:
    - the guest-data search `?q=<name/email/phone>` (`hotel-admin/app/(app)/guest-data/page.tsx:88`);
    - `/usage/devices/<mac>`;
    - `?room=` on sign-in attempts.
  - The Hotel Admin Caddy access log (`deploy/scripts/hotel-admin-cert-manager.sh:218-221`) records the URI and
    request headers, including the `X-*-Search` headers that were added to keep searches out of logs.
- **Why it matters:** names, emails, rooms and MACs sit in the journal and in `/var/log/caddy` for the log period.
  `guest_data_rights.go:14` and our documents say searches are not recorded.
- **Fix:**
  - add a query-redacting logger to edged, as portald has;
  - move `q` and `room` into headers;
  - add a Caddy log filter dropping `X-*-Search` and the query string.

**A2. The pre-sign-in web request is logged, which contradicts "OneGate does not record the websites visited".
✔ confirmed**

- **Evidence:**
  - Before sign-in, guest port 80 is redirected to portald (`deploy/nftables/stayconnect.nft:100`).
  - portald logs the method, host and path with the guest's IP (`cmd/portald/main.go:627`). Only the query is
    redacted (`:805-813`).
- **Why it matters:** browsing data is recorded (pre-sign-in, plain HTTP only, kept 14 days) while the record
  template and DPIA deny it.
- **Fix:** log only the path class for captive redirects, with no host and no original path. Otherwise, correct
  the documents.

**A3. Guest credentials can travel unencrypted, and client isolation is off by default. ✔ confirmed (isolation)**

- **Evidence:**
  - Until the portal certificate is issued, every route is served over HTTP
    (`cmd/portald/portal_https.go:226-242`). Kea hands out the `http://` portal address
    (`deploy/kea/kea-dhcp4.conf:49`).
  - `client_isolation_enabled` defaults to `false` (`data-plane/migrations/0002_edge_networking.up.sql:83`).
- **Why it matters:** another guest on the same network can intercept room + surname, account passwords, phone
  numbers and codes (Art. 32).
- **Fix:**
  - default client isolation to on for guest networks;
  - refuse password sign-in methods on the HTTP fallback, or show the hotel a hard warning.

**A4. Room sign-in guesses are limited per device only. ✔ confirmed**

- **Evidence:** `/auth/pms/phase3` is the only sign-in route not wrapped in the portal damper
  (`cmd/portald/main.go:671`). scd applies no `throttleGuard` to it; every other method has one
  (`cmd/scd/authsec.go:236`). The only count is per MAC (`internal/signinattempt/protection.go:72-81`).
- **Why it matters:** changing the MAC resets the count, so guessing room + surname is effectively unlimited. A hit
  reveals that a named person is in that room (Art. 32). The answers are uniform, which helps, but does not limit
  guessing.
- **Fix:**
  - add the damper and the scd throttle (method `pms` already exists);
  - add caps per IP, per room and per network.

**A5. The guest may see no privacy notice at all. ✔ confirmed**

- **Evidence:**
  - The notice block is hidden when both the text and the link are empty (`cmd/portald/templates.go:498-503`).
  - The server warns only about unfilled `[[placeholders]]`, not about an empty notice
    (`internal/portaldesign/design.go:152-154`).
  - A link to an outside page cannot open before the guest is online.
- **Why it matters:** Art. 13 requires the information at the time of collection.
- **Fix:**
  - refuse to publish without a notice text, or ship a neutral default;
  - warn when only an outside link is set.

**A6. Guest names kept indefinitely in a leftover migration table. ✔ confirmed**

- **Evidence:** `iam_v2.backfill_0066_identity_text` holds `prior_first_name`, `prior_last_name` and `prior_room`
  (`data-plane/migrations/0066_…up.sql:64-74`). It was meant to be dropped once the 0066 rollback window closed;
  that was never done. The retention run (0127) never touches it. Only an individual erasure clears it (0128:350).
- **Fix:** a migration that drops the table, which needs the Product Owner's approval as a destructive change.

**A7. DHCP device names (often a person's name) are kept, undocumented and outside guest rights.**

- **Evidence:**
  - Kea persists leases to `/var/lib/kea/kea-leases4.csv` (`deploy/kea/kea-dhcp4.conf:13-17`).
  - The hostname is read back and shown, searchable, to staff (`cmd/netd/kea.go:141`,
    `cmd/edged/client_devices_admin.go:121-128`).
  - `public.dhcp_reservations` also holds the MAC, hostname and free-text description, with no retention.
- **Uncertain:** whether Kea stores the hostname with DDNS off. Check on PRE-LIVE.
- **Why it matters:** "Johns-iPhone" is personal data (Art. 5, 13, 15, 17, 30).
- **Fix:**
  - document it;
  - include it in the export;
  - drop hostnames from leases older than the device period;
  - include reservations in retention or document them as staff-managed.

**A8. Today's Central retention and notice have errors. ✔ confirmed**

- **Evidence and gaps:**
  - `appliance_assignments` (who moved an appliance, with the operator id and IP;
    `control-plane/migrations/0022_…:12-18`) is not covered by 0054, though the notice says IPs go after 24 months.
  - `appliance_certificate_requests.source_ip` is not covered.
  - The portal certificate contact is **one Central-wide email** (`portal_tls_settings`, a single row, 0050:13-16),
    not "the hotel's contact" as the notice and record say. It is copied into `portal_tls_events.detail` and
    `audit_log` payloads, where 0054 does not remove it.
  - Wrong-password counters are swept only, at a 5% chance, when a new failure occurs (0053:59-62), not reliably
    after 2 days. The key is an unsalted SHA-256 of the email, so it is reversible with a list of addresses.
  - The notice says backups are encrypted, but they are only encrypted once a key is listed.
  - The notice says "administrators of your organisation" can see accounts; `tenant_auditor` and `viewer` can too,
    with the audit log's IP addresses. Our staff's IPs and emails are visible to the customer whose record they
    acted on.
- **Fix:**
  - extend 0054 (assignments, certificate requests, `detail->contact_email`, the hourly throttle sweep, an
    HMAC key);
  - correct the notice and record.

---

## B. Fix soon

**Status:**

| Item | Status |
|---|---|
| B1 | **Fixed** (migration 0137): the export also carries the stay's VIP flag, travel agent, rate plan, PMS guest ids, linked stays and PMS events; verification grants and sign-in restrictions; remembered devices and post-stay access; network sightings and staff actions on devices; usage samples; payments, refunds and room charges; the device address and network of sign-in attempts; the address and browser codes were requested from; and audit details. Format `guest-data-export/2` |
| B2 | **Fixed** (migration 0137): erasure follows stay links, covers PMS events that never resolved to the stay, refuses (instead of skipping) while a PMS event is unprocessed or a device is restricted, and erases a stay the PMS left open after its departure; retention anonymises such a stay too. Unprocessed PMS events (D-B2, migration 0139): one still waiting after the stay retention (30 days by default) is given up by the retention run, FAILED with `RETENTION_GIVEN_UP` and its contents blanked, in one move the inbox guard now allows only for that. Erasure gives up the guest's waiting events older than 24 hours and still refuses for newer ones. PMS activity shows them as "Given up" |
| B3 | **Documented:** Annex I, the hotel record template (row 4) and the guest notice template say that the room, reservation and PMS guest numbers of a room charge stay, pseudonymised, with the financial record for the statutory period, including in the compliance archive |
| B4 | **Fixed** (migration 0138): a disabled staff account is anonymised after a set period (Operators → Disabled accounts; default 730 days, 30 to 3650, audited). Email, name, password, sign-on subject and authenticator are removed, as is the email in activity-log entries about the account. An anonymised account cannot be enabled again. Staff IPs in `system_network_audit` and `portal_https_settings_changes` are cleared after the activity-log period. The label in append-only change-history columns is kept as accountability evidence |
| B5 | **Fixed** (Central migration 0055): the platform can download one user's data (Team → Data; reason, step-up, audited). An email can be corrected, and the history then follows it. Removing a user, or the retention run anonymising one, replaces their email with a removed-user label in event actors, settings and audit payloads, and the removal entry no longer repeats it |
| B6 | **Fixed:** the search text and reasons in `guest_data.*` entries are shown only to holders of `guest-data-rights`; other roles see that the read happened, and who did it |
| B7 | **Fixed:** adding, disabling or resetting a staff account, and adding or removing a role, ask for the administrator's own password |
| B8 | **Fixed:** what a guest typed is revealed only after the operator confirms their password (`POST /guest-signin-credentials/{id}`) |
| B9 | **Fixed:** plain SMTP is accepted only for a loopback, private or link-local address, checked on save and at connect |
| B10 | **Fixed** (D-B10): a "Remember this device" box, unticked by default, on the sign-in page and the message-link page, offered over HTTPS only. The remembered-device cookie is set only when the guest ticks it; otherwise the credential scd issued is revoked at once. The guest's choice is kept in a session cookie for the visit (`og_remember`). The notice template lists both cookies. The lawyer confirms (D-F3) |
| B11 | **Fixed:** the guest notice template now lists the VIP flag, travel agent, room type, rate plan, PMS guest id and room sharers, business-account organisation ids, the code requester's address and browser, room charges, each portal cookie with its lifetime, the backup period, and outside pictures or fonts (C1). Whether VIP and travel agent are needed at all is left to the hotel |
| B12 | **Fixed:** email addresses and phone numbers are removed from provider errors before they are logged or stored. An error already stored is replaced at the provider's next send |

**B1. The guest export (Art. 15/20) is incomplete.** `guest_data_export_records` and `public.guest_data_export`
leave out:

- verification grants and sign-in restrictions;
- remembered-device credentials and post-stay profiles;
- network sightings and device actions;
- usage detail;
- payments, refunds and room-charge postings;
- the PMS event payloads;
- the device IP/MAC of sign-in attempts and the IP/user agent of codes;
- the audit details (only the action is exported).

**B2. Erasure scope gaps.** Erasure:

- does not follow `stay_links` (room moves, split stays);
- skips PMS events without a stay or still PENDING (retention skips PENDING ones for ever, 0127:207,299);
- skips active sign-in restrictions silently instead of refusing;
- leaves stays stuck in RESERVED or IN_HOUSE un-anonymised.

**B3. Room and reservation number survive in financial rows.** `pms_postings.g_number`, `posting_attempts.rn` and
`posting_attempts.g_number` are kept 10 years, never anonymised, and copied into the compliance archive
(`cmd/scd/compliance_archive.go:45-54`).

- This is defensible under financial law, but it is pseudonymisation, not anonymisation.
- Annex I ("anonymised 30 days after checkout") and the record must say so.

**B4. Staff accounts on the appliance are never deleted.** "Remove" only disables the account
(`cmd/edged/resources_operators.go:205`). The email and name stay for ever, with no export or erasure path.

- Staff IPs also sit outside the audit log with no retention: `system_network_audit.source_ip` and
  `portal_https_settings_changes.source_ip`.
- **Fix:** a disabled-account period, as Central now has.

**B5. Central users have no export or erase path.** No endpoint exports one person's data. Deleting an account
hard-deletes the row but leaves the email in:

- the history `actor` columns;
- audit payloads (`user.created`, `user.removed`);
- assignments;
- the `updated_by` columns.

The email cannot be corrected, though the notice says it can. **Fix:** a platform-only export and erase-actor
routine, or a documented manual procedure.

**B6. Guest search text is stored in the audit trail and readable by broad roles. ✔ confirmed.** F8, built today,
stores the search text in `guest_data.searched` (`cmd/edged/guest_data_reads.go:254-266`). The audit log is
readable by site_viewer, payments and desk roles. **Fix:** limit `guest_data.*` entries to holders of
`guest-data-rights`, or store the search without the text.

**B7. Changing or resetting a colleague's password asks for no step-up. ✔ confirmed.** Only the authenticator reset
asks (`operator_mfa.go:469`). Creating, disabling and role changes ask for none either (`resources_operators.go`).
Annex II says "staff resets" do. **Fix:** add step-up to these, or correct Annex II.

**B8. Reading guest sign-in credentials needs no step-up.** Desk roles can read 30 days of submitted values
(`resources_signin_attempts.go:85,286-298`), though a comment claims a step-up exists.

**B9. Plain SMTP is accepted for any mail host** (`internal/mail/smtp.go:34,56-79`), including public ones. Guest
emails and reset links can then cross the internet unencrypted. **Fix:** allow `none` only for loopback and private
addresses.

**B10. The remembered-device cookie needs no opt-in** (`og_client`, 30 to 365 days,
`cmd/portald/client_resume.go:31-48`). It is also not Secure on the HTTP fallback. Under ePrivacy Art. 5(3), a
"remember me" cookie is normally exempt from consent only when the user asks for it.

- This is a legal judgement for the lawyer.
- **Fix:** a "Remember this device" box, unticked by default, and HTTPS only.

**B11. The guest privacy-notice template misses things the system records:**

- the VIP flag and travel agent (requested from the PMS: `internal/pms/fias_wire.go:82-83`, ✔ confirmed);
- room type, rate plan, PMS guest id and room sharers;
- the business-account organisation ids;
- the browser user agent;
- DHCP device names;
- room charges;
- the cookies `og_resume`, `og_account` and `sc_commerce`;
- the backup line.

Also consider whether VIP and travel agent are needed at all (minimisation).

**B12. Provider error texts that carry the recipient** (Twilio, SMTP RCPT) are logged, stored in
`notification_providers.last_error` and shown to site_viewer (`internal/notifyloader/loader.go:380-397`).
**Fix:** strip emails and phone numbers from provider errors.

---

## C. Low, and document wording

1. **Fixed:** the notice line was added with B11; the design check already said to upload the file instead, and now also asks for the host to be named in the notice. Uploads are not enforced, as the hotel may choose an outside host. ~~Outside images and fonts:~~ the portal allows ones the hotel chooses (`cmd/portald/portal_design.go:59-60`),
   which then see guests' IPs. Hotel Admin only warns. Add a line to the notice template, and prefer uploads.
2. **Microsoft sign-in still asks for `profile`.** This was a deliberate choice in C9, because some personal
   accounts need it to give their email.
3. **Fixed** (migration 0141; Product-Owner decision: erase only the guest who asked): a stay with more than one guest is found, exported and erased one guest at a time. Erasing one guest removes their name, PMS guest number and PIN, and their name from the stay's PMS events; the other guests, the stay and its shared access stay. Their export holds only their own details. The last guest erased is the whole stay. ~~Over-erasure:~~ erasing one guest anonymised the whole stay, co-guests included.
4. **Fixed** (migration 0140; Product-Owner decision: its own setting): a "Staff notes" data-retention period, default 730 days (30 to 3650, audited), replaces the notes with a fixed text; the records stay. ~~Staff free-text notes are never limited:~~ `pms_case_resolutions.note`, `device_slot_actions.reason`,
   `post_stay_profiles.revoke_reason`.
5. **Fixed** (Product-Owner decision: warn, do not change the password automatically): the database logs errors without the statement or DETAIL line and its docker log is 3 × 10 MB; Hotel Admin's Overview warns while the default password is accepted. The PRE-LIVE appliance itself has not been checked yet. ~~Logs outside retention, and an old default password:~~
   - the Postgres container's Docker logs are outside log retention (`deploy/compose/infra.yml`);
   - older appliances may still have the default database password.
6. **Fixed:** the request log already named routes only (A1); the Caddy access log is rolled daily and kept 14 days; the two commands log the account id. ~~Central server logs:~~
   - the chi request log writes query text;
   - the Caddy log on the Central host has no roll settings;
   - two admin CLI commands log an email.
7. **Fixed (with B11).** ~~Annex III Part B:~~
   - lists "Opera (FIAS)", which is not offered;
   - leaves out the connectivity check (8.8.8.8, github.com; no personal data) and OS update sources;
   - should note Paymob's four regions.
8. **Fixed (with B3).** ~~Hotel record template, wrong statements:~~
   - backups *are* settings;
   - "Backups → Data retention" is really "Guest data kept for";
   - usage detail is missing from Step 3;
   - the staff activity log is anonymised after 730 days, not deleted;
   - the TLS switch is "Encrypt the connection (TLS)", on Protel only.
9. **Fixed (with B11).** The Central notice already names its fixed periods for people. The 30-day assignment-fetch log is fixed too, but it records appliance polls, not people, so the notice does not list it. ~~Wording in the other documents:~~
   - "every time" a guest record is opened should be "repeat views within 10 minutes are recorded once";
   - "a random token per guest" should be "per value";
   - add "a plain SMTP mode exists for internal relays";
   - README: C9 is built;
   - Central: not every period is a setting (the 30-day fetch log, the 5-minute pending sign-in).
10. **Fixed (with B4):** ~~Retention run ordering:~~ `runDataRetention` stops at the first error, so a failing pass skips the rest that
    hour (`cmd/scd/data_retention.go:47-56`).

---

## D. Final check: 2026-10-19, after the A, B and C fixes

A read-only re-check of the `delivery/gdpr` branch (appliance migrations up to 0141, Central up to 0055), in
four parts: logs and guest-facing protections, retention/export/erasure coverage, staff access, and documents
against code. Nothing was deployed or changed by it.

**Every A, B and C fix holds in the current code.** No new gap needs fixing before launch. These were found:

### D, fix soon (B)

| # | Finding | Evidence |
|---|---|---|
| D1 | **Fixed** (migration 0142): a linked stay with other guests is handled like a shared stay asked for by guest; only the guest's own row on it and their name in its PMS events are exported or erased. ~~**Stay links get round the shared-stay rule of C3.**~~ Erasing or exporting a single-guest stay, or the only guest of one, follows room moves and transfers into linked stays, and does not check whether a linked stay has other guests. A shared linked stay is then exported with the other guests' names, and erased whole. The shared-stay filter applies to the account and identity kinds only. | `migrations/0141` scope (link expansion, filter for `account`/`principal` only); checks on the record asked for only (export, erasure `co_guests`) |
| D2 | **Fixed:** the reconciliation lists and the post-stay list and detail withhold the reservation number (and the case key built from it, and the revoke reason) from roles without `pms-stay-identity`. ~~**Reservation numbers reach the view-only role.**~~ `site_viewer` must not see reservation numbers (with the room they are the room sign-in credential, audit H-7), but the PMS reconciliation lists and the post-stay profile list and detail return them without the `canSeeStayIdentity` check the stay pages use. Post-stay is behind a flag. | `cmd/edged/resources_pms_reconciliation.go` (cases, past departures); `resources_phase5_poststay.go`; compare `resources_phase5_transfer.go` |
| D3 | **Fixed:** an entry about a guest (guest data, accounts, sign-ins, DHCP reservations, client devices, sessions, stays) shows its target and details only to a role that can read the screen it came from; a search by others does not match the hidden target. ~~**Guest identifiers in the activity log are shown to roles that cannot see the guest.**~~ B6 hides the details of `guest_data.*` entries only. The entry's target (a device MAC for a device view) stays visible and searchable, and other entries carry a guest username or MAC in their details (`guest_account.*`, DHCP reservations, sign-in restriction releases, network-pause confirmations). `payments_operator` reads the activity log but not guest accounts. | `cmd/edged/resources_site.go` (target shown and searched); `guest_accounts_iamv2.go`, `guest_accounts_manage.go`, `resources_network.go` |
| D4 | **Fixed:** the record gives the 14-day access log and the journal's contents and limit, and says which periods are fixed. ~~**Central's record of processing says its server logs have no limit.**~~ The access log is now kept 14 days (C6). The service journal on Central still has no limit set, and "every period is a setting" is not true for the access log or the 30-day fetch log. | `docs/privacy/RECORD_OF_PROCESSING.md` (f); `deploy/scripts/central-lib.sh`; `control-plane/internal/api/appliance_ops.go` |
| D5 | **Fixed:** Annex II says which technical events log a guest's IP and that the system-log period applies; the post-stay PIN is listed in the guest notice, Annex I and the hotel record; the guest notice lists technical logs and their period; the backup periods name the newest-7 and pinned exceptions. ~~**Annex II says the services never log the guest's address; the portal does.**~~ When a phone checks for the sign-in page, the portal logs the guest's IP (only fixed probe paths, never browsing). The system-log period covers these lines, but the sentence is wrong. Also: the post-stay PIN sign-in is missing from the lists of what is recorded (guest notice, Annex I, hotel record); the guest notice gives no period for system logs; and the backup periods read as absolute, though pinned sets and the newest 7 can be older than 30 days. | `DPA_ANNEX_II_SECURITY_MEASURES.md` (request logs); `cmd/portald/portal_https.go`, `main.go`; `privacy-template.ts`; `deploy/scripts/stayconnect-backup-cleanup.sh` |

### D, low (C)

All ten were taken up on 2026-10-19 (migration 0143 and code, documents). What is left of them is stated.

1. **Fixed:** the appliance's seed-admin and reset-two-step commands log the account id, not the email.
2. **Fixed (documents):** Annex I and the plan say the device name leaves the lease file when the lease ends plus
   up to two hours of clean-up (about four hours with the default lease, longer with longer leases).
3. **Fixed** (0143): room-charge block notes and clear reasons, voucher notes and the reason typed for an erasure
   follow the staff-notes period; the records stay.
4. **Fixed** (0143), with two limits left: a co-guest's export no longer carries the primary guest's VIP flag or
   travel agent; erasing the primary guest takes those out of the PMS events too; one guest's request neither
   blanks nor exports activity-log entries about the shared stay's PMS events. **Left, documented:** the room's
   sign-in attempts cannot be told apart per guest and go at the sign-in-attempt period; a name spelling the PMS
   corrected later is not matched in older events.
5. **Fixed** (0143): an open stay with no departure is erasable, and anonymised by retention, once the PMS's
   latest full room list no longer has it (retention also waits for no PMS message within the stay period).
   Without a published room list nothing changes, so a long-staying guest never loses access.
6. **Fixed:** the guest-data and usage-stays searches send their text in a header (`?q=` still read for an
   older console).
7. **Fixed:** the three access logs drop the `Location` response header.
8. **Fixed (documents):** the DPA, Annex II and the DPIA say a shared stay is handled one guest at a time and
   that the room's shared access is neither exported nor erased for one guest.
9. **Fixed (notice template):** the guest notice names the two browser-storage entries. **Left for the
   lawyer (D-F3):** `og_resume` (30 days, set without a tick, not Secure over the HTTP fallback).
10. **Fixed:** a failed export is recorded; the erasure list reports a read error; the two comments are
    corrected; an erasure refuses while a waiting PMS event it could not give up still names the guest.
    **No change needed:** name matching upper-cases both sides in the database, so accented names match
    whatever the locale.

### Still open, not defects

- The `[[placeholders]]` in the DPA, Annex II, Annex III, the records, the Central notice and the guest notice
  template, for the Product Owner and the lawyer: company and hotel details, contacts, ICO registration,
  hosting provider, breach contact, transfers and safeguards, signatures and dates.
- Nothing above is deployed: appliance migrations 0127 to 0144 and Central 0052 to 0056 wait for authorisation.
  PRE-LIVE's database password has not been checked.

---

## E. Branch review: 2026-10-20

An independent review of `delivery/gdpr` at `3a63b87f` and again at `b0b8506f`, with four Product-Owner
decisions (D-R1 to D-R4, recorded in the plan §4). Every item below was confirmed in the code before it was
fixed, and every fix carries a regression test that was run against a disposable database built exactly as a
factory-clean appliance (or a migrated Central). Nothing is deployed.

### Blockers found and fixed

| Item | Found | Fixed by |
|---|---|---|
| E1 | `npm ci` failed in both consoles: the lockfiles missed `undici-types` | lockfiles regenerated; real `npm ci` passes |
| E2 | A routine Central upgrade (`central-deploy.sh`) never created `operator-mfa.key`, so every console sign-in would be refused while the smoke test passed | the deploy creates the key with the new binary if absent (never replaced), runs `ctrlapi check-mfa-key` before the swap, and its smoke test requires `/readyz` `"signin":"available"` |
| E3 | The backup restore test errored on the nightly backup (a raw `pg_dump -Fc`, not gzip), which is nearly always the newest | the content is identified by its first bytes; the SQL loaders refuse a nightly dump by name |
| E4 | The stay-link walk passed **through** a shared stay, so erasing one guest could erase another guest's later room | 0144: the walk continues only from the guest's own stays |
| E5 | Two guests with the same name (or a surname alone) were treated as one person | 0144, D-R4 |
| E6 | A guest who checked in after the night's room list, with no departure date, could be erased mid-stay | 0144: the room list must be newer than the stay's last message |
| E7 | Retention disabled and renamed valid client accounts made before 0104 (no creation time) | 0144: a recorded floor dates rows of unknown age; a future `valid_from` keeps an account |
| E8 | Retention blanked unlinked review messages but left them in the staff queue | 0144: closed as `EXPIRED_UNDECIDED` (evidence `DATA_RETENTION`) first; a message about an open reservation is left alone |
| E9 | The idle sign-out never fired on console pages that refresh themselves (both consoles) | the consoles mark timer-driven requests `X-OneGate-Background: 1`, which do not count as activity |
| E10 | Log retention (root) followed symlinks planted in service-writable log directories | `O_NOFOLLOW`, checks on the open descriptor, unlink only after re-checking the name |
| E11 | Central: anyone could keep every administrator locked out, with no unlock | Central 0056, D-R1 |
| E12 | Central retention could hide a revocation from an offline appliance, and could reset the licence version an appliance then rejects | Central 0056: a revoked licence stays until its validity and grace end as well as the history period; the newest licence of every appliance is kept |

### Medium and low items fixed

- **Export and erasure (0144):** a shared stay is exported or erased only where the guest is on it; the records in
  scope are locked during an erasure; an identity used at another site is refused (`other_site`); a reason naming
  the guest is refused; the export carries code-device bindings, sign-in redirects and the 0066 copy of names; the
  export is sent only once its audit entry is written; erasure matches the activity log by whole value or word,
  only since the guest's first record (D-R2).
- **Staff reads (F8):** an entry is written directly when the queue is full instead of dropped.
- **Backups:** the nightly encrypts only to the holders of the site the appliance now serves (0144); a restore
  keeps the live key-holder register (restore step "key holders"); an on-demand dump is encrypted even before scd
  knows its site; Central backups accept ssh recipients and refuse to run in the clear when a recipients file is
  configured but unusable.
- **Sign-in:** new recovery codes need a current authenticator code (D-R3, both consoles); the code step rebuilds
  the session from the account as it is now (roles, status, password); self-checks compare the canonical operator
  id; Central code checks and failure counts are one locked transaction; a signed-out Central session cannot be
  revived; recovery-code whitespace is ignored.
- **Retention:** PMS dates are days in the site's time zone; anonymising a PMS payload may only remove from it;
  devices never seen are dated from the floor; a staff account created disabled is dated; staff-address clean-up
  works in batches; rolling back 0140 no longer fails on recorded runs.
- **Logs and portal:** the activity log's actor filter hides guests like the search does; the remember-device
  sentence and the honoured choice follow the HTTPS-only box exactly.
- **Central:** a user's export masks other people's email addresses; an email correction rewrites only that
  user's own entries; the deploy refuses a Caddyfile Caddy rejects before anything changes.

### Not changed, by decision or pending one

- The Room sign-in network pause works as decided (A4); repeated re-arming by one device and carrying staff
  confirmations over are an open product decision (plan §4, Still open).
- Enrolling an authenticator at first sign-in needs only the password; an administrator-issued code is an open
  decision.
- An account anchor still takes in the stay the account belongs to, as D-C4 defines "this guest"; if that account
  belongs to an unnamed room-mate, the named guest's stay data is in scope. Recorded as a residual risk.
- Upgrade note: `POST /auth/mfa/recovery-codes` (Hotel Admin) now needs `code`, and so does scd's route; edged and
  scd deploy together. Migrations must run before the services (0136 and 0144 are read by scd and edged).

---

## Verified as correct

- **No card data reaches the appliance.** The payment callback refuses card-like values. Paymob billing fields are
  "NA". Stripe and Paymob receive no guest identifier.
- **No guest data goes to Central.** Every appliance call was checked: register, hello, licence, assignment, ack,
  offline reconcile, portal certificate. The OS hostname and NIC MACs are the most it sends.
- **Raw PMS frames** are never stored or logged. No documents, addresses or date of birth are taken from the PMS.
- **No browsing record after sign-in:** no DNS query log, no firewall logging, and usage records hold volumes only.
- **Anonymisation** uses fresh random tokens with no mapping kept. Erasure is refused while the stay is live and
  leaves an ER- reference.
- **Export and erasure** are limited to the site admin and IT manager, with password and reason, and audited.
  Backup download is protected by step-up, a single-use ticket and an audit entry.
- **Secrets:**
  - sealed with AES-256-GCM, never returned by the API, with key permissions enforced;
  - throttle keys on the appliance are HMACs;
  - passwords use argon2id with the parameters stated.
- **Outside companies:**
  - SendGrid tracking is off;
  - SMS and WhatsApp carry the code only;
  - Google, Facebook and Apple are asked for the email only.
- **The portal:**
  - `default-src 'self'`, with no third-party scripts, analytics or fonts built in;
  - cookies are HttpOnly and SameSite=Lax;
  - signing out really forgets the device.
- **Central:**
  - tenant isolation is applied on every read handler checked;
  - customers hold no contact fields;
  - unknown sign-in names are not stored;
  - sessions hold no IP;
  - 0054 covers what it claims within its tables.
- **Most document numbers are right:** about 60 of 75 checked claims were correct.
