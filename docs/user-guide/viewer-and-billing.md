# Viewer & Billing — User Guide

These are the two read-mostly roles in **Velonet Central**. Keep this short because there's not much to do. (On an appliance, the equivalent read-only role in **Velonet Hotel Admin** is **Site viewer** — see [hotel-admin-reference.md](hotel-admin-reference.md#who-can-use-which-page).)

---

## Viewer

You can see everything for your customer in Central. You cannot change anything.

### Who typically gets this role

- Internal or external auditors.
- Regional managers doing spot checks across properties.
- Customer support reps who need context but shouldn't touch configuration.
- Engineers debugging a problem who don't need write access.

### What you can do

Open the menu items for your customer — Dashboard, Sites, Appliances, Licenses, Audit log and the other pages your role may read (Central's server decides which).

Nothing is changed by your role: the server refuses any create, edit or delete you attempt.

### What you cannot do

- Create anything (sites, appliances, licenses, operators).
- Edit, archive or delete anything.
- Activate, deactivate or change an appliance or its license.
- Manage operators.

### Most useful pages for you

- **Audit log** — who did what, when. Your primary tool for investigations.
- **Licenses** — each appliance's license state, online guests against its limit, and validity.
- **Dashboard** — licenses by state at a glance.
- **Appliances** — which appliances are online.

### If you need to change something

Ask a customer admin or customer operator at your organisation. Don't ask to be promoted to customer operator "just this once" — role changes are audited and usually stick. Keep the separation of duties.

---

## Billing

You can view your customer's licenses and usage. That's it.

### Who typically gets this role

- Finance / accounts-payable contact at the customer.
- An operations person whose only Velonet concern is cost.

### What you can do

- See the same read-only views as a Viewer.
- **Commercial → Licenses** — each appliance's max concurrent online guests, current online guests and usage, valid from/until, grace period and state.

### Changing what you are licensed for

There is nothing to change in Central yourself: the signed appliance license is issued and renewed by the Velonet platform admin.

1. Open **Licenses** and note the appliance, its limit and its valid-until date.
2. Contact your Velonet account contact with what you need (more concurrent guests, a longer term).
3. When the platform admin renews it, a new license version appears on the row and the previous one becomes *Superseded*.

### What you cannot do

- Create / edit anything (operators, sites, appliances, licenses, etc.).
- See credentials — PMS, email/SMS and social-login secrets live on the appliances and are write-only there.
- Rename or archive your customer. That's the platform admin.

### Getting invoices

Invoices are not produced by Velonet Central. Ask your Velonet account contact or your organisation's finance team.
