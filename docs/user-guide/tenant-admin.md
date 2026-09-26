# Customer Admin (Tenant Admin) — User Guide

You look after OneGate for your organisation (a hotel, a chain, a group of properties) in **OneGate Central**. In Central you manage your customer's sites, appliances and Central operators, and you can read your licenses. The hotels' day-to-day setup — guest networks, sign-in methods, packages, vouchers, the PMS — is done in **OneGate Hotel Admin** on each appliance, with an operator account created on that appliance.

You can only see your own customer. You **cannot** see or change any other customer. Central's server decides exactly which actions your role may perform; if an action is not allowed, Central says so when you try it.

## First-time setup (new customer checklist)

Do these in order the first time you sign in:

1. **Change your initial password** — ask the platform admin to reset it if you did not choose it yourself.
2. **Check your sites** (one per property). See [Managing sites](#managing-sites).
3. **Check your appliances** are activated and online. See [Enrolling appliances](#enrolling-appliances).
4. **Sign in to Hotel Admin** on each appliance and **pick the sign-in methods** — room sign-in (PMS), vouchers, guest accounts, email/SMS codes, social login. See [Authentication setup](#authentication-setup).
5. **Check Allowed sites** in Hotel Admin — only what the sign-in page needs before guests sign in.
6. **Create the rest of your staff** — see [Managing operators](#managing-operators).
7. **Test a guest connection** at one site before opening to real guests.

## Managing sites

**Central → Infrastructure → Sites**

A site is one physical property. If you have three hotels, you have three sites. If one hotel has two buildings that share the same internet uplink, that's still one site.

- **New site**: code, name, timezone (defaults to UTC), country.
- **Edit**: change name, timezone or country. The code cannot change.
- **Archive / Restore**: hide a site without deleting it.
- **Delete**: only works when nothing is left under it; the dialog lists what blocks it and asks you to type the site code and a reason.

## Enrolling appliances

**Central → Infrastructure → Appliances** and **Onboarding**

An appliance is the OneGate gateway (physical or virtual) at a site. New appliances register themselves and are activated on **Onboarding** — usually by the platform admin, who also sets the license terms.

**What the appliance status column means:**

- **Online** — heard from recently (a pulsing green dot).
- **Enrolled / pending** — registered, not yet fully active.
- **Offline** or other — not heard from. Either the appliance is down or the site lost its uplink. Ask someone at the property to check. The appliance keeps serving guests without Central; Central is used for licensing only.

For an appliance that cannot register itself, **Appliances → Enrollment token** creates a token (shown once) for the installer to enter in Hotel Admin under **Appliance & licence → Advanced / recovery**.

**Moving an appliance** between sites changes its signed assignment and license — ask the platform admin.

## Authentication setup

How guests sign in is configured **on each appliance, in Hotel Admin**, not in Central. What a guest sees is described in [guest-portal.md](guest-portal.md); the pages are:

### Vouchers

**Hotel Admin → Internet offering → Vouchers**

Printed cards with a code, each giving an internet package. **Issue vouchers** (1–500 cards per batch) shows the codes once to copy, download or print. Cancelling a lost card, or reading a code again, asks for a reason and the operator's password.

### PMS (room + name)

**Hotel Admin → Property management system → PMS connection** and **Network routing**

Guests sign in with their room number plus their name or reservation number, checked against the appliance's copy of the PMS guest list. Set up by the hotel's Site admin or Hotel IT manager.

### Email / SMS codes

**Hotel Admin → Guest portal → Email & SMS**

A sender (SendGrid, Amazon SES or Twilio) delivers one-time codes. Then switch **Email code** / **SMS code** on under **Sign-in methods**.

### Social login

**Hotel Admin → Guest portal → Social login**

1. Create an OAuth app with Google / Apple / Facebook / Microsoft (outside OneGate — follow their docs).
2. Enter the client ID and secret in Social login.
3. Tick the provider under **Sign-in methods**.

### Paid Wi-Fi

Internet packages are free to guests today; selling internet is not switched on. The **Charges** pages in Hotel Admin show *Not enabled on this appliance* until it is.

## Managing operators

**Central → Administration → Operators**

These are Central logins for your organisation. Hotel staff who run an appliance day to day get an operator account **on that appliance** instead (**Hotel Admin → System → Operators**, created by its Site admin).

### Roles explained

- **Customer admin** — looks after your customer in Central: sites, appliances, operators; reads licenses. Give sparingly.
- **Customer operator** — day-to-day Central work for your customer.
- **Viewer** — meant for looking, not changing: no licenses, operators or activation. Note that Central's server currently lets every customer role, Viewer included, manage Sites and Appliances, so give it knowingly.
- **Billing** — a legacy role that is no longer granted. Operators who already hold it can read your licenses.

### Creating an operator

1. **New operator**: email, display name, initial password (at least 10 characters), role.
2. Share the initial password out-of-band.
3. They sign in with it.

### Disabling an operator

On their row → **Disable**. They can't sign in again. Use disable instead of deleting staff who leave — it keeps the audit history. You cannot disable yourself.

### Assigning multiple roles

One operator can hold several roles: **Add a role** on their row, or click a role badge to remove it.

## Walled garden

**Hotel Admin → Guest portal → Allowed sites**

Addresses guests can reach before signing in. Keep this short — every entry is reachable without signing in.

Typical entries:

- A captive-portal check or identity provider the sign-in page needs
- A payment provider, if paid Wi-Fi is ever switched on

**Don't** add general-purpose sites (search engines, social networks, CDNs) — it defeats the sign-in page.

## License

**Central → Commercial → Licenses**

Shows each appliance's signed license: **max concurrent online guests**, current online guests against that limit, valid from/until, grace period and state (Active, Grace, Expired, Suspended, Revoked, Superseded, Awaiting appliance binding). Issuing and renewing licenses is done by the vendor's platform admin; ask them when you need more guests or a longer term.

When a license expires past its grace period, or is suspended or revoked, the appliance refuses new guest sign-ins; guests already online are not dropped.

## Audit log

**Central → Administration → Audit log**

Every Central action for your customer in the last 7 days: who, when, what changed. Filter by action name. Changes made on an appliance are in **Hotel Admin → System → Activity** on that appliance.

This log is the authoritative record for compliance and dispute resolution. Entries are never edited or removed.

## Sessions (live monitoring)

**Hotel Admin → Guests → Active sessions** (on each appliance)

Who is connected right now, whose device it is, and the package and allowance they're on. Per device:

- **Disconnect** — takes the device offline; the guest can sign in again.
- **Details** — MAC, IP, data used, how they signed in, the package and service plan.

Also useful as a "is Wi-Fi working?" smoke test — if the count is zero and guests are present, something is wrong. See [common-tasks.md](common-tasks.md).

## Things you should NOT do

- Don't share your login with your staff. Create them an operator account (in Central or on the appliance).
- Don't add broad wildcards to Allowed sites "to be safe" — it opens the internet before sign-in.
- Don't change PMS credentials during business hours without testing first — a broken PMS connection means nobody can sign in by room number.
- Don't delete sites that still have appliances — remove appliances first.

## Escalation

- **Guest can't connect** → [common-tasks.md](common-tasks.md#a-guest-cant-log-in)
- **Appliance offline >30 min** → check the site's internet uplink, then contact Semantics support
- **PMS connection broken** → use **Test the connection** on the PMS connection page first; if it's a PMS-side issue, contact your PMS vendor
- **License question** (more guests, renewal) → your platform admin contact at OneGate
