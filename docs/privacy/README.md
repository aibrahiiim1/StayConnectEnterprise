# Privacy and GDPR documents

| Document | What it is | Status |
|---|---|---|
| [GDPR_REMEDIATION_PLAN.md](GDPR_REMEDIATION_PLAN.md) | The remediation plan and the Product Owner's decisions | Working document |
| [DPA_TEMPLATE.md](DPA_TEMPLATE.md) | Our standard Data Processing Agreement with hotels (Art. 28), with Annex I (description of the processing) | Draft (F3) |
| [DPA_ANNEX_II_SECURITY_MEASURES.md](DPA_ANNEX_II_SECURITY_MEASURES.md) | Annex II: technical and organisational measures (Art. 32) | Draft (F3) |
| [DPA_ANNEX_III_SUBPROCESSORS.md](DPA_ANNEX_III_SUBPROCESSORS.md) | Annex III: our sub-processors, and the hotel's own providers | Draft (F3) |
| [RECORD_OF_PROCESSING.md](RECORD_OF_PROCESSING.md) | Our records of processing: as processor (Art. 30(2)) and, for Central, as controller (Art. 30(1)) | Draft (F3) |
| [../runbooks/PERSONAL_DATA_BREACH.md](../runbooks/PERSONAL_DATA_BREACH.md) | Breach response | Draft (F2) |

**None of these goes to a customer before a lawyer has reviewed it (decision D-F3).** They are engineering drafts:
every fact about the system was checked in the code on `delivery/gdpr` (2026-10-18), but the legal wording is not
advice.

The hotel's own documents (its Art. 30 record, its DPIA, its privacy notice) are the hotel's to write. Templates
for those are a separate F3 item. The guest privacy-notice template already ships in Hotel Admin (F1).

## How the roles were decided

- **The hotel is the controller** of everything on its appliance. The appliance is on its premises and run by its
  staff.
- **We are not a processor merely because we supply the software.** We have no standing access: the link to
  Central carries licensing only, and installation leaves no login of ours. The EDPB's Guidelines 07/2020 on
  controller and processor treat a software supplier without access to the data as neither.
- **We are a processor** only when we touch the data for the hotel: installation, support sessions the hotel
  opens, and files the hotel sends (DPA clause 2.2).
- **We are a controller** for Central: staff accounts, appliance identity, licences, and Central's audit log
  (DPA clause 2.3, record Part 2). That needs our own privacy notice, which does not exist yet.
- **The hotel's email, SMS, social sign-in, payment and PMS providers are the hotel's own processors.** The hotel
  holds the accounts. They are listed for transparency (Annex III, Part B), not as our sub-processors.

## What must be decided before signature

| # | Decision | Where it appears |
|---|---|---|
| 1 | Our company is **Semantics Technology Ltd** (Product Owner, 2026-10-18). Still needed: the registered address, the company number, the country of registration, and a privacy contact email | DPA parties, record |
| 2 | **Where we and our support personnel are established.** Remote access from outside the EEA is a transfer under Chapter V (EDPB Guidelines 05/2021). If outside, the DPA needs the SCCs, Module Two, a transfer impact assessment, and possibly an Art. 27 representative | DPA clause 8, record Part 1(c) |
| 3 | The services our support uses (helpdesk, email, file transfer, remote screen sharing): each becomes a sub-processor | Annex III, Part A |
| 4 | Confirm our internal practices: confidentiality clauses, training, encrypted support laptops, MFA on support systems | Annex II, Part A, rows marked [[confirm]] |
| 5 | The breach contact (D-F2) | DPA clause 7.3 |
| 6 | Governing law and courts; the liability position | DPA clauses 11 and 13 |
| 7 | Retention periods for Central: disabled accounts, audit log, security alerts, retired appliances | Record Part 2 |
| 8 | Whether Central is self-hosted or with a provider | Annex III, Part C; record Part 2 |
| 9 | Our privacy notice for Central users (needed for record Part 2) | DPA clause 2.3 |

## Found while checking the code (not changed; backlog for the plan)

These either limit what the documents can promise or are facts a hotel's DPIA needs. Each is written into the
draft as it is today. Fixing one means updating the draft.

- **Appliance:**
  - SSH is open on the management interface by default (Annex II, Part C2).
  - Backups stay unencrypted until the first key holder is confirmed (Part C3).
  - The PMS link is cleartext (C8).
  - There is no disk encryption (C11).
  - System logs are unbounded: Caddy access log, Kea DHCP log, journald (F5).
- **Retention gaps on the appliance:**
  - `otp_code_escrow`, `otp_code_reveals` and `device_network_appearances` are outside the retention engine.
  - Operator accounts have no retention.
  - Backups keep for up to 30 days data the live database has already anonymised. That is normal for backups, but
    the hotel's notice should say so.
- **Central:**
  - The audit log, security alerts and lifecycle events are never purged.
  - Database backups are not encrypted.
  - There is no per-account password lockout; only a per-address rate limit and the code lockout.
  - ctrlapi connects as the database superuser, and customers are kept apart in the application only, with no
    row-level security.
  - The audit log stores the email typed on a failed sign-in.
- **Documents:**
  - `ROLE_AND_SCOPE_MATRIX.md` says the IT manager has no access to Operators; the code gives read access.
  - `CENTRAL_CONTROL_PLANE.md` still says sessions last 24 hours; it is 12 since Central migration 0052.
- **Social sign-in** also keeps the business-account organisation claims (`hd`, `tid`, `oid`), not only the email
  and subject. The plan and Annex II now say so. C9 (asking providers for less) is still to be built.
