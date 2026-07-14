# StayConnect Control Panel — Page-by-Page Reference

The **Control Panel** (also called Cloud Admin / Central) is the vendor/platform
console you use to run the whole fleet: create customers and sites, onboard and
activate appliances, issue licenses, and monitor health. It runs at your Central
URL (e.g. `https://admin.stayconnect.local/`).

This document describes **every page**: what it shows, what each option does, and
why you'd use it. For step-by-step "how do I create X" instructions, see
[control-panel-config-manual.md](control-panel-config-manual.md).

---

## Things that apply to every page

- **Login & session.** The whole console is gated by your operator login. Your
  session is re-validated every ~30 seconds; if it expires you are returned to
  the login screen. Your email and a **Sign out** button are at the bottom-left.
- **Password step-up.** Sensitive actions (issuing/revoking licenses, activating
  or deleting appliances, deleting customers/sites, etc.) ask you to **re-enter
  your password** even if the page shows no password box — a small prompt appears
  saying "This action requires confirming your password." This is a safety
  confirmation and is recorded in the audit log.
- **Customer (tenant) context.** Most pages show data for one customer at a time.
  Platform super-admins see fleet-wide roll-ups on the Dashboard and Fleet pages.
- **The menu adapts to your role.** You only see the items your role can use.

The sidebar has four groups: **Overview**, **Infrastructure**, **Commercial**,
**Administration**.

---

## OVERVIEW

### Dashboard — `/dashboard`
Your landing overview of licensing posture and this month's usage.

- **Fleet License Summary** (platform admins only) — counts of the licenses the
  Platform has issued across the fleet, by state:
  - **Active** · **Expiring ≤30d** · **Expired** · **Suspended** · **Revoked**
  - **Orphaned** (only appears when > 0) — a license whose bound appliance or
    site was deleted; it should be reconciled.
  - Header text reminds you: *"The Central Platform is the license issuer and
    holds no license of its own."* A **View licenses →** link jumps to Licenses.
- **KPI cards:**
  - **Active sessions** — devices online right now.
  - **Data this month** — total usage; shows `% of cap` if a monthly cap is set,
    else "No monthly cap".
  - **Sessions today** — since local midnight.
  - **Licensed appliances** — count of live entitlements (active + expiring).
- **Top sites (this month)** — a bar chart of the busiest sites by data volume.
- Read-only page — no actions.

---

## INFRASTRUCTURE

### Sites — `/sites`
Create and manage **sites** (physical locations — a hotel, building, or floor)
that own appliances, within the selected customer.

- **Columns:** Code · Name · Status · Timezone · Country · Created.
- **New site** (top-right) opens a form: **Code** (e.g. `hq`), **Name**,
  **Timezone** (default `UTC`), **Country** (optional).
- **Row actions:**
  - **Edit** — change name / timezone / country.
  - **Archive / Restore** — soft-hide a site without deleting it.
  - **Delete** — permanent; opens the Delete dialog (type the **site code** to
    confirm + a reason + password step-up). Blocked if the site still has
    appliances/licenses.

### Appliances — `/appliances`
The manual appliance registry and **enrollment-token** minting — the alternative
to the zero-touch Onboarding flow, plus a way to inspect an appliance's effective
config.

- **Appliances table:** Name · Site · Serial · Status (with a live dot: green =
  fresh, amber = last seen > 25s ago) · Version · Last seen.
- **Enrollment tokens table** (when any exist): Hint · Site · Serial lock ·
  Status · Expires · Created.
- **Header buttons** (need at least one site first):
  - **Enrollment token** → *Mint enrollment token* form: **Site**, **Serial
    (optional, locks the token to one box)**, **TTL hours** (default 24, max
    168). On success the full token is shown **once** with a **Copy** button —
    paste it into the appliance's `Hotel Admin → Setup / Activation` wizard.
  - **New appliance** → register a box by hand: **Site**, **Serial**, **Name**,
    **Model** (optional).
