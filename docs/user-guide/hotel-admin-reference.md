# StayConnect Appliance (Hotel Admin) — Page-by-Page Reference

The **Hotel Admin** is the on-appliance console the hotel's own staff use. It runs
locally on the appliance (fronted by Caddy over TLS at the management IP, e.g.
`https://hotel.stayconnect.local/`) and keeps working even when the cloud is
unreachable.

This document describes **every page**: what it shows, its options, and its
purpose. For step-by-step setup instructions see
[hotel-admin-config-manual.md](hotel-admin-config-manual.md).

---

## Things that apply to every page

- **Login & session.** Gated by your operator login; re-validated every ~30s,
  otherwise returns you to `/login`. Email + **Sign out** at the bottom-left.
- **Roles decide what you see.** The menu has six groups; you only see items your
  role can read, and write controls (New/Edit/Delete) appear only if your role can
  write. The appliance enforces this server-side regardless of the UI.
- **Password step-up.** A few high-impact actions ask you to re-enter your
  password: **applying/rolling-back WAN-LAN changes**, **rotating the TLS
  certificate**, and **restarting a service** in Diagnostics.
- **Secrets are never shown back.** API keys, client secrets, Stripe keys, auth
  keys, the enrollment token and private keys are write-only/masked.

Groups: **Overview · Access · Integrations · Site · Networking · System**.

---

## OVERVIEW

### Dashboard — `/dashboard`
An at-a-glance health/usage snapshot (auto-refreshes every 30s).

- Header: overall edge **status** (ok/warn) + `edged` version.
- **KPI cards:** **Active sessions** · **Sessions today** · **Data today**
  (down/up split) · **License** — shows the enforcement state (Active / Grace /
  Suspended / Expired / Revoked) when a real license is installed, or **"Pending
  activation"** (amber, with an "Activate this appliance →" link) when the
  appliance is not yet licensed.
- **Appliance health** card: **Site database** · **Session controller (scd)** ·
  **Cloud sync outbox** (pending/dead counts) · **Site ID**.
- Read-only.

---

## ACCESS

### Guest access plans — `/guest-access-plans`
Define the WiFi "plans" (duration, data cap, speed, price) that voucher batches
are generated from.

- **Columns:** Code · Name · Duration (∞ if unset) · Data cap (∞ if blank) ·
  Down/Up · Devices · Price · Active.
- **New plan** form: Code, Name, Description, Duration (s), Data cap (bytes; blank
  = unlimited), Down kbps, Up kbps, Max devices (default 1), Price (cents),
  Currency.
- **Actions:** toggle active (click the badge), **Delete** (confirm). License
  limits surface inline ("License limit reached…").

### Voucher batches — `/voucher-batches`
Generate and manage batches of guest WiFi voucher codes from a plan.

- **Columns:** Label · Count · Created · (CSV · Revoke all).
- **New batch** form: **Plan** (dropdown of active plans), **Count** (1–10000,
  default 50), **Label** (optional). Needs at least one active plan.
- **Actions:** **CSV** download of codes · **Revoke all** non-terminal vouchers.
- **Batch detail** (`/voucher-batches/{id}`): lists each code with State (active /
  unused / revoked / expired / exhausted), Issued, Activated; **Download CSV**.

### Sessions — `/sessions`
See connected/recent guest devices and force-disconnect them.

- Tabs **Active** (polls every 10s) / **Recent**.
- **Columns:** IP / MAC · State (+ end reason) · Started · Last activity · Down/Up.
- **Actions:** **Disconnect** an active session (confirm).

---

## INTEGRATIONS

### PMS providers — `/pms-providers`
Connect the hotel Property Management System so guests can authenticate by room
number + name match.

- **Kinds:** `stub`, `protel-fias`, `opera-fias`, `fidelio-fias`, `mews`,
  `apaleo`. FIAS kinds use Host/Port/TLS/Auth key; REST kinds (Mews, Apaleo) use
  Base URL/API key/Property ID.
- **Columns:** Name · Kind · Endpoint · Status (connected/degraded/down) · Last
  record · Enabled.
- **New/Edit** form: Name, Kind, Display name, and the kind-specific fields
  (secrets are write-only; leave blank on edit to keep).
- **Actions (all roles):** **Test** (connectivity + latency), **Health** (status
  JSON), **Cache** (a preview table of Room · Guest · Reservation · dates).
  **New/Edit/Delete** only for writers.

### Notifications — `/notifications`
Configure the email/SMS senders that deliver OTP codes to guests.

