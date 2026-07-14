# Viewer & Billing — User Guide

These are the two read-mostly roles. Keep this short because there's not much to do.

---

## Viewer

You can see everything inside your tenant. You cannot change anything.

### Who typically gets this role

- Internal or external auditors.
- Regional managers doing spot checks across properties.
- Customer support reps who need context but shouldn't touch config.
- Engineers debugging a problem who don't need write access.

### What you can do

Open any menu item — Dashboard, Sites, Appliances, Voucher batches, Sessions, PMS providers, Notifications, Social login, Payments, Plan, Walled garden, Audit log.

Every page is read-only. Buttons that would create/edit/delete are hidden or disabled.

### What you cannot do

- Create anything (sites, appliances, vouchers, operators).
- Edit anything (settings, PMS credentials, walled garden entries).
- Disconnect sessions, reset quotas, or issue refunds.
- Open the Operators page (hidden — you can't see who has which role).

### Most useful pages for you

- **Audit log** — who did what, when. Your primary tool for investigations.
- **Sessions** — snapshot of current activity.
- **Dashboard** — overall health at a glance.
- **Payments** — revenue / refund history (if your tenant uses paid WiFi).

### If you need to change something

Ask a `tenant_admin` or `tenant_operator` at your organisation. Don't ask your tenant admin to promote you to `tenant_operator` "just this once" — role changes are audited and usually stick. Keep the separation of duties.

---

## Billing

You can view the tenant and change the subscription plan. That's it.

### Who typically gets this role

- Finance / accounts-payable contact at the customer.
- An ops person whose only StayConnect concern is controlling cost.

### What you can do

- See the same read-only views as a Viewer.
- **Menu: Plan** — view current plan, usage vs. limit, invoices (if enabled), and change plan.

### Changing the plan

1. **Menu: Plan**
2. Current plan is shown at the top with its limits (max concurrent devices, max sites, monthly cost).
3. Click **Change plan**. Pick a new plan from the list.
4. Confirm:
   - **Upgrades** take effect immediately. You're billed pro-rata for the remaining cycle.
   - **Downgrades** take effect at the start of the next billing cycle. If your current usage exceeds the new plan's limits you'll see a warning — address usage first or the downgrade will be rejected at cycle rollover.

### What you cannot do

- Create / edit anything else (operators, sites, appliances, vouchers, etc.).
- See unredacted credentials (PMS API keys, SMTP passwords, etc.) — these are masked.
- Change the tenant name or disable the tenant. That's `platform_admin`.

### Getting invoices

Invoices are emailed to the tenant's billing email address at the end of each cycle. If you're not receiving them, ask your tenant admin to update the billing email under **Tenant settings** (if the UI exposes it) or contact StayConnect platform support.
