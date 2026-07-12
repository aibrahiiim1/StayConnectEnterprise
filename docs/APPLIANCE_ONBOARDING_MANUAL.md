# Appliance Onboarding Manual (UI-only)

The complete production process to bring a new hotel online. It uses **only** two
web consoles — no CLI, SQL, SSH, environment editing, certificate copying, or
UUIDs. Every screen refreshes itself; you never have to wait-and-press-Refresh.

Nothing is ever typed on the appliance except the **Enrollment Token**.

## Consoles

| Console | URL | Login |
|---------|-----|-------|
| **Central Platform** | `https://150.0.0.252` | your platform operator |
| **Appliance Hotel Admin** | `https://hotel.stayconnect.local` or `https://172.21.60.23` | your Hotel-IT operator |

The Hotel Admin is reachable on the **management network only** (guests are firewalled
off). If your workstation can't resolve `hotel.stayconnect.local`, use the management
IP `https://172.21.60.23`.

---

## A. Central Platform — prepare the hotel
1. **Customers** → create the customer (tenant).
2. **Commercial plan / Subscription** → create or activate the subscription.
3. **Sites** → create the site under that customer.
4. **Appliances → Enrollment token** → choose the site, optionally lock the token to
   the appliance **serial**, set a TTL → **Mint**. **Copy the token now** (shown once).
   The token is single-use, expiring, site-scoped and hashed at rest — it authorizes
   enrollment only and is never a login.

## B. Appliance Hotel Admin — enroll
5. Open the Hotel Admin → **Networking → Setup / Enrollment**.
6. Paste the **Enrollment Token**, confirm the pre-filled **Serial**, → **Submit
   enrollment**. The page shows the live **Onboarding progress** (15 stages) and
   advances on its own — no manual refresh. Invalid, expired, reused or wrong-serial
   tokens are rejected with a clear message and change nothing.

## C. Central Platform — claim, assign, certificate
7. **Onboarding** → the appliance appears under **Pending** automatically. **Claim** it.
8. Choose the **Customer** and **Site** → **Assign**. The signed assignment is issued
   and the appliance adopts it on its own.
9. **Issue certificate** → mutual-TLS (API) and NATS transport come up. The status
   updates automatically on both consoles.

## D. Central Platform — license
10. **Licenses** → **Issue license** for the site. The appliance fetches the signed
    license and its state becomes **Active**.

## E. Confirm (both update live)
- **Hotel Admin → Setup / Enrollment:** stages reach **Setup complete** — identity,
  API mTLS, NATS mTLS all green, License Active, assigned Customer/Site shown by name.
- **Central → Fleet:** the appliance is **online**, licensed, heartbeat current.

Every action on either console refreshes the displayed state automatically, shows
success/failure clearly, is idempotent (re-clicking creates no duplicate), requires
password step-up where already defined, and writes audit evidence. The manual
**Refresh** buttons remain only as an optional diagnostic.

---

*Vendor/support-only procedures (unix-socket enrollment, operator provisioning,
appliance wipe/removal, break-glass diagnostics) are intentionally **not** part of
this manual — see `docs/VENDOR_BREAKGLASS_RUNBOOK.md`. They require root, an incident
reference and a reason, and are not available to Hotel-IT operators.*
