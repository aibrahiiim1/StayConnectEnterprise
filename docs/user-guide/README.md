# OneGate — User Guide

This guide explains how to use OneGate's two operator consoles for each operator role:
**OneGate Central** (the vendor's console in the cloud, used for licensing, activation and fleet status) and **OneGate Admin Console** (formerly Hotel Admin; the
console on the appliance at each site). Start with the section that matches your role; the role pages are
task-oriented ("how do I…") rather than a feature reference.

> **New to OneGate?** Read the
> [Complete Operations Manual](../STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md) first —
> it takes a site from an unpacked appliance to live, licensed guest Wi-Fi. The full
> documentation index is at [docs/README.md](../README.md).
>
> **Current model:** onboarding is **zero-touch** (the appliance registers itself, no token → *Waiting for
> activation* → one **Activate** step in Central); the only entitlement is the **signed appliance
> licence** (max concurrent online clients + validity + grace period). Central is used for licensing,
> activation and fleet status only ([CENTRAL_CONTROL_PLANE.md](../CENTRAL_CONTROL_PLANE.md)); the site's
> networks, sign-in methods, packages and clients are run from the Admin Console.
>
> **Design:** both consoles and the Client Portal (formerly Guest Portal) share the
> [OneGate design system](../../design-system/README.md).

## Complete references & configuration manuals

If you want a **page-by-page reference** (what every screen shows and does) or a
**step-by-step configuration manual**, use these:

| Document | What it covers |
|---|---|
| [control-panel-reference.md](control-panel-reference.md) | Every **OneGate Central** page — Overview, Customers (and the customer page), Appliances (and the appliance page), Licenses, System (Security alerts, Trust & keys, Audit log, Team, Backup health) |
| [control-panel-config-manual.md](control-panel-config-manual.md) | How to create a **Customer → Site**, **activate** an appliance, manage its **licence**, and run day-2 operations in Central |
| [hotel-admin-reference.md](hotel-admin-reference.md) | Every **OneGate Admin Console** page, group by group — Overview; Internet offering; Clients; Property management system; Charges; Client Portal; Networking; System |
| [hotel-admin-config-manual.md](hotel-admin-config-manual.md) | How to **activate and fully configure** an appliance from the Admin Console — networking, guest networks, sign-in methods, packages, vouchers, PMS, portal, operators |
| [guest-portal.md](guest-portal.md) | What **clients** see on the Wi-Fi sign-in page, and which Admin Console settings control it |

The role-based guides below are shorter, task-oriented walkthroughs for each role.

## Who should read what

**OneGate Central** (vendor staff and customer admins):

