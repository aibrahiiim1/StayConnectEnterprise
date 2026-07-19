# StayConnect IAM — Phase 1B Implementation Plan (Credential/Portal Integration, DARK)

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0015 -->
**Current phase:** 3 — PMS Stay Domain, STRICT Multi-PMS Resolution, Room Movement, Checkout Grace and Reinstatement
**Current activity:** `PHASE_3_IMPLEMENTATION_IN_PROGRESS`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 IN_PROGRESS · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Execute the authorized Phase 3 end-to-end as one Phase, DARK, per docs/architecture/StayConnect-IAM-Phase3-Plan.md, then return one final Phase-3 acceptance report at verified DARK maturity. No Phase 4, no PMS financial posting, no IAM-v2 cutover.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D14`.
<!-- END GENERATED PROJECT STATE -->


**Status: ACCEPTED AND CLOSED at DARK maturity (transition T0011, decision D11, 2026-07-17).** Phase 1B implementation was Product-Owner authorized (decision D10, 2026-07-17), executed and live-dark deployed (T0010), and then formally **Product-Owner ACCEPTED AND CLOSED at DARK maturity** (T0011/D11); the live current-state is the GENERATED PROJECT STATE block above (rendered from `governance/project-state.json`). Delivery is on branch `phase/1b-dark-auth` / **PR #2**. Acceptance is at DARK maturity **only**: it does **not** authorize any Production cutover, any Production `iam_v2` runtime read or write, service routing to `iam_v2`, dual-read/write, IAM-v2 data migration, PMS/FIAS traffic, financial posting, network change, enabling any dark feature, or any later Phase — every dark feature remains OFF. **Legacy Production authentication (`public` schema) remains the sole authority throughout Phase 1B.** Machine sentinel: `PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE` — no Production runtime `iam_v2` read, write, shadow evaluation, or rolled-back transaction; all functional `iam_v2` adapter/repository/engine execution occurs in scratch/test only.

**Baseline this plan builds on (verified):** Phase 0 FINAL/CLOSED; Phase 1A **formally Product-Owner ACCEPTED and CLOSED** at `SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER`; production `iam_v2` = 49 empty tables (fingerprint `bd75026f`), no service reads/writes it, no DSN/`search_path` routing, no data migration; at the Phase-1A baseline all services connected as PostgreSQL superuser `stayconnect` (**since eliminated by the Phase 1B Gate-P cutover — all four site-DB daemons now run under least-privilege `svc_*` roles, T0011**). **Phase 1B is credential/identity/auth-context implementation in DARK/flags-OFF mode — it is NOT a cutover** (the atomic complete-domain cutover is a later separately approved gate, only after Phases 2–6 and full-domain acceptance). Ladder reference: [Phase-1A Plan §7a/§11](StayConnect-IAM-Phase1A-Plan.md) (Phase 1B = ladder steps 7–10, dark/flagged, **before** any cutover at step 11).

---

## 1. Exact Phase 1B scope

Scope is derived from FINAL contract §18 (Phase 1B row), §4.4/§4.5/§4.6, §19 B-series acceptance, the verified `iam_v2` schema, and the **current running code** (inventoried §3). Contract §18 Phase 1B = *"Auth contexts; voucher (HMAC/AEAD), account, OTP/social (guest principals) re-pointed; session-after-grant portal flow."* PMS is **not** listed in Phase 1B (it needs the stay domain — Phase 3); packages/quotes/pricing are Phase 2; posting is Phase 4.

**IN Phase 1B (build against `iam_v2`, dark/flagged, no guest-visible decision):**
- Least-privilege database access (the mandatory prerequisite — §2).
- Credential validation + subject resolution against `iam_v2` for the four contract-listed methods: **VOUCHER** (HMAC/AEAD), **ACCOUNT** (argon2id), **OTP** (email/SMS → `guest_principal_identities`), **SOCIAL** (OAuth → `guest_principal_identities`).
- `guest_principals` / `guest_principal_identities` resolution (tenant-wide, MAC-is-never-a-factor) and `devices` registry (MAC = device).
- `auth_contexts` creation (one-time, TTL, method↔subject coherence) for those four methods.
- The **session-after-grant** adapter interface to the `iam_v2` engine (`reserve_device_slot`, `ingest_sample`, `close_session`, entitlement guard) **behind flags**, exercised in **scratch/test ONLY**. In Production this code is present but flag-OFF, privilege-denied, and never invoked — **no read, no write, no shadow evaluation, no rolled-back transaction** against `iam_v2`.
- Feature-flag + kill-switch infrastructure (§5); observability (§11); acceptance (§12).

**EXPLICITLY EXCLUDED from Phase 1B (map to later Phases; do not build here):**
| Excluded behavior | Reason / owning Phase |
|---|---|
| PMS room-lookup auth (method `PMS`) | Requires `stays` + `pms_interface_revisions` populated — **Phase 3** (stay domain/STRICT resolution). |
| Post-stay PIN (method `POST_STAY_PIN`) | Requires `post_stay_profiles` — **Phase 5**. |
| Package selection, offer quotes, pricing, purchases as commerce | **Phase 2** (`offer_quotes`, paid `purchases`, `settlements`). |
| Financial posting / outbox / `P#` | **Phase 4**; folio `UNSET` stays fail-closed. |
| Programmatic reversal | Deferred (capability=false); separate spike. |
| Paid-access guest authentication | **Not implemented in legacy at all** (confirmed absent). Net-new; out of Phase 1B; requires its own PO decision. |
| Guest-visible activation / `search_path`/DSN cutover / legacy cleanup | Separate PO approvals (ladder steps 11–14). |

**Per-behavior scope map** (every proposed Phase 1B behavior → legacy → contract → `iam_v2` object → new work → owner → flag → acceptance → rollback boundary) is maintained as `docs/architecture/Phase1B-Scope-Map.md`-style table, reproduced in Appendix A (§16 export). Summary rows:

| Behavior | Legacy today | Contract | iam_v2 object | New code/migration | Service owner | Flag | Acceptance | Rollback |
|---|---|---|---|---|---|---|---|---|
| Voucher validate | `voucher.Validate` on `public.vouchers` (plaintext `code`, `UNIQUE(tenant,code)`) | §4.4 vouchers (HMAC/AEAD) | `iam_v2.vouchers` (`code_hmac`, AEAD ciphertext), `voucher_code_key_generations` | new `iam_v2` voucher validator (blind-index HMAC lookup); no schema change | scd | `iam_v2.auth.voucher` | B1/B2 | flag OFF |
| Account validate | `authorizeGuestAccount` argon2id on `public.guest_accounts` | §4.4 accounts | `iam_v2.guest_access_accounts` | new `iam_v2` account validator (reuse argon2id verifier + lockout) | scd | `iam_v2.auth.account` | B3/B5 | flag OFF |
| OTP verify → identity | `auth_otps` verify → `guests` email/phone stamp | §4.4 identities | `iam_v2.guest_principals` + `guest_principal_identities` (EMAIL/PHONE) | resolve/create principal+identity from a verified OTP; **`auth_otps` challenge table stays in `public`** (decision D2) | scd | `iam_v2.auth.otp` | B4 | flag OFF |
| Social callback → identity | `social_oauth_states` + provider → `guests` by email | §4.4 identities (issuer-scoped) | `guest_principal_identities` (SOCIAL_SUBJECT, issuer-scoped) | resolve/create principal from `(issuer, subject)`; **`social_oauth_states` stays in `public`** | scd | `iam_v2.auth.social` | B4 | flag OFF |
| auth_context create | (implicit; legacy has no auth_context table) | §4.5 auth_contexts | `iam_v2.auth_contexts` | new one-time context writer (method↔subject CHECK enforced by DB) | scd | (per-method flag) | B6 | flag OFF |
| Device registry | `guests(tenant,mac)` conflated device+person | §4.6 devices | `iam_v2.devices` + `device_network_appearances` | MAC→device upsert; principal separate | scd | (bundled) | B4 (MAC≠owner) | flag OFF |
| Session-after-grant (adapter, scratch-only) | `session.Manager.Start*` on `public.sessions` | §4.6 sessions/entitlements | `iam_v2.sessions` + `entitlements` + `entitlement_devices` (+engine) | adapter **interface** to the `iam_v2` engine, exercised **scratch/test ONLY**; Production is flag-OFF, privilege-denied, never invoked (**zero `iam_v2` read/write/shadow**) | scd | `iam_v2.session.adapter` | B-series in scratch | flag OFF |

