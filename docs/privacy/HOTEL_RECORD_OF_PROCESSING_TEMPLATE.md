# Your record of processing: guest Wi-Fi (OneGate)

> **DRAFT** for the D-F3 legal review. Not for a customer yet. Every pre-filled fact was checked against the
> OneGate code on `delivery/gdpr` (2026-10-19). This is a template, not legal advice: the hotel's own adviser
> should look at the finished record.

## Read this first

**What this is.** The law (GDPR Article 30) asks your hotel to keep a written list of the personal data it
handles. This document is the part of that list that covers your guest Wi-Fi. Add it to the records you already
keep for your other systems (reservations, CCTV, HR and so on).

**Who fills it in.** Someone at the hotel who can open Hotel Admin: usually the IT manager or the site
administrator. It takes about 15 minutes.

**What is already done.** We know exactly what OneGate records, why, and for how long, so most of the record is
already written. You only answer **four short steps** about your own hotel. The record in Part 2 then refers back
to your answers, so you never write the same thing twice.

**How to read the marks:**

| Mark | Means |
|---|---|
| ✏️ | Write your answer here |
| ☐ | Tick the ones that apply (change `[ ]` to `[x]`, or tick on paper) |
| 🔍 | Where to find the answer in Hotel Admin |

Everything without a mark is already correct for OneGate. Leave it as it is, unless your setup really differs.

---

## Part 1: Your four steps

### Step 1: About your hotel

| | Your answer |
|---|---|
| Legal name of the hotel company | ✏️ |
| Address | ✏️ |
| Who to contact about privacy (name or role, and email) | ✏️ |
| Your data protection officer, if you have one | ✏️ *or write "none"* |
| Your representative in the EU or UK, only if your company is based outside it | ✏️ *or write "not needed"* |
| Date you filled this in | ✏️ |

### Step 2: What you have switched on

Tick each feature you use. 🔍 The page that shows it is in brackets.

**How guests sign in** 🔍 *(Client access → Sign-in methods)*

- [ ] Room number and surname (or another detail), checked against your reservations
- [ ] Voucher code
- [ ] Wi-Fi account (username and password)
- [ ] Email code or link
- [ ] Text message (SMS) code
- [ ] WhatsApp code
- [ ] Google, Apple, Facebook or Microsoft account 🔍 *(Identity providers)*

**Your reservation system (PMS)** 🔍 *(PMS connection)*

- [ ] Connected. Which one: ✏️ *(for example Protel)*
- [ ] Internet charges are posted to the guest's room bill 🔍 *(Room charge)*
- [ ] Not connected

**Paid internet** 🔍 *(Payment methods)*

- [ ] Card payments through Stripe
- [ ] Card payments through Paymob
- [ ] No card payments

**Companies that send your codes** 🔍 *(Delivery)*

- [ ] SendGrid (email)
- [ ] Your own email server. Whose: ✏️
- [ ] Twilio (SMS or WhatsApp)
- [ ] Meta (WhatsApp)

**Backups** 🔍 *(Backups)*

- [ ] Backups are copied off the appliance as well. Where to: ✏️
- [ ] Backups stay on the appliance only

### Step 3: How long you keep things

🔍 *Backups → Guest data kept for*, *Backups → Usage detail kept for*, and *Operators → Disabled accounts*. Copy
each number from the screen. If you never changed it, the default is already right, and you can tick the box
instead.

- [ ] I have not changed any of these: we use the defaults.

| What | Default | Your setting (days) |
|---|---|---|
| Reservation details, after checkout | 30 days | ✏️ |
| Devices and sessions, after last use | 365 days | ✏️ |
| Guest accounts, emails and phone numbers, after last use | 365 days | ✏️ |
| Used or expired codes and links, after expiry | 7 days | ✏️ |
| Failed and successful sign-in attempts | 30 days | ✏️ |
| Usage detail (how much data each session used, sample by sample) | 365 days | ✏️ |
| Staff activity log: IP address, browser and record details anonymised after | 730 days (2 years) | ✏️ |
| Disabled staff accounts: email, name and password removed after | 730 days (2 years) | ✏️ |
| Backups | 30 days; the newest 7 are kept whatever their age, and a backup you pin is kept until you unpin it | ✏️ |
| System logs (web, address and service logs) | 14 days | ✏️ |
| Staff notes (PMS case decisions, released devices, revoked post-stay access) removed after | 730 days (2 years) | ✏️ |