- **Row actions:**
  - **Config** (eye icon) — a drawer showing the appliance's *effective config*:
    **PMS providers** (Name · Kind · Scope · Status) and **Walled-garden rules**
    (Kind · Value · Ports · Scope).
  - **Delete** — remove the appliance record (simple confirm).
  - **Revoke** (on a token row) — invalidate an unused enrollment token.

### Onboarding — `/onboarding` ("Connect an Appliance")
The **primary, zero-touch activation flow**. A factory-clean appliance with
internet self-registers as *Pending*; you pick it, choose the customer/site and
the license terms, and click **Activate** once — the server runs the whole
lifecycle (claim → assign → signed assignment → certificate → hardware-bound
license) and the box converges to **Active** on its own.

- **Pending activation table** (auto-refreshes): select radio · Serial · WAN MAC
  · Model · Source IP · First seen. A **Refresh** button is provided.
- **Activate form** (after selecting a pending box):
  - **Customer** — *Existing* (pick one) or *+ New* (type a name, creates the
    customer).
  - **Site** — *Existing* (pick one) or *+ New* (type a name).
  - **Max concurrent online guests** (default 500; `0 = unlimited`,
    appliance-wide across all guest VLANs).
  - **Valid until** (empty = 365 days from now).
  - **Grace period (days)** (default 30 — after expiry guests keep working with
    warnings).
  - **Confirm your password** — required; activation is a step-up action.
- **Progress view:** four steps — **Detected → Activating (assign + certificate +
  license) → Appliance converging (mTLS + assignment adoption) → Active**, then
  **Activate another**.
- **Registered appliances** card (all customers): Serial · State · WAN MAC.
  - **Deactivate** — revoke the appliance's license (reversible; can re-activate).
    Step-up.
  - **Advanced Support** checkbox reveals elevated actions — **Reissue cert**,
    **Reconcile**, **Decommission** — each asks for an audited reason + step-up.
  - **Delete** — permanent; shows a delete-impact preview and requires typing the
    **appliance serial**. After deletion a factory-clean box re-registers as
    Pending.

### Fleet — `/fleet`
Live health/telemetry monitor for enrolled appliances (read-only; rows expand).

- **Columns:** Appliance · Site · **Status** (online/offline) · **Health**
  (overall service-health badge + "N affected") · Version · Last seen ·
  **License** (state + "until <date>") · **TLS cert** (expiry/renewal badge).
- **Expanded row:** per-service **health chips** (state + service name + restart
  count "·Nr" + backoff level "·L#"), the worst failure reason, the raw last
  health JSON, and a **Load telemetry** button for recent telemetry records.
- Monitoring only — no mutating actions here.

---

## COMMERCIAL

### Customers — `/tenants`
Step 1 of commercial onboarding — create/manage **customers** (hotel groups /
brands / property owners). The page shows the recommended order: *Customer → Site
→ enroll & assign an Appliance → issue a License.*

- **Columns:** Slug · Name · Status · Created.
- **New customer** form: **Slug** (e.g. `acme-hotels`) + **Name** (e.g. `Acme
  Hotels Group`).
- **Row actions:**
  - **Rename**.
  - **Archive / Restore** — the normal way to retire a customer while keeping its
    sites/appliances/licenses/audit history.
  - **Delete** — permanent; type the **customer name** + reason + password
    step-up. Blocked (with a list) while the customer still has appliances,
    sites, or licenses — remove those first.

### Licenses — `/licenses`
Issue and manage the **signed, hardware-bound licenses**. Model: *a license binds
to exactly one appliance and carries three controls — max concurrent online
guests, validity window, grace period. No plan or subscription.*

- **Columns:** Customer · Site · Appliance (serial or "not bound") · **v#**
  (version) · Status · **Online / Limit** · **Usage %** · **Valid** (from → until)
  · **Grace ends** · **Last sync**.
