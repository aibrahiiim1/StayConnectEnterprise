# OneGate Central — Page-by-Page Reference

**OneGate Central** (also called the Control Panel or Cloud Admin) is the vendor's console in the cloud. It
answers five questions: *who is the customer, which site and appliance, is it activated, what does its licence
allow, and is it healthy right now.* **Central is used for licensing, activation and fleet status only.** It
never configures a hotel's guest networks, sign-in methods, packages or guests, and it cannot control an
appliance remotely — those are run from **OneGate Hotel Admin** on each appliance (see
[hotel-admin-reference.md](hotel-admin-reference.md)).

The binding description of Central — states, lifecycle, API and roles — is
[CENTRAL_CONTROL_PLANE.md](../CENTRAL_CONTROL_PLANE.md). This document describes every page: what it is for, what
it shows and what an operator can do. For step-by-step instructions see
[control-panel-config-manual.md](control-panel-config-manual.md). The visual language is defined in the
[OneGate design system](../../design-system/README.md). The console spells it *License*; this guide uses the
console's words for buttons and labels.

---

## Things that apply to every page

- **Sign-in.** The login page is titled *OneGate Central*: email and password only. A Central login opens
  nothing on an appliance, and a Hotel Admin login does not work here. If the session has ended you are returned
  to the login page.
- **The sidebar.** Five items: **Overview · Customers · Appliances · Licenses · System**. The button beside the
  OneGate mark collapses it to an icon rail and expands it again; the choice is remembered in that browser. On a
  narrow window the menu is a drawer behind the ☰ button. Your email and **Sign out** are at the bottom.
- **No customer selector.** Every page is either fleet-wide or reached by opening a customer or an appliance.
  Lists filter by customer and site where that helps (Appliances, Audit log).
- **Customer users.** Someone with a customer role (Customer owner, Customer admin, Auditor, Viewer) sees only
  their own customer: Overview, Appliances and Licenses are limited to it by the server, **Customers** opens
  their own customer page directly, and System is not shown.
- **Top bar.** Shows where you are (*Section / Page*) and the theme switch: **Light**, **Dark** or **System**.
- **Who can do what is decided by the server** (roles in [CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles)).
  The console only hides buttons your role could never use. Activation and licence actions are for platform
  admins only.
- **Password confirmation.** Every activation, licence, appliance and delete action is protected by the server.
  When you have not confirmed your password recently, a **Confirm your password** dialog appears and the action
  continues once you enter it.
- **Confirmation dialogs.** Every destructive or licence-changing action opens a dialog that says what will
  happen and asks for a **reason** for the audit log. Deletes and retirement also ask you to **type** the
  customer's name, the site's name or the appliance's serial. Results appear as short notifications.
- **Old addresses.** Bookmarks to the old console pages (`/dashboard`, `/tenants`, `/sites`, `/onboarding`,
  `/operators`, `/security`, `/certificates`, `/assignment-keys`, `/backup-health`, `/audit`, `/commercial`,
  `/subscription`) redirect permanently to the page that replaced them.

## States you will see

| | Values |
|---|---|
| **Activation** | *Waiting for activation* (registered itself, needs an operator) · *Activating* (activated here; the appliance collects its certificate and licence on its next contact, normally within a minute) · *Activated* · *Retiring* (retirement signed, waiting for the appliance to confirm) · *Retired* (credentials dead; only the record remains) |
| **Connection** | *Connected* (contacted Central in the last 5 minutes) · *Recently seen* (last 24 hours) · *Offline* (more than a day; guests are not affected) · *Never connected* |
| **License** | *No license* · *Active* · *Expiring* (30 days or fewer left) · *In grace period* (past its end date, still working until the grace days end) · *Expired* · *Suspended* · *Revoked* · *Replaced* (an older version in the licence history) |

Central computes these; the console never works a state out for itself.

---

## Overview — `/overview`

The fleet at a glance. Counts of customers, appliances by **Activation**, by **Connection** and by **Licenses**
state; every number is a link to the list filtered to it.

- **Needs attention:** appliances waiting for activation, licences expiring, in grace, expired or suspended,
  appliances offline, open security alerts, and retirements the appliance has not confirmed. Each entry links to
  the appliance. *Nothing needs attention* when the list is empty.
- **Actions:** none — it links to the lists.

## Customers — `/customers`

The organisations that own hotels.

- **Shows:** Active / Archived / All, search, and the table Customer, Sites, Appliances, Active licenses, Needs
  attention.
- **New customer:** Name. (A customer can also be created inline while activating an appliance.)

### A customer — `/customers/[id]`

Header actions: **Rename**, **Archive** / **Restore**, **Delete** (typed name + reason; refused while the
customer still has sites or appliances). Tabs:

- **Summary** — sites, appliances, active licences, what needs attention, customer since, status.
- **Sites** — a site is one physical property. **New site**: Name, Time zone, Country (optional), Short code
  (optional). Row actions **Edit**, **Archive** / **Restore**, **Delete** (typed confirmation + reason).
  Buildings, floors, SSIDs and guest networks are configured on the appliance, not here.
- **Appliances** — this customer's appliances (same columns as the Appliances page).
- **Licenses** — this customer's licences.
- **Users** — the customer's own Central sign-ins (optional). **Add user**: Email, Name, Initial password,
  Role (**Customer admin**, **Auditor (read only)**, **Viewer (read only)**). Row actions **Role**,
  **Disable** / **Enable**, **Remove**. Each of these asks you to confirm your own password first.
- **Activity** — the audit log for this customer.

