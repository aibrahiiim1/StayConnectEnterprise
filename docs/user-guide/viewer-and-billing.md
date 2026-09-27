# Viewer & Billing — User Guide

**Viewer** and **Auditor** are the read-only roles in **OneGate Central**; **Billing** is a retired role. Keep this short because there's not much to do. (On an appliance, the equivalent read-only role in **OneGate Hotel Admin** is **Site viewer** — see [hotel-admin-reference.md](hotel-admin-reference.md#who-can-use-which-page).) Roles are defined in [CENTRAL_CONTROL_PLANE.md §7](../CENTRAL_CONTROL_PLANE.md#7-roles).

---

## Viewer

**Viewer (read only)** and **Auditor (read only)** see everything for their customer in Central and change nothing.

### Who typically gets this role

- Internal or external auditors.
- Regional managers doing spot checks across properties.
- Customer support reps who need context but shouldn't touch configuration.
- Engineers debugging a problem who don't need write access.

### What you can do

Open **Overview**, **Customers** (which opens your customer's page: Summary, Sites, Appliances, Licenses, Users, Activity), **Appliances** and **Licenses** — all limited to your customer. The console shows no action buttons for your role, and the server refuses any change.

### What you cannot do

- Create, edit or delete sites or users.
- Activate, move or retire an appliance, or change a licence (those are for the vendor's platform admin).

### Most useful pages for you

- **Customer → Activity** — who did what, when. Your primary tool for investigations.
- **Licenses** — each appliance's licence state, guests online at once, and validity.
- **Overview** — what needs attention at a glance.
- **Appliances** — each appliance's activation and connection state.

### If you need to change something

Ask a customer admin at your organisation. Don't ask to be promoted "just this once" — role changes are audited and usually stick. Keep the separation of duties.

---

## Billing

A retired role: it **grants nothing in Central** and is no longer offered. Someone who still holds only this role sees no customer data in Central. Ask your customer admin for the **Viewer** role if you need to read your licences.

### Changing what you are licensed for

There is nothing to change in Central yourself: the signed appliance licence is issued and renewed by the OneGate platform admin. Contact your Semantics account contact with what you need (more concurrent guests, a longer term); when it is renewed, a new licence version appears in the appliance's licence history.

### Getting invoices

Invoices are not produced by OneGate Central. Ask your OneGate account contact or your organisation's finance team.
