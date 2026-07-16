# StayConnect IAM — Phase 1B Implementation Plan (Credential/Portal Integration, DARK)

**Status: PLANNING ONLY — NOT APPROVED FOR IMPLEMENTATION, NOT IMPLEMENTED.** This document is a complete, production-grade Phase 1B plan produced under a Product-Owner *planning* authorization (2026-07-16). It authorizes **no** code, DDL/DML, role/credential change, DSN/`search_path` change, service routing to `iam_v2`, dual-read/write, deployment, data migration, PMS/FIAS traffic, financial posting, network change, cutover, or any later Phase. Nothing here executes until a separate explicit Product-Owner **implementation** authorization (the blueprint in §14) is given.

**Baseline this plan builds on (verified):** Phase 0 FINAL/CLOSED; Phase 1A `PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER`; production `iam_v2` = 49 empty tables (fingerprint `bd75026f`), no service reads/writes it, no DSN/`search_path` routing, no data migration; all services connect as PostgreSQL superuser `stayconnect`; source baseline `d4fa9be`. Ladder reference: [Phase-1A Plan §7a/§11](StayConnect-IAM-Phase1A-Plan.md) (Phase 1B = ladder steps 7–10, dark/flagged, **before** any cutover at step 11).

---

## 1. Exact Phase 1B scope

Scope is derived from FINAL contract §18 (Phase 1B row), §4.4/§4.5/§4.6, §19 B-series acceptance, the verified `iam_v2` schema, and the **current running code** (inventoried §3). Contract §18 Phase 1B = *"Auth contexts; voucher (HMAC/AEAD), account, OTP/social (guest principals) re-pointed; session-after-grant portal flow."* PMS is **not** listed in Phase 1B (it needs the stay domain — Phase 3); packages/quotes/pricing are Phase 2; posting is Phase 4.

**IN Phase 1B (build against `iam_v2`, dark/flagged, no guest-visible decision):**
- Least-privilege database access (the mandatory prerequisite — §2).
- Credential validation + subject resolution against `iam_v2` for the four contract-listed methods: **VOUCHER** (HMAC/AEAD), **ACCOUNT** (argon2id), **OTP** (email/SMS → `guest_principal_identities`), **SOCIAL** (OAuth → `guest_principal_identities`).
- `guest_principals` / `guest_principal_identities` resolution (tenant-wide, MAC-is-never-a-factor) and `devices` registry (MAC = device).
- `auth_contexts` creation (one-time, TTL, method↔subject coherence) for those four methods.
- The **session-after-grant** portal code path wired to the `iam_v2` engine (`reserve_device_slot`, `ingest_sample`, `close_session`, entitlement guard) **behind flags**, exercised in scratch/test and shadow-only in production.
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
| Session-after-grant (shadow) | `session.Manager.Start*` on `public.sessions` | §4.6 sessions/entitlements | `iam_v2.sessions` + `entitlements` + `entitlement_devices` (+engine) | adapter that *computes* the would-grant via `iam_v2` engine, compares to legacy, **writes nothing to iam_v2 in production dark** | scd | `iam_v2.session.shadow` | B-series in scratch | flag OFF |

**Anti-hybrid rule (inherited from Phase-1A §7a/§8):** Phase 1B does **not** create a per-flow/per-service split source of truth. In production, the legacy `public` schema remains the sole authority for real guest sessions throughout Phase 1B; `iam_v2` is exercised only in scratch/test and, at most, read-only shadow evaluation that takes **no** guest-visible decision. A real switch of authority is the **cutover** (ladder step 11+, separate approval).

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
  - `iam_v2` (prepared for cutover, unused until then): `USAGE` on schema; `SELECT`/`INSERT`/`UPDATE` on the credential/identity/auth/session/entitlement/device tables it will own at cutover; `EXECUTE` on `reserve_device_slot`, `ingest_sample`, `close_session`, `apply_adjustment`; no rights on posting/settlement tables.