## Appliances — `/appliances`

Every appliance across customers. Appliances **waiting for activation** are listed first.

- **Filters:** Activation (All · Waiting · Activating · Activated · Retiring · Retired, with counts), search by
  serial or hostname, Connection, License, Customer and Site.
- **Table:** serial and hostname, customer and site, activation, connection, licence and last contact.
- **Import activation request** — for an appliance without internet: upload the activation-request file saved
  in its Hotel Admin (*Appliance & licence → Files from your OneGate vendor → Download activation request*). It then appears as *Waiting for activation*.
- There is no manual "new appliance" and no enrollment token: appliances arrive by registering themselves (or by
  an imported activation request).

### An appliance — `/appliances/[id]`

Everything about one appliance and every lifecycle action on it (all platform-admin only).

- **Status** — activation, with the action that fits it:
  - *Waiting for activation* → **Activate**: *Where it is installed* (Customer — existing or **New customer…**;
    Site — existing or new, with time zone) and *License* (**Guests online at once**, **Valid for** a number of
    days or **Until a date**, **Grace period (days)**). One step signs the assignment and issues the licence.
  - *Activating* → **Activation package** (offline sites): the signed file (assignment + CA + licence) to upload
    in the appliance's Hotel Admin. Valid for 7 days.
  - *Retiring* / *Retired* / *Marked for replacement* → what is happening and what is left to do.
- **License** — current terms (guests online at once, valid until, grace period), **Issue license** or **Renew
  or change** (always a new signed version that replaces the current one), **Suspend** / **Resume**,
  **Revoke** (permanent; set a new licence to restore service) and **Offline license file**. **License
  history** lists every version. Suspending or revoking a licence does not cut the appliance off from Central.
- **Activity** — recent lifecycle and audit events.
- **Installed at** — customer and site; **Move** re-assigns an activated appliance to another site **of the same
  customer** (new site + reason). Its licence is re-issued for the new site with the same terms in the same step;
  if licensing is unavailable on Central the move is refused and nothing changes, and a licence past its end date
  must be renewed first. To give an appliance to another customer: **Retire** it, factory-reset it, and
  **Activate** it for that customer when it registers again.
- **Appliance** — connection, registered, activated, last address, software version; **Retire appliance**
  (typed serial + reason; the licence is revoked at once; two-phase: it is retired once the appliance confirms,
  normally within a minute, and its credentials stay valid until then; not confirmed within 10 minutes → an
  alert, and *retire it now without waiting* finishes it; the *Emergency* option, for a lost, stolen or dead appliance, does not
  wait) or, for a *Waiting* or *Retired* appliance, **Delete record** (typed serial + reason; audit history is
  kept; a retired appliance's identity can never register again — factory-reset the box first).
- **Advanced** (collapsed) — *Repair*: **Reissue certificate**, **Rebind WAN MAC** (after a network-card change;
  licence terms stay the same), **Mark for replacement** (this appliance keeps working, for up to 72 hours;
  activating the new appliance at the same site revokes this one's licence and retires it through the same
  acknowledged two-phase retirement), **Offline activation package**; and *Technical details* (appliance ID, MACs, hardware and identity
  key fingerprints, certificate, assignment version and signing key, licence version).

## Licenses — `/licenses`

Every appliance's current licence, filterable by state (Active, Expiring, In grace period, Expired, Suspended,
Revoked) and searchable by serial or customer. Select one to act on it on its appliance's page. A licence is
issued when an appliance is activated; there are no plans or subscriptions.

---

## System — `/system/...`

Out of the daily workflow; platform roles only.

### Security alerts — `/system/security-alerts`
Suspicious appliance registrations: *Known appliance on different hardware* (e.g. a cloned disk), *Hardware
already in use*, *WAN MAC does not match its license*. A clone or reused-hardware registration is refused, so that box never appears as waiting.

- **Shows:** Open / Closed / All, search, and When, What, Appliance, From address, Details, Status.
- **Actions:** **Investigate**, **Acknowledge**, **Resolve** and **False positive** (each of these two asks for
  a reason), **Reopen**.

### Trust & keys — `/system/trust`
Read only. **Certificate authority** (its root key is kept offline), **Key registry** (the signed list of
assignment keys appliances accept: version, signed), **Assignment signing keys** (state, appliances signed, in
use since, fingerprint) and **Appliance certificates** (state, expiry, fingerprint; searchable). To reissue one
appliance's certificate use **Advanced** on its page. Setting a signing key to verify-only or revoking it is a
host command on Central (`ctrlapi assignment-key verify-only|revoke`), see
[ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md](../ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md).

### Audit log — `/system/audit`
Every change made in Central and by the appliances, newest first, 50 at a time (**Show older**). Filters:
Customer, Action (e.g. `license.revoked`), From. Entries cannot be edited or removed.

### Team — `/system/team`
The people who run Central. **Add team member**: Email, Name, Initial password, Role (**Platform admin** —
everything, including activation and licences; **Support (read only)**). Row actions **Role**, **Password**,
**Disable** / **Enable**, **Remove**. Every one of these asks you to confirm your own password first. A role
change takes effect at the next sign-in. A customer's own users are managed on that customer's **Users** tab, with
the same password confirmation.

### Backup health — `/system/backup-health`
Whether Central's own backup and rollback storage is healthy. Read only: *Disk used*, *Rollback path*, *Last
cleanup*, *Failures*; the retention policy; lists *Protected*, *Operator-pinned* (when any), *Retained* and
*Delete candidates*.