**Anti-hybrid rule (inherited from Phase-1A §7a/§8):** Phase 1B does **not** create a per-flow/per-service split source of truth. In production, the legacy `public` schema remains the sole authority for real guest sessions throughout Phase 1B; `iam_v2` is exercised **only in scratch/test**. In Production no runtime service reads or writes `iam_v2` at all — **no shadow evaluation and no rolled-back transaction**. A real switch of authority is the **cutover** (ladder step 11+, separate approval).

---

## 2. MANDATORY PREREQUISITE — least-privilege database access

**Gate P (must pass its own acceptance before any Phase-1B runtime routing to `iam_v2`).** Today every service connects as superuser `stayconnect`, so `iam_v2` grant isolation cannot bind them; darkness rests only on zero code refs + `search_path`. Phase 1B **cannot** safely route any service to `iam_v2` until per-service least-privilege LOGIN roles replace the superuser DSNs and pass negative-permission tests.

### 2.1 DB-connecting service inventory (from code)
| Service | DB | Today (user) | In `iam_v2` scope? | Notes |
|---|---|---|---|---|
| `scd` | site `stayconnect_site` | `stayconnect` (superuser) | **YES** (auth/session/credential) | env `SCD_DB_URL` (`data-plane/cmd/scd/main.go:112`) |
| `edged` | site | `stayconnect` (superuser) | **YES** (admin CRUD of credentials/accounts/vouchers) | env `EDGED_DB_URL` (`edged/main.go:57`) |
| `acctd` | site | `stayconnect` (superuser) | **YES** (session usage / accounting) | env `ACCTD_DB_URL` (`acctd/main.go:47`) |
| `netd` | site | `stayconnect` (superuser, root OS) | no (networking public tables) | de-superuser as part of Gate P hygiene, but no `iam_v2` grant |
| `portald` | — | none (dials scd Unix socket) | no | **no DB role needed** |
| `hotel-admin` (Next.js) | — | none (HTTP→edged) | no | **no DB role needed**; the `iam_v2_svc_hoteladm` skeleton role is a misnomer (see §10-D6) |
| `ctrlapi`, `nats-authz` | central `stayconnect` | `stayconnect` (superuser) | no (central DB, different Phase) | out of Phase-1B scope; flagged for a future central-DB hardening |
| migrations (Makefile) | site+central | `stayconnect` (superuser) | migrator role | applied via `psql -U stayconnect`; replace with `iam_v2_migrator`/site-migrator |
| `sitemigrate` | central→site | operator-supplied | maintenance | one-shot; keep operator-supplied, never a service login |

### 2.2 Per-service role design (site DB)
Ownership/migration/service separation (extends the existing `iam_v2_scratch/roles.sql` skeleton, which today defines `iam_v2_owner`, `iam_v2_migrator`, and NOLOGIN `iam_v2_svc_*` placeholders):

- `iam_v2_owner` — **NOLOGIN**, owns schema + all `iam_v2` objects (already the case in production). Never a runtime login.
- `iam_v2_migrator` — **NOLOGIN**, member of `iam_v2_owner` (runs migrations via `SET ROLE`), separate from every service role.
- `svc_scd` (**LOGIN**) — the runtime role scd connects as. Grants:
  - PUBLIC (legacy, needed today): the exact scd read/write set from the DB-access map (`sessions`, `guests`, `guest_accounts`, `vouchers`, `auth_otps`, `social_oauth_*`, `pms_*`, `sync_outbox`, `sync_checkpoints`, `tenant_effective_limits`, `guest_networks`, `audit_log`, …) — `SELECT`/`INSERT`/`UPDATE` per column-family, `USAGE` on sequences it writes; no `DELETE` unless the code path needs it.
  - `iam_v2`: **ZERO in Phase 1B** — no schema `USAGE`, no table/sequence privilege, no function `EXECUTE`, no DML, no read. (Future cutover grants are **design-only**; see the *FUTURE DESIGN — NOT GRANTED OR APPLIED IN PHASE 1B* appendix at the end of this document. They are never created or applied in Phase 1B.)
- `svc_edged` (**LOGIN**) — admin CRUD grants on `public` (its current broad admin set) only; **ZERO `iam_v2` privileges** in Phase 1B (future cutover grants: FUTURE DESIGN appendix — not created or applied here).
- `svc_acctd` (**LOGIN**) — narrow: `public.sessions` (SELECT/UPDATE), `public.accounting_records` (INSERT); **ZERO `iam_v2` privileges** in Phase 1B (future cutover grants: FUTURE DESIGN appendix — not created or applied here).
- `svc_netd` (**LOGIN**) — networking `public` tables only; **ZERO `iam_v2` grant**.

For **each** service role, the plan specifies (per PO prompt §2): current user & DSN source; exact tables/schemas needed; read/write matrix; dedicated role; LOGIN/NOLOGIN & ownership; schema `USAGE`; table/sequence/function privileges; `ALTER DEFAULT PRIVILEGES` so future owner-created objects are denied to service roles by default; `CONNECTION LIMIT`; `statement_timeout`/`lock_timeout`/`idle_in_transaction_session_timeout` set via `ALTER ROLE … SET`; credential storage (see 2.3); rotation (2.4); deployment order (2.5); rollback (2.6); reboot persistence; negative tests (2.7); audit/monitoring (§11).

### 2.3 Credential storage
- Per-service DSNs move to per-service systemd `EnvironmentFile`s (`/etc/stayconnect/{scd,edged,acctd,netd}.env`, already the deployment shape) with **mode 0600, owner root**, containing `*_DB_URL=postgres://svc_<name>:<secret>@127.0.0.1/stayconnect_site`.
- Secrets are random ≥32-byte, generated on the appliance; **never** in Git, exports, logs, telemetry, or process arguments (DSNs passed via env only; `redactDSN`-style masking already exists in `sitemigrate`). Loopback-only Postgres, `sslmode=disable` acceptable on loopback (documented).
- Optional hardening: Postgres `SCRAM-SHA-256` password auth in `pg_hba.conf` scoped to loopback per role.

### 2.4 Rotation procedure
`ALTER ROLE svc_<name> PASSWORD '<new>'` → write new `*_DB_URL` to the env file (0600) → `systemctl restart stayconnect-<svc>` (rolling, one service at a time) → verify healthy connection → invalidate old secret. Rotation is idempotent, scripted, audited (who/when, not the secret), and reboot-persistent (env files survive reboot). No secret ever printed.

### 2.5 Deployment order (Gate P)
1. Create roles + grants (owner via migration as `iam_v2_migrator`/superuser one-time), all NOLOGIN service roles first, then set LOGIN + passwords.
2. Apply `public`-schema grants matching each service's current needs.
3. Write per-service env files (0600).
4. Restart services **one at a time**, each verifying it connects and serves traffic under the new non-superuser role (guest plane must not regress).
5. Confirm no service is superuser; confirm PUBLIC has no unintended privileges; run negative tests (2.7).

### 2.6 Rollback (Gate P)
Point the service env file back to the superuser DSN and restart that one service; roles are additive and can be left in place or dropped. No `iam_v2` routing occurred, so there is no data divergence. Rollback is per-service and reversible.

### 2.7 Negative-permission acceptance (must pass)
- Each `svc_*` role: `rolsuper=false`, `rolbypassrls=false`.
- `svc_scd` cannot `SELECT`/`INSERT`/`UPDATE`/`DELETE` on tables outside its grant set; cannot read another service's restricted data (e.g. `svc_acctd` cannot read voucher secrets; `svc_netd` cannot touch credentials).
- No service role can `DROP`/`ALTER` `iam_v2` objects; schema owner is not a LOGIN role.
- PUBLIC has zero privileges on `iam_v2` and no unintended privileges on new `public` objects (`ALTER DEFAULT PRIVILEGES` verified).
- Superuser elimination verified: `SELECT rolname,rolsuper FROM pg_roles WHERE rolname LIKE 'svc_%'` all false; running backends (`pg_stat_activity`) show the service roles, not `stayconnect`.
- Reboot: after appliance reboot, services reconnect under their least-privilege roles; guest auth still works.

**No service may route to `iam_v2` until Gate P passes.** Gate P is independent of and precedes the credential/portal code activation.

