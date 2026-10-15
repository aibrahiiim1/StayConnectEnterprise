# OneGate — Email + Password self-service accounts (contract)

Status: **authoritative contract** for the self-service delivery (branch `delivery/email-accounts-self-service`).
Product behaviour approved by the Product Owner on 2026-10-15 ("Complete Self-Service Authentication Redesign"),
migration 0117 approved the same day. Where code and this document disagree, the code is wrong.

## 1. What replaces what

| Before | After |
|---|---|
| Email one-time code sign-in (`auth_methods.email`) | **Email accounts**: register with email + password, sign in with email + password (`auth_methods.email_account`) |
| Temporary Internet while reading the email code (verification access windows, nft `verify_*` sets) | Removed. Nothing grants pre-auth Internet. |
| Staff Code requests: temporary-access columns and "clear" | Removed. Code requests remain only for the SMS / WhatsApp codes they still serve. |
| Mail-service allowlist for reading the code before sign-in (`portal.mail_providers/mail_domains`, `internal/mailreach`) | Removed. |
| — | Email verification by single-use link, deadline, suspension, one-time grace hour |

Preserved: SMS and WhatsApp codes (shared OTP engine, `/connect` link for SMS, escrow, staff reveal), staff-created
Personal accounts (`guest_account`), vouchers, room sign-in, social sign-in, payments, quotas, one live entitlement.
Historical rows (`verification_access_grants`, `auth_otps`, escrow, reveals) are kept; only code and UI go.

## 2. The account

A self-service account is an `iam_v2.guest_access_accounts` row (the existing ACCOUNT subject) with
`self_registered = true` and `username = lower(trimmed email)`. It is **site-specific** (unique per tenant + site +
lower(username), migration 0117). It is **not** a guest principal: an email address — verified or not — never
becomes an identity factor, never links to a social/SMS Client, never joins a client group. The account is the
subject that owns entitlements; anyone who knows its password is that account (as for Personal accounts).

* Password: 8–128 characters, not all one character, not equal to the email; Argon2id (same parameters and
  verifier as Personal accounts); constant-time; the unknown-account path still hashes.
* Registration works with the appliance offline: the row, the sign-in and the package step are local. The
  verification email is queued and sent when delivery is possible (§4).
* After registration the client is signed in and goes straight to the packages; packages, prices and payment
  methods are the site's. Free packages follow their rules; paid ones require confirmed settlement.
* Returning clients sign in with email + password. Another device signs in and JOINS the account's live
  entitlement with its remaining time, data and device limit (existing ACCOUNT behaviour). Nothing resets quota.

## 3. Verification lifecycle

Per-site settings (§6). With verification required:

1. At registration `verification_deadline = now() + deadline_hours` (default 24 h) and a VERIFY token is queued.
2. Until the deadline the account works normally. Verifying never interrupts a connection.
3. After the deadline, unverified and with no live grace, the account is **suspended for Internet**: scd's sweep
   (every 30 s) ends the account's live sessions (the operator-disconnect path: nft deny, session ended) and
   activation is refused with `VERIFICATION_REQUIRED`. The entitlement itself is untouched: its window keeps
   running on the wall clock; data and online time do not accrue without sessions. Nothing is terminated.
4. The suspended client sees a page: verify (link in the email), **Resend verification email**, and
   **Allow Internet for 1 More Hour**.
5. **Grace**: requires the account's password again (fresh authentication). Grants `grace_until = now() +
   grace_minutes` once per account by default (`max_grace_extensions`, hard maximum 3). It only lifts the
   verification gate: no entitlement is created, extended or restored; an expired or exhausted package stays so.
   When it ends unverified the account is suspended again; no automatic further extension.
6. Verifying (`email_verified_at`) restores normal status at once; package time, data and ownership unchanged.
7. Policy `WARN_ONLY` never suspends (reminders only). Verification not required: no deadline, no suspension.

Reminders: one reminder email `reminder_hours_before` the deadline (0 = off).

## 4. Email links and tokens

* Tokens: 256-bit random, stored as HMAC-SHA256 (`account_email_tokens.token_hmac`, key derived from the
  appliance's local resume key with domain `account-email-token:v1`), single use, purpose `VERIFY` (lifetime =
  until the deadline, at least 24 h) or `RESET` (30 min). A new token of a purpose supersedes older unused ones.
* Link base: `https://<canonical portal hostname>` while HTTPS is active, otherwise `http://<portal address>`.
  The links work on the guest Wi-Fi (the portal is local); a GET shows a page whose button POSTs (mail scanners
  prefetch links).
* Delivery is an outbox: each token row records `sent_at`, `send_attempts`, `last_send_error`; a loop retries
  unsent rows with backoff while they are valid, so registration never waits for, or fails on, the mail server.
* Resend: allowed after `resend_cooldown_seconds` (default 60) and at most `resend_max_per_day` (default 5).

## 5. Abuse, enumeration, takeover

