# GDPR remediation plan

**Status:** DRAFT for Product Owner decision. Nothing here is implemented yet.
**Branch:** `delivery/gdpr`; all GDPR work goes here, never on `master`.
**Baseline:** audit of `master` at `18e34e90` (2026-10-17), read-only.

## 0. Where we stand

**StayConnect is not GDPR compliant today.** On each guest appliance, the hotel is the **controller** and we are the **processor**. Our product therefore has to let a hotel:

- tell guests what is processed;
- keep data no longer than necessary;
- answer access and erasure requests;
- secure the data.

Today it can do none of the first three. Its security is partial.

### Already sound (keep it this way)

- **No guest data reaches Central.** Every appliance-to-Central payload was checked: licensing data only.
- **Strong password storage:** argon2id for operators, guests and Central.
- **Provider secrets are sealed** with AES-256-GCM: social login, SMTP, payment, vouchers and sign-in attempts.
- **Server-side role matrix.** Revealing credentials or codes requires the operator's password again, and the reveal is written to the audit log.
- **The portal loads no trackers or analytics.** The portal's Content-Security-Policy (CSP) allows only the appliance itself (`default-src 'self'`). Payment providers receive no guest identifiers.
- **Social login stores only the provider's subject id, the verified email and, for business accounts, the provider's organisation claims (`hd`, `tid`, `oid`).** Name and picture are thrown away.
- **Some retention already works:** sign-in attempts are kept 30 days, throttles and OTP bindings are pruned, and the accounting purge exists but is opt-in.

## 1. How the work is classified

The classification follows `CLAUDE.md` §0, by what each item touches:

- **FAST:** no schema change, no deletion of data, no guest-auth, PMS, security or financial core. It can be built on this branch directly.
- **CONTROLLED:** touches one of the §0 triggers. It needs the listed Product Owner decision first, then the full verification for that action.

Priority:

- **P0:** exposure today.
- **P1:** needed for a defensible position.
- **P2:** hygiene.

## 2. FAST work: buildable now on `delivery/gdpr`

| # | P | Item | Gap it closes | PO decision needed |
|---|---|---|---|---|
| F1 | P0 | **Built on `delivery/gdpr`, not yet deployed.** **Privacy notice on the guest portal.** Add a `privacy_url` field and an optional inline privacy text to the portal design (stored in the same design document as `terms_url`, so no schema change). Translate the link text. Show it on every sign-in method, before the guest acts. | Art. 13: guests receive no information today | **D-F1:** Do we ship default notice text that the hotel completes (we recommend a template with controller placeholders)? Or a link only? |
| F2 | P0 | **Drafted; 24 hours decided, contact TBD (D-F2).** **Breach-response runbook** `docs/runbooks/PERSONAL_DATA_BREACH.md`: detection sources, who decides, the hotel's 72-hour deadline as controller, our duty to tell the hotel "without undue delay" as processor, evidence handling. | Art. 33/34: nothing exists | **D-F2:** Who in our organisation is the breach contact? Within how many hours do we promise to tell the hotel? |
| F3 | P0 | **Drafted on `delivery/gdpr` (see [README.md](README.md)): our DPA, annexes and record, and the hotel's record template and DPIA starter (2026-10-19). Waiting for the legal review (D-F3).** **Compliance pack** under `docs/privacy/`: data inventory / Art. 30 record template, sub-processor list (SendGrid, Twilio, Meta, Google, Microsoft, Apple, Stripe, Paymob), notes on data sent outside the EU, DPIA starter, DPA template for hotels. | Art. 28, 30, 35, Chapter V: nothing exists | **D-F3:** Legal review owner. These are engineering drafts and need a lawyer's sign-off before they reach a customer. |
| F4 | P1 | **Turn off SendGrid click/open tracking** on verification and reset mail (`tracking_settings`). | Tracking that guests are not told about; rewritten links | none |
| F5 | P1 | **Built on `delivery/gdpr` (migration 0134); D-F5 decided: 14 days, a hotel setting.** **Bound the logs:** a logrotate rule for `/var/log/stayconnect/*` and Caddy/Kea logs, plus a journald size and age limit. Stop the non-production OTP stubs from writing full recipients and codes. | Art. 5(1)(e): logs grow forever | **D-F5:** Log retention period (recommend 14 days) |
| F6 | P1 | **Moved to C1.** Making the 30-day sign-in-attempt retention a hotel setting needs a migration: the settings policy requires a site-keyed table, a database CHECK and a change log written by a definer function. It ships with the C1 retention settings. | Breaks `OPERATIONAL_SETTINGS_POLICY` §0C | none (keep the 30-day default) |
| F7 | P2 | **Built on `delivery/gdpr`, not yet deployed.** **Explain the remembered-device cookie on the portal** (one sentence, translated). Hotel Admin warns when a logo, background or font comes from an outside host. | Art. 13; outside hosts the guest is not told about | none |
| F8 | P2 | **Built on `delivery/gdpr` (no migration).** **Audit routine reads of guest data:** staff viewing guest-account details, a stay's identity, usage by MAC. | Accountability: who looked at a guest | **D-F8:** Audit every view, or only opening one guest's detail (recommended; avoids flooding the log)? |
| F9 | P2 | **Correct stale documents:** `DATA_OWNERSHIP.md` (the dropped `guests` table; the claim that "retention is enforced locally"; the portal-TLS endpoints) and `CONTROL_PLANE_MIGRATION.md` ("historical guest rows"). | Accurate records | none |