- **Kinds:** email → `stub` / `sendgrid` / `ses`; sms → `stub` / `twilio`.
- **Columns:** Channel · Kind · Sender · Health · Enabled.
- **New/Edit** form: Channel, Kind, Display name, API key (write-only), API user
  (Twilio SID for SMS); email adds From address / From name.

### Social login — `/social-providers`
Register OAuth apps so guests can sign in with Google / Apple / Facebook /
Microsoft.

- **Columns:** Provider · Client ID · Redirect URI · Last success · Enabled.
- **New/Edit** form: Provider, Display name, Client ID, Client secret
  (write-only), Redirect URI, Scopes.

### Payments — `/payments`
Connect Stripe to sell WiFi vouchers on the portal, and review purchases.

- **Stripe accounts** table: Name · Publishable key · URLs · Last success ·
  Enabled. **New/Edit** form: Display name, Publishable key (`pk_live_…`), Secret
  key (write-only), Webhook secret (write-only), Success URL, Cancel URL.
- **Recent payments** table: Status (paid/pending/failed) · Amount · Stripe
  session · Voucher · Created · Completed.
- Write actions use the **`stripe-accounts`** role.

---

## SITE

### Walled garden — `/walled-garden`
Allow pre-authentication (before login) access to portal/payment endpoints.

- **Columns:** Kind (domain/cidr/ip) · Value · Ports (all if blank) · Description.
- **New rule** form: Kind, Value, Ports (comma, blank = all), Description.

### Portal branding — `/portal-branding`
Edit the captive-portal branding document (logo URL, terms, languages, colors) as
raw JSON. **Save** validates it's a JSON object first. Read-only unless your role
can write.

### Operators — `/operators`
Create Hotel Admin staff accounts and manage roles/passwords.

- **Columns:** Operator (+ "(you)") · Roles · Status · Created.
- **New operator** form: Email, Display name, **Password (min 10)**, **Role** (one
  of the seven site roles — see the config manual). 
- **Actions:** **Set password**, **+ role** / remove role, **Disable**. You can't
  remove your own `site_admin` role or disable yourself.

---

## NETWORKING (all gated by the `network` / Hotel IT role)

### WAN / LAN settings — `/network/system`
Configure the appliance's WAN/management IP and the guest LAN gateway, with a
**preview → apply → confirm** flow and automatic rollback.

- **Status cards:** **WAN / Management** (interface, MAC, link, IP mode, IP,
  mask, gateway, DNS, management URL, connectivity dots for gateway/internet/DNS,
  drift warning) and **Guest LAN** (interface, bridge, gateway IP, DHCP range,
  lease time, DNS, bridge members).
- **Change configuration** (writers): WAN IP / prefix / gateway / DNS; LAN gateway
  IP / prefix (DHCP is read-only here — manage it on the DHCP page).
- **Flow:** **Validate & preview** (shows before/after + new management URL) →
  **Apply change** (requires your password) → a **countdown banner** appears:
  **Keep this configuration** or **Roll back now** (rollback needs your password).
  If you don't confirm in time it auto-rolls-back — so a bad IP change can never
  lock you out.
- **Diagnostics** (run + download report) and **Change history** table.

### Cloud connection — `/network/cloud`
Live status of the link to Central (the appliance runs locally even when offline).

- **Cards:** Cloud API (mTLS), NATS (mTLS), Central Control Plane (reachability),
  Appliance identity, License, Telemetry outbox.
- **Actions (writers):** **Test connection**, **Refresh license**, **Download
  diagnostics**. Secrets are masked.

### TLS certificate — `/network/certificate`
Manage the dual-SAN Hotel Admin certificate (for `hotel.stayconnect.local` + the
management IP). It auto-renews at 45 days / on IP change / on SAN drift, issued
from the local StayConnect CA.

- **Status:** a health badge (Healthy / Renewal due / Warning / Critical /
  Emergency / Expired + days left) and full cert metadata (subject, issuer,
  fingerprint, SANs, expiry, last renewal result).
- **Actions:** **Check certificate**; **Rotate** — requires a **Reason**, your
  **password**, and typing **`ROTATE`** to confirm. You cannot upload a key; it
  mints and hot-swaps with auto-rollback.

### Setup / Enrollment — `/setup/enrollment`
The first-run wizard to connect the appliance to Central.

- **Connect** form (before enrollment): **Enrollment code** (the token from the
  Control Panel) + **Serial**. Click **Connect**.
- Friendly progress **Connect → Verify → Ready**, plus a collapsible **15-stage
  lifecycle** (Awaiting enrollment → … → Certificate issued → API/NATS mTLS
  connected → License active → Setup complete), and cards for identity, network/
  Central checks (DNS, Central :443, clock, mTLS :9443, NATS :4223), certificate,
  license, and completion. Once connected it's locked.

