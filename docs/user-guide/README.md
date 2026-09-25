# Velonet — User Guide

This guide explains how to use Velonet's two operator consoles for each operator role:
**Velonet Central** (the vendor's console in the cloud, used for licensing) and **Velonet Hotel Admin** (the
console on the appliance at each hotel). Start with the section that matches your role; the role pages are
task-oriented ("how do I…") rather than a feature reference.

> **New to Velonet?** Read the
> [Complete Operations Manual](../STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md) first —
> it takes a hotel from an unpacked appliance to live, licensed guest Wi-Fi. The full
> documentation index is at [docs/README.md](../README.md).
>
> **Current model:** onboarding is **zero-touch** (the appliance registers itself → *Pending
> activation* → one-click **Activate** in Central); the only entitlement is the **signed appliance
> license** (max concurrent online guests + validity + grace period + entitled features). Central is used
> for licensing only; the hotel's networks, sign-in methods, packages and guests are run from Hotel Admin.
>
> **Design:** both consoles and the guest portal share the
> [Velonet design system](../../design-system/README.md).

## Complete references & configuration manuals

If you want a **page-by-page reference** (what every screen shows and does) or a
**step-by-step configuration manual**, use these:

| Document | What it covers |
|---|---|
| [control-panel-reference.md](control-panel-reference.md) | Every **Velonet Central** page — Dashboard, Sites, Onboarding, Appliances, Customers, Licenses, Operators, Security alerts, Certificates, Assignment keys, Backup health, Audit log |
| [control-panel-config-manual.md](control-panel-config-manual.md) | How to create a **Customer → Site → Appliance → License** and run day-2 operations in Central |
| [hotel-admin-reference.md](hotel-admin-reference.md) | Every **Velonet Hotel Admin** page, group by group — Overview; Internet offering; Guests; Property management system; Charges; Guest portal; Networking; System |
| [hotel-admin-config-manual.md](hotel-admin-config-manual.md) | How to **activate and fully configure** an appliance from Hotel Admin — networking, guest networks, sign-in methods, packages, vouchers, PMS, portal, operators |
| [guest-portal.md](guest-portal.md) | What **guests** see on the Wi-Fi sign-in page, and which Hotel Admin settings control it |

The role-based guides below are shorter, task-oriented walkthroughs for each role.

## Who should read what

**Velonet Central** (vendor staff and hotel-group admins):

