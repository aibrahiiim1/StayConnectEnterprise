# StayConnect IAM — Phase 5 Plan
## Post-Stay PIN re-authentication + Cross-PMS Transfer

**Status: PRODUCT-OWNER ACCEPTED_AND_CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC** (decision **D22**,
closure transition **T0053**, 2026-08-14), against accepted software/runtime candidate
`aef848d253e2c6efebe4f036b0369a22530a5a25` and final verified delivery/evidence head
`4142f5fe857787a745b186d8ac38edaad7b4d268`. Nine limitations (**L5-1**–**L5-9**) are accepted and **not**
promoted to PASS. Pull request **#13 has been MERGED** to master under the separate Product-Owner
merge decision **D23** (transition **T0054**), merge commit `4f27b4d0ea4de57f9bbf6a062d9bb9d294ec6e6a`; the merge introduced no
content. The authoritative acceptance record is
[`docs/acceptance/StayConnect-IAM-Phase5-Live-Dark-Acceptance.md`](../acceptance/StayConnect-IAM-Phase5-Live-Dark-Acceptance.md)
and the final report is
[`docs/reports/StayConnect-IAM-Phase5-Final-Report.md`](../reports/StayConnect-IAM-Phase5-Final-Report.md).

*Authorized under Product-Owner decision **D21**, start transition **T0050** (2026-08-14). Base master HEAD
`d49342c0707bc40c2833b3d7782589ed0e40317f`. Branch `phase/5-poststay-transfer`, one pull request → `master`.
Delivered **DARK**: every Phase-5 flag defaults OFF. The target acceptance maturity stated when this plan was
written — **VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC** on the **development** appliance — is the maturity
the phase was accepted at.*

**This document is preserved as the plan it was.** The sections below describe what was planned and why; they
are not rewritten to match the delivery. Where implementation disproved a planning assumption, the correction
is recorded in the evidence document and in the transition receipts, not by editing this file.

**Explicitly NOT in scope (fail-closed, governance-enforced):** paid Post-Stay, settlement or any price other
than zero; PMS financial posting, `PS`/`PA`, `P#` allocation; payment-provider traffic, refunds or reversals;
paid guest access; Phase-4 feature-flag enablement; IAM-v2 authentication cutover; Production database contact
or migration; deployment to anything other than the development appliance; creation of any **synthetic Stay**
or synthetic PMS state; any **guest-facing PMS selector**; automatic or inferred transfer; room-move-as-transfer;
per-sharer Post-Stay identities; `stay_links(reason='POST_STAY')`; Phase 6; Phase 7; merging the Phase-5 PR.

---

## 1. Two findings that shaped this plan

**1.1 `stay_links(reason='POST_STAY')` is not groundable and is not written.** `iam_v2.stay_links` declares
`from_stay` **and** `to_stay` as `NOT NULL` composite foreign keys to `stays`. Post-Stay has exactly one real
Stay — the origin. Writing that row would require inventing a destination Stay, i.e. synthetic PMS state. The
`POST_STAY` enum value appears only in the §4.2 schema sketch of the FINAL contract; the normative clause §7.3
grounds Post-Stay lineage in *"profiles are unique per episode with read-only origin lineage"* — which is
`post_stay_profiles.(origin_stay_id, origin_lifecycle_version)`, already present. `stay_links` is therefore
written by the **Cross-PMS transfer path only**, where §7.4 names it and both ends are real verified Stays. The
enum value remains reserved and unused. No schema change, no synthetic data, no contract deviation.

**1.2 Cross-PMS transfer detection needs no new engine or table.** `iam_v2.auth_resolutions` already records
STRICT outcome codes — including `AMBIGUOUS` — with `resolved_stay_id` and no guest data, and Hotel Admin
already renders it. Ambiguity is a **review signal only**: no code path may read it to authorize a transfer.

---

## 2. Invariants

Each is enforced in the database wherever it can be, and each has at least one adversarial test that is shown
failing before the guard exists and passing after.

| # | Invariant |
|---|---|
| I-1 | A Post-Stay profile is bound to exactly one Stay **episode** (`UNIQUE(origin_stay_id, origin_lifecycle_version)`) |
| I-2 | PIN authentication succeeds only while the origin Stay's **current** `lifecycle_version` equals the profile's `origin_lifecycle_version` **and** its status is `CHECKED_OUT` or `POST_STAY_ACTIVE` |
| I-3 | Post-Stay identity is never room-owned — no room-keyed lookup path exists |
| I-4 | A PIN is never issued to an unauthenticated requester; there is no anonymous issuance endpoint to attack |
| I-5 | PIN material is write-only (argon2id), revealed exactly once, and never appears in logs, telemetry, exports, audit payloads or error text |
| I-6 | Post-Stay grants no posting permission, ever (`posting_only_in_house` makes it unrepresentable) |
| I-7 | Post-Stay v1 is zero-price; a priced or settlement-requiring revision is refused |
| I-8 | A transfer requires two verified Stays that **already exist**; no Stay is ever created |
| I-9 | A transfer requires two **different PMS interfaces** |
| I-10 | A transfer is not a supersession: the transfer-created entitlement carries no `supersedes_entitlement_id` |
| I-11 | Transfer lineage is cycle-free |
| I-12 | A transfer is idempotent |
| I-13 | Ambiguity is a signal, never an authority |
| I-14 | No guest-facing PMS selector exists |
| I-15 | Flags OFF ⇒ zero Phase-5 SQL and zero Phase-5 routes; a child flag without its master is a startup failure |

