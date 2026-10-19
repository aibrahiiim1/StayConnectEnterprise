# Your risk assessment (DPIA): guest Wi-Fi (OneGate)

> **DRAFT** for the D-F3 legal review. Not for a customer yet. Every pre-filled fact was checked against the
> OneGate code on `delivery/gdpr` (2026-10-19). This is a starting point, not legal advice: the hotel decides,
> with its own adviser or data protection officer.

## Read this first

**What this is.** A data protection impact assessment (DPIA, GDPR Article 35) is a short written check: what
could go wrong for your guests because of how you use their data, what stops it, and whether what is left is
acceptable.

**Why you probably need one.** Regulators expect one when two or more of these apply. For guest Wi-Fi, three
usually do:

- **People's behaviour is monitored**: when each device connected, for how long, and how much data it used.
- **Data from two sources is matched**: your reservation list is matched with what guests type to sign in.
- **Guests have no real alternative** at a hotel, and cannot easily see what happens to their data.

Even where it is not strictly required, writing it down is quick and shows you took care.

**What is already done.** Part 2 lists the realistic risks, what OneGate already does about each one, and what is
left for you. For each risk you only **tick what you have done** and **choose the result**. It takes about
20 minutes. Fill in your [record of processing](HOTEL_RECORD_OF_PROCESSING_TEMPLATE.md) first: this assessment
uses your answers from it.

**How to read the marks:**

| Mark | Means |
|---|---|
| ✏️ | Write your answer here |
| ☐ | Tick the ones that apply (change `[ ]` to `[x]`, or tick on paper) |
| ⭐ | Our suggested answer, if you have done every tick in that risk. Change it if you disagree |

---

## Part 1: What the processing is

Most of this is already in your record of processing, so it is not repeated here.

| | |
|---|---|
| Hotel | ✏️ *(as in Step 1 of your record)* |
| What is assessed | Guest Wi-Fi with OneGate: signing guests in, giving them access, keeping the network secure, and the activities in Part 2(b) of your record |
| Features in use | As ticked in Step 2 of your record |
| How long data is kept | As in Step 3 of your record |
| Roughly how many guests a year | ✏️ |
| Who filled this in | ✏️ |
| Date | ✏️ |

**What OneGate does not do**, which keeps the risk down:

- It does **not** record which websites guests visit. It records only how much data each session used.
- It does **not** collect passports, ID documents or home addresses from your PMS.
- It does **not** receive card details. Guests pay on the payment company's own page.
- It does **not** send guest data to its supplier. The appliance at your hotel talks to Semantics Technology Ltd
  only about its licence.
- It does **not** ask guests to agree to marketing, or use their data for it.

---

## Part 2: The risks

Each risk shows what OneGate already does and what is yours. Tick what you have done, then choose the result.

> **Choosing the result.** *Low*: unlikely, and little harm if it happened. *Medium*: possible, or real harm if it
> happened. *High*: likely, and serious harm. A risk you leave at High must be discussed with your data protection
> authority before you go on (Art. 36).

### R1. A staff member looks up a guest without a good reason

*Could happen if:* someone at the hotel is curious about a particular guest (when they connected, which devices
they used, their email or phone number).

**OneGate already:** gives each role only the screens it needs; logs when a staff member opens a guest's record
(once per 10 minutes for the same record) or searches guest data, with who, when and from where (🔍 *Activity*, "Client data"); asks for a reason and
the password again before exporting or deleting a guest's data.

**Yours:**

- [ ] Each staff member has their own account, with the smallest role that fits their job.
- [ ] Staff are told that looking up guests is logged, and only allowed for their work.
- [ ] Someone looks at the "Client data" entries in Activity now and then. How often: ✏️

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R2. Someone breaks into the appliance

*Could happen if:* an attacker on your network, or a guest, tries to get into the system.

**OneGate already:** keeps guests off the management network and away from each other; serves Hotel Admin only on
the management network; requires a password **and** an authenticator code from every staff member; locks an
account after repeated wrong passwords; runs each service with only the rights it needs.

**Yours:**

- [ ] **SSH** is limited to trusted addresses, or switched off. *It is open on the management network by
      default.*
- [ ] Only trusted devices are on the management network.
- [ ] Any login given for a support session is removed afterwards.
- [ ] Software updates are installed when offered.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R3. The appliance, or its disk, is stolen

*Could happen if:* someone takes the appliance or its disk from the building.

**OneGate already:** stores passwords hashed, and seals provider passwords, authenticator secrets and what guests
type at sign-in. *But the disk itself is not encrypted*, so names, emails, phone numbers, reservations and device
addresses on it could be read.

**Yours:**

- [ ] The appliance is in a locked room or cabinet that only authorised staff can open.
- [ ] Access to that room is controlled (key log, card access or similar).

**Result:** [ ] Low  [ ] Medium ⭐  [ ] High
*(Medium because the disk is not encrypted. If your room is well controlled, Low can be justified: say why.)*
✏️ Why you chose this:

### R4. A backup gets into the wrong hands

*Could happen if:* a backup file is copied, downloaded or sent somewhere it should not be.

**OneGate already:** encrypts every backup to your own key holders, so the appliance cannot read its own backups
and neither can we; keeps backups 30 days; logs every backup download; reminds you every 6 months to test that a
key holder can still restore one.

**Yours:**

- [ ] At least one **backup key holder** is registered 🔍 *(Backups)*. *Until then, backups are not encrypted.*
- [ ] Key holders keep their key safe (a password manager or a locked safe), and there are at least two.
- [ ] The restore test is done when reminded.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R5. Data is kept longer than needed

