# StayConnect IAM — Phase 4 Final Report (Financial Execution Layer)

> **Status: ACCEPTED AND CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC.** Product-Owner decision
> **D19**, closure transition **T0044**, 2026-08-13. Accepted software candidate
> **`b94112d8cb0ab63938b60f829ddd465c14491f97`**; accepted live evidence **T0043**; accepted pre-acceptance
> delivery head **`105af49b9a0b44e6eda131b08f7d5fa6a37a2bbc`**. Run and artifact identifiers are in §3 and
> in `docs/evidence/Phase4-Final-Live-Acceptance-Record.md`.
>
> **LIVE-DARK and NOT ENABLED.** Every Phase-4 feature flag is OFF, every Phase-4 route returns 404 on the
> running appliance, legacy public-schema IAM remains the sole authentication authority, `iam_v2` holds 68
> tables and **zero rows**, and no PS, PA, folio debit, provider CHARGE/REFUND, paid access or reversal has
> ever occurred. Acceptance is at LIVE-DARK maturity **only**.
>
> **Six limitations are accepted and NOT promoted to PASS** — see §6. Two of them constrain any future work:
> C35 has no external archival receipt authority, and the financial restore management marker is a
> **pre-financial-enable prerequisite** that is not yet installed.
>
> **The Phase-4 pull request is OPEN and UNMERGED.** Merging requires a separate explicit Product-Owner
> decision.

---

## 1. شرح مبسّط بالعامية المصرية

الفيز دي بتبني الطبقة المالية: إزاي النظام يحسب فلوس، يبعت الشحنة لنظام الفندق (PMS)، ويتعامل مع الدفع
والصلاحيات — من غير ما يشتغل على فلوس حقيقية خالص. أهم فكرة إن كل حاجة **بتفشل مقفولة**: لو حصل أي شك،
النظام بيوقف ومبيبعتش، لأن أسوأ حاجة ممكن تحصل إنه يشحن الضيف مرتين. كمان لو الداتابيز اترجعت من نسخة قديمة،
النظام بيعرف كده وبيقفل حركة الفلوس لحد ما موظف يراجع ويقرر بإيده، والقرار ده متسجّل باسمه.

اتعمل **تنزيل حقيقي على جهاز التطوير** (WS-L): الجداول اتعملت، الصلاحيات اتظبطت، الجهاز اتعمله ريستارت،
والـrollback اتجرّب ورجّعنا تاني — وكل ده والمفاتيح **مقفولة**. مفيش ولا قرش اتحرك، ومفيش أي حاجة اتبعتت لأي
نظام فندق أو بنك. الـProduct Owner قبل الفيز على المستوى ده بس: الكود موجود ومتجرّب حي وهو **مقفول**.

---

## 2. Phase and authorized scope

- **Phase:** 4 — Financial Execution Layer. **ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity.**
- **Authorized:** Product-Owner decision **D18**, transition **T0029** (2026-08-11).
- **Live WS-L deployment:** transition **T0043** (2026-08-13).
- **Accepted and closed:** decision **D19**, closure transition **T0044** (2026-08-13).
- **Branch:** `phase/4-financial-execution`; **PR #12 — OPEN and UNMERGED**.
- **Appliance:** `radius` / `172.21.60.23` — the **development** appliance. Production was never migrated or contacted.

---

## 3. The accepted basis, kept in three separable parts

Collapsing these is how a green pipeline starts being read as a business decision. They are recorded apart.

### 3a. Software-CI evidence — *does the delivered code pass its own gate?*

| | |
|---|---|
| Accepted software candidate | `b94112d8cb0ab63938b60f829ddd465c14491f97` |
| Phase 4 Financial Core CI | **31690016483 — SUCCESS**, 36/36 |
| Evidence artifact / digest | **9177140558** · `sha256:aacb6e9f687f872632b36a1dfe5c4598340d2108d1e70f31ee6c48cae93ba52d` |
| Pre-acceptance delivery head | `105af49b9a0b44e6eda131b08f7d5fa6a37a2bbc` |
| Delivery-head CI | **31691250489 — SUCCESS**, 36/36 |
| Evidence artifact / digest | **9177645571** · `sha256:1b03e9863d5a8b7ec79a01b89574ad82e8fc9822d06c5561bc29366f6d098200` |

Covered: gofmt, build, vet, unit matrix, race detector, the pre-0011 invariant suite on both chains,
**migrations 0011–0026** with the 269-assertion DB gate, the PG16 integration matrix, least privilege,
payment concurrency with real concurrent transactions, the supported restore drill, Hotel-Admin typecheck /
unit / flags-OFF build / full browser suite, the dependency gate and its self-test, the current-state parity
check and its self-test, the DARK static assertion, and a self-test that breaks a step deliberately and
fails if the gate still reports success.

### 3b. T0043 live evidence — *did that code behave on a real appliance?*

