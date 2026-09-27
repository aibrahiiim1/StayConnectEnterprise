# Platform Admin — User Guide

You run OneGate Central for the vendor. You are not a hotel; your customers are hotel groups. Your job is to create customers and sites, activate their appliances, set and manage each appliance's signed licence, and step in when a customer admin has a problem they can't solve themselves. Central is used for licensing, activation and fleet status only: a hotel's networks, sign-in methods, packages and guests are run by the hotel's own staff in **OneGate Hotel Admin** on the appliance. The binding description of Central is [CENTRAL_CONTROL_PLANE.md](../CENTRAL_CONTROL_PLANE.md).

## What only you can do

Platform admins (`platform_admin`, `platform_owner`) can do everything in Central. In particular, only you can:

- Create, rename, archive and delete **customers**.
- **Activate** appliances, **Move**, **Retire** and **Delete** them, and use the **Advanced** repair actions.
- Issue, renew or change, suspend, resume and revoke **licences**, and download offline licence files and activation packages. The signed licence (max concurrent online guests, validity, grace period) is the only entitlement; there are no plans or subscriptions.
- Triage **Security alerts** and manage the **Team** (Central operators).

**Support (read only)** (`platform_support`) sees everything you see and changes nothing.

## Your daily navigation

The sidebar is **Overview · Customers · Appliances · Licenses · System**. There is no customer selector: start from **Overview** (fleet counts and *Needs attention*), a list (Customers, Appliances, Licenses — each filterable), or open one customer or one appliance. **System** holds Security alerts, Trust & keys, Audit log, Team and Backup health. Page-by-page details: [control-panel-reference.md](control-panel-reference.md).

## Common tasks

### Onboarding a new customer (hotel group signs up)

1. **Customers → New customer** — the name.
2. On the customer page, **Sites → New site** for each property (name, time zone).
3. When each appliance is powered on it registers itself and appears under **Appliances** as **Waiting for activation**. Open it → **Activate** → choose the customer and site and the licence terms. (You can also create the customer and site inside the Activate dialog.) Details: [control-panel-config-manual.md](control-panel-config-manual.md).
4. If the customer should have its own Central login: customer page → **Users → Add user**, role **Customer admin** (or **Auditor**/**Viewer** for read-only). Send them the initial password out-of-band (encrypted email, phone, etc.).
5. Tell them to sign in at your Central address, then follow the [customer admin guide](tenant-admin.md).

Hotel staff never sign in to Central to run their hotel; they use the operator accounts created on their appliance in Hotel Admin.

### Checking on a customer's health

Open the customer: **Summary** shows its sites, appliances, active licences and what needs attention; **Appliances** shows each appliance's activation, connection and licence state; **Licenses** shows validity and grace; **Activity** shows what changed in Central for that customer.

If an appliance is offline you'd usually contact the hotel rather than fix it yourself — you don't have physical access, and an appliance that cannot reach Central keeps serving guests.

### Offboarding a customer

1. **Suspend** or **Revoke** each appliance's licence (the appliance's page → **License**) — guest access on those appliances stops.
2. **Archive** the customer — hidden from active lists; sites, appliances, licences and the audit log are kept. **Restore** reverses it.
3. To remove it permanently, remove bottom-up: **Retire** each appliance and **Delete record** → delete each site → delete the customer. Each delete asks you to type the serial or name and give a reason.

### Changing what a hotel may serve

On the appliance's page, **License → Renew or change**: guests online at once, validity and grace period. It issues a new signed licence version that replaces the old one. For an appliance with no route to Central, download the **Offline license file** and send it to the hotel to upload in Hotel Admin under **Appliance & licence**.

## What you should NOT do

- **Don't** create users inside a customer unless the customer asked. It shows up in their activity log and confuses them.
- **Don't** suspend or revoke a customer's licence without the agreed commercial decision behind it.
- **Don't** expect to change a hotel's PMS, allowed sites, guest networks or portal from Central — these are operational decisions owned by the hotel and made in Hotel Admin.
- **Don't** share one customer's data with another. Each customer is isolated from every other customer.

## Monitoring the platform

- **Overview** — fleet counts and everything that needs attention (waiting, licences expiring/in grace/expired/suspended, appliances offline, open security alerts, unconfirmed retirements).
- **Appliances** — connection state and last contact of every appliance.
- **System → Security alerts** — cloned or reused hardware, WAN MAC mismatches; activation is blocked while an alert is open.
- **System → Trust & keys** — certificate expiry and the state of the signing keys.
- **System → Backup health** — Central's own backup and rollback storage.

Appliance service health, guest sessions and usage are watched on each appliance in Hotel Admin; appliances do not send telemetry to Central (CLAUDE.md §0E). For monitoring of the Central host itself, see `deploy/observability/README.md`.

## Who to escalate to

- **Commercial / legal / contract issues** → your operations or finance team.
- **Engineering bugs** — file an issue with repro steps. Prefer: `audit log entry`, `customer name`, `appliance serial`, `approx timestamp`.
- **Security incident** — follow your incident response runbook. Disable the affected customer's users first (customer page → **Users**), then investigate.
