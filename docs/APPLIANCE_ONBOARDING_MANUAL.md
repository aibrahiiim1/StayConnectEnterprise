# Appliance Onboarding Manual (UI-only)

The complete production process to bring a new hotel online. It uses **only** two
web consoles — no CLI, SQL, SSH, environment editing, certificate copying, or
UUIDs. Every screen refreshes itself; you never have to wait-and-press-Refresh.

> **Normal installs are zero-touch — nothing is typed on the appliance at all.** A
> factory-clean box with internet self-registers with Central and waits under
> **Onboarding** as *Pending activation*, where one operator click activates and
> licenses it. Enrollment tokens are only for the advanced/manual path (see
> Appendix). For the full lifecycle beyond first install, see
> [STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md](STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md).

## Consoles

| Console | URL | Login |
|---------|-----|-------|
| **Central Platform** (Control Panel) | `https://150.0.0.252` | your platform operator |
| **Appliance Hotel Admin** | `https://hotel.stayconnect.local` or `https://172.21.60.23` | your Hotel-IT operator |

The Hotel Admin is reachable on the **management network only** (guests are firewalled
off). If your workstation can't resolve `hotel.stayconnect.local`, use the management
IP `https://172.21.60.23`.

---

## A. Install the appliance
1. Rack and cable the appliance: **WAN** to the site uplink, **LAN/trunk** to the
   switch carrying your guest VLANs. Power on.
2. On first boot the box generates its own identity, detects its hardware, and — if
   it has internet — **self-registers with Central automatically**. There is nothing
   to type on the appliance.

## B. Central Platform — activate the hotel (one flow)
3. **Customers** → create or select the customer (tenant).
4. **Sites** → create or select the site under that customer.
5. **Onboarding** → the appliance appears under **Pending activation** automatically.
   Click **Activate** and set the license terms:
   - **Customer** and **Site** (select the ones from steps 3–4).
   - **Max Concurrent Online Guests** (the licensed capacity, appliance-wide).
   - **License Validity** (valid-from / valid-until).
   - **Grace Period** (days the hotel keeps serving after expiry).
6. One click runs the whole chain server-side: **claim → assign (signed assignment)
   → issue certificate → issue the hardware-bound signed license.** There is **no
   plan or subscription step** — the signed appliance license is the entitlement.

## C. Convergence (automatic — both consoles update live)
7. The appliance **pulls** its certificate and signed license itself over its
   authenticated channel (Central does not push them). Its state walks to **Active**
   within seconds to a minute.

## D. Confirm (both update live)
- **Hotel Admin → Setup / Activation:** stages reach **Setup complete** — identity,
  API mTLS, NATS mTLS all green, License **Active**, assigned Customer/Site shown by
  name.
- **Central → Fleet / Onboarding:** the appliance is **online**, licensed, heartbeat
  current, License **Active** with the capacity and validity you set.

Every action on either console refreshes the displayed state automatically, shows
success/failure clearly, is idempotent (re-clicking creates no duplicate), requires
password step-up where defined, and writes audit evidence. The manual **Refresh**
buttons remain only as an optional diagnostic.

---

## Appendix — Advanced / manual: enrollment token

Use a token **only** when the box cannot auto-register (e.g.
`SCD_AUTO_REGISTER=false`, or an air-gapped bring-up where an installer must type a
code). The zero-touch flow above is the default and needs no token.

1. **Central → Appliances → Enrollment token** → choose the site, optionally lock the
   token to the appliance **Serial**, set a TTL → **Mint**. **Copy the token now**
   (shown once). It is single-use, expiring, site-scoped and hashed at rest — it
   authorizes enrollment only and is never a login.
2. **Hotel Admin → Setup / Activation** → paste the **Enrollment code**, confirm the
   pre-filled **Serial** → **Connect**. Invalid, expired, reused or wrong-serial
   tokens are rejected with a clear message and change nothing.
3. Return to **Central → Onboarding** and **Activate** the appliance exactly as in
   section B (the token only bootstraps the connection; licensing is identical).

For fully offline sites, an **offline activation file** can be imported on the box —
see the Complete Operations Manual, "Advanced / exceptional."

---

*Vendor/support-only procedures (unix-socket enrollment, operator provisioning,
appliance wipe/removal, break-glass diagnostics) are intentionally **not** part of
this manual — see [VENDOR_BREAKGLASS_RUNBOOK.md](VENDOR_BREAKGLASS_RUNBOOK.md). They
require root, an incident reference and a reason, and are not available to Hotel-IT
operators.*
