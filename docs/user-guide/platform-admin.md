# Platform Admin — User Guide

You run Velonet Central for the vendor. You are not a hotel; your customers are hotel groups. Your job is to create customers and sites, activate their appliances, issue and manage each appliance's signed license, and step in when a customer admin has a problem they can't solve themselves. Central is used for licensing only: a hotel's networks, sign-in methods, packages and guests are run by the hotel's own staff in **Velonet Hotel Admin** on the appliance.

## What only you can do

- Create, rename, archive and delete **customers**.
- Choose **All customers** or any one customer in the **Customer context** selector (everyone else is locked to their own customer).
- See the **Fleet license summary** on the Dashboard.
- Activate appliances and issue, renew, suspend, resume and revoke **licenses**. The signed license (max concurrent online guests, validity, grace period, entitled features) is the only entitlement; there are no plans or subscriptions to manage.

## Your daily navigation

Because you're platform-scoped, the sidebar has a **Customer context** selector under the Velonet mark. Pick a customer and Dashboard, Sites, Appliances, Licenses, Operators and Audit log scope to that customer. With **All customers** selected, Sites, Appliances and Licenses list every customer (with a Customer column), while Operators and Audit log ask you to select a customer first. Security alerts, Certificates, Assignment keys and Backup health are always fleet-wide.

## Common tasks

### Onboarding a new customer (hotel group signs up)

1. Go to **Commercial → Customers** (`/tenants`).
2. Click **New customer**. Fill in:
   - **Slug** — short identifier, e.g. `coral-sea`
   - **Name** — e.g. "Coral Sea Resorts"
3. Save. The customer is created with no sites, operators or appliances.
4. Create its **sites** (**Infrastructure → Sites**) and **activate** its appliances (**Infrastructure → Onboarding**) — see [control-panel-config-manual.md](control-panel-config-manual.md). You can also create the customer and site directly from the Activate form.
5. If the customer should have its own Central login: select the customer, go to **Administration → Operators**, click **New operator**:
   - Email of the customer's primary contact
   - Role: **Customer admin**
   - Send them the initial password out-of-band (encrypted email, phone, etc.)
6. Tell them to sign in at your Central address, then follow the [customer admin guide](tenant-admin.md).

Hotel staff never sign in to Central to run their hotel; they use the operator accounts created on their appliance in Hotel Admin.

### Checking on a customer's health

1. Select the customer in the **Customer context**.
2. **Dashboard** shows its licenses by state and its busiest sites.
3. **Appliances** shows each appliance's status and last-seen time.
4. **Licenses** shows online guests against each license's limit, expiry and grace.
5. **Audit log** shows what was changed in Central for that customer lately.

If an appliance is offline you'd usually contact the hotel rather than fix it yourself — you don't have physical access, and an appliance that cannot reach Central keeps serving guests.

### Acting on a customer's behalf

Select the customer in the **Customer context**. Everything you do is recorded as **you** in the audit log, so the customer can see that vendor staff took the action.

### Offboarding a customer

1. **Suspend** or **Revoke** its licenses (**Licenses**) — new guest sign-ins stop; existing sessions are not dropped.
2. **Customers** → **Archive** — hidden from active lists; sites, appliances, licenses and the audit log are kept. **Restore** reverses it.
3. To remove it permanently, delete bottom-up: appliances (Onboarding) → sites → the customer. Each delete asks you to type the name, code or serial and give a reason.

### Changing what a hotel may serve

Use **Licenses → Renew** to change max concurrent online guests, validity or grace period; it issues a new signed license version and supersedes the old one. For an appliance with no route to Central, **Download for offline** and send the file to the hotel to upload in Hotel Admin under **Appliance & licence**.

## What you should NOT do

- **Don't** create operators inside a customer unless the customer admin asked. It shows up in their audit log and confuses them.
- **Don't** suspend or revoke a customer's license without the agreed commercial decision behind it.
- **Don't** expect to change a hotel's PMS, allowed sites, guest networks or portal from Central — these are operational decisions owned by the hotel and made in Hotel Admin.
- **Don't** share one customer's data with another. Each customer is isolated from every other customer.

## Monitoring the platform

Central's pages give you the licensing view of the fleet:

- **Dashboard** — the Fleet license summary (active, expiring in 30 days, expired, suspended, revoked, orphaned).
- **Appliances** — which appliances have been heard from recently.
- **Security alerts** — cloned or reused hardware; activation is blocked while an alert is open.
- **Certificates** and **Assignment keys** — expiry and state of the trust material.
- **Backup health** — Central's own backup and rollback storage.

Appliance service health, guest sessions and usage are watched on each appliance in Hotel Admin; appliances do not send telemetry to Central. For monitoring of the Central host itself, see `deploy/observability/README.md`.

## Who to escalate to

- **Commercial / legal / contract issues** → your operations or finance team.
- **Engineering bugs** — file an issue with repro steps. Prefer: `audit log entry`, `customer slug`, `appliance serial`, `approx timestamp`.
- **Security incident** — follow your incident response runbook. Disable the affected customer's operator accounts first, then investigate.
