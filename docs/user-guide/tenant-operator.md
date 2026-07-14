# Tenant Operator — User Guide

You handle day-to-day WiFi operations for your property. You can do everything operational — vouchers, sessions, walled garden, ticket design, monitoring — but you **cannot** add/remove other staff, change the subscription plan, or delete the tenant.

If you need any of those things, your tenant admin does it.

## Your daily workflow

Most days you'll touch three pages:

1. **Dashboard** — quick check that everything is healthy.
2. **Sessions** — handle guest complaints in real time.
3. **Voucher batches** — top up when reception runs low.

Everything else is setup-once-and-forget.

## Vouchers

**Menu: Voucher batches**

The most common task. Reception prints a stack of codes; when they run low you generate another batch.

### Generating a batch

1. **New batch**:
   - **Name** — something you'll recognise later (e.g. "Reception May 2026").
   - **Site** — which property these are for.
   - **Count** — how many codes to generate. 200–500 for a weekly restock is typical.
   - **Duration** — how long each code lasts after first use (24 h is a sane default).
   - **Data cap** — optional. Leave blank for unlimited.
2. Click **Generate**. Takes a few seconds for large batches.
3. Open the batch → **Download CSV** (for Excel / mail merge) or **Print** (for a receipt-style printout).
4. Hand the sheet to reception.

### When a guest reports their code doesn't work

1. Open **Voucher batches** → find the batch → search for the code.
2. Check: used? expired? revoked? attached to another device?
3. If the code is burned and it's a reception mistake, generate one replacement from the batch and hand it over.
4. If a whole batch is compromised (sheet stolen, PDF leaked), **Revoke batch** and re-print fresh ones.

Individual code revocation: click the code → Revoke.

## Guest sessions

**Menu: Sessions**

The "who is online right now" view. Filter by site, auth method, or search by room number / name.

### Typical requests from reception

- **"Guest in 214 says their WiFi is gone"** → find their session → if it's disconnected, have them reconnect; if it's there, check data cap / time remaining.
- **"Guest needs more data"** → **Quota reset** on their session.
- **"Guest checked out but still connected"** → **Disconnect**. (PMS-based auth usually catches this automatically after the post-checkout grace window.)
- **"Something weird is happening on room 310"** → click the session → see MAC, IP, appliance, data usage pattern. If the MAC keeps changing, someone is sharing the code.

### Signs of abuse to watch for

- Same voucher code across >5 devices → code being shared in a WhatsApp group. Revoke.
- Data usage 10x higher than average → likely torrenting or a hotspot being re-shared. Apply a stricter plan or disconnect.
- Dozens of failed PMS attempts from the same IP → someone guessing. The system auto-locks, but tell your tenant admin so they can review the threshold.

## Walled garden

**Menu: Walled garden**

URLs guests can reach before logging in. Usually set up once by the tenant admin. You'll occasionally add entries when:

- Marketing launches a landing page and wants it reachable without login.
- A payment / PMS / social login integration needs a new domain (often StayConnect auto-adds these, but sometimes manual).

Add entries sparingly. Every entry is a pre-auth bypass.

**To add an entry**: **New rule** → hostname or IP/CIDR → optional port → **Save**. Takes effect within a minute on all appliances.

## Ticket templates

**Menu: Ticket templates**

The email / printed slip a guest gets when they log in via OTP email. You might update these when:

- The hotel rebrands (new logo, new colours).
- Legal asks you to change the T&Cs wording.
- You want to promote something ("Enjoy your WiFi — try our rooftop bar tonight!").

Templates support variables like `{{guest_name}}`, `{{expires_at}}`, `{{site_name}}`. Preview before you save — a broken template means guests get broken emails.

## PMS providers

**Menu: PMS providers**

Usually set up once by the tenant admin. You can **view** the config and **test connectivity**.

If the PMS page shows "disconnected" or many recent failures, it's usually:

- Your PMS is down or doing maintenance → wait / check with PMS support.
- Someone rotated the PMS API key → ask your tenant admin to update it in StayConnect.
- Network path changed → check with IT.

You cannot edit credentials — that's a tenant_admin action, and for good reason.

## Notifications & Social login

**View only** from your role in most cases. If you need to add a new provider, ask your tenant admin.

## Payments

**Menu: Payments**

If your hotel offers paid WiFi plans, this is where you see transactions and issue refunds.

- **Transactions** — search by guest name, room, date, amount.
- **Refund** — pick a reason code (duplicate charge, service not delivered, goodwill) and click Refund. Goes through Stripe; the guest sees the refund on their card in 5–10 business days.

Refunds are logged in the audit log under your name.

## Dashboard

**Menu: Dashboard**

The morning check:

1. **Active sessions** — roughly matches your expected occupancy? (200 rooms at 80% × 2 devices = ~320 sessions.)
2. **Appliances online** — all green? If one is red, call the site.
3. **Recent auth failures** — a spike suggests a broken integration or an attack.
4. **Alerts** — open anything red and triage.

## What you cannot do

- Create / remove / rename operators (including yourself). Ask your tenant admin.
- Change the subscription plan. Ask your tenant admin or billing contact.
- Delete the tenant or any site. Ask your tenant admin.
- Edit PMS / OTP / social credentials. Ask your tenant admin.

## When to escalate to your tenant admin

- Any structural change (new site, new appliance, new integration).
- Recurring abuse patterns you can't shut down alone.
- Any request involving staff accounts.
- Appliance offline for >30 min and you've already checked the physical site's uplink.

When in doubt: your tenant admin sees everything you see plus more. Just ask.
