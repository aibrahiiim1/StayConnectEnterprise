# Viewer & Billing — User Guide

These are the two read-mostly roles in **Velonet Central**. Keep this short because there's not much to do. (On an appliance, the equivalent read-only role in **Velonet Hotel Admin** is **Site viewer** — see [hotel-admin-reference.md](hotel-admin-reference.md#who-can-use-which-page).)

---

## Viewer

You can see your customer's pages in Central. You cannot manage operators or licenses, or activate appliances.

### Who typically gets this role

- Internal or external auditors.
- Regional managers doing spot checks across properties.
- Customer support reps who need context but shouldn't touch configuration.
- Engineers debugging a problem who don't need write access.

### What you can do

Open the menu items for your customer — Dashboard, Sites, Appliances, Licenses, Audit log and the other pages your role may read (Central's server decides which).

Central shows you exactly the actions its server accepts from your role. **Be aware:** Central's server
currently lets every customer role — Viewer included — create, edit, archive and delete **Sites** and create
and delete **Appliances**, and rename its own customer, so those buttons appear for you. Everything else is
refused and hidden.

### What you cannot do

- Issue, renew, suspend, resume or revoke a license.
- Activate, deactivate or decommission an appliance (Onboarding).
- Manage operators.

### Most useful pages for you

- **Audit log** — who did what, when. Your primary tool for investigations.
- **Licenses** — each appliance's license state, its maximum online guests, and validity.
- **Dashboard** — licenses by state at a glance.
- **Appliances** — which appliances have reached Central recently.

### If you need to change something

Ask a customer admin or customer operator at your organisation. Don't ask to be promoted to customer operator "just this once" — role changes are audited and usually stick. Keep the separation of duties.

---

## Billing

A legacy role: it is no longer granted to new operators. If you already hold it, you can read your customer's licenses.

### Who typically gets this role

- Finance / accounts-payable contact at the customer.
- An operations person whose only Velonet concern is cost.

### What you can do

- See the same read-only views as a Viewer.
- **Commercial → Licenses** — each appliance's max concurrent online guests, valid from/until, grace period and state. Current online guests are shown on the appliance itself, in Hotel Admin.

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