- `svc_edged` (**LOGIN**) — admin CRUD grants on `public` (its current broad admin set) + `iam_v2` credential/account/voucher/entitlement admin tables prepared for cutover; write-only on secret-generation tables (never `SELECT` on ciphertext it does not need).
- `svc_acctd` (**LOGIN**) — narrow: `public.sessions` (SELECT/UPDATE), `public.accounting_records` (INSERT); `iam_v2` `SELECT` on `sessions`, `INSERT` on `accounting_records`, `EXECUTE ingest_sample`, `UPDATE` entitlement counters only via `apply_adjustment` (no direct counter UPDATE).
- `svc_netd` (**LOGIN**) — networking `public` tables only; **no** `iam_v2` grant.

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
2. **scd owns credential validation and authentication-context creation.** A new `iam_v2` auth adapter, selected per-method by flag, validates the credential against `iam_v2` credential tables and creates an `iam_v2.auth_contexts` row (method↔subject enforced by DB CHECKs `ac_one_subject`/`ac_method_subject`/`ac_pms_pins`). Subject resolution:
   - VOUCHER → `iam_v2.vouchers` by `code_hmac` (compute HMAC with the active `voucher_code_key_generations`); subject `voucher_id`.
   - ACCOUNT → `iam_v2.guest_access_accounts` by `(tenant, lower(username))`; argon2id verify; subject `guest_account_id`.
   - OTP → verify challenge in `public.auth_otps` (unchanged), then resolve/create `guest_principals`+`guest_principal_identities` (EMAIL/PHONE, tenant-wide); subject `guest_principal_id`.
   - SOCIAL → verify state in `public.social_oauth_states` + provider exchange (unchanged), then resolve/create identity `(SOCIAL_SUBJECT, issuer, subject)` issuer-scoped; subject `guest_principal_id`.
   - Device: MAC → `iam_v2.devices` upsert; `device_id` stamped on the auth_context; `device_network_appearances` records the guest_network.
3. **Portal → auth service call:** unchanged transport (portald→scd Unix socket). scd's response envelope is unchanged (`{session_id, guest_id, duration_seconds, expires_at}`); in dark mode the **legacy** path still produces that real session, while the `iam_v2` adapter runs in **shadow** (computes the would-be auth_context + would-be grant/session via the engine, compares, logs divergence) and takes **no** guest-visible decision.
4. **Tenant/site/PMS-interface context** derived exactly as today (signed assignment; IP→guest_network). No new identity source.
5. **Transaction boundaries:** auth_context creation is a single tx; the (shadow) grant/session computation uses the `iam_v2` engine functions (`reserve_device_slot`, capacity via advisory namespaces `LN_DEVICE_SLOT=11`/`LN_CAPACITY=7`, `ingest_sample`, `close_session`) in a read-mostly/rolled-back tx in production dark (no durable `iam_v2` write in production).
6. **Idempotency keys:** auth_contexts one-time (`consumed_at`), TTL 10m; device slot idempotent reconnect (no slot burn); session close idempotent (`ALREADY_ENDED`); accounting watermarked `(session,seq)`.
7. **Retry/rate-limit/brute-force:** reuse existing layered throttles (account limiter, OTP cooldown/hourly/IP caps, social CSRF single-use + IP/MAC binding); make throttle state durable/shared-ready (address the in-process-resets-on-restart gap) as a hardening item.
8. **Audit events:** per-method auth result classification (success/failure reason), device admission, flag state — **no** secrets/OTP values/tokens/room/PII in logs (§11).
9. **Secret handling:** voucher HMAC/AEAD keys and PMS secrets remain ciphertext + generation + supersession (already modeled); reveal/print requires operator re-auth + audit (contract §4.4, B2).
10. **Account/credential lifecycle, session handoff, error contracts, localization-safe generic guest errors, offline behavior:** preserved from legacy (§3) and mapped onto `iam_v2` objects; generic uniform envelopes for all non-success (no enumeration).

---

## 5. Dark & feature-flag rollout

