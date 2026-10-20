# Privacy and GDPR documents

| Document | What it is | Status |
|---|---|---|
| [GDPR_REMEDIATION_PLAN.md](GDPR_REMEDIATION_PLAN.md) | The remediation plan and the Product Owner's decisions | Working document |
| [ASSISTED_GUEST_DATA_REQUESTS.md](ASSISTED_GUEST_DATA_REQUESTS.md) | How staff hand over or anonymise the internet-usage records of one person's device when no automatic rule can say they are theirs (migration 0146): the workflow, the classes, what it never does, the limits | Authoritative for the assisted workflow |
| [DPA_TEMPLATE.md](DPA_TEMPLATE.md) | Our standard Data Processing Agreement with hotels (Art. 28), with Annex I (description of the processing) | Draft (F3) |
| [DPA_ANNEX_II_SECURITY_MEASURES.md](DPA_ANNEX_II_SECURITY_MEASURES.md) | Annex II: technical and organisational measures (Art. 32) | Draft (F3) |
| [DPA_ANNEX_III_SUBPROCESSORS.md](DPA_ANNEX_III_SUBPROCESSORS.md) | Annex III: our sub-processors, and the hotel's own providers | Draft (F3) |
| [RECORD_OF_PROCESSING.md](RECORD_OF_PROCESSING.md) | Our records of processing: as processor (Art. 30(2)) and, for Central, as controller (Art. 30(1)) | Draft (F3) |
| [HOTEL_RECORD_OF_PROCESSING_TEMPLATE.md](HOTEL_RECORD_OF_PROCESSING_TEMPLATE.md) | For the hotel: its Art. 30 record for guest Wi-Fi, pre-filled. The hotel answers four short steps | Draft (F3) |
| [HOTEL_DPIA_STARTER.md](HOTEL_DPIA_STARTER.md) | For the hotel: its risk assessment (Art. 35), with the risks and OneGate's measures pre-filled. The hotel ticks and chooses | Draft (F3) |
| [CENTRAL_PRIVACY_NOTICE.md](CENTRAL_PRIVACY_NOTICE.md) | Our privacy notice for people with a Central account (Art. 13), linked from DPA clause 2.3 | Draft (F3) |
| [../runbooks/PERSONAL_DATA_BREACH.md](../runbooks/PERSONAL_DATA_BREACH.md) | Breach response | Draft (F2) |

**None of these goes to a customer before a lawyer has reviewed it (decision D-F3).** They are engineering drafts:
every fact about the system was checked in the code on `delivery/gdpr` (2026-10-18), but the legal wording is not
advice.

The hotel's own documents (its Art. 30 record, its DPIA, its privacy notice) are the hotel's. We give it
templates that already say what OneGate does: the two `HOTEL_` documents above, and the guest privacy-notice
template that ships in Hotel Admin (F1). The hotel's part is written to be filled in by its IT manager, not a
lawyer: tick boxes, a few blanks, and where in Hotel Admin each answer is found.

## How the roles were decided

- **The hotel is the controller** of everything on its appliance. The appliance is on its premises and run by its
  staff.
- **We are not a processor merely because we supply the software.** We have no standing access: the link to
  Central carries licensing only, and installation leaves no login of ours. The EDPB's Guidelines 07/2020 on
  controller and processor treat a software supplier without access to the data as neither.
- **We are a processor** only when we touch the data for the hotel: installation, support sessions the hotel
  opens, and files the hotel sends (DPA clause 2.2).
- **We are a controller** for Central: staff accounts, appliance identity, licences, and Central's audit log
  (DPA clause 2.3, record Part 2). Our own privacy notice covers it ([CENTRAL_PRIVACY_NOTICE.md](CENTRAL_PRIVACY_NOTICE.md)).
- **The hotel's email, SMS, social sign-in, payment and PMS providers are the hotel's own processors.** The hotel
  holds the accounts. They are listed for transparency (Annex III, Part B), not as our sub-processors.

## What must be decided before signature

