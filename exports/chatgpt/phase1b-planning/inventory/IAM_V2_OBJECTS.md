# Phase 1B Planning Evidence — iam_v2 Object Inventory (credential/portal domain)

Read-only inventory of the verified `iam_v2` schema (`iam_v2_scratch/migrations/mg1..mg9`, fingerprint `bd75026f`). Input to Phase-1B plan §1/§3/§4/§7. Pattern: composite tenant/site anchor `(tenant_id, site_id, id)`; principal-level tables are tenant-wide (no `site_id`).

## MG-1 PMS interface core (context backbone)
- `pms_interfaces` (lifecycle ACTIVE/AUTH_DISABLED/DRAINING/DECOMMISSIONED; `current_revision_id`).
- `pms_interface_revisions` — append-only; **`folio_identity_strategy NOT NULL DEFAULT 'UNSET'`** (fail-closed CHARGE gate); immutable (mg9 `imm_pms_rev`).
- `pms_interface_secret_generations` — AEAD ciphertext + generation + supersession (mg9 `sg_guard`).
- `guest_network_pms_map` — **cross-schema FK → `public.guest_networks`** (MG-0 anchor); `gnpm_one_default`.
- `pms_interface_pnumber_seq`, `pms_source_conflicts`.
- Room numbers are namespaced **per PMS interface** via MG-4 `stays.normalized_room_number` (partial index `stays_room_lookup`).

## MG-3 identities & credentials — **Phase 1B core**
- `guest_principals` — TENANT-WIDE; unique `(tenant_id,id)`.
- `guest_principal_identities` — verified factors `EMAIL/PHONE/SOCIAL_SUBJECT`; `gpi_social_needs_issuer`; unique `(tenant_id, factor_type, factor_issuer, factor_value_norm)`; FK → `guest_principals` CASCADE. (OTP/social auth resolves here; MAC is never a factor.)
- `guest_access_accounts` — SITE-scoped username/password (`password_hash` argon2id); partial-unique `(tenant_id, lower(username))`; lockout cols.
- `voucher_code_key_generations` — HMAC/AEAD key generations.
- `voucher_batches`; `vouchers` — `code_hmac` (blind-index, globally unique), `code_ciphertext`/`code_nonce` (AEAD reveal), `code_last4`, state UNUSED/REDEEMED/REVOKED/REDEMPTION_EXPIRED.

## MG-5 auth (Phase 1B) & commerce (Phase 2)
- **`auth_contexts`** (Phase 1B) — one-time, TTL 10m; `method PMS/VOUCHER/ACCOUNT/OTP/SOCIAL/POST_STAY_PIN`; exactly-one-subject (`ac_one_subject`); method↔subject (`ac_method_subject`); PMS pins (`ac_pms_pins`); `device_id`+`guest_network_id` NOT NULL; FK → stays/accounts/vouchers/principals/post_stay_profiles/pms_interface_revisions/**`public.guest_networks`**.
- `offer_quotes`, `purchases`, `settlements` — **Phase 2 commerce** (out of 1B).

## MG-6 entitlements / devices / sessions — runtime
- `devices` (MAC = device; unique `(tenant,site,appliance,mac)`); `device_network_appearances` (→ `public.guest_networks`).
- `entitlements` — `purchase_id NOT NULL UNIQUE` (one entitlement per purchase); one-live partial uniques `ent_live_{stay|account|voucher}` + `ent_live_principal(guest_principal_id, site_id)`; counters via adjustment only; supersession.
- `entitlement_adjustments` (append-only), `entitlement_transfers` (cross-PMS), `entitlement_devices` (device slot, PK `(entitlement_id,device_id)`).
- `sessions` (lifecycle), `accounting_records` (append-only `(session_id,sample_seq)`), `session_counter_watermarks`.

## MG-9 engine functions
`trg_reject_update_delete` (immutability/append-only), `trg_secret_gen_guard`, `trg_posting_attempt_oneway`, `trg_entitlement_guard` (no exit from TERMINATED; adjustment-only decrease; same-subject supersession), `apply_adjustment`, `trg_posting_charge_gate` (**fail-closed folio CHARGE gate**), `ns_device_slot`=11, `ns_capacity`=7, `reserve_device_slot` (device admission; RECONNECT/MAX_DEVICES_REACHED/AUTHORIZED), `ingest_sample` (idempotent watermark accounting; DUPLICATE/STALE/APPLIED), `close_session` (idempotent; ALREADY_ENDED/ENDED).

## Cross-schema anchors (MG-0 → `public.guest_networks (tenant_id,site_id,id)`)
`guest_network_pms_map`, `auth_contexts.guest_network_id`, `device_network_appearances.guest_network_id`.

## Phase 1B pick-list
Identity/credential: `guest_principals`, `guest_principal_identities`, `guest_access_accounts`, `vouchers`(+`voucher_batches`,`voucher_code_key_generations`). Auth: `auth_contexts`. Runtime: `entitlements`(+one-live uniques), `entitlement_devices`, `devices`, `sessions`. Engine: `reserve_device_slot`, `ingest_sample`, `close_session`, `trg_entitlement_guard`. Interface: `pms_interfaces`, `pms_interface_revisions`. **Commerce (`offer_quotes`/`purchases`/`settlements`) = Phase 2.** **No new DDL needed for the four in-scope credential methods.**
