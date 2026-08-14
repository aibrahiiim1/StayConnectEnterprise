# StayConnect IAM — Phase 5 Final Report (Post-Stay PIN re-authentication + Cross-PMS Transfer)

> **Status: ACCEPTED AND CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC.** Product-Owner decision
> **D22**, closure transition **T0053**, 2026-08-14. Accepted software/runtime candidate
> **`aef848d253e2c6efebe4f036b0369a22530a5a25`**; final verified delivery/evidence head
> **`4142f5fe857787a745b186d8ac38edaad7b4d268`**; acceptance-evidence fix-forward **T0052**. Run and
> artifact identifiers are in §3.
>
> **LIVE-DARK and NOT ENABLED.** Every Phase-5 flag is OFF and **absent** from every env file and unit, every
> Phase-5 scd route returns **404** on the running appliance, every Phase-5 table holds **zero rows**, legacy
> public-schema IAM remains the sole authentication authority, and no post-stay PIN, transfer, PMS message,
> provider call, folio debit or paid access has ever occurred. Acceptance is at LIVE-DARK maturity **only**.
>
> **Nine limitations are accepted and NOT promoted to PASS** — see §6.
>
> **The Phase-5 pull request has been MERGED** to master on 2026-08-14 UTC under the separate
> Product-Owner merge decision **D23** (transition **T0054**), merge commit `4f27b4d0ea4de57f9bbf6a062d9bb9d294ec6e6a`.
> The merge introduced no content and deployed nothing.

---

## 1. شرح مبسّط بالعامية المصرية

الفيز دي بتحلّ حاجتين. الأولى: الضيف بعد ما يعمل تشيك-أوت ويسيب الفندق، يقدر يرجع يدخل على النت بكود **PIN**
مؤقّت. الحاجة المهمة جداً هنا إن الكود ده مربوط بـ**إقامة الضيف نفسها**، مش بالأوضة — يعني الضيف اللي هينزل
الأوضة بعده **عمره ما يقدر** يستعمله، والكود بيموت مع الإقامة لوحده في الداتابيز من غير ما حد يفتكر يلغيه.
ومفيش أي مكان في النظام بتدوّر بيه برقم الأوضة أصلاً، فمفيش حد يقدر يجرّب أرقام أوض لحد ما يوصل لحاجة.

التانية: لما ضيف ينتقل من فندق لفندق تاني (نظامين PMS مختلفين)، النقل ده بقى **عملية مُعرَّفة وواضحة** لازم
موظف يأكّدها — مش تخمين. قبل كده كان أي تنقّل بين أوضتين ينفع يتسجّل كإنه "نقل بين فنادق"، دلوقتي لازم يكونوا
**نظامين مختلفين فعلاً**، ولازم إقامة الوجهة تكون **موجودة أصلاً** جاية من الـPMS — النظام **بيرفض** إنه يخترع
إقامة وهمية.

اتعمل **تنزيل حقيقي على جهاز التطوير**: الجداول والقواعد اتظبطت، الجهاز اتعمله ريستارت، الباك-أب اترجّع فعلاً
على داتابيز تانية، والـrollback اتجرّب على الداتابيز الحيّة ورجّعنا تاني — وكل ده والمفاتيح **مقفولة**. مفيش
ولا قرش اتحرك، ومفيش أي حاجة اتبعتت لأي نظام فندق. الـProduct Owner قبل الفيز على المستوى ده بس: الكود موجود
ومتجرّب حي وهو **مقفول**.

---

## 2. Phase and authorized scope

- **Phase:** 5 — Post-Stay PIN re-authentication + the Cross-PMS Transfer workflow. **ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity.**
- **Authorized:** Product-Owner decision **D21**, transition **T0050** (2026-08-14), as four milestones with automatic continuation while gates were green.
- **Candidate complete:** transition **T0051** (2026-08-14). **Acceptance-evidence fix-forward:** transition **T0052** (2026-08-14).
- **Accepted and closed:** decision **D22**, closure transition **T0053** (2026-08-14).
- **Branch:** `phase/5-poststay-transfer`; **PR #13 — MERGED to master on 2026-08-14 UTC under the separate Product-Owner merge decision D23** (transition **T0054**), merge commit `4f27b4d0ea4de57f9bbf6a062d9bb9d294ec6e6a`. The merge introduced no content: master's tree is byte-identical to the accepted head's tree.
- **Appliance:** `radius` / `172.21.60.23` — the **development** appliance. Production was never migrated or contacted.

---

## 3. The accepted basis, kept in three separable parts

Collapsing these is how a green pipeline starts being read as a business decision. They are recorded apart.

### 3a. Software-CI evidence — *does the delivered code pass its own gate?*