Phase 1B changes **no** guest behavior on delivery. Flags:
- Default **OFF**; **tenant/site-scoped** where required; a **service-level kill switch** per method and a global `iam_v2.*` master switch.
- **No automatic activation** (no time-based or count-based auto-enable); every enable is an explicit, audited operator/PO action.
- Flag changes **audited** (who/when/scope; never the secret). **Startup validation:** on boot scd validates flag config and fails closed to legacy on any inconsistency. **Offline:** flag state is local (config/DB), evaluated offline. **No silent fallback that hides an incorrect result** — a shadow divergence is logged/alerted, never silently swallowed.

Rollout stages (each gated; no auto-promotion):
1. **Gate P:** least-privilege roles created + verified (§2).
2. Application code deployed with **all `iam_v2` paths OFF** (pure legacy behavior; zero `iam_v2` guest-path access).
3. **Dark shadow evaluation** (explicitly defined, safe): scd computes the `iam_v2` auth_context + would-grant in parallel with the legacy decision, **writes nothing guest-visible**, compares, emits divergence metrics. (Whether shadow may write ephemeral `iam_v2.auth_contexts`/`devices` in production is **PO decision D1** — default recommendation: **no production `iam_v2` writes in 1B**; shadow is compute-only/rolled-back.)
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
- **W4 — session/entitlement engine adapter (shadow).** Bridges to `reserve_device_slot`/`ingest_sample`/`close_session`/entitlement guard; computes would-grant; **production shadow = compute + compare + rolled-back tx** (no durable write) unless D1 approves. Tests: A-series device/capacity/idempotency reuse + B3/B4 attach-to-live-entitlement in scratch.
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
| B3 | account attaches to its **live** entitlement (never fresh quota per login); assigned package follows-current-then-pins | [IMPL] |
| B4 | OTP/social: same verified factor on a new MAC → same tenant-wide principal + same per-site live entitlement; issuer-scoped social; **MAC never an owner** | [IMPL] |
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

**Separate authorization still required for:** guest-visible activation (cutover, step 11+); financial posting (Phase 4); cutover; legacy cleanup; any later Phase. Stop conditions (halt + report, do not proceed): any negative-permission failure; any guest-visible regression while dark; any unauthorized `iam_v2` write in production (unless D1 explicitly approved shadow writes); any secret/PII leak; shadow-divergence above threshold; any acceptance red.

---

## 14. Implementation prompt blueprint (proposed — DO NOT EXECUTE HERE)

> **PROPOSED Product-Owner Phase-1B implementation authorization (for a later message):**
>
> "APPROVED — implement Phase 1B (credential/portal integration, DARK) end-to-end in one controlled execution, per `docs/architecture/StayConnect-IAM-Phase1B-Plan.md`.
>
> **Authorized:** create least-privilege site-DB roles (`svc_scd/edged/acctd/netd`, `iam_v2_migrator`) + grants + per-service 0600 DSN env files, and move those services off superuser `stayconnect` (Gate P), with negative-permission acceptance; implement W0–W9 (iam_v2 data-access, voucher/account/OTP/social validators, auth_context service, shadow session/entitlement adapter, feature flags default OFF, observability) in `data-plane/`; run full [IMPL] acceptance in a disposable scratch DB; deploy to production with **all flags OFF**; run [DARK] acceptance (roles, flags-OFF non-regression, zero unauthorized iam_v2 access, guest-plane non-regression, secret/PII scan); back up before changes; sync docs + regenerate export packs; run `tools/validate-project-state.sh` (repository + extracted-pack) → `ZERO_STALE_LEFTOVERS = PASS`.
>
> **NOT authorized (hard stops):** guest-visible activation or `search_path`/DSN cutover to iam_v2; any guest-visible decision taken from iam_v2; dual-read/dual-write; production credential-data migration into iam_v2; PMS/post-stay/paid auth; packages/quotes/pricing/purchases-as-commerce (Phase 2); financial posting (Phase 4); programmatic reversal; network/HA/deployment-topology change; Central changes; legacy cleanup; Phase 2 or any later Phase.
>
> **Acceptance:** Phase-1B matrix [IMPL]+[DARK] green; **zero guest-visible change**; services non-superuser; no secret/PII leak. **Rollback:** flags OFF and/or DSN revert to legacy; roles are additive/droppable; no data divergence (no iam_v2 guest writes). **Do not continue to Phase 2.** Stop and report on any negative-permission failure, guest-visible regression, unauthorized iam_v2 write, secret/PII leak, or acceptance red."

