# StayConnect IAM — Phase 2 Execution Plan (Commercial Packages, DARK)

**Authoritative — implemented under one end-to-end Phase (Product-Owner authorized, decision D12).**
Delivery: branch `phase/2-commercial-packages` / one PR → `master`. Phase 2 deploys **live-dark**
(all Phase-2 feature flags default OFF); **no** guest-facing enablement, **no** IAM-v2 cutover.

<!-- MACHINE ASSERTION — validated by tools/project-state.py -->
`PHASE_2_PRODUCTION_RUNTIME: DARK` (no Phase-2 repository invoked, no Phase-2 runtime SQL, no Quote/
Purchase/Entitlement row created in production while flags are OFF)

## 0. Scope & non-scope

**In scope (one Phase):** Service Plans + immutable Plan Revisions; Internet Packages + immutable
Package Revisions; package→plan revision pinning; typed Eligibility Rules; ordered first-match Grant
Tiers; package availability/sale windows; settlement-method constraints (free/`NOT_REQUIRED` only);
server-created Offer Quotes (5-min TTL, one-time consumption); **free** Purchases only; atomic Quote +
Auth-Context consumption; portal package discovery/selection/quote/free-purchase; the
entitlement/session-after-grant integration boundary; Hotel Admin revisioned CRUD; grace
package/configuration UI.

**NOT in scope (Phase 3+):** checkout processing, grace-**entitlement creation**, PMS checkout
behavior, PMS stay resolution, paid purchases, Stripe/payment, PMS settlement/posting, folio posting,
refunds, FX, IAM-v2 cutover, legacy removal, network/HA.

## 1. Baseline (canonical schema already on `master`, dark iam_v2)

The iam_v2 schema already defines the commerce tables (Phase 1A, dark, 0 rows). Phase 2 builds domain
logic + APIs + UI **over** them and adds only additive hardening migrations. Canonical tables:
`service_plans` / `service_plan_revisions` (append-only; `current_revision_id` same-parent FK), same for
`internet_packages` / `internet_package_revisions` (revision pins `service_plan_revision_id`;
`price_minor≥0`, `currency`, `settlement_methods text[]` default `{NOT_REQUIRED}`, `visible_from/until`,
`package_type` incl. `GENERAL`/`CHECKOUT_GRACE`), `package_eligibility_rules` (`rule_type`,
`rule_value jsonb`), `package_grant_tiers` (`tier_order` UNIQUE per revision, `grant_value jsonb`),
`offer_quotes` (pins `auth_context_id`,`package_revision_id`,`pms_interface_id`,`settlement_mapping_id`,
price/currency/tax, `grant_snapshot jsonb`, `expires_at`,`consumed_at`; composite UNIQUE pin-chain),
`purchases` (`offer_quote_id UNIQUE`, composite FK to the quote's exact pins, `trigger`,`state`,
`purchase_once_per_stay` partial unique), `settlements` (`method`/`status`), `site_checkout_grace_config`.

Structural invariants already enforced by the schema: **one purchase per quote** (`offer_quote_id
UNIQUE`); **no pin substitution** (purchase↔quote composite FK); same-parent current-revision FKs;
one-time consumption columns (`consumed_at`).

## 2. Additive migrations (Phase 2, iam_v2) — migration `0009_phase2_commerce`

Additive only; apply/twice/rollback/reapply + fingerprint clean. Adds:
1. **Immutable published revisions** — BEFORE UPDATE triggers on `service_plan_revisions` and
   `internet_package_revisions` that reject mutation of business columns once created (append-only).
2. **Free-purchase pin equality** — a null-safe trigger enforcing `purchases.package_revision_id =
   offer_quotes.package_revision_id` AND `purchases.auth_context_id = offer_quotes.auth_context_id`
   even when `pms_interface_id`/`settlement_mapping_id` are NULL (the composite FK is MATCH SIMPLE, so
   it is not enforced when a pinned column is NULL, i.e. the free case).
3. **Free-only settlement guard** — a trigger rejecting an `offer_quotes`/`purchases` row for a package
   revision whose `settlement_methods ≠ {NOT_REQUIRED}` or `price_minor ≠ 0` (Phase-2 fail-closed).
4. **Eligible-package + active-revision indexes** — supporting lookups by (tenant,site,active) and
   current-revision joins and quote/purchase lookups by auth_context.
5. **Grant-tier ordering** already UNIQUE; add a validation index; no overlap enforced in domain.

No broad runtime grants; Gate-P grants extended only by the exact DARK-build needs (see §7).

## 3. Domain (Go) — `data-plane/internal/iamv2` commerce layer

Mirror the accepted dark-authenticator pattern (flags OFF ⇒ never touches the repository, zero SQL):
- **Config**: extend `LoadConfigFromEnv` with a Phase-2 master flag + per-surface flags
  (`STAYCONNECT_PHASE2_MASTER`, `_PORTAL`, `_ADMIN`), all default OFF, malformed ⇒ fail closed.
