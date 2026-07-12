# Appliance Onboarding — Self-Service Manual

How to register and connect an appliance from scratch, using the Central Control
Center and the appliance's Hotel Admin. Nothing is ever hand-edited on the box: a
factory unit has no customer identity and adopts its tenant/site only from the
vendor-signed assignment you issue after claiming it.

## Access points

| Console | URL | Login |
|---------|-----|-------|
| **Central Control Center** (cloud-admin) | `https://150.0.0.252` | `admin@stayconnect.local` / `ProofAdmin2026!` (change this) |
| **Appliance Hotel Admin** | `https://hotel.stayconnect.local` | your local Hotel-IT operator |

`hotel.stayconnect.local` is a Caddy vhost on the appliance (it fronts the Hotel
Admin UI on `:3100`, which proxies its own `/api/edge/*` calls to `edged` on
`:8090`). If your workstation can't resolve it, add a hosts entry pointing at the
appliance's management IP, e.g. `172.21.60.23  hotel.stayconnect.local`, or use the
appliance's DNS. (Note: `admin.stayconnect.local` is a *different*, legacy vhost and
is not the Hotel Admin.)

Reference values for this environment: serial **APP-DEV-0001**, tenant **Harborview
Hotels Group**, site **Harborview Marina Hotel**.

---

## Step 1 — Mint an enrollment token (Control Center)

1. **Appliances** in the left nav.
2. Click **Enrollment token**.
3. Pick the **Site** (Harborview Marina Hotel). Optionally set **Serial** to
   `APP-DEV-0001` to lock the token to exactly this unit, and a **TTL** (default 24h).
4. **Mint token** → the full token is shown **once**. Copy it now.

## Step 2 — Enroll the appliance (Hotel Admin, on the box)

1. Open `https://hotel.stayconnect.local` and go to **Setup → Enrollment**.
2. Step 3 "Enrollment": paste the token, confirm the **Serial** (pre-filled with
   `APP-DEV-0001`), and click **Enroll**.
3. The appliance generates its Ed25519 identity, enrols with Central, and restarts
   its cloud agent. Step 1 "Appliance identity" now shows a fingerprint.

> CLI alternative (root on the box), if you prefer not to use the wizard:
> ```
> curl --unix-socket /run/stayconnect/scd.sock -X POST \
>   -H 'Content-Type: application/json' \
>   -d '{"token":"<paste-token>","serial":"APP-DEV-0001"}' \
>   http://localhost/v1/setup/enroll
> ```

## Step 3 — Claim & assign (Control Center)

1. **Onboarding** in the left nav. The box appears under **Pending appliances**
   (serial + identity-key fingerprint). Click **Refresh** if not yet visible.
2. **Claim** it.
3. In the same row, choose **Customer** = Harborview Hotels Group and **Site** =
   Harborview Marina Hotel, then **Assign**. This mints a vendor-signed assignment
   bound to this appliance's id + identity key; the box fetches and adopts it on its
   own (tenant/site are never typed on the box).

## Step 4 — Issue the client certificate (Control Center)

Still on **Onboarding**, under **Registered appliances**, click **Issue
certificate** for the box. This is what brings up mutual-TLS: the appliance's cloud
API (`:9443`) and NATS (`:4223`) channels come online.

## Step 5 — Issue a license (Control Center)

1. **Licenses** in the left nav.
2. **Issue license** → pick the site (Harborview Marina Hotel), set **Valid days**
   (e.g. 365) and **Offline grace days** (e.g. 30) → **Issue**.
3. The appliance fetches the signed license and its state goes **Active**.

## Step 6 — Verify it's fully connected

**On the appliance (Hotel Admin → Setup):** every step should be green —
- Step 1 Appliance identity: fingerprint present
- Step 2 checks: **API mTLS :9443 pass**, **NATS mTLS :4223 pass**
- License: **Active**
- Banner: setup **complete**

**In the Control Center:**
- **Fleet**: the appliance shows **online** with an **active** license.
- **Dashboard → Fleet License Summary**: **Active** count includes it.
- **Appliances**: status **online** with a live green pulse.

---

## Provisioning the Hotel Admin login (per appliance)

The Hotel Admin login is a local operator in the appliance's site database. Seed or
reset it with one command on the appliance (root) — run it at provisioning / first
boot for a consistent login across your fleet:

```
/opt/stayconnect/bin/edged seed-admin --email <user> --password <pass> --name "Hotel Admin"
```

Passwords must be ≥10 chars by default. To set a deliberately weak one (e.g.
`admin`/`admin`) on a management-network-only box, add `--allow-weak`:

```
/opt/stayconnect/bin/edged seed-admin --email admin --password admin --allow-weak
```

This is a per-appliance provisioning choice, not a compiled-in credential — do NOT
use a weak/shared password on production or guest-reachable deployments.

## Removing an appliance completely (for reference)

Two parts — the control panel record and the physical box:

1. **Control panel:** Appliances → **Delete** on the row (DELETE
   `/v1/appliances/{id}`). This cascades its certificate, signed assignment,
   lifecycle events, terminal-delivery and telemetry. Revoke/remove its license
   separately under **Licenses** if you want the fleet counts clean.
2. **The box (root):** factory-reset so it can re-enrol — wipe identity and
   credentials, keep the manufacture trust anchors and network config:
   ```
   systemctl stop stayconnect-scd stayconnect-edged
   shred -u /etc/stayconnect/identity/ed25519.key
   rm -f  /etc/stayconnect/identity/identity.json
   shred -u /etc/stayconnect/certs/mtls-client.key
   rm -f  /etc/stayconnect/certs/client.crt
   rm -f  /etc/stayconnect/assignment/assignment.json /etc/stayconnect/assignment/registry.json
   # PRESERVE: /etc/stayconnect/assignment-registry-root.pub and /etc/stayconnect/certs/ca.crt
   systemctl start stayconnect-scd
   ```
   `GET /v1/setup/status` should then report `enrolled=false` and the Hotel Admin
   enrollment form unlocks.
