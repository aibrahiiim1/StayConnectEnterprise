# Customer Admin (Tenant Admin) — User Guide

You look after OneGate for your organisation (a single site, a chain or a group of sites) in **OneGate Central**. In Central you manage your customer's **sites** and its **users** (Central sign-ins), and you can see its appliances, licences and activity. Activating appliances and licences is done by the vendor's platform admin. The sites' day-to-day setup — guest networks, sign-in methods, packages, vouchers, the PMS — is done in **OneGate Admin Console** (formerly Hotel Admin) on each appliance, with an operator account created on that appliance.

You can only see your own customer: **Customers** opens your customer's page directly, and Overview, Appliances and Licenses show only your customer. You **cannot** see or change any other customer. Central's server decides exactly which actions your role may perform (roles: [CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles)). **Customer owner** has the same Central rights as Customer admin.

## First-time setup (new customer checklist)

Do these in order the first time you sign in:

1. **Change your initial password** — ask the platform admin to reset it if you did not choose it yourself.
2. **Check your sites** (one per physical location). See [Managing sites](#managing-sites).
3. **Check your appliances** are activated and connected. See [Appliances](#appliances).
4. **Sign in to the Admin Console** on each appliance and **pick the sign-in methods** — room sign-in (PMS), vouchers, client accounts, email/SMS codes, social login. See [Authentication setup](#authentication-setup).
5. **Check Allowed sites** in the Admin Console — only what the sign-in page needs before clients sign in.
6. **Create the rest of your staff** — see [Managing users](#managing-users).
7. **Test a client connection** at one site before opening to real clients.

## Managing sites

**Central → Customers → your customer → Sites**

A site is one physical location. If you run three hotels, offices or campuses, you have three sites. If one site has two buildings that share the same internet uplink, that's still one site.

- **New site**: name, time zone, and optionally country and short code.
- **Edit**: change the site's details.
- **Archive / Restore**: hide a site without deleting it.
- **Delete**: only works when nothing is left under it; the dialog lists what blocks it and asks you to type the site name and a reason.

## Appliances

**Central → Appliances** (or your customer page → **Appliances**)

An appliance is the OneGate gateway (physical or virtual) at a site. A new appliance registers itself with Central when it is powered on with internet and shows **Waiting for activation**; the vendor's platform admin then **activates** it for your site and sets its licence terms. You can see every appliance's state but not change it:

- **Activation** — *Waiting for activation*, *Activating*, *Activated*, *Retiring*, *Retired*.
- **Connection** — *Connected* (contacted Central in the last 5 minutes), *Recently seen* (last 24 hours), *Offline* (more than a day), *Never connected*. An offline appliance keeps serving clients — Central is used for licensing only — but ask someone at the site to check its uplink.

**Moving, retiring or replacing an appliance** changes its signed assignment and licence — ask the platform admin. An appliance can move only between your own sites; giving it to another customer means retiring it and having it factory-reset and activated again.

## Authentication setup

How clients sign in is configured **on each appliance, in the Admin Console**, not in Central. What a client sees is described in [guest-portal.md](guest-portal.md); the pages are:

### Vouchers

**Admin Console → Internet offering → Vouchers**

Printed cards with a code, each giving an internet package. **Issue vouchers** (1–500 cards per batch) shows the codes once to copy, download or print. Cancelling a lost card, or reading a code again, asks for a reason and the operator's password.

### PMS (room + name)

**Admin Console → Property management system → PMS connection** and **Network routing**

Guests sign in with their room number plus their name or reservation number, checked against the appliance's copy of the PMS guest list. Set up by the site's Site admin or Site IT manager.

### Email / SMS codes

**Admin Console → Client Portal → Email & SMS**

A sender (SendGrid, Amazon SES or Twilio) delivers one-time codes. Then switch **Email code** / **SMS code** on under **Sign-in methods**.

### Social login

**Admin Console → Client Portal → Social login**

1. Create an OAuth app with Google / Apple / Facebook / Microsoft (outside OneGate — follow their docs).
2. Enter the client ID and secret in Social login.
3. Tick the provider under **Sign-in methods**.

### Paid Wi-Fi

Internet packages are free to clients today; selling internet is not switched on. The **Charges** pages in the Admin Console show *Not enabled on this appliance* until it is.

## Managing users

**Central → Customers → your customer → Users**

These are Central logins for your organisation. Site staff who run an appliance day to day get an operator account **on that appliance** instead (**Admin Console → System → Operators**, created by its Site admin).

### Roles explained

- **Customer admin** — manages your customer's sites and users; sees its appliances, licences and activity. Give sparingly.
- **Auditor (read only)** and **Viewer (read only)** — see everything for your customer, change nothing.

Older roles (Customer operator, Billing) no longer grant anything in Central.

### Adding a user

1. **Add user**: email, name, initial password, role. Central asks you to confirm your own password first; so does every role change, disable/enable and removal.
2. Share the initial password out-of-band.
3. They sign in with it.

Each user has one role; **Role** on their row changes it (it takes effect at their next sign-in).

### Disabling a user

On their row → **Disable**. They can't sign in again; **Enable** reverses it. Use disable instead of **Remove** for staff who leave — it keeps the account and its audit history.

## Walled garden

**Admin Console → Client Portal → Allowed sites**

Addresses clients can reach before signing in. Keep this short — every entry is reachable without signing in.

Typical entries:

- A captive-portal check or identity provider the sign-in page needs
- A payment provider, if paid Wi-Fi is ever switched on

**Don't** add general-purpose sites (search engines, social networks, CDNs) — it defeats the sign-in page.

## License

**Central → Licenses** (or your customer page → **Licenses**)

Shows each appliance's signed licence: **clients online at once**, valid until, grace period and state (Active, Expiring, In grace period, Expired, Suspended, Revoked). How many clients are online against the limit is shown on the appliance, in the **Admin Console → System → Appliance & licence**. Issuing and renewing licences is done by the vendor's platform admin; ask them when you need more clients or a longer term.

When a licence expires past its grace period, or is suspended or revoked, the appliance refuses new client sign-ins until the licence is renewed, resumed or replaced.

## Audit log

**Central → Customers → your customer → Activity**

Every Central action for your customer, newest first: who, when, what changed. Changes made on an appliance are in the **Admin Console → System → Activity** on that appliance.

This log is the authoritative record for compliance and dispute resolution. Entries are never edited or removed.

## Sessions (live monitoring)

**Admin Console → Clients → Active sessions** (on each appliance)

Who is connected right now, whose device it is, and the package and allowance they're on. Per device:

- **Disconnect** — takes the device offline; the client can sign in again.
- **Details** — MAC, IP, data used, how they signed in, the package and service plan.

Also useful as a "is Wi-Fi working?" smoke test — if the count is zero and clients are present, something is wrong. See [common-tasks.md](common-tasks.md).

## Things you should NOT do

- Don't share your login with your staff. Create them a user in Central or an operator account on the appliance.
- Don't add broad wildcards to Allowed sites "to be safe" — it opens the internet before sign-in.
- Don't change PMS credentials during business hours without testing first — a broken PMS connection means nobody can sign in by room number.
- Don't delete sites that still have appliances — remove appliances first.

## Escalation

- **Client can't connect** → [common-tasks.md](common-tasks.md#a-client-cant-log-in)
- **Appliance offline >30 min** → check the site's internet uplink, then contact Semantics support
- **PMS connection broken** → use **Test the connection** on the PMS connection page first; if it's a PMS-side issue, contact your PMS vendor
- **License question** (more clients, renewal) → your platform admin contact at OneGate
