# Velonet Central — Page-by-Page Reference

**Velonet Central** (also called the Control Panel or Cloud Admin) is the vendor's console in the cloud. It
registers and licenses every appliance: which customer owns it, which site it is at, how many guests it may
serve at once, and until when. **Central is used for licensing only.** It never configures a hotel's guest
networks, sign-in methods, packages or guests — those are run from **Velonet Hotel Admin** on each appliance
(see [hotel-admin-reference.md](hotel-admin-reference.md)).

This document describes every page in the menu: what it is for, what it shows, what an operator can do, and
which actions ask for a reason, password confirmation, typed confirmation or a one-time reveal. For
step-by-step instructions see [control-panel-config-manual.md](control-panel-config-manual.md). The visual
language is defined in the [Velonet design system](../../design-system/README.md).

---

## Things that apply to every page

- **Sign-in.** The login page is titled *Velonet Central*: email and password, and a collapsed **single
  sign-on** option that asks for an **Organisation slug** and then lists that organisation's providers. A
  Central login opens nothing on an appliance, and a Hotel Admin login does not work here. The session is
  re-checked every 30 seconds; if it has ended you are returned to the login page.
- **The sidebar.** Four groups: **Overview · Infrastructure · Commercial · Administration**. The button
  beside the Velonet mark collapses it to an icon rail (tooltips show each name) and expands it again; the
  choice is remembered in that browser. On a narrow window the menu is a drawer behind the ☰ button. Your
  email and **Sign out** are at the bottom. Central has no "Find a screen…" filter; its menu is short.
- **Customer context.** Under the Velonet mark, a platform admin chooses **All customers** or one customer.
  The choice is remembered across pages and refreshes. **Dashboard, Sites, Appliances, Licenses, Operators
  and Audit log** follow it; each page repeats which customer it is showing under its title.
  - In **All customers** mode, Sites, Appliances and Licenses list every customer's rows (with a Customer
    column). Creating appliances and licenses is disabled until you pick a customer; **New site** asks you to
    choose the owning customer in the form. Operators and Audit log show a *"Select a customer"* card
    instead of data.
  - Customer-level operators have no selector: they are always on their own customer, and the server
    enforces it.
- **Top bar.** Shows where you are (*Group / Page*) and the theme switch: **Light**, **Dark** or **System**
  (the default, following your computer). The choice is remembered in that browser.
- **Who can do what is decided by the server.** Central does not hide buttons by role (apart from the
  customer selector and the fleet license summary, which are for platform admins). If your role may not
  perform an action, the server refuses it and the page shows the error.
- **Password confirmation.** License, certificate and appliance actions (issue, renew, suspend, resume,
  revoke or download a license; activation packages; deactivating; the Advanced Support actions; deletes)
  are protected by the server. When you have not confirmed your password recently, a **Confirm your
  password** dialog appears, and the action continues once you enter it. **Activate** on Onboarding always
  has its own password field.
