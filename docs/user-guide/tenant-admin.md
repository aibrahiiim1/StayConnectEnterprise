# Tenant Admin — User Guide

You run StayConnect for your organisation (a hotel, a chain, a group of properties). You are the owner of your tenant: you decide who on your staff can log in, which properties exist, how guests authenticate, and what you pay for.

You can do everything inside your tenant. You **cannot** see or change any other tenant.

## First-time setup (new tenant checklist)

Do these in order the first time you log in:

1. **Change your password** — top-right → your email → Change password.
2. **Create your sites** (one per property). See [Managing sites](#managing-sites).
3. **Enroll your appliances** (one per gateway device). See [Enrolling appliances](#enrolling-appliances).
4. **Pick an authentication method** — vouchers, PMS, OTP email/SMS, social login, or a combination. See [Authentication setup](#authentication-setup).
5. **Configure your walled garden** — add your hotel website, booking engine, whatever guests should be able to reach *before* they log in.
6. **Create the rest of your staff** — see [Managing operators](#managing-operators).
7. **Test a guest connection** at one site before opening to real guests.

## Managing sites

**Menu: Sites**

A site is a physical location. If you have three hotels, create three sites. If one hotel has two buildings that share the same internet uplink, that's still one site.

- **New site**: name, address, timezone (important for nightly reports and PMS stay windows).
- **Edit site**: change name / address / default language for the captive portal.
- **Delete site**: only works if no appliances are attached. Detach appliances first.

## Enrolling appliances

**Menu: Appliances**

An appliance is a StayConnect gateway box (or VM) at a site.

1. Click **Enroll appliance**. Pick the site. The system gives you an **enrollment token**.
2. On the appliance device, run the enrollment command (see the engineering handoff doc — usually `stayconnect-enroll --token <TOKEN>`).
3. The appliance appears in the list with status **online** once it heartbeats home (usually under a minute).

**What the appliance status column means:**

- **Online** — heartbeat in the last 2 minutes.
- **Stale** — no heartbeat for 2–10 minutes. Usually a transient network blip.
- **Offline** — nothing for >10 minutes. Either the box is down or the site lost internet uplink. Ask someone at the property to check.

**Moving an appliance** between sites requires re-enrollment.

## Authentication setup

You can enable one or more of these per site. Guests pick from the methods you enable on the captive portal.

### Vouchers

**Menu: Voucher batches**

Pre-printed codes a guest types in. Best for conferences, paid WiFi, or when you don't trust the PMS data.

1. **New batch**: name, site, number of codes, duration (e.g. 24 h), data cap if any.
2. Click **Generate** → system creates the codes.
3. Click the batch → **Download CSV** or **Print** to distribute.
4. Revoke the whole batch from the batch page if a stack of printouts gets stolen.

Individual codes can be reused by the same device until expiry, but different devices consume separate codes unless the plan allows shared devices.

### PMS (room + name)

**Menu: PMS providers**

Guests log in with their room number and last name (or reservation number). StayConnect checks your PMS in real time.

1. **New PMS provider**: pick the provider type (Mews, Opera, Protel, Apaleo, FIAS, Stub for testing).
2. Fill in the credentials (API key, endpoint, property ID — your PMS vendor provides these).
3. Set the **stay window policy**:
   - **Grace before check-in** (e.g. 2 h — guest can log in from the lobby before their official check-in time)
   - **Grace after check-out** (e.g. 1 h — guest keeps access after checkout while they pack)
   - **Min remaining minutes** — reject logins if less than N minutes of stay remain (avoids "expired 30 seconds later")
4. Save. The provider shows **connected** once the first test call succeeds.

Per-room lockout is automatic: too many wrong-name attempts on the same room triggers a cool-down to stop guessing. Defaults are sane; override under the provider's advanced settings only if needed.

### OTP email / SMS

**Menu: Notifications**

1. Configure your email provider (SendGrid, Mailgun, SES) or SMS provider (Twilio).
2. Put in the API key, sender address/number, from-name.
3. Send a **test notification** from the provider page.

Then under **Ticket templates**, design the email/SMS the guest receives — subject, body, branding.

### Social login

**Menu: Social login**

1. Create an OAuth app with Google / Apple / Facebook / etc. (outside StayConnect — follow their docs).
2. Paste the client ID + secret into StayConnect.
3. Enable on the captive portal.

### Paid WiFi (Stripe)

**Menu: Payments**

1. Connect your Stripe account.
2. Define your pricing (1 h / 24 h / week).
3. Guests pay through the captive portal; sessions start automatically on successful charge.
4. Use the Payments page to view transactions and issue refunds.

## Managing operators

**Menu: Operators**

Only `tenant_admin` can open this page.

### Roles explained

- **tenant_admin** — full control of your tenant. Can create other admins. Give sparingly.
- **tenant_operator** — day-to-day staff. Can do everything operational (vouchers, sessions, walled garden, view PMS) but cannot manage other operators or change the subscription.
- **viewer** — read-only. Good for auditors, regional managers who want to look.
- **billing** — can change your subscription plan. Nothing else. Good for a finance contact.

### Creating an operator

1. **New operator**: email, name, role.
2. System sends an invitation email (if Notifications is configured) or shows you a temporary password to share out-of-band.
3. They log in and are forced to change the password.

### Disabling an operator

Open the operator → **Disable**. Their session is killed, they can't log in again. You can re-enable later. Use disable instead of delete for staff who leave — it preserves audit history.

### Assigning multiple roles

One operator can hold multiple roles. A common pattern: one person is `tenant_admin` + `billing` so they handle both ops and contract renewal.

## Walled garden

**Menu: Walled garden**

URLs and IPs guests can reach before logging in. Keep this short — every entry is a potential way to bypass the captive portal.

Typical entries:

- Your hotel website (e.g. `coralsearesorts.com`)
- Your booking engine (so guests can check their reservation)
- Payment provider domains (auto-added when you enable Stripe)
- PMS provider domains (auto-added when you configure PMS)

**Don't** add general-purpose sites (Google, Facebook, CDNs) — it defeats the captive portal.

## Subscription / Plan

**Menu: Plan**

Shows your current plan, limits (max concurrent devices, max sites, etc.), and usage vs. limit. Click **Change plan** to upgrade/downgrade.

You or your `billing` operator can change plans. Downgrades take effect at the next billing cycle; upgrades are immediate.

## Audit log

**Menu: Audit log**

Every admin action in your tenant is logged: who, when, what changed. Use filters (date range, operator, entity) to investigate.

This log is the authoritative record for compliance and dispute resolution. Treat it as tamper-evident — don't try to edit it.

## Sessions (live monitoring)

**Menu: Sessions**

Who is connected right now. Per-row actions:

- **Disconnect** — boot the device off WiFi (they can log in again immediately unless you also revoke their voucher/OTP).
- **Quota reset** — grant more data if they've hit their cap.
- **Details** — device MAC, IP, data used, auth method, site, appliance.

Also useful as a "is WiFi working?" smoke test — if the count is zero and guests are present, something is wrong. See [common-tasks.md](common-tasks.md).

## Things you should NOT do

- Don't share your login with your staff. Create them an operator account.
- Don't add `*` or broad wildcards to the walled garden "to be safe" — it unlocks the internet pre-auth.
- Don't edit PMS credentials during business hours without testing first — a broken PMS integration means no one can log in to WiFi in rooms.
- Don't delete sites with active appliances — detach appliances first so sessions terminate cleanly.

## Escalation

- **Guest can't connect** → [common-tasks.md](common-tasks.md#a-guest-cant-log-in)
- **Appliance offline >30 min** → check the site's internet uplink, then contact StayConnect support
- **PMS integration broken** → test from PMS providers page first; if it's a PMS-side issue, contact your PMS vendor
- **Billing / plan question** → your platform admin contact at StayConnect
