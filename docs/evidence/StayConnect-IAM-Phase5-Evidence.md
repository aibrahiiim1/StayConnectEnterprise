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

## Milestone 3 — Cross-PMS Transfer · COMPLETE

### Verified gate results

| Gate | Result |
|---|---|
| `internal/transfer` (F9 series) | **11 / 11** |
| `cmd/edged` operator API (post-stay + transfer) | **11 / 11** |
| `cmd/portald` guest surface (strict decoding + uniformity) | **4 / 4** |
| `internal/poststay` | **13 / 13** |
| `internal/authctx` | **7 / 7** |
| `internal/iamv2` flag gating | **4 / 4** |
| foundation / lifecycle DB gates | **70 / 70** · **19 / 19** |
| browser: post-stay 5, transfer 4, guest panel 3 | **12 / 12** |

### What M3 established

* **A transfer is not a room move.** Two Stays on one interface satisfy "two different Stays", which is all
  the original CHECK required — so the database now demands two different **interfaces**, and the operation
  refuses a same-interface pair before anything is written.
* **A transfer is not a supersession.** The destination entitlement carries no `supersedes_entitlement_id`;
  the typed `entitlement_transfers` row is the relationship, because supersession is same-subject and the
  Phase-1A engine rejects the cross-subject form outright.
* **A transfer is never inferred.** The transfer package does not read `auth_resolutions` at all. Ambiguity
  is surfaced as a labelled review signal whose payload says, in the words the screen renders, that it is not
  evidence.
* **The destination must already exist from verified PMS state**, and no Stay is ever created — asserted by
  counting Stays across a refused transfer.
* **Fail closed.** A transfer with nowhere to land refuses *without* terminating the source: taking access
  away to say no would be worse than saying no.
* **Seamless rebind.** The same session rows are re-pointed — no logout, no re-authentication — and the
  destination grant is zero-price and non-posting.

### Concurrency and isolation

* 24 concurrent transfers of the same pair produce **exactly one** success and **one** lineage row.
* Two operators transferring in **opposite directions** between the same pair must not deadlock. The Stays
  are locked in deterministic id order, and that order is **proven load-bearing**: replacing it with
  caller-order locking makes the 16-pair concurrent test fail with a real `SQLSTATE 40P01` deadlock.

### Git-derived inventory — M2 fix-forward `dead716` → M3 (per path)

| Change | Path | Purpose |
|---|---|---|
| A | `data-plane/internal/transfer/transfer.go` | the transfer operation: preview, deterministic lock order, atomic execute, typed lineage |
| A | `data-plane/internal/transfer/transfer_integration_test.go` | the F9 series including concurrency and the opposite-direction deadlock proof |
| A | `data-plane/cmd/edged/resources_phase5_transfer.go` | operator surface: review signal, preview, execute, lineage |
| A | `data-plane/cmd/edged/phase5_transfer_api_integration_test.go` | API contract: signal-authorizes-nothing, preview-is-read-only, full-weight execute, RBAC |
| M | `data-plane/cmd/edged/main.go` | transfer surface mounted behind its OWN flag, not as a child of post-stay |
| M | `data-plane/cmd/edged/auth.go` | `stay-transfers` added to the role matrix |
| A | `hotel-admin/components/phase5/stay-transfer-view.tsx` | operator screen; confirm controls do not exist until a preview succeeds |
| A | `hotel-admin/app/(app)/stay-transfers/page.tsx` | route shell |
| A | `hotel-admin/e2e/phase5-stay-transfer.spec.ts` | browser proof of the signal/preview/confirm ordering |
| M | `hotel-admin/components/nav.tsx`, `hotel-admin/lib/roles.ts` | nav entry and client matrix mirroring edged |

### Limitations added by M3

