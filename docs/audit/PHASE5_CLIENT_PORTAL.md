# Backend audit — Phase 5: Client Portal (portald)

| | |
|---|---|
| Audited commit | `9ffa981b` (master; unchanged since Phase 4) |
| Scope | `data-plane/cmd/portald` (about 11,800 lines of non-test Go; 60 routes) and `data-plane/internal`: portaldesign, mail, sms, whatsapp, notifyloader, codegen (about 3,300 lines). Also how portald reads the device IP and MAC it sends to scd, and the payment return address carried over from Phase 4. |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |
| Open branch checked | `delivery/authenticator-admin-console-branding` (Ahmed, not merged) changes no file in this scope. |

## How to use this file

1. **Format.** It is the same as Phases 1–4. Each finding has a problem, its impact, the recommended fix and the test that proves it. Fixes are grouped into batches at the end.
2. **CONTROLLED areas.** M1 and M2 touch guest authentication and the commerce path (CLAUDE.md §0). Each batch names its area.
3. **Status of a finding:**
   - **Confirmed**: the code path was traced.
   - **Possible**: the code suggests the problem, but it needs a test or deployment facts to prove.
4. **How it was checked.**
   - The auditor read the routing, the device identity code (`main.go`, `arp.go`, `arp_resolve.go`), cookies, origin checks, sign-out, the request timeout, the HTTPS front (`portal_https.go`), every sign-in handler, the commerce session and acquisition, and the trusted-type template fields.
   - One independent reviewer read the notification and design packages. Every claim of theirs listed here was re-checked; the rejected ones are at the end.
5. **Trust boundary.** portald is the most exposed process on the appliance: every guest network reaches it before sign-in, through nftables DNAT. It runs as its own user and reaches only scd's guest routes (Phase 4). Everything a guest sends is attacker-controlled; the hotel's design values come from Admin Console operators.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 2 |
| Low | 12 (M2 was lowered to Low by Phase 6 and keeps its number) |
| Clean code | 4 |

### What was run

| Check | Result |
|---|---|
| `go vet` (all Phase 5 packages) | clean |
| `staticcheck` | 2 unused items and 3 style items (CC1, CC4) |
| `gosec` | 26 leads, triaged below. G203 (trusted template types) are values that pass the `portaldesign.ForGuests` sanitiser. G302 is the 0640 stub log, which holds no content in production. G116 is the translated wording (CC4). G101 are constant names. |
| `govulncheck` | only the Phase 0 P0-2 advisories (net/http, crypto/tls, html/template and x/net among them, all reachable from portald) |
| Unit tests, all Phase 5 packages | pass |

No database is needed for these packages; no container was started.

### How portald identifies the device (asked for in Phase 4)

| Step | What the code does | Verdict |
|---|---|---|
| IP address | The TCP peer of the connection (`clientIP`, `main.go`). There is no `RealIP` middleware, and no handler reads `X-Forwarded-For` or `X-Real-IP`. `forwardedHTTPS` trusts `X-Forwarded-Proto` from a loopback peer only, and only for the scheme. | Sound. |
| MAC address | Read from `/proc/net/arp` by IP (`arp.go`). On a miss portald sends one UDP datagram to the device's discard port so the kernel resolves it, then polls for up to 400 ms (`arp_resolve.go`). Never from the browser. | Sound in principle; see L1. |
| Use | Every sign-in route sends both to scd, and refuses the guest when either is missing. Activation and device replacement send the connection's pair again. | Sound, except the commerce quote and confirm (M2). |
| What it cannot prove | IP and MAC are what the device claims on the local network. A device that copies another device's MAC and IP is indistinguishable here; only the network can stop that (client isolation, DHCP snooping). | Inherent to captive portals. Phase 8 to check the network side. |

### What holds up well (keep it this way when fixing)