---

## 3. Milestones

1. **Foundation + security** — schema and guards, episode-bound Post-Stay identity, PIN lifecycle, transfer
   invariants, and the auth-context integration for `POST_STAY_PIN`, reconciling the PMS-specific
   implementation rather than assuming it can be reused.
2. **Post-Stay vertical slice** — authenticated issuance, one-time reveal, throttle, PIN re-authentication,
   zero-price conversion, operator reset/revoke, Portal and Hotel-Admin surfaces, full F8/security evidence.
3. **Cross-PMS Transfer vertical slice** — review signal, explicit verified source/destination Stays,
   staff-confirmed atomic typed transfer, lineage/state/cycle/idempotency enforcement, seamless device and
   session rebind, UI, full F9/concurrency evidence.
4. **Hardening + final LIVE-DARK candidate** — full Phase-3 and Phase-4 regression, the Phase-5 adversarial
   matrix, least-privilege proof derived from an actual privilege audit, authoritative CI, then controlled
   DARK deployment on the development appliance with migrations, reboot, real scratch restore, rollback
   rehearsal, darkness re-verification, evidence and documentation synchronisation.

Least privilege is **derived**, not pre-committed: the privilege audit measures what the delivered objects
actually require, the minimum is granted, and the refusals are proven as the real roles.

---

## 4. Adversarial verification matrix

| # | Hazard | Required observation |
|---|---|---|
| F8-a | The next occupant of the same room presents the previous guest's PIN | refused, no oracle |
| F8-b | Reinstatement (`lifecycle_version++`), then the old PIN | refused — the profile is bound to the dead episode |
| F8-c | PIN issued before checkout, stay then `CANCELLED`/`NO_SHOW` | never activates |
| F8-d | Room-number-only request | no such endpoint exists |
| F8-e | A second profile for the same episode | rejected by the unique index |
| F8-f | PIN in logs, telemetry, exports, audit or error text | zero occurrences |
| F8-g | Brute force across service restart and appliance reboot | durable throttle and lockout; fail-closed on database failure |
| F8-h | Timing or shape difference between wrong PIN and no profile | constant-shape response |
| F9-a | Transfer to a destination Stay that does not exist | refused; no Stay created |
| F9-b | Transfer within one PMS interface | refused |
| F9-c | An ordinary room move | still takes the F1 path; no transfer row |
| F9-d | A transfer that sets `supersedes_entitlement_id` | rejected |
| F9-e | `A→B→A` and longer cycles | rejected |
| F9-f | Duplicate and concurrent transfer (≥24 handlers) | exactly one row; idempotent |
| F9-g | Session continuity across transfer | rebound in place, zero nft churn, no re-authentication |
| F9-h | An `AMBIGUOUS` resolution used to authorize | impossible; no such code path |
| F9-i | Transfer racing checkout or grace | one transaction wins; no split state |
| X-1 | A priced or settlement-requiring Post-Stay revision | refused; zero outbox rows, zero `P#` |
| X-2 | Master flag OFF | zero Phase-5 SQL, zero routes, zero PMS connections |
| X-3 | A child flag without its master | startup failure |
| X-4 | Cross-tenant / cross-site / cross-interface fuzz | rejected at the SQL layer |
| REG | Phase-3 F1–F7 and the Phase-4 suite | unchanged |

---

## 5. Acceptance criteria (LIVE-DARK, development appliance only)

1. Migration integrity — each applied once, recorded once, schema matching the expected fingerprint.
2. Least privilege — derived, measured, and negatively proven as the real roles.
3. DARK — every Phase-5 flag absent from every env file and unit; every Phase-5 route 404 on the running
   services; zero financial egress; the legacy `public` schema structurally unchanged and still the sole
   authentication authority.
4. Service health across `scd`, `edged`, `pmsd`, `netd`, `acctd`.
5. Reboot persistence — an authorized reboot, after which state and darkness survive.
6. A supported backup taken and really restored into a scratch database.
7. The documented rollback rehearsed and reversed, verified by the **structural** fingerprint (a DOWN→UP cycle
   legitimately shifts `ordinal_position`).
8. Darkness re-proved after every drill.
9. The full adversarial matrix and the Phase-3/4 regression green on the exact final candidate head, with CI
   run identifiers and artifact digests recorded.
10. Production never migrated and never contacted.

Any failure in any of these fails closed. Phase 5 is not marked ACCEPTED or CLOSED without a Product-Owner
decision.
