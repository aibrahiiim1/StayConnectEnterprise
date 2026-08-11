# StayConnect IAM — Phase 2 Execution Plan (Commercial Packages, DARK)

**Authoritative — implemented under one end-to-end Phase (Product-Owner authorized, decision D12).**
Delivery: branch `phase/2-commercial-packages` / one PR → `master`. Phase 2 deploys **live-dark**
(all Phase-2 feature flags default OFF); **no** guest-facing enablement, **no** IAM-v2 cutover.

<!-- MACHINE ASSERTION — validated by tools/project-state.py -->
`PHASE_2_PRODUCTION_RUNTIME: DARK` (no Phase-2 repository invoked, no Phase-2 runtime SQL, no Quote/
Purchase/Entitlement row created in production while flags are OFF)

## AS-BUILT FINAL STATUS

**`IMPLEMENTED + LIVE-DARK DEPLOYED + REBOOT VERIFIED; ACCEPTED_AND_CLOSED AT VERIFIED DARK MATURITY`** (authorized D12/T0012; live-dark deployment T0013; **Product-Owner ACCEPTED and CLOSED by decision D13 / closure transition T0014 (`transition_accepted: true`), 2026-07-18**; PR #4 MERGED and CLOSED (merge commit `fe6a0d1`), post-merge Governance CI green; no cutover; no paid access; no PMS settlement; no Phase 3). Phase 3 was `NOT_STARTED` at Phase-2 acceptance and was subsequently authorized separately under D14/T0015; enabling guest Commerce requires a separately authorized IAM-v2 authentication cutover.

The sections below are the **approved plan** (retained). Where the pre-implementation wording differs from what was actually built, the following as-built corrections govern (and are applied inline in §2/§4/§5/§7):

- **Revision immutability triggers were NOT added by migration 0009.** They were already provided by the accepted Phase-1A canonical schema (MG-9 `imm_plan_rev` / `imm_pkg_rev` append-only triggers) and were **preserved unchanged**. Migration `0009_phase2_commerce` adds only: (1) a null-safe Purchase↔Quote **full** pin/money/tax equality trigger; (2) an Offer-Quote **immutability-except-one-time-consume** trigger; (3) **six lookup indexes**.
- **There is no separate Phase-2 free-only schema trigger.** Free-only (`price_minor = 0`) and `settlement_methods = {NOT_REQUIRED}` are enforced in **publication + domain + UI validation** (edged writer sets those values; quote/confirm re-validate; grace requires a free CHECKOUT_GRACE revision). This keeps the schema forward-compatible with later paid/PMS phases.
- **Actual internal-socket portal routes:** `GET /v1/commerce/packages`, `POST /v1/commerce/quote`, `POST /v1/commerce/confirm` (the `/v1/packages/*` names in §4 were a planning placeholder). A trusted **portald server bridge** injects the pins; the browser submits only opaque `package_id` / `quote_id`.
- **UI surfaces:** the Guest Portal is the Go **`portald` success-page template + client JavaScript** (not a Next app); the Hotel Admin is **Next.js**. Both are deployment-flag gated and **OFF** in the live deployment.
- **Tests + deployment (as-built):** Go C-series + software gate green on the disposable DB; **45 automated UI tests** (36 Vitest+RTL, 9 Playwright E2E); an authoritative Next production build (all 31 routes); the initial deployment + one UI-only redeploy, with **two** reboot verifications.

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

Additive only; apply/twice/rollback/reapply + fingerprint clean. **As-built, migration 0009 adds ONLY items 2, 4 below** (the null-safe Purchase↔Quote pin/money/tax equality trigger, the offer-quote immutability trigger, and six lookup indexes):
1. ~~**Immutable published revisions** — BEFORE UPDATE triggers on plan/package revisions.~~ **As-built: NOT added by 0009.** These append-only immutability triggers were already provided by the accepted **Phase-1A MG-9 schema** (`imm_plan_rev` / `imm_pkg_rev`) and were preserved unchanged.
2. **Free-purchase pin equality** — a null-safe trigger enforcing that a `purchases` row referencing an
   `offer_quote` pins the SAME tenant/site/package_revision/auth_context/pms_interface/settlement_mapping
   AND the same amount/currency/exponent/tax_code/tax_rate/tax_amount as that quote (`IS NOT DISTINCT
   FROM`), even when `pms_interface_id`/`settlement_mapping_id` are NULL (the composite FK is MATCH
   SIMPLE, so it is not enforced in the free case). **(In 0009.)** Plus an **offer-quote immutability**
   trigger — a created quote's pins/price/tax/expiry/grant_snapshot are frozen; the only legal mutation
   is the one-time consume (`consumed_at` NULL → timestamp). **(In 0009.)**
3. ~~**Free-only settlement guard** — a trigger rejecting non-`NOT_REQUIRED`/priced rows.~~ **As-built:
   there is NO separate Phase-2 free-only schema trigger.** Free-only (`price_minor = 0`) and
   `settlement_methods = {NOT_REQUIRED}` are enforced in **publication + domain + UI validation**, so the
   schema stays forward-compatible with later paid/PMS phases.
4. **Six lookup indexes** — (tenant,site,active) active-package lookup; current-revision visibility;
   offer-quote by auth_context; open-quote expiry; purchase by auth_context; eligibility-rules by
   revision. **(In 0009.)**
5. **Grant-tier ordering** is already UNIQUE per revision (Phase-1A schema); no overlap is enforced in the
   schema (domain resolves deterministic first-match by `tier_order`).

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

- **Portal (scd, flag `_PORTAL`)** — **as-built routes** are `GET /v1/commerce/packages` (eligible free
  packages for an auth-context), `POST /v1/commerce/quote` (create quote), `POST /v1/commerce/confirm`
  (confirm free purchase → entitlement/session). Internal Unix-socket API only (chmod 0660
  root:stayconnect), never on a guest TCP listener; a trusted **portald server bridge** derives the
  auth-context/device/guest-network from its session and forwards them, and the browser submits only the
  opaque `package_id` / `quote_id`. Generic errors only; no identifier enumeration; no PMS/room/paid flow.
- **Hotel Admin (edged, flag `_ADMIN`)**: revisioned CRUD for plans/plan-revisions, packages/
  package-revisions, eligibility rules, grant tiers, current-revision publish, activate/deactivate +
  sale windows, free/`NOT_REQUIRED` settlement config, grace config; read-only quote/purchase
  inspection. Hotel-scoped RBAC + audit on every mutation; re-auth for destructive/retire actions per
  existing policy; validate before publish; published revisions immutable; no paid/Stripe/PMS options.

## 5. UI — portal + Hotel Admin (Next), flag-gated (env), default OFF

**As-built:** the **Guest Portal is the Go `portald` success-page template + client JavaScript** (not a
Next app) — list eligible free packages → guest-appropriate package/plan info → grants
(speed/data/time/device) → create quote → confirm → entitlement/session state; generic errors;
double-submit prevented. The **Hotel Admin is Next.js** — production-grade management screens (Packages/
Service Plans/Grace/Inspection) with a plan-revision selector, typed eligibility/tier editors, sale
windows, duration policy, revision history and grace config. Both surfaces are **deployment-flag gated and
OFF** in the live deployment (`NEXT_PUBLIC_PHASE2_ADMIN` for admin; the portal panel renders only when the
portal flag is on). No fake data in production.

## 6. Tests — C-series + software gate

C1 eligibility/tiers; C2 quote exactness + ≥24-concurrent single-Purchase races; C3 purchase
uniqueness; C4 revision immutability; C5 mapping/revision retirement; money-safety (integer minor
units, zero price, `NOT_REQUIRED`, ISO-4217, no payment/posting/refund/FX). Plus build/unit/DB-integration/
migration-lifecycle/API/portal/admin/RBAC/audit/flags-OFF-zero-SQL/reboot-config/redaction/rollback.
Disposable DB; destroy all disposable infra after the gate. Governance CI is NOT a substitute.

## 7. Live-dark deployment (appliance 172.21.60.23)

Reverify host → fresh backup + fingerprints + iam_v2 row counts → apply 0009 via the migration executor
→ **as-built: zero new runtime iam_v2 privileges were required for the DARK build** (nil repo while
flags OFF) → deploy pinned backend+UI → confirm every Phase-2 flag OFF + zero Phase-2 SQL + no invented
Quote/Purchase/Entitlement/Package data → legacy smoke tests → reboot → repeat dark + operational checks.
**As-built there were TWO reboot verifications** (first at the initial deployment `2026-07-18 08:35:06`;
second at the final acceptance-gate UI-only redeploy `2026-07-18 11:56:34`). **Infrastructure only; not
guest-enabled; no cutover.**

## 8. Governance

Decision **D12** (authorization); transition **T0012** (Phase 2 = only current/open phase); full sync of
project-state/registers/transitions/plan/Handoff/START-HERE/runbook/privilege-matrix/acceptance/evidence/
PR/packs at the final milestone. No stale Phase-1B execution text becomes current state.
