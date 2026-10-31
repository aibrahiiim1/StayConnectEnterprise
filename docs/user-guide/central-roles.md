# OneGate Central — role guides

> Consolidated on 2026-10-28 (T0265) from the four per-role pages, unchanged in substance. Roles and permissions are defined in [CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles); the Central pages are described in [control-panel-reference.md](control-panel-reference.md).

## Platform admin

You run OneGate Central for the vendor. You do not run a site; your customers are the organisations that own sites. Your job is to create customers and sites, activate their appliances, set and manage each appliance's signed licence, and step in when a customer admin has a problem they can't solve themselves. Central is used for licensing, activation and fleet status only: a site's networks, sign-in methods, packages and clients are run by the site's own staff in **OneGate Admin Console** (formerly Hotel Admin) on the appliance. The binding description of Central is [CENTRAL_CONTROL_PLANE.md](../CENTRAL_CONTROL_PLANE.md).

### What only you can do

Platform admins (`platform_admin`, `platform_owner`) can do everything in Central. In particular, only you can:

- Create, rename, archive and delete **customers**.
- **Activate** appliances, **Move**, **Retire** and **Delete** them, and use the **Advanced** repair actions.
- Issue, renew or change, suspend, resume and revoke **licences**, and download offline licence files and activation packages. The signed licence (max concurrent online clients, validity, grace period) is the only entitlement; there are no plans or subscriptions.
- Triage **Security alerts** and manage the **Team** (Central operators).

**Support (read only)** (`platform_support`) sees everything you see and changes nothing.

### Your daily navigation

The sidebar is **Overview · Customers · Appliances · Licenses · System**. There is no customer selector: start from **Overview** (fleet counts and *Needs attention*), a list (Customers, Appliances, Licenses — each filterable), or open one customer or one appliance. **System** holds Security alerts, Trust & keys, Audit log, Team and Backup health. Page-by-page details: [control-panel-reference.md](control-panel-reference.md).

### Common tasks

#### Onboarding a new customer (an organisation signs up)

