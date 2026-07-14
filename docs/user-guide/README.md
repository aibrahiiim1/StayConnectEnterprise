# StayConnect Admin — User Guide

This guide explains how to use the StayConnect admin console for each operator role. Start with the section that matches your role; each page is task-oriented ("how do I…") rather than a feature reference.

## Complete references & configuration manuals

If you want a **page-by-page reference** (what every screen shows and does) or a
**step-by-step configuration manual**, use these:

| Document | What it covers |
|---|---|
| [control-panel-reference.md](control-panel-reference.md) | Every **Control Panel** (Cloud Admin) page — Dashboard, Sites, Appliances, Onboarding, Fleet, Customers, Licenses, Operators, Security alerts, Certificates, Assignment keys, Backup health, Audit |
| [control-panel-config-manual.md](control-panel-config-manual.md) | How to create a **Customer → Site → Appliance → License** and run day-2 operations on the Control Panel |
| [hotel-admin-reference.md](hotel-admin-reference.md) | Every **Appliance Hotel Admin** page — Dashboard, Access plans, Vouchers, Sessions, PMS/Notifications/Social/Payments, Walled garden, Branding, Operators, WAN/LAN, Cloud, TLS, Setup/Enrollment, Guest networks, DHCP, Config history, Diagnostics, License, Backups, Audit |
| [hotel-admin-config-manual.md](hotel-admin-config-manual.md) | How to **enroll, activate, and fully configure** an appliance from Hotel Admin — networking, guest VLANs, auth methods, vouchers, integrations, operators |

The role-based guides below are shorter, task-oriented walkthroughs for each role.

## Who should read what

| Role | Read this | In one sentence |
|---|---|---|
| **Platform admin** | [platform-admin.md](platform-admin.md) | You run the whole StayConnect platform. You create tenants and assign each tenant's first admin. |
| **Tenant admin** | [tenant-admin.md](tenant-admin.md) | You run a hotel chain / brand / property group on StayConnect. You manage sites, appliances, staff, PMS, subscription. |
| **Tenant operator** | [tenant-operator.md](tenant-operator.md) | You do day-to-day WiFi operations for one property — voucher batches, guest sessions, walled garden. |
| **Viewer** | [viewer-and-billing.md](viewer-and-billing.md#viewer) | You can look at everything in your tenant but not change anything (auditors, support, read-only stakeholders). |
| **Billing** | [viewer-and-billing.md](viewer-and-billing.md#billing) | You can view the tenant and change the subscription plan. Nothing else. |

If a guest (hotel WiFi user) can't log in and you're trying to help them, jump straight to [common-tasks.md](common-tasks.md#a-guest-cant-log-in).

## Accessing the admin console

- **URL**: `https://admin.stayconnect.local/` (or whatever hostname your platform admin gave you)
- **First-time login**: use the email + password your platform admin or tenant admin gave you. You'll be asked to set a new password.
- **Session**: stays logged in for the browser session. Click **Sign out** at the bottom left when done.

The left-hand menu shows up to 14 items. You'll only see the ones your role can use. Items you can't use either won't appear in the menu or will show a read-only view.

## What each menu item does

| Menu item | What it is |
|---|---|
| **Dashboard** | Live summary: sessions, appliances online, recent alerts |
| **Sites** | Physical locations (a hotel, a building, a floor) |
| **Appliances** | The StayConnect gateway devices installed at each site |
| **Ticket templates** | Email/print templates guests receive when they log in via OTP |
| **Voucher batches** | Bulk-generated codes (e.g. 500 vouchers for conference week) |
| **Sessions** | Active guest WiFi sessions — who is connected right now |
| **PMS providers** | Property Management System integrations (Mews, Opera, etc.) |
| **Notifications** | Email/SMS provider settings (SendGrid, Twilio, etc.) |
| **Social login** | "Log in with Google/Apple/Facebook" setup |
| **Payments** | Guest payments (paid WiFi plans) via Stripe |
| **Plan** | Your StayConnect subscription |
| **Operators** | Staff accounts — who can log into this admin console |
| **Walled garden** | URLs guests can reach *before* logging in (captive portal, hotel website) |
| **Audit log** | Every action taken in the admin — who did what, when |

## Glossary

- **Tenant** — A customer account (e.g., "Coral Sea Resorts"). Has its own sites, staff, subscription.
- **Site** — A physical property belonging to a tenant (e.g., "Coral Sea Resort Hurghada").
- **Appliance** — A StayConnect gateway installed at a site. Handles DHCP, captive portal, firewall, and sessions.
- **Session** — One guest device currently connected to WiFi.
- **Voucher** — A pre-printed code a guest types in to get online. Usually time-limited.
- **PMS** — Property Management System (Mews, Opera, Protel, etc.). Guests log in with their room + name, and StayConnect checks the PMS for an active reservation.
- **Walled garden** — Hostnames/IPs the guest can reach *before* authenticating. Keep it small.

---

Next: pick your role from the table at the top and open that file.