Migrations **0011–0026** applied through the controlled path, each recorded **exactly once**; `iam_v2` 62 →
**68** base tables, **0 rows before and after**, all `iam_v2_owner`. `public` column fingerprint
`a8fec747…` **identical** before and after. Five **NOLOGIN** roles, **no Phase-4 login role and no Phase-4
DSN**. Binaries and Hotel-Admin bundle deployed. Authorized reboot survived with every criterion persisted.
Backup taken before any mutation and **really restored** into a scratch database. Rollback **rehearsed and
reversed**. Throughout: flags **absent**, Phase-4 routes **404**, **zero financial egress**.

Full detail: `docs/evidence/Phase4-Final-Live-Acceptance-Record.md`.

### 3c. Product-Owner decision — *is that enough?*

**D19.** Not derivable from 3a or 3b, and not taken by the implementer.

---

## 4. What was delivered (all DARK, flags OFF)

- **Schema** — additive migrations `0011`–`0026`: the posting execution core with per-interface `P#`
  allocation and a structural no-blind-retry gate; currency pinned on the immutable interface revision;
  the derived `posting_execution_state`; the reversal ledger; payment/settlement state machines with an
  append-only deduplicated provider-callback ledger; least privilege; financial identity and provenance;
  `FINANCIAL_RECOVERY_MODE` with a **structural** hold; observability; the restricted-runtime trust
  boundary; the restore-generation model; **one** entitlement grant kernel with the provider-outcome
  authority split from the execution authority; the zero-attempt audited retry; and C35 failing closed on
  an external verified receipt.
- **Runtime** — the Go payment domain whose only exported constructor takes no `Config` and no `Transport`,
  so no provider adapter can be injected; the posting engine; the entitlement grant path shared with
  Phase-2 free grants; recovery; observability.
- **Operator surface** — financial health, Manual Review, settlements, and recovery including the
  zero-attempt path, all behind the financial permission and a password step-up, and all absent from the
  delivered bundle while flags are OFF.
- **Governance tooling** — the current-state parity validator and its self-test, the Product-Owner-owned
  dependency acceptance model and its self-test, and the structural schema fingerprint.

---

## 5. C1–C38 status

The layered matrix lives in `docs/architecture/Phase4-Financial-Schema-Gap-Audit.md` §7 and is not duplicated
here. At closure: every database row is present and behaviourally verified; the runtime rows are implemented
and verified DARK **and re-measured live** for the security-relevant ones; **C35** is implemented and
**fails closed** with its external half absent; **C37** is DARK with flags OFF and no egress; **C38** remains
`BLOCKED_BY_PRODUCT_OWNER_DECISION`.

---

## 6. Accepted limitations — preserved, NOT promoted to PASS

1. **C35 external archival receipt authority does not exist.** The implemented archive, digest and purge gate
   are accepted **because they fail closed**. Cross-customer purge remains **unavailable** until a real
   external receipt authority exists.
2. **C38 real-financial acceptance remains outside Phase 4** and requires a separate future Product-Owner
   authorization.
3. **The financial restore management marker is not installed** on the development appliance. **Pre-financial-enable
   prerequisite:** it must be initialized/installed and verified through the supported process **before any
   future real financial traffic is authorized**.
4. **Legacy live-session continuity remains NOT PROVEN** from Phase 3, and must **not** be inferred as PASS
   from WS-L row counts.
5. **No real payment-provider adapter or real-provider behaviour is accepted** by this DARK closure.
6. **Per-property Tier-2 financial onboarding remains mandatory** before any property can be financially
   enabled; Hotel ID 2 remains **UNAPPROVED**.

### 6a. One wording correction, made forward

T0043 and the WS-L report said **"no role at all"** can execute `iam_v2.p4_record_compliance_receipt`. The
accurate statement is that **no deployed runtime, service or PUBLIC role is authorized** to call it and it is
**unreachable from the current runtime** — 0 of the ten roles measured hold EXECUTE. The controlled function
exists **deliberately** for a future real external archival authority. Corrected forward under D19; T0043 is
**not** rewritten.

---

## 7. Explicitly NOT authorized by this acceptance

No further appliance mutation · no Production migration or Production database contact · no Phase-4 flag
enablement · no IAM-v2 authentication cutover · no real PMS PS or PA · no real payment-provider CHARGE or
REFUND · no folio debit · no paid guest access · no Hotel ID 2 financial onboarding · no implicit FX · no
executable PMS reversal · no C38 execution · no Phase 5/6/7 work · **no pull-request merge**.

---

## 8. Governance trail

`D18`/`T0029` authorized Phase 4. `T0030`–`T0041` record the implementation increments and the corrections
made along the way, including the ones where an earlier claim was measured and found stronger than its
evidence. `T0042` reconciled stale current-state claims and added the parity validator that makes that class
of staleness fail. `T0043` records the WS-L live-DARK deployment. `D19`/`T0044` accept and close Phase 4.
**No historical receipt was rewritten at any point.**