1. **Customers → New customer** — the name.
2. On the customer page, **Sites → New site** for each site (name, time zone).
3. When each appliance is powered on it registers itself and appears under **Appliances** as **Waiting for activation**. Open it → **Activate** → choose the customer and site and the licence terms. (You can also create the customer and site inside the Activate dialog.) Details: [control-panel-config-manual.md](control-panel-config-manual.md).
4. If the customer should have its own Central login: customer page → **Users → Add user**, role **Customer admin** (or **Auditor**/**Viewer** for read-only). Central asks you to confirm your own password first, as it does for every change to a sign-in. Send them the initial password out-of-band (encrypted email, phone, etc.).
5. Tell them to sign in at your Central address, then follow the [customer admin guide](central-roles.md).

Site staff never sign in to Central to run their site; they use the operator accounts created on their appliance in the Admin Console.

#### Checking on a customer's health

Open the customer: **Summary** shows its sites, appliances, active licences and what needs attention; **Appliances** shows each appliance's activation, connection and licence state; **Licenses** shows validity and grace; **Activity** shows what changed in Central for that customer.

If an appliance is offline you'd usually contact the site rather than fix it yourself — you don't have physical access, and an appliance that cannot reach Central keeps serving clients.

#### Offboarding a customer

1. **Suspend** or **Revoke** each appliance's licence (the appliance's page → **License**) — client access on those appliances stops.
2. **Archive** the customer — hidden from active lists; sites, appliances, licences and the audit log are kept. **Restore** reverses it.
3. To remove it permanently, remove bottom-up: **Retire** each appliance and **Delete record** → delete each site → delete the customer. Each delete asks you to type the serial or name and give a reason.

#### Moving, replacing or re-homing an appliance

- **Another site of the same customer** — the appliance's page → **Move**. The licence is re-issued for the new site with the same terms; if Central cannot re-issue it, nothing changes.
- **New hardware at the same site** — old appliance → **Advanced → Mark for replacement**; activate the new appliance for the same customer and site. The old one then retires through the acknowledged two-phase retirement.
- **Another customer** — there is no move between customers. **Retire** the appliance, have it factory-reset on site, and **Activate** it for the new customer when it registers again as *Waiting for activation*.

Procedures: [control-panel-config-manual.md §5](control-panel-config-manual.md#5-move-replace-repair-or-retire-an-appliance).

#### Changing what a site may serve

On the appliance's page, **License → Renew or change**: clients online at once, validity and grace period. It issues a new signed licence version that replaces the old one. For an appliance with no route to Central, download the **Offline license file** and send it to the site to upload in the Admin Console under **Appliance & licence**.

### What you should NOT do

- **Don't** create users inside a customer unless the customer asked. It shows up in their activity log and confuses them.
- **Don't** suspend or revoke a customer's licence without the agreed commercial decision behind it.
- **Don't** expect to change a site's PMS, allowed sites, client networks or portal from Central — these are operational decisions owned by the site and made in the Admin Console.
- **Don't** share one customer's data with another. Each customer is isolated from every other customer.

### Monitoring the platform

- **Overview** — fleet counts and everything that needs attention (waiting, licences expiring/in grace/expired/suspended, appliances offline, open security alerts, unconfirmed retirements).
- **Appliances** — connection state and last contact of every appliance.
- **System → Security alerts** — cloned or reused hardware, WAN MAC mismatches, unconfirmed retirements; a refused registration never appears as waiting.
- **System → Trust & keys** — certificate expiry and the state of the signing keys.
- **System → Backup health** — Central's own backup and rollback storage.

Appliance service health, client sessions and usage are watched on each appliance in the Admin Console; appliances do not send telemetry to Central (CLAUDE.md §0E). For monitoring of the Central host itself, see `deploy/observability/README.md`.

### Who to escalate to

- **Commercial / legal / contract issues** → your operations or finance team.
- **Engineering bugs** — file an issue with repro steps. Prefer: `audit log entry`, `customer name`, `appliance serial`, `approx timestamp`.
- **Security incident** — follow your incident response runbook. Disable the affected customer's users first (customer page → **Users**), then investigate.


## Customer admin (tenant admin)

You look after OneGate for your organisation (a single site, a chain or a group of sites) in **OneGate Central**. In Central you manage your customer's **sites** and its **users** (Central sign-ins), and you can see its appliances, licences and activity. Activating appliances and licences is done by the vendor's platform admin. The sites' day-to-day setup — client networks, sign-in methods, packages, vouchers, the PMS — is done in **OneGate Admin Console** (formerly Hotel Admin) on each appliance, with an operator account created on that appliance.

You can only see your own customer: **Customers** opens your customer's page directly, and Overview, Appliances and Licenses show only your customer. You **cannot** see or change any other customer. Central's server decides exactly which actions your role may perform (roles: [CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles)). **Customer owner** has the same Central rights as Customer admin.

### First-time setup (new customer checklist)

Do these in order the first time you sign in:

1. **Change your initial password** — ask the platform admin to reset it if you did not choose it yourself.
2. **Check your sites** (one per physical location). See [Managing sites](#managing-sites).
3. **Check your appliances** are activated and connected. See [Appliances](#appliances).
4. **Sign in to the Admin Console** on each appliance and **pick the sign-in methods** — room sign-in (PMS), vouchers, client accounts, email/SMS codes, social login. See [Authentication setup](#authentication-setup).
5. **Check Allowed sites** in the Admin Console — only what the sign-in page needs before clients sign in.
6. **Create the rest of your staff** — see [Managing users](#managing-users).
7. **Test a client connection** at one site before opening to real clients.

### Managing sites

**Central → Customers → your customer → Sites**

A site is one physical location. If you run three hotels, offices or campuses, you have three sites. If one site has two buildings that share the same internet uplink, that's still one site.

- **New site**: name, time zone, and optionally country and short code.
- **Edit**: change the site's details.
- **Archive / Restore**: hide a site without deleting it.
- **Delete**: only works when nothing is left under it; the dialog lists what blocks it and asks you to type the site name and a reason.

### Appliances

**Central → Appliances** (or your customer page → **Appliances**)

An appliance is the OneGate gateway (physical or virtual) at a site. A new appliance registers itself with Central when it is powered on with internet and shows **Waiting for activation**; the vendor's platform admin then **activates** it for your site and sets its licence terms. You can see every appliance's state but not change it:

- **Activation** — *Waiting for activation*, *Activating*, *Activated*, *Retiring*, *Retired*.
- **Connection** — *Connected* (contacted Central in the last 5 minutes), *Recently seen* (last 24 hours), *Offline* (more than a day), *Never connected*. An offline appliance keeps serving clients — Central is used for licensing only — but ask someone at the site to check its uplink.

**Moving, retiring or replacing an appliance** changes its signed assignment and licence — ask the platform admin. An appliance can move only between your own sites; giving it to another customer means retiring it and having it factory-reset and activated again.

### Authentication setup

How clients sign in is configured **on each appliance, in the Admin Console**, not in Central. What a client sees is described in [guest-portal.md](guest-portal.md); the pages are:

#### Vouchers

**Admin Console → Internet offering → Vouchers**

Printed cards with a code, each giving an internet package. **Issue vouchers** (1–500 cards per batch) shows the codes once to copy, download or print. Cancelling a lost card, or reading a code again, asks for a reason and the operator's password.

#### PMS (room + name)

**Admin Console → Hotel → PMS connection**, **Hotel → PMS routing** and **Hotel → Room sign-in**

Guests sign in with their room number plus one detail from the reservation — first name, surname or reservation number, or any one of the three (recommended), as chosen under **Hotel → Room sign-in** — checked against the appliance's copy of the PMS guest list. The method is switched on under **Client Portal → Sign-in methods**. Set up by the site's Site admin or Site IT manager.

#### Email / SMS codes

**Admin Console → Client Portal → Email & SMS**

A sender (SendGrid, Amazon SES or Twilio) delivers one-time codes. Then switch **Email code** / **SMS code** on under **Sign-in methods**.

#### Social login

**Admin Console → Client Portal → Social login**

1. Create an OAuth app with Google / Apple / Facebook / Microsoft (outside OneGate — follow their docs).
2. Enter the client ID and secret in Social login.
3. Tick the provider under **Sign-in methods**.

#### Paid Wi-Fi

Internet packages are free to clients today; selling internet is not switched on. The charge pages under **Hotel** in the Admin Console show *Not enabled on this appliance* until it is.

### Managing users

**Central → Customers → your customer → Users**

These are Central logins for your organisation. Site staff who run an appliance day to day get an operator account **on that appliance** instead (**Admin Console → System → Operators**, created by its Site admin).

#### Roles explained

- **Customer admin** — manages your customer's sites and users; sees its appliances, licences and activity. Give sparingly.
- **Auditor (read only)** and **Viewer (read only)** — see everything for your customer, change nothing.

Older roles (Customer operator, Billing) no longer grant anything in Central.

#### Adding a user

1. **Add user**: email, name, initial password, role. Central asks you to confirm your own password first; so does every role change, disable/enable and removal.
2. Share the initial password out-of-band.
3. They sign in with it.

Each user has one role; **Role** on their row changes it (it takes effect at their next sign-in).

#### Disabling a user

On their row → **Disable**. They can't sign in again; **Enable** reverses it. Use disable instead of **Remove** for staff who leave — it keeps the account and its audit history.

### Walled garden

**Admin Console → Client Portal → Allowed sites**

Addresses clients can reach before signing in. Keep this short — every entry is reachable without signing in.

Typical entries:

- A captive-portal check or identity provider the sign-in page needs
- A payment provider, if paid Wi-Fi is ever switched on

**Don't** add general-purpose sites (search engines, social networks, CDNs) — it defeats the sign-in page.

### License

**Central → Licenses** (or your customer page → **Licenses**)

Shows each appliance's signed licence: **clients online at once**, valid until, grace period and state (Active, Expiring, In grace period, Expired, Suspended, Revoked). How many clients are online against the limit is shown on the appliance, in the **Admin Console → System → Appliance & licence**. Issuing and renewing licences is done by the vendor's platform admin; ask them when you need more clients or a longer term.

When a licence expires past its grace period, or is suspended or revoked, the appliance refuses new client sign-ins until the licence is renewed, resumed or replaced.

### Audit log

**Central → Customers → your customer → Activity**

Every Central action for your customer, newest first: who, when, what changed. Changes made on an appliance are in the **Admin Console → System → Activity** on that appliance.

This log is the authoritative record for compliance and dispute resolution. Entries are never edited or removed.

### Sessions (live monitoring)

**Admin Console → Clients → Active sessions** (on each appliance)

Who is connected right now, whose device it is, and the package and allowance they're on. Per device:

- **Disconnect** — takes the device offline; the client can sign in again.
- **Details** — MAC, IP, data used, how they signed in, the package and service plan.

Also useful as a "is Wi-Fi working?" smoke test — if the count is zero and clients are present, something is wrong. See [common-tasks.md](common-tasks.md).

### Things you should NOT do

- Don't share your login with your staff. Create them a user in Central or an operator account on the appliance.
- Don't add broad wildcards to Allowed sites "to be safe" — it opens the internet before sign-in.
- Don't change PMS credentials during business hours without testing first — a broken PMS connection means nobody can sign in by room number.
- Don't delete sites that still have appliances — remove appliances first.

### Escalation

- **Client can't connect** → [common-tasks.md](common-tasks.md#a-client-cant-log-in)
- **Appliance offline >30 min** → check the site's internet uplink, then contact Semantics support
- **PMS connection broken** → use **Test the connection** on the PMS connection page first; if it's a PMS-side issue, contact your PMS vendor
- **License question** (more clients, renewal) → your platform admin contact at OneGate


## Customer operator (tenant operator)

You handle day-to-day Wi-Fi operations for your organisation. That work happens on each site's appliance in **OneGate Admin Console** (formerly Hotel Admin), with an operator account created for you on that appliance (usually the **Client services operator** or **Site IT manager** role).

**The *Customer operator* role grants nothing in OneGate Central** ([CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles)). Central is for licensing, activation and fleet status; if you need to see your appliances' licences there, ask your customer admin for a **Viewer** login. You **cannot** add or remove staff, change licences, or change sites — your customer admin (or the OneGate platform admin, for licences) does it.

### Your daily workflow

Most days you'll touch three Admin Console pages:

1. **Overview** — quick check that everything is healthy.
2. **Active sessions** and **Hotel → Guest sign-in attempts** — handle client complaints in real time.
3. **Vouchers** — issue more cards when the desk runs low.

Everything else is set up once and left alone.

### Vouchers

**Admin Console → Internet offering → Vouchers**

The most common task. The desk hands out printed cards; when they run low you issue another batch.

#### Issuing a batch

1. **Issue vouchers**:
   - **Package** — which internet package the cards give (speed, time and data come from it).
   - **How many cards** — 1 to 500 per batch.
   - **Valid from / Valid until** — optional. Leave empty for cards usable now that never expire.
   - **Note** — something you'll recognise later (e.g. "Conference desk, week 12").
2. **Review** and issue.
3. The codes are shown **once**: **Copy all**, **Download CSV**, or **Print cards** (set the heading printed on each card first).
4. Hand the cards to the desk.

#### When a client reports their card doesn't work

1. Open **Vouchers** → search by the last characters of the code.
2. Check the status: *Available*, *Not yet valid*, *Expired never used*, *Used* or *Cancelled*; the card's history shows what happened.
3. If the card is spent, hand the client a fresh card.
4. If a stack of cards is lost or stolen, open the batch (**Batches → View cards**) and **Cancel card** on each unused card (reason + your password). There is no whole-batch cancel.

Reading a full code again (**Show full code**) asks for a reason and your password, and is recorded.

### Client sessions

**Admin Console → Clients → Active sessions**

The "who is online right now" view. Search by room, name, username, IP or MAC, or filter by how the client signed in. It refreshes every 10 seconds.

#### Typical requests from the desk

- **"Guest in 214 says their Wi-Fi is gone"** → find their session → if it has ended, the status gives the reason (time or data used up, checked out, idle…); if it's there, check the allowance meters.
- **"Guest can't sign in with their room number"** → **Hotel → Guest sign-in attempts**: read **Why**, and **Release** the device if it has been asked to wait (releasing lets it try again; it does not sign the guest in).
- **"Guest checked out but still connected"** → **Disconnect**. (Checkout normally ends room access automatically, while After Check-out Access is on — **Hotel → After Check-out Access**.)
- **"Something weird is happening on room 310"** → click the session → see MAC, IP, package and data. **Usage explorer → By access source** (type *Hotel room/stay*) shows the room's full history.

#### Signs of abuse to watch for

- A voucher or account constantly at its device limit → the code may be shared. Cancel the card or change the account's password.
- Data usage far above average → likely a device re-sharing the connection. Disconnect it, and ask the Site admin about the service plan's limits.
- Many failed room sign-ins from one device → someone guessing. The appliance makes the device wait automatically; tell your admin so they can review the thresholds under **Sign-in methods**.

### Walled garden

**Admin Console → Client Portal → Allowed sites**

Addresses clients can reach before signing in. Usually set up once by the site's Site admin or Site IT manager, who can add entries when a sign-in method needs a new address. Add entries sparingly — every entry is reachable without signing in.

**To add an entry** (Site admin or Site IT manager): **Allow a site** → type (domain name, single address or address range), address, optional ports, why it is needed.

### Portal settings

**Admin Console → Client Portal → Portal settings**

The look and wording of the client sign-in page: layout template, logo and photographs, colours, site name, welcome and help lines, terms link, and the wording in each language. You might update it when:

- The site rebrands (new logo, new colours).
- Legal asks you to change the terms link.
- You want to change the welcome or help line.

Check the live preview (desktop, tablet, mobile) before you **Save changes** — clients see it immediately. Changing it needs the Site admin or Site IT manager role.

### PMS connection

**Admin Console → Hotel → PMS connection**

Usually set up once by the Site IT manager. With a desk role you can **view** the connection's state and whether room sign-in is working.

If it shows room sign-in not working or many recent failures, it's usually:

- The PMS is down or in maintenance → wait / check with PMS support.
- The PMS credential changed → ask the Site IT manager to replace it.
- A client network points at the wrong PMS → **Hotel → PMS routing** (Site admin).

You cannot edit the connection from a desk role — that's the Site IT manager's job, and for good reason.

### Email & SMS and Social login

**View only** from desk roles in most cases. If you need a new sender or provider, ask the Site IT manager.

### Charges

**Admin Console → Hotel → Charge health, Manual review, Settlements, Recovery**

Selling internet is not switched on today, so these pages are usually quiet or *Not enabled on this appliance*. Decisions about room charges belong to the Payments operator and Site admin; there is no refund button by design.

### Dashboard

**Admin Console → Overview**

The morning check:

1. **Needs attention** — anything listed? Each line links to the page that fixes it.
2. **Clients online** — roughly matches your occupancy?
3. **Room sign-in** — *Ready*? And **Sign-in outcomes** — a spike in refusals suggests a PMS or routing problem.
4. **Services** and the health pill in the top bar — all healthy?

### What you cannot do

- Create / remove operators (including yourself). Ask your customer admin (Central users) or the appliance's Site admin (Admin Console operators).
- Change licences. Ask your platform admin contact.
- Create, change or delete sites. Ask your customer admin.
- Change PMS, email/SMS or social-login credentials from a desk role. Ask the Site IT manager.

### When to escalate to your customer admin

- Any structural change (new site, new appliance, new integration).
- Recurring abuse patterns you can't shut down alone.
- Any request involving staff accounts.
- Appliance offline for >30 min and you've already checked the site's uplink.

When in doubt: your customer admin sees more than you do. Just ask.


## Viewer, auditor and billing

**Viewer** and **Auditor** are the read-only roles in **OneGate Central**; **Billing** is a retired role. Keep this short because there's not much to do. (On an appliance, the equivalent read-only role in **OneGate Admin Console** (formerly Hotel Admin) is **Site viewer** — see [hotel-admin-reference.md](hotel-admin-reference.md#who-can-use-which-page).) Roles are defined in [CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles).

---

### Viewer

**Viewer (read only)** and **Auditor (read only)** see everything for their customer in Central and change nothing.

#### Who typically gets this role

- Internal or external auditors.
- Regional managers doing spot checks across sites.
- Customer support reps who need context but shouldn't touch configuration.
- Engineers debugging a problem who don't need write access.

#### What you can do

Open **Overview**, **Customers** (which opens your customer's page: Summary, Sites, Appliances, Licenses, Users, Activity), **Appliances** and **Licenses** — all limited to your customer. The console shows no action buttons for your role, and the server refuses any change.

#### What you cannot do

- Create, edit or delete sites or users.
- Activate, move or retire an appliance, or change a licence (those are for the vendor's platform admin).

#### Most useful pages for you

- **Customer → Activity** — who did what, when. Your primary tool for investigations.
- **Licenses** — each appliance's licence state, clients online at once, and validity.
- **Overview** — what needs attention at a glance.
- **Appliances** — each appliance's activation and connection state.

#### If you need to change something

Ask a customer admin at your organisation. Don't ask to be promoted "just this once" — role changes are audited and usually stick. Keep the separation of duties.

---

### Billing

A retired role: it **grants nothing in Central** and is no longer offered. Someone who still holds only this role sees no customer data in Central. Ask your customer admin for the **Viewer** role if you need to read your licences.

#### Changing what you are licensed for

There is nothing to change in Central yourself: the signed appliance licence is issued and renewed by the OneGate platform admin. Contact your Semantics account contact with what you need (more concurrent clients, a longer term); when it is renewed, a new licence version appears in the appliance's licence history.

#### Getting invoices

Invoices are not produced by OneGate Central. Ask your OneGate account contact or your organisation's finance team.