### 2.8 Production grants — ZERO iam_v2 DML (binding)
During Phase-1B **production**, service roles receive **only** the exact privileges required for their **current legacy `public`** behavior:
- Service roles receive **zero `iam_v2` DML** (no SELECT/INSERT/UPDATE/DELETE on any `iam_v2` table) and **no** `iam_v2` `EXECUTE`.
- PUBLIC receives **zero** `iam_v2` privileges.
- **No production service is capable of writing to `iam_v2`** (reinforces D1).
- Granting any `iam_v2` privilege to a runtime service role requires a **later separate runtime-routing authorization** (the cutover gate), never Phase 1B.
- Only **scratch/test** roles receive the `iam_v2` privileges needed for acceptance testing.
- No service role ever receives broad `ALL TABLES`, owner membership, `CREATE`/`ALTER`/`DROP`, `BYPASSRLS`, or superuser.

### 2.9 Exact privilege matrix (machine-reviewable)
The complete, ellipsis-free grant matrix — **service role · database/schema · table · columns (where applicable) · SELECT · INSERT · UPDATE · DELETE · sequence USAGE/SELECT · function EXECUTE · reason/source-code-path · negative-permission test · rollback** — is maintained as **[`docs/architecture/Phase1B-Privilege-Matrix.md`](Phase1B-Privilege-Matrix.md)** (bundled in the planning pack). It is derived from the completed DB-access inventory and exact query/code inspection, and is the authoritative grant specification for Gate P. Production rows grant **only** `public`-schema privileges (zero `iam_v2`); scratch rows add the `iam_v2` privileges for acceptance.

### 2.10 Migration executor (non-runtime)
Migrations are **never** run by a runtime service role and **never** depend on permanent use of the `stayconnect` superuser:
- `iam_v2_owner` — **NOLOGIN**, owns schema + objects.
- `iam_v2_migrator` — **NOLOGIN**, `MEMBER OF iam_v2_owner` (applies `iam_v2` migrations via `SET ROLE iam_v2_owner`); a parallel **NOLOGIN** `site_migrator` owns/applies `public`-schema migrations.
- A separate **controlled migration-executor LOGIN role** (`migrate_exec`) or an equally explicit secure operator mechanism is used to *drive* migrations: it is `MEMBER OF` the relevant NOLOGIN migrator role and uses `SET ROLE`; it holds **no** runtime service privileges.
- **Credential storage:** `migrate_exec` secret in a 0600 root-only operator env, **enabled only for the migration window** (time-limited); **disabled/removed after use**; every migration run **audited** (who/when/what); secret **rotated** after use.
- **Rollback:** migration down-scripts run through the same executor; on failure, drop the migration's objects; the executor role is disabled either way. **Do not** leave the migration path dependent on `stayconnect` superuser.

### 2.11 Superuser rollback = break-glass only (retained, with removal gate)
Returning a service to the `stayconnect` **superuser** DSN exists **only** as an **emergency break-glass rollback during Gate P**:
- **Time-bounded**, **audited**, and **explicitly approved by the execution runbook** at the moment of use.
- **Followed by credential revocation/removal** once Gate-P acceptance is re-established under the least-privilege role.
- Classified **`RETAINED for rollback` with an explicit removal gate** (removed at Gate-P completion); it is **not** a normal long-term operating option and must never be the steady state.

---

## 3. Current authentication & portal inventory (from code — read-only)

Two-daemon pipeline: **portald** (captive portal; `:8380`/`:8343`; resolves MAC from ARP; **no DB, no credential logic**; proxies to scd over Unix socket `/run/stayconnect/scd.sock`) → **scd** (owns `sessions`, nft `auth_ipv4` set, credential validation, license gating, session issuance; Unix-socket only). **acctd** enforces quotas (not an auth entry point). **edged**+**hotel-admin** are the admin plane (config), not the guest path.

All auth handlers converge on one shared pipeline: `licenseGate → validate credential → (optional per-credential max-devices reserve) → atomic licensed-capacity gate + sessions INSERT → nft Allow → tc shaping → record network → metrics`; any post-insert failure rolls back nft/tc and calls `sess.End`.

| Path | Entry (portald→scd) | Legacy tables | Validation | Session issue | Offline | Notes/legacy |
|---|---|---|---|---|---|---|
| **Voucher** | `/auth/voucher` → `/v1/sessions/authorize` | `vouchers`⋈`ticket_templates`; `guests`,`sessions` | Crockford-normalize code; state machine; wall-clock window from first activation; aggregate cap | `Start` (upsert guest by MAC; `reserveDeviceSlot("voucher_id")`; capacity gate) | **fully offline** (local PG + local license) | plaintext `code` + `UNIQUE(tenant,code)` — no HMAC yet |
| **Account** | `/auth/credentials` → `/v1/sessions/authorize-credentials` | `guest_accounts`,`guests`,`sessions`,`ticket_templates` | **argon2id** (constant-time; dummy-hash anti-enum; 5-fail→15m lockout; generic error) + layered in-proc throttle | `StartGuestAccount` (`reserveDeviceSlot("guest_account_id")`) | fully offline | 1-char passwords allowed → extra limiter; throttle is process-local |
| **OTP** | `/auth/otp/request`+`/verify` → `/v1/auth/otp/issue`,`/v1/sessions/authorize-otp` | `auth_otps`,`guests`,`sessions`,`tenants.auth_methods` | 6-digit; `SHA-256(salt\|code)` (fast, short TTL, 5-attempt cap); TTL 10m; cooldown/hourly/IP caps | `StartOTP` (no per-cred device cap; capacity only) | **verify offline; delivery needs SendGrid/Twilio** (or logging Stub) | migration comment says "argon2id" but code is SHA-256 (verify intent) |
| **Social** | `/auth/social/start`+`/callback` → `/v1/auth/social/start`,`/v1/sessions/authorize-social` | `social_oauth_states`,`social_oauth_providers`,`guests`,`sessions` | single-use CSRF state (FOR UPDATE), IP+MAC device binding, provider exchange, **requires verified email** | `StartOTP` channel email (attach by verified email) | **not offline** (provider exchange); Stub is local-only | default **Stub** unless a real provider row; only Google impl real |
| **PMS** | `/auth/pms/verify` → `/v1/auth/pms/verify` | `pms_attempts`,`pms_providers`,`guests`,`sessions` | per-IP + per-room lockout; provider `ValidateGuest` (FIAS GI/GC cache); stay-window; caps session to checkout | `StartPMS` (drops room/reservation — audit gap) | FIAS cache local (offline while link recently up); Mews/Apaleo cloud | **Phase 3, not 1B** |
| **Paid** | — | — | — | — | — | **ABSENT**; `ticket_templates.price_cents` unused; Stripe is operator billing only |

