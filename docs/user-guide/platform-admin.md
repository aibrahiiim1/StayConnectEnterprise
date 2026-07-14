# Platform Admin — User Guide

You run the StayConnect platform itself. You are not a hotel; your customers are hotels (tenants). Your job is to onboard new tenants, keep the platform healthy, and step in when a tenant admin has a problem they can't solve themselves.

## What only you can do

- Create, rename, and delete **tenants**.
- Assign the **first tenant admin** for a new tenant.
- See **any tenant's data** (everyone else is locked to their own tenant).
- Manage **subscription plans** at the catalog level (the list of plans tenants can choose from — managed directly in the database, not in the UI yet).

Everything else — sites, appliances, staff, PMS, guest sessions — is normally done by the tenant's own staff. You only touch these if a customer asks for help.

## Your daily navigation

Because you're platform-scoped, most pages show a **"Tenant:"** selector at the top. Pick a tenant and the page scopes to that tenant. Without a selection, you see platform-wide data (e.g., all tenants listed on the Tenants view).

## Common tasks

### Onboarding a new tenant (hotel chain signs up)

1. Go to **Dashboard → Tenants** (or `/tenants`).
2. Click **New tenant**. Fill in:
   - **Name** — e.g. "Coral Sea Resorts"
   - **Slug** — short URL-safe identifier, e.g. `coral-sea`
   - **Plan** — pick one from the plan catalog (usually starts on "Starter" or "Trial")
3. Save. The tenant is created with no sites, no operators, no appliances.
4. Go to **Operators**, switch the tenant selector to the new tenant, and click **New operator**:
   - Email of the customer's primary contact
   - Role: **tenant_admin**
   - Send them the temporary password out-of-band (encrypted email, phone, etc.)
5. Tell them to log in at `https://admin.stayconnect.local/` and change their password, then follow the [tenant-admin guide](tenant-admin.md).

From this point the tenant admin runs their own setup. You don't need to create their sites or appliances.

### Checking on a tenant's health

1. Use the **Tenant:** dropdown to scope into a tenant.
2. **Dashboard** shows their sessions, online appliances, recent errors.
3. **Appliances** shows last-seen time for each gateway. Red / stale = the tenant has a site in trouble.
4. **Audit log** shows everything their staff has done lately.

If an appliance is offline you'd usually ping the tenant admin rather than fix it yourself — you don't have physical access anyway.

### Impersonating a tenant to reproduce a bug

Use the **Tenant:** selector on any page. All read operations show that tenant's data. Writes are audited as **you** making the change, so the tenant's audit log will show platform support took the action.

### Revoking a tenant (offboarding)

1. **Tenants** → pick the tenant → **Disable** (soft-delete; data retained per retention policy).
2. All their operators lose admin access immediately.
3. Their appliances stop authorising new sessions (data plane reads tenant.enabled).
4. After retention period, hard-delete via the database or a maintenance script — there's no UI for permanent deletion yet.

### Creating / editing subscription plans

There is no admin UI for plan catalog management yet. Edit the `plans` table directly via SQL on the control-plane database:

```sql
INSERT INTO plans (slug, name, price_cents, max_concurrent_devices, max_sites, ...)
VALUES ('pro', 'Pro', 9900, 500, 10, ...);
```

Changes take effect immediately. Existing tenants keep their assigned plan until their tenant_admin changes it.

## What you should NOT do

- **Don't** create operators inside a tenant unless the tenant admin asked. It shows up in their audit log and confuses them.
- **Don't** change a tenant's subscription plan without their billing contact's explicit request.
- **Don't** edit PMS credentials, walled garden rules, or site configs for a tenant unprompted — these are operational decisions owned by the hotel.
- **Don't** share another tenant's data with a tenant. Each tenant is isolated from every other tenant.

## Monitoring the platform

Admin UI gives per-tenant views. For platform-wide health use:

- **Grafana** — dashboards for all tenants combined (sessions/sec, appliance heartbeat, error rates)
- **Alertmanager** — critical alerts route to your on-call channel
- **Postgres/TimescaleDB direct queries** for custom reports

See `deploy/observability/README.md` for the observability stack. (If that doesn't exist yet, ask the engineering team.)

## Who to escalate to

- **Billing / legal / contract issues** → your ops / finance team.
- **Engineering bugs** — file an issue with repro steps. Prefer: `audit log entry id`, `tenant slug`, `appliance id`, `approx timestamp`.
- **Security incident** — follow your incident response runbook. Rotate the affected tenant's operator credentials first, then investigate.