| | |
|---|---|
| Accepted software/runtime candidate | `aef848d253e2c6efebe4f036b0369a22530a5a25` |
| Final verified delivery/evidence head | `4142f5fe857787a745b186d8ac38edaad7b4d268` |
| Provenance (`inventory_head`) | `8877c47a6f1df25fab075f1dfda6f01e32846a75` |
| Phase 5 Post-Stay and Transfer CI | **31836130617 — SUCCESS** · artifact **9232578379** · `sha256:ed9da35202574f6c93713753153c1cc53ff7084e9e5765a69c34adf232d7383b` |
| Phase 4 Financial Core CI | **31836130580 — SUCCESS** · artifact **9232732340** · `sha256:6d0e343f16f51f51e47214c6049580abf6e1379a9879e825fbd5d46358163404` |
| Phase 3 Software CI | **31836130394 — SUCCESS** · artifact **9232717163** · `sha256:2bde46c2c54461d3a7d2f937cec76f8920f3f3cdca963035d926fa001cf7be7c` |
| Project Governance | **31836130544 — SUCCESS**, re-confirmed as **31837196633** |

Covered by the Phase-5 gate: gofmt, build, vet on both tag sets, the counted unit matrix, the DARK guard,
migrations **0027–0029** on the authoritative chain, the foundation + security gate, the migration-lifecycle
gate judged on the **structural** fingerprint, the **derived** least-privilege proof, the
`integration && phase5` matrix — F8 written as attacks, F9 including 24-way concurrency and the
opposite-direction deadlock proof, and the contractual **F9-i** race against the real Phase-3
checkout/grace conversion — and a full re-run of the Phase-4 financial core.

The Phase-5 digest is not a number copied from a web page: the artifact was downloaded and hashed locally,
and the computed SHA-256 is byte-identical to the digest GitHub reports.

### 3b. Live evidence — *did that code behave on a real appliance?*

Backup taken and verified **before any change** (`6,260,737` bytes, sha256 `cf1bda43…`). Migrations
**0027–0029** each applied once and recorded exactly once. iam_v2 base tables **68 → 68** — Phase 5 creates
no tables — public tables **44 → 44**, structural fingerprint `71dde7dc…` → `07e08329…`. Every Phase-5 table
**0 rows**; 13 Phase-5 objects; **no role besides the schema owner** holds any privilege on them; **no
Phase-5 flag** in any env file or unit; all three scd Phase-5 routes **404** — absent, not
present-and-refusing. `scd`, `edged`, `netd`, `acctd` active; **pmsd** verified against its DARK contract in
six arms (§4). Authorized **reboot** survived with every criterion persisted. Backup **really restored** into
`phase5_restore_drill`. Rollback **rehearsed on the live database** and reversed, returning the fingerprint to
exactly its pre-deployment value with the Phase-3 guard restored **including its refusal message**.

Full detail: `docs/evidence/StayConnect-IAM-Phase5-Evidence.md`.

### 3c. Product-Owner decision — *is that enough?*

**D22.** Not derivable from 3a or 3b, and not taken by the implementer.

---

## 4. What was delivered (all DARK, flags OFF)

- **Schema** — additive migrations `0027`–`0029`: post-stay identity bound to a Stay **episode** with an
  argon2id PIN, generation and lifecycle version, issuance provenance and revocation; the Phase-5
  controlled-writer boundary; `p5_post_stay_authenticable`; the `CHECKED_OUT → POST_STAY_ACTIVE` arm added to
  the Phase-3 lifecycle guard; the transfer guard requiring **two different interfaces**, entitlement–Stay
  ownership, state coupling, no supersession and a cycle-free walk; the `stay_link` guard that refuses
  `POST_STAY`; a post-stay throttle method; and the restatement of one-time reveal as happening **at mint**.
- **Runtime** — the post-stay PIN lifecycle with a constant-work verification path and no early exit; lineage
  resolution that derives the eligible Stay and candidate profiles from the device's own authorization
  history; zero-price conversion that refuses a priced revision rather than granting it free; the typed
  cross-PMS transfer with deterministic id-order locking; and the `POST_STAY_PIN` re-verification arm in the
  auth-context consumer.
- **Guest surface** — no `stay`, `room`, `pms_interface` or `profile` parameter exists at all. It is
  **absent, not validated**, on both the internal and the public decoder, and an identity-looking unknown
  field is refused with the ordinary uniform non-success rather than silently ignored.
- **Operator surface** — post-stay reset/reissue/revoke and the transfer preview/execute screens, behind
  role authorization and a step-up, audited, and absent from the delivered bundle while flags are OFF.
- **Gates and CI** — three database gates (foundation 72/72, lifecycle 19/19, derived least privilege 23/23),
  a new authoritative Phase-5 workflow, and a fail-closed evidence publisher with its own self-test.

### The pmsd DARK contract

