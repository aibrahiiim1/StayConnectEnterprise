# OneGate portal names and certificates under getsemantics.com — operator runbook

Current since 2026-11-01. **`onegate.work` is retired and is never used again** (Product Owner); its former runbook,
[ONEGATE_WORK_DNS_AND_CERTIFICATES.md](ONEGATE_WORK_DNS_AND_CERTIFICATES.md), is kept only as history. Design:
[contract](../architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md) §2, §3 and §16.

## 1. The names

| Name | What it is | Who writes it |
|---|---|---|
| `central.onegate.getsemantics.com` | OneGate Central only | Product Owner (existing) |
| `portal.onegate.getsemantics.com` | the shared Client Portal name of every appliance, `A 10.255.255.1` | Product Owner (done 2026-11-01) |
| `<label>.gw.onegate.getsemantics.com` | each appliance's permanent technical hostname, `A <its portal address>` | Central |
| `_acme-challenge.<name>` under `onegate.getsemantics.com` | temporary DNS-01 TXT values | Central |
| `<16 chars>.acme.onegate.getsemantics.com` | a portal domain's challenge delegate (temporary TXT) | Central |
| `onegate.getsemantics.com CAA` | `0 issue "letsencrypt.org; accounturi=…"`, `0 issuewild ";"` | Central (**Apply CAA**, an explicit action) |

The Cloudflare zone is `getsemantics.com`. Central's name base is `onegate.getsemantics.com`; Central finds the
enclosing zone itself and **refuses to write or delete any record outside the base** (a token for the zone could
otherwise edit every record of the company's domain). CAA goes on the base, never on the `getsemantics.com` apex.
The base's CAA also covers `central.onegate.getsemantics.com`; Central's own certificate comes from its internal CA,
which CAA does not govern.

**No CNAME at `_acme-challenge.portal.onegate.getsemantics.com`.** Central writes that name's TXT values itself. A
CNAME there (for instance the `sharedportal.acme.onegate.work` delegation an earlier layout described) would capture
the lookups; Central reports the shared name *not ready* until it is deleted. None exists today (checked 2026-11-01).

## 2. Create the token Central uses

Cloudflare → **My Profile → API Tokens → Create Token → Create Custom Token**:

| Field | Value |
|---|---|
| Name | `OneGate Central — portal names under onegate.getsemantics.com` |
| Permissions | `Zone` · `DNS` · `Edit` and `Zone` · `Zone` · `Read` |
| Zone Resources | `Include` · `Specific zone` · `getsemantics.com` |
| Client IP Address Filtering | `Is in` · `154.239.10.198` (Central's outbound address, measured 2026-11-01) |
| TTL | leave empty, or set an end date and diary its renewal |

Create it and keep the page open: the token is shown once.

## 3. Enter it into Central (write-only)

1. Central console → **Portal certificates**. The zone shows `onegate.getsemantics.com` (migration 0064).
2. **DNS token** → paste → Save. It is stored root-only at `/etc/stayconnect/portal-tls/dns-api.token`, never
   displayed, logged or sent to an appliance.
3. **Contact email** (Let's Encrypt expiry notices); leave **ACME directory = staging**.
4. **Test DNS access**: it shows the zone `getsemantics.com`, its two Cloudflare nameservers, and registers the
   staging ACME account.

## 4. Staging, then production

1. Within five minutes each appliance's hostname is published (`<label>.gw.onegate.getsemantics.com`, DNS
   **Published**), and Central issues **staging** certificates for it and for the shared name. The appliance does
   not install them (staging roots are trusted by no device) and says so: this step proves the token, DNS-01 and
   the account.
2. Central → **Portal certificates** → **ACME directory = production** → Save → **Test DNS access** (registers the
   production account) → **Apply CAA**.
3. The appliance orders production certificates with new keys within about five minutes, installs them, and the
   portal turns **HTTPS active** on `https://portal.onegate.getsemantics.com/`; DHCP option 114 then carries
   `https://portal.onegate.getsemantics.com/captive-portal/api`.

## 5. Revoking the token

Cloudflare → API Tokens → Roll or Delete. Certificates already issued keep working until they expire; renewals fail
until a new token is entered (the console says why).