| # | Limitation | Status |
|---|---|---|
| L5-5 | A transfer is **not reversible by an inverse transfer**: the source entitlement is terminated, and transferring back would need its own new grant. The lineage records what happened; it does not undo it. | By design. `from_entitlement_id` is UNIQUE, so a return journey is a new transfer between the same Stays in the other direction, subject to every rule again. |
| L5-6 | The destination grant is a **bounded window** (4 hours), not an open-ended entitlement. The destination property's own authentication then applies. | By design: a transfer keeps a guest online across the change; it does not silently grant them a stay's worth of access on a property that never authenticated them. |

---

## Milestone 4 — Hardening + final LIVE-DARK candidate · COMPLETE

### M4 fix-forward: the contractual F9-i, and the two defects it found

The test carrying the F9-i label proved that two TRANSFERS cannot deadlock. The Plan defines F9-i as a
transfer racing **checkout/grace**. The deadlock test is kept; the contractual one was added, driving the real
`ConvertAtCheckout` against a real transfer on the same Stay.

Writing it showed the first version was **not racing anything**: `seedEvent` leaves the boundary event
unapplied, checkout correctly refuses that, and the transfer "won" twelve times against nothing. With a real
applied event the race failed immediately and exposed two defects:

| # | Defect | Fix |
|---|---|---|
| D5-1 | The transfer left the source's device **authorization intervals open** against a TERMINATED entitlement. The interval model is append-only, not a flag: every later question that reads it — which entitlement was this device under at time T, what does the checkout boundary see, which accounting samples attribute where — gets a wrong answer. | The source's intervals are closed in the same transaction that opens the destination's, as checkout does at its boundary. **Proven load-bearing:** removing the closure makes the race fail with 2 attachments left on the source. |
| D5-2 | A transfer accepted a source Stay that had **already checked out** — moving checkout *grace*, a courtesy the departed property granted, to a property that never agreed to it. | The source must be `IN_HOUSE`, re-checked under the lock. |

**A corrected assertion.** My first version demanded the source hold no live entitlement after a transfer.
That was wrong: when checkout commits afterwards it legitimately creates grace, because the Stay *did* hold an
active entitlement at the boundary. Measurement showed the grace is an **empty shell** — zero devices, zero
intervals, zero sessions — so the assertion is now that nothing is *attached* to it, which is the property
that actually matters.

The checkout-wins branch was never reached by the unbiased race (the transfer takes both Stay locks in one
statement and finishes first, even with a head start). Rather than tune a sleep until the scheduler
cooperated, that outcome is exercised explicitly by letting checkout commit first.

### Other M4 findings

* **The gates were not self-sufficient.** Run the way CI runs them — on a freshly built chain — the foundation
  gate failed 47 of 71 assertions and the least-privilege gate one, because both silently depended on fixture
  state someone else had created. Both are now self-seeding or fixture-free.
* **Both gates leaked their probe role**, because `DROP ROLE` fails *silently* while grants exist. Each then
  reported the other's leftover as a privilege defect, making the DARK claim measurably false for whichever
  ran second.
* **Two Phase-3 test-scoping fixes** (assertions counted across the whole database rather than their own
  site), so the suite is repeatable against a reused database.
* **A regression I introduced and CI caught:** adding the post-stay tab *before* the PMS one made it
  `enabled[0]`, so the portal's default panel became a PIN field and the room form a first-time guest needs
  was hidden. Fourteen Phase-3 portal tests timed out on an invisible field.
* **The darkness grant check was wrong on the real target.** It excluded `current_user`, which equals the
  table owner only on a scratch database; on the appliance, where Gate P gives `iam_v2` its own owner, the
  owner's implicit rights appeared as 21 "non-owner" grants and condemned a correct deployment. It now
  excludes the table's actual owner, resolved from the catalog.

### Authoritative CI

| Workflow | Run | Head | Result |
|---|---|---|---|
| Phase 5 Post-Stay and Transfer CI | 31824484043 | `ffdeef5` | **success** |
| Phase 4 Financial Core CI | 31824484134 | `ffdeef5` | **success** |
| Phase 3 Software CI | 31825068831 | `ffdeef5` | dispatched on the branch |