- **Confirmation dialogs.** Every destructive or license-changing action opens a dialog listing what will
  happen. The heaviest deletes also require a **typed confirmation** (the customer's name, the site's code
  or the appliance's serial) and a **reason** for the audit log; see *The Delete dialog* at the end. No
  action uses a browser pop-up; results appear as short notifications.
- **One-time reveals.** An enrollment token is shown **once**, with **Copy** and an acknowledgement.
- **Live figures.** Onboarding and Appliances refresh on their own and show **"Updated x ago"** with a
  refresh button.

---

## OVERVIEW

### Dashboard — `/dashboard`
Fleet health at a glance: licenses issued by state and, for one customer, how busy their sites are.

- **Fleet license summary** (platform admins): *Active*, *Expiring in 30 days or less*, *Expired*,
  *Suspended*, *Revoked*, and *Orphaned* (a license whose appliance or site was deleted). A link opens
  Licenses.
- **Tiles:** *Active sessions*, *Data this month*, *Sessions today* (these need one customer selected; in
  All customers mode they say *"Select a customer to see this"*) and *Licensed appliances*.
- **Top sites (this month):** the five sites that used the most data, for the selected customer.
- The session and data figures come from usage that appliances report to Central. Appliances are connected
  for licensing only, so do not expect these figures to be complete; a hotel's own figures are on
  **Hotel Admin → Overview**.
- **Actions:** none.

---

## INFRASTRUCTURE

### Sites — `/sites`
A site is one physical property — one hotel or resort. It belongs to exactly one customer and holds one or
more appliances. Buildings, floors, SSIDs and guest networks are configured on the appliance, not here.

- **Shows:** search, a status filter (Active / Archived), and the table Customer (All customers mode), Code,
  Name, Status, Timezone, Country, Created.
- **New site:** Owning customer, Code (short and unique), Name, Timezone (UTC if empty), Country (optional).
- **Row actions:** **Edit** (name, timezone, country; the code cannot change), **Archive / Restore**,
  **Delete** — the Delete dialog: **type the site code + reason**, password confirmation when asked. Blocked
  while the site still holds appliances or licenses.

### Onboarding — `/onboarding`
Connect an appliance. A factory-clean appliance with internet registers itself and waits here as **Pending
activation**; you select it, choose its customer, site and license terms, and activate it once.

- **Pending activation** (refreshes every 5 seconds): Serial, WAN MAC, Model, Source IP, First seen. Select a
  row to open the activate form.
- **Activate** form: **Customer** (existing, or type a new name), **Site** (existing, or type a new name),
  **Max concurrent online guests** (0 = unlimited; across the whole appliance), **Valid until** (empty = 365
  days), **Grace period (days)** (after expiry guests are still served, with warnings), **Confirm your
  password**, then **Activate**.
- **Progress:** **Detected → Activating → Appliance converging → Active**, then **Activate another**.
- **Offline activation:** for an appliance with no route to Central, upload the activation request file the
  appliance saved (*Hotel Admin → Appliance & licence → Offline*). It then appears as pending; activate it as
  usual, then download its activation package (below) and carry it back.
- **Registered appliances:** Serial, State, WAN MAC, and per row:
  - **Deactivate** — confirmation dialog: its license is revoked, new guest sign-ins are refused, existing
    guest sessions are not dropped; it can be activated again later. Password confirmation when asked.
  - **Activation package** — downloads the signed file that completes an offline activation (valid 7 days,
    single use), to upload in Hotel Admin under Appliance & licence. Password confirmation when asked.
  - **Delete** — the Delete dialog with an impact preview: **type the appliance serial + reason**. A
    factory-clean appliance will then register again as pending.
  - With **Advanced Support** switched on: **Reissue cert**, **Reconcile**, **Decommission** — each asks for a
    **reason** (recorded) and password confirmation when asked.

### Appliances — `/appliances`
Every appliance, where it is and whether it is online. Appliances normally arrive by themselves under
Onboarding; the tools here are for recovery.

- **Shows:** tiles *Appliances*, *Online*, *Enrolled or pending*, *Offline or other*; search; table Customer
  (All customers mode), Name, Site (or *unassigned*), Serial, Status (online with a live dot when heard from
  recently), Version, Last seen. The list refreshes on its own.
- **Header (needs a customer and a site):**
  - **Enrollment token** — only for an appliance that cannot register itself: Site, Serial (optional; locks
    the token to one appliance), Valid for 1–168 hours (default 24). The token is a **one-time reveal**:
    enter it in the appliance's Hotel Admin under *Appliance & licence → Advanced / recovery*.
  - **New appliance** — manual registration: Site, Serial, Name, Model.
- **Row actions:** **Config** — a read-only view of the PMS connections and allowed-site rules Central holds
  for that appliance's site; **Delete** — **type the appliance serial** to confirm.
- **Enrollment tokens** table: Hint, Site, Serial lock, Status, Expires, Created; **Revoke** an unused token
  (confirmation dialog).

---

## COMMERCIAL

### Customers — `/tenants`
The hotel groups and companies that own sites. Order of work: Customer, then Site, then activate an
Appliance, which issues its License.

- **Shows:** search, a status filter (Active / Archived), and the table Slug, Name, Status, Created.
- **New customer:** Slug (lower-case, unique, also used for single sign-on) and Name.
- **Row actions:** **Rename**; **Archive** (confirmation dialog: hidden from active lists, everything kept)
  and **Restore**; **Delete** — the Delete dialog: **type the customer name + reason**, blocked while the
  customer still has sites, appliances or licenses.

### Licenses — `/licenses`
Each appliance's signed license: max concurrent online guests, validity window and grace period. The license
is the only entitlement.

- **Shows:** tiles *Active*, *In grace*, *Expired or revoked*, *Awaiting appliance binding*; search and a state
  filter; table Customer, Site, Appliance (serial or *not bound*), Version, Status, Online / Limit (∞ when
  unlimited), Usage, validity, grace ends, Last sync.
- **Issue license** (needs a customer selected and a site): Site, Appliance, **Max concurrent online guests**
  (0 = unlimited), Grace period (days), Valid from (empty = now), Valid until (empty = 365 days).
- **Row actions** (each with password confirmation when asked):
  - **Renew** — new max guests, days from now and grace days. Issues a new signed version; the previous one
    becomes *Superseded* and can never be used again.
  - **Download for offline** — the signed, appliance-bound file, to upload in Hotel Admin under Appliance &
    license.
  - **Suspend** — confirmation dialog: new guest sign-ins stop; existing sessions are not dropped; the portal,
    DHCP, DNS and Hotel Admin stay up. **Resume** reverses it.
  - **Revoke** — confirmation dialog: permanent; the appliance refuses new guest sign-ins; existing sessions
    are not dropped; issue a new license to restore service.

License states: **Active**, **Grace** (past valid-until, inside the grace days; guests still served with a
warning), **Expired**, **Suspended**, **Revoked**, **Superseded** (replaced after Renew), **Awaiting appliance
binding**.

---

## ADMINISTRATION

### Operators — `/operators`
A customer's own staff sign-ins to Central, and their roles. Requires a customer to be selected.

- **Shows:** tiles *Operators*, *Active*, *Invited*, *Disabled*; search; table Email (*you* marker), Name,
  Status, Roles.
- **New operator:** Email, Display name, Initial password (at least 10 characters), Role (**Customer admin**,
  **Customer operator**, **Viewer**, **Billing**).
- **Row actions:** **Add a role** (dialog), remove a role (confirmation dialog), **Reset password** (new
  password, at least 10 characters), **Disable** (confirmation dialog). You cannot disable yourself.

### Security alerts — `/security`
Raised when an appliance registration looks wrong — a cloned identity, a reused serial, or a WAN MAC that
does not match the signed license. Activation is blocked while an alert is open.

- **Shows:** tiles *Open*, *Investigating*, *Acknowledged*, *Resolved or false positive*; search and **Show
  resolved**; table When, Kind, Serial, Source IP, Detail, Status.
- **Actions:** **Investigate**, **Acknowledge**, **Resolve** and **False positive** (each of the last two asks
  for a **reason**), **Reopen**.

### Certificates — `/certificates`
Appliance certificates issued by Central's certificate authority. Read-only, metadata only.

- **Shows:** tiles *Active*, *Expiring in 30 days*, *Expired*, *Revoked*; search and **Show superseded**; table
  Appliance, Customer, Site, Fingerprint, Issuer, Issued, Expires, Status, last rotation, revocation.
- **Actions:** none.

### Assignment keys — `/assignment-keys`
The keys that sign the documents binding an appliance to its customer and site. Read-only.

- **Shows:** tiles *Active*, *Verify-only*, *Revoked*, *Current assignments*; table Key ID, Fingerprint, State,
  Rotation, Dependencies, Created, Retired, Reason.
- **Actions:** none.

### Backup health — `/backup-health`
Whether Central's own backup and rollback storage is healthy. Read-only.

- **Shows:** tiles *Disk used*, *Rollback path*, *Last cleanup*, *Failures*; the retention policy; lists
  *Protected*, *Operator-pinned* (when any), *Retained* and *Delete candidates*.
- **Actions:** none.

### Audit log — `/audit`
Who did what for the selected customer in the last 7 days. Entries are never edited or removed.

- **Shows:** table When, Actor, Action, Target, IP, Payload.
- **Filter by action** (comma-separated action names) and **Apply**. No paging or date range.

---

## Retired pages

`/commercial` and `/subscription` are not in the menu and are labelled *retired*. They belong to a pricing
model that is no longer part of Velonet; the signed appliance license is the only entitlement. Do not use
them.

---

## The Delete dialog (Customers, Sites, Onboarding)

Deleting a customer, a site or an appliance (from Onboarding) opens one dialog that:

1. states it **cannot be undone**;
2. lists anything that blocks the delete — delete never cascades, so remove bottom-up: **appliances → sites →
   customer**;
3. requires **typing** the customer's name, the site's code or the appliance's serial exactly;
4. requires a **reason**, recorded in the audit log;
5. asks for **password confirmation** when the server requires it.

Deleting an appliance from the **Appliances** page asks only for the typed serial.
