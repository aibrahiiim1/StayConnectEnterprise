# Data Processing Agreement (template)

> **DRAFT. Not for use with a customer.** An engineering draft written against the code on `delivery/gdpr`
> (2026-10-18). A qualified lawyer must review it before any hotel signs it (plan decision **D-F3**). Every
> `[[placeholder]]` must be completed, and the drafting notes in [README.md](README.md) resolved, first.

This Data Processing Agreement ("**DPA**") is made between:

1. **[[Hotel legal name]]**, [[address]], [[registration number]] (the "**Controller**"); and
2. **[[StayConnect legal entity]]**, [[address]], [[registration number]] (the "**Processor**"),

each a "**Party**". It forms part of the [[name of the licence / support agreement]] dated [[date]] (the
"**Main Agreement**") and applies from the date the last Party signs it.

## 1. Definitions

1.1 "**GDPR**" means Regulation (EU) 2016/679. "**Data Protection Law**" means the GDPR and any national law
supplementing it that applies to the processing under this DPA. Terms such as *personal data*, *processing*,
*controller*, *processor*, *data subject*, *personal data breach* and *supervisory authority* have the meanings
given in Art. 4 GDPR.

1.2 "**Software**" means the StayConnect appliance software (the guest portal, the Admin Console and the services
behind them) installed on equipment at the Controller's premises (the "**Appliance**").

1.3 "**Central**" means the Processor's OneGate Central service, which the Appliance contacts for identity,
activation, licensing and the guest-portal certificate.

1.4 "**Services**" means the services described in clause 2.2.

1.5 "**Customer Files**" means any file the Controller or its staff sends to the Processor, such as logs,
screenshots, database extracts, guest-data exports or backups.

1.6 "**Sub-processor**" means another processor engaged by the Processor to carry out processing on the
Controller's behalf under this DPA.

## 2. Scope and roles

2.1 **The Controller operates the Software itself.** The Appliance and all guest data on it are at the
Controller's premises and under the Controller's control. Supplying, licensing and updating the Software is not
processing of personal data on the Controller's behalf, and the Processor has no standing access to the Appliance
or to the personal data on it. In particular:

- (a) the Appliance's connection to Central carries identity, activation, licensing and portal-certificate
  information only. It carries no guest data and gives no remote command, shell or data access;
- (b) installation creates no account, SSH key, tunnel or VPN for the Processor; and
- (c) the email, SMS, WhatsApp, social sign-in, payment and PMS services the Software connects to are accounts
  the Controller holds with those providers directly (see clause 7.6).

2.2 **The Processor processes personal data on the Controller's behalf only when it performs these Services:**

- (a) **installation and commissioning** of the Appliance at the Controller's request;
- (b) **support sessions**, in which the Controller gives the Processor's personnel access to the Appliance (for
  example a root or SSH login, or an Admin Console account) for a stated purpose;
- (c) **handling Customer Files**.

Annex I describes this processing.

2.3 **Processing for the Processor's own purposes.** Central holds the accounts of the Controller's staff who sign
in to Central, the Appliance's identity and licence, and an audit log of actions taken in Central. The Processor
decides why and how that data is processed, so it does this as a **controller**, not under this DPA. Its privacy
notice ([[link]]) explains that processing. No guest data reaches Central.

2.4 The Controller remains responsible for the lawfulness of the processing it carries out with the Software, for
the information it gives data subjects (Art. 13 and 14 GDPR), and for its own processors (clause 7.6).

## 3. Instructions (Art. 28(3)(a))

3.1 The Processor processes the personal data only on the Controller's documented instructions, including with
regard to transfers to a third country (clause 8), unless Union or Member State law to which the Processor is
subject requires otherwise. In that case the Processor informs the Controller of that legal requirement before
processing, unless that law prohibits such information on important grounds of public interest.

3.2 The Controller's instructions are this DPA, the Main Agreement, and the request opening each support session
or sending each Customer File, recorded in the support ticket (purpose, scope and the person who gave it).

3.3 The Processor informs the Controller immediately if, in its opinion, an instruction infringes Data Protection
Law (Art. 28(3), final subparagraph). It may suspend that instruction until the Controller confirms or changes it.

## 4. Confidentiality (Art. 28(3)(b))

4.1 The Processor ensures that the persons it authorises to process the personal data have committed themselves
to confidentiality or are under an appropriate statutory obligation of confidentiality, and that they process the
data only as needed for the Service concerned.

4.2 Access is limited to the personnel working on the installation, support session or Customer File concerned.

## 5. Security (Art. 28(3)(c), Art. 32)

5.1 The Processor implements the technical and organisational measures in **Annex II, Part A** for the processing
it carries out under this DPA.