A Phase-5 workflow did not exist before M4. It builds its own disposable PostgreSQL, proves the dark posture,
applies 0027–0029 on the authoritative chain, runs the three database gates and the integration matrix, and
re-runs the Phase-4 financial core.

### Controlled LIVE-DARK deployment — development appliance only

Host `radius` (172.21.60.23), database `stayconnect_site`. **Production was never contacted.**

| Area | Evidence |
|---|---|
| Backup before any change | `/opt/stayconnect/backups/phase5-iam-20260814T174052Z/site.dump`, 6,260,737 bytes, sha256 `cf1bda432c82c7007976d3f51972c225aea862be370264764a3a80163255c07a` |
| Migration integrity | 0027, 0028, 0029 each applied once and recorded exactly once |
| Schema effect | iam_v2 base tables 68 → **68** (Phase 5 creates no tables); public tables 44 → **44**; structural fingerprint `71dde7dc871b935ae555bcab2e5c1252` → `07e08329beebef509e811a147524cdc5` |
| Least privilege | no role besides the schema owner holds any privilege on a Phase-5 table |
| Darkness | every Phase-5 table 0 rows; 13 Phase-5 objects present; no Phase-5 flag in any env file or unit; all three scd Phase-5 routes **404** (absent, not present-and-refusing) |
| Service health | `scd`, `edged`, `netd`, `acctd` all active |
| Reboot persistence | rebooted 17:41:53 UTC; migrations still recorded, services active, darkness re-verified |
| Real restore | backup sha256 verified, restored into `phase5_restore_drill`: 68 base tables, **0** Phase-5 migrations — the pre-deployment state, which is the rollback path proven |
| Rollback rehearsal | 0029→0028→0027 down on the live database: fingerprint returned to exactly `71dde7dc871b935ae555bcab2e5c1252`, 0 Phase-5 objects, 0 ledger rows, Phase-3 guard restored **including its refusal message**; re-applied to `07e08329beebef509e811a147524cdc5` |
| Final darkness | re-verified after every drill |

### Limitations added by M4

| # | Limitation | Status |
|---|---|---|
| L5-7 | Several Phase-3/Phase-4 integration tests are **not repeatable against a reused database** (they count across the whole database rather than their own site). Two were fixed; the rest are unchanged. | Recorded. CI builds a fresh database per run, which is the documented contract; re-engineering those suites is outside D21. |
| L5-8 | `cmd/scd` integration tests require migrations 0001–0006, which the Phase-5 local harness does not build. They pass in CI, which builds the full chain. | Recorded. The local harness is scoped to the Phase-5 surface deliberately. |

---

## Acceptance-evidence fix-forward · transition T0052

Recorded **2026-08-14**, from candidate head `aef848d253e2c6efebe4f036b0369a22530a5a25`, under the same
authorization **D21**. **No product behaviour was added, removed or redesigned**; the Phase-5 implementation
and the contractual F9-i fix-forward are the accepted software basis. Everything above this line is preserved
as written — the stale statements below are corrected *forward*, here, not edited in place.

### E5-1 · The Phase-5 CI gate was green and published nothing

The exact-head Phase-5 workflow reported **success for all four milestones while producing no artifact at
all**. Three independent causes, each sufficient on its own:

* the evidence staging directory is dot-prefixed, and `actions/upload-artifact@v4` silently skips hidden files;
* `if-no-files-found: warn` demoted *the evidence does not exist* to a log line;
* and nothing assembled or checked the evidence before uploading it.

A green run with no artifact is indistinguishable from a green run whose evidence was never produced, so all
three are closed. `scripts/ci/phase5_evidence.py` assembles the artifact from files the gate's own runners
wrote while gating, and **refuses to publish a partial one** — a missing env record, a missing required step
outcome, or a missing/typeless machine count each exit non-zero. The upload sets `include-hidden-files`,
`if-no-files-found: error` and 90-day retention, and is named for the head under test. A final step reads the
upload action's own `artifact-id` and `artifact-digest` back and fails the job if either is absent, so *there
is a downloadable artifact with this ID and this digest* is measured rather than assumed.

