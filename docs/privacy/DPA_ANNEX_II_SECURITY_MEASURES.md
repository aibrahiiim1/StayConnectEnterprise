# Annex II: Technical and organisational measures (Art. 28(3)(c) and 32)

> **DRAFT**, part of [DPA_TEMPLATE.md](DPA_TEMPLATE.md). It is not for use with a customer before the D-F3 legal
> review. Part B was checked against the code on `delivery/gdpr` (2026-10-18) and describes only what the
> Software does. The headings follow Annex II of the Standard Contractual Clauses (Decision (EU) 2021/914).
> Items marked **[[confirm]]** are practices of ours that the Product Owner must confirm exist before signature.

The annex has three parts:

- **Part A**: what the Processor does when it processes personal data for the Controller (support sessions,
  installation, Customer Files).
- **Part B**: the security features built into the Software that the Controller runs.
- **Part C**: what stays with the Controller.

## Part A: Measures of the Processor

| Area | Measure |
|---|---|
| Access to the Appliance | No standing access. Access exists only when the Controller grants it, for a stated purpose recorded in a support ticket (reference, reason, the person working). It is closed afterwards, and the Processor asks the Controller to remove the login or key. Installers keep no root or SSH login after an installation. |
| Personnel | Only personnel working on the case get access. All are bound to confidentiality **[[confirm: employment contract or NDA clause]]** and trained on this DPA and the breach procedure **[[confirm]]**. |
| Customer Files | The Processor asks for the smallest extract that answers the question. Files are kept only within the support case, with access limited to the people working on it. They are never copied into chat, other tickets, source code, commits or delivery evidence, and are deleted when the case closes (DPA clause 9.2). |
| Processor's own equipment | Devices used for support have full-disk encryption, screen lock and current security updates **[[confirm]]**. Accounts used for support systems use multi-factor authentication **[[confirm]]**. |
| Breach handling | A written procedure (`docs/runbooks/PERSONAL_DATA_BREACH.md`): first notice to the Controller within 24 hours, a named breach contact **[[D-F2]]**, and a record of every incident whether or not it is reported. |
| Software vulnerabilities | The Processor tells affected Controllers within 24 hours of confirming a flaw that exposes personal data (DPA clause 7.4). |
| Software integrity | Licences and site assignments are signed with Ed25519, and the Appliance refuses unsigned or altered ones. Release packages are checked against SHA-256 checksums at deployment. The vendor signing key is kept encrypted, with no plaintext copy. |
| Sub-processors | Engaged only under DPA clause 6, with equivalent obligations. |

## Part B: Security features of the Software

### Pseudonymisation and encryption

- **Secrets are sealed at rest** with AES-256-GCM, each one bound to its owner record. This covers provider
  credentials (email, SMS, WhatsApp, social sign-in, payment, PMS), staff authenticator secrets, one-time codes
  held for staff, voucher codes, and the credential part of every guest sign-in attempt. The keys sit in a
  root-only directory, and a key file readable by others is refused.
- **Passwords are hashed with argon2id** (64 MiB, 1 pass, 4 lanes, 16-byte salt) for staff and guest accounts.
- **Backups are encrypted** with age (X25519) to the Controller's own key holders. The Appliance keeps only the
  public keys and cannot read its own backups. The Processor holds no backup key. Once a key holder exists, a
  backup that cannot be encrypted fails rather than being written in clear.
- **Retention and erasure anonymise:** expired or erased records have their identifiers replaced with a random
  token per guest, with no mapping kept, and free text blanked.

### Confidentiality, integrity, availability and resilience

- **Network separation:** the firewall drops by default. Guests reach only their own gateway (address assignment,
  name resolution, portal) until signed in, never the management network or each other's networks. Guest IPv6 is
  dropped, and client isolation (devices on a network cannot reach each other) is on by default for a new
  network with a sign-in page. The Admin Console is served on the management interface
  only.
- **Services run apart:** separate service accounts, a database role per service with only the rights it needs
  (no superuser, no row-level-security bypass), and an internal socket that admits only the expected service.
- **Database access:** the database listens on the appliance only. A new appliance gets a random superuser
  password. An older appliance may still have the default one; the Admin Console warns until it is changed, and
  the Processor changes it on the appliances it maintains.
- **Database log:** the database logs errors without the statement or the values that caused them, and its
  container log is limited by size (3 files of 10 MB).
- **Offline operation:** guests keep being admitted from the last verified copy of the PMS data when the PMS
  link drops.

### Restoring availability and access in a timely manner

- Nightly encrypted backups of the database and the configuration, with retention limits.
- A restore-test reminder, every 6 months by default (a Controller setting), proving a key holder can still
  open a backup.
- Documented restore and clean-install procedures.

### Regular testing

- Automated tests cover sign-in, access control, retention, export and erasure, including against a real
  database.
- The full test and dependency-audit run is started on demand before a release. **[[Decide whether to promise a
  cadence.]]**

### User identification and authorisation

- **Staff sign in with a password and an authenticator-app code** (TOTP, RFC 6238). Ten single-use recovery codes
  are issued. A code is never accepted twice.
- **Wrong passwords and codes are throttled** per account and per address: by default 5 per account or 20 per
  address within 15 minutes locks for 15 minutes.
- **Sessions end** after 30 minutes idle and 12 hours at most (Controller settings). Session cookies are
  HttpOnly and Secure.