* **Squatting / takeover**: password reset sends a single-use link to the email; completing it proves control of
  the address, sets the new password, marks the email verified, revokes outstanding tokens and ends the account's
  live sessions (a squatter on someone else's address loses it). No other recovery exists (no security questions,
  no staff password reveal for self-service accounts).
* **Enumeration**: sign-in answers the same for unknown account and wrong password; "forgot password" always
  answers the same. Registration must say an address is already registered (the client cannot otherwise proceed);
  that answer is throttled like sign-in and offers sign-in / reset, nothing else.
* **Quota resets**: free packages keep their PRIOR_PURCHASE rules (by account and by device);
  `registrations_per_device_per_day` (default 3, hard maximum 10) caps new accounts per device.
* Throttles: the existing login limiter and durable throttle cover sign-in, registration, grace, resend and
  forgot (per IP, per device, per identity).

## 6. Settings (Hotel Admin → Sign-in methods → Email accounts)

`iam_v2.site_self_service_settings` (one row per site, defaults below), changed only through
`iam_v2.self_service_settings_set` with an append-only change log and a reason, step-up in the Admin Console.

| Setting | Default | Bounds |
|---|---|---|
| `verification_required` | true | — |
| `verification_deadline_hours` | 24 | 1–168 |
| `suspension_policy` | `SUSPEND` | `SUSPEND`, `WARN_ONLY` |
| `reminder_hours_before` | 4 | 0–72, < deadline |
| `resend_cooldown_seconds` | 60 | 30–3600 |
| `resend_max_per_day` | 5 | 1–20 |
| `grace_minutes` | 60 | 15–240 |
| `max_grace_extensions` | 1 | 0–3 |
| `registrations_per_device_per_day` | 3 | 1–10 |

## 7. Interfaces (scd local socket; portald is the only caller of the guest routes)

All guest routes take the device as portald derives it (`ip`, `mac` from the kernel). Replies never contain a
password, hash or token. `account_token` is an scd-issued HMAC token (`account_id`, device MAC, expiry 15 min)
that portald keeps in an HttpOnly cookie; it is the "recently authenticated" proof for resend / grace / status.

* `POST /v1/auth/account/register {email,password,ip,mac,lang}` → 200 `{auth_context_id, device_id,
  guest_network_id, method:"ACCOUNT", live_entitlement_id:null, account_token, account}`; 409 `email_taken`;
  422 `invalid_email|weak_password`; 429 `TOO_MANY_ATTEMPTS|registration_limit`; 403 `METHOD_DISABLED`.
* `POST /v1/sessions/authorize-credentials {username,password,ip,mac}` (existing) — for a self-registered account
  requires `email_account` enabled (staff accounts keep `guest_account`); reply gains `account_token` and
  `account`. A suspended account still authenticates (reply `account.suspended=true`); activation refuses it.
* `account` = `{verified, verification_required, deadline, suspended, grace_until, grace_available,
  resend_available_in}`.
* `POST /v1/auth/account/status {account_token,ip,mac}` → `{account}`.
* `POST /v1/auth/account/resend {account_token,ip,mac}` → 202 | 429 `resend_wait{retry_after_seconds}|resend_limit`.
* `POST /v1/auth/account/grace {email,password,ip,mac}` → 200 `{auth_context_id,…,account_token,account}`
  | 409 `grace_unavailable|not_suspended` | 401 `invalid_credentials`.
* `POST /v1/auth/account/verify {token}` → 200 `{verified:true}` | 410 `invalid_or_used`.
* `POST /v1/auth/account/forgot {email,ip,mac}` → always 202.
* `POST /v1/auth/account/reset {token,password}` → 200 | 410 | 422 `weak_password`.
* Activation (`activateIAMv2Session`) refuses an ACCOUNT subject whose account is suspended:
  `VERIFICATION_REQUIRED`.
* Admin (edged): `GET/PUT /v1/admin/self-service-settings` via the definer functions; accounts list shows
  self-registered + verification state; `POST /v1/admin/accounts/{id}/resend-verification`.

## 8. Verified on PRE-LIVE (2026-10-15) and what remains

Migration 0117 applied on PRE-LIVE after a database backup (the email switch converted to `email_account`), Gate-P
grants reconciled. Guest-device test `scripts/email-accounts-e2e-test.sh` 24/24 (see the acceptance checklist §F);
regression suites 18/18, 12/12, 15/15. Found and fixed during the run: a second device on a 1-device account
reached the database's own device-limit refusal and saw "could not bring your device online"; it is now answered
as the device limit.

**Device slots and sign-out (existing behaviour, unchanged).** An admitted device keeps its slot after it signs
out; another device takes the slot only through the guest device release (Phase-6 device self-service, dark on
PRE-LIVE). So on a 1-device package a client cannot move to a new device on their own; on a multi-device package
they join. Whether sign-out should free the slot is a product decision (it trades convenience against sequential
sharing of one package), not changed here.

Not yet verified on a real device: following the verification and reset links from a real mailbox, Chrome/Edge's
login tab from an `https://` page, Windows.
