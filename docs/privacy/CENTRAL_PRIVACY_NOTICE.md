# Privacy notice: OneGate Central

> **DRAFT** for the D-F3 legal review. It is not to be published before that. Every fact was checked against the
> code on `delivery/gdpr` (2026-10-19). `[[placeholders]]` are facts the code cannot give. The DPA (clause 2.3)
> links to this notice. Once approved, publish it where Central's users can read it before signing in, for
> example a link on the sign-in page.

This notice is for **anyone who has an account on OneGate Central** (`https://central.onegate.getsemantics.com`):
our own staff, and the staff of hotels and other customers who use Central to manage their OneGate appliances and
licences. It explains what we record
about you, why, and for how long.

It does **not** cover hotel guests. Central holds no guest data: a hotel's guest Wi-Fi data stays on the
appliance at the hotel, and the hotel's own privacy notice covers it.

## Who we are

**Semantics Technology Ltd** is responsible for this data (the "controller").

- Registered office: 124 City Road, London EC1V 2NX, United Kingdom. Company number [[ ]].
- Privacy contact: [[privacy contact email]].
- Data protection officer: [[name and contact, or "we have not appointed one"]].
- Representative in the EU: [[legal review: name and contact, or delete this line]].

## What we record, and why

| What | Details | Why | Lawful basis |
|---|---|---|---|
| **Your account** | Email address, name, role, whether the account is active, and when you last signed in. Your password is stored only as a one-way hash (argon2id). The secret behind your authenticator app is stored encrypted, and recovery codes only as hashes | To give you access to Central with the rights your role allows | Our staff: the employment contract. Customers' staff: our legitimate interest in providing the service your employer has contracted (Art. 6(1)(f)) |
| **Your sign-in session** | Your account, roles, and when the session started and was last used. Not your IP address or browser | To keep you signed in, and to sign you out when the session should end | Legitimate interest: security (Art. 6(1)(f)) |
| **The audit log** | What you did in Central and when, with your IP address and browser. A failed sign-in to an existing account is recorded against that account; for a name that has no account, only the attempt and its IP address are recorded, not what was typed | To protect Central, investigate misuse, and show who made each change | Legitimate interest: security and accountability (Art. 6(1)(f)) |
| **Wrong-password counting** | A one-way hash of the name you typed and how many wrong passwords were tried, to lock the account for a while after repeated failures. It never holds the email address itself | To stop password guessing | Legitimate interest: security (Art. 6(1)(f)) |
| **Your name in appliance history and alerts** | When you activate, retire or replace an appliance, or handle a security alert, the history records your email and IP address | To keep a reliable history of each appliance and licence | Legitimate interest: security and accountability (Art. 6(1)(f)) |
| **The web server's access log** | The time, the page or service asked for (without anything after `?`), the answer, and your IP address. Not your email address, what you typed or your browser's headers | To keep Central running and find faults and attacks | Legitimate interest: security (Art. 6(1)(f)) |
| **Certificate contact** *(only if you are the one we name)* | One contact email for all of Central, registered with Let's Encrypt for the guest-portal certificates, normally one of ours. A change to it is recorded in the history with the old and new address | To obtain and renew the certificates | Legitimate interest in running the service (Art. 6(1)(f)) |

You must give an email address and set a password to have an account. Without them we cannot give you
access. An authenticator is optional and recommended.

We do not use your data for marketing, sell it, or make decisions about you by automated means. An account that
receives repeated wrong passwords is locked automatically for a short time (15 minutes by default). The lock ends
by itself and decides nothing else about you.

## Who receives it

- **Our staff** who run Central.
- **People at your own organisation with a Central account**, depending on their role: administrators, and also
  auditor and viewer roles, can see the organisation's accounts with their last sign-in and its audit log,
  including IP addresses.
- **For our own staff:** when you act on a customer's records, that customer's accounts can see in its history
  and audit log that you did, with your email and IP address.
- **Our hosting provider:** [[Central hosting provider, not decided yet: name and country]], which runs the
  servers Central's database is on, under a contract that allows it to use the data only to provide that
  service.
- **Let's Encrypt** (Internet Security Research Group, United States) receives the certificate contact email and the guest portal's host names. Host names are published in public certificate transparency
  logs. **Cloudflare** (United States) receives the guest portal's host names for DNS. Neither receives your
  account details.
- **Authorities**, only when the law requires it.

## Data sent outside the UK

- **Hosting:** [[none, if Central is hosted in the UK; otherwise the country and the safeguard]].
- **Let's Encrypt and Cloudflare** are in the United States. Only host names and the certificate contact email
  go there. [[Safeguard: each company's certification under the UK extension to the EU–US Data Privacy
  Framework, or the UK International Data Transfer Agreement. Legal review to confirm for each.]]

## How long we keep it

The main periods are settings in Central (System → Team → Data retention); the session limits, the five-minute sign-in step, the two-day counter and the 14-day access log are fixed. These are the defaults:

| What | How long |
|---|---|
| Your account, while you use it | As long as the account is active |
| Your account, after it is disabled | 24 months. Then your email, name, password and authenticator are removed for good |
| Your sign-in session | Ends after 30 minutes without activity, and 12 hours after you signed in at most. A half-finished sign-in ends after 5 minutes |
| The audit log | 24 months |
| The web server's access log | 14 days |
| Wrong-password counts | Removed within an hour once 2 days have passed without a wrong password |
| Your name and IP address in appliance history, appliance moves, certificate requests and alerts | 24 months. The history itself is kept 6 years without them |
| Backups of Central's database | The newest 7 nightly backups, encrypted once a key holder is registered. Data removed from Central stays in a backup until that backup is replaced |

## Cookies and your browser

Central uses only what it needs to work:

- `sc_signin`: holds a half-finished sign-in between your password and your authenticator code, for 5 minutes;
- `sc_session`: keeps you signed in;
- the console remembers in your browser whether you collapsed its side menu.

There is no tracking or analytics, and the console loads nothing from other websites.

## How we protect it

Every account signs in with a password; an account that turns on two-step sign-in (recommended) also needs an authenticator code. Sessions end on their own, and some actions
ask for your password again. Each role sees only what it needs. Central is reached only over HTTPS, and
appliances connect with their own certificates. Secrets are encrypted, and database backups are encrypted to our
own key holders once one is registered.

## Your rights

You can ask us:

- for a copy of your data;
- to correct it;
- to delete it, or limit how we use it;
- to give it to you in a portable form, where the law allows;
- to stop using it, where we rely on our legitimate interest.

Write to [[privacy contact email]]. We answer within one month. If your account belongs to a customer, you can
also ask your organisation's administrator, who can correct your email or disable your account.

When your account is removed, or anonymised after it has been disabled for the set period, your email is
replaced in our records by "a removed user". What was done, and when, stays.

You can complain to the **Information Commissioner's Office** (ico.org.uk). If you are in the EU, you can also
complain to the data protection authority in your country.

## Changes

We will update this notice when what we record changes, and show the date below.

Last updated: [[date]]
