# StayConnect IAM — Phase 5 execution evidence

Authorization **D21** / transition **T0050**. Branch `phase/5-poststay-transfer`, base master
`d49342c0707bc40c2833b3d7782589ed0e40317f`. Delivered **DARK**; every Phase-5 flag defaults OFF.

This file is append-only in intent: each milestone adds its section and its Git-derived inventory, and no
earlier section is rewritten. Where a later milestone corrects an earlier decision the correction is recorded
here rather than by editing the record of what was believed at the time.

---

## Milestone 1 — Foundation + security · ACCEPTED (Product-Owner checkpoint)

**Head:** `50e641bafe6bc94cb6823493c3738af9bbbaaab2`

Schema guards, episode-bound Post-Stay identity, PIN lifecycle, transfer invariants and the `POST_STAY_PIN`
auth-context integration. Migration **0027**.

The auth-context package was reconciled rather than reused. Two findings, both load-bearing:

* the PMS **issuer** demands `IN_HOUSE`, an interface Revision, occupancy evidence and freshness — every one
  of those is false for a valid post-stay context, so `IssuePostStayTx` is its own path;
* `ConsumeTx` re-verifies live subject state **per method**, and the check was a literal test for the PMS
  method. A method with no arm gets **no** re-verification, so a `POST_STAY_PIN` context minted seconds
  before a reinstatement would have stayed usable for its whole TTL. Proven load-bearing: with the new arm
  disabled the reinstatement and revocation tests both accept the context.

**Gates:** foundation 70/70 · lifecycle 18/18 · authctx post-stay 7/7 + PMS suite unregressed.

---

## Milestone 2 — Post-Stay vertical slice · COMPLETE

**Head:** `def9b8a7d3d439fec0a5dd2af693abb4a5e3ecd1`
(M2 core `b36ae80689a7ae7ffbcd31892f3df57abab5ef10`; exposure layer `def9b8a`.)

### Verified gate results at that head

| Gate | Result |
|---|---|
| `iam_v2_scratch/phase5_0027_foundation.sh` | **70 / 70** |
| `iam_v2_scratch/phase5_0027_lifecycle.sh` | **19 / 19** |
| `internal/poststay` (F8 + uniformity + device lineage) | **13 / 13** |
| `internal/authctx` (post-stay) | **7 / 7** |
| `cmd/edged` operator API | **6 / 6** |
| `internal/iamv2` Phase-5 flag gating | **4 / 4** |
| `hotel-admin` browser (operator screen) | **5 / 5** |

### What M2 established

* **The guest never names their own subject.** No stay, room, PMS-interface or profile parameter exists on
  the guest surface — absent, not validated. Issuance derives the eligible Stay from the device's own
  authorization lineage; verification derives the candidate profiles the same way.
* **Reset ≠ Revoke.** Reset rotates an ACTIVE profile's credential. Revoke is terminal for that Stay episode:
  not reversible, no replacement profile, and a reset against a revoked profile is refused (409).
* **`pin_revealed_at` is not proof of delivery.** It records that the server RETURNED a plaintext. A lost
  response is recovered only by minting a NEW generation.
* **Uniform non-success**, asserted identical down to the error text across wrong PIN, no PIN, expired,
  revoked, stale episode, no post-stay identity, and Phase 5 being dark.

### Corrections recorded during M2

* **Migration 0029** restated the one-time-reveal rule. 0027 refused a profile created already-revealed, on
  the assumption that revealing was a separate later act; implementing issuance disproved it, because the
  plaintext exists only in the minting response. 0029 makes the reveal happen AT MINT and is strictly
  stronger — it also refuses a profile created *without* one.
* **Migration 0028** gave post-stay its own throttle method rather than borrowing an existing one.

### M2 fix-forwards (carried during M3, not a reopening)

1. **The public guest surface is now strict.** scd's internal decoder already refused unknown fields, but
   portald — the process a guest's browser actually talks to — decoded permissively and would have **silently
   dropped** an identity-looking field. Not exploitable, and indistinguishable from a surface that honours
   it, which is what invites someone to build against it. Now refused outright, as the ordinary uniform
   non-success, before any hop to scd.

### Known limitations carried forward