## 3. CONTROLLED work: needs a decision first

| # | P | Item | Trigger(s) | PO decision needed |
|---|---|---|---|---|
| C1 | P0 | **Built on `delivery/gdpr` (migration 0127), not yet deployed.** **Retention engine for guest data.** One purge job driven by hotel settings, per category: PMS stays, stay guests and stay events after checkout; sessions; devices (MAC/IP); guest identities (email, phone, social subject); `auth_otps`; `audit_log`; expired single-use rows (OAuth states, email tokens, auth contexts, verification grants, sign-in restrictions). Needs migrations, and the append-only trigger on `stay_events` must allow deletion by the retention job only. | schema, destructive data, PMS mirror | **D-C1 (retention defaults):** recommended starting points:<br>• PMS stay, guest and event data: **30 days after checkout**<br>• sessions, devices, usage detail: **12 months** (check the hotel's national rules on keeping connection data)<br>• guest identities with no stay or account activity: **12 months**<br>• OTP, OAuth and token rows: **7 days after expiry**<br>• audit log: **24 months**<br>• financial records: **legal period, set per country (default 10 years)**, kept and not purged.<br>Also: purge for real, or anonymise and keep the totals? |
| C2 | P0 | **Built on `delivery/gdpr` (migration 0131).** **Change the accounting-detail default** from *unlimited* to the D-C1 value for new sites. Decide what happens on existing sites. | destructive / historical data | **D-C2:** Apply the new default to existing sites (purges history on PRE-LIVE), or only to new installs? |
| C3 | P0 | **Built on `delivery/gdpr` (migration 0128), not yet deployed.** **Erase one guest.** A single action in Hotel Admin that finds everything held about one guest (account, principal and identities, stays, sessions, devices, sign-in attempts, OTPs, audit mentions) and erases or anonymises it. Fixes "Remove" keeping the email inside the renamed username. | destructive data, guest auth core, product semantics | **D-C3 (erasure semantics):**<br>(a) **Pseudonymise** rows that must survive (financial postings, entitlement totals, and audit rows as events) by replacing identifiers with a one-way token. **Hard-delete** everything else. *Recommended.*<br>(b) Hard-delete everything: breaks the financial and audit chains, not recommended.<br>Also decide: what happens to `stay_events` raw payloads (recommend: blank the guest fields, keep the event); whether to erase guests the PMS still shows as checked in (recommend: refuse, and tell staff the PMS will copy them back). |
| C4 | P1 | **Built on `delivery/gdpr` (migration 0128), not yet deployed.** **Export one guest's data (access and portability).** The same lookup as C3, producing a machine-readable JSON/CSV bundle. Requires the operator's password again, is audited, and is limited to one role. | product semantics (guest-data surface) | **D-C4:** decided, see §4 (site_admin and hotel_it_manager; staff hand the export over). |
| C5 | P0 | **Built on `delivery/gdpr` (migration 0129), not yet deployed.** **Encrypt backups and keep the keys apart.** Encrypt the nightly dump and the `/etc` archive to a public key, so the appliance can write backups but cannot read them back. Stop the seal keys travelling in the same unencrypted archive. Add an age limit to the on-demand dumps as well as the count limit. | security, keys and trust | **D-C5:** decided, see §4 (hotel-held keys, several holders, restore-test reminder, no escrow). |
| C6 | P1 | **Built on `delivery/gdpr` (migration 0130).** **Record consent / notice acknowledgement.** Store the version of the notice and terms each guest saw, with the time. | schema, guest auth flow | **D-C6:** Lawful basis for Wi-Fi access. We recommend **contract / legitimate interest, not consent**, so we *record that the notice was shown* rather than block access on a checkbox. Consent is needed only if marketing is ever added. |
| C7 | P1 | **Built on `delivery/gdpr` (migration 0133).** **Store less from the PMS.** Stop mirroring date of birth, VIP, travel agent and rate plan unless a feature needs them. Clear the existing values. | PMS protocol, schema, historical data | **D-C7:** Does any feature need date of birth (e.g. as a sign-in factor)? If none, drop it. |
| C8 | P1 | **Built on `delivery/gdpr` (no migration), off by default.** **Encrypt the Protel FIAS link** (`UseTLS` is read but never used). | PMS configuration and traffic | **D-C8:** Does the hotel's Protel interface support TLS? Is turning it on at this hotel authorised? |
| C9 | P1 | **Built on `delivery/gdpr` (no migration).** **Request fewer profile fields from social providers:** drop `profile`, `public_profile`, `name` and the Facebook `picture` field. Limit the free-text scope field in Hotel Admin to an allow-list. | guest authentication | **D-C9:** Reject or warn when a hotel adds extra scopes? |
| C10 | P1 | **Built on `delivery/gdpr`: Hotel Admin (migration 0132) and Central (Central migration 0052).** **Harden operator access:** MFA for Hotel Admin and Central, an absolute session limit plus an idle timeout, `EDGED_COOKIE_SECURE` on by default. | security | **D-C10:** Which MFA method (TOTP recommended)? Mandatory for admin roles only, or for everyone? Idle timeout (recommend 30 min) and absolute limit (recommend 12 h)? |
| C11 | P2 | **Encryption at rest:** disk encryption (LUKS) for new installs; TLS or Unix sockets for database connections. | appliance deployment, security | **D-C11:** Accept for new installs only (existing appliances need a reinstall)? How is the key unlocked at boot (TPM vs passphrase)? |
| C12 | P0 (ops) | **Rotate exposed credentials:** the Gmail app password that `SECURITY_HARDENING.md` reports was once committed and never rotated; the default Postgres password fallback. | security | **D-C12:** None for the rotation itself, which the account owner must do. Confirm removing the personal Gmail address from the repository. |

## 4. Product Owner decisions taken (2026-10-17)

### D-C1, retention defaults: **Balanced**

Each value ships as a hotel setting with this default, following `OPERATIONAL_SETTINGS_POLICY` §0C:

| Data | Default |
|---|---|
| PMS stay, stay-guest and stay-event data | 30 days after checkout |
| Sessions, devices (MAC/IP), usage detail | 12 months |
| Guest identities (email, phone, social subject) with no stay or account activity | 12 months |
| OTP, OAuth-state, email-token, auth-context and verification-grant rows | 7 days after expiry |
| Audit log | 24 months |
| Financial records (purchases, payments, postings, settlements) | 10 years, never purged automatically |

### D-C3, erasure semantics: **Anonymise only**

"Erase this guest" deletes no rows. Instead it replaces every identifier of that guest, everywhere, with a token. Conditions for the result to count as *anonymous* rather than *pseudonymous*:

- The token is **random per erasure** (not an HMAC of the identifier), and nothing keeps a mapping back to the person.
- Every identifier is replaced: username/email, phone, social subject, display name, names and date of birth in stay guests, MAC and IP in sessions, devices, sign-in attempts and grants, and room number where it is tied to the guest.
- Free-text fields are **blanked**, not tokenised: notes, sign-in attempt names, `stay_events` payload guest fields, and audit-log payload values naming the guest.
- Erasure fixes "Remove" too: the renamed username no longer contains the original.
- Erasure is refused while the PMS still shows the guest in-house; staff are told the PMS would copy the guest back.
- The erasure itself is written to the audit log by token, not by name.

### D-C5, backup encryption: **Hotel-held keys, several holders, no escrow** (revised the same day; replaces "public key + escrow")

**Why no escrow.** GDPR does not require the vendor to hold a backup key. Art. 32(1)(c) requires that the **hotel**, as controller, can restore guest data in good time. An escrow key would let us read every escrowed hotel's guest data, which adds processor liability and conflicts with Central handling licensing only (§0E). We therefore hold no key, and protection against key loss is provided inside the hotel.

- **Encryption.** Backups (the database dump and the `/etc/stayconnect` archive, including the seal keys) are encrypted with age to the hotel's public keys. The appliance can write backups but can never read them back. The seal keys travel only inside the encrypted archive.
- **Keys are per appliance and generated at setup.** In Hotel Admin the appliance generates the key pair and hands the private key to the hotel once, as a file and as a printable recovery sheet. The appliance keeps no copy. Encrypted backups switch on only after the hotel types back a short check code, proving it saved the key.
- **Several key holders.** Each backup is encrypted to every registered key holder (for example the IT manager, the General Manager, or a printed copy in the hotel safe). Any one holder can restore alone. The hotel can add, replace or remove holders at any time. New backups use the current holders; older backups still open with the keys they were made with.
- **Restore-test reminder.** Hotel Admin asks the hotel to prove periodically that it still holds a working key, by decrypting a small test file. A failed or overdue test shows a warning in Hotel Admin. The interval is a hotel setting (§0C): **default 6 months**, with the usual unit, explanation, permission check, validation and change audit.
- **Downloads stay encrypted.** A backup downloaded from Hotel Admin is the encrypted file; the hotel decrypts it with its own key.
- **If every key is lost.** Live data is unaffected and new backups work as soon as a new key is set; older backups cannot be recovered. The setup screen says this plainly, and the hotel acknowledges it.

### D-C6, lawful basis: **Notice, no checkbox**

- Guests get Wi-Fi under the stay contract / legitimate interest. No checkbox blocks access.
- The portal shows the privacy notice (F1), and C6 records which notice version was shown to whom and when.
- Consent is introduced only if a marketing feature is ever added.

### D-C1a, what happens when retention expires: **Anonymise, the same way erasure does**

- The C1 retention jobs and the C3 erasure action share one anonymisation routine. When a category's retention period ends, its rows are anonymised under the D-C3 rules (a fresh random token per value, free text blanked); no rows are deleted.
- Reports and totals (usage, entitlements, financials) keep working on the anonymised rows.
- The `stay_events` append-only trigger is relaxed only for that routine blanking the guest fields; deletion stays refused.

### D-C4, guest export and erasure: **Site admin and IT manager; staff hand the export over**

- Only `site_admin` and `hotel_it_manager` may export or erase a guest. Other staff roles get neither action.
- Each export and each erasure asks for the operator's password again and a reason, and is written to the audit log by internal record id (an export) or erasure token (an erasure), never by name, email, phone or room.
- The export is a machine-readable JSON file downloaded from Hotel Admin. Staff check the guest's identity and hand the file over. The guest portal gets no self-service download, so a weak portal sign-in cannot leak another guest's data.

### How C3 and C4 were built (migration 0128, on `delivery/gdpr`)

- **Hotel Admin → System → Guest data requests**, key `guest-data-rights` (site_admin and IT manager only, D-C4). Staff search by email, phone, name, client account, room, reservation or MAC address, then **Export** (a JSON file) or **Erase**, each with the password and a reason.
- **What counts as "this guest"**: the record staff picked, plus the records one step away through the internet access it held (the stay a client account belongs to, the portal identities that signed in on a stay), that access, and the devices it used. A device another guest's access also used is left alone and counted. A stay is erased whole, because it is one reservation.
- **Erasure** reuses the C1 anonymisation routine, and also covers the guest's sign-in attempts, verification records, sign-in codes, OAuth states and audit-log mentions. It is refused while the PMS shows the stay as **reserved or in-house**, while the guest's **access has not ended**, or while a **device is online**; staff see which. Each erasure leaves a reference (`ER-…`) to give the guest, recorded without personal data.
- **"Remove"** on a client account is unchanged. A removed account (whose username still carries the address) can now be found and erased here, and retention anonymises it in any case.

### How C5 was built (migration 0129, on `delivery/gdpr`)

- **Hotel Admin → System → Backups → Backup encryption.** A site administrator adds a key holder; the appliance generates the age key pair, hands the private key over once (key file and printable recovery sheet) and keeps only the public key. The holder counts after the check code from the file or sheet is typed back, within an hour. The last active holder cannot be removed. Holders and their changes are recorded append-only.
- **What is encrypted**: the nightly dump and `/etc` archive (seal keys included) and the on-demand and restore-safety dumps, as they are written, to every active holder. Until the first holder is confirmed, backups stay in the clear and the screen says so. If holders exist but cannot be read, the backup fails rather than fall back to plaintext.
- **Opening a backup on the appliance** (Verify, Restore, restore test) asks for a holder's key, used in memory for that action only. Downloads stay encrypted. An engineer opens a nightly backup with `scd backup-crypt decrypt` (or any age tool).
- **Restore-test reminder**: backup setting `RESTORE_TEST_MONTHS`, default 6; overdue shows a warning. On-demand dumps also get an age limit, `DB_BACKUP_DAYS` (default 30), beside the count limit.
- **New dependency**: `filippo.io/age` v1.2.1, the reference Go implementation of age (the format D-C5 named).

### D-F1, privacy notice: **A template the hotel completes**

- We ship a starting notice; the hotel, as controller, completes and publishes it. Every hotel-specific fact (who the controller is, retention periods, which outside services are on) is a `[[placeholder]]`.

### How F1 and F7 were built (no migration, on `delivery/gdpr`)

- **Design fields** `privacy_text` (plain text, up to 8,000 characters, never markup) and `privacy_url` (same rule as `terms_url`), in the same design document; `internal/portaldesign` checks and serves both.
- **Hotel Admin → Portal settings → Content → Privacy notice.** "Start from the template" fills in the shipped notice (`portal-branding/privacy-template.ts`). It says only what this system records. While any `[[placeholder]]` is left, the checks warn, but the notice can still be saved.
- **On the portal**, "How we use your data" sits on the sign-in card under every sign-in method, including the welcome-back screen, and opens the text in place: an outside page would not load before the guest is online. The optional link is shown beside it. The three new phrases are translated into all six shipped languages.
- **F7.** One sentence explains the remembered-device cookie wherever a sign-in can set it (code or identity provider) and on the welcome-back screen. The design checks now **warn** (they do not refuse) for every outside host the page would load from: a logo, background or hero image on https, `url()` in custom CSS (including `@font-face`), and `src` in custom HTML. Hotel Admin lists these under "Worth knowing before you save".

### How C10 was built for Hotel Admin (migration 0132, on `delivery/gdpr`)

- **Signing in is two steps.** The password no longer opens a session: it opens a five-minute "password accepted" state, and the 6-digit code from an authenticator app (TOTP, RFC 6238) opens the session. The first time, the app is set up there: a QR code drawn in the browser (no outside service), the key to type, a first code, then ten single-use recovery codes shown once.
- **The secret is sealed by scd** with a new appliance key, `operator_totp_dek.key` (keybootstrap creates it), bound to the operator. edged, which serves the console, cannot open it. The routes are edged-only on scd's socket. A code is never accepted twice; recovery codes are stored as an HMAC under the same key.
- **Wrong codes** are throttled per operator with the sign-in protection's account limits, and per address, and audited (`operator.mfa_failed`, `operator.mfa_locked`).
- **Password confirmations stay a password.** NIST SP 800-63B allows one factor to re-confirm inside a session opened with two, so no existing screen changed.
- **Sessions end** after 30 minutes idle and 12 hours at most, a setting under System → Operators → Session limits. The console's background check every 30 seconds does not count as activity, so an open tab does sign out.
- **Lost phone:** a recovery code; or a site administrator resets a colleague (password, reason, audited, their sessions end; never one's own); or, for the last administrator, `edged reset-mfa --email E --reason R` on the appliance.
- **Your account → Two-step sign-in** shows the state and makes a new set of recovery codes.
- **The session cookie is Secure by default** (`EDGED_COOKIE_SECURE=false` is needed to switch it off).
- **New dependency:** `qrcode` 1.5.4 (MIT) in Hotel Admin, pinned.

### How C10 was built for Central (Central migration 0052, on `delivery/gdpr`)

- **The same two steps** as Hotel Admin, for every Central account: platform operators and customer users. The pending state between the password and the code is a row in Postgres (a hash of the cookie token, five minutes).
- **The secret is sealed by ctrlapi** with AES-256-GCM under a new key file, `/etc/stayconnect/operator-mfa.key` (`CTRLAPI_OPERATOR_MFA_KEY`). `central-install.sh` creates it, and the export bundle carries it, so moving Central keeps everyone's authenticator. Without the key, every sign-in is refused.
- **Wrong codes** lock the authenticator for 15 minutes after five, audited (`auth.mfa_failed`, `auth.mfa_locked`). A code is never accepted twice.
- **Sessions end** after 30 minutes idle (`operator_idle_minutes`, 5-240) and 12 hours at most (`operator_session_max_hours`; the old default 24 moved to 12 where nobody had changed it). Both are under System > Team > Sign-in limits. The console's whoami poll does not count as activity.
- **Lost phone:** a recovery code; an administrator's "Reset two-step" on the Team or a customer's Users (reason, audited, sessions ended, never one's own); or `ctrlapi reset-mfa --email E --reason R` for the last administrator.
- **The Two-step sign-in page** in the sidebar shows the state and makes new recovery codes (after step-up).
- **The test tools sign in with both steps.** `cmd/acceptance`, `cmd/pkitest` and the two regression scripts (`scripts/lifecycle-regression.sh`, `scripts/replacement-factoryreset-regression.sh`, through `cmd/regtest -signin`) use `internal/opsignin`: the first sign-in sets the operator's authenticator up and keeps its secret in a 0600 keyring on the machine running the tool (`OPERATOR_TOTP_KEYRING`, default in the user's config directory); later sign-ins compute the code. An operator whose authenticator was set up elsewhere is refused with the `ctrlapi reset-mfa` command that hands it back. The older phase scripts (`scripts/phase*-test.sh`) still sign in with a password alone and are not current coverage.

### D-F5, log retention: **14 days, a hotel setting** (Product Owner, 2026-10-19)

- "14 days, but if it can be configurable by law make it that way": the default is 14 days, and a site administrator may set 1 to 730 days, so a hotel whose national law requires connection logs to be kept longer can follow it.

### The audit's before-launch items (GDPR_AUDIT_2026-10-19.md, section A), built 2026-10-19

The Product Owner chose:
- a warning, not refusal, for the plain-HTTP portal;
- client isolation on for new networks;
- a built-in short notice when the hotel has none.

**A1 and A2: request logs.**
- edged, portald, scd and Central log the matched route pattern only (`data-plane/internal/reqlog`,
  `control-plane/internal/http/reqlog.go`): no path, query or client address.
- The Caddy access logs (portal, Hotel Admin, Central) drop request headers, the query and MAC addresses
  through a log filter.

**A3: guest networks.**
- A new network with a captive portal isolates its clients unless told otherwise. A replacement keeps its
  predecessor's value.
- Sign-in methods warns while Portal HTTPS is not active and a typed sign-in method is on.

**A5: the privacy notice.**
- The portal shows a short built-in notice (`privacy.default`, in every portal language) whenever the hotel
  has written none.
- Saving the portal design warns while there is none.

**A6: the 0066 copy (migration 0135).**
- `anonymise_stay` also clears 0066's copy of the names and room, and the copy is cleared now for stays
  already anonymised.
- Dropping the table is still open.

**A7: DHCP device names.** Documented in Annex I, the hotel record and the notice template. They live in Kea's
lease file while a device is connected and leave it when the lease ends plus up to two hours of clean-up
(about four hours with the default two-hour lease).

**A8: Central (0054 extended before its first deployment).**
- Appliance moves and certificate-request addresses are now covered.
- The certificate contact is removed from portal-certificate history.
- Idle wrong-password counters are swept hourly, and the counter key is an HMAC.
- The Central notice and record are corrected: the Central-wide certificate contact, conditional backup
  encryption, and who in a customer can see accounts.

**A4 (D-A4, Product Owner 2026-10-19: "A, but also allow staff to confirm genuine customers"), migration 0136.**

- **The pause.** Wrong room sign-ins are also counted per guest network, whichever devices they come from. At
  `network_max_failures` within `network_window_seconds`, Room sign-in pauses on that network for
  `network_pause_seconds`. The defaults are 30, 600 s and 900 s: settings under Sign-in methods, with a reason
  and the password, kept in their own append-only history.
- **What it does not change.** Other methods and networks are unaffected. Refusals during a pause are not
  counted, and the burst a pause answered does not start the next one.
- **What staff can do.** Staff see the pause on Sign-in methods and Guest sign-in attempts, and can:
  - **confirm a genuine guest's device**, picked from the devices that tried, by the room they typed. The device
    may try again, and its own device restriction ends.
  - **end the pause.**

  Both need a reason and are audited. Neither creates access.
- **Privacy.** A confirmation holds the device MAC and is removed a day after its pause ends (scd's minute sweep).
  Ended pauses are removed after 30 days.
- **How it is built.** The per-device rule (0068) is unchanged. The new state lives in its own tables and definer
  functions, with grants in `deploy/gatep/svc-signin-network-pause-grants.sql`.

### D-SSH, appliance SSH: **to decide before launch** (Product Owner, 2026-10-19)

- **The fact:** the firewall accepts SSH on the uplink interface (`deploy/nftables/stayconnect.nft`, `ens160` port 22), and we ship no SSH settings, so the operating system's defaults apply: password login, no lockout. Guests cannot reach it. Whether the internet can depends on the hotel's own router.
- **Decided:** SSH stays available, so the hotel's IT can manage the appliance. GDPR does not require turning it off; it requires security appropriate to the risk (Art. 32).
- **Open until before launch:** how the login is protected. The options discussed:
  1. **Passwords, hardened** (recommended, keeps it easy for admins): lockout after repeated failures (a setting), no direct root login, every login recorded, strong passwords required at set-up; keys optional.
  2. **Key-only:** strongest, but harder for admins.
  3. **As today:** hard to defend after a breach.
- In every option the documents tell the hotel to reach SSH through its VPN, not directly from the internet.
- Until decided, Annex II Part C2 and the hotel DPIA (R2) say SSH is open by default and ask the hotel to protect it.

### D-CR, Central retention (Product Owner, 2026-10-19)

- The audit log: **24 months**.
- Security alerts and appliance history: "as long as possible while adhering to GDPR", settled as **6 years** for the events (the UK limitation period for a claim under the licence contract), with the **people removed after 24 months** (the security purpose, as for the audit log).
- Disabled accounts: "as long as we can under GDPR", settled as **24 months** after disabling (the identity is needed only to read the audit log, which is kept 24 months).
- Retired appliances and licence history: **6 years**.
- Central's hosting provider: **not decided yet**; a placeholder in the record and Annex III.

### How Central retention was built (Central migration 0054, on `delivery/gdpr`)

- **Four settings**, `central_retention_settings`, under System > Team > Data retention, with a reason and step-up, audited from/to: audit log months (24, 6-120), people in the history months (24, 6-120), history years (6, 1-10), disabled account months (24, 1-120).
- **`central_retention_apply()`, hourly from ctrlapi's housekeeping:** deletes audit entries, closed alerts, lifecycle, certificate and portal-certificate events past their period; removes the operator email, source IP and acknowledger from what is left after the shorter period; deletes ended licences (revoked, superseded or expired); clears a retired appliance's MAC addresses, host name and last public IP; anonymises disabled accounts (email replaced, name, password, authenticator and roles removed). Open alerts and live licences are never removed. Each run that changes anything is audited as `retention.applied`.
- **Retired appliance records are cleared, not deleted:** deleting one revokes certificates and releases DNS names, which a background job must not do. `retired_appliance_identities` holds keys and serials only and stays, so a retired key stays refused.
- `operators.disabled_at` is stamped by a trigger; an anonymised account cannot be enabled again. `portal_tls_events` stays append-only except to the retention function.

### How F5 was built (migration 0134, on `delivery/gdpr`)

- **One more data-retention period**, `log_days`, beside the six of 0127: Hotel Admin → Backups → Data retention, "System logs", 1-730 days, default 14, with the password and a reason, logged like the others.
- **scd applies it hourly** (`cmd/scd/log_retention.go`): `journalctl --vacuum-time`; each file in `/var/log/stayconnect`, `/var/log/caddy` and `/var/log/kea` is cut once a day (copied to `<name>.<YYYYMMDD>` and emptied in place, safe for the appending writers); cuts, Kea's numbered and Caddy's rolled files older than the period are deleted; a live log nobody has written for longer than the period is emptied.
- **journald archives daily** (`deploy/journald/stayconnect-retention.conf`, installed by `install-service-units.sh`), so the vacuum can remove each day.
- The code-sender logs already write only a masked recipient in production builds; only development builds write codes.

### How C7 was built (migration 0133, on `delivery/gdpr`)

- **Checked:** Protel FIAS is asked only for RN, G#, GN, GF, GA, GD, A0 and A1; an unrequested field is used only inside the event fingerprint and never stored; the REST connectors map no birth date.
- `iam_v2.stay_guests.date_of_birth` is dropped; retention and the guest export no longer name it. The down migration adds it back, empty.

### How C8 was built (no migration, on `delivery/gdpr`)

- A Protel connection gains **"Encrypt the connection (TLS)"**, off by default, and an optional **PMS certificate fingerprint (SHA-256)** that pins a self-signed certificate. Without a pin, the certificate must chain to a system authority and match the address.
- pmsd wraps the socket in TLS 1.2 or later and **never falls back** to plain TCP: a failed handshake leaves the link down.
- Stored as `config.tls` only when on, so existing connections read exactly as before. Turning it on for a hotel stays a separate decision (D-C8).

### Also built on 2026-10-19 (gaps from the F3 review)

- **Code device bindings** older than a day are now removed by the minute sweep, not only when the next code is sent.
- **Central locks an account** after 5 wrong passwords (at sign-in or at the confirmation) for 15 minutes, both settings (Central migration 0053); unknown names lock alike, so the lock reveals no accounts; the authenticator-code lock uses the same settings.
- **Central's database backups are encrypted** with age to the public keys in `/etc/stayconnect/backup-recipients.txt`; Central holds no private key. The installer adds `age` and warns while no key is listed.

### How C9 was built (no migration, on `delivery/gdpr`)

- **Asked for by default** (when the hotel leaves Scopes empty): Google `openid email` (was `openid email profile`); Facebook `email` (was `email public_profile`), and the profile fetch asks only for `id,email` (was also name and picture); Apple `email` (was `name email`).
- **Microsoft keeps `openid email profile`.** A personal Microsoft account may give its email only as `preferred_username`, which needs `profile`; dropping it would refuse those guests.
- **Hotel Admin → Social providers** warns, and still saves, when Scopes asks for more than the guest's name and email (`hotel-admin/lib/social-scopes.ts`), naming each extra scope and that the hotel needs a reason and must say so in its notice.
- **Existing providers** keep whatever Scopes they saved. Only an empty field follows the new defaults.
- The privacy-notice template now says sign-in keeps only the email address and the account id.

### How F8 was built (no migration, on `delivery/gdpr`)

- **What is recorded**, in the existing activity log (`audit_log`), by edged (`cmd/edged/guest_data_reads.go`):
  - `guest_data.viewed`: opening one client account, device, session, stay, sign-in attempt, post-stay profile, or one stay's, device's or session's usage. The target is the record's id.
  - `guest_data.searched`: every search of a guest list (client accounts, devices, sessions, stays, PMS activity, sign-in attempts, usage, guest data requests, DHCP leases), with the text searched for.
  - Each entry has the staff member, the time of the read, the address and the browser. Only successful reads count. A list browsed without a search is not recorded. Revealing what a guest typed at sign-in was already audited and is unchanged.
- **Pages never wait.** The request puts the entry on an in-memory queue (4,096 places); one background writer saves it. A full queue drops the entry with a warning. Entries still waiting when edged stops are written first.
- **The log stays readable.** A search typed letter by letter is one entry: it waits 3 seconds, and a longer or shorter search by the same person in the same list replaces it. The same read by the same person is recorded once per 10 minutes, so self-refreshing lists add nothing.
- **Erasure and retention cover the entries:** erasing a guest anonymises the entries that name the guest's records, address or username; retention anonymises all entries after the audit-log period (730 days by default). A search by name only is not linked to the guest at erasure, and is anonymised by retention.
- **Hotel Admin → Activity** lists them under a new **Client data** filter, beside export and erasure.
- **A route list** (`guestDataReads`) says which pages count. A test fails if a listed route no longer exists.

### How C2 was built (migration 0131, on `delivery/gdpr`)

- **Still the hotel's setting.** Hotel Admin's "Usage detail kept for" keeps working as before: a site admin chooses 30 to 3650 days or Unlimited, with the password and a reason, and every change is logged.
- **Only the default moved.** A site that never chose keeps 365 days. A site whose saved choice was Unlimited moved to 365 days, logged as a change by "migration 0131". A site that chose a number of days keeps it.
- **Effect:** the hourly purge removes usage detail older than a year under the existing rules (ended sessions, finished packages, totals kept, every run recorded).

### How C6 was built (migration 0130, on `delivery/gdpr`)

- **What is recorded:** for every sign-in, by any method, the privacy notice in force when it completed: the notice text, the notice link and the terms link.
- **Versions:** each distinct notice is stored once, by its SHA-256 fingerprint, in `public.privacy_notice_versions` (append-only). The portal design keeps only its last 20 saves, so it could not serve as the record. A design with no notice is a version too, so "nothing was shown" is recorded rather than implied.
- **The stamp:** a trigger on `iam_v2.auth_contexts`, which every sign-in method writes, sets `privacy_notice_version_id` and `privacy_notice_shown_at` (the sign-in time, which that table did not record). The writer cannot supply it and nobody can change it afterwards. If the stamp cannot be made, the sign-in still goes ahead and the database logs a warning: a guest is never refused over the record.
- **What the guest sees:** the guest export (C4) gains `privacy_notices_shown` (one entry per sign-in) and `privacy_notices` (each notice's text once).
- **Retention:** the notice text is the hotel's, not personal data, so retention and erasure leave it alone. The sign-in record it hangs off holds no identifier of its own.
- **Limit:** the stamp is the notice in force at sign-in. A notice edited while a guest had the page open is the only case where the two differ.

### D-F2, breach notification: **24 hours** (contact still to name)

- We tell the hotel without undue delay and within **24 hours** of becoming aware, as a first notice that may be incomplete. GDPR sets no number of hours for a processor (Art. 33(2), "without undue delay"); 24 hours leaves the hotel most of its 72. The breach contact is still to be named.

### F2, breach runbook: drafted, waiting for the D-F2 contact

- `docs/runbooks/PERSONAL_DATA_BREACH.md`. The breach contact is marked **TBD** until it is named; it is not given to a customer before then, nor before the D-F3 legal review.
- **Scope, from the Product Owner's answers (2026-10-17), checked in the code.** Guest data stays on the hotel's appliance and we have no standing access: the Central link is licensing only, installation creates no vendor login, tunnel or key, break-glass needs root the hotel gives us, and our installers keep no root or SSH login after an installation (Product Owner's statement; not enforced by code). The messaging, identity-provider and payment accounts are the hotel's own. We are therefore a processor only (A) during a support session the hotel opens and (B) for files it sends us, such as logs; and (C) as the supplier, we warn hotels of a security flaw in our software. The runbook covers those three cases, with one promised notification time.
- **For F3:** the sub-processor list shrinks accordingly. SendGrid, Twilio, Meta, Google, Microsoft, Apple, Stripe and Paymob are the hotel's own processors, not ours.

### Product Owner answers on C2, C7 to C12 and F8 (2026-10-17)

The product is still in development: there is no paying customer and no live hotel. Work stays on `delivery/gdpr`; nothing is merged to `master` or deployed to PRE-LIVE as part of this plan.

- **D-C2: the new default applies everywhere.** With no live customer, there is no history to protect, so the 12-month default for accounting detail replaces "unlimited" on every site, not only new installs.
- **D-C7: nothing to remove but the empty date-of-birth column.** Checked in the code: `stay_guests.date_of_birth` is never written or read outside retention and erasure, so it is always empty. VIP, travel agent and rate plan are used by the package offers, so they stay. C7 shrinks to: drop the unused column when a migration next touches the stay domain, and confirm that the PMS event payloads carry no date of birth. Not confirmed yet.
- **D-C8: build it, switched off.** Nobody knows yet whether a hotel's Protel interface supports TLS. C8 makes the existing `UseTLS` switch work, defaulting to off; turning it on for a hotel is a separate decision, because it changes PMS traffic.
- **D-C9: warn, do not refuse.** When a hotel asks a sign-in provider for more than name and email, Hotel Admin warns; the hotel, as controller, decides.
- **D-C10: follow the industry standard (NIST SP 800-63B, OWASP ASVS), kept simple:**
  - an authenticator app (TOTP) as the second step, for every Hotel Admin and Central account; staff still sign in with their email and password;
  - setting up the app at the next sign-in, by scanning a QR code;
  - 10 single-use recovery codes;
  - a site admin can reset a colleague's second step, with the reset audited;
  - idle logout after 30 minutes and a session limit of 12 hours, both hotel settings (§0C).
- **D-F8: record views at the level the standards ask for, without slowing pages down.** Opening one guest's details and every guest search are recorded; list pages are not. Entries are written in the background, so a page never waits for the log.
- **C12:** the account owner rotates the Gmail app password later.

### Still open

- **D-C11** (disk encryption): needs more information first.
- **D-F2** (breach contact), **D-F3** (legal reviewer).

## 5. Proposed order

1. **Decisions first:** D-C1, D-C1a, D-C3, D-C4, D-C5 and D-C6 are taken (§4). C1, C3, C4 and C5 are no longer blocked by a decision.
2. **Start now in parallel (FAST, no decision needed):** F4 and F9 are done on this branch; F6 moved to C1. F1, F2 and F3 can be drafted now and finalised once D-F1, D-F2 and D-F3 are made.
3. **Then:** C1 (built: migration 0127, includes F6) + C2 (retention) → C3 + C4 (built: migration 0128, erasure and export sharing one guest lookup) → C5 (built: migration 0129, backups encrypted to hotel-held keys).
4. **Then:** C6–C11 as their decisions arrive.

Every step stays on `delivery/gdpr` as local commits. By the Product Owner's direction (2026-10-17), nothing in this plan is deployed to PRE-LIVE or merged to `master`.
