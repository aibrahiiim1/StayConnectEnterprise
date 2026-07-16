# StayConnect Enterprise — Complete Operations Manual

> **Audience:** an IT engineer who is new to StayConnect and has no knowledge of
> how the product was built. If you read this document top to bottom you can take a
> hotel from an unpacked appliance to live, licensed guest WiFi, and run it day-2.
>
> **This is the recommended starting point.** For screen-by-screen reference and
> shorter step lists, see the companion documents linked in
> docs/user-guide/README.md.
>
> Everything here is verified against the current production code (not against UI
> text or older docs). Where an operator-facing label differs from an internal
> value, both are given.

---

## Table of contents

1. [System architecture at a glance](#1-system-architecture-at-a-glance)
2. [Central vs Appliance — who does what](#2-central-vs-appliance--who-does-what)
3. [The one official onboarding workflow](#3-the-one-official-onboarding-workflow)
4. [Physical install & first boot](#4-physical-install--first-boot)
5. [WAN / LAN connectivity](#5-wan--lan-connectivity)
6. [Automatic registration → Pending activation](#6-automatic-registration--pending-activation)
7. [Activation: customer, site, license terms, one click](#7-activation-customer-site-license-terms-one-click)
8. [Convergence: how the appliance becomes Active](#8-convergence-how-the-appliance-becomes-active)
9. [The license model (states & enforcement)](#9-the-license-model-states--enforcement)
10. [Concurrent online guest capacity](#10-concurrent-online-guest-capacity)
11. [Guest networks / VLANs](#11-guest-networks--vlans)
12. [Worked examples: VLAN 100 & VLAN 200](#12-worked-examples-vlan-100--vlan-200)
13. [DHCP, DNS, NAT & the captive portal](#13-dhcp-dns-nat--the-captive-portal)
14. [Guest authentication methods](#14-guest-authentication-methods)
15. [Access plans & vouchers](#15-access-plans--vouchers)
16. [Integrations: PMS, OTP, Social, Payments](#16-integrations-pms-otp-social-payments)
17. [Walled garden](#17-walled-garden)
18. [Guest zero-to-internet acceptance test](#18-guest-zero-to-internet-acceptance-test)
19. [License lifecycle behavior (Active → Grace → Expired → Suspended → Revoked)](#19-license-lifecycle-behavior)
20. [License renewal & anti-replay](#20-license-renewal--anti-replay)
21. [Reboot & service recovery](#21-reboot--service-recovery)
22. [Central outage behavior (offline operation)](#22-central-outage-behavior-offline-operation)
23. [Appliance replacement & WAN-MAC rebind](#23-appliance-replacement--wan-mac-rebind)
24. [Factory reset](#24-factory-reset)
25. [Deactivate / Revoke / Decommission / Delete](#25-deactivate--revoke--decommission--delete)
26. [Safe deletion & dependency order](#26-safe-deletion--dependency-order)
27. [Security alerts & certificate monitoring](#27-security-alerts--certificate-monitoring)
28. [Backup & recovery](#28-backup--recovery)
29. [Audit & monitoring](#29-audit--monitoring)
30. [Troubleshooting](#30-troubleshooting)
31. [Go-live checklist](#31-go-live-checklist)
32. [Day-2 operations checklist](#32-day-2-operations-checklist)
33. [Terminology (canonical operator terms)](#33-terminology-canonical-operator-terms)
34. [Advanced / exceptional: enrollment tokens & offline activation](#34-advanced--exceptional-enrollment-tokens--offline-activation)

---

## 1. System architecture at a glance

StayConnect has two tiers:

- **Central Control Plane** ("Cloud Admin", **Control Panel**) — a multi-tenant
  web app (`cloud-admin`, Next.js) backed by a Go API (`ctrlapi`/control-plane).
  You manage every hotel's customers, sites, appliances and **licenses** here.
  Central is the licensing authority and the fleet console.
- **Appliance** (the on-site gateway) — a hardened box running Go daemons and an
  on-box web app called **Hotel Admin** (`hotel-admin`, Next.js). The daemons:
  - `scd` — supervisor / guest-authorization / license state.
  - `edged` — resource CRUD (guest networks, DHCP, integrations) + Central channel.
  - `netd` — applies WAN/LAN/VLAN/nftables changes with an auto-rollback watchdog.
  - `portald` — serves the captive portal (`:8380` HTTP, `:8343` HTTPS).
  - `acctd` — traffic accounting & shaping.
  - Plus `caddy` (TLS reverse proxy for Hotel Admin), `kea` (DHCP), `unbound` (DNS).

The appliance **does not broadcast WiFi**. Your wireless controller / APs
broadcast SSIDs and tag them onto VLANs; the appliance is the gateway, DHCP
server, captive portal, firewall and license enforcer for those VLANs.

The trust between the two tiers is **mutual TLS (mTLS)** plus a **signed license**
and **signed assignment**. The appliance holds an Ed25519 identity key it
generates itself on first boot; Central never learns the private key.

---

## 2. Central vs Appliance — who does what

| Responsibility | Central (Control Panel) | Appliance (Hotel Admin) |
|---|---|---|
| Create customers, sites | ✅ | — |
| Activate an appliance, issue/renew/revoke its **license** | ✅ | — (shows state; can import an offline license file) |
| Fleet health, security alerts, certificates, audit | ✅ | Local health/audit only |
| Guest networks / VLANs, DHCP, DNS, NAT | — | ✅ |
| Captive portal branding, auth methods, walled garden | — | ✅ |
| Access plans, voucher batches, sessions | — (issues license terms) | ✅ |
| PMS / OTP / Social / Payments integrations | — | ✅ |
| WAN / LAN IP configuration | — | ✅ |
| Enforce concurrent-guest capacity & license state | — | ✅ (locally, offline-safe) |

**Rule of thumb:** Central decides *whether and how much* a hotel may serve (the
license). The appliance decides *how the hotel network actually works* and
enforces the license locally, even if Central is unreachable.

### Ownership hierarchy (how everything is organized)

```
Platform  →  Customer  →  Site  →  Appliance  →  Guest Networks / VLANs
```

- **Customer** = the hotel group / company / owner (a tenant). A Customer owns
  one or more Sites.
- **Site** = **one physical property** (a single hotel/resort/deployment
  location). A Site belongs to exactly one Customer and contains one or more
  Appliances. Buildings, floors, wings, SSIDs and guest VLANs are **not** Sites —
  they are configured on the appliance.
- **Appliance** = the on-site gateway. It belongs to exactly one Site at a time;
  moving it to another Customer's Site requires the explicit, audited
  cross-customer reassignment (never a silent move).

These rules are enforced in the UI, the API, **and** the database (a composite
foreign key makes an appliance-under-another-customer's-site impossible).

### Customer context (Control Panel)

The Control Panel has a **Customer context** selector at the top of the sidebar.
Platform admins pick **All Customers** or one specific Customer; the choice
persists across navigation and refresh. It scopes the customer-owned pages
(Sites, Appliances, Licenses, Operators, Audit, Dashboard) to the selected
Customer. **Creating a Site or Appliance requires a specific Customer to be
selected** — create forms show "Owner: <Customer>", and in All-Customers mode
creation is disabled until you choose one. A tenant operator has no selector and
is pinned to their own Customer (enforced server-side). See the Control Panel
reference for details.

---

## 3. The one official onboarding workflow

This is the **only** normal way to bring a hotel online. Use it every time.

1. **Install the appliance** at the site and cable WAN + LAN.
2. **Configure WAN** only if the site does not provide DHCP on the WAN uplink
   (many do — then this step is automatic).
3. The appliance **automatically appears in Central** under **Onboarding** as
   **Pending activation** (no token, no manual steps).
4. In Central, **create or select the Customer**.
5. **Create or select the Site**.
6. Set **Max Concurrent Online Guests** (the licensed capacity).
7. Set **License Validity** (valid-from / valid-until).
8. Set **Grace Period** (days the hotel keeps serving after expiry).
9. Click **Activate** — one click runs claim → assign → certificate → signed
   license.
10. **Wait for automatic convergence** — the appliance pulls its certificate and
    license itself; its status walks to **Active**.
11. **Verify Active** in Central (Onboarding/Appliances) and on the box
    (Hotel Admin → License).
12. On the appliance, **configure Guest Networks / VLANs**.
13. **Configure authentication methods** (voucher / OTP / PMS / social / payment).
14. **Perform a real guest acceptance test** (connect a device, reach the portal,
    authenticate, reach the internet).
15. **Go live.**

> Enrollment tokens are **not** part of this workflow. They exist only for the
> advanced/exceptional cases in §34. Do not hand installers a token for a normal
> install — a factory-clean box with internet self-registers on its own.

---

## 4. Physical install & first boot

- Rack/mount the appliance. Connect the **WAN** NIC to the site uplink and the
  **LAN/trunk** NIC to the switch that carries your guest VLANs.
- Reference topology used throughout this manual:
  - **WAN interface: `ens160`** (uplink to the internet / site network).
  - **LAN trunk interface: `ens192`** (802.1Q trunk to your switch; guest VLANs
    ride on it).
- Power on. On first boot with no stored identity, `scd` generates an Ed25519
  identity keypair, detects hardware (serial, WAN MAC, hardware fingerprint), and
  — if it has internet — self-registers with Central (see §6). No operator action
  on the box is required for a normal install.
- If the box has **no internet yet**, it simply waits; it will register as soon as
  WAN connectivity is established.

---

## 5. WAN / LAN connectivity

WAN/LAN is configured **on the appliance** in Hotel Admin → **WAN / LAN settings**
(`/network/system`). You normally only touch this if the WAN is static or the base
bridge gateway must change.

> This page covers only the **WAN uplink** and the appliance's **legacy base
> bridge** (`br-lan`). It is **not** where guest WiFi lives — each Guest Network
> has its own VLAN, bridge, gateway, DHCP pool and captive portal (see §11).
> Example: `CHR` → VLAN 90 → `ens192.90` → bridge `br-g90` → gateway `10.20.0.1/22`
> → DHCP pool `10.20.0.100–10.20.3.250`. The legacy base bridge is shown under an
> **Advanced · Base LAN / Legacy Bridge** section with a *Legacy* badge; its DHCP
> showing **off** there is normal when guests are served by Guest Networks.

1. Review the **WAN / Management** status card and the **Guest Networks** pointer.
2. Under **Change configuration**:
   - **WAN:** IP address, prefix length, default gateway, DNS (comma-separated).
   - **Base LAN:** base-bridge gateway IP, prefix length (guest DHCP pools are
     managed per guest network on the DHCP page, not here).
3. Click **Validate & preview** — check the before/after and the new management URL.
4. Click **Apply change** and **re-enter your password**.
5. A **countdown banner** appears. If the management IP changed, reconnect to the
   new URL, then click **Keep this configuration**.
6. **If you do not confirm within the window (default 120 seconds), the change
   auto-rolls-back.** A wrong IP can never lock you out.

The same apply-and-confirm safety wraps guest-network changes (§11).

---

## 6. Automatic registration → Pending activation

- A factory-clean appliance auto-registers by POSTing a **self-signed** request
  (proving possession of its identity key) to Central's public, token-less
  `POST /v1/appliances/register`. This is trust-on-first-use.
- Central creates the appliance record in state **`pending_approval`**
  (internal `lifecycle_state`), which the Control Panel shows to operators as
  **"Pending activation."** On the box's own setup screen the equivalent word is
  `pending_activation`.
- **Clone / hardware-reuse protection:** if a known identity key appears from
  different hardware, or a serial reappears with a new key while an active record
  exists, Central refuses (HTTP 403) and raises a **security alert**. The correct
  remedy is to **decommission** the old record first (§25).

You do **not** approve a raw registration by itself — you **activate** it (§7),
which both approves and licenses the box in one step.

---

## 7. Activation: customer, site, license terms, one click

In Central go to **Onboarding**, find the Pending appliance, and **Activate** it.
The form asks for exactly what the license needs:

- **Customer** (tenant) — create or select.
- **Site** — create or select (a site belongs to a customer).
- **Max Concurrent Online Guests** — the licensed capacity (use `-1` / "Unlimited"
  only where intended).
- **License Validity** — valid-from / valid-until.
- **Grace Period (days)** — how long the hotel keeps serving guests after the
  license expires.

Clicking **Activate** performs, server-side and atomically:

1. **Claim** the appliance.
2. **Assign** it to the customer/site and issue a **vendor-signed assignment**.
3. **Issue a certificate** — Central waits briefly for the appliance's CSR and
   issues its mTLS certificate (if the CSR hasn't arrived yet, it issues
   automatically the moment it does).
4. **Issue a hardware-bound signed license** with the terms you entered.

There is **no plan and no subscription** step. The signed appliance license *is*
the entitlement.

The appliance's internal `lifecycle_state` walks
`pending_approval → claimed → assigned → activated`, and then the licensing
reconciler maintains the time/license-driven states (`licensed`, `grace`,
`license_expired`).

---

## 8. Convergence: how the appliance becomes Active

Central does **not** push the license. The appliance **pulls** everything itself
over its authenticated channel:

1. It submits a CSR and fetches its issued **certificate**.
2. It then fetches its **signed license** from Central
   (`GET /v1/appliance/license`), immediately and on a periodic loop.
3. Once the valid license is installed, the box reports **Active** (Hotel Admin →
   License and Central → Appliances/Onboarding).

Convergence is normally seconds-to-a-minute. If Central is briefly unreachable
during this window, the box keeps retrying; fetch failures are non-fatal.

**Verify Active:** Central shows the appliance Active/licensed; Hotel Admin →
**License** shows **State: Active**, the **Max online guests**, and **Valid until**.

---

## 9. The license model (states & enforcement)

The license is a **signed document bound to one appliance** (identity key
fingerprint + appliance id + serial + hardware fingerprint + WAN MAC). It carries:

- **max concurrent online guests** (the capacity),
- **validity window** (valid-from / valid-until),
- **grace period** (days),
- **entitled features** (e.g. PMS, paid WiFi, SMS/email OTP, social login, HA,
  white-label) — features are real license fields, gated per-license.

The appliance evaluates state locally against the signed document and the local
clock. States and what they do to **new** guest logins:

| State | New guest logins | Existing sessions | DHCP/DNS/portal/Hotel-Admin |
|---|---|---|---|
| **Active** | Allowed, up to capacity | Keep running | Up |
| **GracePeriod** (expired but within grace) | **Still allowed**, with renewal warning | Keep running | Up |
| **Expired** (past valid-until + grace) | **Refused** (`license_expired`, 403) | Keep running to natural end | Up |
| **Suspended** (billing hold) | **Refused**; provisioning off | Keep running | Up |
| **Revoked** (explicit revocation) | **Refused** immediately | Keep running | Up |
| **Unlicensed / "Pending activation"** (no valid license) | **Refused** (fail-closed, capacity 0) | — | Up |
| **Restricted** *(legacy pre-v3 licenses only)* | Allowed, but provisioning/features off | Keep running | Up |

Key guarantees:

- **A state change never drops existing guest sessions** — only *new*
  authorization is refused.
- **DHCP, DNS, captive portal and Hotel Admin always keep running**, regardless of
  license state, so staff never lose control of the box.
- **Production appliances fail closed:** with no valid license the state is
  `unlicensed` and capacity is 0. (Only development builds run permissively.)

---

## 10. Concurrent online guest capacity

- The cap is **per-appliance, appliance-wide across ALL its guest VLANs** — it is
  **not** per-VLAN and **not** per-tenant. Two guests on VLAN 100 and three on
  VLAN 200 count as five against one capacity number.
- It is enforced **locally** on the appliance inside the same transaction that
  creates a guest session (a per-appliance lock + a live count of active
  sessions). Central is never consulted, so enforcement works offline.
- `-1` means **unlimited**.
- **At capacity**, a new guest login is refused with HTTP 403
  `{"error":"LICENSE_CAPACITY_REACHED","limit":N,"current":M}` and **nothing is
  provisioned** for that guest (no firewall/shaping/accounting/session rows). A
  slot frees when an existing session ends.

To raise capacity, **renew/re-issue the license** in Central with a higher
Max Concurrent Online Guests (§20).

---

## 11. Guest networks / VLANs

Create guest networks in Hotel Admin → **Guest networks** (`/network`) →
**New guest network**. The 7-step wizard:

1. **Identity** — Name, Description, **SSID label** (a label only — the appliance
   does not broadcast; your controller maps the SSID onto the VLAN).
2. **Interface / VLAN** — pick the parent interface (guest-access or guest-trunk).
   Tick **VLAN tagged (802.1Q)** and set the **VLAN id** (1–4094) for a tagged
   network.
3. **Subnet & gateway** — Subnet CIDR and Gateway IP. The appliance owns the
   gateway; guests use it as gateway **and** DNS.
4. **DHCP & DNS** — DHCP pool ranges; DNS mode (**appliance** resolver or
   **custom servers**); domain name; lease default/min/max.
5. **Captive portal** — toggle Captive portal, Internet access, **NAT
   (masquerade)**, **Client isolation**. Portal is served at
   `http://{gateway}:8380`.
6. **Review** — includes the "map SSID → VLAN on your controller" reminder.
7. **Apply** — **Create, validate & apply**, then **Confirm within the countdown**
   (default **120 s**) or it **auto-rolls-back**.

> **Immutable after creation:** network **type**, **VLAN id**, **parent
> interface**, and **bridge name**. To change any of these, delete and recreate
> the network. All other settings are editable on the network's detail page.
>
> A guest network **cannot be deleted while it is enabled or has active
> sessions** — disable it / let sessions drain first.

**DHCP reservations:** pin a device MAC to a fixed IP on **DHCP & leases**
(`/network/dhcp` → Reservations → New) or from the network's detail page.

---

## 12. Worked examples: VLAN 100 & VLAN 200

Reference hardware: **WAN = `ens160`**, **guest trunk = `ens192`**.

### Example A — two VLANs, two different portals/experiences

| | VLAN 100 (Guest) | VLAN 200 (Conference) |
|---|---|---|
| Subnet | `10.100.0.0/24` | `10.200.0.0/24` |
| Gateway (appliance) | `10.100.0.1` | `10.200.0.1` |
| DHCP pool | `10.100.0.50–10.100.0.250` | `10.200.0.50–10.200.0.250` |
| Portal | Portal A branding | Portal B branding |
| Auth | Voucher + room (PMS) | Voucher only |
| Portal URL | `http://10.100.0.1:8380` | `http://10.200.0.1:8380` |

Steps: create two guest networks on parent **`ens192`**, VLAN tagged, ids **100**
and **200**, with the subnets/gateways above; give each its own DHCP pool; enable
captive portal + NAT on both; apply + confirm each. On your wireless controller,
map SSID "Hotel Guest" → VLAN 100 and SSID "Conference" → VLAN 200. Set branding
per network in Portal branding.

### Example B — two VLANs sharing one portal/experience

Create VLAN 100 and VLAN 200 exactly as above but give them the **same** portal
branding and the **same** enabled auth methods. Guests on either VLAN see an
identical login page. This is common when you segment traffic (e.g. floors or
buildings) for routing/DHCP reasons but want one guest experience.

### Capacity across VLANs

In **both** examples the **licensed Max Concurrent Online Guests is a single
appliance-wide number**. If the license allows 300 concurrent guests, that 300 is
shared across VLAN 100 **and** VLAN 200 (and any legacy `br-lan`), not 300 each.

---

## 13. DHCP, DNS, NAT & the captive portal

- **DHCP** is served by `kea` per guest network from the pools you defined.
  Reservations pin a MAC to an IP.
- **DHCP option 114** advertises the captive-portal URL
  (`http://{gateway}:8380`) so modern OSes auto-pop the portal (RFC 8910). This is
  the only reliable cross-OS auto-pop mechanism.
- **DNS** is served by `unbound`, either as the appliance resolver or forwarding
  to custom servers per network.
- **NAT (masquerade)** is per-network — enable it so guests reach the internet via
  the WAN. **Client isolation** (per-network) stops guests talking to each other.
- The **captive portal** (`portald`) listens on `:8380` (HTTP) and `:8343`
  (HTTPS) and is reached at the network's gateway IP.

---

## 14. Guest authentication methods

Guests can authenticate by:

- **Voucher** — a printed/emailed code (needs Access plans + Voucher batches, §15).
- **Username & Password (Guest Accounts)** — a named account with a password,
  bound to an Access plan (§15). Basic-access like vouchers (no license feature
  gate). Managed on **Hotel Admin → Guest accounts**; the portal tab is shown only
  when you enable it there. Passwords are stored hashed (argon2id) and are
  write-only.
- **OTP** — email or SMS one-time code (needs a Notifications provider, §16).
- **PMS** — room number + name checked against the hotel PMS (needs a PMS
  provider, §16).
- **Social login** — Google/Apple/Facebook/Microsoft (needs a Social provider).
- **Payment** — paid WiFi via Stripe (needs a Payments provider).

All methods share the **same** authorization pipeline (credential check → license
state → atomic appliance-wide capacity reservation → session → nft → shaping →
accounting); a failed login creates no session or authorization. Which methods you
may use depends on what you enable **and** what the license entitles (the License
page's Entitlements table shows the licensed features; voucher and
username/password are always available).
Make sure the portal, payment and OAuth callback hosts are reachable **before**
login via the **Walled garden** (§17).

---

## 15. Access plans & vouchers

**Access plan** (Hotel Admin → **Guest access plans** → **New plan**): Code, Name,
Description, **Duration (s)** (blank = unlimited time), **Data cap (bytes)** (blank
= unlimited), **Down/Up kbps**, **Max devices**, **Price (cents)**, **Currency**.

> "Max devices" on an access plan is a **per-credential device limit** — the
> concurrent devices allowed on one voucher or one guest account — a different
> concept from the license's appliance-wide concurrent-guest capacity (§10). Both
> are enforced on **every** login, atomically and concurrency-safe: a device
> rejected for either reason gets `MAX_DEVICES_REACHED` / `LICENSE_CAPACITY_REACHED`
> and no session/nft/shaping/accounting/voucher-activation is created. A reconnect
> from a device already signed in on the same credential does **not** consume a
> second slot; disconnect/expiry/reap frees the slot. Guest Access Plans are the
> per-guest tiers here — not the retired commercial *License Plans*.

**Voucher batch** (**Voucher batches** → **New batch**): choose an active **Plan**,
a **Count** (1–10000), a **Label**, and the **code generation options**:
- **Code length** 6–10 — the **random portion** only.
- **Character mode**: **Numbers** · **Uppercase letters** · **Uppercase letters
  and numbers** · **Uppercase/lowercase letters and numbers**. The form shows a
  live **example** and the exact **character set**.
- **Optional prefix** (A–Z/0–9, e.g. `PARTY`) — **additional** to the random
  portion.
- **Exclude ambiguous characters** (on by default): drops `0/O`, `1/I/L`, `5/S`.
  (`I, L, O, U` are *always* excluded so a code matches exactly what the guest
  types.) Codes use secure random generation and are globally unique; a batch too
  large for the chosen space is rejected rather than weakening randomness.

> **Voucher duration model (canonical):** a voucher's plan **Duration** is a
> **validity window** that opens at the voucher's **first activation** and runs
> on **wall-clock** time. The window end (`Valid until`) is fixed at that first
> use and is **durable** — it never moves for reconnects, extra devices, a crash,
> a service restart or a reboot. So a "10-minute" voucher gives **10 minutes of
> total access from first use**, shared by all devices the plan's Max devices
> allows; a second simultaneous device does **not** make the clock run faster,
> and disconnecting/reconnecting neither pauses nor resets it. Once the window
> closes the voucher shows **Expired** (reason: time). The **Data cap** is an
> **aggregate** across every session/device; when the combined usage reaches the
> cap the voucher shows **Exhausted** (reason: data). Consumed time and data are
> **derived** from the durable window and a live sum of session bytes — never
> accrued on session close — so usage is counted exactly once and a duplicate or
> retried close can't double-charge. (Guest **accounts** are reusable credentials
> by design — each login gets the plan duration afresh.)

Then **view the codes** (search/filter, copy, print, **download CSV**), open a
code for its **Details** (state, plan, duration, speed, data cap, max devices,
active devices, dates), **revoke** an unused code, or **Revoke unused** for the
batch. **Change a voucher's plan** from a dropdown — for one voucher or the batch
(*Unused only* / *All eligible*): unused vouchers change at once, a voucher with a
**live session** is never repointed (end it first), and revoked/expired/exhausted
vouchers can't be changed. The code, usage history and audit trail are preserved;
each change records previous plan, new plan, operator and reason. Legacy (12-char)
batches keep working unchanged.

**Guest accounts** (**Guest accounts** → **New account**): **username** (1–64;
one letter/digit allowed; case-**insensitive**, unique per property), **password**
(1–128, case-**sensitive**; short is allowed with a non-blocking weak-password
warning), an active **Plan** (dropdown showing duration/speed/max-devices), and
optional display name / valid-from / valid-until / notes. The password can be typed
(show/hide) or **Generated**, and is shown **once** afterwards with **Copy** — it
can never be retrieved later (only an Argon2id hash is stored; no read/list/export/
log/audit ever returns it). **Edit** any account in place (username, password,
plan, display name, validity, enabled, notes — no delete/recreate); plan changes
apply to **future** logins while a running session keeps its policy. The list shows
**active devices** (e.g. `1 of 2`), locked status, validity and login history; per
account you can **Disconnect** active devices and (on reset) optionally disconnect
existing sessions. Toggle **Show Username & Password tab on the captive portal** to
expose the method. Wrong/unknown/disabled/expired/locked all return one **generic**
error and create no session; per-account lockout plus layered throttling
(username+IP, username+device, endpoint-wide) damps brute force.

New plans/voucher batches/guest accounts require the license to permit
provisioning — if the license is Expired/Suspended/Revoked/Unlicensed you'll get a
"license doesn't currently allow…" error; renew or activate first.

---

## 16. Integrations: PMS, OTP, Social, Payments

- **PMS providers** → **New provider**: Name, **Kind** (`protel-fias` /
  `opera-fias` / `fidelio-fias` for FIAS; `mews` / `apaleo` for REST; `stub` for
  testing), Display name. FIAS: Host, Port, Auth key (write-only), Use TLS. REST:
  Base URL, API key (write-only), Property ID. Use **Test**, **Health**, **Cache**.
- **Notifications** (OTP email/SMS) → **New**: Channel (email/sms), Kind
  (`sendgrid`/`ses` for email; `twilio` for sms; `stub`), Display name, API key
  (write-only), API user (Twilio SID). Email adds From address / From name.
- **Social login** → **New**: Provider (google/apple/facebook/microsoft), Display
  name, Client ID, Client secret (write-only), Redirect URI, Scopes.
- **Payments** (Stripe) → **New**: Display name, Publishable key, Secret key
  (write-only), Webhook secret (write-only), Success/Cancel URLs. Recent purchases
  show in **Recent payments**.

---

## 17. Walled garden

Hotel Admin → **Walled garden** → **New rule**: Kind (domain/ip/cidr), Value,
Ports (comma; blank = all), Description. Add the hosts your portal, payment
callbacks and OAuth redirects need so guests can reach them **before**
authenticating. Keep the list small.

---

## 18. Guest zero-to-internet acceptance test

Do this before go-live, on a real device, per guest VLAN:

1. Join the guest SSID (mapped to the VLAN on your controller).
2. Confirm the device gets a DHCP lease in the expected subnet and the gateway/DNS
   is the appliance gateway IP.
3. Confirm the **captive portal auto-pops** (or browse to any HTTP site and get
   redirected to `http://{gateway}:8380`).
4. Authenticate with a real method (voucher/OTP/PMS/social/payment).
5. Confirm the device **reaches the internet** afterwards.
6. Confirm the session appears in Hotel Admin → **Sessions**.
7. Confirm the **concurrent count** increments (License page / dashboard).

If capacity is reached during testing you'll see `LICENSE_CAPACITY_REACHED` —
that's expected behavior, not a fault.

---

## 19. License lifecycle behavior

See the table in §9 for the enforcement matrix. Timeline of a normal license:

- **Active** from valid-from until valid-until.
- At **valid-until** it enters **GracePeriod** for `grace_period_days` — guests
  keep working, staff see renewal warnings.
- After **valid-until + grace**, it becomes **Expired** — new logins refused,
  existing sessions drain, the box stays up.
- **Suspended** and **Revoked** are administrative (billing hold / explicit
  revocation) and take effect on receipt regardless of dates; Revoked is the
  strongest and is delivered as a signed notice so the box stops even if it later
  reconnects.
- **Unlicensed** is the fail-closed default when no valid license is installed.

---

## 20. License renewal & anti-replay

- **Renew / change terms** in Central (Licenses → renew/re-issue, or re-activate).
  Every re-issue gets a **new, higher license version** and supersedes the prior
  one atomically. The appliance pulls the new envelope automatically.
- **Anti-replay is enforced on the appliance and survives reboot** (cleared only
  by factory reset):
  - **Monotonic version** — a lower version, or the same version under a different
    license id/fingerprint, is rejected even with a valid signature and Central
    offline.
  - **issued-at may not go backwards.**
  - **Revoked ids can never be re-installed.**
  - **Clock-rollback protection** via a persisted high-water mark (48h tolerance).
  - Rejections surface as `LICENSE_ROLLBACK_REJECTED`.

This means you cannot "downgrade" a hotel by replaying an old license file, and a
renewal issued while the box is offline still applies cleanly when it reconnects.

---

## 21. Reboot & service recovery

- The appliance daemons are supervised. On crash/reboot they self-heal with an
  adaptive backoff; Hotel Admin → **Diagnostics** (`/health`) shows each service's
  health, restart counts, backoff and recovery history.
- License state and anti-replay high-water marks are persisted, so a reboot does
  not change licensing.
- You should not normally intervene. If needed: **Recheck** re-runs a health
  check, **Logs** shows recent sanitized logs, and **Restart** (reason + password)
  restarts a service.

---

## 22. Central outage behavior (offline operation)

- **Guests keep working.** Guest authorization and the capacity gate evaluate
  entirely against the on-disk signed license and the local clock — nothing in the
  guest path calls Central.
- License fetch/refresh failures are **non-fatal** ("offline-safe"); they only
  affect renewal freshness and set a **Cloud validation stale** warning flag (a
  warning, not a state change).
- DHCP, DNS, portal and Hotel Admin are local and unaffected.
- Time-based transitions (Active → Grace → Expired) still occur offline via the
  local ticker, honoring the grace window.
- For fully offline sites, an **offline activation file** can be imported on the
  box (§34).

---

## 23. Appliance replacement & WAN-MAC rebind

**Replace** (swap hardware, keep the site): Central → appliance → **Replace**
mints a **72-hour single-use replacement token** bound to the same customer/site.
The **old box keeps its license until the replacement goes Active**, then the old
one is auto-terminated (bounded overlap). Use this for RMA / hardware swaps.

**WAN-MAC rebind** (same box, WAN NIC changed): a WAN-MAC-only mismatch is
**soft** — the license stays installed, the hotel keeps running in a mismatch
grace state, and a security alert is raised. Resolve it in Central →
appliance → **Rebind MAC** (new MAC + reason + confirm + step-up), which re-issues
a corrected hardware-bound license and clears the alert.

> A mismatch of **identity key / appliance id / serial / hardware fingerprint** is
> a **hard** reject — the license is refused and the box will not serve guests.
> That indicates the license and hardware genuinely don't match (wrong file, or a
> cloned box), not a simple NIC change.

---

## 24. Factory reset

- Factory reset is a **local appliance action**, not a Central action. It wipes the
  box's identity, license and config.
- Central authority is **not** deleted by a factory reset; the box is reconciled on
  its next hello. If you are permanently retiring the box, use **Decommission** or
  **Delete** in Central (§25) so its bound license is revoked.
- After a factory reset the box is factory-clean again and will self-register as
  Pending activation if it has internet.

---

## 25. Deactivate / Revoke / Decommission / Delete

All are Central actions with a single, shared rule: **every terminal path revokes
the appliance's bound license.** Choose by intent:

| Action | Reversible? | What it does |
|---|---|---|
| **Deactivate** | ✅ Yes | Revokes the bound license, drops the appliance to `assigned`, **keeps** identity/cert/assignment so it can be re-activated later. |
| **Revoke** | ❌ Terminal | Marks `revoked`/`retired`, revokes the bound license, and delivers a **signed revoked assignment** so the box stops even if it reconnects. |
| **Decommission** | ❌ Terminal | Same terminal path as Revoke (bound license revoked + signed terminal assignment). Use when permanently retiring hardware. |
| **Delete** | ❌ Permanent | Requires typing the **serial** + reason + password step-up. Revokes bound licenses and certs, then deletes the record (cascading assignments, certs, commands, networks, lifecycle events — terminating mTLS/NATS trust). |
| **Reassign** (cross-tenant) | — | Revokes the previous tenant's bound license and reassigns the box to a new customer/site. |

> **Site-wide licenses** (licenses not bound to a specific appliance) are
> deliberately **left valid** by these appliance-level actions.

### Cross-customer transition & secure data purge

An appliance's local site database holds tenant-owned guest data (access plans,
vouchers, sessions, guest PII, PMS/notification/social/payment **credentials**,
walled garden, portal config, operators, usage/accounting). When an appliance
moves to a **different Customer** — a genuine reassignment, or a Customer that was
**deleted and recreated** (a new tenant UUID), decommission-and-reuse, or an
ownership transfer — that previous customer's data must never remain readable
under the new owner.

The appliance enforces this automatically, comparing **immutable tenant UUIDs**
(never names/slugs):

- **Same-customer** deactivate/reactivate (same tenant UUID) → **all tenant data
  is preserved** (plans, vouchers, sessions stay valid).
- **Cross-customer** transition (different tenant UUID) → on the next boot the
  appliance **securely purges every previous-tenant row and cached secret** in one
  transaction *before it authorizes any guest*: it repoints the live guest
  networks (VLAN/DHCP/portal stay up) to the new owner, deletes all foreign-tenant
  rows across every tenant-owned table + the local tenant/site mirror, drops
  pending outbox events queued under the old identity, flushes runtime guest
  authorization (nftables), and writes an **audited transition record**
  (`appliance.tenant_transition_purge`) with the purged counts.
- **Fail-closed:** if the purge cannot complete, the appliance authorizes **no**
  guests (`tenant_transition_pending`) until it succeeds — a partial cleanup can
  never expose one customer's data to another. The purge is idempotent, so a
  retry (or reboot) completes safely.

Preserved across a transition: appliance/system/network/bootstrap state (WAN/mgmt,
identity, certs, guest-network topology) and the immutable security **audit
history**. Guest-facing data and secrets are not.

---

## 26. Safe deletion & dependency order

StayConnect never silently cascades a customer/site delete. Deletion is **blocked
while owned records still exist**, and the dialog lists exactly what to remove.

**Teardown order:** **Appliances → Site → Customer.**

- Delete/decommission the **Appliances** first (this revokes their bound licenses).
- Then delete the **Site** (a site with appliances is blocked).
- Then delete the **Customer** (a customer with sites is blocked).

There is **no subscription step** in the teardown — subscriptions were retired and
are never a delete blocker.

Notes verified in code:
- An **appliance delete** itself is unconditional once you type its serial (it
  relies on database cascade for its own dependents); the safety is the typed
  serial + password step-up + license/cert revocation, not a session check.
- A **guest network** cannot be deleted while enabled or with active sessions.
- A **customer hard-delete** purges its archived legacy data as part of the
  operation (legacy read-only guards are handled server-side in-transaction).

---

## 27. Security alerts & certificate monitoring

- **Security alerts** (Central) surface clone/hardware-reuse attempts, WAN-MAC
  mismatches, permissive-license attempts and similar events. Triage them on the
  **Security alerts** page. A hardware-reuse alert usually means an old record must
  be decommissioned before the new box can register.
- **Certificates** (Central) tracks appliance mTLS certs and expiry. On the box,
  the Hotel Admin **TLS certificate** page auto-renews the on-box cert (45-day
  window / on IP change / SAN drift); **Rotate** forces a fresh cert (reason +
  password; you never upload a key).

---

## 28. Backup & recovery

- Both Central and the appliance run automated backup with fail-safe
  artifact retention. Cleanup never deletes the current/previous release, the
  newest DB backup, PKI material, or pinned artifacts.
- Central exposes a **Backup health** page (and a backup-health API) so you can
  confirm backups are current across the fleet.
- For disaster recovery of an appliance, prefer **Replace** (§23) — it preserves
  the site binding and licenses cleanly.

---

## 29. Audit & monitoring

- **Every** privileged action in Central and on the appliance is written to an
  **audit log** with actor, action, target and reason. Delete/rotate/restart
  actions require a reason that is recorded.
- Central aggregates fleet **service health** and **security** telemetry over an
  mTLS NATS channel; the Dashboard and Fleet pages summarize it.
- Legacy `subscription.*` audit action names may appear on **historical** rows;
  no new normal-workflow action uses them.

---

## 30. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Appliance never appears in Central | No WAN/internet, or `SCD_AUTO_REGISTER=false` | Fix WAN (§5); confirm auto-register, or use a token (§34) |
| Registration refused (403) + security alert | Clone/hardware-reuse protection | Decommission the old record (§25), then retry |
| Stuck at Pending activation | Not activated yet | Activate it (§7) |
| Stuck activating / no license | CSR/license not yet pulled, or Central briefly unreachable | Wait; check Diagnostics; confirm Central reachability |
| Guests denied, License shows Expired | Past valid-until + grace | Renew the license (§20) |
| Guests denied, `LICENSE_CAPACITY_REACHED` | At appliance-wide concurrent capacity | Wait for a slot, or raise Max Concurrent Online Guests (§20) |
| Guest denied, `MAX_DEVICES_REACHED` | The voucher/account is at its **plan max devices** | Disconnect a device (Hotel Admin → Guest accounts / Sessions), raise the plan's Max devices, or use another credential |
| A device's reconnect seems to "use up" a slot | It doesn't — same credential + same device reuses its slot | Confirm the extra slot is a *different* device (MAC); check active devices on the account/voucher Details |
| Guest login says "Invalid username or password" for a known-good account | Disabled, outside valid-from/until, or locked after failed attempts | Check enabled + validity; reset the password (clears the lockout); short-wait if throttled |
| Guest login says "Too many attempts" | Brute-force throttle tripped (username+IP/device or endpoint-wide) | Wait ~1 minute and retry |
| Lost a guest password | Passwords are shown once and never stored in plaintext | Set a new password (Guest accounts → Password); it's shown once again |
| Can't change a voucher's plan | Voucher is revoked/expired/exhausted, or has a live session | Only unused/idle vouchers can be repointed; disconnect the session first |
| Guest keeps re-using an "expired" voucher for more time | Voucher duration is a **validity window** from first use; it doesn't reset | Expected: once the window closes the voucher shows **Expired** and re-login is refused; a reconnect only gets the remaining window. Issue a new voucher for more access |
| A voucher expired "too fast" with two devices | The window is wall-clock from first activation, **not** per-device | By design — a second device shares the same window, it doesn't add time. Give each guest their own voucher, or use a longer plan |
| Guests denied, state Unlicensed | No valid license (fail-closed) | Activate / import license (§7, §34) |
| Can't create plans/vouchers/accounts | License not Active | Renew/activate |
| Portal doesn't auto-pop | DHCP option 114 / walled garden | Verify network settings and walled garden (§13, §17) |
| Locked out after a WAN/LAN change | Confirmation window elapsed | It auto-rolled-back; reconnect to the old IP (§5) |
| "Cloud validation stale" warning | Central unreachable | Informational only; guests keep working (§22) |
| WAN-MAC mismatch warning | WAN NIC changed | Rebind MAC (§23) |

---

## 31. Go-live checklist

- [ ] Appliance registered and **Activated** (Central shows Active; Hotel Admin →
      License shows **Active**, correct **Max online guests** and **Valid until**).
- [ ] WAN/LAN correct and confirmed (no pending rollback).
- [ ] Guest network(s) / VLAN(s) created, applied and confirmed.
- [ ] DHCP pools, DNS, NAT and client isolation set per network.
- [ ] Captive portal reachable at `http://{gateway}:8380`; option 114 popping the
      portal.
- [ ] Auth method(s) configured and tested; integrations healthy.
- [ ] Walled garden covers portal/payment/OAuth hosts.
- [ ] Access plans + voucher batches ready (if using vouchers).
- [ ] **Real guest acceptance test passed** on each VLAN (§18).
- [ ] Branding correct per network.
- [ ] Operators created with least-privilege roles.

---

## 32. Day-2 operations checklist

| Task | Where |
|---|---|
| Watch fleet health & alerts | Central → Dashboard / Fleet / Security alerts |
| Renew a license before expiry | Central → Licenses (§20) |
| Raise/lower concurrent capacity | Central → re-issue license (§10, §20) |
| Add/replace an appliance | Central → Replace (§23) |
| Rebind after a NIC swap | Central → Rebind MAC (§23) |
| Rotate a cert | Hotel Admin → TLS certificate (§27) |
| Issue voucher batches | Hotel Admin → Voucher batches (§15) |
| Add a guest VLAN | Hotel Admin → Guest networks (§11) |
| Check backups | Central → Backup health (§28) |
| Review audit log | Central / Hotel Admin → Audit (§29) |
| Retire hardware | Central → Decommission/Delete (§25, §26) |

---

## 33. Terminology (canonical operator terms)

Use these operator-facing terms consistently. Internal/technical names in the
right column are kept in code/URLs where changing them would create migration
risk, but should not be shown to operators as the primary term.

| Concept | Canonical operator term | Internal / technical name(s) |
|---|---|---|
| Paying organization | **Customer** | tenant, `tenant_id`, `/tenants` |
| Physical property | **Site** | site |
| On-site gateway | **Appliance** | appliance |
| Bring an appliance online (normal) | **Activate / Activation** (zero-touch) | onboarding, claim, register |
| Manual/offline install method | **Enrollment token (advanced)** | bootstrap token |
| The entitlement | **Signed appliance license** | license (NOT plan/subscription) |
| License states | **Active / GracePeriod / Expired / Suspended / Revoked / Unlicensed** | `Restricted` = legacy pre-v3 only |
| Capacity | **Max concurrent online guests** (appliance-wide) | `max_concurrent_online_guests` |
| Per-voucher device limit | **Max devices** (on an access plan) | plan max devices |
| Limit-exceeded error | **License limit reached** | `limit_exceeded` |
| Live guest count | **Online guests / Active sessions** | `current_online_guests` |
| Appliance↔Central link | **Connected / Disconnected** | reachable, cloud_stale |
| Guest pricing/policy product | **Access plan** | guest access plan |
| Pre-login allowlist | **Walled garden** | walled garden |
| Portal | **Captive portal / Landing page** | portald |

**Retired terms — do not present as current:** Plan, Subscription, Commercial
plan, Trial, plan limits. The signed appliance license is the only entitlement.

---

## 34. Advanced / exceptional: enrollment tokens & offline activation

These are **not** the normal install path. Use only when zero-touch cannot apply.

**Enrollment token** — for pre-registering a box, or when
`SCD_AUTO_REGISTER=false`, or an installer must type a code:

1. Central → **Appliances** → **Enrollment token** → optionally lock it to the
   box's **Serial** → **Mint token** (shown once — copy it).
2. On the box, Hotel Admin → **Setup / Activation** → paste the **enrollment
   code** + confirm the **Serial** → **Connect**.
3. Continue with **Activation** in Central exactly as in §7.

**Offline activation** — for sites with no cloud path:

1. Hotel Admin → **License** → copy the **StayConnect Serial Number** and **WAN
   MAC** and send them to StayConnect.
2. StayConnect returns a signed `.license` file bound to that exact serial + WAN
   MAC.
3. Hotel Admin → **License** → **Offline activation** → **Upload license file**.
   The box verifies the file is bound to this exact hardware before accepting it.

Both paths converge to the same **Active** state and the same license model as
zero-touch; only the bootstrap differs.
