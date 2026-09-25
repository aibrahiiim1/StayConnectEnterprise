# Velonet Central — Configuration Manual

Step-by-step instructions for setting up customers, sites, appliances and
licenses in **Velonet Central** (also called the Control Panel or Cloud Admin).
For a description of what each page shows, see
[control-panel-reference.md](control-panel-reference.md).

**The recommended order is:** Customer → Site → Appliance → License.

> Many of these steps ask you to **confirm your password** in a dialog. Have it
> ready. Every step is recorded in the audit log.

---

## 1. Create a Customer

A **customer** is a hotel group, brand, or property owner.

1. Go to **Commercial → Customers** (`/tenants`).
2. Click **New customer**.
3. Fill in:
   - **Slug** — a short identifier, e.g. `acme-hotels` (lowercase, hyphens).
     Must be unique; it is also used for single sign-on.
   - **Name** — the display name, e.g. `Acme Hotels Group`.
4. Click **Create customer**.

The customer appears in the table with status **Active**.

> To retire a customer later, use **Archive** (keeps its history). Use **Delete**
> only for a customer with nothing under it — you must first delete its
> appliances, then its sites (see §6).

---

## 2. Create a Site

A **Site** is **one physical property** — a single hotel or resort — that belongs
to exactly one Customer and can contain one or more Appliances. Buildings, floors,
wings, SSIDs and guest networks are configured on the appliance in Hotel Admin;
they are **not** Sites. A hotel with two buildings on one uplink is still one Site.

1. **Select the owning Customer** in the **Customer context** selector at the top
   of the sidebar (recommended — the form is then pre-filled).
2. Go to **Infrastructure → Sites** (`/sites`).
3. Click **New site**.
4. Fill in:
   - **Owning customer** — pre-filled from the customer context; in **All
     customers** mode you must choose it here. A Site always has exactly one
     owner.
   - **Code** — short and unique, e.g. `hurghada` or `marina`.
   - **Name** — the property name, e.g. `Coral Sea Resort Hurghada`.
   - **Timezone** — optional, defaults to `UTC` (e.g. `Africa/Cairo`).
   - **Country** — optional two-letter code (e.g. `EG`).
5. Click **Create site**.

---

## 3. Onboard & activate an Appliance (recommended: zero-touch)

Use this when the appliance is installed, powered, and has internet to Central.

1. On the appliance, complete first boot so it **registers itself**. A
   factory-clean appliance that can reach Central appears as **Pending
   activation**. Nothing needs to be typed on the appliance.
2. In Central, go to **Infrastructure → Onboarding** (`/onboarding`).
3. In **Pending activation**, find the appliance (match the **Serial** / **WAN
   MAC**) and select it. The list refreshes every few seconds.
4. In the **Activate** form:
   - **Customer** — pick an existing one, or type a new name to create it.
   - **Site** — pick an existing one, or type a new name to create it.
   - **Max concurrent online guests** — e.g. `500`. `0` means unlimited. This
     limit covers the whole appliance, across all guest networks.
   - **Valid until** — leave empty for 365 days, or pick a date.
   - **Grace period (days)** — e.g. `30`. After expiry, guests are still served
     (with warnings) for this long.
   - **Confirm your password**.
5. Click **Activate**.
6. Watch the progress: **Detected → Activating → Appliance converging →
   Active**. When it reaches Active, the appliance is assigned to the customer
   and site, holds its certificate, and has its signed license installed — no
   further steps needed.

**What "Activate" does for you** in one action: takes the pending appliance,
assigns it to the customer and site, issues its signed assignment and certificate,
and issues a hardware-bound license with the terms you set.

### Offline activation (appliance with no route to Central)

1. In Hotel Admin on the appliance, **System → Appliance & licence → Appliance
   setup → Offline → Download activation request**.
2. In Central, **Onboarding → Offline activation**: upload that file. The
   appliance appears under **Pending activation**; activate it as above.