### Step 4: Does a law make you keep connection records?

Some countries require businesses that offer Wi-Fi to keep connection records for a set time. If yours does, set
the system logs period in Step 3 to match.

- [ ] No, not that we know of.
- [ ] Yes. The law: ✏️ How long it requires: ✏️

---

## Part 2: The record (GDPR Article 30(1))

### (a) Who is responsible

The **controller** is the hotel in Step 1, with its contact, data protection officer and representative.

OneGate runs on an appliance at the hotel, operated by the hotel's staff. Semantics Technology Ltd, which supplies
OneGate, handles the data only when the hotel asks for installation or support, or sends it files. It does this
as the hotel's **processor**, under the Data Processing Agreement.

### (b) Why the data is used, and (c) whose data and what data

Leave out any row that depends on a feature you did not tick in Step 2.

| # | Activity | Why (lawful basis) | Whose data | What data |
|---|---|---|---|---|
| 1 | **Guest Wi-Fi access** | To provide the Wi-Fi the guest asked for: contract (Art. 6(1)(b)) | Guests and visitors who connect | Device network addresses (MAC and IP), connection times, how much data each session used, and, while connected, the name the device gives itself on the network (often its owner's name, such as "Anna's iPhone"). **Not** the websites visited: OneGate does not record them, and its request logs name the page type served, never the address asked for |
| 2 | **Signing guests in** | Contract (Art. 6(1)(b)) | As above | Only for the methods ticked in Step 2: room and the detail typed with it; voucher code; Wi-Fi username and password (stored hashed); email address; phone number; for Google, Apple, Facebook or Microsoft, the account id and email, and for a business account the organisation's ids; for post-stay access, a PIN (stored hashed) and its validity |
| 3 | **Checking room sign-ins against reservations** *(only if a PMS is connected)* | Contract (Art. 6(1)(b)); for room sharers, legitimate interest (Art. 6(1)(f)) in letting everyone in the room connect | Everyone named on a reservation, **including room sharers who may never use the Wi-Fi** | Reservation number, room, arrival and departure, names, VIP flag, travel agent, room type, rate plan, the PMS guest id. **No** passports, ID documents or home addresses |
| 4 | **Selling internet packages** *(only if card payments or room charge are on)* | Contract (Art. 6(1)(b)); keeping payment records, legal obligation (Art. 6(1)(c)) | Guests who buy a package | Package, price, payment reference. Card details go to Stripe or Paymob directly, never to the hotel. For room charge: the room, reservation number, PMS guest number and amount posted to the bill. These stay with the financial record for the statutory period, also after the reservation itself is anonymised: they are pseudonymous, not anonymous |
| 5 | **Keeping the network secure** | Legitimate interest (Art. 6(1)(f)) in stopping misuse; legal obligation (Art. 6(1)(c)) if you ticked "Yes" in Step 4 | Guests and anyone who tries to sign in | Sign-in attempts, including failed ones (the room typed, device addresses; what was typed with it is stored sealed). System logs with device addresses and sign-in names |
| 6 | **Staff accounts and the activity log** | Legitimate interest (Art. 6(1)(f)) in security and accountability; for employees, also the employment contract | Hotel staff who use Hotel Admin | Email, name, password (hashed), authenticator secret (sealed), role, sign-ins. Log of what each staff member did, including **when they opened a guest's record or searched guest data** (opening the same record again within 10 minutes is recorded once), with time, IP address and browser |
| 7 | **Showing the privacy notice** | Legal obligation (Art. 6(1)(c)) to inform guests (Art. 13), and to be able to show they were told | Guests who sign in | Which version of your privacy notice was shown at each sign-in, and when |
| 8 | **Answering guest requests** (copy, deletion, correction) | Legal obligation (Art. 6(1)(c)) | Guests who ask | The request, a reference, and who handled it. For an *assisted request*, while it is open (at most 30 days): the requester's name or reference, how they were verified, a note of the evidence, and the device addresses concerned. Closing it removes them and the link to the guest's records; what stays is who, when, how it was verified, and how many records |
| 9 | **Backups** | Legitimate interest (Art. 6(1)(f)) in recovering from a fault | Everyone above | A full copy of the database, encrypted to your own key holders |

**Special categories** (health, religion and the like, Art. 9) and **criminal records** (Art. 10): none. OneGate
does not ask for them. Staff should not type them into free-text notes.

**Consent** is not used. Guests are not asked to tick a box to get online. They are shown your privacy notice
instead, and row 7 records that they saw it. If you ever use guest details for marketing, that needs consent and
a new row here.

### (d) Who receives the data

| Who | Receives | When |
|---|---|---|
| Semantics Technology Ltd (OneGate supplier), United Kingdom | Whatever is on the appliance, during installation or a support session you open, and files you choose to send | Only when you ask. Your processor, under the Data Processing Agreement |
| The code-sending companies you ticked in Step 2 | The guest's email address or phone number, and the code | Each time a code is sent |
| The sign-in companies you ticked (Google, Apple, Facebook, Microsoft) | They learn that the guest signed in here. The guest signs in on their page | Each social sign-in |
| The payment companies you ticked (Stripe, Paymob) | Amount, package, a payment reference. **No** guest name or room. The guest types card details on their page | Each card payment |
| Your PMS supplier, if connected | Room, reservation number and amount of each internet charge | Each room charge |
| Backup location, if you ticked one in Step 2 | Encrypted backups | Nightly |
| Authorities | Only when the law requires it | On a lawful request |

### (e) Data sent outside the UK and EEA

Leave out the companies you did not tick in Step 2. Check each company's current safeguard on its own website or
in your contract with it; it can change.

| Company | Country | Safeguard: tick one |
|---|---|---|
| Semantics Technology Ltd | United Kingdom | Covered by the EU adequacy decision for the UK. Nothing to do for an EEA hotel |
| SendGrid / Twilio | United States | [ ] EU–US Data Privacy Framework  [ ] Standard contractual clauses in our contract  [ ] Still to check |
| Meta (WhatsApp, Facebook) | United States | [ ] EU–US Data Privacy Framework  [ ] Standard contractual clauses  [ ] Still to check |
| Google, Apple, Microsoft | United States | [ ] EU–US Data Privacy Framework  [ ] Standard contractual clauses  [ ] Still to check |
| Stripe | United States | [ ] EU–US Data Privacy Framework  [ ] Standard contractual clauses  [ ] Still to check |
| Paymob | Egypt | [ ] Standard contractual clauses  [ ] Still to check *(Egypt has no EU adequacy decision)* |
| Your backup location, if outside the UK or EEA | ✏️ | ✏️ |

For a UK hotel, read "UK" for "EEA", and use the UK's own extension to the Data Privacy Framework or the UK
addendum to the clauses.

### (f) How long data is kept

As in your answers to **Step 3**. When a period ends, OneGate deletes the record, or makes it anonymous by
removing everything that identifies the person. It checks every hour.

Two more points:

- **Backups** keep their copy for the period in Step 3. Data deleted in the live system can stay in a backup
  until that backup expires. Your privacy notice should say so.
- **Financial records** (payments, refunds, room charges) are kept for the period the law sets, and are not
  removed automatically.

### (g) How the data is protected

OneGate's built-in protections are described in **Annex II, Part B** of your Data Processing Agreement with us.
In short: staff sign in with a password and an authenticator code; each role sees only what it needs; every look
at guest data is logged; passwords and secrets are stored hashed or sealed; backups are encrypted; and guests
cannot reach the management network or each other.

Some protections are yours to switch on or look after. Tick each one you have done:

- [ ] At least one **backup key holder** is registered 🔍 *(Backups)*. Until then, backups are not encrypted.
- [ ] **SSH** on the appliance is limited to trusted addresses, or switched off. It is open on the management
      network by default.
- [ ] The appliance is in a **locked room or cabinet**. Its disk is not encrypted.
- [ ] Every staff member has **their own account**. No shared logins. Leavers are removed.
- [ ] The **PMS link is encrypted** 🔍 *(PMS connection → Encrypt the connection (TLS); offered for Protel)*,
      or it runs on a network only the hotel uses.
- [ ] Your **privacy notice** is completed and published 🔍 *(Portal settings → Content → Privacy notice)*.
- [ ] Someone knows how to answer a guest request 🔍 *(Guest data requests)*: find the guest, send a copy, or
      delete. A request about **shared** internet access (a room's use, a device used with others) uses the
      *Assisted request* for what the tool can establish as the requester's, is answered by hand for the rest, and is
      never answered by waiting for the retention period.

---

## Keep it up to date

Look at this record again:

- once a year;
- when you switch a feature on or off in Step 2;
- when you change a period in Step 3;
- when you change a code-sending, sign-in or payment company.

Give it to your data protection authority if they ask for it.