This blueprint is a **proposal only** and is not executed by the current planning task.

---

## 15. Documentation & export synchronization (performed by the planning task)

- Created: this plan (`docs/architecture/StayConnect-IAM-Phase1B-Plan.md`).
- Updated: `00-START-HERE.md` (pack), `StayConnect-IAM-Handoff.md`, `MANIFEST.md`, roadmap/status pointers — **Phase 1A acceptance status unchanged**; next authorized action → PO approval/rejection of this plan.
- Project Pack: this plan added as a bundled file.
- Planning evidence pack: inventories (DB-access, auth entry points, iam_v2 objects), scope/acceptance matrices, and the §14 blueprint, with checksums.
- Validator run in repository + extracted-pack modes → `ZERO_STALE_LEFTOVERS = PASS`.

---

## 16. Risks, assumptions & unresolved Product-Owner decisions

**Assumptions:** Phase-1A baseline holds (verified); the 49-table schema is sufficient for the four in-scope methods (verified — no new DDL); legacy code paths remain the sole production authority throughout Phase 1B.

**Unresolved PO decisions (D#) — must be answered before/at implementation approval:**
- **D1 — Production shadow writes.** May the dark `iam_v2` path write ephemeral rows (`auth_contexts`/`devices`) in production, or is production strictly compute-only/no-iam_v2-write with functional proof only in scratch? *Recommendation: no production iam_v2 writes in 1B (safest; avoids the first-production-write boundary).* 
- **D2 — Transient challenge tables.** Keep `auth_otps`/`social_oauth_states`/`pms_attempts` in `public` (recommended — ephemeral, not identity) or model them in `iam_v2` (needs new objects → contract change)?
- **D3 — Session-after-grant coupling.** A free grant needs a zero-amount `purchase` (`entitlements.purchase_id NOT NULL UNIQUE`), which is Phase-2 territory. Confirm Phase 1B stops at *shadow* would-grant (no real entitlement/session in iam_v2), with real entitlement/session creation deferred to cutover/Phase 2.
- **D4 — Throttle durability.** Login/OTP throttles are in-process (reset on restart, not shared). Harden to durable/shared storage in 1B, or accept as-is and defer?
- **D5 — Flag storage.** Config-file flags (recommended) vs a DB-backed flag table (adds one object).
- **D6 — Role scope.** Move all edge services (scd/edged/acctd/netd) off superuser in Gate P, or only the iam_v2-touching ones (scd/edged/acctd)? Note `iam_v2_svc_hoteladm`/`iam_v2_svc_portald` skeleton roles are unused (hotel-admin has no DB; portald has no DB) — recommend removing/renaming them. *Recommendation: de-superuser all edge site-DB services; drop the unused skeleton roles.*
- **D7 — OTP hashing intent.** Migration `0008` comment says "argon2id" but code uses `SHA-256(salt|code)` (deliberate for short-TTL OTP). Confirm expected behavior; align comment.
- **D8 — Paid access.** Absent in legacy; out of Phase 1B. Confirm it is **not** in Phase 1B scope (net-new; future).
- **D9 — Phase 1A acceptance recording.** The current authorization treats Phase 1A as accepted/closed, but the acceptance record/handoff still say "pending final PO acceptance." Per the instruction to not change Phase 1A acceptance status, this plan left it unchanged — PO should confirm whether to formally record Phase 1A acceptance.

**Risks:** voucher plaintext→HMAC/AEAD re-encode transform (cutover) is non-trivial and must be proven in scratch; social defaults to Stub unless a real provider row (only Google impl real); OTP delivery/social exchange are online-dependent; in-process throttles don't survive restart/HA; empty production `iam_v2` means production 1B is flags-OFF-only (functional proof is scratch-bound) — this must be communicated so no one expects a live iam_v2 credential check in 1B.

---

**End of Phase 1B plan. PLANNING ONLY — not approved, not implemented.** Single next authorized action: **Product-Owner approval or rejection of this complete Phase 1B plan.**