pmsd is the one StayConnect daemon that is **healthy when dead**: with no Phase-3 flag set it loads no
assignment, builds no database pool, decrypts no secret, starts no worker and opens no PMS socket — it says
so and exits 0, and `Restart=on-failure` means that clean exit does not restart. Putting it in an
`is-active` loop would have had exactly two outcomes: fail a correct appliance, or be made green by starting
a **live PMS connector**, which is real PMS traffic that nothing in Phase 5 authorizes. The criterion is
therefore the contract itself, measured in six arms — installed and enabled; `inactive (dead)` with
`Result=success` and exit `0`; `NRestarts=0`, one start this boot, zero storm indicators; the daemon's own
dark statement and clean stop in the journal; zero processes and sockets; zero attributable database
backends — all clean on the development appliance.

---

## 5. Defects found and fixed during the phase

| # | Found by | Fix |
|---|---|---|
| D5-1 | the contractual F9-i race | the transfer left the source's device **authorization intervals open** against a terminated entitlement — closed in the same transaction that opens the destination's; proven load-bearing by mutation |
| D5-2 | the same race | a transfer accepted a source Stay that had **already checked out**, moving checkout *grace* to a property that never granted it — the source must now be `IN_HOUSE`, re-checked under the lock |
| D5-3 | Phase-3 portal CI | the post-stay tab displaced the room form as the portal's default panel; fourteen tests timed out on an invisible field |
| D5-4 | the appliance itself | the darkness grant check excluded `current_user` rather than the table's **owner**, so on the Gate-P-separated appliance the owner's own rights read as 21 "non-owner" grants and condemned a correct deployment |
| E5-1 | the T0052 evidence review | the Phase-5 CI gate was green for four milestones and **published no artifact at all** — dot-prefixed staging, `if-no-files-found: warn`, and nothing assembling or checking the evidence. Publication is now fail-closed and self-tested |
| E5-2 | the same review | the LIVE-DARK health check never covered **pmsd**; the DARK-contract criterion above closes it |

Two of these are worth stating plainly because they change how the evidence should be read. The test
originally carrying the **F9-i** label proved that two *transfers* cannot deadlock, which is not what the plan
defines F9-i to be; it was kept as additional concurrency evidence and the real race was added. Writing that
race then showed it **was not racing anything** — the boundary event was left unapplied, checkout correctly
refused it, and the transfer "won" twelve times against nothing. Made real, it failed immediately and
exposed D5-1 and D5-2.

---

## 6. Accepted limitations — preserved, NOT promoted to PASS

1. **`POST_STAY_ACTIVE` has no exit transition.** The FINAL contract draws no arrow out of it; a reinstatement
   for a converted Stay is refused by the guard and lands in the operator queue. (**L5-1**)
2. **Post-Stay v1 is zero-price only.** A priced or settlement-requiring revision is refused rather than
   granted free. (**L5-2**)
3. **Post-stay requires a device authorized during the stay.** A wholly new device receives the uniform
   non-success — the trade that removes the client-supplied-subject parameter altogether. (**L5-3**)
4. **`stay_links(reason='POST_STAY')` is never written**; both ends are `NOT NULL` Stays and post-stay has
   one. Enforced by the database guard. (**L5-4**)
5. **A transfer is not reversible by an inverse transfer.** Reversal is an operator/PMS-level correction.
   (**L5-5**)
6. **The destination grant is a bounded window**, not an equivalence between source and destination access.
   (**L5-6**)
7. **Several Phase-3/Phase-4 integration tests are not repeatable against a reused database.** Two were
   fixed; the rest are unchanged. (**L5-7**)
8. **`cmd/scd` integration tests need migrations 0001–0006** the local harness does not build; they run in
   CI, which builds the full chain. (**L5-8**)
9. **Four pmsd DARK-contract arms were proven at the predicate level, not end-to-end**, because no
   enabled-and-inactive substitute unit exists on the development appliance. (**L5-9**)

None of the nine is promoted.

---

## 7. Explicitly NOT authorized by this acceptance

No deployment or further appliance mutation · no Phase-5 or Phase-4 feature-flag
enablement · no IAM-v2 authentication cutover · no Production migration or Production database contact · no
PMS, provider or real financial traffic · no paid guest access · no Phase 6 or Phase 7 work.

Phases 0, 1A, 1B, 2, 3 and 4 are preserved unchanged and the six accepted Phase-4 limitations are not
promoted.

---

## 8. Governance trail

`D21`/`T0050` authorized Phase 5. `T0051` records the completed acceptance candidate at verified LIVE-DARK.
`T0052` records the acceptance-evidence fix-forward — the fail-closed CI evidence publication, the pmsd
DARK-contract criterion, the head/CI reconciliation and the corrected inventory — and corrects the earlier
statements **forward** rather than rewriting them. `D22`/`T0053` accept and close Phase 5, and `D23`/`T0054` record the merge to master as a separate delivery decision. **No historical
receipt was rewritten at any point**, and the closure round changes no product-runtime file — no path under
`data-plane/`, `hotel-admin/`, `scripts/` or `.github/workflows/` — which is proven from Git rather than
asserted. The only changes outside `governance/`, `docs/` and `exports/` are two governance validators,
`tools/validate-current-state-parity.py` and `tools/validate-project-state.sh`, both named here rather
than hidden inside a broader claim.