- **Eligibility** (`commerce_eligibility.go`): typed `rule_type` vocabulary (NON-PMS only):
  `DATE_WINDOW`, `AUTH_METHOD`, `SUBJECT_KIND`, `PRIOR_PURCHASE`, `SITE_NETWORK`. PMS-dependent types
  (`STAY_*`, `VIP`, `ROOM_TYPE`, `TRAVEL_AGENT`) are **capability-disabled** — recognized but fail
  closed / unavailable in the Phase-2 UI until Phase 3 provides Stay resolution.
- **Grant tiers** (`commerce_tiers.go`): deterministic ordered first-match by `tier_order`; snapshot the
  matched tier's `grant_value` into the quote's `grant_snapshot`.
- **Quote creation** (`commerce_quote.go`): server-only resolution of package revision → pinned plan
  revision, eligibility result, first-match tier, price(0)/currency/tax/settlement(`NOT_REQUIRED`),
  auth-context, tenant/site/device/guest-network; write `offer_quotes` (5-min TTL). The browser submits
  only IDs; nothing price/grant/revision is client-supplied.
- **Free purchase** (`commerce_purchase.go`): one FOR UPDATE transaction — compare-and-set consume the
  quote (`consumed_at IS NULL`) and the auth-context, create `purchases` (state `GRANTED`, `trigger
  GUEST_SELECTION`, amount 0), `settlements` (`NOT_REQUIRED`/`NOT_REQUIRED`), and create/supersede the
  Entitlement via the existing accepted engine boundary. Concurrent confirms ⇒ exactly one Purchase
  (offer_quote_id UNIQUE + CAS); losers get a deterministic generic consumed/conflict result; zero
  orphan rows. No Purchase for another Principal/Account/Voucher/Tenant/Site/Device/GuestNetwork; no
  replay.
- Reuse the accepted IAM-v2 subject / auth-context / session interfaces + lock ordering
  (`LN_DEVICE_SLOT`=11 / `LN_CAPACITY`=7); do not weaken Phase-1A/1B concurrency, device-capacity,
  supersession or accounting foundations.

## 4. APIs — scd (portal) + edged (Hotel Admin), flag-gated dark

- **Portal (scd, flag `_PORTAL`)**: `GET /v1/packages` (eligible free packages for an auth-context),
  `POST /v1/packages/quote` (create quote), `POST /v1/packages/purchase` (confirm free purchase →
  entitlement/session). Generic errors only; no identifier enumeration; no PMS/room/paid flow.
- **Hotel Admin (edged, flag `_ADMIN`)**: revisioned CRUD for plans/plan-revisions, packages/
  package-revisions, eligibility rules, grant tiers, current-revision publish, activate/deactivate +
  sale windows, free/`NOT_REQUIRED` settlement config, grace config; read-only quote/purchase
  inspection. Hotel-scoped RBAC + audit on every mutation; re-auth for destructive/retire actions per
  existing policy; validate before publish; published revisions immutable; no paid/Stripe/PMS options.

## 5. UI — portal + Hotel Admin (Next), flag-gated (env), default OFF

Portal: list eligible free packages → package/plan revision info (guest-appropriate) → grants
(speed/data/time/device) → create quote → confirm → entitlement/session state; generic errors. Hotel
Admin: production-grade management screens for the above with revision history + grace config. No fake
data in production.

## 6. Tests — C-series + software gate

C1 eligibility/tiers; C2 quote exactness + ≥24-concurrent single-Purchase races; C3 purchase
uniqueness; C4 revision immutability; C5 mapping/revision retirement; money-safety (integer minor
units, zero price, `NOT_REQUIRED`, ISO-4217, no payment/posting/refund/FX). Plus build/unit/DB-integration/
migration-lifecycle/API/portal/admin/RBAC/audit/flags-OFF-zero-SQL/reboot-config/redaction/rollback.
Disposable DB; destroy all disposable infra after the gate. Governance CI is NOT a substitute.

## 7. Live-dark deployment (appliance 172.21.60.23)

Reverify host → fresh backup + fingerprints + iam_v2 row counts → apply 0009 via the migration executor
→ apply exact least-privilege grants for the DARK build → deploy pinned backend+UI → confirm every
Phase-2 flag OFF + zero Phase-2 SQL + no invented Quote/Purchase/Entitlement/Package data → legacy
smoke tests → one reboot → repeat dark + operational checks. **Infrastructure only; not guest-enabled;
no cutover.**

## 8. Governance

Decision **D12** (authorization); transition **T0012** (Phase 2 = only current/open phase); full sync of
project-state/registers/transitions/plan/Handoff/START-HERE/runbook/privilege-matrix/acceptance/evidence/
PR/packs at the final milestone. No stale Phase-1B execution text becomes current state.