- **Issue license** form (step-up): **Site**, **Appliance** (filtered to the
  site), **Max concurrent online guests** (0 = unlimited), **Valid from**, **Valid
  until** (empty = 365 days), **Grace period days** (default 30).
- **Row actions** (all password step-up):
  - **Renew** — prompts for max guests, valid days, grace days; issues a **new
    signed license with a higher version**; the previous one is superseded and
    can never be replayed.
  - **Suspend** — stops new guest authorization; existing sessions run out
    naturally; portal/DHCP/DNS/Hotel Admin stay up.
  - **Resume** — reactivate a suspended license.
  - **Revoke** — the appliance permanently refuses new guest authorizations.

---

## ADMINISTRATION

### Operators — `/operators`
Manage operator accounts, roles, and passwords for the customer.

- **Columns:** Email · Name · Status · Roles.
- **Create operator** form: **Email**, **Display name**, **Initial password**
  (min 10 chars), **Role** (`tenant_admin` / `tenant_operator` / `viewer` /
  `billing`).
- **Row actions:** **+ role** / remove role (click a role badge), **Reset**
  password, **Disable** account. You cannot remove your own roles or disable
  yourself.

### Security alerts — `/security`
Triage clone / registration-anomaly alerts (hardware-identity mismatch, hardware
reuse, WAN-MAC mismatch, and — new — **license permissive-attempt** and appliance
service-health alerts). Auto-licensing is denied while an alert is open.

- **Show resolved** filter (resolved hidden by default).
- **Columns:** When · Kind · Serial · Source IP · Detail · Status · Triage.
- **Triage:** Investigate → Acknowledge → Resolve / False positive / Reopen.
  Resolving asks for an audited reason.

### Certificates — `/certificates`
Read-only inventory of appliance mTLS client certificates from the internal CA
(metadata only — never private keys).

- **Show superseded** filter.
- **Columns:** Appliance · Customer · Site · Fingerprint · Issuer · Issued ·
  Expires · Status · Last rotation · Revocation (date + reason).

### Assignment keys — `/assignment-keys`
Read-only inventory of the keys that sign appliance→tenant/site assignment
documents (public fingerprint + metadata only).

- **Columns:** Key ID · Fingerprint · State (active / verify_only / revoked, with
  an "emergency" flag) · Rotation · Dependencies (current assignments) · Created ·
  Retired · Reason.

### Backup health — `/backup-health`
Read-only retention/rollback health for the Central host.

- **KPI cards:** **Disk used** (with warn/crit thresholds) · **Rollback path**
  (valid / INVALID) · **Last cleanup** · **Failures**.
- **Policy line** + item tables: **Protected**, **Operator-pinned**, **Retained**,
  **Delete candidates (next apply)**.

### Audit log — `/audit`
The immutable audit trail (last 7 days) for the customer.

- **Filter** by action (comma-separated, e.g. `site.created,operator.disabled`).
- **Columns:** When · Actor · Action · Target · IP · Payload.

---

## Legacy pages (reachable by URL only, not in the sidebar)

Plans and subscriptions were **retired** in the simple-license model — the license
itself is the entitlement. These pages remain for viewing historical data:

- **`/subscription`** — legacy view of a customer's subscription + effective
  limits + plan catalog (can switch plans).
- **`/commercial`** — full legacy entitlements console (plans, subscription terms,
  tenant overrides, plan limits), each change reason-logged + step-up.

You should not need these for the simple-license workflow.

---

## The Delete dialog (Customers, Sites, Onboarding appliances)

Permanent delete **never cascades**. It always requires all three of:
1. a **typed confirmation** matching exactly — customer **name**, site **code**,
   or appliance **serial**;
2. a **reason** (recorded in the audit log);
3. a **password step-up**.

If dependent records still exist, the dialog lists them ("cannot be deleted
because it still contains: N …") with guidance to remove them first, in order:
**Appliances → Site → Customer**.
