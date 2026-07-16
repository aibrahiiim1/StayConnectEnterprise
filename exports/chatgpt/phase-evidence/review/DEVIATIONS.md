# DEVIATIONS — scratch implementation vs FINAL contract

Deviations are **scratch-environment** deviations only; none change approved architecture. Each is either a faithful scratch stand-in or an explicitly deferred/not-applicable item.

## Faithful scratch stand-ins (not architectural changes)

1. **Platform fixtures in `public`.** `public.tenants`, `public.sites`, `public.guest_networks` are minimal stand-ins for the *existing* platform. The **offline real-schema test** (`OFR-01..09`) additionally builds `iam_v2` on top of the **committed real platform migration chain** `data-plane/migrations/0001..0006` and proves the `iam_v2` catalog is **identical** (fingerprint `bd75026f…`) — so the fixture is not load-bearing for fidelity.
2. **`auth_contexts.device_id` FK added in MG-6.** The contract lists it under `auth_contexts` (§4.5); the scratch orders it after `devices` (MG-6) to respect table creation order. Same object, same constraint.
3. **Scratch harness tables in `public`:** `_scratch_marker` (disposable marker for the allowlist guard) and `_iam_v2_migrations` (migration ledger enabling idempotent re-apply). These are orchestration infrastructure, **not** IAM objects, and do not exist in the contract; they are excluded from the "no accidental public objects" check.
4. **Auxiliary tables materialized concretely.** Contract §4.x describes some auxiliary tables in prose (`package_eligibility_rules`, `package_grant_tiers`, `stay_folios`, `stay_events`, `stay_links`, `post_stay_profiles`, `device_network_appearances`, `entitlement_adjustments`, `pms_source_conflicts`, `auth_resolutions`, `posting_review_actions`, `financial_epoch`, `compliance_archives`); scratch materializes them with the stated keys/FKs. Column sets for prose-only tables are a **reasonable minimal realization**, not a contract quote — flagged for review at live-build time.

## Deferred (later approved phase — NOT scratch failures)

- Aggregate data-cap **enforcement loop** and **reconciliation rebuild** (A5/A13) — `acctd`/reaper + a later reconciliation procedure. DB primitives proven; enforcement deferred.
- Programmatic reversal — `capability=false`; no executable reversal built (contract §9d). REVERSAL is only a passive ledger row + linkage.
- Stripe merchant-account FK — deferred to the payment phase (no `(tenant_id,id)` anchor exists on `public.stripe_accounts`; not invented).
- `AGGREGATE_ONLINE_TIME` — enum-present, behaviorally inert (v1).

## Not applicable to a pure-SQL scratch DB (NEVER counted as PASS)

Appliance reboot on real hardware/VM; real `scd`/`acctd`/`portald`/`edged` integration; nft/tc zero-churn; running-production-service zero-write; live DSN/`search_path` behavior; real-traffic accounting; service-driven session revocation side effects. See `TEST_MATRIX.md` (N/A-SCRATCH rows).

## Open item

- `folio_identity_strategy` fail-closed amendment is **implemented** (`DEFAULT 'UNSET'`, CHARGE gate) and proven (`FOLIO-01..04`, `E4b`). No open folio item remains.

## No secrets / no guest PII

Scratch artifacts contain no passwords, tokens, private keys, or guest identifiers (redaction/scan `SEC-01/02`). The disposable container password lives only in a one-off `docker run` command, never committed.
