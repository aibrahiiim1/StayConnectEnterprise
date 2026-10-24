# OneGate — HTTPS Client Portal and Customer-Owned Social Login (contract)

Status: **authoritative design contract** for the HTTPS portal delivery (branch `delivery/https-portal-social`).
It records the Product-Owner decisions this delivery implements and the technical decisions taken within that
authorisation. Where this document and code disagree, the code is wrong.

> **Terminology (D71, 2026-10-24).** OneGate serves hotels, offices and campuses, so where this contract says
> *hotel domain* or *hotel portal domain* the screens say **portal domain** (appliance) and **custom domain**
> (Central), and the examples on `nilehotel.com` are illustrations only. The identifiers `custom_domain` and
> `needs_hotel_domain` are unchanged. Nothing else in this contract changes.

## 1. Product-Owner decisions this contract implements (not re-opened here)

1. Central manages licensing, activation, **appliance portal hostnames**, and the **HTTPS certificate lifecycle**
   (issuance, renewal, monitoring, revocation). Central never carries guest traffic or guest data.
2. Every appliance has its **own hostname, its own publicly trusted certificate and its own private key**,
   generated on the appliance and never leaving it.
3. Client Portal authentication, sessions, entitlements and guest traffic stay **local to the appliance**.
4. Each hotel **owns** its Google, Apple, Microsoft and Facebook applications (registration, payment, secrets).
   OneGate never owns or operates them.
5. The Admin Console gives the hotel **self-service** configuration of provider credentials, callback URLs and
   enable/disable.
6. **HTTP fallback**: when HTTPS is unavailable the existing HTTP portal keeps manual code entry working; emails
   then carry the **code only** (no link); administrators are alerted; HTTPS recovers automatically.

## 2. Names and addresses

| Thing | Value | Owner |
|---|---|---|
| Zone | `onegate.work` (registrar GoDaddy; DNS hosted at Cloudflare) | OneGate |
| Appliance hostname | `<label>.gw.onegate.work`, label = 8 chars `[a-z][a-z0-9]{7}`, random, permanent, never reused | Central |
| Its public A record | `<label>.gw.onegate.work A <portal address>` (TTL 300, DNS-only, never proxied, no AAAA) | Central |
| ACME challenge (OneGate name) | `_acme-challenge.<label>.gw.onegate.work TXT` (temporary) | Central |
| ACME challenge delegate (hotel domain) | `<challenge_label>.acme.onegate.work TXT` (temporary), challenge_label = 16 `[a-z0-9]` | Central |
| CAA | `onegate.work CAA 0 issue "letsencrypt.org; accounturi=<OneGate ACME account URL>"`, `0 issuewild ";"` | Central (explicit action) |
| Hotel portal domain | e.g. `wifi.nilehotel.com` (a subdomain of a domain the hotel owns; never under onegate.work, never an apex) | Hotel |

**HSTS is never set** on any portal name: it would make the approved HTTP fallback unreachable.

### 2.1 The portal address (replaces `192.0.2.1`)

RFC 5737 §4 says the documentation blocks "are not for local use". The portal therefore uses a **private
(RFC 1918) /32** that exists only on the appliance:

