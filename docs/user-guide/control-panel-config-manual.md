# OneGate Central — Configuration Manual

Step-by-step instructions for customers, sites, appliance activation and
licences in **OneGate Central** (also called the Control Panel or Cloud Admin).
For a description of what each page shows, see
[control-panel-reference.md](control-panel-reference.md); for the binding
description of states, lifecycle and API, see
[CENTRAL_CONTROL_PLANE.md](../CENTRAL_CONTROL_PLANE.md).

**The order is:** Customer → Site → activate the Appliance (which issues its
Licence). The customer and site can also be created inside the **Activate**
dialog, so steps 1 and 2 are optional.

> Every activation, licence, appliance and delete action may ask you to
> **confirm your password** in a dialog, and asks for a **reason**. Every step
> is recorded in the audit log. These actions are for platform admins only.

---

## 1. Create a Customer

A **customer** is the organisation that owns hotels (a hotel group, brand or
property owner).

1. Go to **Customers** (`/customers`).
2. Click **New customer**, enter the **Name**, click **Create customer**.

The customer page opens. Its tabs are **Summary · Sites · Appliances · Licenses
· Users · Activity**.

> To retire a customer later, use **Archive** (keeps everything). Use **Delete**
> only for a customer with nothing under it (see §6).

---

## 2. Create a Site

A **Site** is **one physical property** — a single hotel or resort — that
belongs to exactly one Customer and can contain one or more Appliances.
Buildings, floors, SSIDs and guest networks are configured on the appliance in
Hotel Admin; they are **not** Sites. A hotel with two buildings on one uplink is
still one Site.

1. Open the customer (**Customers** → the customer) and select the **Sites** tab.
2. Click **New site** and fill in **Name** (e.g. `Semantics Demo Hotel`),
   **Time zone** (e.g. `Africa/Cairo`), and optionally **Country** and **Short
   code**.
3. Save.

---

## 3. Activate an Appliance (zero-touch)

Use this when the appliance is installed, powered and has internet to Central.

1. Power the appliance on. It **registers itself** with Central — no token,
   nothing to type on the appliance — and keeps retrying until Central answers.
   Its Hotel Admin shows **Waiting for activation** and its serial number.
2. In Central, go to **Appliances** (`/appliances`). Appliances **waiting for
   activation** are at the top (or use the **Waiting** filter; the Overview's
   *Needs attention* list links to them too). Match the **serial**.
3. Open it and click **Activate**:
   - **Where it is installed** — **Customer** (existing, or **New customer…**)
     and **Site** (existing, or a new one with its time zone).
   - **License** — **Guests online at once** (e.g. `500`; the limit covers the
     whole appliance, across all guest networks), **Valid for** (a number of
     days, or **Until a date**), and **Grace period (days)** (how long it keeps
     working after the end date).
4. Confirm. The appliance shows **Activating**; within about a minute of its
   next contact it collects its signed assignment, certificate and licence and
   shows **Activated**. Hotel Admin shows *Activated* and the licence too
   (**Check now** there makes it contact Central immediately).

**What "Activate" does** in one step: assigns the appliance to the customer and
site with a signed assignment and issues a hardware-bound licence with the terms
you set. The certificate is issued automatically when the appliance asks for it.

### Offline activation (appliance with no route to Central)

1. In the appliance's Hotel Admin, **Appliance & licence → Files from your
   OneGate vendor → Offline activation**: **Download activation request** (shown
   while the appliance has never reached Central).
2. In Central, **Appliances → Import activation request**: upload that file.
   The appliance appears as **Waiting for activation**; activate it as above.
3. On the appliance's page (now **Activating**), click **Activation package**
   and carry the file back (valid 7 days).
4. In Hotel Admin, **Upload activation package** in the same place.

There are no enrollment tokens and no manual appliance creation.

---

## 4. Manage the Licence

A licence binds to **one appliance** and sets: guests online at once, the
validity window and the grace period. Activation issues the first one. Every
licence action is on the appliance's page (**Appliances** → the appliance, or
**Licenses** → select a row), in the **License** card:

- **Renew or change** (or **Issue license** when there is none) — guests online
  at once, valid for / until, grace period, reason. Always a **new signed
  version** that replaces the current one; the old version shows as *Replaced*
  in **License history**.
- **Suspend** — the licence is paused and guest access on the appliance stops
  until **Resume**.
- **Revoke** — permanent; set a new licence to restore service.
- **Offline license file** — the signed, appliance-bound file, to upload in Hotel
  Admin (**Appliance & licence → Files from your OneGate vendor → Upload licence
  file**) when the appliance cannot reach Central.

Suspending or revoking a licence never cuts the appliance off from Central: it
keeps contacting Central and picks up the change.

---

## 5. Move, replace, repair or retire an appliance

All on the appliance's page:

- **Move** (*Installed at*) — to another site. Moving to **another customer**
  revokes its licence and the appliance erases the previous customer's local
  data; set a new licence after the move.
- **Retire appliance** — type the serial and a reason. Retirement is two-phase:
  the appliance confirms, normally within a minute, then its credentials are
  revoked. Tick **Emergency** for a lost, stolen or dead appliance: it is retired
  without waiting.
- **Advanced → Repair:** **Reissue certificate**; **Rebind WAN MAC** after a
  network-card change (licence terms stay the same); **Mark for replacement**
  (activating a new appliance at the same site then retires this one);
  **Offline activation package**.
- **Delete record** — only for a *Waiting for activation* or *Retired*
  appliance: type the serial and a reason. Audit history is kept.

---

## 6. Fully remove a customer (clean teardown)

Delete never cascades, so remove bottom-up:

1. **Retire** every appliance of the customer, then **Delete record** for each.
2. **Delete** every site (customer page → **Sites** → delete → type the site
   name).
3. **Delete** the customer (customer page → **Delete** → type the customer
   name).

If a delete is refused, the dialog lists exactly what remains.

---

## 7. Day-2 operations quick reference

| I want to… | Go to | Do |
|---|---|---|
| See what needs attention | Overview | *Needs attention* list |
| Find appliances waiting for activation | Appliances | **Waiting** filter |
| See whether an appliance is connected | Appliances | Connection column / filter |
| Renew or change a licence | the appliance → License | **Renew or change** |
| See licences by state | Licenses | state filter |
| Add a Central operator | System → Team | **Add team member** |
| Give a customer its own read-only or admin login | the customer → Users | **Add user** |
| Investigate a clone / hardware alert | System → Security alerts | Investigate → Resolve / False positive |
| Check certificate expiry and signing keys | System → Trust & keys | read only |
| Confirm Central's backups are healthy | System → Backup health | read only |
| Review who changed what | System → Audit log, or the customer → Activity | filter by customer, action, date |

A hotel's own networks, guests, sessions and appliance health are not managed in
Central; they are in **OneGate Hotel Admin** on the appliance.