**Shared pipeline facts:** MAC (from ARP; nft-DNAT preserves source IP) is the device correlator (`guests` unique `(tenant,mac)`) and device-slot key. `reserveDeviceSlot` = per-credential `pg_advisory_xact_lock`, credential-first then appliance-second lock order, idempotent reconnect (closes device's prior active session, no double-count), `MAX_DEVICES_REACHED` at limit (voucher+account only). `gateCapacity` = per-appliance advisory lock + `count(*) active` in the insert tx from the **local signed license** `MaxConcurrentOnlineGuests` (offline). **Tenant/site = signed ASSIGNMENT document** (identity.json + verified keypair; bootstrap-token; legacy env only as migration fallback), re-verified every boot; before assignment the guest plane is disabled. Source IP → `guest_networks` row (longest-prefix) → `{NetworkID,VLANID,Bridge,GatewayIP}` (nft/tc target, not tenant). Central control plane is **never** in the guest-authorization path (supplies signed assignment + signed license + receives outbox telemetry only). Reaper (30s) closes expired/idle sessions.

**Legacy public-schema tables (guest-auth domain):** `ticket_templates`, `vouchers` (plaintext `code`, `UNIQUE(tenant,code)`, states unused/active/exhausted/expired/revoked), `guests` (tenant,mac; email/phone + verified_at), `sessions` (state pending/active/suspended/closed; end_reason CHECK), `guest_accounts` (argon2id; lockout), `auth_otps` (channel email/sms; `salt:sha256`), `social_oauth_states`, `social_oauth_providers`, `pms_attempts`, `pms_providers`, `guest_networks` (+dhcp/interfaces), `tenants.auth_methods` (jsonb per-method), `plan_limits`/`tenant_effective_limits`. (Full column lists in the Phase-1B planning pack inventory.)

**iam_v2 legacy↔target divergences to reconcile in code (not data):**
- `guests(tenant,mac)` conflates **device + person** → `iam_v2` splits into `devices` (MAC) + `guest_principals`/`guest_principal_identities` (verified factors; MAC never a factor). B4 enforces MAC≠owner.
- Voucher `code` plaintext → `code_hmac` (blind index) + AEAD ciphertext (reveal/print) + `voucher_code_key_generations`.
- OTP challenge (`auth_otps`) and social CSRF (`social_oauth_states`) have **no `iam_v2` equivalent** — they are ephemeral operational state, not identity. Decision D2 keeps them in `public`; `iam_v2` receives only the resolved `guest_principal_identity` + `auth_context`.
- `sessions` in `iam_v2` requires an `entitlement` which requires a `purchase` (`entitlements.purchase_id NOT NULL UNIQUE`) — even a free grant needs a zero-amount purchase row (`trigger` ACCOUNT_AUTO_GRANT / VOUCHER_REDEMPTION). This couples "session-after-grant" to a minimal grant/purchase path — see §6/§10-D3 boundary decision.

---

## 4. Target Phase 1B architecture

Preserved invariants (must not regress): local-first Edge; **signed assignment** as the only tenant/site identity source; no hardcoded `tenant_id`/`site_id`; one Edge → many independent PMS Interfaces; room numbers scoped by PMS Interface; **no guest-facing PMS selector**; no fake managed state / invented protocol behavior; **no financial posting in 1B**; no programmatic reversal; `folio_identity_strategy='UNSET'` stays financially fail-closed.

Target flow (dark/flagged; per-method):
1. **portald** unchanged in structure: resolves MAC (ARP), proxies credential over the Unix socket. No DB, no `iam_v2` awareness. (Its login-tab list comes from scd's license-filtered `/api/auth-methods`.)
2. **scd owns credential validation and authentication-context creation.** A new `iam_v2` auth adapter, selected per-method by flag, validates the credential against `iam_v2` credential tables and creates an `iam_v2.auth_contexts` row (method↔subject enforced by DB CHECKs `ac_one_subject`/`ac_method_subject`/`ac_pms_pins`). **This adapter is exercised in scratch/test ONLY; in Production it is flag-OFF and privilege-denied and is never invoked (no `iam_v2` read/write).** Subject resolution (scratch/test):
   - VOUCHER → `iam_v2.vouchers` by `code_hmac` (compute HMAC with the active `voucher_code_key_generations`); subject `voucher_id`.
   - ACCOUNT → `iam_v2.guest_access_accounts` by `(tenant, lower(username))`; argon2id verify; subject `guest_account_id`.
   - OTP → verify challenge in `public.auth_otps` (unchanged), then resolve/create `guest_principals`+`guest_principal_identities` (EMAIL/PHONE, tenant-wide); subject `guest_principal_id`.
   - SOCIAL → verify state in `public.social_oauth_states` + provider exchange (unchanged), then resolve/create identity `(SOCIAL_SUBJECT, issuer, subject)` issuer-scoped; subject `guest_principal_id`.
   - Device: MAC → `iam_v2.devices` upsert; `device_id` stamped on the auth_context; `device_network_appearances` records the guest_network.
3. **Portal → auth service call:** unchanged transport (portald→scd Unix socket). scd's response envelope is unchanged (`{session_id, guest_id, duration_seconds, expires_at}`). In Production dark mode the **legacy** path is the sole path that produces the real session; the `iam_v2` adapter is **flag-OFF and not invoked in Production** (no read, no write, no shadow, no rolled-back tx). The adapter/engine comparison ("would-be auth_context/grant") is exercised in **scratch/test only**.
4. **Tenant/site/PMS-interface context** derived exactly as today (signed assignment; IP→guest_network). No new identity source.
5. **Transaction boundaries (scratch/test):** auth_context creation is a single tx; the grant/session computation uses the `iam_v2` engine functions (`reserve_device_slot`, capacity via advisory namespaces `LN_DEVICE_SLOT=11`/`LN_CAPACITY=7`, `ingest_sample`, `close_session`). **In Production there is no `iam_v2` transaction at all — no read, no write, and no rolled-back transaction (D1).** All engine execution occurs in scratch/test only.
6. **Idempotency keys:** auth_contexts one-time (`consumed_at`), TTL 10m; device slot idempotent reconnect (no slot burn); session close idempotent (`ALREADY_ENDED`); accounting watermarked `(session,seq)`.
7. **Retry/rate-limit/brute-force:** reuse existing layered throttles (account limiter, OTP cooldown/hourly/IP caps, social CSRF single-use + IP/MAC binding); make throttle state durable/shared-ready (address the in-process-resets-on-restart gap) as a hardening item.
8. **Audit events:** per-method auth result classification (success/failure reason), device admission, flag state — **no** secrets/OTP values/tokens/room/PII in logs (§11).
9. **Secret handling:** voucher HMAC/AEAD keys and PMS secrets remain ciphertext + generation + supersession (already modeled); reveal/print requires operator re-auth + audit (contract §4.4, B2).
10. **Account/credential lifecycle, session handoff, error contracts, localization-safe generic guest errors, offline behavior:** preserved from legacy (§3) and mapped onto `iam_v2` objects; generic uniform envelopes for all non-success (no enumeration).

### 4a. Transient challenge state (D2 — RETAINED, governed)
`public.auth_otps` (OTP challenge) and `public.social_oauth_states` (social CSRF/state) are classified **`RETAINED — OPERATIONAL TRANSIENT STATE`**: short-TTL operational challenge state, **not** an identity source of truth.
- **Ownership:** `scd` (edge). **Retention:** OTP rows expire ≤10 min (single-use `consumed_at`); social state ≤10 min single-use; a cleanup sweep bounds storage.
- **Prohibited:** treating either as durable guest identity, or as an `iam_v2` identity substitute. The resolved verified factor is written to `iam_v2.guest_principal_identities`; the challenge tables never carry authority.
- **Removal/rehome gate:** before the later complete-domain cutover, these tables get a **review/removal/rehome decision** (keep as ephemeral `public` operational state, or model as `iam_v2` challenge objects via a contract amendment). Until then they must not create an unacknowledged hybrid IAM authority — they hold no identity of record.
- `pms_attempts` is **Phase 3** scope (not touched in 1B).

### 4b. Durable, local-first throttling (D4 — required before any activation)
Process-local throttling (the current `credentials_ratelimit.go` limiter) resets on restart and is insufficient for future guest-visible activation. **Chosen design — local Postgres bucket table**, reusing the **proven** local-first durable pattern already in the codebase (`pms_attempts` durable attempt counting in `data-plane/internal/pmsguard/guard.go`, and `guest_accounts.failed_attempts`/`locked_until`). **No Redis or other unverified dependency.**
- **Store:** a minimal new `public.auth_throttle_buckets` table (loopback-only local Postgres; the same store every edge service already uses) — this is the **minimal explicit schema amendment** required, identified as part of the Phase-1B implementation authorization (it is `public`-schema, not `iam_v2`).
- **Shape:** `(scope_kind text, scope_key text, method text, window_start timestamptz, window_len_seconds int, count int, expires_at timestamptz, PRIMARY KEY (scope_kind, scope_key, method, window_start))`. `scope_kind ∈ {ENDPOINT, IDENTITY, IP, DEVICE, METHOD}` (endpoint, identity/IP, device and method scopes).
- **Atomic increment:** `INSERT … ON CONFLICT (…) DO UPDATE SET count = auth_throttle_buckets.count + 1 RETURNING count` (single-statement atomic; no read-modify-write race).
- **Expiry & bounded storage:** each row carries `expires_at`; a periodic cleanup (reaper-style, like the session reaper) deletes expired rows; window keys are time-bucketed so storage is bounded by active scopes × windows.
- **No PII/secret:** `scope_key` stores a **hash** of the identifier (e.g. HMAC of username/MAC/IP), never the raw credential, OTP, or plaintext identifier.
- **Failure behavior:** on DB error the limiter **fails closed** (deny/throttle) rather than allowing unbounded attempts; cleanup failures degrade gracefully (storage grows bounded until the next sweep).
- **Survives restart + reboot** (durable in local Postgres); **no Central dependency**.
- **Acceptance:** negative tests (limit enforced), concurrency tests (atomic increment under parallel logins), and **reboot persistence** (counters survive service restart + appliance reboot). Classified `[IMPL]` in scratch + `[DARK]` reboot check.

### 4c. OTP verification secret design (D7)
`SHA-256(salt|code)` is **not** the final Phase-1B security design for a 6-digit OTP. **Chosen design — keyed HMAC with a dedicated protected local key + constant-time comparison** (Argon2id fallback only if the key-management foundation cannot safely support HMAC), built on the **verified existing secret foundation** (the `voucher_code_key_generations` / `pms_interface_secret_generations` model: ciphertext + AEAD + `encryption_key_id` + generation numbering + supersession).
- **Key source & storage:** a dedicated OTP HMAC key held like other edge secrets (encrypted at rest via the existing local key-encryption mechanism; loopback-only; 0600; never in Git/logs/exports/process args).
- **Generation/version pin:** each stored OTP row pins the `key_generation_id` used, so rotation never invalidates in-flight codes; **rotation** supersedes the generation (new `superseded_at`), old generation retained until all pinned rows expire.
- **Comparison:** `crypto/subtle` constant-time compare of `HMAC(key_gen, salt|code)`.
- **Expiry / attempt cap:** unchanged strong controls — TTL 10 min, single-use, 5-attempt cap, cooldown/hourly/IP caps (now backed by §4b durable throttling).
- **Migration compatibility:** during rollout, verify supports both the legacy `salt:sha256` format (for in-flight codes) and the new HMAC format (generation-pinned); new issuance uses HMAC only; the transition is documented and time-bounded.
- **Reboot:** key + generation survive reboot (durable secret store); in-flight codes still verify.
- **Secret/log/export protection:** the plaintext code is never persisted or logged after delivery; the HMAC key never leaves the appliance.
- **Doc/comment alignment:** correct the stale "argon2id" comment in migration `0008` so code, migration, and docs all state the **same** algorithm (keyed HMAC).

### 4d. Social Stub safety (production must refuse the Stub)
The social-auth Stub provider (`internal/social/stub.go`) is **test-only**. Phase 1B requires:
- The **Stub is disabled/refused in production mode** — a production build/flag must not select the Stub.
- The production social flag **cannot enable** unless a **real configured `social_oauth_providers` row passes startup validation** (client id/secret/redirect present, provider implemented — today only Google).
- **No fake social success response** is ever returned in production.
- **Negative tests** prove production **refuses** the Stub (flag-on + Stub-only ⇒ social remains unavailable, not a fake success).

---

## 5. Dark & feature-flag rollout

Phase 1B changes **no** guest behavior on delivery. Flags:
- Default **OFF**; **tenant/site-scoped** where required; a **service-level kill switch** per method and a global `iam_v2.*` master switch.
- **No automatic activation** (no time-based or count-based auto-enable); every enable is an explicit, audited operator/PO action.
- Flag changes **audited** (who/when/scope; never the secret). **Startup validation:** on boot scd validates flag config and fails closed to legacy on any inconsistency. **Offline:** flag state is local (config/DB), evaluated offline. **No silent fallback that hides an incorrect result** — a shadow divergence is logged/alerted, never silently swallowed.

Rollout stages (each gated; no auto-promotion):
1. **Gate P:** least-privilege roles created + verified (§2).
2. Application code deployed with **all `iam_v2` paths OFF** (pure legacy behavior; zero `iam_v2` guest-path access).
3. **Dark evaluation, ZERO production `iam_v2` writes (D1 — RESOLVED/REJECTED shadow writes).** Phase 1B permits **no** production write to `iam_v2` — **including writes inside a transaction that is later rolled back** (rejected: sequences, external side effects, or other non-transactional behavior may survive, and it may cross the first-production-write boundary). Production Phase 1B permits **only**: application deployment with all IAM-v2 flags OFF; **no IAM-v2 repository execution on guest requests**; optional **read-only** schema/version/connection health checks; and **no** credential/device/principal/auth-context/entitlement/session/audit row written to `iam_v2`. All functional IAM-v2 write-path testing is **scratch/test only**.
4. **No guest-visible decision** is ever taken from `iam_v2` in Phase 1B.
5. **Controlled internal test path:** a synthetic tenant/site (or a maintenance flag limited to an operator test MAC) exercises the full `iam_v2` path end-to-end in a scratch/test DB, and in production only via an explicitly bounded internal test that touches no real guest.
6. **Product-Owner acceptance** before any guest-visible activation (which is the cutover, ladder step 11 — separate approval).

**Dual-write/dual-read:** NOT introduced. If ever proposed, it requires a precise necessity, a reconciliation method, a single ownership boundary, a rollback boundary, and **separate** PO approval. Phase 1B's position: no dual-write, no dual-read; legacy is sole authority until cutover.

---

## 6. Data & migration strategy

**Decision: Phase 1B performs NO production data migration and requires none.**
- `iam_v2` credential/identity tables stay empty in production during Phase 1B. Real credential data (account hashes, voucher key material, principals) is populated **only at cutover** (a later, separately approved step), per Phase-1A §8 ("real carry-forward data copied only at cutover, never before").
- Functional acceptance of the `iam_v2` credential/portal paths is performed in **scratch/test** with **seeded, disposable** fixtures (compromised test data: `opadmin@test.local`, `guest1`, throwaway vouchers) — never real guest PII.
- Because production `iam_v2` is empty and no migration is authorized, **production Phase 1B cannot validate real guest credentials against `iam_v2`**; production Phase 1B = code deployed, flags OFF, roles enforced, zero guest-visible change. This is stated explicitly so no one assumes a live `iam_v2` credential check in 1B.

Per-category disposition (source → target → migrate in 1B? → mapping/ownership/PII):
| Category | Legacy source | iam_v2 target | Migrate in 1B? | Mapping / notes | PII / retention |
|---|---|---|---|---|---|
| Guest accounts | `public.guest_accounts` | `guest_access_accounts` | **No** (cutover only) | username/argon2id hash 1:1; `(tenant,site)` owned; dedup by `(tenant,lower(username))` | password hash (sensitive); retention = account lifecycle |
| Voucher key material | `public.vouchers.code` (plaintext) | `vouchers.code_hmac`+AEAD + `voucher_code_key_generations` | **No** (cutover, with a deterministic re-encode transform) | plaintext → HMAC(blind index)+AEAD ciphertext; requires a key-generation; **transform is non-trivial** — designed for cutover, tested in scratch | code is secret; last4 only in exports |
| Guest identities (OTP/social) | `public.guests` email/phone/social | `guest_principals`+`guest_principal_identities` | **No** (cutover) | split device vs person; issuer-scoped social; dedup by normalized factor | email/phone/subject (PII); hashed/normalized |
| Devices | `public.guests(mac)` | `devices` | **No** (cutover) | MAC → device; separate from principal | MAC = device id (not PII owner) |
| Sessions/entitlements | `public.sessions` | `sessions`/`entitlements` | **No** — live runtime state; not migrated (Phase-1A §8) | rebuilt at cutover window under write-freeze | — |
| Transient (OTP/social/pms challenges) | `auth_otps`,`social_oauth_states`,`pms_attempts` | **stay in `public`** (D2) | **No** | ephemeral; not identity | short TTL |

Conflict handling, immutability, idempotency, reconciliation, encryption/hashing are specified for the **cutover** carry-forward transform (a later phase), captured here so Phase 1B builds the *code* that can consume it. **No bulk production migration is authorized or assumed.**

---

## 7. Database & migration breakdown

**Phase 1B requires NO new `iam_v2` schema objects** for the four in-scope credential methods — the verified 49-table schema (mg1–mg9) already contains every needed object (`guest_principals`, `guest_principal_identities`, `guest_access_accounts`, `vouchers`+`voucher_code_key_generations`+`voucher_batches`, `auth_contexts`, `devices`, `entitlements`, `entitlement_devices`, `sessions`, engine functions). **Phase 1A objects are NOT altered** to ease implementation; if a genuine contract gap is found (e.g., no OTP-challenge table, no per-method throttle table), it is **escalated for PO decision** (D2/D4), not patched silently.

Migration groups actually required by Phase 1B:
| Group | Purpose | Objects | Tx / lock | Online-safe | Up/Down | Idempotent | Perms | Preflight | Verify | Rollback |
|---|---|---|---|---|---|---|---|---|---|---|
| **P-ROLES** (Gate P) | least-privilege roles + grants (site DB) | `CREATE ROLE svc_*`, `GRANT`/`REVOKE`, `ALTER DEFAULT PRIVILEGES`, `ALTER ROLE … SET timeout/conn-limit` | each in its own tx; `GRANT` takes brief cat locks only | yes (no table rewrite) | down = `REVOKE`+`DROP ROLE` (after DSN rollback) | yes (idempotent `GRANT`, guarded `CREATE ROLE`) | applied as superuser/`iam_v2_migrator` one-time | roles absent; services on superuser | negative-permission tests (§2.7) | point DSNs back to superuser |
| **P-FLAGS** (optional) | flag storage if a DB-backed flag table is chosen | a small `iam_v2` or `public` config table **only if** config-file flags are insufficient — **PO decision D5** | single tx | yes | additive | yes | owner | — | flag read/write test | drop table (unused) |

No other DDL. No `ALTER` of the 49 Phase-1A tables. (If shadow production writes are approved under D1, no new objects are needed — writes target existing tables.)

---

## 8. Application work breakdown

Workstreams (each: repo/package, files, APIs, domain services, persistence, portal/UI, flags, config, observability, tests, deploy order, rollback). All in `data-plane/` (Go) unless noted; portald/hotel-admin only where stated.

- **W0 — Least-privilege roles & DSNs (Gate P).** Files: `deploy/systemd/*.service` env files, `iam_v2_scratch/roles.sql`→a production `roles`/grants migration, ops runbook. No app-logic change. Deploy first. Rollback: DSN revert.
- **W1 — `iam_v2` data-access layer.** New package `data-plane/internal/iamv2/` (repositories for principals/identities/accounts/vouchers/auth_contexts/devices/entitlements/sessions; wraps `pgxpool`; `search_path`-safe, schema-qualified `iam_v2.*`). No behavior change until flags on. Tests: repo unit tests in scratch.
- **W2 — Credential validators (iam_v2).** Voucher (HMAC blind-index + AEAD), account (argon2id verifier reused from `credentials_handlers.go`), OTP identity resolver, social identity resolver (issuer-scoped). APIs: internal `scd` service methods behind per-method flags. Reuse existing throttles/lockouts. Tests: B1–B5 in scratch.
- **W3 — auth_context service.** Creates `iam_v2.auth_contexts` (method↔subject), TTL/one-time; device upsert (`devices`, `device_network_appearances`). Tests: B6, method↔subject coherence.
- **W4 — session/entitlement engine adapter interface (scratch-tested only; D3).** Implements the **adapter interface** required by the future session-after-grant path (bridges to `reserve_device_slot`/`ingest_sample`/`close_session`/entitlement guard). **Production performs ZERO `iam_v2` writes (D1)** — no rolled-back-transaction shadow. Real acquisition, zero-cost purchase creation, entitlement creation, and `iam_v2` session issuance are **DEFERRED TO PHASE 2 / FULL-DOMAIN** — not Phase 1B. Tests (scratch only): A-series device/capacity/idempotency reuse + the credential/principal/device/auth-context portions of B3/B4; the entitlement/session portions of B3/B4 are deferred.
- **W5 — Feature-flag & kill-switch infra.** Config-file-first flags (default OFF), per-method + master; startup validation; audited changes; `/api/auth-methods` continues to reflect **legacy** availability while dark. Tests: flags-OFF non-regression; kill-switch.
- **W6 — Observability.** Metrics/logs/audit/correlation IDs; shadow-divergence metric; role-connection health; no PII (§11).
- **W7 — Portal wiring (portald).** No structural change; ensure the socket contract and error envelopes are unchanged; add nothing guest-visible. (Portald remains DB-less.)
- **W8 — Admin (edged/hotel-admin).** Optional: surface flag state read-only; no credential-behavior change. Deferred if not needed for acceptance.
- **W9 — Test harness & acceptance automation.** Scratch seeding, B-series harness, negative-permission harness, secret/PII scanners, static analysis, build.

**Phase boundaries (explicit):** Phase 1B stops at credential validation + auth_context + shadow session/entitlement for VOUCHER/ACCOUNT/OTP/SOCIAL. **Phase 2** = packages/quotes/pricing/purchases-as-commerce/portal package selection. **Phase 3** = PMS stay resolution + PMS auth + post-stay. **Phase 4** = financial posting. No workstream crosses these lines.

---

## 9. Security threat model (Phase 1B)

For each: prevention / detection / recovery.
| Threat | Prevention | Detection | Recovery |
|---|---|---|---|
| Credential stuffing | layered throttles (endpoint/user+IP/user+MAC), generic errors, lockout | throttle-hit metrics, failed-auth rate alerts | lockout auto-expiry; operator unlock (audited) |
| Brute force (account) | argon2id, constant-time, dummy-hash anti-enum, 5-fail lockout | per-account failure counters | `locked_until` expiry |
| OTP abuse | cooldown/hourly/IP caps, 5-attempt cap, TTL 10m, single-use, delivery-independent verify | OTP issue/verify metrics per destination/IP | cap reset on TTL |
| Voucher enumeration | HMAC blind-index (no plaintext lookup), generic errors, single-use | redemption-failure rate | key-generation rotation |
| Replay | one-time `auth_contexts`/quotes (`consumed_at`), single-use OTP/social state, session idempotency | duplicate-consume attempts | reject; audit |
| Session fixation | server-issued session ids; auth_context bound to device/network; no client-supplied session | mismatch metrics | reject |
| Tenant/site context spoofing | tenant/site from **signed assignment** only; composite tenant/site FKs on every row; no client-supplied tenant | assignment-verify failures | fail closed; re-verify on boot |
| PMS interface confusion | (PMS excluded from 1B) auth_context `ac_pms_pins` CHECK reserved; STRICT resolution deferred to Phase 3 | — | — |
| Room-number ambiguity | room scoped by PMS interface (Phase 3); N/A in 1B | — | — |
| Privilege escalation | least-privilege roles, no service superuser, owner≠login, default-deny privileges | `pg_roles`/`pg_stat_activity` audits | revoke; rotate |
| SQL injection | parameterized `pgx` everywhere; no string-built SQL; schema-qualified | static analysis (W9) | patch |
| Secret leakage | ciphertext+AEAD for voucher/PMS secrets; DSNs in 0600 env; no secrets in logs/exports/args | secret/PII scanner (W9, validator check 10) | rotate; revoke |
| Log/PII leakage | no passwords/OTP/tokens/room/PII in logs/metrics/audit; last4 only | PII scanner | scrub; rotate |
| Forged assignment context | signature by active dedicated assignment key; appliance binding; re-verify each boot | verify failures → awaiting-assignment | fail closed |
| Offline abuse | local license cap + device cap enforced offline; fail-closed license gate | capacity/cap metrics | reaper; cap |
| Compromised Hotel Admin | edged least-privilege role; admin actions audited; re-auth for sensitive ops | audit_log anomalies | revoke operator; rotate |
| Compromised service credential | per-service scoped role (blast radius limited to that role's grants); rotation | connection anomalies | rotate that role only |

---

## 10. Offline & failure behavior

Edge stays **local-first**; no HA claimed. Behavior:
| Condition | Behavior |
|---|---|
| Central Control Plane outage | guest auth unaffected (Central never in auth path); license evaluated from last signed envelope; telemetry buffers in outbox |
| NATS outage | telemetry/commands queue; auth unaffected |
| PMS outage | (PMS is Phase 3) N/A to 1B; FIAS cache behavior unchanged in legacy |
| DB connection failure | fail closed (no session issued); services retry with backoff; least-privilege role reconnects |
| One service unavailable | portald without scd → login fails closed; acctd down → no quota accrual (sessions persist), reaper still bounds; edged down → admin only |
| Appliance reboot | services reconnect under least-privilege roles; sessions/reaper state rebuilt; flags re-validated; no guest-visible change while dark |
| Credential-provider timeout (OTP/social) | verify still local; delivery/exchange failures return generic error; no session |
| Clock skew | TTLs use server clock; monotonic where available; windows tolerant; no security decision on client time |
| Partial deployment | flags OFF means legacy everywhere; a half-deployed fleet still serves legacy; no split authority |
| Feature-flag inconsistency | startup validation fails closed to legacy; alert |

---

## 11. Observability & operations

- **Metrics:** per-method auth attempts/success/failure (reason-classified), throttle hits, lockouts, device admissions/rejections, capacity rejections, **shadow-divergence count**, `iam_v2` role connection health, flag state gauge.
- **Structured logs:** correlation/trace id per request (portald→scd); auth result class; **never** passwords/OTP values/tokens/room/stay ids/PII.
- **Audit events:** flag changes, role/credential rotations (who/when, not the secret), admin credential actions, operator re-auth for reveal/export.
- **Dashboards/alerts:** failed-auth spikes, throttle saturation, shadow-divergence > threshold (a divergence means the `iam_v2` path would decide differently — a correctness alert, not a silent fallback), DB-role connection failures, flag inconsistency, reaper backlog.
- **Rate-limit visibility, feature-flag state visibility, rollback triggers, runbooks (Gate P rollout/rollback, rotation, flag enable/disable), support diagnostics.** No metric/log exposes secrets or unnecessary guest PII (enforced by the validator secret/PII scan).

---

## 12. Acceptance matrix (Phase 1B)

Classification: **[IMPL]** implementation acceptance (scratch/test); **[DARK]** dark-production acceptance; **[LATER]** later guest-visible acceptance (post-cutover, separate approval); **[DEFER]** later Phase.

| # | Test | Class |
|---|---|---|
| P1 | least-privilege role migration applied; every `svc_*` `rolsuper=false` | [DARK] |
| P2 | negative permissions: each service denied outside its grant set; cannot read another service's restricted data | [DARK] |
| P3 | PUBLIC has zero privileges on `iam_v2`; default-deny on new objects | [DARK] |
| P4 | credential rotation + rollback; reboot persistence of least-privilege connections | [DARK] |
| F1 | all feature flags OFF by default; startup validation fails closed | [DARK] |
| F2 | zero guest-visible change while dark (voucher/account/OTP/social behave exactly as legacy) | [DARK] |
| F3 | zero unauthorized `iam_v2` access in production (no guest-path `iam_v2` read/write while dark) | [DARK] |
| B1 | voucher HMAC redemption, single-use enforced (scratch) | [IMPL] |
| B2 | voucher reveal/export re-auth + audit + CSV formula-injection guard + last4 default | [IMPL] |
| B3a | account **credential validation** (argon2id, lockout, generic error) | [IMPL] |
| B3b | account **attaches to its live entitlement** (never fresh quota per login); assigned package follows-current-then-pins | **[DEFER] — DEFERRED TO PHASE 2 / FULL-DOMAIN ACCEPTANCE** (needs real entitlement/session; no complete B3 PASS in 1B) |
| B4a | OTP/social **principal/identity resolution**: same verified factor on a new MAC → same tenant-wide `guest_principal`; issuer-scoped social; **MAC never an owner** | [IMPL] |
| B4b | OTP/social same-per-site **live entitlement** attach | **[DEFER] — DEFERRED TO PHASE 2 / FULL-DOMAIN ACCEPTANCE** (needs real entitlement/session; no complete B4 PASS in 1B) |
| B5 | lockouts, layered throttles, generic errors, one-time password reveal | [IMPL] |
| B6 | auth_contexts one-time/TTL; method↔subject coherence (PMS context without stay rejected, etc.) | [IMPL] |
| S1 | device slot: over-limit reject; same-device reconnect no slot burn (`reserve_device_slot`) | [IMPL] |
| S2 | capacity gate atomic (concurrent logins can't exceed local license cap) | [IMPL] |
| S3 | accounting idempotent watermark `(session,seq)`; session close idempotent | [IMPL] |
| I1 | tenant/site isolation (composite FK fuzz; cross-tenant/site rejected) | [IMPL] |
| I2 | signed-assignment context enforcement (no client-supplied tenant/site) | [DARK] |
| R1 | multiple PMS interfaces with duplicate room numbers resolve correctly; **no guest-facing PMS selector** | [DEFER] (Phase 3) |
| H1 | session handoff idempotency; concurrency | [IMPL] |
| O1 | offline behavior (voucher/account offline; OTP verify offline; delivery/exchange failure → generic error) | [IMPL] |
| X1 | failure injection (DB down, provider timeout, partial deploy, flag inconsistency) → fail closed | [IMPL]/[DARK] |
| G1 | secret/PII scan of code, logs, exports = clean; build + static analysis pass | [IMPL]/[DARK] |
| RB1 | rollback: flags OFF / DSN revert restores legacy with zero residue | [DARK] |
| HC1 | service health + **guest-plane non-regression** across the fleet while dark | [DARK] |
| G2 | guest-visible activation acceptance (cutover) | [LATER] |
| G3 | financial posting acceptance | [DEFER] (Phase 4) |

---

## 13. Execution gates (Phase 1B approval ladder)

Aligns with Phase-1A §7a/§11 (Phase 1B = steps 7–10, before cutover at 11+). A single future implementation authorization (§14) may execute steps 1–9 end-to-end, retaining these stop conditions:
1. **Phase 1B plan approval** (this document).
2. **Read-only production preflight** (confirm baseline: iam_v2 empty/dark, services superuser, backups fresh).
3. **Backup & rollback preparation** (pre-change full + schema dump; rollback scripts).
4. **Least-privilege role implementation + acceptance (Gate P)** — P1–P4 pass; services off superuser.
5. **Application/database implementation with flags OFF** (W0–W9; no `iam_v2` DDL beyond roles).
6. **Test/scratch acceptance** — full B/S/I/H/O/X/G [IMPL] matrix green in a disposable DB.
7. **Production dark deployment** — code live, flags OFF, roles enforced.
8. **Dark acceptance** — F1–F3, I2, RB1, HC1, secret/PII, guest-plane non-regression.
9. **Documentation & export synchronization** + `ZERO_STALE_LEFTOVERS = PASS`.
10. **Product-Owner review before any guest-visible activation.**

**Separate authorization still required for:** guest-visible activation (cutover, step 11+); financial posting (Phase 4); cutover; legacy cleanup; any later Phase. Stop conditions (halt + report, do not proceed): any negative-permission failure; any guest-visible regression while dark; **any `iam_v2` read or write in production (D1 forbids all production `iam_v2` runtime access, including rolled-back transactions)**; any secret/PII leak; any scratch/test shadow-divergence above threshold; any acceptance red.

---

## 14. Implementation prompt blueprint (proposed — DO NOT EXECUTE HERE)

> **PROPOSED Product-Owner Phase-1B implementation authorization (for a later message):**
>
> "APPROVED — implement Phase 1B (credential/identity/auth-context, **DARK**) end-to-end in one controlled execution, per `docs/architecture/StayConnect-IAM-Phase1B-Plan.md`.
>
> **Authorized:** **Gate P** — create the least-privilege site-DB roles and **de-superuser ALL site-DB runtime services** (`scd`, `edged`, `acctd`, `netd`) using the exact grant matrix in `Phase1B-Privilege-Matrix.md` (**production service roles receive ZERO `iam_v2` DML**; PUBLIC zero; no service can write `iam_v2`), a non-runtime migration-executor model (NOLOGIN owner + NOLOGIN migrator + time-limited audited `migrate_exec`; **no permanent `stayconnect` superuser dependency**), per-service 0600 DSN env files, credential rotation, and negative-permission acceptance; remove the unused `iam_v2_svc_portald`/`iam_v2_svc_hoteladm` skeleton roles. Implement W0–W9 in `data-plane/` (iam_v2 data-access, voucher/account/OTP/social validators, auth_context service, the session-after-grant **adapter interface**, **durable local-Postgres throttling** incl. the minimal `public.auth_throttle_buckets` amendment, **keyed-HMAC OTP secret**, deployment-controlled local flags default OFF, observability); **IAM-v2 functional tests run in scratch ONLY**; deploy to production with **all flags OFF** and performing **ZERO `iam_v2` writes** (no rolled-back-tx shadow; read-only health checks only); run [DARK] acceptance (roles/negative permissions, flags-OFF non-regression, **zero `iam_v2` writes**, guest-plane non-regression, production refuses the social Stub, secret/PII scan, reboot persistence of durable throttling); back up before changes; sync docs + regenerate all export packs; run `tools/validate-project-state.sh` (repository + extracted-pack) → `ZERO_STALE_LEFTOVERS = PASS`.
>
> **NOT authorized (hard stops):** guest-visible activation or authority switch or `search_path`/DSN cutover to iam_v2; any guest-visible decision from iam_v2; **any production `iam_v2` write** (incl. rolled-back transactions); real production **purchase/entitlement/`iam_v2` session** issuance; dual-read/dual-write; production credential-data migration into iam_v2; **any `iam_v2` DML grant to a runtime service role**; PMS/post-stay/paid auth; packages/quotes/pricing/purchases-as-commerce (Phase 2); financial posting (Phase 4); programmatic reversal; network/HA/deployment-topology change; Central changes; legacy cleanup; Phase 2 or any later Phase.
>
> **Acceptance:** Phase-1B matrix [IMPL] (scratch) + [DARK] (production) green; **zero guest-visible change**; **zero production `iam_v2` writes**; services non-superuser with exact grants; durable throttling + keyed-HMAC OTP proven; production refuses Stub; no secret/PII leak. **Rollback:** flags OFF and/or DSN revert to legacy (break-glass superuser only as time-bounded audited Gate-P rollback, removed after); roles additive/droppable; no data divergence (no iam_v2 writes at all). **Do NOT continue if durable throttling, OTP secret design, the exact grant matrix, or Gate-P acceptance fails. Do not continue to Phase 2. No cutover.** Stop and report on any negative-permission failure, guest-visible regression, any production iam_v2 write, secret/PII leak, production Stub acceptance, or acceptance red."

This blueprint is a **proposal only** and is not executed by the current planning task.

---

## 15. Documentation & export synchronization (performed by the planning task)

- Created: this plan (`docs/architecture/StayConnect-IAM-Phase1B-Plan.md`).
- Updated: `00-START-HERE.md` (pack), `StayConnect-IAM-Handoff.md`, `MANIFEST.md`, roadmap/status pointers — **Phase 1A acceptance status unchanged**; next authorized action → PO approval/rejection of this plan.
- Project Pack: this plan added as a bundled file.
- Planning evidence pack: inventories (DB-access, auth entry points, iam_v2 objects), scope/acceptance matrices, and the §14 blueprint, with checksums.
- Validator run in repository + extracted-pack modes → `ZERO_STALE_LEFTOVERS = PASS`.

---

## 16. Risks, assumptions & Product-Owner decisions (D1–D9 — BINDING, RESOLVED)

**Assumptions:** Phase-1A baseline holds (verified, and Phase 1A is now formally accepted/CLOSED at its DARK maturity); the 49-table schema is sufficient for the four in-scope methods (verified — no new `iam_v2` DDL); legacy code paths remain the **sole** production authority throughout Phase 1B.

**Resolved Product-Owner decisions (binding — no longer open questions):**
| D# | Decision (binding) |
|---|---|
| **D1** | **REJECTED — zero production `iam_v2` writes**, including writes inside a later-rolled-back transaction. Production 1B = deploy with all IAM-v2 flags OFF; no IAM-v2 repository execution on guest requests; optional read-only schema/version/connection health checks only; no credential/device/principal/auth-context/entitlement/session/audit row in `iam_v2`. All IAM-v2 write-path testing is scratch/test only. (§5, §8-W4) |
| **D2** | **APPROVED WITH GOVERNANCE.** `public.auth_otps` + `public.social_oauth_states` **RETAINED — OPERATIONAL TRANSIENT STATE** (short-TTL challenge state, not an identity source of truth): scd-owned, TTL/retention bounded (OTP ≤10 min; social state ≤10 min), single-use, must **not** be treated as durable guest identity, with a **review/removal/rehome gate before the later complete-domain cutover** so they never become an unacknowledged hybrid IAM authority. `pms_attempts` = Phase 3. (§4a) |
| **D3** | **CONFIRMED.** Phase 1B does **no** real production entitlement/session issuance via `iam_v2`. It implements + scratch-tests credential validation, principal/identity resolution, device resolution, one-time `auth_context`, and the session-after-grant **adapter interface** only. Real acquisition / zero-cost purchase / entitlement / `iam_v2` session belong to Phase 2 / full-domain. The entitlement/session portions of B3/B4 are `DEFERRED TO PHASE 2 / FULL-DOMAIN ACCEPTANCE` — **no complete B3/B4 PASS is claimed in Phase 1B**. (§12) |
| **D4** | **DURABLE, LOCAL-FIRST THROTTLING REQUIRED before any activation** — chosen design in §4b (local Postgres bucket table, the proven `pms_attempts`/`guest_accounts.locked_until` pattern; no Redis/new dependency). Requires the minimal `public.auth_throttle_buckets` amendment, identified as part of the Phase-1B implementation authorization. (§4b) |
| **D5** | **Deployment-controlled local config/env flags** — master + per-method, default OFF, startup validation, **no DB-backed flag table**, no Hotel-Admin/Central activation UI in 1B, changed only by an explicit deployment action recorded in deployment/audit evidence, all OFF in production 1B. Guest-visible flag governance is later/separate. (§5) |
| **D6** | **De-superuser all site-DB runtime services** (`scd`, `edged`, `acctd`, `netd`); `portald` + Hotel Admin get **no** DB role (no direct connection); **remove/retire** the unused `iam_v2_svc_portald`/`iam_v2_svc_hoteladm` skeleton roles; central-DB roles are a **separate future security-hardening item**. (§2) |
| **D7** | OTP verification design = **keyed HMAC with a dedicated protected local key + constant-time compare** (Argon2id fallback if HMAC key-management cannot be safely supported); generation-pinned, rotatable; correct the stale "argon2id" comment in migration `0008` to match. Full design in §4c. (§4c) |
| **D8** | **CONFIRMED OUT OF PHASE 1B** — no guest paid access implemented or implied. |
| **D9** | Phase 1A is **formally Product-Owner ACCEPTED and CLOSED** at `SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER`. Phase 1B planning is the current activity. All status carriers corrected accordingly. |

**Risks & stop conditions:** voucher plaintext→HMAC/AEAD re-encode transform (a cutover-time step) is non-trivial and must be proven in scratch; social defaults to Stub unless a real provider row (only Google impl real) — production must **refuse** Stub (§4d); OTP delivery / social exchange are online-dependent; empty production `iam_v2` means production 1B is **flags-OFF-only** (functional proof is scratch-bound — no live `iam_v2` credential check in 1B). **Halt + report (do not proceed) on:** any negative-permission failure; any guest-visible regression while dark; **any production `iam_v2` write**; failure of durable-throttle, OTP-secret, exact-grants, or Gate-P acceptance; any secret/PII leak; production accepting the social Stub.

---

## Appendix Z — FUTURE DESIGN — NOT GRANTED OR APPLIED IN PHASE 1B

The `iam_v2` privileges below are the **future cutover** design for the runtime roles. They are **design reference only**: they are **NOT created, granted, or applied in Phase 1B**, and no Phase 1B role receives them. Applying any of them requires a separate, explicitly-approved cutover authorization (ladder step 11+, after Phases 2–6). During Phase 1B every production runtime role holds **ZERO** `iam_v2` privileges (see §2.2, §2.8, and `Phase1B-Privilege-Matrix.md` → `PRODUCTION_IAM_V2_DML: NONE`).

- **`svc_scd` (future cutover only):** `USAGE` on `iam_v2`; `SELECT`/`INSERT`/`UPDATE` on the credential/identity/auth/session/entitlement/device tables it would own at cutover; `EXECUTE` on `reserve_device_slot`, `ingest_sample`, `close_session`, `apply_adjustment`; no posting/settlement rights.
- **`svc_edged` (future cutover only):** `iam_v2` credential/account/voucher/entitlement admin tables; write-only on secret-generation tables.
- **`svc_acctd` (future cutover only):** `iam_v2` `SELECT` on `sessions`, `INSERT` on `accounting_records`, `EXECUTE ingest_sample`, entitlement counters only via `apply_adjustment`.
- **`svc_netd`:** never receives any `iam_v2` grant (not even at cutover for this phase's design).

**These grants are `FUTURE DESIGN — NOT GRANTED OR APPLIED IN PHASE 1B`.**

---

**End of Phase 1B plan. IMPLEMENTATION AUTHORIZED — IN PROGRESS (dark / flags-OFF; PR #2 not merged).** Single next authorized action: **complete Phase 1B execution and live-dark verification**, then Product-Owner acceptance or rejection. `PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE`.