5.2 **Annex II, Part B** describes the security features of the Software that the Controller operates. They assist
the Controller with its own obligations under Art. 25 and 32. Configuring and running them on the Appliance is the
Controller's responsibility, together with the matters in **Annex II, Part C**.

5.3 The Processor may update the measures in Annex II, provided the overall level of protection does not fall.

## 6. Sub-processors (Art. 28(2) and (4))

6.1 The Controller gives general written authorisation for the Processor to engage the Sub-processors listed in
**Annex III**.

6.2 The Processor informs the Controller at least **[[30]] days** before adding or replacing a Sub-processor. The
Controller may object on reasonable data-protection grounds within that period. If the Parties cannot resolve an
objection in good faith, the Controller may terminate the affected Service without penalty.

6.3 The Processor imposes on each Sub-processor, by contract, the same data-protection obligations as this DPA, in
particular sufficient guarantees of appropriate technical and organisational measures. The Processor remains fully
liable to the Controller for the performance of each Sub-processor's obligations.

## 7. Assistance

7.1 **Data subject rights (Art. 28(3)(e)).** The Software lets the Controller answer requests itself: Hotel Admin's
*Guest data requests* finds one guest's records, exports them as a machine-readable file (access and
portability), and erases them by anonymisation. Retention settings limit how long data is kept. The Processor
gives further reasonable assistance on request. A request the Processor receives directly from a data subject
is passed to the Controller without undue delay and is not answered by the Processor.

7.2 **Security, breach notification, DPIA and prior consultation (Art. 28(3)(f)).** The Processor assists the
Controller with its obligations under Art. 32 to 36, taking into account the nature of the processing and the
information available to it. This includes giving the Controller the information in Annex I and Annex II for its
data protection impact assessment.

7.3 **Personal data breach (Art. 33(2)).** The Processor notifies the Controller without undue delay, and in any
case within **24 hours** of becoming aware, of a personal data breach affecting personal data it processes under
this DPA. The first notice may be incomplete. Further information follows in phases as it becomes available
(Art. 33(4)). The notice describes, as far as known:

- the nature of the breach, the categories and approximate number of data subjects and of records concerned;
- the likely consequences;
- the measures taken or proposed, including to mitigate possible adverse effects; and
- the contact point at the Processor: [[breach contact: name, role, phone, email; deputy]] (plan decision D-F2).

The Processor does not notify a supervisory authority or data subjects on the Controller's behalf unless the
Controller instructs it in writing to do so.

7.4 **Vulnerabilities in the Software.** Outside the processing under this DPA, the Processor will tell the
Controller within **24 hours** of confirming a security flaw in the Software that exposes, or could expose,
personal data on the Appliance. It will say what is at risk and what the Controller should do. This does not
affect any reporting duty the Processor has as the Software's manufacturer.

7.5 The Processor may charge for assistance under clause 7 at its standard rates where the effort is
disproportionate or results from the Controller's own failure. It may not charge for clause 7.3.

7.6 **The Controller's own providers.** The Controller contracts directly with any email, SMS, WhatsApp, social
sign-in, payment or PMS provider it connects to the Software. Those providers are the Controller's processors (or
independent controllers), not Sub-processors of the Processor. The Controller is responsible for its own
agreements with them. Annex III, Part B lists them and the data the Software sends to each.

## 8. Transfers outside the EEA (Chapter V)

8.1 The Processor does not transfer the personal data to a country outside the European Economic Area, or to an
international organisation, except under clause 8.2 or on the Controller's documented instructions.

8.2 [[Choose one, after the D-F3 review. Remote access from outside the EEA is a transfer.]]

- **[[If the Processor and all support personnel are in the EEA:]]** The Processor performs the Services from
  within the EEA. A Sub-processor outside the EEA is engaged only if that country has an adequacy decision
  (Art. 45) or the Standard Contractual Clauses (Commission Implementing Decision (EU) 2021/914) are in place.
- **[[If the Processor, or any support personnel, is outside the EEA (in [[country]]):]]** The Parties enter into
  the Standard Contractual Clauses, Module Two (controller to processor), set out in [[Schedule]], which prevail
  over this DPA if they conflict. The Processor has carried out a transfer impact assessment, available to the
  Controller on request. Where Art. 3(2) GDPR applies to the Processor, it names its representative in the Union
  under Art. 27: [[representative]].

## 9. Deletion and return (Art. 28(3)(g))

9.1 **Support access.** At the end of each support session the Processor stops using the access and asks the
Controller to remove the login or key. The Processor keeps no copy of credentials after the session.

9.2 **Customer Files.** The Processor deletes Customer Files when the support case is closed, and in any case
within **[[30]] days** of it, unless the Controller asks for them to be returned first.