A fail-closed component that has only ever been watched succeeding has not been shown to fail closed, so
`scripts/ci/phase5-evidence-selftest.sh` drives the **real** assembler — never a copy — against one complete
staging directory and six deliberately damaged ones, and fails if it accepts any of them. The complete case is
the control: without it, six refusals would also be what a publisher that refuses everything looks like. It
runs **first** in the workflow, before any gate can make the staging directory look complete. 9/9.

The workflow also gained a counted unit-matrix step, so the artifact carries a machine test total produced by
the same run that gated rather than a second, weaker one. The count fixture in the self-test is generated by
the real producer (`gojson_summary.py`) rather than hand-written, because a hand-written fixture is a second
independent statement of the file's shape and the two drift silently — which is how a self-test ends up
validating a shape CI never writes. That is not hypothetical: the first version of this assembler required a
`tests` key that `gojson_summary.py` has never written, and only the generated fixture exposed it.

### E5-2 · The LIVE-DARK health check never covered pmsd

The deployment's service-health loop covered `scd`, `edged`, `netd` and `acctd` and said nothing about
**pmsd**, the dedicated PMS connector — so its posture was unmeasured on the very appliance whose darkness the
deployment claims.

pmsd is the one StayConnect daemon that is **healthy when dead**. With no Phase-3 flag set,
`LoadPMSConfigFromEnv` resolves every flag OFF and `pmsd.Run` returns immediately: no assignment, no database
pool, no decrypted secret, no worker, no PMS socket. It says so and exits 0, and `Restart=on-failure` (not
`always`) means that clean exit does not restart. Adding it to the `is-active` loop would therefore have had
exactly two outcomes: **fail a correct appliance, or make it green by starting a live PMS connector** — real
PMS traffic that no Phase-5 authorization covers. The criterion is the DARK contract itself, measured in six
arms, and it deliberately sits outside that loop.

| Arm | What it refuses to assume | Measured on `radius` |
|---|---|---|
| Installed and enabled | that "dark" might mean a missing or masked unit, which is indistinguishable from an appliance that lost its connector | `LoadState=loaded`, `UnitFileState=enabled` |
| Clean disabled exit | that "not running" is one state — a crash is also not running | `inactive (dead)`, `Result=success`, `ExecMainStatus=0` |
| No restart storm | that `Restart=on-failure` is honoured, rather than checking it | `NRestarts=0`, starts-this-boot `1`, storm indicators `0` |
| The daemon's own statement | that a gone process took the flags-OFF path, rather than never reaching it | the dark line and the clean stop both present in this boot's journal |
| No PMS socket or traffic | that the flags imply the footprint | `0` processes, `0` sockets |
| No database activity | that "builds no pool" is true because the code says so | `0` attributable backends |

`PHASE5_PMSD_UNIT` exists so the criterion can be shown to **fail**, and defaults to the real unit. Pointed at
`stayconnect-scd` it aborts on the active-state arm; pointed at `stayconnect-ctrlapi` it aborts on the enabled
arm. **The other four arms were demonstrated at the predicate level, not end-to-end**, because no
enabled-and-inactive substitute unit exists on this appliance: the same expressions were run against real data
that violates them — a unit with no pmsd start line, a unit with no dark statement, a running service user
with 209 processes and 7 sockets, and the same database query shape with a pattern matching 12 live backends.
Each returned the value that aborts.

### E5-3 · The recorded authoritative CI pointed at an intermediate head

`phase5_authoritative_ci` and `T0051.authoritative_ci` named runs against `ffdeef5` — a mid-M4 head — and
described the Phase-3 run only as *dispatched*. The final acceptance candidate `aef848d` had four green
exact-head runs that no governance surface named. Corrected forward:

