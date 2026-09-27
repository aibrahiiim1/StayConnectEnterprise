# Appliance Onboarding Manual (UI-only)

The complete production process to bring a new site online. It uses **only** two
web consoles — no CLI, SQL, SSH, environment editing, certificate copying, or
UUIDs. Both consoles refresh themselves.

> **Normal installs are zero-touch — nothing is typed on the appliance at all.** A
> factory-clean box with internet registers itself with Central (no token) and
> waits under **Appliances** as *Waiting for activation*, where one **Activate**
> step assigns and licenses it. A site without internet uses offline activation
> (Appendix). For the full lifecycle beyond first install, see
> [STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md](STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md);
> for Central itself, [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md).

## Consoles

| Console | URL | Login |
|---------|-----|-------|
| **OneGate Central** (Control Panel) | `https://sc-central.echofusion.com` | your Central operator account |
| **Appliance Admin Console** (formerly Hotel Admin) | `https://hotel.stayconnect.local` or the appliance's management IP | your Hotel-IT operator |

The Admin Console is reachable on the **management network only** (clients are firewalled
off). If your workstation can't resolve `hotel.stayconnect.local`, use the appliance's
management IP.

---

## A. Install the appliance
1. Rack and cable the appliance: **WAN** to the site uplink, **LAN/trunk** to the
   switch carrying your guest VLANs. Power on.
2. On first boot the box generates its own identity, detects its hardware, and — if
   it has internet — **registers itself with Central automatically**, retrying until
   Central answers. There is nothing to type on the appliance. Admin Console →
   **System → Appliance & licence** shows *Waiting for activation* and the serial
   number.

## B. Central — activate the site (one step)
3. **Appliances** → the appliance is listed first as **Waiting for activation**
   (match the serial). Open it and click **Activate**:
   - **Customer** — select, or **New customer…**.
   - **Site** — select, or create it (one site = one physical location).
   - **Clients online at once** (the licensed capacity, appliance-wide).
   - **Valid for** (days, or an end date).
   - **Grace Period** (days the site keeps serving after expiry).
4. Confirming signs the assignment and issues the hardware-bound signed license in
   one transaction; the certificate is issued when the appliance asks for it. There
   is **no plan or subscription step** — the signed appliance license is the
   entitlement.

## C. Convergence (automatic)
5. The appliance **pulls** its signed assignment, certificate and signed license
   itself (Central does not push them). Central shows **Activating**, then
   **Activated**, normally within a minute.

## D. Confirm
- **Admin Console → System → Appliance & licence:** Activation **Activated** (customer
  and site shown by name), Licence **Active**, OneGate Central **Connected**.
- **Central → Appliances:** **Activated**, connection **Connected**, license
  **Active** with the capacity and validity you set.

Every action on either console shows success/failure clearly, is idempotent
(re-clicking creates no duplicate), requires password step-up where defined, and
writes audit evidence. **Check now** in the Admin Console makes the appliance contact
Central immediately.

---

## Appendix — offline activation (no internet at the site)

1. **Admin Console → System → Appliance & licence → Files from your OneGate vendor →
   Offline activation → Download activation request.**
2. **Central → Appliances → Import activation request** → the appliance appears as
   *Waiting for activation* → **Activate** it as in section B → **Activation
   package** on its page (valid 7 days).
3. **Admin Console → same place → Upload activation package.** The appliance verifies
   every signature and its hardware binding before accepting it.

There are no enrollment tokens.

---

## Appendix — moving, replacing or re-homing an appliance

- **Another site of the same customer:** Central → the appliance → **Move**. The licence follows it with
  the same terms; nothing is done on the appliance.
- **New hardware at the same site:** Central → the old appliance → **Advanced → Mark for replacement**;
  install the new appliance and activate it for the same customer and site. The old one retires itself once it
  acknowledges its retirement.
- **Another customer:** not a move. **Retire** it in Central, factory-reset it on site
  ([DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md](DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md)), then activate it
  for the new customer when it shows *Waiting for activation* again.
- If Admin Console shows **Removed from OneGate Central**, the appliance's record was deleted in Central after
  it had served a customer; only a factory-clean install and a new activation bring it back.

---

*Vendor/support-only procedures (operator provisioning, appliance wipe/removal,
break-glass diagnostics) are intentionally **not** part of this manual — see
[VENDOR_BREAKGLASS_RUNBOOK.md](VENDOR_BREAKGLASS_RUNBOOK.md). They require root, an
incident reference and a reason, and are not available to Hotel-IT operators.*