- **No forwarding headers.** The device address is always the connection's.
- **Cross-site requests.** The SMS and WhatsApp code request needs a JSON content type (a cross-origin page cannot send one without a preflight portald never answers) and refuses any other Origin. Sign-out needs a per-device HMAC token, so a page elsewhere cannot sign a guest out.
- **Codes and tokens in links.** Links in messages never act on GET; the button posts. Query strings are redacted from the request log. Link pages send `no-referrer` and `noindex`.
- **Cookies.** HttpOnly and SameSite=Lax everywhere, Secure over HTTPS. The long-lived "remember this device" credential is set only over HTTPS.
- **The hotel's design.** Values are sanitised twice (on save in edged, on serve here) by a parse-and-rebuild allowlist. The page CSP allows only scripts carrying the response's nonce. JSON for scripts is escaped so it cannot close the script element.
- **HTTPS front.** Redirect targets are rebuilt from the parsed path, so an absolute-form request line cannot smuggle a host. scd's hostnames and certificate directory are validated before they become paths or redirect targets. Requests for a name that is not the portal's get 421.
- **Payment return address.** It is the canonical HTTPS name, else the portal address, else the address the connection reached (`returnBase`); the guest's `Host` header is used only when none of those exist, which does not happen on a running appliance.
- **Outbound providers.** SendGrid, Twilio and Meta base URLs are constants; every client has a 10 s timeout and reads error bodies through a limit. SMTP verifies certificates, requires STARTTLS with no plaintext fallback, and refuses password authentication without TLS.
- **Code generation.** `crypto/rand` with `rand.Int`, so no modulo bias; voucher length is bounded by scd (6–64), post-stay PINs are 8 characters.

## Medium

### M1 — One guest device can exhaust the portal for every guest (Confirmed)

**Where.** `cmd/portald/main.go:622-625`: both `http.Server`s set only `ReadHeaderTimeout: 5s`. No `ReadTimeout`, `WriteTimeout` or `IdleTimeout`. `email_handlers.go:67` and `:134` decode the body with no size limit; the form routes rely on `ParseForm`'s 10 MB default. `deploy/nftables/stayconnect.nft:68` accepts the portal ports with no per-source connection limit.

**Problem.** After the headers, a connection may send its body as slowly as it likes. `portalTimeout` cancels the request context after 30 s, but a body read does not watch the context, so the handler goroutine and the connection stay. Idle keep-alive connections are never closed. Nothing limits how many connections one device opens.

**Impact.** One device on any guest network can open thousands of connections, each trickling a body, and hold memory and file descriptors until portald stops accepting. Every guest on every network then loses the sign-in page. A restart frees them, and the device can repeat it at once. The sign-in damper (`throttle.go`) does not help: it counts requests that reach a handler, not open connections.

**Recommended fix.**
1. Set `ReadTimeout: 15s`, `WriteTimeout: 40s` (the 30 s request timeout plus margin) and `IdleTimeout: 60s` on both servers. `MaxHeaderBytes: 16 KB`.
2. Wrap every guest body in `http.MaxBytesReader(w, r.Body, 64<<10)` in one middleware, so `ParseForm` and every JSON decoder are capped together.
3. Phase 8: add a per-source concurrent connection limit for ports 8380 and 8343 in netd's nftables rules (`ct count over N`). This is a networking change; it needs the Phase 8 batch.

**Test.** A test server built by the same constructor closes a connection that sends one byte per second after 15 s; a 1 MB form post is refused with 413; an idle keep-alive connection is closed after 60 s.

### M2 — The commerce session cookie is a bearer capability, sent in clear on the HTTP fallback (Confirmed; Low after Phase 6)

**Where.** `cmd/portald/commerce.go:51-83` (store and lookup by cookie only); `iamv2_commerce_session.go:253-283` (quote and confirm send the session's `device_id`, not the connection's IP and MAC); `cookies.go:13` (Secure only over TLS).

**Problem.** After sign-in, `sc_commerce` holds the guest's auth context for 10 minutes. portald accepts it from any connection, and the quote and confirm calls to scd carry no proof of which device is asking. On the HTTP fallback the cookie crosses the guest network in clear text, and guest Wi-Fi is often open or shares one password.

**Impact.** Someone on the same network who sees the cookie can, within 10 minutes, choose and confirm a package on the victim's sign-in. The access goes to the victim's device (activation re-checks the connection), so the attacker gains no internet, but can spend what the victim's sign-in allows. If that includes charging the room (`PMS_POSTING`) or a voucher, it is a financial act the guest did not choose. 

**Settled by Phase 6.** No money moves on this cookie alone. `ConfirmPurchase` (`internal/iamv2/commerce_flow.go:213-345`) handles three methods. Free grants at once. Voucher is allowed only for a voucher sign-in, and burns the voucher the guest already typed. Card only opens a checkout that someone must pay. Any other method, room charge included, is refused (`method_not_available`). Room charge goes only through the room sign-in grant (`cmd/scd/phase3_paid.go`), which consumes the sign-in against the connection's own device (`authctx.ConsumeTx` with the device from portald's IP and MAC). So someone holding the cookie can, at most, take a free package or redeem the victim's voucher for the victim's own device. That can replace a better access the victim already had. **Severity: Low.** The fix stays as written.

