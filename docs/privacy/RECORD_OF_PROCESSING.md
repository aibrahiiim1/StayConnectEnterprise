# Records of processing activities (Art. 30)

> **DRAFT** for the D-F3 legal review. It was checked against the code on `delivery/gdpr` (2026-10-18).
> `[[placeholders]]` are facts the code cannot give.

There are two records here, because we hold two roles:

- **Part 1** is our record as **processor** (Art. 30(2)), for the services we perform for hotels under the DPA.
- **Part 2** is our record as **controller** (Art. 30(1)), for OneGate Central.

Keep both up to date and give them to a supervisory authority on request (Art. 30(4)). The Art. 30(5) exemption
for organisations under 250 employees is not relied on: Central's processing is continuous, not occasional.

## Common details

| | |
|---|---|
| Name and contact details | Semantics Technology Ltd, registered office 124 City Road, London EC1V 2NX, United Kingdom, company number [[ ]], [[privacy contact email]] |
| UK registration with the ICO (data protection fee) | [[registration number: a UK controller must pay the fee unless exempt]] |
| Representative in the Union (Art. 27): we are established in the United Kingdom, outside the EU | [[legal review: whether one is needed; if so, name and contact]] |
| Data protection officer (Art. 37), if one is designated | [[name and contact, or "none designated": record the reason]] |
| Breach contact | [[D-F2]] |

## Part 1: Processing on behalf of hotels (Art. 30(2))

**(a) Controllers.** Each hotel that has signed the DPA. The register of signed DPAs ([[where it is kept]]) lists
each hotel's name, its contact details, its representative and DPO if any, and the DPA date.

**(b) Categories of processing carried out on behalf of each controller.**

| Activity | What we do | Data concerned (DPA Annex I) |
|---|---|---|
| Installation and commissioning | Install the Software on the hotel's Appliance and connect it to the hotel's network and PMS, on site or with access the hotel gives | Everything on the Appliance may be visible: PMS reservation data, guest sign-in identities, devices and usage, staff accounts, audit log |
| Support sessions | Diagnose and fix faults, with root, SSH or Admin Console access the hotel opens for the session | As above |
| Customer Files | Receive and analyse the logs, screenshots, exports or backups the hotel sends | Whatever the hotel sends. System logs contain device MAC and IP addresses and sign-in identifiers |

We keep no guest data in our own systems beyond Customer Files. They are deleted when the case closes, within
[[30]] days (DPA clause 9.2).

**(c) Transfers to a third country.** We work from the United Kingdom. For hotels in the EEA, remote access and
Customer Files reach the UK under the EU adequacy decision for the United Kingdom (DPA clause 8.2). Any support
from another country, or any sub-processor in DPA Annex III, Part A, outside the UK, EEA and adequate countries:
[[none, or the country and the safeguard]].

**(d) Security measures.** DPA Annex II, Part A.

## Part 2: OneGate Central (Art. 30(1))

Central's staff console is at `central.onegate.getsemantics.com`. Appliances reach Central for licensing at
`sc-central.echofusion.com`.

**(a) Controller.** As in *Common details*.

**(b) Purposes and (c) categories.**

| Purpose | Data subjects | Personal data | Lawful basis | Retention today |
|---|---|---|---|---|
| **Central accounts:** sign-in and permissions for our platform staff and for customers' staff | Our staff; the hotels' staff who have a Central account | Email, display name, password hash (argon2id), sealed authenticator secret, recovery-code hashes, roles, status, last sign-in | Art. 6(1)(b) for our own staff (employment). Art. 6(1)(f) for customers' staff: providing the contracted service to their employer | While the account is in use. Anonymised 24 months after it is disabled (email replaced; name, password, authenticator and roles removed) |
| **Sessions** | As above | Session record: operator id, email, display name, roles, times. No IP or browser | Art. 6(1)(f): keeping the service secure | 30 minutes idle, 12 hours at most. Pending sign-ins 5 minutes |
| **Security and accountability:** the audit log of Central actions | As above | Who did what and when, IP address and browser user agent. A failed sign-in is recorded against the account if one exists, otherwise with no name | Art. 6(1)(f): security, fraud prevention, accountability | 24 months |
| **Appliance identity, activation and licensing** | Hotels. Personal data only where a hotel is a sole trader or a person can be identified | Serial, hardware fingerprint, MAC addresses, host name, the hotel's public IP, customer and site names, licence terms | Art. 6(1)(b): the licence contract | For the life of the customer relationship. A retired appliance's MAC addresses, host name and public IP are cleared 6 years after retirement; ended licences are deleted 6 years after they end (the UK limitation period for a contract claim). Lifecycle history: 6 years, with the operator and IP address removed after 24 months. Assignment fetch log: 30 days |
| **Guest-portal certificate** | The one Central-wide certificate contact (normally our own staff) | Portal host names. The ACME contact email, also in the change history | Art. 6(1)(f) | Contact email removed from the history after 24 months; the setting while it is used. Host names are published permanently in Certificate Transparency logs |
| **Security alerts** about appliances | As for licensing | Appliance serial, alert detail, source IP, who acknowledged it | Art. 6(1)(f): security | Source IP and who acknowledged it removed after 24 months; closed alerts deleted after 6 years. Open alerts stay until closed |

No guest data is processed in Central. It holds no guest records, and the Appliance sends it none.

**(d) Recipients.** Cloudflare (DNS records and lookups for portal host names), Let's Encrypt / ISRG (certificate
issuance), [[Central hosting provider: not decided yet; its name, and the country where Central's data is stored]]. See DPA Annex III, Part C.

**(e) Transfers to a third country.** Let's Encrypt (ISRG) and Cloudflare are United States companies:
[[safeguard: EU–US Data Privacy Framework certification or SCCs, to confirm for each]]. Only portal host names and
one contact email are involved.

**(f) Time limits for erasure.** As in the table. The main periods are settings in Central (System > Team > Data retention), applied hourly and audited; the session limits, the 30-day appliance fetch log and the web access log are fixed. Backups of Central's database: the newest 7 are kept on the
Central server. The web server's access log (IP addresses) is rolled daily and kept 14 days. The service journal
names the route served, never the person or their address, and is kept within the host's journal size limit.

**(g) Security measures.**

- Two-step sign-in (password and authenticator code), optional per account and recommended (D68), enforced for every account that turns it on. After 5 wrong codes the account locks
  for 15 minutes.
- Sessions end after 30 minutes idle and 12 hours at most.
- The password is asked again for sensitive actions.
- Permissions are checked on the server, and customers are scoped to their own records.
- HTTPS for the console, and mutual TLS for appliances.
- Authenticator secrets are sealed with AES-256-GCM. Key files are root-only.
- Signing keys are kept apart. The root CA key is moved out of the running service's directory (it is still on the
  same server; moving it to offline media is an open step).
- The firewall allows only SSH, HTTPS and the appliance port.
- Central has no outside analytics, fonts or trackers.
