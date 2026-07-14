# StayConnect Control Panel — Configuration Manual

Step-by-step instructions for setting up customers, sites, appliances and
licenses from the **Control Panel** (Cloud Admin / Central). For a description of
what each page shows, see
[control-panel-reference.md](control-panel-reference.md).

**The recommended order is:** Customer → Site → Appliance → License.

> Many of these steps ask you to **re-enter your password** (a "step-up"
> confirmation). Have it ready. Every step is recorded in the audit log.

---

## 1. Create a Customer

A **customer** (tenant) is a hotel group, brand, or property owner.

1. Go to **Customers** (`/tenants`).
2. Click **New customer**.
3. Fill in:
   - **Slug** — a short URL-safe identifier, e.g. `acme-hotels` (lowercase,
     hyphens). Must be unique.
   - **Name** — the display name, e.g. `Acme Hotels Group`.
4. Click **Create customer**.

The customer appears in the table with status **active**.

> To retire a customer later, use **Archive** (keeps its history). Use **Delete**
> only for a customer with nothing under it — you must first delete its
> appliances, then its sites (see §6).

---

## 2. Create a Site

A **site** is a physical location that owns appliances (a hotel, building, or
floor).

1. Make sure the correct customer is selected.
2. Go to **Sites** (`/sites`).
3. Click **New site**.
4. Fill in:
   - **Code** — short identifier, e.g. `hq` or `marina`.
   - **Name** — e.g. `Headquarters` or `Marina Hotel`.
   - **Timezone** — optional, defaults to `UTC` (e.g. `Europe/London`).
   - **Country** — optional (e.g. `US`).
5. Click **Create**.

---

## 3. Onboard & activate an Appliance (recommended: zero-touch)

Use this when the appliance is racked, powered, and has internet to Central.

1. On the appliance, complete first-boot so it **self-registers**. A factory-clean
   box with Central connectivity automatically appears as **Pending**.
2. In the Control Panel, go to **Onboarding** (`/onboarding`).
3. In **Pending activation**, find your box (match the **Serial** / **WAN MAC**)
   and select its radio button. (Click **Refresh** if it hasn't appeared yet.)
4. In the **Activate** form:
   - **Customer** — choose **Existing** and pick one, or **+ New** and type a name.
   - **Site** — choose **Existing** and pick one, or **+ New** and type a name.
   - **Max concurrent online guests** — e.g. `500`. `0` means unlimited. This cap
     is appliance-wide across all guest VLANs.
   - **Valid until** — leave empty for 365 days, or pick a date.
   - **Grace period (days)** — e.g. `30`. After expiry, guests keep working (with
     warnings) for this long.
   - **Confirm your password**.
5. Click **Activate**.
6. Watch the progress: **Detected → Activating → Converging → Active**. When it
   reaches Active, the appliance is assigned, holds an mTLS certificate, and has a
   hardware-bound license installed — no further steps needed.

**What "Activate" does for you** in one action: claims the pending box, assigns it
to the customer/site, issues a signed assignment, issues its mTLS certificate, and
issues a hardware-bound license with the terms you set.

### Alternative: token-based / manual enrollment

Use this when you want to pre-register a box, or the installer will type a token:

- **Mint a token:** **Appliances** (`/appliances`) → **Enrollment token** →
  choose **Site**, optionally lock to a **Serial**, set **TTL hours** → **Mint
  token** → **Copy** the token (shown once). Give it to the installer, who enters
  it in the appliance's **Hotel Admin → Setup / Enrollment** wizard.
- **Or register by hand:** **Appliances** → **New appliance** → **Site**,
  **Serial**, **Name**, **Model**.

After a manual enrollment, activate/assign it and issue a license (below).

---

## 4. Issue a License

A license binds to **one appliance** and controls: max concurrent online guests,
the validity window, and the grace period. (The zero-touch Onboarding flow issues
one for you; use this page to issue additional/replacement licenses or to manage
existing ones.)

1. Go to **Licenses** (`/licenses`).
2. Click **Issue license** (needs at least one site + appliance to exist).
3. Fill in:
   - **Site** — pick the site.
   - **Appliance** — pick the appliance (list is filtered to the chosen site).
   - **Max concurrent online guests** — `0` = unlimited.
   - **Valid from** / **Valid until** — leave both empty for "starts now, valid
     365 days".
   - **Grace period days** — default 30.
4. Click **Issue license** and confirm your password.

The license row shows the version (**v1**), status **Active**, the online/limit
usage, and validity.

### Renew / Suspend / Resume / Revoke

On the license row (all confirm your password):

- **Renew** — enter new **max guests**, **valid days from now**, and **grace
  days**. This issues a **new signed license with a higher version**; the old one
  is superseded and can never be replayed on the appliance.
- **Suspend** — new guest logins stop; existing sessions run out naturally;
  portal/DHCP/DNS/Hotel Admin stay up. Use **Resume** to reactivate.
- **Revoke** — permanent; the appliance refuses all new guest authorization.

---

## 5. Replace or move an appliance

- **Deactivate** (Onboarding → Registered appliances) revokes the license but
  keeps identity/certificate — the box can be re-activated later.
- **Reissue cert / Reconcile / Decommission** are under **Advanced Support** on
  the Onboarding page (each needs a reason + step-up).
- **Delete** an appliance (Onboarding delete dialog, type the serial) permanently
  removes it; a factory-clean box then re-registers as Pending.

---

## 6. Fully remove a customer (clean teardown)

Delete never cascades, so remove bottom-up:

1. **Deactivate + Delete** every appliance under the customer (Onboarding).
2. **Delete** every site (Sites → Delete → type the site code).
3. **Delete** the customer (Customers → Delete → type the customer name).

Licenses bound to a deleted appliance are revoked automatically. Vestigial legacy
subscriptions no longer block the delete (they are purged as part of it). If a
delete is still blocked, the dialog tells you exactly what remains.

---

## 7. Day-2 operations quick reference

| I want to… | Go to | Do |
|---|---|---|
| Add a staff login | Operators | **Create operator** (set email + role) |
| Reset a staff password | Operators | **Reset** on their row |
| See who's online / usage | Dashboard, Fleet | read-only |
| Check appliance health | Fleet | expand the appliance row |
| Investigate a clone alert | Security alerts | triage the alert |
| Check cert expiry | Certificates, Fleet | read-only |
| Confirm backups are healthy | Backup health | read-only |
| Review who changed what | Audit log | filter by action |