3. Under **Registered appliances**, click **Activation package** for it and
   carry the file back.
4. In Hotel Admin, upload it under **Appliance & licence → Offline**.

### Alternative: enrollment token or manual registration

Use this only when an appliance cannot register itself:

- **Enrollment token:** **Infrastructure → Appliances** (`/appliances`) →
  **Enrollment token** → choose the **Site**, optionally lock it to a
  **Serial**, set **Valid for (hours)** (1–168) → the token is shown **once**;
  copy it. The installer enters it in the appliance's **Hotel Admin → Appliance
  & licence → Advanced / recovery**.
- **Or register by hand:** **Appliances** → **New appliance** → **Site**,
  **Serial**, **Name**, **Model**.

After a manual enrollment, activate it and issue a license (below).

---

## 4. Issue a License

A license binds to **one appliance** and controls: max concurrent online guests,
the validity window, and the grace period. (Activation on Onboarding issues one
for you; use this page to issue additional or replacement licenses or to manage
existing ones.)

1. Go to **Commercial → Licenses** (`/licenses`), with the customer selected.
2. Click **Issue license** (needs at least one site and appliance).
3. Fill in:
   - **Site** — pick the site.
   - **Appliance** — pick the appliance (filtered to the chosen site).
   - **Max concurrent online guests** — `0` = unlimited.
   - **Grace period (days)** — default 30.
   - **Valid from** / **Valid until** — leave both empty for "starts now, valid
     365 days".
4. Click **Issue license** and confirm your password if asked.

The license row shows its version, status **Active**, online guests against the
limit, and validity.

### Renew / Download for offline / Suspend / Resume / Revoke

On the license row (each confirms your password when asked):

- **Renew** — enter new **max concurrent online guests**, **valid for (days from
  now)** and **grace period**. This issues a **new signed license version**; the
  old one becomes *Superseded* and can never be used again.
- **Download for offline** — the signed, appliance-bound file, to upload in Hotel
  Admin under **Appliance & licence** when the appliance cannot reach Central.
- **Suspend** — new guest sign-ins stop; existing sessions are not dropped; the
  portal, DHCP, DNS and Hotel Admin stay up. Use **Resume** to reactivate.
- **Revoke** — permanent; the appliance refuses new guest sign-ins. Issue a new
  license to restore service.

---

## 5. Replace or move an appliance

- **Deactivate** (Onboarding → Registered appliances) revokes the license but
  keeps the appliance's identity — it can be activated again later.
- **Reissue cert / Reconcile / Decommission** are under **Advanced Support** on
  the Onboarding page (each needs a reason and password confirmation).
- **Delete** an appliance (Onboarding → Delete: type the serial and a reason)
  permanently removes it; a factory-clean appliance then registers again as
  Pending activation.

---

## 6. Fully remove a customer (clean teardown)

Delete never cascades, so remove bottom-up:

1. **Deactivate + Delete** every appliance under the customer (Onboarding).
2. **Delete** every site (Sites → Delete → type the site code).
3. **Delete** the customer (Customers → Delete → type the customer name).

Licenses bound to a deleted appliance are revoked automatically. If a delete is
still blocked, the dialog tells you exactly what remains.

---

## 7. Day-2 operations quick reference

| I want to… | Go to | Do |
|---|---|---|
| Add a staff login to Central | Operators | **New operator** (email, name, password, role) |
| Reset a staff password | Operators | **Reset password** on their row |
| See licenses by state | Dashboard, Licenses | read-only |
| See whether an appliance is online | Appliances | Status and Last seen |
| Investigate a clone alert | Security alerts | Investigate → Resolve / False positive |
| Check certificate expiry | Certificates | read-only |
| Confirm backups are healthy | Backup health | read-only |
| Review who changed what | Audit log | filter by action |

A hotel's own networks, guests, sessions and appliance health are not managed in
Central; they are in **Velonet Hotel Admin** on the appliance.
