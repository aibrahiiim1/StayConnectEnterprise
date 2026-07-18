# StayConnect IAM Phase 2 — Live-Dark Acceptance **Candidate**

**Status: CANDIDATE — pending a single Product-Owner acceptance decision. NOT self-accepted; NOT closed. PR #4 remains unmerged.**

- Phase: 2 (Commercial Packages) — one end-to-end Phase under Product-Owner authorization **D12**, transition **T0012** (`transition_accepted: false`).
- Branch: `phase/2-commercial-packages`; PR #4.
- Maturity offered for acceptance: **verified DARK** (implementation + live-dark deployment + one reboot + post-reboot re-verification).
- Appliance: `radius` / `172.21.60.23`.

## What is delivered (all DARK, flags OFF)
- **Schema** — additive migration `0009_phase2_commerce` (null-safe Purchase↔Quote money-pin equality trigger; offer-quote immutability-except-one-time-consume trigger; lookup indexes). Applied live, `iam_v2_owner`-owned, public schema unchanged.
- **Domain/engine** — typed eligibility + publication validation; typed grant snapshots; authoritative ISO-4217 currency; immutable duration policy (PMS/checkout modes capability-disabled); free-only quote/confirm with rollback-at-every-boundary + tamper defense; one-live-entitlement-per-subject supersession; guest eligible-package listing (read-only, no disclosure of ineligible packages).
- **APIs** — scd guest-portal `GET /v1/commerce/packages`, `POST /quote`, `POST /confirm` (internal Unix-socket, server-derived pins); edged Hotel-Admin commerce-admin (packages + immutable revision history, service plans + revisions, typed rule/tier publish, grace config with validation, read-only PII-free inspection) with RBAC + audit + step-up; portald trusted server bridge (browser submits only opaque ids).
- **UI** — guest Portal package-selection panel; full Hotel-Admin management UI (packages/plans/revisions/grace/inspection). Both hidden behind deployment flags.

## Verification (see evidence records)
- Software gate: `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` (build/vet/test/tsc green; disposable infra only).
- Live-dark + reboot: `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` (pinned artifact hashes; migration; darkness verified before AND after one reboot; zero runtime iam_v2 privileges; iam_v2 49/0; public schema unchanged; legacy auth sole authority).

## Explicitly NOT done (out of scope / requires separate authorization)
- No Phase-2 flag enabled; no guest paid access; no PMS settlement/posting/folio/tax; no IAM-v2 cutover or data migration; no Phase 3; no self-acceptance; PR #4 not merged.

## Requested decision
Accept Phase 2 at verified DARK maturity (record acceptance decision + transition), **or** return findings. No enablement or cutover is requested by this record.
