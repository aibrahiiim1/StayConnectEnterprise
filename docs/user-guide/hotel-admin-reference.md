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
- **"PMS offline" does not mean guests are cut off.** The appliance keeps its own copy of the guest list, so
  room sign-in, vouchers, guest accounts, packages and every session in progress continue working while the
  PMS link is down — this is a normal operating mode on this property, not a fault. What you lose is *news*:
  arrivals and changes the PMS made since the last completed sync are not known here until the link is back,
  so a guest who checked in during the outage cannot sign in by room number yet. Judge the situation on four
  separate facts, not one: is the PMS link up, is the local guest list present, how old is it, and can a guest
  actually get online. Only the last one is an outage. See
  [Edge architecture §5A](../EDGE_ARCHITECTURE.md).
- **The sidebar can be collapsed to an icon rail.** The button beside the
  StayConnect mark collapses the menu to icons and expands it again; it is
  labelled with what it will do ("Collapse sidebar" / "Expand sidebar") and works
  from the keyboard. Collapsed, every icon still shows its full name as a tooltip
  on hover **and** on keyboard focus, the current page keeps its highlight, and
  your account and **Sign out** stay where they are. The magnifier reopens the
  menu and puts the cursor straight in the filter, so "find a screen" never
  requires expanding it first. Your choice is remembered on that device — across
  navigation, refresh and later sessions — and the default is expanded. On phones
  and narrow windows nothing changes: the menu is still the full labelled drawer
  behind the ☰ button, because names like "Duplicate sources" are not guessable
  from an icon.
- **Roles decide what you see.** The menu has six groups; you only see items your
  role can read, and write controls (New/Edit/Delete) appear only if your role can
  write. The appliance enforces this server-side regardless of the UI.
- **Password step-up.** A few high-impact actions ask you to re-enter your
  password: **applying/rolling-back WAN-LAN changes**, **rotating the TLS
  certificate**, and **restarting a service** in Diagnostics.
- **Secrets are never shown back.** API keys, client secrets, Stripe keys, auth
  keys, the enrollment token and private keys are write-only/masked.
- **Light, dark or follow the system.** The control sits in the top bar of every
  page (and on the sign-in page). There are three states — **Light**, **Dark** and
  **System** — and **System is the default**, so until you choose otherwise the
  appliance admin follows your computer's own light/dark preference. Your choice is
  remembered in that browser.
- **Edits open in a dialog.** Add/Edit opens a modal over the list you were reading
  rather than pushing a form into the page, so the record you are editing stays in
  view and the form can never open off-screen. `Esc` or clicking outside closes it;
  nothing is saved until you press the confirming button.
