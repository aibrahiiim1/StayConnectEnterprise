# Phase 1B Planning Evidence — Guest Authentication & Portal/Session Inventory

Read-only code inventory (source baseline `afade95`). Input to Phase-1B plan §3.

## Pipeline
`portald` (captive portal; `:8380`/`:8343`; resolves client MAC from ARP; **no DB, no credential logic**; proxies each credential over the Unix socket `/run/stayconnect/scd.sock`) → `scd` (owns `sessions`, nft `auth_ipv4` set, credential validation, license gating, session issuance; Unix-socket only). `acctd` enforces quotas (not an auth entry). `edged`+`hotel-admin` = admin plane (config), not the guest path.

Shared pipeline: `licenseGate → validate credential → (optional per-credential max-devices reserve) → atomic licensed-capacity gate + sessions INSERT → nft Allow → tc shaping → record network → metrics`; any post-insert failure rolls back nft/tc + `sess.End`.

## Auth paths (confirmed in code)
| Path | portald → scd | Legacy tables | Validation | Session issue | Offline | Notes |
|---|---|---|---|---|---|---|
| **Voucher** | `/auth/voucher` → `/v1/sessions/authorize` (`scd/main.go:296-389`) | `vouchers`⋈`ticket_templates`; `guests`,`sessions` | Crockford normalize; state machine; wall-clock window from first activation; aggregate cap (`voucher/voucher.go`) | `session.Manager.Start` (`reserveDeviceSlot("voucher_id")`) | **fully offline** | plaintext `code`, `UNIQUE(tenant,code)` |
| **Account** | `/auth/credentials` → `/v1/sessions/authorize-credentials` (`credentials_handlers.go:67`) | `guest_accounts`,`guests`,`sessions`,`ticket_templates` | **argon2id** constant-time; dummy-hash anti-enum; 5-fail→15m lockout; generic error; layered in-proc throttle (`credentials_ratelimit.go`) | `StartGuestAccount` (`reserveDeviceSlot("guest_account_id")`) | fully offline | 1-char passwords allowed |
| **OTP** | `/auth/otp/request`+`/verify` → `/v1/auth/otp/issue`,`/v1/sessions/authorize-otp` (`otp_handlers.go`) | `auth_otps`,`guests`,`sessions`,`tenants.auth_methods` | 6-digit; `SHA-256(salt\|code)`; TTL 10m; 5-attempt; cooldown/hourly/IP caps (`otp/otp.go`) | `StartOTP` | verify offline; **delivery needs SendGrid/Twilio** (or logging Stub) | migration comment says argon2id but code is SHA-256 |
| **Social** | `/auth/social/start`+`/callback` → `/v1/auth/social/start`,`/v1/sessions/authorize-social` (`social_handlers.go`) | `social_oauth_states`,`social_oauth_providers`,`guests`,`sessions` | single-use CSRF state (FOR UPDATE); IP+MAC binding; provider exchange; **verified email required** | `StartOTP` channel email | **not offline** | default **Stub** unless real provider row; only Google impl real |
| **PMS** | `/auth/pms/verify` → `/v1/auth/pms/verify` (`pms_handlers.go`) | `pms_attempts`,`pms_providers`,`guests`,`sessions` | per-IP + per-room lockout; provider `ValidateGuest` (FIAS GI/GC cache); stay-window; caps session to checkout | `StartPMS` (drops room/reservation) | FIAS cache local; Mews/Apaleo cloud | **Phase 3, NOT 1B** |
| **Paid** | — | — | — | — | — | **ABSENT** (Stripe = operator billing only) |

## Shared facts
- MAC (from ARP; nft-DNAT preserves source IP) = device correlator (`guests` unique `(tenant,mac)`) + device-slot key.
- `reserveDeviceSlot` (`session.go:73-102`): per-credential `pg_advisory_xact_lock`, credential-first→appliance-second, idempotent reconnect, `MAX_DEVICES_REACHED` (voucher+account only).
- `gateCapacity` (`session.go:125-150`): per-appliance advisory lock + `count(*) active` in insert tx from **local signed license** `MaxConcurrentOnlineGuests` (offline).
- **Tenant/site = signed ASSIGNMENT** (identity.json + verified keypair; bootstrap-token; legacy env fallback), re-verified each boot; before assignment guest plane disabled.
- Source IP → `guest_networks` row (longest-prefix) → `{NetworkID,VLANID,Bridge,GatewayIP}` (nft/tc target, not tenant).
- Central control plane **never** in the guest-authorization path (supplies signed assignment + license, receives outbox telemetry).
- Reaper (30s) closes expired/idle sessions; `end_reason` CHECK-constrained.

## Legacy public-schema tables (guest-auth domain)
`ticket_templates`, `vouchers` (plaintext code), `guests` (tenant,mac; email/phone+verified_at), `sessions` (state pending/active/suspended/closed), `guest_accounts` (argon2id; lockout), `auth_otps` (channel email/sms; `salt:sha256`), `social_oauth_states`, `social_oauth_providers`, `pms_attempts`, `pms_providers`, `guest_networks` (+dhcp/interfaces), `tenants.auth_methods` (jsonb), `plan_limits`/`tenant_effective_limits`.

## Legacy↔iam_v2 divergences (reconcile in code)
- `guests(tenant,mac)` conflates device+person → split into `devices` (MAC) + `guest_principals`/`guest_principal_identities` (verified factors; MAC never a factor).
- Voucher `code` plaintext → `code_hmac` blind index + AEAD ciphertext + `voucher_code_key_generations`.
- OTP challenge (`auth_otps`) + social CSRF (`social_oauth_states`) have **no iam_v2 equivalent** — ephemeral; keep in `public` (plan D2).
- `iam_v2.sessions` needs `entitlements.purchase_id` (even free grants need a zero-amount purchase) → session-after-grant coupling (plan D3).

## Flagged gaps/risks
Paid auth absent; social defaults to Stub; OTP is SHA-256 not argon2id; in-process throttles reset on restart; PMS `StartPMS` drops room/reservation; direct-Postgres coupling (planned SQLite+NATS split not implemented).