9.3 **On termination** of the Main Agreement, the Processor deletes or, at the Controller's choice, returns all
personal data it still holds under this DPA within **[[30]] days**, and confirms this in writing. This does not
apply to data that Union or Member State law requires it to keep. The Processor protects any such data under this
DPA for as long as it is kept.

## 10. Information and audits (Art. 28(3)(h))

10.1 The Processor makes available to the Controller all information necessary to demonstrate compliance with
Art. 28 GDPR. This includes the record in clause 10.3 and, on request, the support-ticket record of who had access
to the Appliance, when and why.

10.2 The Processor allows for and contributes to audits, including inspections, by the Controller or an auditor
it mandates who is bound by confidentiality. The Controller gives at least [[30]] days' notice. Audits take place
no more than once in any 12 months, unless a personal data breach or a supervisory authority requires otherwise.
Each Party bears its own costs.

10.3 The Processor keeps a record of processing under Art. 30(2) GDPR covering the Services.

## 11. Liability

11.1 [[Liability and its limits follow the Main Agreement, to the extent Data Protection Law allows. Art. 82 GDPR
applies regardless as between the Parties and data subjects.]]

## 12. Duration and precedence

12.1 This DPA lasts as long as the Processor processes personal data under it, including after the Main Agreement
ends, until clause 9.3 has been performed.

12.2 If this DPA conflicts with the Main Agreement on the protection of personal data, this DPA prevails. The
Standard Contractual Clauses, where they apply, prevail over both.

## 13. Governing law

13.1 This DPA is governed by [[the law of an EU Member State]], and the courts of [[place]] have exclusive
jurisdiction, without prejudice to data subjects' rights under Art. 79 GDPR.

Signed for the Controller: [[name, role, date]]

Signed for the Processor: [[name, role, date]]

---

## Annex I: Description of the processing (Art. 28(3), first subparagraph)

| Item | Description |
|---|---|
| **Subject matter** | Installation, commissioning and support of the Software on the Controller's Appliance, and handling Customer Files. |
| **Duration** | For each support session, while the access is open. For Customer Files, until deleted under clause 9.2. Overall, as set out in clause 12.1. |
| **Nature** | Access to the Appliance on the Controller's request (viewing, configuring, diagnosing, restoring), and receiving, storing and analysing Customer Files. The Processor does not copy personal data off the Appliance except as Customer Files the Controller chooses to send. |
| **Purpose** | To install the Software, diagnose and fix faults, and restore service, as the Controller requests. |
| **Data subjects** | (1) the Controller's guests and Wi-Fi users; (2) people the Controller's PMS lists on a reservation, including room sharers, who may never use the Wi-Fi; (3) the Controller's staff who use the Admin Console. |
| **Special categories (Art. 9) and criminal data (Art. 10)** | None are collected by the Software. Free-text fields (such as notes on a guest account) are entered by the Controller's staff and should not contain them. |
| **Retention at the Processor** | None beyond the support session and clause 9.2. Data on the Appliance follows the Controller's retention settings. |

**Categories of personal data the Processor may encounter on the Appliance or in Customer Files:**

| Category | Data | Default retention in the Software (a Controller setting) |
|---|---|---|
| PMS reservation data | Reservation number, room, arrival and departure, names of the guest and room sharers, VIP flag, travel agent, room type, rate plan, PMS guest id | Anonymised 30 days after checkout (7–3,650) |
| Guest sign-in identity | Email address, phone number, social sign-in subject id (and, for business accounts, the provider's organisation identifiers), display name, guest account username and password hash | Anonymised after 365 days without activity (30–3,650) |
| Devices and usage | Device MAC and IP address, session times, data volumes, the network used | Anonymised after 365 days (30–3,650). Usage detail deleted after 365 days (30–3,650, or unlimited) |
| Sign-in attempts | Room typed, device MAC and IP, and (sealed) the name and reservation number typed | Deleted after 30 days (7–365) |
| One-time codes and links | Destination email or phone, hashed code, IP and browser user agent | Anonymised 7 days after expiry (1–365) |
| Payments | Provider references and amounts. **No card data**: guests pay on the provider's own page | Kept for the statutory period. Not removed automatically |
| Privacy notice record | Which version of the Controller's privacy notice was in force at each sign-in, and when | Follows the sign-in record |
| Staff accounts | Email, display name, password hash, sealed authenticator secret, role, sign-in history | While the account exists |
| Audit log | Staff member, action, time, IP and user agent, and the record acted on | IP, user agent and record anonymised after 730 days (90–3,650) |
| Logs and backups | Device MAC and IP addresses and sign-in identifiers in system logs; a full copy of the database in backups | Backups: 30 days, with the newest 7 always kept. Logs: see Annex II, Part C |
