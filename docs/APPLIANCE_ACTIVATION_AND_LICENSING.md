# Appliance activation and licensing — operator guide

There are exactly **two** ways to activate a StayConnect appliance: **Online** and **Offline**. Everything
else — registration, signed assignment, certificates, mTLS, convergence — happens by itself; its details sit
under the collapsed **Technical details** on Hotel Admin's **System → Appliance & licence** page and under
**Advanced** on the appliance's page in Central. You do not drive it. The Central side is specified in
[CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md).

---

## Online — the normal path

Nothing is typed on the appliance. A factory-clean appliance with a route to the control panel registers
itself — token-less, signed with its own identity key — and keeps retrying (30 s, backing off to 5 minutes)
until Central answers.

**On the appliance (Hotel Admin → System → Appliance & licence):** it shows *Waiting for activation* and the
**serial number**. That is all. (*Not registered yet* means Central has not answered yet; **Check now**
retries immediately.)

**In the control panel (Appliances):**

1. The appliance is listed first as **Waiting for activation**. Open it and check the **serial** (and, under
   **Advanced**, the WAN MAC) against the box in front of you before continuing.
2. Press **Activate**. **Customer** — pick an existing one or create it.
3. **Site** — pick an existing one or create it. This is the site the appliance is bound to.
4. **Guests online at once** — the licensed ceiling. `0` is unlimited. It is **appliance-wide across all guest
   VLANs**, and it is enforced per appliance, so this is the real limit for this box.
5. **Valid for** — a number of days, or an end date.
6. **Grace period (days)** — after the end date guests keep working, with warnings.
7. Confirm (re-enter your password if asked).

Central shows **Activating** until the appliance has collected its assignment, certificate and licence
(normally within a minute), then **Activated**. Hotel Admin follows on its own: *Finishing activation…*, then
**Activated** with your customer and site, licence **Active**.

There are no enrollment tokens.

---

## Offline

### Licence renewal — two steps

For an appliance that is **already activated**:

1. **Control panel → the appliance's page → License** — **Renew or change** to issue the new version, then
   **Offline license file**. You get one signed file.
2. **Hotel Admin → System → Appliance & licence → Files from your OneGate vendor → Upload licence file.**

The appliance checks the vendor signature, that the file is bound to *this* appliance, that it has not
expired, and that it has not been used before. A licence **older** than the one installed is refused, so a
renewal cannot roll you backwards. A file for another appliance is refused. A file used twice is refused. In
every rejection nothing on the appliance changes.

The file carries no private key. The appliance proves it is the intended recipient using a key that never
leaves it.

### Before the first offline activation — pin the vendor trust key

An appliance may believe an activation package only because it is signed by a key it **already trusts**,
pinned before the package arrived. A trust root cannot travel with the thing it authorises: an appliance that
learned its verification key from the package would accept any package that brought its own key.

```
deploy/scripts/install-vendor-trust-key.sh <vendor-license.pub>
deploy/scripts/install-vendor-trust-key.sh --show      # print the installed fingerprint
```

The **public** key goes on every appliance and is not secret. The **private** signing key stays on Central
and never touches an appliance — anyone holding it can mint a package for any appliance in the fleet. The
script refuses a 64-byte private key outright, because copying the wrong file while doing exactly this is the
most likely way to leak one.

Deliver it by any channel whose **integrity** you can verify — in the appliance image, on the installer's
USB stick, or over a channel you already trust; it needs no confidentiality. The normal route is
`deploy/pki/vendor-license.pub`: provisioning pins whatever is in that slot automatically, so no installer
has to remember a step. Then compare the fingerprint the script prints against the one published by the
holder of the private key, **out of band**. That comparison is the whole security of the offline path.

Both short forms of the fingerprint are always shown — `base64url  (licence key_id hex)` — because activation
packages name their signer in one encoding and signed licences name theirs in the other. They are the same
eight bytes, and an operator holding one string and reading the other has no way to tell a matching key from
a different one.

Replacing an already-installed key needs `--force` and stops every package signed by the old one from
verifying. Until a key is pinned, offline first activation refuses every package and says so. Online
activation is unaffected.

### The other half: the vendor signing key on Central

There is exactly **one** vendor keypair for the product, and it has to outlive the server it was made on.

```
vendor-signing-key.sh init                  the first Central host, once, ever
vendor-signing-key.sh backup <file.enc>     encrypted escrow — do this immediately
vendor-signing-key.sh restore <file.enc>    a replacement or migrated Central host
vendor-signing-key.sh export-public <dir>   the 32 bytes appliances pin
vendor-signing-key.sh rotate --force        deliberate replacement of the fleet's trust root
```

The dangerous case is not losing the key, it is **quietly replacing** it. A rebuilt Central that generated a
"missing" key would look like a successful deployment and would have invalidated every appliance in the
field at once: their pinned public key no longer matches anything, so every licence and every activation
package is refused. So `init` refuses to run when a key exists, `restore` refuses to overwrite a *different*
key, and replacement is only ever `rotate --force`, typed on purpose — after which every appliance must be
re-pinned by hand before it will accept anything. The previous key is kept, because appliances still pinned
to it exist until someone visits them.

`backup` refuses to write a plaintext copy. An escrow copy travels — to a safe, to another site, onto
removable media — and a plaintext one is just the signing key lying somewhere nobody is watching.

`central-preflight.sh` checks all of this on a host before anyone drives to a hotel.

### First activation — three steps

For an appliance that has **never** been activated and has no route to the control panel.