| Role | Read this | In one sentence |
|---|---|---|
| **Platform admin** | [platform-admin.md](platform-admin.md) | You run Velonet Central for all customers: you create customers, activate appliances and issue licenses. |
| **Customer admin** (tenant admin) | [tenant-admin.md](tenant-admin.md) | You look after one hotel group's sites, appliances and Central operators, and read its licenses. |
| **Customer operator** (tenant operator) | [tenant-operator.md](tenant-operator.md) | You do day-to-day Central work for one hotel group, and usually run its hotels' Hotel Admin too. |
| **Viewer** | [viewer-and-billing.md](viewer-and-billing.md#viewer) | You can look at everything for your customer but not change anything. |
| **Billing** | [viewer-and-billing.md](viewer-and-billing.md#billing) | You can view your customer's licenses and usage. Nothing else. |

**Velonet Hotel Admin** (hotel staff) has seven roles of its own — Site admin, Hotel IT manager, Front office
operator, Guest relations operator, Voucher operator, Payments operator and Site viewer. Which pages each can
use is listed in [hotel-admin-reference.md](hotel-admin-reference.md#who-can-use-which-page).

If a guest can't get online and you're trying to help them, jump straight to
[common-tasks.md](common-tasks.md#a-guest-cant-log-in).

## Accessing the consoles

- **Velonet Central**: the Central address your platform admin gave you. Sign in with your Central
  operator account (or your organisation's single sign-on). A Central account does not open any appliance.
- **Velonet Hotel Admin**: `https://` + the appliance's management address on the hotel network. Sign in
  with the account created for you on that appliance; it works only at that property.
- **Session**: both consoles re-check your session every 30 seconds. Click **Sign out** at the bottom of the
  sidebar when done.
- **Theme**: both consoles offer Light, Dark and System (the default) in the top bar.

You only see the menu items your role can use; pages you can read but not change show a read-only notice
and no action buttons.

## What each menu item does

**Velonet Central** — four groups:

| Group | Pages |
|---|---|
| **Overview** | Dashboard — licenses by state; usage for one customer |
| **Infrastructure** | Sites (one per hotel) · Onboarding (activate pending appliances) · Appliances (list and recovery tools) |
| **Commercial** | Customers (hotel groups) · Licenses (issue, renew, suspend, revoke) |
| **Administration** | Operators · Security alerts · Certificates · Assignment keys · Backup health · Audit log |

**Velonet Hotel Admin** — eight groups:

| Group | Pages |
|---|---|
| **Overview** | Overview — the shift view |
| **Internet offering** | Internet packages · Service plans · Checkout grace · Vouchers |
| **Guests** | Stays · Guest accounts · Active sessions · Usage explorer · Guest devices · Online-time budgets · Post-stay access |
| **Property management system** | PMS connection · Network routing · PMS activity · Guest sign-in checks · Guest sign-in attempts · Duplicate sources · Cross-PMS transfer |
| **Charges** | Charge health · Manual review · Settlements · Recovery |
| **Guest portal** | Sign-in methods · Portal settings · Allowed sites · Social login · Email & SMS |
| **Networking** | Guest networks · DHCP & leases · WAN / LAN settings · Config history · TLS certificate |
| **System** | Diagnostics · Alerts · Activity · Appliance & licence · Backups · Operators |

## Guest portal

The **guest portal** is the page that opens on a guest's device when it joins the hotel Wi-Fi. Guests have
no account; they:

- **sign in** with their room number plus a name or reservation number, a voucher code, a personal account
  created by staff, a one-time code sent by email or SMS, a social account (Google, Apple, Facebook), or a
  post-stay PIN after checkout;
- see short **messages** when something is wrong — worded so that room sign-in never reveals whether a room
  exists or who is in it;
- **choose a package** when more than one internet package is available to them;
- land on **"You're online"** with the time remaining, a Disconnect button and, where enabled, the time left
  on an online-time budget and a list of their devices.

The portal speaks **six languages** — English, Arabic (right to left, fully mirrored), German, French,
Italian and Russian — chosen from the guest's device or the language selector, and it comes in **six layout
templates**: Classic, Split, Immersive, Header bar, Resort and Kiosk.

In Hotel Admin, **Guest portal → Sign-in methods** controls which methods are offered (and what a guest types
for room sign-in, and how many wrong tries are allowed), and **Guest portal → Portal settings** controls the
template, brand, wording, languages and custom CSS/HTML. Details: [guest-portal.md](guest-portal.md).

## Glossary

- **Customer** — The hotel group or company that owns sites (e.g., "Coral Sea Resorts").
- **Site** — One physical property (one hotel or resort). Buildings, floors and SSIDs are not sites.
- **Appliance** — The Velonet gateway at a site. Hands out addresses, shows the sign-in page, enforces speed
  and time limits and talks to the PMS. It does not broadcast Wi-Fi; the hotel's access points do.
- **Activate / Pending activation** — A new appliance registers itself and waits as *Pending activation*
  until an operator activates it in Central.
- **License** — The signed appliance license: max concurrent online guests, valid from/until, grace period
  and entitled features. The only entitlement.
- **Max concurrent online guests** — How many guests may be online at once across the whole appliance.
- **Grace period** — Days after a license expires during which guests are still served, with warnings.
- **Guest network** — A Wi-Fi network for guests, carried on a VLAN, with its own addresses and portal.
- **Internet package** — What a guest is offered; uses one service plan plus rules on who gets it and for
  how long.
- **Service plan** — The technical recipe behind a package: speed, devices, data, time, timeouts.
- **Stay** — One reservation in the PMS: room, guests, arrival, departure.
- **PMS** — The hotel's property management (reservation) system, e.g. Protel.
- **Voucher** — A printed card with a code that gives an internet package.
- **Guest account** — A username and password created by staff for a guest.
- **Session** — One device's period online.
- **Allowed sites** (walled garden) — Addresses a guest device can reach *before* signing in. Keep it small.
- **Operator** — A staff login (Central or Hotel Admin; the two are separate).
- **Password confirmation** — Re-entering your own password for a sensitive action.

---

Next: pick your role from the table at the top and open that file.