| Workflow | Run | Head | Result |
|---|---|---|---|
| Phase 5 Post-Stay and Transfer CI | 31831004671 | `aef848d` | **success** — and published no artifact, which is E5-1 |
| Phase 4 Financial Core CI | 31831004746 | `aef848d` | **success** |
| Phase 3 Software CI | 31831007345 | `aef848d` | **success** |
| Project Governance | 31831007337 | `aef848d` | **success** |

The CI runs for the delivery head introduced by *this* section cannot be named inside it: the delivery-only
commit sits one commit above the head the packs and manifest were generated at, and a commit cannot contain
the run identifiers of workflows that only start once it exists. They are recorded in the PR #13 body and in
the final report — the same self-reference protocol already applied to the delivery head's own SHA.

### E5-4 · The inventory was 87 paths, and one filename was wrong

GitHub reports **88** changed physical paths at `aef848d`. The 87 came from grouping paths with brace
shorthand in the report, and the added evidence-pack file was named `GIT_STAT_2c2fc6a.txt` when the file
actually added is `GIT_STAT_34f09e9.txt` — the pack is stamped with the **source commit it was built from**,
which is the implementation commit, never the delivery commit above it. The inventory is regenerated from Git
for the new delivery head and reported with every physical path listed separately.

### What this fix-forward did not change

Phase 5 remains **IN_PROGRESS** and unaccepted; PR #13 remains **open and unmerged**; no flag was enabled
anywhere; no IAM-v2 cutover was performed; Production was not contacted, migrated or read; no PMS, provider,
financial or paid-access traffic occurred; no Phase-6 or Phase-7 work was started; and T0044 through T0051 are
unchanged.

---

## Product-Owner acceptance and closure · decision D22 / transition T0053

Recorded **2026-08-15**. Phase 5 is **ACCEPTED AND CLOSED at VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC
maturity**, against accepted software/runtime candidate `aef848d253e2c6efebe4f036b0369a22530a5a25` and final
verified delivery/evidence head `4142f5fe857787a745b186d8ac38edaad7b4d268`. Every milestone section above is
preserved exactly as written; nothing in it is rewritten by this closure.

The accepted basis is the one already established and is deliberately kept in three separable parts —
software-CI evidence, live development-appliance evidence, and the Product-Owner judgement that the two
together are sufficient. Collapsing them is how a green pipeline starts being read as a business decision.
The authoritative record is
[`docs/acceptance/StayConnect-IAM-Phase5-Live-Dark-Acceptance.md`](../acceptance/StayConnect-IAM-Phase5-Live-Dark-Acceptance.md);
the final report is
[`docs/reports/StayConnect-IAM-Phase5-Final-Report.md`](../reports/StayConnect-IAM-Phase5-Final-Report.md).

**All nine recorded limitations — L5-1 through L5-9 — are preserved exactly as limitations. None is promoted
to PASS.** L5-9, the four pmsd DARK-contract arms proven at the predicate level rather than end-to-end, is
carried into the closure unchanged rather than being quietly upgraded by the acceptance that followed it.

**The closure round changes no product-runtime file, and that is measured rather than asserted.**
`git diff --name-only` between the accepted delivery/evidence head and the closure delivery head returns no
path under `data-plane/`, `hotel-admin/`, `scripts/` or `.github/workflows/`. Only `governance/`, `docs/`,
`exports/` and one governance validator under `tools/` change — `validate-current-state-parity.py`, which
gains the Phase-5 plan in its phase-plan map so the accepted-phase-semantics rule actually reads that
surface, named here rather than hidden inside a broader claim. An acceptance round is exactly the moment at which a quiet code change is least likely to
be noticed, because attention is on the paperwork, so the proof comes from Git.

**Pull request #13 remains OPEN and UNMERGED.** Acceptance and merge are separate Product-Owner decisions, as
they were for Phases 2, 3 and 4. This closure authorizes no merge, no deployment or further appliance
mutation, no Phase-5 or Phase-4 feature-flag enablement, no IAM-v2 cutover, no Production migration or
contact, no PMS/provider or financial traffic, no paid access, and no Phase-6 or Phase-7 work.

---