| Role | Read this | In one sentence |
|---|---|---|
| **Platform admin** (and read-only **Support**) | [platform-admin.md](platform-admin.md) | You run OneGate Central for all customers: you create customers, activate appliances and manage licences. |
| **Customer admin / Customer owner** (tenant admin) | [tenant-admin.md](tenant-admin.md) | You manage one customer's sites and Central users, and see its appliances and licences. |
| **Viewer / Auditor** | [viewer-and-billing.md](viewer-and-billing.md#viewer) | You can look at everything for your customer but not change anything. |
| **Customer operator**, **Billing** (retired) | [tenant-operator.md](tenant-operator.md) · [viewer-and-billing.md](viewer-and-billing.md#billing) | These roles grant nothing in Central any more; day-to-day site work is in the Admin Console. |

**OneGate Admin Console** (site staff) has seven roles of its own — Site admin, Site IT manager, Front office
operator, Client relations operator, Voucher operator, Payments operator and Site viewer. Which pages each can
use is listed in [hotel-admin-reference.md](hotel-admin-reference.md#who-can-use-which-page).

If a client can't get online and you're trying to help them, jump straight to
[common-tasks.md](common-tasks.md#a-client-cant-log-in).

## Accessing the consoles

- **OneGate Central**: the Central address your platform admin gave you. Sign in with your Central
  email and password. A Central account does not open any appliance.
- **OneGate Admin Console**: `https://` + the appliance's management address on the site network. Sign in
  with the account created for you on that appliance; it works only at that site.
- **Session**: when your session ends, either console returns you to its sign-in page. Click **Sign out** at
  the bottom of the sidebar when done.
- **Theme**: both consoles offer Light, Dark and System (the default) in the top bar.

You only see the menu items your role can use; pages you can read but not change show a read-only notice
and no action buttons.

## What each menu item does

**OneGate Central** — five items, no customer selector:

| Item | What it is for |
|---|---|
| **Overview** | Fleet counts and everything that needs attention |
| **Customers** | The organisations that own sites; each customer page has Sites, Appliances, Licenses, Users and Activity |
| **Appliances** | Every appliance (waiting ones first); each appliance page has Activate, licence actions, Move, Retire, Delete and Advanced |
| **Licenses** | Every licence, by state |
| **System** | Security alerts · Trust & keys · Audit log · Team · Backup health (platform staff only) |

**OneGate Admin Console** — eight groups:

| Group | Pages |
|---|---|
| **Overview** | Overview — the shift view |
| **Internet offering** | Internet packages · Service plans · Checkout grace · Vouchers |
| **Clients** | Stays · Client accounts · Active sessions · Usage explorer · Client devices · Online-time budgets · Post-stay access |
| **Property management system** | PMS connection · Network routing · PMS activity · Client sign-in checks · Client sign-in attempts · Duplicate sources · Cross-PMS transfer |
| **Charges** | Charge health · Manual review · Settlements · Recovery |
| **Client Portal** | Sign-in methods · Portal settings · Allowed sites · Social login · Email & SMS |
| **Networking** | Guest networks · DHCP & leases · WAN / LAN settings · Config history · TLS certificate |
| **System** | Diagnostics · Alerts · Activity · Appliance & licence · Backups · Operators |

## Client Portal

The **Client Portal** is the page that opens on a client's device when it joins the site's Wi-Fi. Clients have
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
Italian and Russian — chosen from the client's device or the language selector, and it comes in **six layout
templates**: Classic, Split, Immersive, Header bar, Resort and Kiosk.

In the Admin Console, **Client Portal → Sign-in methods** controls which methods are offered (and what a client types
for room sign-in, and how many wrong tries are allowed), and **Client Portal → Portal settings** controls the
template, brand, wording, languages and custom CSS/HTML. Details: [guest-portal.md](guest-portal.md).

## Glossary

- **Customer** — The organisation (for example a hotel group, company or institution) that owns sites (e.g., "Semantics").
- **Site** — One physical location (for example one hotel, resort, office or campus). Buildings, floors and SSIDs are not sites.
- **Appliance** — The OneGate gateway at a site. Hands out addresses, shows the sign-in page, enforces speed
  and time limits and talks to the PMS. It does not broadcast Wi-Fi; the site's access points do.
- **Activate / Waiting for activation** — A new appliance registers itself and waits as *Waiting for
  activation* until a platform admin activates it in Central.
- **License** — The signed appliance licence: max concurrent online clients, valid from/until and grace
  period. The only entitlement.
- **Max concurrent online clients** — How many clients may be online at once across the whole appliance.
- **Grace period** — Days after a license expires during which clients are still served, with warnings.
- **Guest network** — A Wi-Fi network for clients, carried on a VLAN, with its own addresses and portal.
- **Internet package** — What a client is offered; uses one service plan plus rules on who gets it and for
  how long.
- **Service plan** — The technical recipe behind a package: speed, devices, data, time, timeouts.
- **Stay** — One reservation in the PMS: room, guests, arrival, departure.
- **PMS** — The site's property management (reservation) system, e.g. Protel.
- **Voucher** — A printed card with a code that gives an internet package.
- **Client account** — A username and password created by staff for a client.
- **Session** — One device's period online.
- **Allowed sites** (walled garden) — Addresses a client device can reach *before* signing in. Keep it small.
- **Operator / user** — A staff login (Central or the Admin Console; the two are separate). In Central, vendor staff
  are the **Team** and a customer's own logins are its **Users**.
- **Password confirmation** — Re-entering your own password for a sensitive action.

---

Next: pick your role from the table at the top and open that file.
