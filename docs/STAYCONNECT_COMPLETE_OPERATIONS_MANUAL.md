# StayConnect Enterprise — Complete Operations Manual

> **Audience:** an IT engineer who is new to StayConnect and has no knowledge of
> how the product was built. If you read this document top to bottom you can take a
> site from an unpacked appliance to live, licensed client Wi-Fi, and run it day-2.
>
> **This is the recommended starting point.** For screen-by-screen reference and
> shorter step lists, see the companion documents linked in
> [docs/user-guide/README.md](user-guide/README.md).
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
6. [Automatic registration → Waiting for activation](#6-automatic-registration--waiting-for-activation)
7. [Activation: customer, site, license terms, one step](#7-activation-customer-site-license-terms-one-step)
8. [Convergence: how the appliance becomes Active](#8-convergence-how-the-appliance-becomes-active)
9. [The license model (states & enforcement)](#9-the-license-model-states--enforcement)
10. [Concurrent online client capacity](#10-concurrent-online-client-capacity)
11. [Client networks / VLANs](#11-client-networks--vlans)
12. [Worked examples: VLAN 100 & VLAN 200](#12-worked-examples-vlan-100--vlan-200)
13. [DHCP, DNS, NAT & the captive portal](#13-dhcp-dns-nat--the-captive-portal)
13a. [Client Portal settings](#13a-client-portal-settings)
14. [Client authentication methods](#14-client-authentication-methods)
14a. [Client sign-in protection (configurable)](#14a-client-sign-in-protection-configurable)
15. [Access plans & vouchers](#15-access-plans--vouchers)
16. [Integrations: PMS, OTP, Social, Payments](#16-integrations-pms-otp-social-payments)
17. [Walled garden](#17-walled-garden)
18. [Client zero-to-internet acceptance test](#18-client-zero-to-internet-acceptance-test)
19. [License lifecycle behavior (Active → Grace → Expired → Suspended → Revoked)](#19-license-lifecycle-behavior)
20. [License renewal & anti-replay](#20-license-renewal--anti-replay)
21. [Reboot & service recovery](#21-reboot--service-recovery)
22. [Central outage behavior (offline operation)](#22-central-outage-behavior-offline-operation)
23. [Appliance replacement & WAN-MAC rebind](#23-appliance-replacement--wan-mac-rebind)
24. [Factory reset](#24-factory-reset)
25. [Move / Retire / Delete](#25-move--retire--delete)
26. [Safe deletion & dependency order](#26-safe-deletion--dependency-order)
27. [Security alerts & certificate monitoring](#27-security-alerts--certificate-monitoring)
28. [Backup & recovery](#28-backup--recovery)
29. [Audit & monitoring](#29-audit--monitoring)
30. [Troubleshooting](#30-troubleshooting)
31. [Go-live checklist](#31-go-live-checklist)
32. [Day-2 operations checklist](#32-day-2-operations-checklist)
33. [Terminology (canonical operator terms)](#33-terminology-canonical-operator-terms)
34. [Exceptional: offline activation](#34-exceptional-offline-activation)

---

## 1. System architecture at a glance

StayConnect has two tiers:

- **Central Control Plane** ("Cloud Admin", **Control Panel**) — a multi-tenant
  web app (`cloud-admin`, Next.js) backed by a Go API (`ctrlapi`/control-plane).
  You manage every site's customers, sites, appliance activation and
  **licenses** here. Central is **licensing, activation and fleet status only**
  — the binding description is [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md).
- **Appliance** (the on-site gateway) — a hardened box running Go daemons and an
  on-box web app called **Admin Console** (formerly Hotel Admin; `hotel-admin`, Next.js). The daemons:
  - `scd` — supervisor / client authorization / license state.
  - `edged` — the Edge API behind the Admin Console (client networks, DHCP, integrations,
    and the appliance's Central status).
  - `netd` — applies WAN/LAN/VLAN/nftables changes with an auto-rollback watchdog.
  - `portald` — serves the captive portal (`:8380` HTTP, `:8343` HTTPS).
  - `acctd` — traffic accounting & shaping.
  - Plus `caddy` (TLS reverse proxy for the Admin Console), `kea` (DHCP), `unbound` (DNS).

The appliance **does not broadcast WiFi**. Your wireless controller / APs
broadcast SSIDs and tag them onto VLANs; the appliance is the gateway, DHCP
server, captive portal, firewall and license enforcer for those VLANs.

The trust between the two tiers is **mutual TLS (mTLS)** plus a **signed license**
and **signed assignment**. The appliance holds an Ed25519 identity key it
generates itself on first boot; Central never learns the private key.

---

## 2. Central vs Appliance — who does what

| Responsibility | Central (Control Panel) | Appliance (Admin Console) |
|---|---|---|
| Create customers, sites | ✅ | — |
| Activate an appliance, issue/renew/revoke its **license** | ✅ | — (shows state; can import an offline license file) |
| Fleet status (activation, connection, licence), security alerts, certificates, audit | ✅ | Local health/audit only |
| Client networks / VLANs, DHCP, DNS, NAT | — | ✅ |
| Captive portal branding, auth methods, walled garden | — | ✅ |
| Access plans, voucher batches, sessions | — (issues license terms) | ✅ |
| PMS / OTP / Social / Payments integrations | — | ✅ |
| WAN / LAN IP configuration | — | ✅ |
| Enforce concurrent-client capacity & license state | — | ✅ (locally, offline-safe) |

**Rule of thumb:** Central decides *whether and how much* a site may serve (the
license). The appliance decides *how the site network actually works* and
enforces the license locally, even if Central is unreachable.

### Ownership hierarchy (how everything is organized)

```
Platform  →  Customer  →  Site  →  Appliance  →  Client Networks / VLANs
```

- **Customer** = the organisation that owns the sites: a hotel group, company or other owner (a tenant). A Customer owns
  one or more Sites.
- **Site** = **one physical location** (a single hotel, resort, office, campus or other
  deployment location). A Site belongs to exactly one Customer and contains one or more
  Appliances. Buildings, floors, wings, SSIDs and client VLANs are **not** Sites —
  they are configured on the appliance.
- **Appliance** = the on-site gateway. It belongs to exactly one Site at a time
  and can **move** only between Sites of the same Customer. Giving it to another
  Customer is not a move: **retire → factory-reset → it registers again →
  activate** for the new Customer (§25).

These rules are enforced in the UI, the API, **and** the database (a composite
foreign key makes an appliance-under-another-customer's-site impossible).

The Central console has no customer selector: its pages are **Overview ·
Customers · Appliances · Licenses · System**, and you reach a customer's sites,
appliances, licences and users by opening that customer. A customer's own users
see only their customer (enforced server-side). See the
[Control Panel reference](user-guide/control-panel-reference.md).

---

## 3. The one official onboarding workflow

This is the **only** normal way to bring a site online. Use it every time.

1. **Install the appliance** at the site and cable WAN + LAN.
2. **Configure WAN** only if the site does not provide DHCP on the WAN uplink
   (many do — then this step is automatic).
3. The appliance **automatically appears in Central** under **Appliances** as
   **Waiting for activation** (no token, no manual steps).
4. Open it and click **Activate**; **create or select the Customer**.
5. **Create or select the Site**.
6. Set **Clients online at once** (the licensed capacity).
7. Set **License Validity** (a number of days, or an end date).
8. Set **Grace Period** (days the site keeps serving after expiry).
9. Confirm — one step signs the assignment and issues the signed license.
10. **Wait for automatic convergence** — the appliance collects its assignment,
    certificate and license itself; Central shows **Activating**, then
    **Activated**.
11. **Verify** in Central (Appliances → **Activated**, license **Active**) and on
    the box (Admin Console → **Appliance & licence**: *Activated*, licence
    *Active*).
12. On the appliance, **configure Client networks / VLANs**.
13. **Configure authentication methods** (voucher / OTP / PMS / social / payment).
14. **Perform a real client acceptance test** (connect a device, reach the portal,
    authenticate, reach the internet).
15. **Go live.**

> There are no enrollment tokens. A factory-clean box with internet registers
> itself; a site with no internet uses offline activation (§34).

---

## 4. Physical install & first boot

- Rack/mount the appliance. Connect the **WAN** NIC to the site uplink and the
  **LAN/trunk** NIC to the switch that carries your client VLANs.
- Reference topology used throughout this manual:
  - **WAN interface: `ens160`** (uplink to the internet / site network).
  - **LAN trunk interface: `ens192`** (802.1Q trunk to your switch; client VLANs
    ride on it).
- Power on. On first boot with no stored identity, `scd` generates an Ed25519
  identity keypair, detects hardware (serial, WAN MAC, hardware fingerprint), and
  — if it has internet — self-registers with Central (see §6). No operator action
  on the box is required for a normal install.
- If the box has **no internet yet**, it simply waits; it will register as soon as
  WAN connectivity is established.

---

## 5. WAN / LAN connectivity

WAN/LAN is configured **on the appliance** in the Admin Console → **WAN / LAN settings**
(`/network/system`). You normally only touch this if the WAN is static or the base
bridge gateway must change.

> This page covers only the **WAN uplink** and the appliance's **legacy base
> bridge** (`br-lan`). It is **not** where client Wi-Fi lives — each client network
> has its own VLAN, bridge, gateway, DHCP pool and captive portal (see §11).
> Example: `CHR` → VLAN 90 → `ens192.90` → bridge `br-g90` → gateway `10.20.0.1/22`
> → DHCP pool `10.20.0.100–10.20.3.250`. The legacy base bridge is shown under an
> **Advanced · Base LAN / Legacy Bridge** section with a *Legacy* badge; its DHCP
> showing **off** there is normal when clients are served by client networks.

1. Review the **WAN / Management** status card and the **Client networks** pointer.
2. Under **Change configuration**:
   - **WAN:** IP address, prefix length, default gateway, DNS (comma-separated).
   - **Base LAN:** base-bridge gateway IP, prefix length (client DHCP pools are
     managed per client network on the DHCP page, not here).
3. Click **Validate & preview** — check the before/after and the new management URL.
4. Click **Apply change** and **re-enter your password**.
5. A **countdown banner** appears. If the management IP changed, reconnect to the
   new URL, then click **Keep this configuration**.
6. **If you do not confirm within the window (default 120 seconds), the change
   auto-rolls-back.** A wrong IP can never lock you out.

The same apply-and-confirm safety wraps client-network changes (§11).

---

## 6. Automatic registration → Waiting for activation

- A factory-clean appliance auto-registers by POSTing a **self-signed** request
  (proving possession of its identity key) to Central's public, token-less
  `POST /v1/appliances/register`. This is trust-on-first-use. Until Central
  answers it retries in the background (30 s, backing off to every 5 minutes),
  and **Check now** in the Admin Console retries immediately.
- Central creates the appliance record in state **`pending_approval`**
  (internal `lifecycle_state`), which Central shows as activation
  **"Waiting for activation"**. Admin Console shows the same word; before the
  first successful registration it shows *Not registered yet*.
- **Clone / hardware-reuse protection:** if a known identity key appears from
  different hardware, or a serial reappears with a new key while an active record
  exists, Central refuses (HTTP 403) and raises a **security alert**. The correct
  remedy is to **retire** the old appliance first (§25).

You do **not** approve a raw registration by itself — you **activate** it (§7),
which both approves and licenses the box in one step.

---

## 7. Activation: customer, site, license terms, one step

In Central go to **Appliances**, open the appliance **Waiting for activation**,
and click **Activate**. The dialog asks for exactly what the license needs:

- **Customer** — select, or **New customer…**.
- **Site** — select, or create one (a site belongs to a customer).
- **Clients online at once** — the licensed capacity (`0` = unlimited; use it
  only where intended).
- **Valid for** — a number of days, or an end date.
- **Grace Period (days)** — how long the site keeps serving clients after the
  license expires.

Confirming performs, server-side in one transaction (platform admin + password
step-up):

1. **Assign** it to the customer/site and issue a **vendor-signed assignment**.
2. **Issue a hardware-bound signed license** with the terms you entered.

The **certificate** is issued automatically when the appliance's CSR arrives.
There is **no plan and no subscription** step. The signed appliance license *is*
the entitlement.

The stored `lifecycle_state` is identity only: `pending_approval → assigned`
(later `revoked` or `decommissioned`). Central derives what operators see —
activation (*Waiting → Activating → Activated → Retiring → Retired*),
connection and license state — on every read; license state is never stored
on the appliance record ([CENTRAL_CONTROL_PLANE.md §3](CENTRAL_CONTROL_PLANE.md#3-appliance-states-single-source-of-truth-computed-by-ctrlapi)).

---

## 8. Convergence: how the appliance becomes Active

Central does **not** push the license. The appliance **pulls** everything itself
over its authenticated channel:

1. It fetches its **signed assignment** (every 30 seconds) and verifies it.
2. It submits a CSR and fetches its issued **certificate**.
3. It fetches its **signed license** from Central (`GET /v1/appliance/license`)
   — every minute until it holds a usable license, then every 6 hours, and at
   once on **Check now**.
4. Once the valid license is installed, Central shows **Activated** and Admin
   Console → **Appliance & licence** shows *Activated* and licence *Active*.

Convergence is normally seconds-to-a-minute. If Central is briefly unreachable
during this window, the box keeps retrying; fetch failures are non-fatal.

**Verify:** Central shows the appliance **Activated** with license **Active**;
Admin Console → **Appliance & licence** shows the licence **Active**, **Clients
online, all client networks** against the licensed maximum, and **Valid until**.

---

## 9. The license model (states & enforcement)

The license is a **signed document bound to one appliance** (identity key
fingerprint + appliance id + serial + hardware fingerprint + WAN MAC). It carries:

- **max concurrent online clients** (the capacity),
- **validity window** (valid-from / valid-until),
- **grace period** (days),
- **features** (PMS, paid WiFi, SMS/email OTP, social login, HA, white-label) —
  real license fields; every license Central issues today carries all of them,
  so the only commercial controls are the binding, the capacity and the
  validity window.

The appliance evaluates state locally against the signed document and the local
clock, every minute and at boot, and re-checks the hardware binding on every
evaluation. States and what they do to **new** client logins:

| State | New client logins | Existing sessions | DHCP/DNS/portal/Admin Console |
|---|---|---|---|
| **Active** | Allowed, up to capacity | Keep running | Up |
| **GracePeriod** (expired but within grace) | **Still allowed**, with renewal warning | Keep running | Up |
| **Expired** (past valid-until + grace) | **Refused** (`license_expired`, 403) | Keep running to natural end | Up |
| **Suspended** (billing hold) | **Refused**; provisioning off | Keep running | Up |
| **Revoked** (explicit revocation) | **Refused** immediately | Keep running | Up |
| **Unlicensed / "Waiting for activation"** (no valid license) | **Refused** (fail-closed, capacity 0) | — | Up |
| **Restricted** *(legacy pre-v3 licenses only)* | Allowed, but provisioning/features off | Keep running | Up |

Key guarantees:

- **A state change never drops existing client sessions** — only *new*
  authorization is refused.
- **DHCP, DNS, captive portal and the Admin Console always keep running**, regardless of
  license state, so staff never lose control of the box.
- **Production appliances fail closed:** with no valid license the state is
  `unlicensed` and capacity is 0. (Only development builds run permissively.)

---

## 10. Concurrent online client capacity

- The cap is **per-appliance, appliance-wide across ALL its client VLANs** — it is
  **not** per-VLAN and **not** per-tenant. Two clients on VLAN 100 and three on
  VLAN 200 count as five against one capacity number.
- It is enforced **locally** on the appliance inside the same transaction that
  creates a client session (a per-appliance lock + a live count of active
  sessions). Central is never consulted, so enforcement works offline.
- **Every Client Access method shares it**: voucher, client account, OTP, social
  login and **PMS room sign-in**. A room guest's first device, a second device
  joining the same stay, and a device rejoining after its session ended each
  take a slot; a device signing in again while its session is still open keeps
  that session and takes none. A refused room sign-in is recorded on **Hotel →
  Guest sign-in attempts** as *Licensed capacity full* (`LICENSE_CAPACITY_REACHED`);
  one the licence did not admit at all, as *Licence refused new clients*
  (`LICENSE_REFUSED`).
- `0` means **unlimited** (older licenses may carry `-1`, also unlimited).
- **At capacity**, a new client login is refused with HTTP 403
  `{"error":"LICENSE_CAPACITY_REACHED","limit":N,"current":M}` and **nothing is
  provisioned** for that client (no firewall/shaping/accounting/session rows). A
  slot frees when an existing session ends.

To raise capacity, **renew/re-issue the license** in Central with a higher
Max Concurrent Online Clients (§20).

---

<a id="11-guest-networks--vlans"></a>
## 11. Client networks / VLANs

Create client networks in the Admin Console → **Networking → Client networks** (`/network`) →
**New client network**. The 7-step wizard:

1. **Identity** — Name, Description, **SSID label** (a label only — the appliance
   does not broadcast; your controller maps the SSID onto the VLAN).
2. **Interface / VLAN** — pick the parent interface (a port whose role is
   *Client access* or *Client trunk*, or an unused one).
   Tick **VLAN tagged (802.1Q)** and set the **VLAN id** (1–4094) for a tagged
   network.
3. **Subnet & gateway** — Subnet CIDR and Gateway IP. The appliance owns the
   gateway; clients use it as gateway **and** DNS.
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
> A client network **cannot be deleted while it is enabled or has active
> sessions** — disable it / let sessions drain first.

**DHCP reservations:** pin a device MAC to a fixed IP on **DHCP & leases**
(`/network/dhcp` → Reservations → New) or from the network's detail page.

---

## 12. Worked examples: VLAN 100 & VLAN 200

Reference hardware: **WAN = `ens160`**, **client trunk = `ens192`**.

### Example A — two VLANs, two different portals/experiences

| | VLAN 100 (Clients) | VLAN 200 (Conference) |
|---|---|---|
| Subnet | `10.100.0.0/24` | `10.200.0.0/24` |
| Gateway (appliance) | `10.100.0.1` | `10.200.0.1` |
| DHCP pool | `10.100.0.50–10.100.0.250` | `10.200.0.50–10.200.0.250` |
| Portal | Portal A branding | Portal B branding |
| Auth | Voucher + room (PMS) | Voucher only |
| Portal URL | `http://10.100.0.1:8380` | `http://10.200.0.1:8380` |

Steps: create two client networks on parent **`ens192`**, VLAN tagged, ids **100**
and **200**, with the subnets/gateways above; give each its own DHCP pool; enable
captive portal + NAT on both; apply + confirm each. On your wireless controller,
map SSID "Client Wi-Fi" → VLAN 100 and SSID "Conference" → VLAN 200. Set branding
per network in Portal settings.

### Example B — two VLANs sharing one portal/experience

Create VLAN 100 and VLAN 200 exactly as above but give them the **same** portal
settings and the **same** enabled auth methods. Clients on either VLAN see an
identical login page. This is common when you segment traffic (e.g. floors or
buildings) for routing/DHCP reasons but want one client experience.

### Capacity across VLANs

In **both** examples the **licensed Max Concurrent Online Clients is a single
appliance-wide number**. If the license allows 300 concurrent clients, that 300 is
shared across VLAN 100 **and** VLAN 200 (and any legacy `br-lan`), not 300 each.

---

## 13. DHCP, DNS, NAT & the captive portal

- **DHCP** is served by `kea` per client network from the pools you defined.
  Reservations pin a MAC to an IP.
- **DHCP option 114** advertises the captive-portal URL
  (`http://{gateway}:8380`) so modern OSes auto-pop the portal (RFC 8910). This is
  the only reliable cross-OS auto-pop mechanism.
- **DNS** is served by `unbound`, either as the appliance resolver or forwarding
  to custom servers per network.
- **NAT (masquerade)** is per-network — enable it so clients reach the internet via
  the WAN. **Client isolation** (per-network) stops clients talking to each other.
- The **captive portal** (`portald`) listens on `:8380` (HTTP) and `:8343`
  (HTTPS) and is reached at the network's gateway IP.

### 13a. Client Portal settings

**Admin Console → Client Portal → Portal settings.** (Client Portal was formerly Guest Portal.) How the sign-in page looks and
reads. There is one portal and one current configuration for it: change
something, watch the preview, press **Save changes**, and clients see it. There is
no draft to promote, no version to pick and no publish step.

| Section | What it holds |
|---|---|
| **General** | Site name, welcome line, help line, terms-of-use link. |
| **Branding** | Logo, background photograph, brand colour, button shade, text colour, corner radius, typeface. |
| **Languages** | Which languages clients are offered, and the wording in each. |
| **Advanced** | Custom CSS and a custom HTML fragment. |

**Images live on the appliance.** Upload a PNG, JPEG, WebP or GIF up to 8 MB; it
is stored locally and served from the same origin as the sign-in page, which is
what makes it load for a client who has no internet yet. The file type is decided
by inspecting the bytes, not the filename. **SVG is refused** — it is a document
format that can carry script, and this page collects room numbers and voucher
codes.

**The preview is the real page.** It is the portal's own sign-in page rendered
with the settings you are editing, in a sandboxed frame at real Desktop and
Mobile widths — not a drawing of it that could drift.

#### Password confirmation: what needs it, and why

*Approved by the Product Owner, 2026-09-18.*

| Change | Confirmation |
|---|---|
| Site name, welcome/help text, terms link, logo, background, colours, typeface, corner radius, languages, wording | **None.** Save and it is live. |
| **Custom CSS or custom HTML**, including clearing them | **Password step-up required.** |

The reason is the boundary, not the screen. Those two fields are the only ones
that can put executable-shaped content on a page where clients type their surname
and voucher codes; every other field is constrained on the appliance to a colour,
a length, a font stack, a bounded string, or an appliance path / https URL /
inline image, so a save that leaves the Advanced fields untouched cannot
introduce executable content at all. Requiring a password to correct a typo in
the site's name was ceremony that taught operators to type it without reading.

What is unchanged: the operator still needs the portal-branding write
permission, every save is audited with who made it, and a design containing a
script tag, an inline event handler, a frame, an extra form or `@import` is
**refused outright** rather than quietly cleaned up.

#### Languages, and how a client gets theirs

Six languages ship complete with the product — **English, Arabic, Italian,
French, Russian, German** — so offering one costs a tick, not an afternoon of
translation. Two separate things, deliberately:

- **Client languages** — which of them appear in the portal's selector. English is
  always available and is what anything missing falls back to.
- **Wording** — one language at a time, every field already showing the real text
  a client reads. Type over a string to change it for your site; press its
  reset button, or type the original back, and the customisation is removed
  rather than stored. **Only strings you actually changed are kept**, so improved
  wording reaches your clients without you re-entering anything.

A language you add yourself is not shipped with the product, so you supply its
wording; anything left empty shows English, and the screen says how many strings
that is.

**Arabic is laid out right to left** on the portal and in the editor.

**A client's language is chosen automatically**, in this order:

1. what that client chose on this device before, if they chose;
2. otherwise their device's own ordered language list, first entry the site
   offers — `ar-EG` matches Arabic, `de-AT` matches German, and a phone set to
   Japanese then Italian gets Italian at a site offering Italian;
3. otherwise English.

A client's own choice always outranks detection and survives being bounced back
to the portal. Detection never records a preference, so it cannot be mistaken
for a choice later. If you switch a language off after a client chose it,
detection simply runs again for them.

---

## 14. Client authentication methods

Clients can authenticate by:

- **Voucher** — a printed/emailed code (needs Access plans + Voucher batches, §15).
- **Username & Password (Client Accounts)** — a named account with a password,
  bound to an Access plan (§15). Basic-access like vouchers (no license feature
  gate). Managed on the **Admin Console → Client accounts**; the portal tab is shown only
  when you enable it there. Passwords are stored hashed (argon2id) and are
  write-only.
- **OTP** — email or SMS one-time code (needs a Notifications provider, §16).
- **PMS (Room sign-in)** — room number plus exactly one detail from the
  reservation, checked against the appliance's copy of the PMS guest list (needs
  a PMS provider, §16). What the guest types is chosen in **Admin Console →
  Hotel → Room sign-in**: *any one of first name, surname or reservation number*
  (recommended — one box, compared against all three; an ambiguous match in the
  room is refused), *surname*, *first name* or *reservation number*. The on/off
  switch stays in **Client Portal → Sign-in methods**, which links to it. Subject to the same licence gate and concurrent-client
  capacity as every other method (§10).
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

## 14a. Client sign-in protection (configurable)

Repeated incorrect room sign-ins from the **same device** cause that device to be asked to wait before it can
try again. It is always on — there is no switch — and the three numbers that decide how strict it is are
editable in the **Admin Console → Client Portal → Sign-in methods → Client sign-in protection**.

| Setting | Unit | Default | Allowed | What it does |
|---|---|---|---|---|
| Maximum failed attempts | attempts | **5** | 3–20 | How many incorrect sign-ins from one device are allowed before it has to wait. |
| Observation window | seconds | **60** | 30–3600 | Attempts older than this stop counting. The window moves continuously (rolling), so it cannot be sidestepped by waiting for a clock boundary. |
| Wait after too many attempts | seconds | **60** | 30–3600 | How long the device is asked to wait. Attempts made during the wait do not extend it. |

**Scope is the device, on its site and client network.** Not the room — restricting a room number would lock
out the client who actually lives there while whoever chose that number simply moves to the next one. Not the
address — a client network NATs, so an address is a floor. The hardware address used is the one the appliance
reads from its own neighbour table, never a value the browser sends, so refreshing the page, reopening the
portal, clearing cookies or typing a different room does not reset anything.

*Honest limit:* a MAC address is not unspoofable. Someone on the client VLAN who changes their device address
gets a fresh counter. What the control buys is that casual enumeration stops being free, that no client is ever
restricted by another client's behaviour, and that every restriction is attributable and releasable by a named
member of staff.

**What counts, and what does not.** Only an incorrect credential counts:

* `CREDENTIAL_MISMATCH` — the room exists and has an eligible stay, but the value entered matched nothing.
* `ROOM_NOT_IN_MIRROR` — no stay on any mapped interface carries that room number.

Nothing else does. A stale or unreachable PMS mirror, a routing or interface failure, an internal error, a
malformed submission, an ambiguous room, a stay outside its eligibility window and an attempt already refused
for waiting all leave the counter untouched — a site whose PMS feed is down must not lock out its own
clients on top of it. A **successful** sign-in clears that device's counter immediately.

**What the client sees.** *"Too many attempts. Please wait N seconds and try again."* — counting down from the
server's own expiry. A browser that ignores the countdown gains nothing: the appliance refuses the next
submission itself.

**Existing sessions are never disconnected.** A client already online stays online.

**Ending a wait early.** **Admin Console → Hotel → Guest sign-in attempts → Active restrictions** lists every device
currently waiting, with its client network, the last room it typed (shown as *unverified input* — it is what
somebody typed, not where anyone is staying), the failure count, when the wait started and ends, a link to
that device's sign-in attempts, and a **Release** action.

Release needs the *Release client sign-in restriction* permission and a short reason; who released it, which
device, when and why are recorded. **Releasing allows another attempt — it does not sign anybody in.** The
client still has to enter details the site accepts.

**Who can do what** (see [ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md) §3):

| Role | Change the settings | Release a restriction |
|---|---|---|
| Site admin | yes | yes |
| Site IT manager | yes | yes |
| Client services operator | no (read-only) | yes |
| Client relations operator | no (read-only) | yes |
| Site viewer | no | no |

The desk releases and does not re-tune, deliberately: turning "five" into "twenty" for the whole site
must not be the quickest way to help one person.

**Changes take effect immediately**, on the next sign-in attempt — no restart, rebuild or deployment. They
apply to what happens **next**: a device already waiting keeps the time it was given, and shortening the
setting does not end a wait already running. Use **Release** for that. Every settings change records the
operator, the previous values and the new values.

---

### PMS configuration: Current configuration and History

**Admin Console → Hotel → PMS connection.**

The connection screen shows the **current configuration** only — the version in force, what it is set to, and
when and by whom it was saved. Previous versions are behind a **History** button, and putting an older one
back in use ("roll back") is done from there.

Every saved version is kept permanently and cannot be edited or removed. History states what changed from the
version before it, derived from the stored values. Where two versions hold identical connection settings,
History says so and names the internal difference instead of implying a change that did not happen. Where a
version's origin was never recorded, History says that too rather than guessing.

The word *Revision* is internal. Operators see **Version**, **Current configuration**, **Previous** and
**History**; the database and the API are unchanged.

**Connection recovery settings apply to this connection only.** They are stored per PMS interface, so a
site running two connections tunes each on its own terms -- a link behind a flaky VPN can be given
patient backoff without slowing down a healthy one. Changing one connection's values leaves every other
connection at the site exactly as it was, and each keeps its own change history.

Where a version's origin cannot be read at all -- as opposed to never having been recorded -- History says
so explicitly and names it as a fault to report. The configuration itself is still shown: losing the audit
trail never costs an operator the ability to see what the connection is set to.

## 14b. Unresolved departures (PMS reconciliation)

**Admin Console → Hotel → PMS connection → Advanced diagnostics → Unresolved departures.**

> **Not a routine screen, and deliberately not in the menu.** Reconciliation runs by itself after every
> complete guest list and this page carries no action. A site where the integration is healthy never
> needs to open it. When something genuinely needs investigating, the PMS connection page says so — under
> *Needs investigation* — and links straight here. Roster reconciliation sits beside it, under the same
> Advanced diagnostics heading.

This PMS reports every checkout as a departure carrying a **room number and no reservation number**. When the
room does not identify exactly one in-house stay, the departure cannot be applied and waits for a human. Two
things then used to happen that made the backlog unusable: a waiting departure is **restaged on every
reconnect**, and the dashboard counted rows. One unresolved checkout became thousands of "messages needing
attention".

The screen now counts **decisions**, not rows. One departure is one case, with the number of recorded copies
shown beside it — that number is a fact about the feed and is not hidden.

**What a case can say, and what it needs:**

| State | Meaning | What would resolve it |
|---|---|---|
| **The PMS can settle this now** | One matching stay, it began before the departure was raised, and it is absent from the PMS's own latest complete in-house list. | A departure sent from the PMS for that room will apply cleanly. |
| **Nothing outstanding** | Nobody is in that room now. | Nothing is waiting. This is **not** proof the departure was applied. |
| **Room has more than one stay** | Sharing a room is ordinary and legal. | The reservation number from the PMS. |
| **A later guest is in that room** | The current occupant arrived **after** this departure. Applying it would check out a resident guest. | The reservation number from the PMS. |
| **The PMS still lists them as in house** | Fresh authoritative evidence contradicts the older departure. | The PMS resolving its own disagreement. |
| **Needs evidence from the PMS** | Usually: no complete in-house list has been received to compare against. | A completed full synchronisation. |

**There is no action on this screen, and that is the answer rather than a gap.** A departure the appliance
could not place is resolved by the PMS sending one it *can* place — typically once somebody corrects the
record there. Two deliberate invariants make that the only route: a PMS event is one-way (once it reaches a
terminal state its result is frozen), and a checkout boundary must be an *applied* departure event. The PMS
is the source of truth for whether a client has left, and the appliance asserting it from a re-reading of an
old message would be claiming to know something it does not.

The list is the value: 397 distinct departures instead of 12,271 rows, each labelled with the evidence it is
waiting for, so the desk knows which rooms to ask the PMS about.

**Three rules this screen will not break**, because breaking any of them disconnects a resident client:

* a **planned departure date is not a checkout** — "Past their departure date" is its own tab and closes nothing;
* **two stays in one room are not a duplicate** — shared occupancy is ordinary;
* an **old room-only departure is never applied to today's occupant**.

The engine enforces the third one itself: a departure whose matching stay arrived later is refused as
`GO_STALE_ROOM_DEPARTURE` rather than applied.

---

## 14c. What the appliance sends to OneGate Central

**The appliance uses OneGate Central for its identity, activation and licence only** (CLAUDE.md §0E). There is
no reporting link to switch on: the appliance has no telemetry queue, no usage or health producer, no message
bus client, no remote command channel and no software-update agent. The Admin Console's **System → Appliance &
licence** shows activation, licence and whether Central is reachable; the site's operations are watched on
the appliance itself.

**Everything the appliance exchanges with Central** — all of it HTTPS to Central's API:

| Exchange | Purpose | What it carries | Cadence |
|---|---|---|---|
| `POST /v1/appliances/register` | establish this appliance's identity (token-less, signed with its identity key) | serial, public key, hardware inventory (MACs, hardware fingerprint, hostname, model) | until Central answers (30 s backing off to 5 min) and on **Check now**; never while *Removed from OneGate Central* |
| `POST /v1/appliance/csr` → `GET /certificate` | obtain and renew the client certificate | certificate signing request; the issued certificate | on activation and before expiry |
| `GET /v1/appliance/license` | licence retrieval, renewal, suspension, revocation | appliance id; the signed licence document | every minute until licensed, then every 6 hours, plus on **Check now** |
| `POST /v1/appliance/offline-reconcile` | tell Central an offline activation package was consumed | the package id | after an offline import, retried every 10 minutes until Central confirms |
| `GET /v1/appliance/hello` | **licence enforcement** — detects that Central deleted this appliance (§26) | signed appliance id only | at boot, then every 5 minutes |
| `GET /v1/appliance/assignment`, `/assignment-registry`, `POST /assignment/ack` | the signed customer/site binding the licence is scoped to; carries retirement | signed document: customer, site, state, version; the ack returns the version adopted | every 30 seconds (mutual TLS only); a terminal acknowledgement is retried until Central confirms |

**No client identity, stay, session, usage or log content appears in any of them.**

**Nothing local depends on Central.** The PMS connection, mirrored stays, client sign-in and its attempt
records, packages, allowances, sessions, accounting, enforcement and every Admin Console screen run on this
appliance. A client does not need Central to get online, and the offline licence and grace rules apply.

**History.** A telemetry link (usage, health and service-health reports, remote session revocation, remote PMS
operations, a command channel and an update agent over a message bus) was built and verified, switched off by
the Product-Owner decision of 2026-09-13, and removed from the appliance on 2026-09-27 (appliance migration
0093 dropped its settings, queue and ledgers with their rows). The former **Network → Cloud connection** page
redirects to **System → Appliance & licence**.

---

## 15. Access plans & vouchers

**Access plan** (Admin Console → **Client access plans** → **New plan**): Code, Name,
Description, **Duration (s)** (blank = unlimited time), **Data cap (bytes)** (blank
= unlimited), **Down/Up kbps**, **Max devices**, **Price (cents)**, **Currency**.

> "Max devices" on an access plan is a **per-credential device limit** — the
> concurrent devices allowed on one voucher or one client account — a different
> concept from the license's appliance-wide concurrent-client capacity (§10). Both
> are enforced on **every** login, atomically and concurrency-safe: a device
> rejected for either reason gets `MAX_DEVICES_REACHED` / `LICENSE_CAPACITY_REACHED`
> and no session/nft/shaping/accounting/voucher-activation is created. A reconnect
> from a device already signed in on the same credential does **not** consume a
> second slot; disconnect/expiry/reap frees the slot. Client Access Plans are the
> per-client tiers here — not the retired commercial *License Plans*.

**Voucher batch** (**Voucher batches** → **New batch**): choose an active **Plan**,
a **Count** (1–10000), a **Label**, and the **code generation options**:
- **Code length** 6–10 — the **random portion** only.
- **Character mode**: **Numbers** · **Uppercase letters** · **Uppercase letters
  and numbers** · **Uppercase/lowercase letters and numbers**. The form shows a
  live **example** and the exact **character set**.
- **Optional prefix** (A–Z/0–9, e.g. `PARTY`) — **additional** to the random
  portion.
- **Exclude ambiguous characters** (on by default): drops `0/O`, `1/I/L`, `5/S`.
  (`I, L, O, U` are *always* excluded so a code matches exactly what the client
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
> retried close can't double-charge. (Client **accounts** are reusable credentials
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

**Client accounts** (**Client accounts** → **New account**): **username** (1–64;
one letter/digit allowed; case-**insensitive**, unique per site), **password**
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

New plans/voucher batches/client accounts require the license to permit
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

Admin Console → **Walled garden** → **New rule**: Kind (domain/ip/cidr), Value,
Ports (comma; blank = all), Description. Add the hosts your portal, payment
callbacks and OAuth redirects need so clients can reach them **before**
authenticating. Keep the list small.

---

## 18. Client zero-to-internet acceptance test

Do this before go-live, on a real device, per client VLAN:

1. Join the client SSID (mapped to the VLAN on your controller).
2. Confirm the device gets a DHCP lease in the expected subnet and the gateway/DNS
   is the appliance gateway IP.
3. Confirm the **captive portal auto-pops** (or browse to any HTTP site and get
   redirected to `http://{gateway}:8380`).
4. Authenticate with a real method (voucher/OTP/PMS/social/payment).
5. Confirm the device **reaches the internet** afterwards.
6. Confirm the session appears in the Admin Console → **Sessions**.
7. Confirm the **concurrent count** increments (License page / dashboard).

If capacity is reached during testing you'll see `LICENSE_CAPACITY_REACHED` —
that's expected behavior, not a fault.

---

## 19. License lifecycle behavior

See the table in §9 for the enforcement matrix. Timeline of a normal license:

- **Active** from valid-from until valid-until.
- At **valid-until** it enters **GracePeriod** for `grace_period_days` — clients
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

- **Renew / change terms** in Central (the appliance's page → **License → Renew
  or change**). Every re-issue gets a **new, higher license version** and
  supersedes the prior one atomically. The appliance pulls the new envelope
  automatically (within 6 hours, or at once on **Check now**).
- **Anti-replay is enforced on the appliance and survives reboot** (cleared only
  by factory reset):
  - **Monotonic version** — a lower version, or the same version under a different
    license id/fingerprint, is rejected even with a valid signature and Central
    offline.
  - **issued-at may not go backwards.**
  - **Revoked ids can never be re-installed.**
  - **Clock-rollback protection** via a persisted high-water mark (48h tolerance).
  - Rejections surface as `LICENSE_ROLLBACK_REJECTED`.

This means you cannot "downgrade" a site by replaying an old license file, and a
renewal issued while the box is offline still applies cleanly when it reconnects.

---

## 21. Reboot & service recovery

- The appliance daemons are supervised. On crash/reboot they self-heal with an
  adaptive backoff; Admin Console → **Diagnostics** (`/health`) shows each service's
  health, restart counts, backoff and recovery history.
- License state and anti-replay high-water marks are persisted, so a reboot does
  not change licensing.
- You should not normally intervene. If needed: **Recheck** re-runs a health
  check, **Logs** shows recent sanitized logs, and **Restart** (reason + password)
  restarts a service.

---

## 22. Central outage behavior (offline operation)

- **Clients keep working.** Client authorization and the capacity gate evaluate
  entirely against the on-disk signed license and the local clock — nothing in the
  client path calls Central.
- License fetch/refresh failures are **non-fatal** ("offline-safe"); they only
  affect renewal freshness. Admin Console → **Appliance & licence** shows OneGate
  Central as *Temporarily unreachable* with the last answer and last problem — a
  warning, not a license state change.
- DHCP, DNS, portal and the Admin Console are local and unaffected.
- Time-based transitions (Active → Grace → Expired) still occur offline via the
  local ticker, honoring the grace window.
- For fully offline sites, an **offline activation file** can be imported on the
  box (§34).

---

## 23. Appliance replacement & WAN-MAC rebind

**Replace** (swap hardware, keep the site): Central → the old appliance →
**Advanced → Mark for replacement** (reason + step-up) opens a **72-hour
window**. The new box registers itself and is activated **for the same customer
and site**; the **old box keeps its license until the replacement is
activated**. Then the old one's license is revoked and it is retired through the
same **acknowledged two-phase retirement** as **Retire** (§25): a signed
terminal assignment is issued, its credentials stay valid until it collects the
assignment and acknowledges, and only then are they revoked. If it never
acknowledges (a dead box), the retirement shows as not confirmed and an
**emergency** retire finishes it. If the window elapses before the new box is
activated, a security alert asks for an operator decision. Use this for RMA /
hardware swaps.

**WAN-MAC rebind** (same box, WAN NIC changed): a WAN-MAC-only mismatch is
**soft** — the license stays in force, Admin Console shows *The internet (WAN)
network adapter has changed*, and a security alert is raised. Resolve it in
Central → appliance → **Advanced → Rebind WAN MAC** (reason + step-up), which
re-issues a corrected hardware-bound license with the same terms.

> A mismatch of **identity key / appliance id / serial / hardware fingerprint** is
> a **hard** reject — the license is refused and the box will not serve clients.
> That indicates the license and hardware genuinely don't match (wrong file, or a
> cloned box), not a simple NIC change.

---

## 24. Factory reset

- Factory reset is a **local appliance action**, not a Central action: the
  factory-clean install ([DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md](DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md)).
  It wipes the box's identity, license, data and config.
- Central authority is **not** deleted by a factory reset. If you are permanently
  retiring the box, **Retire** it in Central (§25) so its bound license is
  revoked.
- After a factory reset the box has a new identity key. It registers again as
  **Waiting for activation** if its previous appliance is waiting or retired —
  on that same Central record (same serial), the old key being recorded as
  retired; while that previous appliance is still activated, registration is
  refused with a *hardware already in use* security alert until you retire it.
  A retired identity key itself can never register again (`403 identity_retired`).
- Factory reset is also the **only** way back for an appliance that shows
  *Removed from OneGate Central* (§26), and the required step when an appliance
  changes customer (§25).

---

## 25. Move / Retire / Delete

All are Central actions on the appliance's page (platform admin, reason,
password step-up). **Every terminal path revokes the appliance's bound
license.** Suspending or revoking a *license* is separate (§19) and never
changes the appliance's lifecycle. Choose by intent:

| Action | Reversible? | What it does |
|---|---|---|
| **Move** | — | Re-assigns an activated appliance to another site **of the same customer** (new signed assignment). Its license is re-issued for the new site with the **same terms** in the same transaction. **Fails closed:** if licensing is unavailable on Central, or the license cannot be read, nothing changes (`503`); a license already past its end date must be renewed first (`409`). A move to another customer is refused (`409 cross_customer_move`). |
| **Retire** | ❌ Terminal | Typed serial. The license is revoked at once. Two-phase: a **signed terminal assignment** is delivered (*Retiring*), the appliance acknowledges (retrying until Central confirms), then its credentials are revoked (*Retired*). If no acknowledgement arrives within 10 minutes, the retirement is flagged *unconfirmed* with a security alert (credentials are **not** revoked). **Emergency** (lost, stolen or dead box, or *retire it now without waiting*) revokes credentials at once without waiting. |
| **Delete record** | ❌ Permanent | Only for a *Waiting for activation* or *Retired* appliance. Typed serial + reason. Removes the record; audit history is kept. |

There is no "deactivate": an activated appliance whose service should pause has
its **license suspended** instead.

**Changing an appliance's customer** is never a move: **Retire** it → have it
**factory-reset** on site (§24; new identity key) → it registers again as
*Waiting for activation* → **Activate** it for the new customer. The old
customer's local data leaves with the factory reset, not with an in-place purge.

### Cross-customer transition & secure data purge

An appliance's local site database holds tenant-owned client data (access plans,
vouchers, sessions, client PII, PMS/notification/social/payment **credentials**,
walled garden, portal config, operators, usage/accounting). When an appliance
moves to a **different Customer** — a genuine reassignment, or a Customer that was
**deleted and recreated** (a new tenant UUID), decommission-and-reuse, or an
ownership transfer — that previous customer's data must never remain readable
under the new owner.

Central never sends a cross-customer assignment (moves stay within the customer,
and a new customer means a factory-clean box), so the purge below is a
**defence-in-depth guard**, not an operator workflow. The appliance enforces it
automatically, comparing **immutable tenant UUIDs**
(never names/slugs):

- **Same-customer** changes (a move to another site of the same customer, same
  tenant UUID) → **all tenant data is preserved** (plans, vouchers, sessions stay
  valid).
- **Cross-customer** transition (different tenant UUID) → on the next boot the
  appliance **securely purges every previous-tenant row and cached secret** in one
  transaction *before it authorizes any client*: it repoints the live client
  networks (VLAN/DHCP/portal stay up) to the new owner, deletes all foreign-tenant
  rows across every tenant-owned table + the local tenant/site mirror, flushes runtime client
  authorization (nftables), and writes an **audited transition record**
  (`appliance.tenant_transition_purge`) with the purged counts.
- **Fail-closed:** if the purge cannot complete, the appliance authorizes **no**
  clients (`tenant_transition_pending`) until it succeeds — a partial cleanup can
  never expose one customer's data to another. The purge is idempotent, so a
  retry (or reboot) completes safely.

Preserved across a transition: appliance/system/network/bootstrap state (WAN/mgmt,
identity, certs, client-network topology) and the immutable security **audit
history**. Client-facing data and secrets are not.

---

## 26. Safe deletion & dependency order

StayConnect never silently cascades a customer/site delete. Deletion is **blocked
while owned records still exist**, and the dialog lists exactly what to remove.

**Teardown order:** **Appliances → Site → Customer.**

- **Retire** the **Appliances** first (this revokes their bound licenses), then
  **Delete record** for each.
- Then delete the **Site** (a site with appliances or a current license is blocked).
- Then delete the **Customer** (a customer with sites or appliances is blocked).

There is **no subscription step** in the teardown — subscriptions were retired and
are never a delete blocker.

Notes verified in code:
- An **appliance delete** is allowed only for a *Waiting for activation* or
  *Retired* appliance; the safety is that state + the typed serial + password
  step-up. A **retired** appliance's identity key is recorded when its record is
  deleted and can never register again.
- **What a running appliance does when its record is deleted** (it finds out on
  its next hello, within 5 minutes): one that **never held a customer** clears its
  identity and registers again as waiting. One that **has held a customer** (a
  retired appliance, for example) removes its license and client certificate,
  keeps its identity and data, records the fact durably
  (`/etc/stayconnect/removed-from-central.json`) and **never registers again**.
  It admits no new clients (clients already online are not disconnected), refuses
  license and activation files, and the Admin Console shows *Removed from OneGate
  Central*. Only a factory-clean install (§24) and a new activation bring it back.
- A **client network** (on the appliance) cannot be deleted while enabled or with
  active sessions.

---

## 27. Security alerts & certificate monitoring

- **Security alerts** (Central → **System → Security alerts**) surface
  clone/hardware-reuse attempts, WAN-MAC mismatches, unconfirmed retirements,
  elapsed replacement windows and similar events. A hardware-reuse alert usually
  means an old appliance must be retired before the new box can register.
- **Trust & keys** (Central → **System**) shows the CA, appliance mTLS
  certificates and expiry, the assignment signing keys and the key registry
  (read-only; a key's state is changed with the host command
  `ctrlapi assignment-key verify-only|revoke`). On the box,
  the Admin Console **TLS certificate** page auto-renews the on-box cert (45-day
  window / on IP change / SAN drift); **Rotate** forces a fresh cert (reason +
  password; you never upload a key).

---

## 28. Backup & recovery

- Both Central and the appliance run automated backup with fail-safe
  artifact retention. Cleanup never deletes the current/previous release, the
  newest DB backup, PKI material, or pinned artifacts.
- Central exposes a **Backup health** page (and a backup-health API) so you can
  confirm backups are current across the fleet.
- For disaster recovery of an appliance, prefer **Mark for replacement** (§23) —
  it preserves the site binding and hands over the license cleanly.
- `edge_offline_packages` (the offline-activation single-use ledger) is created
  by appliance migration 0048. The command-channel and update-agent ledgers
  (`edge_executed_commands`, `edge_installed_updates`) and the telemetry queue
  (`sync_outbox`, `sync_checkpoints`) are dropped by migration 0093; see
  [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md).

### Verifying an appliance after a reboot or a restore

Two scripts re-run the acceptance checks on a live appliance. Both are
read-only or self-restoring, contact no Production system, and enable nothing:

```sh
bash deploy/scripts/phase7-appliance-m4.sh      # the assembled system: services,
                                                # roles, boundaries, darkness,
                                                # backup+restore, restoration proof
bash deploy/scripts/phase7-final-reboot.sh      # REBOOTS the appliance, then proves
                                                # it came back with no operator action
```

`phase7-final-reboot.sh` really reboots the machine and verifies the kernel boot
id changed — a service restart is not reboot evidence. It waits for the
appliance to converge to **serving** (not merely `active`, since a unit can be
active while the thing it serves does not answer) and repairs nothing: if the
appliance needs a human to come back, the run fails rather than hiding it.

---

## 29. Audit & monitoring

- **Every** privileged action in Central and on the appliance is written to an
  **audit log** with actor, action, target and reason. Delete/rotate/restart
  actions require a reason that is recorded.
- Central receives **no telemetry** from appliances — the appliance has no
  telemetry subsystem (CLAUDE.md §0E): its
  **Overview** and **Appliances** pages show activation, connection (from the
  last authenticated appliance call) and license state only. Appliance service
  health, sessions and usage are watched in each appliance's Admin Console.
- Central's audit log is **System → Audit log** (filter by customer, action,
  date) and each customer's **Activity** tab. Legacy `subscription.*` action
  names may appear on **historical** rows; nothing writes them any more.

---

## 30. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Appliance never appears in Central | No WAN/internet, or `SCD_AUTO_REGISTER=false` | Fix WAN (§5) and use **Check now** in the Admin Console → Appliance & licence; or use offline activation (§34) |
| Registration refused (403) + security alert | Clone/hardware-reuse protection | Retire the old appliance (§25), then retry |
| Registration refused `403 identity_retired` | This identity key was retired | Factory-reset the box (§24); it registers with a new key |
| Admin Console shows *Removed from OneGate Central* | Central deleted the appliance after it had held a customer | Factory-clean install (§24), then activate it again (§26) |
| Room sign-in attempts show *Licence refused new clients* / *Licensed capacity full* | The licence, not the client's details: room sign-in answers to the same licence gate and capacity as every method | Check the license state; wait for a slot or raise capacity (§10) |
| Stuck at Waiting for activation | Not activated yet | Activate it (§7) |
| Stuck activating / no license | CSR/license not yet pulled, or Central briefly unreachable | Wait; check Diagnostics; confirm Central reachability |
| Clients denied, License shows Expired | Past valid-until + grace | Renew the license (§20) |
| Clients denied, `LICENSE_CAPACITY_REACHED` | At appliance-wide concurrent capacity | Wait for a slot, or raise Max Concurrent Online Clients (§20) |
| Client denied, `MAX_DEVICES_REACHED` | The voucher/account is at its **plan max devices** | Disconnect a device (Admin Console → Client accounts / Sessions), raise the plan's Max devices, or use another credential |
| A device's reconnect seems to "use up" a slot | It doesn't — same credential + same device reuses its slot | Confirm the extra slot is a *different* device (MAC); check active devices on the account/voucher Details |
| Client login says "Invalid username or password" for a known-good account | Disabled, outside valid-from/until, or locked after failed attempts | Check enabled + validity; reset the password (clears the lockout); short-wait if throttled |
| Client login says "Too many attempts" | Brute-force throttle tripped (username+IP/device or endpoint-wide) | Wait ~1 minute and retry |
| Lost a client password | Passwords are shown once and never stored in plaintext | Set a new password (Client accounts → Password); it's shown once again |
| Can't change a voucher's plan | Voucher is revoked/expired/exhausted, or has a live session | Only unused/idle vouchers can be repointed; disconnect the session first |
| Client keeps re-using an "expired" voucher for more time | Voucher duration is a **validity window** from first use; it doesn't reset | Expected: once the window closes the voucher shows **Expired** and re-login is refused; a reconnect only gets the remaining window. Issue a new voucher for more access |
| A voucher expired "too fast" with two devices | The window is wall-clock from first activation, **not** per-device | By design — a second device shares the same window, it doesn't add time. Give each client their own voucher, or use a longer plan |
| Clients denied, state Unlicensed | No valid license (fail-closed) | Activate / import license (§7, §34) |
| Can't create plans/vouchers/accounts | License not Active | Renew/activate |
| Portal doesn't auto-pop | DHCP option 114 / walled garden | Verify network settings and walled garden (§13, §17) |
| Locked out after a WAN/LAN change | Confirmation window elapsed | It auto-rolled-back; reconnect to the old IP (§5) |
| OneGate Central *Temporarily unreachable* | Central unreachable | Informational only; clients keep working (§22) |
| WAN-MAC mismatch warning | WAN NIC changed | Rebind WAN MAC (§23) |

---

## 31. Go-live checklist

- [ ] Appliance **Activated** (Central shows Activated, license Active; Admin
      Console → Appliance & licence shows licence **Active**, the correct client
      maximum and **Valid until**).
- [ ] WAN/LAN correct and confirmed (no pending rollback).
- [ ] Client network(s) / VLAN(s) created, applied and confirmed.
- [ ] DHCP pools, DNS, NAT and client isolation set per network.
- [ ] Captive portal reachable at `http://{gateway}:8380`; option 114 popping the
      portal.
- [ ] Auth method(s) configured and tested; integrations healthy.
- [ ] Walled garden covers portal/payment/OAuth hosts.
- [ ] Access plans + voucher batches ready (if using vouchers).
- [ ] **Real client acceptance test passed** on each VLAN (§18).
- [ ] Branding correct per network.
- [ ] Operators created with least-privilege roles.

---

## 32. Day-2 operations checklist

| Task | Where |
|---|---|
| Watch the fleet & alerts | Central → Overview (*Needs attention*) / System → Security alerts |
| Renew a license before expiry | Central → the appliance → License → Renew or change (§20) |
| Raise/lower concurrent capacity | Central → the appliance → License → Renew or change (§10, §20) |
| Add/replace an appliance | Central → Advanced → Mark for replacement (§23) |
| Move an appliance to another site of the same customer | Central → the appliance → Installed at → Move (§25) |
| Give an appliance to another customer | Retire → factory-reset → Activate (§25) |
| Rebind after a NIC swap | Central → Advanced → Rebind WAN MAC (§23) |
| Rotate a cert | Admin Console → TLS certificate (§27) |
| Issue voucher batches | Admin Console → Voucher batches (§15) |
| Add a client VLAN | Admin Console → Networking → Client networks (§11) |
| Check backups | Central → System → Backup health (§28) |
| Review audit log | Central → System → Audit log / Admin Console → Activity (§29) |
| Retire hardware | Central → Retire appliance, then Delete record (§25, §26) |

---

## 33. Terminology (canonical operator terms)

Use these operator-facing terms consistently. Internal/technical names in the
right column are kept in code/URLs where changing them would create migration
risk, but should not be shown to operators as the primary term.

| Concept | Canonical operator term | Internal / technical name(s) |
|---|---|---|
| Paying organization | **Customer** | tenant, `tenant_id` (API: `customer_id`) |
| Physical location | **Site** | site |
| Wi-Fi end user | **Client** | guest (`guests`, `guest_*`, `/guest-*`) |
| On-appliance console | **Admin Console** | `hotel-admin` |
| Captive sign-in page | **Client Portal** | portald |
| End-user access network | **Client network** (Admin Console: Networking → Client networks) | `guest_networks`, `guest_network_id`, bridge/VLAN names |
| On-site gateway | **Appliance** | appliance |
| Bring an appliance online (normal) | **Activate / Activation** (zero-touch) | register, `pending_approval → assigned` |
| Install without internet | **Offline activation** | activation request / activation package |
| Take an appliance out of service | **Retire** | revoked / decommissioned, terminal assignment |
| The entitlement | **Signed appliance license** | license (NOT plan/subscription) |
| License states | **Active / GracePeriod / Expired / Suspended / Revoked / Unlicensed** | `Restricted` = legacy pre-v3 only |
| Capacity | **Max concurrent online clients** (appliance-wide) | `max_concurrent_online_guests` |
| Per-voucher device limit | **Max devices** (on an access plan) | plan max devices |
| Limit-exceeded error | **License limit reached** | `limit_exceeded` |
| Live client count | **Online clients / Active sessions** | `current_online_guests` |
| Appliance↔Central link | Central: **Connected / Recently seen / Offline / Never connected**; Admin Console: **Connected / Temporarily unreachable / Not configured** | `connection`, `central.state` |
| Client pricing/policy product | **Access plan** | guest access plan (`GuestAccessPlan`) |
| Pre-login allowlist | **Walled garden** | walled garden |
| Portal | **Captive portal / Landing page** | portald |

**Retired terms — do not present as current:** Plan, Subscription, Commercial
plan, Trial, plan limits, Enrollment token / bootstrap token, Onboarding page,
Customer context, Deactivate, Decommission (as an operator action),
cross-customer move, cloud reporting / telemetry. The signed
appliance license is the only entitlement.

---

## 34. Exceptional: offline activation

Not the normal install path. Use it only for a site with no route to Central
(or with `SCD_AUTO_REGISTER=false`). There are no enrollment tokens.

1. Admin Console → **System → Appliance & licence → Files from your OneGate vendor
   → Offline activation** → **Download activation request** (offered while the
   appliance has never reached Central) and send it to your vendor.
2. Central → **Appliances → Import activation request**. The appliance appears
   as **Waiting for activation**; **Activate** it as in §7.
3. On its page (now *Activating*) → **Activation package** (signed assignment +
   CA + license; valid 7 days) and return the file to the site.
4. Admin Console → same place → **Upload activation package**. The box verifies
   every signature and the hardware binding before accepting it.

Later renewals for an offline site: Central → the appliance → **License →
Offline license file**; Admin Console → **Upload licence file**.

Offline activation converges to the same **Activated** state and the same
license model as zero-touch; only the transport differs.
