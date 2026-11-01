# onegate.work DNS hosting and portal certificates — operator runbook

> **RETIRED, 2026-11-01.** `onegate.work` is no longer used by OneGate and must never be used again. This runbook is kept only as history. Current procedure: [ONEGATE_PORTAL_DNS_AND_CERTIFICATES.md](ONEGATE_PORTAL_DNS_AND_CERTIFICATES.md).

Who: the OneGate platform owner (holds the GoDaddy and Cloudflare logins). Contract:
[`docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md`](../architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md).

The domain stays registered at GoDaddy. Only its **DNS hosting** moves to Cloudflare (free plan), because
Cloudflare API tokens can be limited to this one zone and to Central's address, and GoDaddy's cannot.
**Never paste the token into a chat, ticket or email.** It is entered once, directly into the Central console.

## 1. Before you start — what the zone holds today

Read from GoDaddy's nameservers on 2026-10-13: `onegate.work A 3.33.130.190` and `A 15.197.148.33` (GoDaddy's
parking/forwarding service) and `www.onegate.work CNAME onegate.work`. No MX, TXT or CAA. Nothing else depends
on the zone.

## 2. Move DNS hosting to Cloudflare (about 15 minutes, then propagation)

1. Sign in at https://dash.cloudflare.com (create a free account with the company mailbox if none exists).
2. **Add a domain** → `onegate.work` → plan **Free**. Cloudflare scans and imports the existing records.
3. Check the imported list matches §1. If the parking records are missing, add them exactly as above.
   Set every imported record to **DNS only** (grey cloud) — nothing here should be proxied.
4. Cloudflare shows two nameservers (`<name>.ns.cloudflare.com`). Write them down.
5. In GoDaddy → My Products → `onegate.work` → **DNS** → **Nameservers** → **Change** → "I'll use my own
   nameservers" → enter the two Cloudflare nameservers → Save. Keep **Domain lock ON**. Do not change anything
   else (registration, contacts, transfer).
6. Wait until Cloudflare shows the zone **Active** (usually under an hour, at most 24 h).
7. In Cloudflare → **DNS → Settings → DNSSEC → Enable**, then copy the DS record into GoDaddy → `onegate.work`
   → DNS → **DNSSEC** → Add (Cloudflare shows the exact fields). Optional but recommended.

**Rollback:** in GoDaddy set nameservers back to **Default** (`ns09.domaincontrol.com`,
`ns10.domaincontrol.com`). GoDaddy's copy of the zone is untouched by this procedure, so it answers again as
before. Disable DNSSEC at GoDaddy first if you enabled it.

## 3. Create the token Central will use

Cloudflare → **My Profile → API Tokens → Create Token → Create Custom Token**:

| Field | Value |
|---|---|
| Name | `OneGate Central — onegate.work DNS` |
| Permissions | `Zone` · `DNS` · `Edit` and `Zone` · `Zone` · `Read` |
| Zone Resources | `Include` · `Specific zone` · `onegate.work` |
| Client IP Address Filtering | `Is in` · `154.239.10.198` (Central's outbound address, measured 2026-10-13) |
| TTL | leave empty, or set an end date and diary its renewal |

Create it, and **keep the page open**: the token is shown once.

## 4. Enter it into Central (write-only)

1. Open the Central console → **Portal certificates**.
2. **DNS token** → paste → Save. The console only ever shows "Set"; the token is stored root-only on Central at
   `/etc/stayconnect/portal-tls/dns-api.token` and is never displayed, logged or sent to an appliance.
3. Fill **Contact email** (Let's Encrypt expiry notices) and leave **ACME directory = staging**.
4. Press **Test DNS access**: it must show the zone id and the two Cloudflare nameservers, and register the
   OneGate ACME account (staging).
5. Close the Cloudflare token page.

## 5. Prove issuance on staging, then switch to production

1. On the PRE-LIVE appliance's Admin Console → **Network → Portal HTTPS**, the OneGate hostname appears within
   five minutes and DNS shows **Published**. Central obtains a **staging** certificate (Central → Portal
   certificates shows it ISSUED), but the appliance **does not install it**: staging certificates chain to Let's
   Encrypt's staging roots, which no device or trust store accepts, and the appliance verifies every chain against
   the system roots. It reports "a Let's Encrypt STAGING certificate … not installed (expected until Central
   switches to the production directory)" and stays on the HTTP fallback. That is the correct result of this step:
   the staging issuance proves the token, the zone, the DNS record, DNS-01 and the ACME account. (Measured on
   2026-10-14: the staging chain verifies against the published staging root *(STAGING) Pretend Pear X1* and fails
   against the system store.)
2. In Central → **Portal certificates**, set **ACME directory = production** → Save → **Test DNS access** again
   (registers the production account) → **Apply CAA** (restricts `onegate.work` to OneGate's Let's Encrypt
   account and forbids wildcard certificates).
3. Nothing else is needed: switching the directory marks every staging certificate due for renewal, so within about
   five minutes the appliance orders a production certificate with a new key, installs it, and the portal turns
   **HTTPS active**. (**Renew now** on the appliance does the same at once.)
4. Optional check, from any machine: `curl -v https://<hostname>/` fails to connect from outside (the portal
   address is private) — use a phone on the guest Wi-Fi, see
   `docs/acceptance/HTTPS-Portal-Real-Device-Acceptance-Checklist.md`.

## 6. Revoking the token

Cloudflare → API Tokens → Roll or Delete. Central then reports DNS errors and keeps every installed certificate
working until it expires (the appliance falls back to HTTP only when its certificate expires). Enter a new
token in the Central console to recover.
