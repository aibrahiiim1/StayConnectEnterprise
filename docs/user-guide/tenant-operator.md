# Customer Operator (Tenant Operator) — User Guide

You handle day-to-day Wi-Fi operations for your hotel group. In **Velonet Central** your role is day-to-day work for your customer; most guest-facing work happens on each hotel's appliance in **Velonet Hotel Admin**, with an operator account created for you on that appliance (usually the **Front office operator** or **Hotel IT manager** role). You **cannot** add or remove other staff, change licenses, or delete the customer.

If you need any of those things, your customer admin (or the Velonet platform admin, for licenses) does it.

## Your daily workflow

Most days you'll touch three Hotel Admin pages:

1. **Overview** — quick check that everything is healthy.
2. **Active sessions** and **Guest sign-in attempts** — handle guest complaints in real time.
3. **Vouchers** — issue more cards when reception runs low.

Everything else is set up once and left alone.

## Vouchers

**Hotel Admin → Internet offering → Vouchers**

The most common task. Reception hands out printed cards; when they run low you issue another batch.

### Issuing a batch

1. **Issue vouchers**:
   - **Package** — which internet package the cards give (speed, time and data come from it).
   - **How many cards** — 1 to 500 per batch.
   - **Valid from / Valid until** — optional. Leave empty for cards usable now that never expire.
   - **Note** — something you'll recognise later (e.g. "Conference desk, week 12").
2. **Review** and issue.
3. The codes are shown **once**: **Copy all**, **Download CSV**, or **Print cards** (set the heading printed on each card first).
4. Hand the cards to reception.

### When a guest reports their card doesn't work

1. Open **Vouchers** → search by the last characters of the code.
2. Check the status: *Available*, *Not yet valid*, *Expired never used*, *Used* or *Cancelled*; the card's history shows what happened.
3. If the card is spent, hand the guest a fresh card.
4. If a stack of cards is lost or stolen, open the batch (**Batches → View cards**) and **Cancel card** on each unused card (reason + your password). There is no whole-batch cancel.

Reading a full code again (**Show full code**) asks for a reason and your password, and is recorded.

## Guest sessions

**Hotel Admin → Guests → Active sessions**

The "who is online right now" view. Search by room, name, username, IP or MAC, or filter by how the guest signed in. It refreshes every 10 seconds.

### Typical requests from reception

- **"Guest in 214 says their Wi-Fi is gone"** → find their session → if it has ended, the status gives the reason (time or data used up, checked out, idle…); if it's there, check the allowance meters.
- **"Guest can't sign in with their room number"** → **Guest sign-in attempts**: read **Why**, and **Release** the device if it has been asked to wait (releasing lets it try again; it does not sign the guest in).
- **"Guest checked out but still connected"** → **Disconnect**. (Checkout normally ends room access automatically, after any checkout grace.)
- **"Something weird is happening on room 310"** → click the session → see MAC, IP, package and data. **Usage explorer** shows the room's full history.

### Signs of abuse to watch for

- A voucher or account constantly at its device limit → the code may be shared. Cancel the card or change the account's password.
- Data usage far above average → likely a device re-sharing the connection. Disconnect it, and ask the Site admin about the service plan's limits.
- Many failed room sign-ins from one device → someone guessing. The appliance makes the device wait automatically; tell your admin so they can review the thresholds under **Sign-in methods**.

## Walled garden

**Hotel Admin → Guest portal → Allowed sites**

Addresses guests can reach before signing in. Usually set up once by the hotel's Site admin or Hotel IT manager, who can add entries when a sign-in method needs a new address. Add entries sparingly — every entry is reachable without signing in.

**To add an entry** (Site admin or Hotel IT manager): **Allow a site** → type (domain name, single address or address range), address, optional ports, why it is needed.

## Portal settings

**Hotel Admin → Guest portal → Portal settings**

The look and wording of the guest sign-in page: layout template, logo and photographs, colours, hotel name, welcome and help lines, terms link, and the wording in each language. You might update it when:

- The hotel rebrands (new logo, new colours).
- Legal asks you to change the terms link.
- You want to change the welcome or help line.

Check the live preview (desktop, tablet, mobile) before you **Save changes** — guests see it immediately. Changing it needs the Site admin or Hotel IT manager role.

## PMS connection

**Hotel Admin → Property management system → PMS connection**

Usually set up once by the Hotel IT manager. With a desk role you can **view** the connection's state and whether room sign-in is working.

If it shows room sign-in not working or many recent failures, it's usually:

- The PMS is down or in maintenance → wait / check with PMS support.
- The PMS credential changed → ask the Hotel IT manager to replace it.
- A guest network points at the wrong PMS → **Network routing** (Site admin).

You cannot edit the connection from a desk role — that's the Hotel IT manager's job, and for good reason.

## Email & SMS and Social login

**View only** from desk roles in most cases. If you need a new sender or provider, ask the Hotel IT manager.

## Charges

**Hotel Admin → Charges**

Selling internet is not switched on today, so these pages are usually quiet or *Not enabled on this appliance*. Decisions about room charges belong to the Payments operator and Site admin; there is no refund button by design.

## Dashboard

**Hotel Admin → Overview**

The morning check:

1. **Needs attention** — anything listed? Each line links to the page that fixes it.
2. **Guests online** — roughly matches your occupancy?
3. **Room sign-in** — *Ready*? And **Sign-in outcomes** — a spike in refusals suggests a PMS or routing problem.
4. **Services** and the health pill in the top bar — all healthy?

## What you cannot do

- Create / remove operators (including yourself). Ask your customer admin (Central) or the appliance's Site admin (Hotel Admin).
- Change licenses. Ask your platform admin contact.
- Delete the customer or any site. Ask your customer admin.
- Change PMS, email/SMS or social-login credentials from a desk role. Ask the Hotel IT manager.

## When to escalate to your customer admin

- Any structural change (new site, new appliance, new integration).
- Recurring abuse patterns you can't shut down alone.
- Any request involving staff accounts.
- Appliance offline for >30 min and you've already checked the site's uplink.

When in doubt: your customer admin sees more than you do. Just ask.