- **Destructive actions say what they do.** Disabling, removing or disconnecting
  asks in a dialog that names the consequence ("guests on this network will no
  longer be able to sign in with their room number"), and where the appliance
  requires a reason and your password, both are collected in that dialog with the
  password masked. No action uses a browser prompt.
- **One page gutter.** Every screen is inset from the window edge and the menu by
  the same margin, supplied by the shell rather than by each page.
- **Every field has a label.** Controls are programmatically bound to their labels,
  and figures that are not self-explanatory carry a **?** you can hover or focus for
  a sentence explaining what the number means.

Groups: **Overview · Internet offering · Guests · Property management system ·
Charges · Guest portal · Networking · System**. Which groups you see depends on your
role and on which capabilities are enabled for the appliance.

---

## OVERVIEW

### Dashboard — `/dashboard`
"Tonight at a glance" — the operational state of the property, refreshed every 30s
and read-only. Everything labelled *today* is counted from **this appliance's** local
midnight, and the page names the date it means.

- **Needs attention** — appears only when something actually needs a person: the
  site database or session controller unreachable, no signed licence installed, a PMS
  connection that cannot verify guests, PMS messages waiting for a decision, room
  charges awaiting review, or a cloud reporting queue that has stopped draining. Each
  line says what it stops and links to the screen that fixes it. When nothing is
  wrong, the strip is absent rather than green.
- **Tiles:** **Guests online** (counted by what the internet was granted to — one
  room, account or voucher — so a family with four devices is one guest, with the
  device count beneath) · **Sign-ins today** and the busiest hour · **Data today**
  (down/up split) · **Licence** state in plain words.
- **Sign-ins by hour** — a 24-column chart of sessions started today. Quiet hours are
  shown as zero rather than omitted. It deliberately plots sign-ins and **not** data:
  hour-by-hour traffic lives in the usage ledger, which this admin service is not
  permitted to read, and attributing a guest's whole evening to the hour they
  connected would be an invented number.
- **Occupancy** — in house, how many of those rooms have an internet package,
  arrivals and departures today, with a meter for the first against the second. A room
  with no package normally means nobody from it has signed in yet.
- **Property management system** — per connection, stated as *whether a guest can
  sign in with their room number right now*, plus guests in house, the ingestion
  backlog, how many messages need a decision, and when the last message arrived. A
  guest-list refresh in progress is shown as such.
- **Room sign-in checks** — the last 24 hours as a verified/refused split with the
  reasons behind the refusals. No guest is named.
- **Packages in use** — what guests are actually on right now, and how many were given
  out in the last 7 days.
- **Guest networks** — devices online per network and how much of each address pool is
  in use.
- **Services this appliance depends on** — the site database, the session controller
  and cloud reporting, each described by **what stops working** if it is down rather
  than by its process name. The cloud reporting row states the waiting and given-up
  counts and says plainly that guest internet, sign-in and the PMS do not depend on
  that queue.
- **Charges posted to the PMS** — posted, rejected, queued, awaiting review and
  outcome-unknown. If room charging is not in use on the appliance the card says so
  instead of showing zeros.

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

- **Columns:** Label · Count · **Format** (generation mode/length/prefix) ·
  **Totals** (unused/active/used/revoked) · Created · (View codes · CSV · Revoke
  unused).
- **New batch** form: **Plan**, **Count** (1–10000), **Label**, **Code length**
  (6–10; the **random portion** only), **Character mode** (Numbers / Uppercase
  letters / Uppercase letters and numbers / Uppercase-lowercase letters and
  numbers), **Prefix** (optional, A–Z/0–9; *additional* to the random portion),
  **Exclude ambiguous** (on by default). A live **example** and the exact
  **character set** are shown. Codes use secure random generation, are globally
  unique, and always exclude I/L/O/U (so a printed code matches what is typed).
- **Batch detail** (`/voucher-batches/{id}`): search/filter codes by text and
  state; click a code for its **Details** — state + **exhaustion reason** (time /
  data / revoked), plan, duration **window**, speed, **first activated**, **valid
  until**, **time remaining**, **max/active devices**, **data cap / used
  (all devices) / remaining**; **copy**; **print**;
  **download CSV**; **revoke** an unused code. **Change plan** for one voucher or
  the whole batch (*Unused only* / *All eligible*) from a plan dropdown — unused
  vouchers change immediately, vouchers with a live session are skipped, and
  revoked/expired/exhausted vouchers are never changed. The code and history are
  preserved; each change is audited (previous plan, new plan, operator, reason).

### Guest accounts — `/guest-accounts`
*(Add/Edit open in a dialog; removals confirm in one that names what
stops working.)*
Username & Password guest sign-in — an alternative to vouchers, bound to a Guest
Access Plan. **License capacity is appliance-wide; the plan's max devices is
per account** — both are enforced on every login.

- **Columns:** Username · Name · Plan (inactive badge if retired) · **Devices**
  (active *of* max) · Status · **Locked** · Validity · Last login · Logins.
- **New account:** Username (1–64; one letter/digit allowed; case-insensitive,
  unique per property), Password (1–128, case-sensitive; short allowed with a
  non-blocking weak-password warning; **write-only**), **Plan** (dropdown with
  duration/speed/max-devices), optional display name / valid-from / valid-until /
  notes. Password can be typed (show/hide) or **Generated**.
- **One-time password:** after create/reset the exact password is shown **once**
  with **Copy**, then never retrievable. Only an Argon2id hash is stored; no API,
  list, export, log or audit payload returns the password or hash.
- **Edit** (pre-filled form): username, plan, display name, valid-from/until,
  enabled, notes — no delete/recreate. Plan changes apply to future logins only;
  a running session keeps its policy.
- **Per-account actions:** **Edit**, **Password** (set/reset with optional
  *Generate* and *Disconnect existing sessions after reset*), **Enable/Disable**,
  **Disconnect** active devices, **Delete**.
- **Portal toggle:** *Show Username & Password tab on the captive portal*.
  Wrong/unknown/disabled/expired/locked all return one **generic** error and
  create no session; per-account lockout plus layered throttling (username+IP,
  username+device, endpoint-wide) damps brute force.

### Active sessions — `/sessions`
Every device currently online and **which guest it belongs to**. A session is one
device; a guest may have several.

- **Online now / Recent** switch, a search box (room, guest name, username, IP or
  MAC) and a filter by how the guest signed in.
- **Tiles:** devices online · guests online · rooms online · data in the list.
- Each row leads with the guest: **Room 412 · Anderson** for a room sign-in, the
  **username** for an account, **Voucher** for a voucher. The voucher **code is not
  shown and the row says why** — the admin service holds no privilege on the voucher
  table, so the code is genuinely unreadable here rather than omitted. A session whose
  access record cannot be read falls back to the MAC address and is marked as having
  no guest record, so an unexplained device is visible rather than silently normal.
- Remaining columns: how they signed in and when · the **internet package** and its
  speed · **allowance used** (data and time meters, shown only where the plan sets a
  limit — an unmetered plan says *Unlimited* rather than showing an empty meter) ·
  guest network and IP · data down/up · status.
- Clicking a row opens a detail dialog: usage, when access ends, the package and
  service plan, the network, MAC/IP, and for a room sign-in the room, reservation and
  a link to the stay.
- **Disconnect** asks in a dialog that names the guest, the device and how many of
  their other devices stay online. Their other devices are unaffected and they can
  sign in again.

---

## GUESTS (continued)

### Stays — `/stays`
What the property management system reports about who is in the building, and **which
internet package each room has**. Read-only: stays are changed in the PMS.

- Tiles: stays listed · how many have an internet package · devices online · arriving.
- Search by room, guest, reservation or package; filter by status (In house by
  default).
- The **Internet package** column shows the live package, its speed and the devices
  online against the allowed limit, or *None yet* — which for an in-house room usually
  means nobody from it has signed in, since a package is granted at sign-in and not at
  check-in.
- **View** opens a dialog led by an *Internet for this room* block (package, service
  plan, speed, devices of limit), then arrival/departure, charge permission and why it
  is closed, when the PMS last confirmed the stay, the occupants and the folios.
- If the guest list has arrived and **no** in-house room has a package, the page says
  so and points at Internet packages and Network routing.

---

## PROPERTY MANAGEMENT SYSTEM

### PMS connection — `/pms-interfaces`
The link to the hotel's property management system: what lets a guest get online by
typing their room number and name, and where the appliance's copy of the guest list
comes from.

- Tiles: whether **room sign-in** is working · guests in house · messages waiting to
  be applied · when the guest list was last fully refreshed (or that a refresh is
  running).
- The list leads with **"Can guests sign in?"** and, when the answer is no, the reason
  in plain words. Also per row: the link state and when the PMS was last heard from ·
  the guest list state, or *Loading now* with the live record count · the backlog, with
  a link when messages need a decision.
- **Manage** opens the detail: connection status across three axes (connection, live
  updates, guest list) each with a **?** explaining the question it answers; the
  **guest list refresh** section; configuration history; and which Wi-Fi networks use
  this connection.
- **Refresh the guest list now** asks for a reason and your password in a dialog that
  states the current list stays in use until the new one is complete, so nobody online
  is interrupted. While a refresh runs the page shows the real record count and says
  plainly that the PMS provides no total, so there is no percentage to show.
- **Settings** and **Add connection** are dialogs. Timing settings are folded away
  behind *Show timing settings*; the two fields that matter (address and time zone) are
  not buried among them.
- Putting a connection into or out of use requires a reason and your password.

### Network routing — `/pms-routing`
**Which PMS each Wi-Fi network checks.** When a guest types a room number, the
appliance decides which property management system to check it against from the network
the device is on.

- Getting this wrong produces no error anywhere: the guest is checked against another
  property's guest list, finds no matching room, and cannot get online while every
  other screen reports healthy. The page says this at the top.
- Two lists: networks that **can** offer room sign-in (with the connection and the
  scope), and networks with **no** PMS — which is a legitimate configuration, because
  vouchers and guest accounts never involve the PMS.
- **Settable here, by a Site admin only.** Change / Remove / Point at a PMS open a
  dialog; every other role sees the page read-only, matching what the appliance
  enforces.

### PMS activity — `/stay-events`
Every message the PMS has sent: check-ins, check-outs and stay changes, and whether the
guest list was updated from each one.

- Opens on the **whole feed**, not on the review queue — a screen that opens empty is
  read as "the feed is dead". When messages do need a decision, a banner says how many
  and offers to filter to them.
- Tiles: when the last message arrived · applied · needing a decision · **not matched
  to a stay**.
- Each row is **about a room**, with the guest or reservation beneath, the event in
  hotel words (Check-in, Check-out, Stay changed), and the result with what it means. A
  message the appliance could not match to a stay says so — usually why it is still
  waiting.
- **Details** carries the PMS's own message identifier, which is the string to quote to
  the PMS vendor and is useful for nothing else.

### Guest sign-in checks — `/pms-resolutions`
Every recent attempt to verify a guest against the PMS, and what the appliance
concluded. **No guest is named on this page** — a list of who tried and failed would let
any read-only account work out who is staying, so it is deliberately not collected.

- Tiles: checks recorded · let online · refused · networks involved.
- **Why guests were refused** explains every outcome and what to do about it.
- **By Wi-Fi network** shows how many got in per network, and the page calls out the
  pattern that matters: all attempts failing on ONE network while others succeed,
  which usually means that network points at the wrong PMS or at none.
- The table is timestamps, network names and outcomes only.

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
*(Add/Edit open in a dialog; removals confirm in one that names what
stops working.)*
Configure the email/SMS senders that deliver OTP codes to guests.

- **Kinds:** email → `stub` / `sendgrid` / `ses`; sms → `stub` / `twilio`.
- **Columns:** Channel · Kind · Sender · Health · Enabled.
- **New/Edit** form: Channel, Kind, Display name, API key (write-only), API user
  (Twilio SID for SMS); email adds From address / From name.

### Social login — `/social-providers`
*(Add/Edit open in a dialog; removals confirm in one that names what
stops working.)*
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
*(Add/Edit open in a dialog; removals confirm in one that names what
stops working.)*
Allow pre-authentication (before login) access to portal/payment endpoints.

- **Columns:** Kind (domain/cidr/ip) · Value · Ports (all if blank) · Description.
- **New rule** form: Kind, Value, Ports (comma, blank = all), Description.

### Portal branding — `/portal-branding`
Edit the captive-portal branding document (logo URL, terms, languages, colors) as
raw JSON. **Save** validates it's a JSON object first. Read-only unless your role
can write.

### Operators — `/operators`
*(Add/Edit open in a dialog; removals confirm in one that names what
stops working.)*
Create Hotel Admin staff accounts and manage roles/passwords.

- **Columns:** Operator (+ "(you)") · Roles · Status · Created.
- **New operator** form: Email, Display name, **Password (min 10)**, **Role** (one
  of the seven site roles — see the config manual). 
- **Actions:** **Set password**, **+ role** / remove role, **Disable**. You can't
  remove your own `site_admin` role or disable yourself.

---

## NETWORKING (all gated by the `network` / Hotel IT role)

### WAN / LAN settings — `/network/system`
Configure the appliance's WAN/management IP and the legacy base-bridge gateway,
with a **preview → apply → confirm** flow and automatic rollback.

> **This page is only the WAN uplink + the legacy base bridge — it is NOT where
> guest WiFi lives.** Your Guest Networks (each with its own VLAN, bridge, gateway,
> DHCP pool and captive portal) are created and checked on the **Guest Networks**
> (`/network`) and **DHCP & leases** (`/network/dhcp`) pages. Example: guest network
> `CHR` → VLAN 90 → `ens192.90` → bridge `br-g90` → gateway `10.20.0.1/22` → DHCP
> pool `10.20.0.100–10.20.3.250`.

- **Status cards:** **WAN / Management** (interface, MAC, link, IP mode, IP,
  mask, gateway, DNS, management URL, connectivity dots for gateway/internet/DNS,
  drift warning) and a **Guest Networks** pointer card (links to the Guest Networks
  and DHCP & leases pages).
- **Advanced · Base LAN / Legacy Bridge** (collapsible): the appliance's legacy
  base bridge (`br-lan`) — interface, bridge, MAC, link, base gateway IP, DNS,
  members. It carries a **Legacy** badge; when guests are served by Guest Networks
  its DHCP shows **off** here, which is **normal** (not a warning). Guest DHCP is
  never configured on this bridge.
- **Change configuration** (writers): WAN IP / prefix / gateway / DNS; base-LAN
  gateway IP / prefix (DHCP is read-only here — manage it per guest network on the
  DHCP page).
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

### Setup / Activation — `/setup/enrollment`
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
