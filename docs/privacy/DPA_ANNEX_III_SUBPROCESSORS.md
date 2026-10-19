# Annex III: Sub-processors

> **DRAFT**, part of [DPA_TEMPLATE.md](DPA_TEMPLATE.md). It is not for use with a customer before the D-F3 legal
> review. Part B was checked against the code on `delivery/gdpr` (2026-10-18).

## Part A: Sub-processors of the Processor (DPA clause 6)

Under the DPA the Processor handles personal data only in support sessions, during installation, and in Customer
Files (DPA clause 2.2). Its sub-processors are therefore the services those activities pass through. The code does
not reveal which services these are. **The Product Owner must fill in every row before signature, or delete
it.** A row that stays "none" means the Processor commits not to use such a service.

| Service | Purpose | Personal data | Location | Transfer safeguard |
|---|---|---|---|---|
| [[Helpdesk / ticketing system]] | Support case records, including attached Customer Files | Whatever the Controller attaches: log extracts, screenshots, exports | [[ ]] | [[ ]] |
| [[Email provider]] | Receiving Customer Files and support correspondence | As above, plus staff names and email addresses | [[ ]] | [[ ]] |
| [[File transfer / storage]] | Receiving large Customer Files (backups, database extracts) | As above | [[ ]] | [[ ]] |
| [[Remote-session tool, if used]] | Screen sharing during a support session | What is on screen | [[ ]] | [[ ]] |

The Software itself uses no Processor-side service to process guest data.

## Part B: The Controller's own providers (not Sub-processors; DPA clause 7.6)

The Software connects to these services only when the Controller configures them, with the Controller's own
account and credentials, which are stored sealed on the Appliance. Each is the **Controller's** processor, or an
independent controller, under the Controller's own agreement with it. The Processor has no account with them for
this purpose and receives nothing from them.

| Provider | Used for | What the Appliance sends |
|---|---|---|
| SendGrid, or any SMTP server | Email codes, verification and password-reset links | The guest's email address and the code or link. Click and open tracking is switched off |
| Twilio | SMS codes, WhatsApp codes | The guest's phone number and the message text |
| Meta (WhatsApp Cloud API) | WhatsApp codes | The guest's phone number and the message text |
| Google, Facebook (Meta), Microsoft, Apple | Social sign-in | The guest authenticates with the provider directly. The Appliance receives identity claims and keeps only the subject id, the verified email and, for business accounts, the organisation identifiers |
| Stripe | Card payment for internet packages | Amount, currency, product name, a payment reference and return addresses. No guest identifier. The guest enters card details on Stripe's page |
| Paymob (Egypt, Saudi Arabia, UAE, Oman) | Card payment | As for Stripe. The billing fields are sent as "NA" |
| The PMS: Protel / Opera (FIAS), Apaleo, Mews, Opera Cloud | Reservation data in. Internet charges out | In: names, room, dates, reservation. Out: room, reservation number and amount of a charge |

Some of these providers are outside the EEA. Any transfer to them is made by the Controller under its own
agreement with the provider. The Controller's privacy notice should name them.

## Part C: Services used by the Processor as controller (information only)

These are not Sub-processors under the DPA. They support the Processor's own processing for Central (DPA
clause 2.3), and are listed here for transparency. No guest data reaches them.

| Service | Purpose | Data |
|---|---|---|
| Cloudflare (DNS API, and DNS-over-HTTPS together with Google Public DNS) | Publishing and checking the guest-portal host names for certificate issuance | Portal host names, the Appliance's **private** portal address, verification records |
| Let's Encrypt (ISRG, United States) | Issuing the guest-portal certificate | Portal host names (published in public Certificate Transparency logs) and the contact email set in Central |
| [[Central hosting provider: not decided yet. Name, country, and the transfer safeguard if outside the UK and EEA]] | Running Central | Central's database: staff accounts, Appliance identities, licences, audit log |
