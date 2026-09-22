# StayConnect Appliance (Hotel Admin) — Configuration Manual

Step-by-step instructions for configuring an appliance from the on-box **Hotel
Admin** console. For a description of what each page shows, see
[hotel-admin-reference.md](hotel-admin-reference.md).

**Typical order:** Connect (zero-touch) → Activate (license) → build a Guest network → set up
Auth methods → create Access plans → generate Vouchers → optional integrations
(PMS / Notifications / Social / Payments) → branding → operators.

> A few actions ask you to **re-enter your password**: applying/rolling-back
> WAN-LAN changes, rotating the TLS certificate, and restarting a service.

---

## 1. Connect the appliance to Central

**The normal path is zero-touch — you do nothing on the box.** A factory-clean
appliance with internet self-registers with Central and appears under the Control
Panel's **Onboarding** page as **Pending activation**, where an operator activates
it (no token). See the Control Panel manual, "Onboard & activate an Appliance."

You only use the on-box **Setup / Activation** page (`/setup/enrollment`) for the
**advanced/manual** path — when the box can't auto-register (e.g.
`SCD_AUTO_REGISTER=false`) or your installer was given an enrollment token:

1. In the Control Panel, mint a token: *Appliances → Enrollment token*
   (optionally locked to this appliance's **Serial**). Copy it.
2. In Hotel Admin, go to **Setup / Activation** (`/setup/enrollment`).
3. Enter the **Enrollment code** (the token) and confirm the **Serial**.
4. Click **Connect**.
5. Watch the progress. Expand *Technical details* to see the full lifecycle and the
   network/Central checks (DNS, Central :443, clock, mTLS, NATS).

When it shows **Setup complete**, the appliance holds its mTLS certificate and is
bound to the customer/site. Either way, the operator still **Activates** it in the
Control Panel to issue the license (§2).

---

## 2. Activate / license the appliance

An appliance won't authorize guests until a **real signed license** is installed.
Until then the Dashboard and License page show **Pending activation** and guests
are denied.

**Online (normal):** the Control Panel operator activates the box (Onboarding →
Activate, or Licenses → Issue license). The appliance then **fetches** its signed
license itself over its authenticated channel and installs it automatically; the
License page flips to **Active** (Central does not push it).

**Offline activation (no cloud):**
1. On the License page, copy the **StayConnect Serial Number** and **WAN MAC
   Address** (one-click copy) and send them to StayConnect.
2. StayConnect returns a signed `.license` file generated for that exact
   Serial + WAN MAC.
3. On the **License** page → **Offline activation** → **Upload license file**.
   The appliance verifies the file is bound to this exact hardware before
   accepting it.

**Reading the License page states:**
- **Active** — licensed and enforcing; guests can connect up to the limit.
- **Grace period** — the license expired but guests keep working (with warnings)
  until the grace end; renew soon.
- **Expired / Revoked / Suspended** — new guest logins are refused; existing
  sessions keep running; DHCP/DNS/portal/admin stay up.
- **Capacity reached** — you're at the concurrent-guest limit; new logins get
  `LICENSE_CAPACITY_REACHED` until a slot frees.
- **Hardware mismatch** — the WAN NIC changed; running on a time-limited grace —
  ask StayConnect to authorize a **Rebind**.
- **Not activated / Pending** — no real license; guests denied.

---

## 3. Configure WAN / LAN networking

Go to **WAN / LAN settings** (`/network/system`).

> This page covers only the **WAN uplink** and the appliance's **legacy base
> bridge** (`br-lan`). Guest WiFi is **not** configured here — each Guest Network
> has its own VLAN, bridge, gateway, DHCP pool and captive portal, managed under
> **Guest networks** (§4) and **DHCP & leases**. The legacy base bridge appears in
> an **Advanced · Base LAN / Legacy Bridge** section with a *Legacy* badge; its
> DHCP showing **off** is normal when guests are served by Guest Networks.

1. Review the **WAN / Management** status card and the **Guest Networks** pointer.
2. Under **Change configuration** set what you need:
   - **WAN:** IP address, prefix length, default gateway, DNS (comma-separated).
   - **Base LAN:** base-bridge gateway IP, prefix length. (Guest DHCP is managed
     per guest network on the DHCP page, not here.)
3. Click **Validate & preview** — review the before/after and the new management
   URL.
4. Click **Apply change** and **enter your password**.
5. A **countdown banner** appears. Reconnect to the new management URL if the IP
   changed, then click **Keep this configuration**. If you don't confirm in time,
   it **auto-rolls-back** — so a wrong IP can never lock you out.

---

## 4. Create a Guest network (WiFi VLAN with captive portal + DHCP)

Go to **Guest networks** (`/network`) → **New guest network**. The 7-step wizard:

1. **Identity** — Name, Description, SSID label (this is just a label; StayConnect
   does not broadcast WiFi — your wireless controller does).
2. **Interface / VLAN** — pick the parent interface (guest-access or guest-trunk).
   Tick **VLAN tagged (802.1Q)** and set the **VLAN id** (1–4094) for a tagged
   network.
3. **Subnet & gateway** — Subnet CIDR (e.g. `10.20.0.0/22`) and Gateway IP (e.g.
   `10.20.0.1`) — the appliance owns the gateway; guests use it as gateway + DNS.
4. **DHCP & DNS** — add DHCP pool ranges; DNS mode (**appliance** resolves on the
   gateway, or **custom servers**); Domain name; lease default/min/max.
5. **Captive portal** — toggle Captive portal, Internet access, NAT (masquerade),
   Client isolation. The portal is served at `http://{gateway}:8380`.
6. **Review** — check the summary and the "map SSID→VLAN on your controller" note.
7. **Apply** — **Create, validate & apply**, then **Confirm** within the countdown
   (or it rolls back).

> Topology (type, VLAN, parent interface, bridge) is **immutable** — to change it,
> delete and recreate the network. Other settings are editable on the network's
> detail page.

**DHCP reservations:** pin a device MAC to a fixed IP on **DHCP & leases**
(`/network/dhcp` → Reservations → New) or on the guest network's detail page.

---

## 5. Choose guest authentication methods

Guests can authenticate by **voucher**, **OTP** (email/SMS), **PMS** (room + name),
**social login**, or **payment** — depending on what you configure and what your
license entitles (see the Entitlements table on the License page).

- **Vouchers** need an internet package to grant, then a printed batch (below).
- **OTP** needs a **Notifications** provider (§8).
- **PMS** needs a **PMS provider** (§7).
- **Social** needs **Social login** OAuth apps (§9).
- **Paid WiFi** needs a **Payments/Stripe** account (§10).

Make sure the portal endpoints are reachable pre-login via the **Walled garden**
(§11).

---

## 6. Create Access plans and print vouchers

**Access plan** (**Guest access plans** → **New plan**):
- Code, Name, Description, **Duration (s)** (blank = unlimited time), **Data cap
  (bytes)** (blank = unlimited), **Down/Up kbps**, **Max devices**, **Price
  (cents)**, **Currency**.

> **License capacity vs. plan max devices.** The signed license caps the total
> concurrent guests **across the whole appliance**. A plan's **Max devices** caps
> the concurrent devices **per voucher or per guest account**. Both are enforced
> on every login, atomically: a device rejected for either reason gets a clear
> error (`MAX_DEVICES_REACHED` / `LICENSE_CAPACITY_REACHED`) and no session is
> created. A reconnect from a device that is already signed in on the same
> credential does not consume a second slot; freeing a device (disconnect/expiry)
> frees its slot.

**Vouchers** (**Vouchers** in the sidebar) — printed cards a guest redeems for
internet access.

- **Code format** (top of the screen, set once by the IT manager or site admin):
  **Digits only** for a property whose guests type on a numeric keypad, or
  **Digits and letters** for a printed card, and a **length of 6, 7 or 8
  characters**. Eight is the maximum, for both. Characters guests misread from a
  card — I, L, O, U and the ambiguous 0/O, 1/I/L, 5/S — are always excluded and
  are not a setting. A change applies to the **next** batch you print; cards
  already issued keep the format they were printed with and stay redeemable, and
  nothing is restarted or redeployed.
- **Print a batch**: choose the **internet package revision** the cards grant,
  **how many** (1–500), an optional **valid until**, and an optional **note**.
  The codes appear once, on the screen, ready to print. A *valid until* already
  in the past is refused — cards printed from it could never be redeemed. Leave
  it empty for cards that never expire.
- **What a batch is**: every print run gets a batch id, which is how a run of
  cards is exported or traced later. The list shows each card's last four
  characters, its state (*not used yet*, *redeemed*, *cancelled*, *expired
  unused*), its batch, when it was printed and when it expires.
- **Show code** recovers ONE code for a card already in circulation — the guest
  at the desk whose card is smudged. It asks for your password and a reason, and
  records both permanently, with your name. **Export codes** does the same for a
  whole batch and writes one record naming how many you took.
- **Cancel card** stops an unused card working. It cannot be undone. A card that
  has already been **redeemed** cannot be cancelled here: that guest has access
  now, and ending it is done from their session.
- **Who has read a code** lists every reveal and export. Nothing on it can be
  edited or removed by anyone, including a site admin.

> **A voucher code is recoverable, and that is why the record matters.** A
> post-stay PIN and a guest-account password are stored hashed, so "shown once"
> is enforced by arithmetic — nobody can produce them again. A voucher code is
> stored encrypted, so it **can** be read again. What protects it is not secrecy
> from the hotel but visibility: no one can read a code without leaving a row
> saying who they are and why.

**Guest accounts** (**Guest accounts** → **New account**) — username/password
sign-in, an alternative to vouchers:
- **Username** (1–64 chars; a single letter `A` or digit `1` is allowed;
  case-**insensitive** and unique per property), **Password** (1–128 chars,
  case-**sensitive**; very short passwords are allowed for temporary guest
  credentials but a non-blocking *weak password* warning is shown), **Guest
  access plan** (from the dropdown; label shows duration/speed/max-devices),
  optional **display name / valid-from / valid-until / notes**. The plan supplies
  duration/data-cap/speed/max-devices, exactly like a voucher.
- **Passwords are shown once.** On create/reset you can type a password (with
  show/hide) or **Generate** one; the exact password is then displayed **once**
  in a confirmation panel with **Copy**. It cannot be retrieved afterwards — if
  it is lost, set a new one. Nothing ever returns the password or its hash; it is
  stored only as an Argon2id hash.
- Edit any account with a pre-filled form — **username, password, plan, display
  name, valid-from/until, enabled/disabled, notes** — no delete-and-recreate.
  Changing the plan applies to **future** logins; a running session keeps its
  policy. The plan list shows only this property's active plans; an existing
  account on a now-inactive plan still shows it with an **inactive** badge.
- The list shows **active devices** (e.g. `1 of 2`), enabled state, **locked**
  status, validity, last login and login count. Per row: **Edit**, **Password**
  (set/reset, with optional *Generate* and *Disconnect existing sessions after
  reset*), **Enable/Disable**, **Disconnect** active devices, **Delete**.
- Toggle **Show Username & Password tab on the captive portal** to let guests use
  it. Wrong/unknown/disabled/expired/locked all return one **generic** error and
  create no session; per-account lockout plus layered throttling (username+IP,
  username+device, endpoint-wide) damps brute force.

---

## 7. Connect a PMS (room + name login)

**PMS providers** → **New provider**:
- **Name**, **Kind** (`protel-fias` / `opera-fias` / `fidelio-fias` for FIAS, or
  `mews` / `apaleo` for REST, or `stub` for testing), **Display name**.
- FIAS: **Host**, **Port**, **Auth key** (write-only), **Use TLS**.
- REST: **Base URL**, **API key** (write-only), **Property ID**.

Use **Test** to check connectivity, **Health** for status, and **Cache** to see
which reservations are currently loaded.

---

## 8. Set up OTP delivery (email / SMS)

**Notifications** → **New**:
- **Channel** (email or sms), **Kind** (email: `sendgrid` / `ses`; sms: `twilio`;
  or `stub` for testing), **Display name**, **API key** (write-only), **API user**
  (Twilio SID for SMS). Email also: **From address**, **From name**.

---

## 9. Add social login

**Social login** → **New**:
- **Provider** (google / apple / facebook / microsoft), **Display name**, **Client
  ID**, **Client secret** (write-only), **Redirect URI**, **Scopes**
  (e.g. `openid email profile`).

---

## 10. Sell WiFi (Stripe payments)

**Payments** → **New** (Stripe account):
- **Display name**, **Publishable key** (`pk_live_…`), **Secret key** (write-only),
  **Webhook secret** (write-only), **Success URL**, **Cancel URL**.

Recent guest purchases appear in the **Recent payments** table.

---

## 11. Walled garden (pre-login access)

**Walled garden** → **New rule**: **Kind** (domain/ip/cidr), **Value**, **Ports**
(comma; blank = all), **Description**. Add the domains/IPs your portal, payment,
and OAuth callbacks need so guests can reach them before authenticating.

---

## 12. Portal branding

**Portal branding** — edit the branding JSON (logo URL, terms, languages, colors)
and **Save** (it validates the JSON first).

---

## 13. Manage Hotel Admin staff (operators)

**Operators** → **New operator**: Email, Display name, **Password (min 10)**,
**Role**. Roles (least → most access):

| Role | Can do |
|---|---|
| `site_viewer` | Read-only across the appliance |
| `voucher_operator` | Print, cancel and reveal voucher codes + read sessions/reports |
| `guest_relations_operator` | Vouchers (incl. reveal) + sessions + read integrations |
| `front_office_operator` | Vouchers (incl. reveal) + sessions + read integrations/reports |
| `payments_operator` | Manage payments/Stripe |
| `hotel_it_manager` | Networking, PMS, integrations, certificate, diagnostics-restart |
| `site_admin` | Everything |

> **Reading a printed code is its own permission.** Printing and cancelling cards
> is one power (`vouchers`); recovering a code **in the clear** for a card already
> in a guest's hand is another (`voucher-codes`), and it asks for the operator's
> password and a reason every time, then records both permanently. The hotel IT
> manager chooses what codes look like and cannot read one — configuring is not
> reading. Unlike a post-stay PIN, a voucher code is recoverable, so what protects
> it is that it cannot be read unseen.


Use **Set password** to reset, **+ role** / remove role to adjust access,
**Disable** to revoke a login. You can't remove your own `site_admin` role or
disable yourself.

---

## 14. TLS certificate maintenance

**TLS certificate** — the cert auto-renews (45 days / on IP change / SAN drift).
Use **Check certificate** to validate now, and **Rotate** (Reason + password +
type `ROTATE`) to force a fresh certificate. You never upload a key.

---

## 15. Diagnostics & recovery

**Diagnostics** (`/health`) shows every service's health, restart counts, adaptive
backoff, and recovery history. Services self-heal automatically — you should not
normally need to intervene. If you do: **Recheck** re-runs a health check,
**Logs** shows recent sanitized logs, and **Restart** (reason + password) restarts
a service.

---

## Quick reference

| I want to… | Page |
|---|---|
| Connect the box to the cloud (advanced/manual) | Setup / Activation |
| Activate / install a license | License |
| Change WAN or LAN IP | WAN / LAN settings |
| Add a new guest VLAN | Guest networks → New |
| Pin a device to a fixed IP | DHCP & leases → Reservations |
| Issue guest WiFi codes | Vouchers → Print a batch |
| Room-number login | PMS providers |
| Email/SMS OTP | Notifications |
| Google/Apple sign-in | Social login |
| Sell WiFi | Payments |
| Add a staff login | Operators |
| Check appliance health | Diagnostics |