**Recommended fix.**
1. Store the connection's `ip|mac` in the commerce session when it is issued, and refuse the session in `resolveCommerceSession` when the current connection's pair differs. Treat a refusal as "please sign in again".
2. Send the connection's IP and MAC on `/v1/commerce/quote` and `/confirm`, and have scd check they resolve to the session's device, as activation already does.
3. Prune expired sessions (L4).

**Test.** A commerce cookie presented from another IP or MAC gets the sign-in page and makes no scd commerce call; scd refuses a confirm whose IP and MAC do not match the device.

### M3 — A broken notification provider looks healthy while no code is delivered (Confirmed)

**Where.** `internal/notifyloader/loader.go:132-172`, `:228-241` (a provider that fails to construct, or has an unknown kind, falls back to the stub); `internal/mail/mail.go:44-67`, `internal/sms/sms.go:42`, `internal/whatsapp/whatsapp.go:114` (in a production build the stub returns `nil`); `loader.go:255-269`, `:328-355` (`updateHealth` with a nil error sets `last_success_at` and clears `last_error` on the enabled row).

**Problem.** If an enabled provider cannot be built (a malformed token, a secret that cannot be unsealed, a bad setting), every send goes to the stub, which reports success. The wrapper then records a success on the enabled provider row.

**Impact.** Guests are told a code was sent and none arrives, so SMS, WhatsApp and email sign-in, verification and password reset stop working. The Admin Console's provider screen shows the provider as working, so staff have nothing to go on but guest complaints. Only a warning in the journal at load time says otherwise.

**Recommended fix.**
1. In a production build the stub returns a typed `ErrNotDelivered`.
2. `resolve` records the construction failure on the provider row (`last_error`, without the secret), and the wrappers skip `updateHealth` when the kind is `stub`.
3. scd answers a code request on a channel with no working provider as unavailable, so the portal says so instead of "code sent".

