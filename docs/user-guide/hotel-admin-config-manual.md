# Velonet Hotel Admin — Configuration Manual

Step-by-step instructions for configuring an appliance from **Velonet Hotel
Admin**, the console on the appliance. For a description of what each page shows,
see [hotel-admin-reference.md](hotel-admin-reference.md).

**Typical order:** Connect and activate (license) → WAN / LAN → build a Guest
network → Service plans and Internet packages → Sign-in methods → Vouchers and
guest accounts → PMS → Email & SMS / Social login → Allowed sites → Portal
settings → Operators.

> Sensitive actions ask you to **confirm your password** in a dialog — for
> example applying or rolling back WAN / LAN changes, rotating the TLS
> certificate, restarting a service, backups, showing a voucher code, putting a
> PMS configuration live, and saving custom CSS or HTML on the portal. Many also
> ask for a **reason**, which is written to the activity log. The full list is at
> the end of [hotel-admin-reference.md](hotel-admin-reference.md#actions-that-need-more-than-a-click).

---

## 1. Connect the appliance to Central

**The normal path is zero-touch — you type nothing on the appliance.** A
factory-clean appliance with internet registers itself with Velonet Central and
appears on Central's **Onboarding** page as **Pending activation**, where a
Central operator activates it. See the Central manual, "Onboard & activate an
Appliance."

Everything about this lives on **System → Appliance & licence** (`/appliance`),
tab **Appliance setup**:

- **Online** (recommended) shows the appliance's serial and whether Velonet
  Central is reachable. Once it is activated in Central, a three-phase progress
  runs — **Connect → Verify → Ready** — and ends with *"This appliance is
  connected"* and **Setup complete**.
- **Offline**, for an appliance with no route to Central: **Download activation
  request**, import it in Central under **Onboarding → Offline activation**, have
  it activated, then upload the returned **Activation package file** here.
- **Advanced / recovery** (collapsed) is only for an enrollment token minted in
  Central (*Appliances → Enrollment token*): enter it as the **Enrollment code**.
  The detailed checks (identity, connectivity, certificate, license, completion)
  are also here.

Only the Site admin and Hotel IT manager can activate; other roles can follow
the progress.

---

## 2. Activate / license the appliance

An appliance serves no guests until a **signed license** is installed.

**Online (normal):** the Central operator activates the appliance (Onboarding →
Activate, or Licenses → Issue license). The appliance fetches its signed license
itself and installs it; the **Licence** tab flips to **Active**.

**Offline:**
1. On **Appliance & licence → Licence**, copy the **Serial number** and **WAN MAC
   address** (large, with copy buttons) and send them to your Velonet contact.
2. You receive a signed license file generated for that exact serial and WAN MAC
   (Central: *Licenses → Download for offline*).
3. On the **Licence** tab → **Upload licence file**. The appliance checks that the
   file belongs to this hardware before accepting it.

**Reading the License tab:**
- **Active** — licensed; guests can connect up to **Max concurrent online
  guests** (shown as a bar: green, amber from 80%, red at 100%).
- **Grace period** — the license has expired but guests are still served (with
  warnings) until the grace period ends; renew soon.
- **Expired / Revoked / Suspended** — new guest logins are refused; existing
  guest sessions are not dropped; DHCP, DNS, the sign-in page and Hotel Admin
  stay up.
- **Licensed capacity reached** — new guest logins are refused until someone goes
  offline.
- **Hardware mismatch** — the WAN network card changed; ask Velonet to rebind the
  license.
- **Connection to Central** — *Used for: Licensing only*.

---

## 3. Configure WAN / LAN networking

Go to **Networking → WAN / LAN settings** (`/network/system`).

> This page covers only the **WAN uplink / management address** and the
> appliance's **legacy base bridge**. Guest Wi-Fi is **not** configured here —
> each guest network has its own VLAN, gateway, address pool and sign-in page,
> managed under **Guest networks** (§4) and **DHCP & leases**. The legacy bridge
> sits in a collapsed **Advanced · Base LAN / Legacy bridge** card.

1. Review the **WAN / Management** card.
2. Under **Change configuration** set what you need:
   - **WAN:** IP address, prefix length, default gateway, DNS.
   - **Base LAN:** gateway IP and prefix length. (DHCP is managed per guest
     network, not here.)
3. Click **Validate & preview** — review before → after and the new management
   address.
4. Click **Apply change…** and **confirm your password**.
5. A **countdown banner** appears. Reconnect to the new management address if it
   changed, then click **Keep this configuration**. If nobody confirms in time, the
   appliance **rolls back automatically** — so a wrong address cannot lock you
   out. **Roll back now** also asks for your password.

---

## 4. Create a Guest network (a VLAN with its own addresses and sign-in page)

Go to **Networking → Guest networks** (`/network`) → **New guest network**. The
seven steps:

1. **Identity** — name, description, SSID label (a label only; the appliance
   does not broadcast Wi-Fi — the hotel's wireless controller does).
2. **Interface / VLAN** — pick the parent interface; for a tagged network tick
   the VLAN option and set the **VLAN id** (1–4094).
3. **Subnet & gateway** — e.g. `10.20.0.0/22` and `10.20.0.1`. Guests use the
   gateway as their router and DNS.
4. **DHCP & DNS** — one or more address pools; DNS from the appliance or custom
   servers; domain name; lease times.
5. **Captive portal** — sign-in page, internet access, NAT, client isolation.
6. **Review** — including the *"Wireless controller action required"* reminder to
   map the SSID to the VLAN.
7. **Apply** — **Create, validate & apply**, then **Keep this change** before the
   countdown ends (or it rolls back on its own).

> The topology (type, VLAN, parent interface) cannot change after creation — to
> change it, delete and recreate the network. Other settings are editable on the
> network's own page; saved edits reach guests only after **Apply changes** on
> Guest networks (and confirming in time).

**DHCP reservations:** pin a device's MAC to a fixed address on **DHCP & leases**
(`/network/dhcp` → Reservations → **New reservation**) or on the guest network's
own page.

---

## 5. Choose guest sign-in methods

Go to **Guest portal → Sign-in methods** (`/sign-in-methods`). Each switch applies
immediately. Which methods can be offered also depends on the license.

- **Voucher code** — needs an internet package and printed cards (§6).
- **Guest account** — username and password accounts (§6).
- **Room sign-in (from the PMS)** — needs a working PMS connection and Network
  routing (§7). Choose what the guest types besides the room number: *Any of the
  three (recommended)*, *Last name (surname)*, *First name* or *Reservation
  number*.
- **Email code / SMS code** — need a sender under **Email & SMS** (§8).
- **Social login** — tick each provider set up under **Social login** (§9).
- **Guest sign-in protection** — maximum failed attempts, observation window and
  how long a device must wait. Only the Site admin and Hotel IT manager can
  change these; reception can release a single waiting device on **Guest sign-in
  attempts**.

Make sure anything the sign-in page needs before sign-in is listed under
**Allowed sites** (§11).

---

## 6. Create service plans, internet packages, vouchers and guest accounts

**Service plan** (**Internet offering → Service plans → Add plan**) — the technical
recipe: plan code, display name, download/upload speed (Mbps; empty = unlimited),
**devices at once** and what happens when the limit is reached, total time
allowance, data allowance (GB), disconnect after inactivity, maximum single
session, and whether the speed is per device or shared. Saving creates a new
version; you are then offered **Apply these settings to packages?** for future
guests.

**Internet package** (**Internet offering → Internet packages → Add package**) —
what the guest is offered: name, short code, the service plan, how long access
lasts, the data allowance (the service plan's, or per night of the stay), and **who it is
offered to** (conditions; empty means everyone who signs in). Packages are free to
the guest. Only the Site admin can create or change packages and service plans (the
Site viewer can read them; other roles do not see these pages).

> **License capacity vs. max devices.** The signed license caps the total number
> of guests online **across the whole appliance**. A service plan's **devices at
> once** caps the devices **per guest, voucher or account**. Both are checked on
> every sign-in; a device refused for either reason gets a clear message on the
> portal and no session. A device that is already signed in does not use a
> second place when it reconnects.

**Vouchers** (**Internet offering → Vouchers**) — printed cards a guest redeems for
internet access.

- **Code format** (Site admin or Hotel IT manager, on the **Code security** tab):
  **Digits only** or **Letters and digits**, and a length of **6, 7 or 8**
  characters, with a reason. A change applies to the **next** batch; cards already
  printed keep working.
- **Issue vouchers**: choose the **internet package**, **how many** (1–500),
  optional **valid from / valid until**, and an optional **note**. The codes are
  shown **once** — **Copy all**, **Download CSV** or **Print cards** (with a
  heading of up to 60 characters on each card).
- **Batches**: every issue run is a batch. **Export codes** (whole batch or unused
  only) asks for a **reason and your password**.
- **Show full code** recovers one code for a card already in circulation — the
  guest at the desk whose card is smudged. It asks for a **reason and your
  password** and records both permanently with your name.
- **Cancel card** stops an unused card working (**reason and password**). It
  cannot be undone. A used card cannot be cancelled; end that guest's access from
  **Active sessions**.
- **Access log** lists every code that was read, by whom and why. Nobody can edit
  or remove it.
- **Code keys** (Site admin or Hotel IT manager): **Retire** a key (**reason and
  password**, cannot be undone) and new batches use a fresh one; cards already
  printed keep working.

> **A voucher code is recoverable, and that is why the record matters.** A
> post-stay PIN and a guest-account password are shown once and cannot be
> produced again. A voucher code can be read again — but never without leaving a
> record of who read it and why.

**Guest accounts** (**Guests → Guest accounts → Add account**) — username and
password sign-in, an alternative to vouchers:
- **Username** (what the guest types; a single character is allowed), **Name**
  (for staff only), **Password** (typed, with show/hide and a warning when it is
  short, or **generated**), optional **Valid from / Valid until** and **Notes**.
  Which internet package the guest may then take is decided by the package rules.
- **Passwords are shown once**, with **Copy** and **I have it**. They cannot be
  looked up later — if lost, set a new one with **Password** (optionally
  disconnecting the account's devices).
- Per row: **Edit**, **Password**, **Disable / Enable**, **Disconnect** (when
  devices are online), **Delete**.
- Switch **Offer username-and-password sign-in** on to show the account form on
  the portal.

---

## 7. Connect the PMS (room sign-in)

1. **Property management system → PMS connection** (`/pms-interfaces`) → **Add
   connection**: **Provider → Connection** (name, PMS time zone, address and
   port, timing settings) **→ Credentials** (if the provider needs one; stored
   write-only) **→ Review**. The configuration is saved as a draft.
2. Open the connection (**Manage**) → **Configuration** → **Put live** the draft
   (**password**, with a reason).
3. **Actions** → **Activate** (**password**, with a reason). **Test the
   connection** reads a small sample and writes nothing to the PMS.
4. **Property management system → Network routing** (`/pms-routing`, Site admin
   only): point each guest network that should offer room sign-in at the
   connection.
5. Turn on **Room sign-in** under **Sign-in methods** (§5).

If the guest list needs reloading, use **Actions → Refresh the guest list now**
(**password**, with a reason); the current list stays in use until the new one is
complete.

---

## 8. Set up code delivery (Email & SMS)

**Guest portal → Email & SMS** (`/notifications`) → add a sender:
- **Channel** (Email or Text message) and **Service** (SendGrid or Amazon SES for
  email, Twilio for SMS, or *Test only*), **Name**, **API key** (write-only),
  **Account SID** (SMS) or **API user**; for email also **From address** and
  **From name**. Switch **Use this sender** on.

---

## 9. Add social login

**Guest portal → Social login** (`/social-providers`) → add a provider:
- **Provider** (Google, Apple, Facebook or Microsoft), **Name on the portal**,
  **Client ID**, **Client secret** (write-only), **Redirect URI** (must match the
  one registered with the provider), **Scopes**, and **Offer this provider**.

---

## 10. Charges (room charges and online payments)

Selling internet is not switched on today; packages are free to the guest. The
**Charges** group (Charge health, Manual review, Settlements, Recovery) shows how
room charges and online payments are moving where the feature is enabled, and is
otherwise *Not enabled on this appliance*. Decisions there belong to the Site
admin and Payments operator and each needs your password.

---

## 11. Allowed sites (before sign-in)

**Guest portal → Allowed sites** (`/walled-garden`) → **Allow a site**: **Type**
(Domain name, Single address or Address range), **Address**, **Ports** (comma
separated; empty = every port), and **Why it is needed**. Add only what the
sign-in page itself needs — every entry is reachable without signing in.

---

## 12. Portal settings

**Guest portal → Portal settings** (`/portal-branding`) — design the guest
sign-in page with a live preview (desktop, tablet, mobile), then **Save
changes**. Guests see it immediately.

- **Template** — Classic, Split, Immersive, Header bar, Resort or Kiosk, and its
  options.
- **Brand** — logo and background photograph (PNG, JPEG, WebP or GIF, up to 8 MB;
  no SVG), colours, corner radius, typeface.
- **Content** — hotel name, welcome line, help line, terms of use link.
- **Sign-in page text** and **Languages** — reword any built-in string per
  language; choose which languages are offered or add another.
- **Advanced HTML & CSS** — custom code, saved with **password confirmation**.
- **History** — restore an earlier save (**password confirmation**).

What guests see is described in [guest-portal.md](guest-portal.md).

---

## 13. Manage Hotel Admin staff (operators)

**System → Operators** → **Add operator** (Site admin only): **Email or username**,
**Name**, **Password (at least 10 characters)**, **Role** (*Site viewer* by
default). Roles:

| Role | Can do |
|---|---|
| Site viewer | Read-only across the appliance; never sees guest credentials or voucher codes |
| Voucher operator | Issue, cancel and show voucher codes; guest accounts; read sessions |
| Guest relations operator | Vouchers (including showing codes), guest accounts, sessions (including disconnect), post-stay access, releasing a waiting device; read-only elsewhere |
| Front office operator | As guest relations, plus read-only Charges and roster reconciliation |
| Payments operator | Charges decisions; read-only sessions, usage and voucher cards |
| Hotel IT manager | Networking, PMS connection, sign-in methods and protection, portal settings, checkout grace, code format, diagnostics; cannot show voucher codes, does not see Internet packages or Service plans, cannot manage operators or take backups |
| Site admin | Everything, including operators, internet packages, Network routing and backups |

> **Reading a printed code is its own permission.** Issuing and cancelling cards
> is one power; recovering a code in the clear is another, and it asks for the
> operator's password and a reason every time. The Hotel IT manager chooses what
> codes look like and cannot read one.

Per operator: **Change password**, **+ role** / remove role (×), **Disable**. You
cannot remove your own Site admin role or disable yourself.

---

## 14. TLS certificate maintenance

**Networking → TLS certificate** — the certificate Hotel Admin is served with
renews automatically. Use **Check certificate** to check it now, and **Rotate**
(**reason + type `ROTATE` + password**) to force a new one. You never upload a key.

---

## 15. Diagnostics & recovery

**System → Diagnostics** (`/health`) shows every service's health, restart counts,
backoff, and recovery history. Services recover on their own; you should not
normally need to intervene. If you do: **Recheck** re-runs a health check,
**Logs** shows recent logs (with secrets and guest details removed), and
**Restart** (**reason + password**) restarts a service.

---

## Quick reference

| I want to… | Page |
|---|---|
| Activate the appliance / enter an enrollment code | System → Appliance & licence → Appliance setup |
| Install a license file | System → Appliance & licence → Licence |
| Change the WAN or management address | Networking → WAN / LAN settings |
| Add a new guest network | Networking → Guest networks → New guest network |
| Pin a device to a fixed address | Networking → DHCP & leases → Reservations |
| Define speeds and limits | Internet offering → Service plans |
| Decide what guests are offered | Internet offering → Internet packages |
| Issue guest Wi-Fi cards | Internet offering → Vouchers → Issue vouchers |
| Turn sign-in methods on or off | Guest portal → Sign-in methods |
| Room-number sign-in | Property management system → PMS connection, Network routing |
| Email/SMS codes | Guest portal → Email & SMS |
| Google/Apple sign-in | Guest portal → Social login |
| Change how the sign-in page looks | Guest portal → Portal settings |
| Add a staff login | System → Operators |
| Check appliance health | System → Diagnostics |