| # | Decision | Where it appears |
|---|---|---|
| 1 | **Semantics Technology Ltd**, registered in the United Kingdom, office at 124 City Road, London EC1V 2NX, United Kingdom (Product Owner, 2026-10-18 and 19). Still needed: the company number, which UK register (England and Wales assumed), a privacy contact email, and the ICO registration | DPA parties, record |
| 2 | **Transfers, now drafted for a UK company.** Remote access from the UK is a transfer for an EEA hotel, covered by the EU adequacy decision for the UK; no SCCs while it stands (DPA clause 8.2). Still needed: whether anyone supports hotels from outside the UK (clause 8.3), and the lawyer's view on an Art. 27 representative in the EU (clause 8.4). UK hotels are under the UK GDPR, which clause 1.1 now covers | DPA clause 8, record Part 1(c) |
| 3 | The services our support uses (helpdesk, email, file transfer, remote screen sharing): each becomes a sub-processor | Annex III, Part A |
| 4 | Confirm our internal practices: confidentiality clauses, training, encrypted support laptops, MFA on support systems | Annex II, Part A, rows marked [[confirm]] |
| 5 | The breach contact (D-F2) | DPA clause 7.3 |
| 6 | Governing law and courts; the liability position | DPA clauses 11 and 13 |
| 7 | ~~Retention periods for Central~~: decided 2026-10-19 (D-CR) and built (Central migration 0054): audit log 24 months; alerts and appliance history 6 years with the people removed after 24 months; disabled accounts 24 months; retired appliances and licences 6 years. All settings | Record Part 2 |
| 8 | Central's hosting provider: a provider, not yet chosen (Product Owner, 2026-10-19). Name, country and, outside the UK and EEA, the safeguard | Annex III, Part C; record Part 2 |
| 9 | ~~Our privacy notice for Central users~~: drafted 2026-10-19 ([CENTRAL_PRIVACY_NOTICE.md](CENTRAL_PRIVACY_NOTICE.md)). Where to publish it once approved: a link on Central's sign-in page | DPA clause 2.3 |
| 10 | **Appliance SSH, to decide before launch (D-SSH).** It stays open for the hotel's IT. How the login is protected is still open: passwords hardened (lockout, no direct root login, logins recorded, strong passwords), key-only, or as today. GDPR does not require turning it off; it requires appropriate protection (Art. 32). See the plan, D-SSH | Annex II, Part C2; hotel DPIA R2 |

## Found while checking the code (not changed; backlog for the plan)

These either limit what the documents can promise or are facts a hotel's DPIA needs. Each is written into the
draft as it is today. Fixing one means updating the draft.

- **Appliance:**
  - SSH is open on the appliance's uplink (management) interface by default, with the operating system's default login settings: no lockout, root and password login not restricted by us (Annex II, Part C2). Decision D-SSH, before launch.
  - Backups stay unencrypted until the first key holder is confirmed (Part C3).
  - The PMS link can be encrypted since C8 (2026-10-19), but it is off by default.
  - There is no disk encryption (C11).
  - ~~System logs are unbounded~~: fixed by F5 (migration 0134, 2026-10-19), 14 days by default.
- **Retention gaps on the appliance:**
  - ~~`otp_code_escrow`, `otp_code_reveals` and `device_network_appearances` are outside the retention engine~~.
    Checked 2026-10-19: escrowed codes and reveals were already removed every minute, and network sightings refer
    only to a device id, anonymous once the device is. The real gap, code device bindings kept on a quiet site,
    is fixed.
  - Operator accounts have no retention.
  - Backups keep for up to 30 days data the live database has already anonymised. That is normal for backups, but
    the hotel's notice should say so.
- **Central:**
  - ~~The audit log, security alerts and lifecycle events are never purged~~: Central migration 0054 (2026-10-19), periods as decided in D-CR, all settings.
  - ~~Database backups are not encrypted~~: encrypted with age since 2026-10-19, once a key is listed in
    `/etc/stayconnect/backup-recipients.txt`. Until a key is listed they stay plain, and every run warns.
  - ~~There is no per-account password lockout~~: Central migration 0053 (2026-10-19), 5 wrong passwords lock for
    15 minutes, both settings.
  - ctrlapi connects as the database superuser, and customers are kept apart in the application only, with no
    row-level security.
  - ~~The audit log stores the email typed on a failed sign-in~~: since 2026-10-19 a failure is recorded against the account if one exists, otherwise with no name.
  - ~~The authenticator app shows "StayConnect Central"~~: "OneGate Central" for new enrolments since 2026-10-19; existing entries keep their old label and their codes still work.
- **Documents:**
  - ~~`ROLE_AND_SCOPE_MATRIX.md` says the IT manager has no access to Operators~~: corrected to read-only, 2026-10-19.
  - ~~`CENTRAL_CONTROL_PLANE.md` still says sessions last 24 hours~~: corrected to 12, 2026-10-19.
- **Social sign-in** also keeps the business-account organisation claims (`hd`, `tid`, `oid`), not only the email
  and subject. The plan and Annex II now say so. C9 (asking providers for less) is built.