**Test.** With an enabled SMS row whose token is malformed: the code request is refused as unavailable, `last_error` names the construction failure, and `last_success_at` does not change.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | **The MAC lookup is IPv4-only, matches by IP on every interface, and ignores the entry's state.** The same IP on two interfaces (an overlapping subnet, or a non-guest LAN that can reach the listener) returns the first row. A failed or permanent entry is accepted if its MAC is not all zeros. The whole file is read for every landing page (every captive probe) and up to ten more times while resolving. An IPv6 guest is told it is not on the Wi-Fi. | `cmd/portald/arp.go:12-33`, `arp_resolve.go:42-67` | Look the neighbour up over netlink on the interface the connection arrived on (from `http.LocalAddrContextKey`), accept only REACHABLE, STALE, DELAY or PROBE. Phase 8 confirms whether guest networks carry IPv6; if they do, add the IPv6 neighbour table. Test with a fake neighbour source: the same IP on two interfaces returns the arriving interface's MAC; a FAILED entry is refused. |
| L2 | **The flood damper does not cover every route that checks a secret or sends a message.** Undamped: `/auth/account/grace` (a password check), `/auth/account/resend` (sends email), `/account/verify` and `/account/reset` (link tokens), `/auth/pms/phase3` (room and name), `/auth/post-stay-pin` (PIN), `/devices/*`, `/packages/acquire`. scd throttles most of these, but only when its durable throttle is on (Phase 4 L1). | `cmd/portald/main.go:524-596` | Wrap every POST that reaches a scd credential, message or commerce route in `h.damped`. Keep the Phase 3 failure-time budget: damp before the budget starts, or answer the damper's refusal through the budget. Test: the 31st grace post in a minute from one address gets the throttle answer and makes no scd call. |
| L3 | **Expired commerce sessions are never removed.** `put` adds, `get` refuses an expired one but leaves it, and only sign-out deletes. Every sign-in leaves a session in memory until the next restart. | `cmd/portald/commerce.go:51-71` | Delete on an expired `get`, and prune expired entries in `put` once the map passes a bound, as `pendingOTPStore` does. Test: after the TTL, the map no longer holds the token. |
| L4 | **Design image fields accept any appliance path.** `logo_url`, `background_url` and `hero_image_url` pass `CheckImageURL`, which allows any `/path`; fragment images are limited to `/assets/` for exactly this reason. An operator's mistake (or a compromised operator) makes every guest's browser issue a GET to a portald route, for example `/auth/social/start?provider=…`, which creates a sign-in attempt in scd on every page view. CSS `url()` may also point at any https host, which can read typed values with attribute selectors once a device is online. Operators are step-up gated, so this is defence in depth. | `internal/portaldesign/urls.go:91-107`, `design.go` (image fields), `css.go` (`url()`) | Use the `/assets/` rule (`checkFragmentImage`) for the three image fields and for CSS `url()` paths; allow https images only where the design needs them. Test: `logo_url=/auth/social/start` is refused on save. |
| L5 | **An unsealable provider secret silently falls back to the plaintext column.** When the sealed generation cannot be opened, the loader keeps whatever is in `api_key`, which may be stale or empty, and logs nothing. | `internal/notifyloader/loader.go:104-110` | Log the failure with the provider id (never the secret) and record it as the provider's `last_error`. Once every appliance has sealed secrets, stop reading the plaintext column (a migration to clear it is a separate, CONTROLLED step). |
| L6 | **A provider failure caused by a cancelled send is not recorded, and a long error can fail to record.** `updateHealth` derives its context from the send's, so a send that failed because its context ended cannot write its health. `truncate` cuts bytes, so a non-ASCII error can be cut mid-character and rejected by Postgres; the error is ignored. | `internal/notifyloader/loader.go:333`, `:357-362` | Use `context.WithoutCancel(ctx)` with its own 2 s timeout; truncate on runes. Unit test both. |
| L7 | **Admin-set SMTP host and port are unrestricted, and the server's reply is shown back.** An operator can point the mailer at a loopback or LAN service and read its greeting or error line in `last_error` on the provider screen. Deliberate for an internal relay, but it is a small internal probe. | `internal/mail/smtp.go:52-63`, `:114-143` | Show the operator a generic failure ("connection refused", "TLS failed", "authentication failed") and keep the server's words in the journal; optionally limit ports to 25, 465, 587 and 2525. |
| L8 | **Email message format.** The multipart boundary is a constant, so a body containing that line reshapes the message. Bodies have no Content-Transfer-Encoding or line wrapping (RFC 5322 limits lines to 998 octets). There is no `Message-ID`. The `From:` display name is written from the raw string, so a name containing `"` or `<` can change how the sender is displayed. | `internal/mail/smtp.go:215-240` | Random boundary; quoted-printable bodies; add `Message-ID`; build `From`/`To` with `mail.Address{}.String()`. Test a body containing the old boundary and a 2,000-character line. |
| L9 | **Twilio SMS does not validate the destination** (WhatsApp checks E.164); SendGrid does not validate the recipient. Both are JSON- or form-encoded, so this is not injection, but a malformed destination costs a provider call. | `internal/sms/twilio.go` (`Send`), `internal/mail/sendgrid.go` (`Send`) | Reuse `phone.Normalize` and `net/mail.ParseAddress` before the call. |
| L10 | **Design value checks are loose.** A font stack may carry an unbalanced quote, which swallows the next CSS declaration (it cannot inject: `;{}()` are excluded). Colours accept 5- and 7-digit hex. | `internal/portaldesign/design.go:75-77`, `:272` | Require balanced quotes; accept only 3, 4, 6 or 8 hex digits. |
| L11 | **Provider rows are read in no order**, and nothing enforces one enabled row per channel, so with two enabled rows the last one read wins. A bad port or timeout silently becomes the default; `ValidateSMTPConfig` allows a timeout of 0 while its message says 1–120. | `internal/notifyloader/loader.go:81-98`, `:214-226` | Add a partial unique index (one enabled row per tenant and channel; a CONTROLLED migration) or `ORDER BY updated_at DESC`; refuse an unparsable number at save; make the validator match its message. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code.** `humanDuration` (`main.go:649`) and `black` (`portal_colour.go:34`) are unused. `portalLinkBase` and the `portal_base` field it fills (`otp_pending.go:227-243`, `email_handlers.go:93`) are dead: scd ignores the value because it is a guest-supplied `Host` (`cmd/scd/otp_handlers.go:92`). `sms.Message.From` is never read. In `notifyloader`, `providerRow` duplicates `ProviderRow` and `region` is unused. `portaldesign/html.go:291` has an `abbr|title` case already handled above it. | Delete them, and stop sending `portal_base`. |
| CC2 | **Two cookie setters.** `cookies.go` says every portal cookie is set there, but `setAccountCookie` (`account_handlers.go:57-69`) builds its own. `codegen`'s `ModeComplex` produces the same alphabet as `ModeAlnum`, and the `Normalize` comment claims compatibility with the voucher normaliser that it does not have. | Use `setPortalCookie` for the account cookie. Fix or merge the codegen mode and correct its comment. |
| CC3 | **Stale comments on security points.** `main.go:132` says Admin Console and portald "run as the same service user"; portald has had its own account since audit N-26 (`deploy/systemd/stayconnect-portald.service`). `social_handlers.go:150` says Caddy terminates TLS for the portal; Caddy's portal vhost now answers loopback only and portald serves HTTPS itself. | Correct each to one line. |
| CC4 | **Size and style.** `templates.go` is 2,272 lines of HTML and JavaScript inside Go strings, and most files carry long narrative comments, against the project's "no comments unless critical" rule. `languages.go` contains invisible Unicode format characters in literals (staticcheck ST1018, gosec G116). Three near-identical instrumented wrappers in `notifyloader`. | Move page markup and script to embedded files (`embed`), which also lets the CSP test read them. Write format characters as `\u` escapes. One generic wrapper. Trim comments when a batch touches a file. |