- **Role-based access control is enforced on the server.** Sensitive actions require the password again: reveal
  of credentials or codes (including what a guest typed at sign-in), backups, network and PMS changes, and every
  change to a staff account: adding, disabling, resetting a password or an authenticator, and changing roles.
  Email for guests goes over TLS, except to a mail server at a private address. Exporting or erasing
  a guest is limited to the site administrator and IT manager roles, needs the password and a reason, and is
  logged.
- **Guest sign-in** is throttled, and one-time codes expire after 10 minutes and allow 5 tries.
- **Room sign-in guessing is limited** per device (5 wrong in 60 seconds wait 60 seconds), and per network: by
  default 30 wrong room sign-ins on one network within 10 minutes, from any devices, pause Room sign-in there for
  15 minutes. Staff can let a confirmed guest's device try again, or end a false alarm, with a reason, audited.
  All of these are Controller settings.

### Protection of data during transmission

- The Admin Console is served over HTTPS (TLS 1.2 or higher) with HSTS, using a certificate from the Appliance's
  own authority.
- The guest portal is served over HTTPS (TLS 1.2 minimum) with a publicly trusted certificate. A plain-HTTP
  address remains available, because a guest's device must reach the portal before it trusts anything.
- The Appliance talks to Central over mutual TLS (TLS 1.2 minimum), with a certificate bound to its identity.
- Email can be sent with STARTTLS or TLS (TLS 1.2 minimum, certificate checked).
- Database connections stay on the Appliance's loopback interface.

### Protection of data during storage

- See *Pseudonymisation and encryption*.
- Card data never reaches the Appliance: guests pay on the payment provider's own page. Payment providers receive
  no guest identifier.

### Physical security

- This is the Controller's responsibility (Part C). The Appliance sits at the Controller's premises.

### Events logging

- An audit log records staff sign-ins, failed sign-ins and lockouts, authenticator events, configuration changes,
  credential reveals, backup downloads, and guest exports and erasures, with time, staff member, IP address and
  user agent. It also records when staff open one guest's record (opening the same record again within 10
  minutes is recorded once) or search guest data, written in the background so pages do not slow down. What was
  searched for is shown only to roles with guest-data rights. Service accounts can add entries but cannot change
  or delete them.
- **Request logs name the route, not the person.** The appliance's services and Central log the kind of page or
  API served (for example `/edge/v1/usage/devices/{mac}`), never the address asked for, its query or the client's
  address. The web server's access logs drop the query, MAC addresses and request headers. So searches typed by
  staff, single-use links and the web addresses guests' browsers ask for before sign-in are not logged. Some
  appliance services log a guest device's IP address with a technical event (for example when a phone checks for
  the sign-in page, or a session starts); these logs are kept for the system-log period.

### System configuration and defaults

- The portal loads nothing from outside hosts by default (`default-src 'self'`). Hotel Admin warns when a design
  would load a logo, image or font from elsewhere.
- Operational limits (retention periods, throttles, session limits, the restore-test interval) are settings with
  validated bounds, and every change is logged with who made it and why.

### Data minimisation, quality and limited retention

- Social sign-in keeps only the provider's subject id, the verified email and, for business accounts, the
  provider's organisation identifiers. Google, Facebook and Apple are asked for the email only; Microsoft also for
  the profile, which some personal accounts need to give their email. Hotel Admin warns when a hotel asks a
  provider for more.
- PMS integrations exclude documents and addresses.
- Every guest-data category has a retention period the Controller sets (Annex I). Retention runs hourly.

### Accountability, portability and erasure

- **Guest data requests** in Hotel Admin find one guest's records across the system, export them as a
  machine-readable JSON file, or erase them by anonymisation. Erasure is refused while the stay, access or device
  is still live, and leaves a reference to give the guest.
- **Privacy notice record:** every sign-in records which version of the Controller's privacy notice was in force,
  stored once per version and unchangeable.

## Part C: The Controller's responsibilities

The Software is designed to be operated by the Controller. These matters stay with the Controller:

1. **Physical security** of the Appliance, and who holds its root or console login.
2. **The SSH service is open on the management interface by default.** The Controller restricts it to trusted
   addresses or turns it off, and removes any login given for a support session once the session ends.
3. **Backup keys:** register at least one key holder at set-up. Backups stay unencrypted until the first holder is
   confirmed. The Controller also stores keys safely and runs the restore test when reminded.
4. **Off-site copies:** backups are kept on the Appliance. Copying them elsewhere, if wanted, is the Controller's
   choice.
5. **Retention settings** and the **privacy notice** shown to guests (Art. 13).
6. **Its own providers** (email, SMS, WhatsApp, social sign-in, payment, PMS) and their agreements.
7. **Disk encryption:** the Software does not encrypt the Appliance's disk. **[[Revisit after D-C11.]]**
8. **The PMS link (Protel FIAS)** can be encrypted with TLS (with the PMS certificate pinned by its fingerprint),
   but it is off by default and works only if the Controller's PMS supports TLS; the Controller decides to turn it
   on. While it is off, the link should run on a trusted network segment.
9. **System logs** (web access log, address-assignment log, service journal) are deleted after the Controller's
   log period, 14 days by default (a setting of 1 to 730 days). The Controller sets a longer period only if its
   national law requires connection logs to be kept.
10. **HTTPS for the guest portal.** Until the portal's certificate is issued, the sign-in page is served over plain
    HTTP, and what guests type to sign in crosses the guest network unencrypted. Hotel Admin warns on the Sign-in
    methods page while this lasts. The Controller completes Portal HTTPS before guests rely on typed sign-in
    methods, and keeps client isolation on.