*Could happen if:* old reservations, devices and sign-ins pile up for years.

**OneGate already:** deletes or anonymises every kind of guest data at the end of its period, checking every hour.
Defaults: reservations 30 days after checkout, devices and accounts 365 days after last use, system logs 14 days.

**Yours:**

- [ ] The periods in Step 3 of your record are the shortest that work for the hotel.
- [ ] Any period longer than the default has a written reason. ✏️ Reason:
- [ ] Your privacy notice tells guests the same periods.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R6. Guests do not know what happens to their data

*Could happen if:* the sign-in page does not explain it, or the explanation is wrong.

**OneGate already:** ships a privacy notice template that describes exactly what it records; shows the notice on
every sign-in page, before the guest does anything; records which version each guest was shown.

**Yours:**

- [ ] The privacy notice is completed and published 🔍 *(Portal settings → Content → Privacy notice)*, with no
      `[[...]]` left.
- [ ] It names the code-sending, sign-in and payment companies you use.
- [ ] It mentions that deleted data can remain in backups for up to 30 days.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R7. People who never used the Wi-Fi are in the system

*Could happen if:* your PMS is connected. OneGate copies the names on each reservation to check room sign-ins, so
room sharers and other people on the booking are held even if they never connect.

**OneGate already:** takes only what sign-in needs from the PMS (no documents, no addresses); anonymises
reservation data 30 days after checkout by default.

**Yours:**

- [ ] The reservation period in Step 3 is as short as you can make it.
- [ ] Your privacy notice, or your hotel's main privacy notice, tells all guests on a booking about this.
- [ ] No PMS is connected, so this risk does not apply.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R8. Outside companies receive more than they need

*Could happen if:* a code-sending, sign-in or payment company uses guest data for its own ends, or keeps it in a
country with weaker protection.

**OneGate already:** sends each company only what its job needs (an email or phone number to send a code; an
amount and a reference to take a payment, with no guest name or room); switches off email click and open
tracking; asks Google, Facebook and Apple for the email address only, and warns you if you ask for more.

**Yours:**

- [ ] You have a contract or terms with each company you use, covering data protection.
- [ ] Every company outside the UK and EEA has a safeguard ticked in part (e) of your record, none left at "Still
      to check".

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R9. The link to your reservation system is listened to

*Could happen if:* someone on the network between the appliance and the PMS reads the reservation data passing
by.

**OneGate already:** can encrypt the Protel link and check the PMS's certificate, if your PMS supports it. *It is
off by default.* Cloud PMS links (Apaleo, Mews, Opera Cloud) are always encrypted.

**Yours:**

- [ ] Encryption is switched on 🔍 *(PMS connection → TLS)*.
- [ ] Or: the link runs on a network only the hotel's own systems use.
- [ ] No PMS is connected, so this risk does not apply.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R10. A guest asks for their data, or for it to be deleted, and cannot get it

*Could happen if:* nobody at the hotel knows how to answer, or the one-month legal deadline is missed.

**OneGate already:** finds one guest's records across the system, exports them as a file, and deletes them by
making them anonymous (🔍 *Guest data requests*). Each request is logged with a reference to give the guest.

**Yours:**

- [ ] Reception knows where to send a request.
- [ ] At least two people can use Guest data requests.
- [ ] Requests are answered within one month.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### R11. The supplier sees guest data during support

*Could happen if:* Semantics Technology Ltd is given access to fix a problem.

**OneGate already:** has no standing access for its supplier. Access exists only when you open it, for a stated
reason, and is recorded in the support ticket. The Data Processing Agreement binds the supplier to
confidentiality, to tell you of a breach within 24 hours, and to delete any files you sent when the case closes.

**Yours:**

- [ ] The Data Processing Agreement with Semantics Technology Ltd is signed.
- [ ] You remove any login given for a support session when it ends.
- [ ] You send the smallest extract that answers the question, not a whole backup.

**Result:** [ ] Low ⭐  [ ] Medium  [ ] High

### Any other risk

✏️ Something specific to your hotel (for example, Wi-Fi in a hospital wing, or many child guests):

**Result:** [ ] Low  [ ] Medium  [ ] High

---

## Part 3: Is it necessary and proportionate?

Already answered for OneGate. Tick if it is true for your hotel.

- [ ] **A clear purpose:** Wi-Fi for guests and keeping it secure. Nothing else, such as marketing or profiling.
- [ ] **A lawful basis for each use:** as in part (b) of your record. No consent needed, because access is part of
      the stay.
- [ ] **Only what is needed:** no websites visited, no documents, no card details.
- [ ] **Kept no longer than needed:** the periods in Step 3, applied automatically.
- [ ] **Guests are told:** the privacy notice on the sign-in page.
- [ ] **Guests can use their rights:** Guest data requests (R10).
- [ ] **Processors are under contract:** the supplier (R11) and the outside companies (R8).
- [ ] **Transfers have safeguards:** part (e) of your record.

---

## Part 4: Conclusion and sign-off

| | |
|---|---|
| Risks left at **High** | ✏️ *list them, or write "none"* |
| Actions still to do, with who and by when | ✏️ |
| Advice of your data protection officer, if you have one | ✏️ |
| **Decision** | [ ] Go ahead  [ ] Go ahead once the actions above are done  [ ] Consult the data protection authority first (any High risk left) |
| Approved by (name, role) | ✏️ |
| Date | ✏️ |
| Next review | ✏️ *(one year from today, or sooner if you switch a feature on or off)* |

---

<sub>For OneGate's own detail behind each answer, see the Data Processing Agreement: Annex I (the data and its
default periods) and Annex II (security measures, and Part C, what stays with the hotel).</sub>
