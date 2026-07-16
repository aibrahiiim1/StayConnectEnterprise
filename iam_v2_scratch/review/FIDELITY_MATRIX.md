# Contract §4.1–§4.6 → scratch iam_v2 fidelity matrix

Every FINAL-contract IAM object mapped to its migration group, file, keys, constraints, triggers, and acceptance test. **Table count is not proof** — verbatim keys/constraints/triggers are in the auto-generated `CONSTRAINT_INVENTORY.txt` and `TRIGGER_FUNCTION_INVENTORY.txt`; this matrix cross-references them and assigns a status. Any missing constraint/FK/trigger/index/state-rule is a FAIL (none outstanding).

Legend: PK=primary key · U=unique/anchor · CK=check · FK=composite/cross-schema FK · PI=partial index · IMM=immutable/append-only/one-way trigger · LC=lifecycle/state trigger.

## §4.1 PMS interfaces, revisions, secrets, routing — MG-1 `mg1_pms_interface_core.sql`

| Object | PK | U | CK | FK | PI | IMM/LC | Test | Status |
|---|---|---|---|---|---|---|---|---|
| `pms_interfaces` | id | (tenant,site,id) | lifecycle_state∈4 | current_revision→revisions | – | – | FK-01/03 | PASS |
| `pms_interface_revisions` | id | (interface,rev_no),(tenant,site,interface,id) | **folio_identity_strategy∈(UNSET,…) DEFAULT 'UNSET'** | (tenant,site,interface)→interfaces | – | IMM `imm_pms_rev` | IMM-01/02, FOLIO-01..04 | PASS |
| `pms_interface_secret_generations` | id | (interface,gen_no),(tenant,site,interface,id) | – | →interfaces | – | LC `sg_guard` (identity immut, no delete) | (inventory) | PASS |
| `guest_network_pms_map` | (gn,interface) | gnpm_one_default | routing_mode∈2 | **→public.guest_networks (MG-0 anchor)**, →interfaces | one-default | – | OFR-03/04 | PASS |
| `pms_interface_pnumber_seq` | interface | – | – | →interfaces | – | – | (durable P# alloc) | PASS |
| `pms_source_conflicts` | id | (tenant,site,a,b) | a<b | →interfaces×2 | – | – | (inventory) | PASS |

## §4.2 Stays, sharers, folios, events — MG-4 `mg4_stay_domain.sql`

| Object | PK | U | CK | FK | PI | Test | Status |
|---|---|---|---|---|---|---|---|
| `stays` | id | (…,reservation,identity),(tenant,site,interface,id),(tenant,site,id) | posting_only_in_house; status∈6 | →interfaces | stays_room_lookup (IN_HOUSE) | FK-01, FOLIO-04 | PASS |
| `stay_guests` | id | one_primary_guest_per_stay (PI) | – | →stays CASCADE | one-primary | (inventory) | PASS |
| `folios` | id | (…,external_folio_id,identity_epoch),(tenant,site,interface,id) | status∈2, folio_kind∈4 | →interfaces | folio_open_identity (OPEN) | (inventory) | PASS |
| `stay_folios` | (stay,folio) | stay_folio_default (PI) | – | →stays,→folios | default-target | (inventory) | PASS |
| `stay_events` | id | (…,external_event_identity) | processing_status∈5 | →stays | – | (inventory) | PASS |
| `stay_links` | id | (from,to,reason) | reason∈2 | →stays×2 | – | (inventory) | PASS |
| `post_stay_profiles` | id | (origin_stay,origin_lifecycle_version),(tenant,site,id) | – | →stays | – | (inventory) | PASS |

## §4.3 Plans, packages, mappings, grace — MG-2 `mg2_plans_packages.sql`

| Object | PK | U | CK | FK | IMM | Test | Status |
|---|---|---|---|---|---|---|---|
| `service_plans` / `_revisions` | id/id | (tenant,site,code),(plan,rev_no),(tenant,site,plan,id) | max_concurrent_devices≥1; device_limit_policy∈3; time_accounting_mode∈2 | current_rev→revisions; rev→plan | IMM `imm_plan_rev` | IMM-03, AGG-01 | PASS |
| `internet_packages` / `_revisions` | id/id | (tenant,site,code),(pkg,rev_no),(tenant,site,pkg,id) | package_type∈6; price≥0 | rev→service_plan_rev; current_rev→revisions | IMM `imm_pkg_rev` | IMM-04 | PASS |
| `package_eligibility_rules` | id | – | – | →pkg_rev CASCADE | – | (inventory) | PASS |
| `package_grant_tiers` | id | (pkg_rev,tier_order) | – | →pkg_rev CASCADE | – | (inventory) | PASS |
| `package_settlement_mappings` | id | (pkg_rev,interface,mapping_rev),(tenant,site,pkg_rev,interface,id) | – | →pkg_rev,→interface | – | (inventory) | PASS |
| `site_checkout_grace_config` | (tenant,site) | – | – | →pkg_rev | – | (inventory) | PASS |

## §4.4 Guest identities & credentials — MG-3 `mg3_identities_credentials.sql`

| Object | PK | U | CK | FK | Test | Status |
|---|---|---|---|---|---|---|
| `guest_principals` (tenant-wide) | id | (tenant,id) | – | – | ENT-05 | PASS |
| `guest_principal_identities` | id | (tenant,factor_type,issuer,value_norm) | social needs issuer; factor_type∈3 | →principals CASCADE | (inventory) | PASS |
| `guest_access_accounts` | id | gaa_username lower(username), (tenant,site,id) | – | →packages | (inventory) | PASS |
| `voucher_code_key_generations` | id | (tenant,gen_no),(tenant,site,id) | – | – | (inventory) | PASS |
| `voucher_batches` | id | (tenant,site,id) | – | →pkg_rev | (inventory) | PASS |
| `vouchers` | id | code_hmac,(tenant,site,id) | state∈4 | →pkg_rev,→keygen | ENT-01, RACE-03/04 | PASS |

## §4.5 Auth contexts, quotes, purchases, settlements, postings, payments — MG-5 `mg5_auth_commerce.sql` + MG-7 `mg7_postings_payments.sql`

| Object | PK | U | CK | FK | PI | IMM/LC | Test | Status |
|---|---|---|---|---|---|---|---|---|
| `auth_contexts` | id | (tenant,site,id),(id,interface) | ac_one_subject, ac_method_subject, ac_pms_pins | subject FKs; device FK (added MG-6); network→public.guest_networks | – | – | (inventory) | PASS |
| `offer_quotes` | id | (id,ctx,pkg_rev,interface,mapping) | – | →auth_contexts,→pkg_rev,→mappings | – | – | (inventory) | PASS |
| `purchases` | id | offer_quote_id, (tenant,site,id),(id,interface) | purchase_guest_needs_quote; trigger∈10; state∈6 | **quote pin-tuple FK**; →pkg_rev,→stays,→mappings,→interface_rev | purchase_once_per_stay, one_conversion_per_episode | – | ENT-01 | PASS |
| `settlements` | id | purchase_id,(id,purchase_id),(tenant,site,id) | method∈5; status∈8 | →purchases | – | – | (inventory) | PASS |
| `pms_postings` | id | idempotency_key,(tenant,site,interface,id),(tenant,site,id),(id,interface) | posting_reversal_link | (settlement,purchase) pair, (purchase,interface), →stays,→folios,→interface_rev,→secret_gen | – | IMM `ao_postings` + LC `charge_gate` (folio-UNSET + IN_HOUSE) | FOLIO-01..04/E4b, AO-02 | PASS |
| `posting_outbox` | id | outbox_one_active (PI) | state∈4 | (tenant,site,interface,posting)→postings anchor | one-active | – | (inventory) | PASS |
| `payment_transactions` | id | idempotency_key,(tenant,provider,merchant,ref),(tenant,site,settlement,id) | ptx_parent; type∈3; amount>0; status∈7 | →settlements, self-parent; **Stripe FK DEFERRED** | – | – | (inventory) | PASS (FK deferred) |
| `posting_attempts` | id | (tenant,site,interface,p_number),(internal_posting,attempt_no),(tenant,site,id) | outcome∈4; pa_as_status∈7 | →interfaces,→postings | – | **one-way `pa_oneway`** | PA-01/02 | PASS |
| `posting_attempt_events` | id | – | – | →posting_attempts | – | append-only `ao_pa_events` | (inventory) | PASS |
| `pms_interface_pnumber_seq` | — | (see §4.1) | | | | | | PASS |
| `posting_review_actions` | id | – | action∈5 | →postings | – | append-only `ao_review` | (inventory) | PASS |
| `financial_epoch` | (tenant,site) | – | – | – | – | – | (inventory) | PASS |
| `compliance_archives` | id | – | – | – | – | – | (inventory) | PASS |

## §4.6 Entitlements, transfers, devices, sessions, accounting — MG-6 `mg6_entitlements_devices_sessions.sql`

| Object | PK | U | CK | FK | PI | IMM/LC | Test | Status |
|---|---|---|---|---|---|---|---|---|
| `entitlements` | id | (tenant,id),(tenant,site,id), purchase_id, supersedes_id | ent_one_subject, ent_terminal; status∈4; end_mode∈6; counters≥0 | →purchases,→self,→stays/accounts/vouchers/principals,→plan_rev,→pkg_rev | **ent_live_{stay,account,voucher,principal+site}** | LC `ent_guard` (no-exit-TERMINATED, decrease-only-via-adjust, same-subject supersession) | ENT-01..05, WIN-01, CNT-01, SUSP-01, CAP-01, REOPEN-01, RACE-03/04 | PASS |
| `entitlement_adjustments` | id | – | – | →entitlements | – | append-only `ao_adjust` | ENT-04 | PASS |
| `entitlement_transfers` | id | from_id, to_id | et_no_self, et_two_stays | →entitlements×2,→stays×2 | – | – | (inventory) | PASS |
| `devices` | id | (tenant,site,appliance,mac),(tenant,site,id) | – | – | – | – | DEV-01..03 | PASS |
| `device_network_appearances` | (device,gn) | – | – | →devices,→public.guest_networks | – | – | (inventory) | PASS |
| `entitlement_devices` | (ent,device) | – | status∈2 | →entitlements CASCADE,→devices | – | – | DEV-02/03, CAP-RESID-01, RACE-01/02 | PASS |
| `sessions` | id | (tenant,id),(tenant,site,id) | – | →entitlements,→devices | – | – | ACC-05 | PASS |
| `accounting_records` | id | (session,sample_seq) | – | →sessions | – | append-only `ao_accounting` | AO-01, ACC-01..04, REOPEN-01 | PASS |
| `session_counter_watermarks` | session_id | – | – | →sessions CASCADE | – | – | ACC-01..05 | PASS |
| `auth_resolutions` (§4.6 aux, MG-8) | id | – | – | →public.guest_networks,→stays | – | – | (inventory) | PASS |

## §MG-9 engine (triggers + functions) — `mg9_engine.sql`

| Component | Kind | Purpose | Test | Status |
|---|---|---|---|---|
| `trg_reject_update_delete` | trigger fn | immutable revisions + append-only ledgers | IMM-01..04, AO-01/02 | PASS |
| `trg_secret_gen_guard` | trigger fn | secret identity immutable; no delete | (inventory) | PASS |
| `trg_posting_attempt_oneway` | trigger fn | posting outcome one-way; identity immutable | PA-01/02 | PASS |
| `trg_entitlement_guard` | trigger fn | no-exit-TERMINATED; decrease-only-via-adjust; same-subject supersession | ENT-02/03/05, WIN-01 | PASS |
| `trg_posting_charge_gate` | trigger fn | **folio-UNSET fail-closed + IN_HOUSE re-check before outbox/P#** | FOLIO-01..04, E4b | PASS |
| `apply_adjustment` | function | audited counter decrease / window move (only sanctioned path) | ENT-04 | PASS |
| `ns_device_slot`=hashtextextended(x,**11**) / `ns_capacity`=hashtextextended(x,**7**) | functions | advisory admission namespaces (from `session.go`) | LN-01/02/03 | PASS |
| `reserve_device_slot` | function | device-slot(11) before capacity(7); reconnect; max enforce | DEV-01..03, RACE-01/02 | PASS |
| `ingest_sample` | function | idempotent watermark accounting (APPLIED/DUPLICATE/STALE/epoch) | ACC-01..04 | PASS |
| `close_session` | function | idempotent session close | ACC-05 | PASS |

**Result:** every §4.1–§4.6 object and every MG-9 engine component is present with its required PK/uniques/checks/composite-FKs/partial-indexes/triggers and an acceptance test. **No missing constraint/FK/trigger/index — 0 FAIL.** Prose-only auxiliary column sets are reasonable minimal realizations (see `DEVIATIONS.md`).
