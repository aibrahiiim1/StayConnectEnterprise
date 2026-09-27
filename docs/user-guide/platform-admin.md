# Platform Admin — User Guide

You run OneGate Central for the vendor. You do not run a site; your customers are the organisations that own sites. Your job is to create customers and sites, activate their appliances, set and manage each appliance's signed licence, and step in when a customer admin has a problem they can't solve themselves. Central is used for licensing, activation and fleet status only: a site's networks, sign-in methods, packages and clients are run by the site's own staff in **OneGate Admin Console** (formerly Hotel Admin) on the appliance. The binding description of Central is [CENTRAL_CONTROL_PLANE.md](../CENTRAL_CONTROL_PLANE.md).

## What only you can do

Platform admins (`platform_admin`, `platform_owner`) can do everything in Central. In particular, only you can:

- Create, rename, archive and delete **customers**.
- **Activate** appliances, **Move**, **Retire** and **Delete** them, and use the **Advanced** repair actions.
- Issue, renew or change, suspend, resume and revoke **licences**, and download offline licence files and activation packages. The signed licence (max concurrent online clients, validity, grace period) is the only entitlement; there are no plans or subscriptions.
- Triage **Security alerts** and manage the **Team** (Central operators).

**Support (read only)** (`platform_support`) sees everything you see and changes nothing.

## Your daily navigation

The sidebar is **Overview · Customers · Appliances · Licenses · System**. There is no customer selector: start from **Overview** (fleet counts and *Needs attention*), a list (Customers, Appliances, Licenses — each filterable), or open one customer or one appliance. **System** holds Security alerts, Trust & keys, Audit log, Team and Backup health. Page-by-page details: [control-panel-reference.md](control-panel-reference.md).

## Common tasks

### Onboarding a new customer (an organisation signs up)

1. **Customers → New customer** — the name.
2. On the customer page, **Sites → New site** for each site (name, time zone).
3. When each appliance is powered on it registers itself and appears under **Appliances** as **Waiting for activation**. Open it → **Activate** → choose the customer and site and the licence terms. (You can also create the customer and site inside the Activate dialog.) Details: [control-panel-config-manual.md](control-panel-config-manual.md).
4. If the customer should have its own Central login: customer page → **Users → Add user**, role **Customer admin** (or **Auditor**/**Viewer** for read-only). Central asks you to confirm your own password first, as it does for every change to a sign-in. Send them the initial password out-of-band (encrypted email, phone, etc.).
5. Tell them to sign in at your Central address, then follow the [customer admin guide](tenant-admin.md).

Site staff never sign in to Central to run their site; they use the operator accounts created on their appliance in the Admin Console.

### Checking on a customer's health

Open the customer: **Summary** shows its sites, appliances, active licences and what needs attention; **Appliances** shows each appliance's activation, connection and licence state; **Licenses** shows validity and grace; **Activity** shows what changed in Central for that customer.

If an appliance is offline you'd usually contact the site rather than fix it yourself — you don't have physical access, and an appliance that cannot reach Central keeps serving clients.

### Offboarding a customer

1. **Suspend** or **Revoke** each appliance's licence (the appliance's page → **License**) — client access on those appliances stops.
2. **Archive** the customer — hidden from active lists; sites, appliances, licences and the audit log are kept. **Restore** reverses it.
3. To remove it permanently, remove bottom-up: **Retire** each appliance and **Delete record** → delete each site → delete the customer. Each delete asks you to type the serial or name and give a reason.

### Moving, replacing or re-homing an appliance

- **Another site of the same customer** — the appliance's page → **Move**. The licence is re-issued for the new site with the same terms; if Central cannot re-issue it, nothing changes.
- **New hardware at the same site** — old appliance → **Advanced → Mark for replacement**; activate the new appliance for the same customer and site. The old one then retires through the acknowledged two-phase retirement.
- **Another customer** — there is no move between customers. **Retire** the appliance, have it factory-reset on site, and **Activate** it for the new customer when it registers again as *Waiting for activation*.

Procedures: [control-panel-config-manual.md §5](control-panel-config-manual.md#5-move-replace-repair-or-retire-an-appliance).

### Changing what a site may serve

On the appliance's page, **License → Renew or change**: clients online at once, validity and grace period. It issues a new signed licence version that replaces the old one. For an appliance with no route to Central, download the **Offline license file** and send it to the site to upload in the Admin Console under **Appliance & licence**.

## What you should NOT do

- **Don't** create users inside a customer unless the customer asked. It shows up in their activity log and confuses them.
- **Don't** suspend or revoke a customer's licence without the agreed commercial decision behind it.
- **Don't** expect to change a site's PMS, allowed sites, guest networks or portal from Central — these are operational decisions owned by the site and made in the Admin Console.
- **Don't** share one customer's data with another. Each customer is isolated from every other customer.

## Monitoring the platform

- **Overview** — fleet counts and everything that needs attention (waiting, licences expiring/in grace/expired/suspended, appliances offline, open security alerts, unconfirmed retirements).
- **Appliances** — connection state and last contact of every appliance.
- **System → Security alerts** — cloned or reused hardware, WAN MAC mismatches, unconfirmed retirements; a refused registration never appears as waiting.
- **System → Trust & keys** — certificate expiry and the state of the signing keys.
- **System → Backup health** — Central's own backup and rollback storage.

Appliance service health, client sessions and usage are watched on each appliance in the Admin Console; appliances do not send telemetry to Central (CLAUDE.md §0E). For monitoring of the Central host itself, see `deploy/observability/README.md`.

## Who to escalate to

- **Commercial / legal / contract issues** → your operations or finance team.
- **Engineering bugs** — file an issue with repro steps. Prefer: `audit log entry`, `customer name`, `appliance serial`, `approx timestamp`.
- **Security incident** — follow your incident response runbook. Disable the affected customer's users first (customer page → **Users**), then investigate.