| # | Limitation | Status |
|---|---|---|
| L5-1 | `POST_STAY_ACTIVE` has **no exit transition**. The FINAL contract draws no arrow out of it, so a reinstatement for a converted Stay is refused by the guard and lands in the operator's queue. | **Recorded and tested** (F8-i). Product Owner confirmed: do not invent the transition in this phase. |
| L5-2 | Post-Stay v1 is **zero-price only**. A priced or settlement-requiring revision is refused rather than granted free. | By design under D21. Paid post-stay is a Phase-4-enablement question, not a Phase-5 one. |
| L5-3 | Post-stay verification requires the guest to be on a **device that was authorized during the stay**. A guest on an entirely new device gets the uniform non-success. | Deliberate: it is what removes the client-supplied-subject parameter altogether. Fails in the safe direction. |
| L5-4 | `stay_links(reason='POST_STAY')` is **never written**. Both its ends are NOT NULL Stays and post-stay has one. | Enforced by the database guard; the enum value stays reserved. |

### Git-derived inventory — M1 `50e641b` → M2 `def9b8a` (30 paths)

| Change | Path | Purpose |
|---|---|---|
| M | `data-plane/cmd/edged/auth.go` | `post-stay-profiles` added to the role→permission matrix (write for IT manager, front office, guest relations; read for viewer) |
| M | `data-plane/cmd/edged/main.go` | Phase-5 config load + DARK mount of the operator surface |
| A | `data-plane/cmd/edged/phase5_poststay_api_integration_test.go` | operator API contract: RBAC, step-up, reason, audit-without-the-secret, cross-site indistinguishability |
| A | `data-plane/cmd/edged/resources_phase5_poststay.go` | operator endpoints: list, get, reset (rotate), revoke (terminal) |
| M | `data-plane/cmd/portald/main.go` | guest post-stay routes, mounted unconditionally as pure proxies |
| A | `data-plane/cmd/portald/phase5_poststay_handlers.go` | guest-facing handlers over the Phase-3 response-time budget and uniform-failure builder |
| M | `data-plane/cmd/scd/main.go` | Phase-5 flag load, guest route mount, and the refusal to serve the guest surface without the Phase-3 arm |
| A | `data-plane/cmd/scd/phase5_poststay.go` | issue / verify / convert, all device-derived |
| M | `data-plane/internal/authctx/poststay_integration_test.go` | seeds updated for the 0029 reveal-at-mint rule; unconditional transaction rollback |
| A | `data-plane/internal/iamv2/phase5_config.go` | Phase-5 DARK flag set; child-without-master is a startup failure |
| A | `data-plane/internal/iamv2/phase5_config_test.go` | flag gating: child-without-master, default-dark, unparseable-is-an-error, master-alone-mounts-nothing |
| A | `data-plane/internal/poststay/convert.go` | zero-price conversion; a priced revision is refused, not granted free |
| A | `data-plane/internal/poststay/lineage.go` | trusted server-side lineage: eligible Stay and candidate profiles, both from the device |
| A | `data-plane/internal/poststay/poststay.go` | PIN lifecycle: generate (reused `codegen` policy), hash, verify, reset, revoke, throttle |
| A | `data-plane/internal/poststay/poststay_integration_test.go` | the F8 series, written as attacks |
| A | `data-plane/internal/poststay/uniformity_integration_test.go` | the uniformity matrix and the device-derivation proof |
| M | `data-plane/internal/throttle/throttle.go` | `post_stay_pin` admitted as its own method |
| M | `data-plane/internal/writerguard/writerguard.go` | `OpenPhase5` — a separate call because the two openers have separate allowlists |
| A | `data-plane/migrations/0028_*.up.sql` / `.down.sql` | post-stay throttle method (CHECK widened; rollback deletes its buckets first) |
| A | `data-plane/migrations/0029_*.up.sql` / `.down.sql` | one reveal per generation, recorded at mint; rollback restores the 0027 body verbatim |
| A | `hotel-admin/app/(app)/post-stay/page.tsx` | route shell; `canAct` decides only whether buttons are offered |
| M | `hotel-admin/components/nav.tsx` | nav entry behind `NEXT_PUBLIC_PHASE5_ADMIN` |
| A | `hotel-admin/components/phase5/post-stay-view.tsx` | operator screen; revoke styled and worded as terminal, with a typed confirmation |
| A | `hotel-admin/e2e/phase5-post-stay.spec.ts` | browser proof that reset and revoke are distinguishable before the click |
| M | `hotel-admin/lib/roles.ts` | client matrix mirrors edged |
| M | `hotel-admin/playwright.config.ts` | `NEXT_PUBLIC_PHASE5_ADMIN=1` on the TEST-only server profile |
| M | `iam_v2_scratch/phase5_0027_foundation.sh` | gate follows the 0029 reveal rule |
| M | `iam_v2_scratch/phase5_0027_lifecycle.sh` | restores and asserts the chain head after cycling 0027 alone |

---

## Milestone 3 — Cross-PMS Transfer · ACTIVE

Under way at the time of writing. Its inventory and gate results are appended below as it completes.
