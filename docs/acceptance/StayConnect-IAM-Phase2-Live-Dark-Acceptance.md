# StayConnect IAM Phase 2 — Live-Dark Acceptance **Candidate**

**Status: CANDIDATE — pending a single Product-Owner acceptance decision. NOT self-accepted; NOT closed. PR #4 remains unmerged.**

- Phase: 2 (Commercial Packages) — one end-to-end Phase.
- **Authorized** under Product-Owner decision **D12**, authorization/start transition **T0012**.
- **Deployment candidate** recorded by transition **T0013** (live-dark deploy + reboot; `transition_accepted: false`).
- **Not yet Product-Owner accepted** — this record requests that decision; it does not self-accept.
- Branch: `phase/2-commercial-packages`; PR #4 (unmerged).
- Maturity offered for acceptance: **verified DARK** (implementation + automated UI tests + live-dark deployment + **two** reboots, each with post-reboot re-verification).
- Appliance: `radius` / `172.21.60.23`.

## Final acceptance-gate additions
- **45 automated UI tests, all green:** 36 Vitest + React Testing Library (component/unit) + 9 Playwright E2E (3 Hotel Admin against the real Next app with edged mocked; 6 Guest Portal driving the real portald success-page template).
- **Authoritative production build:** `NODE_OPTIONS=--max-old-space-size=12288 npm run build` — EXIT 0, `✓ Compiled successfully`, **`✓ Generating static pages (31/31)`**.
- **Current deployed hotel-admin bundle SHA-256:** `678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95` (release `20260718-115608`). Go binaries unchanged (`1e25f9ef`/`30ed45f1`/`bf400654`).
- **Two reboot verifications:** first at `2026-07-18 08:35:06` (initial deployment), second at `2026-07-18 11:56:34` (final UI-only redeploy). Both re-verified full darkness.
- Governance: authorized under D12/T0012; deployment candidate transition T0013; `transition_accepted: false`.

## What is delivered (all DARK, flags OFF)
- **Schema** — additive migration `0009_phase2_commerce` (null-safe Purchase↔Quote money-pin equality trigger; offer-quote immutability-except-one-time-consume trigger; lookup indexes). Applied live, `iam_v2_owner`-owned, public schema unchanged.
- **Domain/engine** — typed eligibility + publication validation; typed grant snapshots; authoritative ISO-4217 currency; immutable duration policy (PMS/checkout modes capability-disabled); free-only quote/confirm with rollback-at-every-boundary + tamper defense; one-live-entitlement-per-subject supersession; guest eligible-package listing (read-only, no disclosure of ineligible packages).
- **APIs** — scd guest-portal `GET /v1/commerce/packages`, `POST /quote`, `POST /confirm` (internal Unix-socket, server-derived pins); edged Hotel-Admin commerce-admin (packages + immutable revision history, service plans + revisions, typed rule/tier publish, grace config with validation, read-only PII-free inspection) with RBAC + audit + step-up; portald trusted server bridge (browser submits only opaque ids).
- **UI** — guest Portal package-selection panel; full Hotel-Admin management UI (packages/plans/revisions/grace/inspection). Both hidden behind deployment flags.

## Verification (see evidence records)
- Software gate: `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md` (Go build/vet/test/gofmt green + disposable infra; 45 UI tests; authoritative 31/31 production build).
- Live-dark + reboots: `docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md` (current + historical artifact hashes; migration 0009; darkness verified before AND after BOTH reboots; zero runtime iam_v2 privileges; iam_v2 49/0; public schema unchanged; legacy auth sole authority).
- Final report: `docs/reports/StayConnect-IAM-Phase2-Final-Report.md`. Change manifest: `docs/manifests/Phase2-change-manifest.md`.

## Explicitly NOT done (out of scope / requires separate authorization)
- No Phase-2 flag enabled; no guest paid access; no PMS settlement/posting/folio/tax; no IAM-v2 cutover or data migration; no Phase 3; no self-acceptance; PR #4 not merged.

## Requested decision
Accept Phase 2 at verified DARK maturity (record acceptance decision + transition), **or** return findings. No enablement or cutover is requested by this record.