### Guest networks — `/network`
List guest networks and drive the validate/apply/confirm lifecycle.

- **Columns:** Name · SSID label · Type (VLAN {id} / untagged) · Parent · Gateway
  · Subnet · DHCP (local/relay/external/disabled) · Pool · Portal · Enabled ·
  Clients.
- **Header:** **Validate**, **Apply changes**, **New guest network**.
- A **pending-confirmation banner** with countdown appears after Apply —
  **Confirm** or it **rolls back** automatically.
- **Row actions:** **Edit** · **Disable** · **Delete** (only when disabled).

**New guest network wizard** (`/network/new`) — 7 steps: **Identity → Interface /
VLAN → Subnet & gateway → DHCP & DNS → Captive portal → Review → Apply** (create,
validate, apply with timed confirmation). Reminder: StayConnect doesn't broadcast
WiFi — map the SSID→VLAN on your wireless controller.

**Guest network detail** (`/network/{id}`) — topology is read-only (type, VLAN,
parent, bridge are immutable — delete & recreate to change them); editable
settings (name, subnet, gateway, DHCP pools, DNS, leases, portal/NAT/isolation
toggles), and per-network **DHCP reservations** (pin a MAC to a fixed IP).

### DHCP & leases — `/network/dhcp`
- Tabs **Active leases** (IP · MAC · Hostname · Subnet · State · Expires) and
  **Reservations** (Guest network · MAC · Reserved IP · Hostname · Enabled).
- **New reservation** form: Guest network, MAC, Reserved IP, Hostname.

### Config history — `/network/revisions`
Every validate/apply of guest-network config, with Seq · State · Summary ·
Applied · Confirmed · Failure. Expand a row for validation issues, apply events,
and health checks; **Confirm/Rollback** a pending revision.

---

## SYSTEM

### Diagnostics — `/health`
Per-service health supervision with adaptive recovery (auto-refreshes every 10s).
See the Appliance Health & Recovery design for the full model.

- Header: overall appliance badge; a **boot-convergence** banner if the box hasn't
  finished coming up. **Summary tiles:** Healthy · Degraded · Recovering ·
  Crash-loop · Failed · Starting.
- **Services table** (scd, edged, netd, portald, acctd, hotel-admin, caddy, Kea,
  Unbound, PostgreSQL): Service · State · Health check (✓/✗ + dependency + detail)
  · Restarts (count + in-window + consecutive) · Backoff / next retry · Last
  failure · Uptime.
- **Row actions:** **Recheck** · **Logs** (recent, sanitized) · **Restart**
  (writers only — requires a **reason** + your **password**; audited).
- Click a service for a **detail drawer**: full counters, exit code/signal,
  dependency, recent sanitized logs, and **recovery history**.

### License — `/license`
Activate the appliance and view licensing/entitlements (polls every 5s).

- **Header badge:** Active / Licensed / Pending / Hardware mismatch / **Not
  activated**.
- **Banners:** **Permissive-blocked** (critical — a blocked attempt to run
  unlicensed) · **Hardware mismatch** (WAN NIC changed — running on grace, ask for
  a Rebind) · **Grace period** · **Expired / Revoked / Suspended** · **Capacity
  reached**.
- **Appliance identity** card — the two values you send to StayConnect to
  activate: **StayConnect Serial Number** and **WAN MAC Address** (one-click copy).
- **License** card — a usage meter (online guests vs limit), plus Activation,
  License status, Max concurrent online guests, Valid from/until, Grace period,
  Grace ends, Customer, Hotel/Site.
- **Offline activation** — **Upload license file** (a signed `.license`/`.json`
  from StayConnect, generated for this exact Serial + WAN MAC).
- **Advanced details** — identity/transport fingerprints + an **Entitlements**
  table (PMS, Paid WiFi, SMS OTP, Email OTP, Social login, HA, White label).

### Backups — `/backups`
Read-only list of backup runs: Started · Finished · Status · Kind · Path · Size ·
Error.

### Audit log — `/audit`
Operator/system action trail. Filter by **Action** + **Limit**. Columns: Time ·
Actor · Action · Target · IP · Payload.

---

## Which actions need extra confirmation

| Action | Where | Confirmation |
|---|---|---|
| Apply WAN/LAN change | WAN / LAN settings | Password + timed auto-rollback |
| Roll back WAN/LAN | WAN / LAN settings | Password |
| Rotate TLS certificate | TLS certificate | Reason + password + type `ROTATE` |
| Restart a service | Diagnostics | Reason + password |
| Apply guest-network change | Guest networks | Timed confirm / rollback (120s) |
| Delete / disconnect / revoke | most pages | Browser confirm |