1. **Hotel Admin → System → Appliance & licence → Files from your OneGate vendor → Offline activation →
   Download activation request.**
   The appliance writes a request describing itself: serial, hardware evidence and the **public** half of
   its identity keypair. The private half never leaves the appliance. The request is signed with it, which is
   what proves the box asking is the box that holds the key.
2. **Control panel → Appliances → Import activation request.**
   This registers the appliance as **Waiting for activation** and nothing more. It carries no authority over
   customer or site. Open it, press **Activate** and choose **Customer**, **Site** and licence terms exactly as
   you would online — then press **Activation package** on its page to download one file.
3. **Hotel Admin → same place → Upload activation package.** Done.

The package carries everything first activation needs: the **signed assignment** (the only authority for
tenant and site), the **trust material**, and the **signed licence**. It is bound to that exact request and
that exact identity key, single-use and expiring.

**What is refused, with nothing changed on the appliance:** a package for another appliance, one that answers
a different request, a replayed or stale nonce, a serial mismatch, an expired file, a file edited after
signing, one signed by anything but the vendor key, one carrying no signed assignment, and any package
offered to an appliance that is *already* assigned — that is a lifecycle action with its own authority, not
something a USB stick may do.

Applying is all-or-nothing, including through a power cut. The package is journalled and fsynced before a
single durable change, and the journal is removed only once everything has landed. On the next boot an
appliance that finds a journal either finishes the activation or rolls it back to unassigned — it decides by
inspecting what actually landed, not by remembering how far it got. A partly-activated appliance, assigned
but unlicensed or licensed with no identity, never survives a restart; that state looks finished and is the
one outcome worth engineering against.

Within a normal run the same holds: single-use is claimed first, so a package cannot be reused even if a
later step fails; the assignment is rolled back if the trust material or licence is refused; and the identity
is adopted **last**. A rolled-back activation does not release its package — it was spent, and reusing it
would be the replay the ledger exists to prevent, so generate a fresh one.

## What each state means

Hotel Admin's one status card ([CENTRAL_CONTROL_PLANE.md §8](CENTRAL_CONTROL_PLANE.md#8-hotel-admin--central)):

| Hotel Admin shows | Meaning |
|---|---|
| Activation: Not registered yet | Central has not answered a registration yet; the appliance keeps retrying |
| Activation: Waiting for activation | Central can see it; an operator must press Activate |
| Activation: Finishing activation… | Activated in Central; collecting assignment, certificate and licence |
| Activation: Activated | Assignment adopted and verified; bound to *customer · site* |
| Activation: Retired | Retired in Central; credentials revoked |
| Licence: Grace period | Ended, still serving, with warnings |
| Licence: Expired / Suspended / Revoked / Wrong appliance | No new guest sessions |
| OneGate Central: Connected / Temporarily unreachable / Not configured | Link to Central only; guests are unaffected by it |

Tenant and site are **never** typed into the appliance. They arrive only through the signed assignment, and
every daemon takes them from the verified assignment — there is no environment-variable fallback in a
production build. An appliance with no assignment shows empty tenant-scoped screens (Guest accounts, Portal
branding) and says so — that is the correct state, not a fault.

The licence's binding to this appliance (identity key, appliance id, serial, hardware fingerprint, WAN MAC) is
checked on every evaluation — every minute and at boot — not only when a licence is installed.

**DHCP shows `waiting`, not `failed`, until guest networking exists.** Kea binds the LAN bridge, which does
not exist until an operator applies and confirms a guest network, so on a new appliance it is deliberately
stopped. Health reports that as *waiting* with the reason, and boot convergence treats it as satisfied —
otherwise every factory-clean appliance would report itself broken and permanently unconverged, burying any
real convergence failure underneath a false one. The moment a guest network is confirmed, Kea is checked
normally and a genuine failure is reported as a failure. Nothing starts Kea early to make the screen green.

---

## How the appliance reaches Central

**`https://sc-central.echofusion.com` — a name, not an address.** It is defined once, in
`deploy/config/central-endpoint.env`, which both appliance provisioning and Central read. Moving Central is
then a DNS change and nothing else: no appliance is edited, because no appliance ever learned an IP. Only if
the *name* changes does anything else happen, and that is a deliberate fleet migration.

Provisioning refuses an IP, a plain `http://` URL, a bare hostname and a URL with a path — each of those
would work on the day it was typed and fail later, at a site, in a way nobody would connect to this decision.
An `/etc/hosts` entry is a fine stopgap while DNS propagates, and provisioning names it as one; it is not the
product's dependency, because it exists on exactly one machine.

There is deliberately **no fallback value in code**. A plausible-looking default would point appliances at
DNS that may not exist, and nobody would discover it until a site was already installed. With
`CTRLAPI_APPLIANCE_BASE` unset, Central logs that offline activation is disabled and why, rather than minting
packages that name an endpoint nobody operates. Activation packages carry the endpoint, so an offline
appliance learns where to reconcile without anyone typing an address into it.

**Always outbound, always appliance-initiated.** The appliance dials Central over the WAN. Central never
dials in: no port-forward, no inbound rule, no route into the hotel LAN. Nothing about a hotel's network has
to be exposed for the fleet to work.

**The appliance API is a separate surface from the human admin UI.** They can be secured independently — the
admin UI can later require MFA, VPN or Zero-Trust without any of that becoming a runtime dependency of a
hotel's connectivity.

**Local-first.** Once activated with a valid signed licence, losing Central changes nothing that a guest or
the hotel can see: Hotel Admin, guest networking, local authentication, PMS operation and existing sessions
all continue. The licence carries an offline grace period, and the assignment's expiry is a refresh horizon,
not a kill switch.

## Control panel sign-in

Sign-in is email and password only. There is no single sign-on.