## Fix batches

A batch is done when its tests pass and the portald package tests still pass.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| **B1** Server limits | M1 (steps 1–2) | `cmd/portald/main.go`, a body-limit middleware | Small, no behaviour change for real guests. **Do first.** Step 3 goes into the Phase 8 netd batch. |
| **B2** Commerce session binding | M2, L3 | `cmd/portald/commerce.go`, `iamv2_commerce_session.go`, `cmd/scd/commerce_handlers.go` | CONTROLLED (guest authentication, commerce). Settled by Phase 6: no money moves, so this batch is no longer urgent. |
| **B3** Notification honesty | M3, L5, L6, L11 | `internal/notifyloader`, `internal/{mail,sms,whatsapp}` stubs, `cmd/scd` code request | L11's unique index is a CONTROLLED migration: leave it out unless authorised, and use `ORDER BY` meanwhile. |
| **B4** Device identity | L1 | `cmd/portald/arp.go`, `arp_resolve.go` | CONTROLLED (guest authentication). Needs a Linux host to test the netlink lookup. |
| **B5** Flood damper coverage | L2 | `cmd/portald/main.go`, `pms_phase3_budget.go` | Guest authentication. Keep the room sign-in timing budget intact. |
| **B6** Design and mail hygiene | L4, L7, L8, L9, L10 | `internal/portaldesign`, `internal/mail`, `internal/sms` | L4 can refuse designs already saved: check PRE-LIVE's saved design before deploying. |
| **B7** Clean code | CC1–CC4 | various | After the behaviour batches, never alongside them (same files). CC4's template move is large: do it alone. |

No item in this phase needs a Product-Owner decision.

## Checked and rejected

- **"The payment return address can be steered with the Host header"** (from Phase 4). `returnBase` uses the canonical HTTPS name, the portal address, or the address the connection reached (`iamv2_commerce_session.go:437-447`); `Host` is a last resort that a running appliance never reaches.
- **"A guest can put their own domain in the code message's link with the Host header."** portald still sends `portal_base` from `Host`, but scd never uses it for the link (`cmd/scd/otp_handlers.go:92`). Recorded as dead code (CC1).
- **"Forwarding headers can make the portal act on another guest's address."** No middleware or handler reads them; `forwardedHTTPS` trusts only the scheme, from loopback.
- **gosec G203 (template.URL, template.CSS, template.HTML, template.JS).** Each value comes from `portaldesign.ForGuests` (URLs through `CheckImageURL`/`CheckLinkURL`, CSS rebuilt from parsed rules, `</style` refused) or from `json.Marshal`, which escapes `<`, `>` and `&`. The CSP blocks any script without the nonce.
- **"Short numeric vouchers (4 digits) are allowed by codegen."** scd bounds voucher length to 6–64 (`voucher_code_format.go:49`, `:64`). The real risk is Phase 4 M2.
- **"SMS code request CSRF."** The route needs a JSON content type and refuses another Origin (`email_handlers.go:62`).
- **"The account and resume cookies can be replayed from another device."** scd's account and resume routes receive the connection's IP and MAC and check them (Phase 4).
- **gosec G302** (0640 stub logs): a production build writes only a masked address and "NOT DELIVERED".
- **"Provider IDs in API paths allow path steering"** (`..` survives `PathEscape`): operator-set, same provider host, negligible.

## Not verified

- **Connection limits in the network layer** (M1 step 3) and whether guest networks carry IPv6 (L1). Phase 8.
- **Client isolation and DHCP snooping on guest networks**, which decide how easily a device can copy another's IP and MAC. Phase 8.
- **The browser tests** (`csp_checkout_browser_test.go`, `portal_shots_test.go`) were run only as far as `go test` runs them without a browser.
- **Race-detector runs.** These still need a Linux build host.