* default chosen once by migration 0116 from `10.255.255.1`, `10.254.254.1`, `10.253.253.1`, `10.252.252.1`
  (first one no guest network overlaps); never from 172.16.0.0/12 (Docker's pools) or 192.168.0.0/16;
* configured on a dummy interface `ogportal0` by netd; answered only from guest interfaces, on TCP 443 (TLS,
  redirected to portald :8343) and TCP 80 (redirected to portald :8380); never routed, never NATed, never
  accepted on the WAN or management interface;
* a site setting (`public.portal_https_settings.portal_ip`, audited in `portal_https_settings_changes`), changed
  from the Admin Console; a change is a **network change** applied through netd's validate/apply/confirm;
* the validator refuses an address inside any guest subnet, the WAN/management interface subnet, or any subnet
  present on the appliance (including Docker bridges).

Why one fixed address and not each network's gateway: the hostname must resolve identically from public DNS
(guests' own resolvers, Private DNS, the hotel's DNS in external-DNS mode), and public DNS gives one answer.

## 3. Certificate lifecycle

```
appliance (scd)                                  Central (ctrlapi)                      Let's Encrypt / Cloudflare
  POST /v1/appliance/portal-tls/sync  ------->   allocate hostname (once), upsert A record ---> Cloudflare
  <-------- hostname, dns state, custom domain, per-hostname certificate status (renew_after, revoked)
  generate NEW P-256 key + CSR (per hostname)
  POST /v1/appliance/portal-tls/orders --------> validate (names, rate) -> QUEUED
                                                 worker: ACME order, DNS-01 TXT ---------------> LE validates
                                                 finalize with the appliance CSR -> chain; ARI renew_after
  GET  /v1/appliance/portal-tls/orders/{id} ---> ISSUED + chain
  verify chain (host, key match, validity) -> install key 0640 + fullchain atomically -> portald reloads by SNI
```

* **Keys**: ECDSA P-256, generated by scd, written `/etc/stayconnect/portal-tls/<hostname>.key` (0640
  root:stayconnect-portald); fullchain `<hostname>.crt` (0644). A pending key is `<hostname>.key.pending` until
  its certificate installs. **Every renewal uses a new key.**
* **Renewal**: Central computes `renew_after` from ACME Renewal Information (ARI, RFC 9773) when the CA offers
  it, otherwise at two thirds of the lifetime; it re-checks ARI daily. The appliance orders a new certificate
  once `now > renew_after`, or immediately when its certificate is missing, expired, revoked or does not match.
* **Limits**: Central allows at most one open order per (appliance, hostname), 10 orders per appliance per day,
  and at most `weekly_new_limit` (default 45 < Let's Encrypt's 50) *new* `gw.onegate.work` certificates per
  rolling 7 days; further new orders wait QUEUED. A renewal (same hostname previously issued) is not new.
  Hotel-domain certificates count against the hotel's own registered domain, not `onegate.work`.
* **Validation by Central before ordering**: the CSR is a valid P-256 or RSA≥2048 CSR, self-signed, naming
  exactly one DNS name, which is the appliance's own OneGate hostname or its VERIFIED hotel domain.
* **Revocation**: on appliance decommission/revoke/delete (the central lifecycle path), on hotel-domain removal,
  and by a platform operator from the Central console. Revocation also deletes the DNS A record when the
  appliance is retired.
* **Secrets on Central**: `/etc/stayconnect/portal-tls/acme-account.key` (created on first use) and
  `/etc/stayconnect/portal-tls/dns-api.token` (entered write-only from the Central console; never displayed,
  logged or returned). Both root 0600. Nothing of either ever reaches an appliance.
* **Staging first**: `portal_tls_settings.acme_directory` starts at `staging`; a platform operator switches to
  `production` from the console after a staging issuance has been proven.

### 3.1 Hotel portal domain (delegated issuance)

The hotel enters `wifi.nilehotel.com` in the Admin Console. Central returns three records the hotel publishes:

| Record | Purpose |
|---|---|
| `_onegate-verify.wifi.nilehotel.com TXT <verify_token>` | proves the claimant controls the domain and binds it to this appliance |
| `wifi.nilehotel.com CNAME <label>.gw.onegate.work.` | the name follows the portal address |
| `_acme-challenge.wifi.nilehotel.com CNAME <challenge_label>.acme.onegate.work.` | lets Central answer DNS-01 for this one name |

Central verifies all three through two independent public resolvers (DNS-over-HTTPS: Cloudflare and Google),
checks CAA on the name and its parents permits `letsencrypt.org`, refuses names under `onegate.work`, apex
names, public suffixes, and a name already claimed by another appliance. It re-checks before every order and
daily; a broken delegation marks the domain FAILED, stops renewals and alerts. Removal revokes its certificate.

## 4. Canonical hostname and the HTTP fallback

scd computes, every cycle:

* `canonical` = the hotel domain if VERIFIED and its certificate is installed and valid; else the OneGate
  hostname if its certificate is installed and valid; else none.
* `https_active` = canonical exists, the portal address is up on `ogportal0`, and the canonical certificate is
  valid for at least 5 more minutes.
* otherwise `fallback_reason` ∈ `NOT_CONFIGURED` (Central has no DNS/ACME configuration yet), `NO_HOSTNAME`,
  `NO_CERTIFICATE`, `CERTIFICATE_EXPIRED`, `PORTAL_ADDRESS_NOT_ACTIVE`, `CENTRAL_UNREACHABLE`.

State is persisted in `public.portal_tls_state` and served to portald by scd (`GET /v1/portal/tls-state` on the
local socket).

| | HTTPS active | Fallback |
|---|---|---|
| DHCP option 114 (local DHCP) | `https://<canonical>/captive-portal/api` | today's `http://<gateway>:8380/` |
| OS probe on port 80 | 302 → `https://<canonical>/` | today's behaviour |
| Any other HTTP portal page | 302 → same path on `https://<canonical>` | served over HTTP |
| Email/SMS code message | code + link `https://<canonical>/connect?...` | **code only** |
| Social sign-in | offered per §5 | hidden (providers accept HTTPS callbacks only) |
| Admin Console | HTTPS card green | red banner on the Portal HTTPS page and a health check `portal_https` |

Requests over TLS for a portal hostname that is not canonical are redirected to the canonical one, except the
captive API (answered on every portal hostname: API clients need not follow redirects) and
`/auth/social/callback` (the attempt's stored redirect URI decides, see §5).

## 5. Social sign-in (customer-owned applications)

* **Callback URL**: `https://<portal hostname>/auth/social/callback`. The Admin Console shows it for the
  OneGate hostname and, when one exists, the hotel domain; the hotel ticks which ones it registered in its app
  (`social_oauth_providers.callback_hosts`). A provider is **offered only while HTTPS is active and the
  canonical hostname is in its callback_hosts**.
* **Google** accepts only callback domains whose top private domain the app owner verified in Search Console,
  so Google requires the hotel portal domain. Apple (no domain file needed — Apple's own documentation),
  Microsoft and Facebook work on the OneGate hostname.
* **Secrets** are write-only, sealed by scd (AES-256-GCM under `social_dek.key`, AAD = tenant, provider,
  generation) into `iam_v2.social_provider_secret_generations`; the plaintext column is cleared. scd reloads
  providers when edged signals a change and every 60 s.
* **Attempt binding**: a sign-in attempt is a single-use server-side row (`social_oauth_states`): 256-bit
  `state`, PKCE S256 `code_verifier` (Google, Microsoft), OIDC `nonce` (Google, Microsoft, Apple), the device's
  IP and MAC, 10-minute expiry. The callback is accepted only for an unexpired, unconsumed attempt from the same
  device; this is CSRF/replay protection. **Identity comes only from the provider's verified ID token or
  verified profile**, never from MAC/IP. Google identity is taken from the verified ID token (issuer, audience,
  nonce, expiry, signature against Google's JWKS) and requires `email_verified`.
* **No cookies are needed** for the flow (Apple returns with a cross-site form POST; captive browsers drop
  cookies).
* **Embedded browsers**: Google and Facebook refuse sign-in inside an Android WebView (the old captive login
  window, user agent containing `; wv)`). There the portal sends those two providers to an "open in your
  browser" page with the portal address; other methods stay available. The captive API advertises
  `x-android-use-custom-tabs` so Android 12+ (Captive Portal Login module ≥ 361335020, January 2026) opens the
  portal in a real Chrome Custom Tab, where Google sign-in is allowed.
* **Pre-sign-in reachability**: each enabled provider's fixed sign-in domains are allowed (TCP 443 only) in the
  walled garden only while that provider is enabled and configured (existing `socialGardenDomains`).

## 6. Captive portal API (RFC 8908 / RFC 8910)

`GET /captive-portal/api` on portald (TLS listener), per requesting device (identified by its TCP source
address, looked up in scd):

```json
{"captive": true, "user-portal-url": "https://<canonical>/", "venue-info-url": "https://<canonical>/",
 "x-android-use-custom-tabs": 361335020}
{"captive": false, "user-portal-url": "https://<canonical>/", "seconds-remaining": 3540}
```

`Content-Type: application/captive+json`, `Cache-Control: private, no-store`. Option 114 carries this URI only
while HTTPS is active (§4). A device whose TLS check of the API fails falls back to its normal HTTP probes.

## 7. Appliance ↔ Central API (mutual TLS + appliance JWT, `/v1/appliance/` group)

* `POST /v1/appliance/portal-tls/sync` — body `{"portal_ip":"10.255.255.1","custom_domain":"wifi.nilehotel.com"|null}`.
  Idempotent: allocates the hostname if none, upserts the A record when the address changed, registers /
  re-checks / removes the hotel domain to match `custom_domain`. Response:
  `{"available":bool,"hostname":"…","dns_state":"PENDING|SYNCED|ERROR","custom_domain":{"fqdn","status","records":[{"type","name","value"}],"error"}|null,"certificates":[{"hostname","kind","order_id","status","serial","not_after","renew_after","revoked","error"}]}`.
  `available=false` means Central has no DNS/ACME configuration yet (the appliance reports `NOT_CONFIGURED`).
* `POST /v1/appliance/portal-tls/orders` — `{"hostname","csr_pem"}` → `202 {"order_id","status"}`; 409 when an
  order is already open for that hostname (returns its id); 422 on a refused CSR/hostname; 429 over the daily limit.
* `GET /v1/appliance/portal-tls/orders/{id}` — `{"status","chain_pem"?,"serial","not_after","renew_after","error"}`.

These are the three new appliance→Central messages, all licensing-plane certificate lifecycle (CLAUDE.md §0E):
they carry hostnames, the portal address, CSRs and certificate status — no guest data.

## 8. Admin Console and Central console

* Appliance Admin Console, **Network → Portal HTTPS**: hostname(s), HTTPS state and reason, certificates
  (expiry, renew-after), portal address (edit → network apply), hotel domain (enter → records to publish with
  copy buttons → Check now → status; Remove), Renew now. Permissions `portal_https.read` / `portal_https.manage`
  (manage requires step-up).
* **Sign-in → Social providers**: per provider, the callback URLs for each available hostname with copy buttons
  and the "registered in my app" ticks; secret fields write-only; status line (offered / why not).
* Central console, **Portal certificates**: settings (ACME directory, contact, zone, weekly limit), DNS token
  (write-only), Test DNS access, Apply CAA, fleet table (appliance, hostname, address, DNS state, hotel domain,
  certificate expiry, renew-after, last error), Revoke.

## 9. Security properties

* No company-wide DNS credential ever reaches an appliance; the Cloudflare token is zone-scoped and kept on
  Central only.
* No shared private keys; no wildcard certificates (`issuewild ";"`); CAA pins the OneGate ACME account.
* The portal address is unreachable from the WAN and from management interfaces.
* MAC/IP is never identity: device binding protects the sign-in attempt; identity is the provider's verified
  token or the verified code.
* Fallback is explicit and visible; nothing silently downgrades a TLS session (no HSTS, no mixed origins).

## 10. Known limitations (documented, accepted)

* Android older than 7.1.1 does not trust ISRG Root X1 and shows a certificate warning on the HTTPS portal.
* Google sign-in needs a hotel portal domain; Facebook and Google are unavailable inside Android WebView login
  windows.
* A site whose own DNS server applies DNS-rebinding protection must allow `gw.onegate.work` (and its hotel
  domain) to resolve to a private address.
* With external DHCP, the hotel's DHCP server must hand out option 114 itself if wanted; probes still work.

## 11. Implementation decisions taken during delivery (2026-10-13)

* **The hostname travels in the sync answer, not the signed assignment.** The mutual-TLS, appliance-JWT-bound
  sync endpoint already authenticates Central to the appliance; adding a tail field to the signed assignment
  would have re-issued every assignment for no extra assurance.
* **DHCP option 114 is emitted by code 114, never by the name `v4-captive-portal`.** Kea 2.0 (the appliance runs
  2.0.2) resolves that name to code 160, the obsolete RFC 7710 assignment, so until this delivery no appliance
  on Kea 2.0 delivered option 114 at all (found by capturing a real exchange on PRE-LIVE).
* **The portal address is explicitly dropped on every non-guest interface and in the forward chain** (netd).
  The input policy alone did not protect it: the interface-wide SSH and admin accepts matched, and a missing
  `ogportal0` let a signed-in guest's packet to it be forwarded and NATed. Both were proven on a real kernel
  before the rules were added.
* **Redirects:** page loads get 302; other methods 307 so a form submitted just before HTTPS came up is not
  turned into a GET. An unknown Host over TLS gets 421.
* **A certificate Central reports revoked is removed at once and a new one ordered**; the portal is on the HTTP
  fallback for the seconds that takes (about 30 s on PRE-LIVE) rather than serve a revoked certificate.
* **portald switches to the fallback after two failed polls of scd** (15–30 s), so an scd restart does not flap
  guests between schemes.
* **A production build never registers the social-login stub** (it accepts any identity named in its code) —
  neither in scd (the stub provider) nor in portald (its consent page `/api/oauth/stub/authorize` and
  `/authorize-confirm`, compiled only into a `-tags devlicense` build, `stub_oauth_dev.go`; a production portald
  answers those paths as any unknown path). Even in a development build the stub only returns to this portal's
  own `/auth/social/callback` on the origin it was reached on, and its page carries the portal CSP (audit P-2).
* **form-action** lists `'self'`, the portal's own HTTPS names (so a form posted over HTTP and re-posted 307 to
  `https://<canonical>` is not refused) and the payment adapters' declared hosted-checkout origins (so the card
  checkout redirect is not refused). The package page also submits the choice by script and navigates with
  `window.location`, which form-action does not govern (audit P-1, P-33).
* **On the HTTP fallback** the portal keeps the whole journey on one origin: while the portal address is up, a
  page load on any other name (a captive probe host, a site the client typed) is redirected to
  `http://<portal address>/`; Android/Windows probes go to the portal address, or to the address the
  connection reached — never to the `.local` `PORTALD_FQDN`, which Apple devices resolve by mDNS only. The card
  provider's return URL is built from the canonical origin, never from the request's Host (audit P-15, P-21,
  P-27). Every portal cookie is `Secure` when set over TLS (audit P-17).
* **Central console:** the page is System → Portal certificates; reads need `fleet.view`, writes
  `security.manage` plus a password step-up.
* **Central refuses certificate orders (503) while it is not configured**, so none pile up before the DNS token
  exists.

## 12. Verified on Central and PRE-LIVE (2026-10-13), with a temporary test CA

The full chain ran on the real Central (`172.21.96.196`) and the PRE-LIVE appliance (`172.21.60.25`) against a
temporary Pebble ACME CA (15-minute certificates) and a challenge-test DNS server standing in for Let's Encrypt
and Cloudflare, then both were removed and the configuration reset (the appliance is back on `NOT_CONFIGURED`).
Phones do not trust that CA, so this proves the machinery, not public trust.

| Proven | Evidence |
|---|---|
| Hostname allocation, A-record publication | `t89b76xm.gw.onegate.work` → `10.255.255.1`, DNS SYNCED |
| Issuance (DNS-01) for an appliance CSR; key 0640 on the appliance only | order ISSUED, chain verified on install |
| Guest device: option 114 = `https://<host>/captive-portal/api`, DNS, RFC 8908 API, TLS, no HSTS, probe and page redirects, nothing else intercepted | `scripts/portal-https-e2e-test.sh` 18/18 |
| Voucher and account + free package sign-in over HTTPS, enforcement, real internet, captive API → not captive, sign-out | `scripts/portal-https-signin-e2e-test.sh` 12/12 |
| Renewal with a NEW key, not counted as new, previous SUPERSEDED, served without restart | two CSRs with different public keys |
| Central down: HTTPS unaffected | 18/18 while ctrlapi was stopped |
| CA down until expiry: HTTP fallback, option 114 back to HTTP, critical admin alert; automatic recovery | fallback 18:16:29, recovered 18:22:30 UTC with no action |
| Hotel domain: three records, two-resolver verification, delegated DNS-01, canonical switch, old name redirects, API on both | 18/18 on `wifi.nilehotel-test.example` |
| Hotel domain removed: certificate revoked (CA confirms reason 5), files and DNS answer removed, back to OneGate hostname | — |
| Operator revocation: appliance replaces the certificate by itself | back on HTTPS in 30 s |
| Social offering: sealed secret, button only when the callback host is canonical, authorize with HTTPS callback + PKCE S256 + nonce, forged state refused, WebView page, garden pairs | `scripts/portal-social-offering-test.sh` 15/15 (placeholder Microsoft app) |

## 13. Verified with the real Cloudflare zone and Let's Encrypt (2026-10-14)

onegate.work is hosted at Cloudflare; the zone-scoped token is on Central (entered by the Product Owner).

| Step | Evidence |
|---|---|
| Staging | Central issued staging certificates for `t89b76xm.gw.onegate.work` through Cloudflare DNS-01. The chain verifies against Let's Encrypt's published staging root *(STAGING) Pretend Pear X1* (SHA-256 `E7:05:70:A9:…:02:91:21:98`) and fails against the system store; the appliance therefore refused it, as designed, and now says it is a staging certificate. |
| Switch to production | 14:46:20 UTC: `SETTINGS_CHANGED` staging -> production and `RENEWAL_FORCED` (the staging certificate made due at once). The production ACME account was registered. |
| Production certificate | 14:47:19 UTC order ISSUED (counted as new against the weekly cap); installed 14:47:49 UTC after verification against the system roots only. Issuer Let's Encrypt `YE1`, serial `068FF9014F5C7C668B155972C652D8CDC86D`, valid to 2027-01-12. |
| Public DNS | `t89b76xm.gw.onegate.work A 10.255.255.1` (Cloudflare resolver). |
| Guest device on PRE-LIVE, system roots only | `portal-https-e2e-test.sh` 18/18; `portal-https-signin-e2e-test.sh` 12/12; `portal-social-offering-test.sh` 15/15. |

**Not yet verified** (needs the Product Owner): real iPhone/Android behaviour (checklist), and a real
social-provider round trip (needs the hotel's own provider apps).

## 14. Captive-portal discovery across operating systems (investigated 2026-10-14/15)

What OneGate offers every device, and what each platform does with it. Sources: RFC 8908/8910/8952, Apple
(support 101555, developer news q78sq5rv, support 102554), Android NetworkStack `NetworkMonitor` and the Custom
Tabs page, Microsoft KB4494446 (NCSI), Chromium `captive_portal_tab_reloader.cc`, `ssl_error_handler.cc`.

| Platform | Mechanism | OneGate | Verified |
|---|---|---|---|
| iOS / iPadOS 14+ | option 114 -> RFC 8908 API (TLS); also the `captive.apple.com` probe | option 114 by code; API on the public certificate; probe 302 to HTTPS | iPhone opened the sheet by itself (PRE-LIVE, 2026-10-14) |
| Android 11+ | option 114 API is authoritative for "captive"; HTTP/HTTPS probes | API + `x-android-use-custom-tabs`; probes 302 | Android (A71) opened by itself |
| macOS 11+ | as iOS | as iOS | a MacBook received option 114 and ran no captive check at all (packet capture): Mac-side configuration |
| Windows 10/11 | NCSI probe `msftconnecttest.com/connecttest.txt`, then opens the browser at `/redirect`; no RFC 8910 use documented | probe 302 to HTTPS; `/redirect` reaches the portal | not yet on a real Windows device |
| Desktop Chrome / Edge, a typed or bookmarked `https://` site | captive check only on ERR_CONNECTION_TIMED_OUT, ERR_SSL_PROTOCOL_ERROR, a certificate error, or its 30-s slow-TLS timer; a reset or refusal suppresses it | **pre-sign-in TCP 443 is now dropped silently (was reset)**, so Chrome/Edge run their check and open the login tab | kernel test: no RST, nothing leaves the WAN; real browser not yet |
| Firefox | its own probe (`firefox-portal-detection.com`), "Open network login page" | probe 302 to HTTPS | seen on the MacBook (2026-10-14) |
| Mobile Chrome, Safari | no browser-level captive detection; they rely on the OS | — | — |

**Never done:** HTTPS interception, certificate spoofing, TLS downgrade, DNS hijacking of probe names (Android
classes a probe name resolving to a private address as no-Internet), HSTS.

**Unavoidable limits (documented):** an `https://` page typed into Safari or mobile Chrome before signing in cannot
show the portal: no portal can answer TLS for another site's name. A device whose own captive detection is off
(macOS managed or configured, VPN, some security suites) will not open the login window by itself. **Fallback:**
opening any `http://` address, or the portal hostname, reaches the HTTPS portal; the Admin Console's network page
and printed material should show the portal hostname.

## 15. The HTTP fallback is not secure (recorded 2026-10-15)

The Product Owner chose not to redesign the HTTP fallback at this stage: while HTTPS is not active (§4), the portal is
served over plain HTTP on the guest network, as before. That is a security limitation, not a secure mode: anyone on
the same guest network can observe or alter portal traffic on the fallback, including passwords typed into the
Email-account and Personal-account forms. HTTPS active is the secure path; the fallback exists only to keep the
portal reachable, and the administrator alert (`portal_https`) says when it is in force.

**The portal health check measures portald itself (fixed 2026-10-15).** edged's check followed the HTTP listener's
302 to `https://<canonical>/`, which from the appliance host reaches Caddy's :443 rather than portald (guests reach
portald :8343 only through the guest-interface DNAT), and failed TLS there -- so "Client Portal is degraded" was shown
continuously from the moment HTTPS went live (2026-10-14 14:48 UTC) while the portal served clients normally. The
check no longer follows redirects, and while HTTPS is active it completes a verified TLS request to portald's own
:8343 for the canonical hostname (system roots, hostname, validity), so a real certificate or listener failure is
still reported.

