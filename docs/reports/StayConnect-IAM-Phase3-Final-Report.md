# StayConnect IAM — Phase 3 Final Report (Stay Resolution, PMS Auth Context, Checkout Grace)

> **Status: ACCEPTED AND CLOSED AT VERIFIED DARK MATURITY.** Product-Owner decision **D16**, transition
> **T0024**, 2026-08-11. Accepted runtime candidate **`7c8b8cf019c5af3dd2294ee268e8f7137e6ef5d4`** — the build
> the appliance runs. Both CI gates were SUCCESS on that HEAD and its evidence artifact was independently
> verified; the run and artifact identifiers are deliberately **not recorded here** — they are in the PR #6 body
> and in `docs/evidence/Phase3-Final-Live-Acceptance-Record.md`, because a committed report that cites the run
> its own commit triggers is always a round behind.
>
> **DARK and NOT CUT OVER.** All Phase-3 flags OFF, legacy public-schema IAM remains the sole production
> authority, iam_v2 dark at 63 tables / 0 rows, zero runtime `iam_v2` grants, no financial posting, no paid
> access. Acceptance is at DARK maturity **only**.
>
> **Accepted limitation, NOT promoted to PASS:** legacy live-session continuity is **NOT PROVEN** — no real
> legacy guest was available during the authorized live windows. Nothing was fabricated to close it.
>
> §6b preserves the ORIGINAL Increment-9 run exactly as observed, including the item that FAILED then. §6c
> records the blocked subset re-validated live on 2026-08-11, every item PASS. Historical failures stay in the
> record; only their current interpretation changes.
>
> **PR #6 IS MERGED AND CLOSED** — merged into `master` on 2026-08-11 as merge commit `8a7230a7220e4c773bfb6399ce7774f31f20c906` under the
> separate Product-Owner merge authorization (decision **D17**, transition **T0025**). The merge introduced
> no content: at the merge, `master`'s tree equalled the merged head's and the whole runtime tree was
> byte-for-byte identical to the accepted candidate. It deployed nothing and enabled nothing. *That
> whole-tree statement describes the merge, not today* — post-closure remediation (T0026, T0027) has since
> changed Caddy deployment material and operational tooling under `deploy/` and `scripts/`. The accepted
> **application/runtime binaries** are unchanged: `data-plane/`, `hotel-admin/`, `control-plane/` and every
> migration path still differ by zero files, and the six appliance binaries still hash-match the accepted
> build. Post-merge gates: Phase 3 Software CI
> **31497023194** and Project Governance **31497023118**, both SUCCESS on the merge commit.

---

## 1. شرح مبسّط بالعامية المصرية

الفيز دي بتخلّي النظام يعرف الضيف من غير ما يسأله كلمة سر: بيقرا من نظام الفندق (الـPMS) مين ساكن فين، وبيربط
ده بالشبكة اللي الضيف متوصّل عليها. أهم حاجة اتعملت إن كل قرار بقى **متسجّل ومتثبت**: لو الضيف عمل تشيك-أوت،
النظام بيحدّد لحظة الخروج **من حدث الـPMS نفسه** مش من ساعة السيرفر، وبيدي الضيف فترة سماح (Grace) بقواعد
معتمدة، وبيقفل الوصول لأي جهاز مش من ضمن اللي كانوا شغالين وقت الخروج — من غير ما يعمل logout للي شغال فعلاً.
وكمان أي حاجة اتسجّلت غلط بتتصلّح بتسجيل تصحيح جديد، مش بمسح القديم. كله لسه **مقفول (DARK)** — يعني الكود
موجود ومتجرّب لكن مفيش أي حاجة اشتغلت على أي فندق حقيقي لحد ما يتاخد قرار التشغيل.

## 2. Current Phase and authorized scope

- **Phase:** 3 — Stay Resolution / PMS Auth Context / Checkout Grace. **ACCEPTED AND CLOSED at verified DARK
  maturity** (D16 / T0024). DARK and **not cut over**.
- **Constraints that still stand:** all Phase-3 flags OFF; zero persistent runtime `iam_v2` privileges; no
  `PS`/`PA`; no financial posting; no paid-access implementation; no Gate-P runtime grants; no IAM-v2 cutover;
  no Phase 4; both CIs green on the same pushed HEAD; never fabricate live evidence.
- **Live contact HAS occurred, under explicit Product-Owner authorization.** The appliance, the production site
  database and the live PMS were contacted during Phase-3 live-dark execution and acceptance: Live Increment 9
  on 2026-08-10 (§6b), its re-validation on 2026-08-11 (§6c), and the final closure. Migration 0010 is applied
  and the accepted runtime candidate is deployed. Those results are recorded in §6b/§6c and in
  `docs/evidence/Phase3-Final-Live-Acceptance-Record.md`.
  > *Historical (superseded 2026-08-10).* Throughout the Phase-3 SOFTWARE-development period, up to and
  > including the pre-live safety candidate, the standing constraints additionally included **"PR #6 open and
  > unmerged"** and **"no appliance, Production DB or live PMS contact"**. Both were true for that period and
  > were enforced by the gates of the day. The first ended with the authorized merge of PR #6; the second ended
  > when Live Increment 9 was authorized and executed.
- **PO authorization reference:** the Phase-3 execution directive and the eleven successive correction
  directives against the Increment-7 Checkout subsystem, the closing scorecard, the Live Increment-9
  authorization and its re-validation, and the final acceptance decision **D16**.

## 2a. Where Phase 3 stands

**ACCEPTED AND CLOSED at verified DARK maturity** (D16 / T0024, 2026-08-11). The complete Phase-3 software
scope is implemented and every dimension in the Acceptance Matrix (§6a) carries a verdict.

The accepted runtime candidate `7c8b8cf019c5af3dd2294ee268e8f7137e6ef5d4` is deployed and running on the
appliance. Live Increment 9 (§6b) and its re-validation (§6c) are complete; the two findings the re-validation
produced are closed and live-verified. One dimension remains **NOT PROVEN** and is accepted as such: legacy
live-session continuity, because no real legacy guest was available. No guest, session or authorization was
ever fabricated to close it.

## 3. What was implemented

**Stay domain and event application**
- One physical Stay-Event→Checkout transaction (`NewProcessorWithCheckout`); the legacy server-clock flip is
  **deleted**, and a `GO` event with no wired Converter fails closed rather than establishing an unverified
  boundary.
- Per-Interface ordered application under an advisory lock; proven under 24 concurrent processors.
- Exact event lineage (`stays.last_applied_event_id`), **structurally enforced and composite-scoped** so it can
  never reference another tenant/site/interface's event.
- Sharers (legal multi-occupancy, exactly one primary that moves rather than duplicating), Folios, and
  source-conflict detection routed to `MANUAL_REVIEW` with bounded codes.

**Entitlement lifecycle**
- **Bitemporal history**: `effective_at` (true business time, stored verbatim, never clamped) and `recorded_at`
  (system time, monotonic). Corrections are explicit — `supersede_entitlement_transition` and
  `terminate_entitlement_at_boundary` — and nothing is ever edited or deleted.
- Controlled-writer authorization boundary (`SECURITY DEFINER`, per-family owner resolved by `regprocedure`),
  append-only history, deferred status/history coherence.
- Controlled device authorization/deauthorization with the plan device limit enforced atomically.

**Checkout Grace**
- Boundary derived from the trusted PMS event; eligibility proven from history at the boundary; grandfathering
  by authorization interval; sessions rebound **without logout**; canonical Emergency-Grace catalog fallback
  with a resolvable operational alert; one conversion per episode.
- **Post-boundary revocation** on every outcome: nothing keeps forwarding traffic for access that has ended.

**Commerce, accounting and enforcement**
- Atomic Auth Context → Quote → Purchase → Entitlement → device authorization in one transaction; paid access
  refused (`ErrSettlementRequired`), never approximated.
- Accounting attributed by session→entitlement binding intervals; boundary watermarks freeze decision evidence;
  late samples recorded as delayed, never folded back.
- Derived netd shaping plan and acctd expiry enforcement at the true ending time.

**Resolution, surfaces and tooling**
- STRICT resolver fan-out over the complete candidate vector with idempotent `auth_resolutions`.
- Guest-portal uniform non-success contract (byte-identical failures).
- Hotel-Admin Phase-3 API + RBAC + four UI pages, dark-gated.
- Increment-9 offline preflight, evidence collector and deployment/rollback/reboot runbook.

**Network enforcement — the pre-live safety round**
- **`netd` is the single privileged network-enforcement owner**, holding both kernel halves. `nft` decides
  packet authorization; `tc` decides accountable metering; `iam_v2.sessions.state` is durable runtime state.
  Removing a tc classification denies **nothing** — those packets fall back to the bridge's default class and
  keep flowing, unmetered. Phase 3 authorizes only in `phase3_auth_ipv4`; legacy `auth_ipv4` is unreachable
  from every Phase-3 path.
- **Packet authorization is a bounded renewable kernel lease.** 90 seconds — equal to the producer's plan
  validity, so forwarding stops at the same moment the appliance stops being able to vouch for it — clamped by
  the session's own hard access boundary, and 15 seconds while durable `active` is not yet proven. A healthy
  reconciliation renews it; silence expires it. Renewal is an atomic delete+add in one nft transaction,
  because a repeated `add element` does not restart an existing element's timer.
- **An unprovable durable activation fails closed.** The promotion is retried and, on an ambiguous failure,
  authoritative state is re-read (a committed transaction whose acknowledgement was lost is not a failure).
  Past the activation grace the session is denied, torn down and quarantined with a doubling backoff.
- **"ACTIVE means authorized AND accountable" is enforced by the database.**
  `iam_v2.activate_session_enforcement` verifies the accounting checkpoint itself — this session, its device,
  the bridge, the class minor implied by its IP and the exact generation — and refuses a missing, foreign,
  stale or contested origin.
- **The Phase-3 nft structure is produced by the renderer itself (`nftconverge`, ADR-0003).** `netd` reconciles
  the live ruleset against a fresh render of the running binary on every start, executing nothing when it
  already matches, so the Phase-3 set and rules arrive with the service and survive every restart and reboot.
  **This is the current approved deployment and cutover path; no manual installation step exists or is
  permitted.**
  > *Historical, superseded.* A **surgical live-dark nft foundation** (`cmd/phase3-foundation`) was built to
  > install the Phase-3 set and rules into a RUNNING ruleset in one nft transaction, and an earlier revision of
  > this report said the cutover became flag-only *only after that install had been performed on the unit*.
  > Live Increment 9 disproved that: what the tool installed did not survive the next `netd` start, because boot
  > reconciliation replayed a stored bundle rendered by an older binary (§6b). The tool is **retired from the
  > deployment and rollback procedure** and survives only as a read-only diagnostic, which invalidates the
  > render marker if a structural `install`/`rollback` is ever run by hand.
- **Guest-visible timing now composes with enforcement.** The uniform non-success budget is derived from the
  reconciliation cadence rather than a PMS round trip, and scd's enforcement wait is derived from the caller's
  deadline instead of a flat maximum it could never reach. There is still exactly one budget for the endpoint,
  so the occupancy-enumeration protection is unchanged in form.

**The identity-binding correction.** Three narrow gaps, all in what the security journal was allowed to be
trusted about:

- **The journal envelope was never checked against this appliance.** It carries a tenant, site and appliance
  id, and `load()` validated only the records inside it. A journal from another property — arriving by an
  entirely ordinary route: a restored image, a cloned VM, a copied `/var/lib`, a re-homed unit after a tenancy
  change — would have been read as a valid document with no attempts outstanding, and every guest here would
  have been given a fresh grace on the strength of a file describing somewhere else. The scope is now a
  parameter of `load()`, so the file cannot be read without stating whose it must be; a foreign, partial or
  empty scope is UNKNOWN, and it is never silently re-scoped into ours. The only clean first run is a file
  that genuinely does not exist.
- **A record carried two identities that were never compared.** `Key = bridge|sessionID` and `SessionID` were
  both present, and validation checked only that the key contained a pipe — so a record whose key named
  `session-B` beside a `SessionID` of `session-A` passed, and whichever identity a reader used, the other was
  wrong. There is now one canonical parser: exactly one separator, non-empty bridge and session, and the key's
  session must equal `SessionID`. `br-guest|a|b` is rejected outright rather than read two ways.
- **"Boot identity" was validated as an opaque token.** Eight or more printable characters was far too
  generous for a value whose entire job is to be equal to itself across a restart and unequal across a reboot.
  `/proc/sys/kernel/random/boot_id` is a canonical lowercase 8-4-4-4-12 UUID and nothing else, so the check is
  now exact — a truncated read, a partially written file or an error message copied into place is refused
  rather than compared.

**The previous pre-live blockers.** Three more, and the first two were failures of *trust* rather than of logic:

- **Boot identity could fail open.** The cross-reboot guarantee rests on two readings — a monotonic uptime and
  the identity of the boot it belongs to — but the identity was a bare string, empty on any read error. So
  `crossBoot()` compared `""` with `""`, concluded the boot was unchanged, and a previous boot's deadline
  could be measured against the new boot's uptime. The bound was silently gone with nothing in the logs. The
  identity is now read with an error and validated for shape; an unreadable, empty or malformed value denies
  new *and renewed* provisional authorization, and `crossBoot` treats an untrusted identity on either side as
  a boot change. It deliberately does **not** disconnect a session already proven durably `ACTIVE`: that
  session needs no bound, and manufacturing an outage for it would be the wrong direction.
- **The security journal was trusted because it parsed.** It is authority now, and valid JSON is not coherent
  security state: a deadline an hour past its start, a negative reading, boot-relative numbers with no boot,
  two records for one key — all parse perfectly and all widen or erase the bound. Every persisted record is
  now validated for coherent, bounded state, and anything incoherent becomes UNKNOWN and fail-closed, exactly
  like an unreadable file. Nothing is normalised: repairing a corrupt record into a plausible one is how a
  destroyed bound becomes an ordinary-looking fresh grace.
- **A transition receipt described its own future.** T0019 recorded `2026-08-10T15:30:00Z` in a file first
  committed at `15:03:39Z` — twenty-six minutes before the moment it claimed. It is corrected to
  `2026-08-10T15:02:33Z`, the committer timestamp of its own `source_commit` `41aa0b8`, read from the
  repository rather than estimated again. An audit of all nineteen receipts found four more (T0007, T0008,
  T0016, T0017) future-dated against their introducing commits; those are historical evidence and are **not**
  rewritten — they are listed explicitly by the new check and grandfathered, with the rule enforced from T0018
  onward. Rewriting an old receipt to satisfy a new rule would manufacture exactly what the rule exists to
  prevent.

**The previous durability closure.** Three more things were wrong in ways that read as correct:

- **The security timer was not durable at the instant access began.** The attempt was recorded with the rest
  of the class inventory at the END of a reconciliation pass, but the guest was authorized in the middle of
  one. A crash in between lost the bound entirely, and the next process found either a perfectly valid older
  file or none at all — which is indistinguishable from a clean first run — and awarded a brand-new grace.
  Repeat the crash and the guest holds provisional access indefinitely with every individual bound still
  correct. The record is now WRITE-AHEAD: fsynced, file and directory, in its own journal, before the first
  provisional element goes in. A write that cannot be proven durable denies admission — there is nothing to
  recover, because nothing was granted. A failed CLEAR is the opposite asymmetry and leaves the record in
  place, which is harmless to a session that can be proven active and protective for one that cannot.
- **The bound was measured against a clock anyone could move.** It compared Unix wall-clock timestamps, so an
  NTP correction, a wrong RTC after a power cut or a resumed snapshot made `now − began` smaller (or negative)
  and stretched a 30-second grace to the size of the jump. It is now measured against boot-relative monotonic
  time from `/proc/uptime`, which no clock adjustment can move; the wall-clock stamp survives only as an
  audit field, explicitly labelled. Across a reboot the monotonic timeline restarts and cannot be bridged
  honestly — the only available estimate of the downtime comes from the very RTC that is not trusted — so a
  boot change never yields a fresh grace: the session stays denied until durable state proves it ACTIVE.
- **The repository was not actually Zero-Stale.** `current_activity` read PRE-LIVE SAFETY while
  `phase3_execution.stage` still opened with the superseded SOFTWARE-candidate token and still quoted the old
  1200ms Portal budget as current, and PR #6's Status line still announced `DARK ACCEPTANCE CANDIDATE`. The
  first two are now checked against each other; the third is checked at the CI boundary, because the PR body
  is the one authoritative surface that is not committed content and every previous check stopped at the
  repository edge.

**The previous pre-live corrections.** Three things were wrong in ways that read as correct:

- **A clamped lease could overshoot its own deadline.** `leaseFor` computed the exact remaining time to
  `AccessEndsAt`, and then the nft layer rounded it UP to whole seconds — so a boundary 1.9 seconds away
  became a `timeout 1s`… but a boundary 200ms away became `timeout 1s` too, which is 800ms **past** the
  deadline. On almost every real boundary (they do not land on whole seconds) the clamp permitted exactly
  what it existed to forbid. The clamp now truncates, and a remainder too short to represent is refused
  outright rather than rounded into existence — the guest loses at most the last 999ms of a stay, which
  nobody can perceive, instead of gaining up to 999ms past a contract.
- **The unproven-activation bound was measured against process uptime.** The grace clock and the quarantine
  lived only in memory, so restarting netd handed the same guest a brand-new grace: a process restarting
  every few seconds could renew a provisional authorization indefinitely while durable state never once said
  the guest was active. Every individual bound in the code was correct; the thing they were measured against
  was not durable. The clock is now persisted beside the class inventory and restored on start, so a restart
  and a reboot continue the same countdown — and if that record cannot be read or written, an unprovable
  activation is treated as having already spent its grace, because losing a file must not buy access.
- **A Zero-Stale check had never actually run.** The "stale preflight total" check referenced `$ROOT`, a
  variable this validator does not define. Under `set -u` the unbound expansion killed only the command
  substitution's subshell, so the value came back empty and the guard below skipped the whole check in
  silence — for two rounds. It was found by making a *new* check fail loudly when it cannot read state
  instead of passing quietly, and the moment it was fixed it immediately caught a genuinely stale preflight
  total in this very report. A governance check that silently does nothing is worse than none, because it is
  trusted; both now fail rather than skip.

**What the real-kernel suite found that nothing else could.** `nft -j` emits set elements in two shapes: an
element with stateful attributes (a timeout, an expiry) is wrapped as `{"elem":{"val":{"concat":[…]},…}}`,
while an element with **none** — which is exactly what a permanent legacy authorization is — carries no
wrapper and no `val` at all: `{"concat":[…]}`. The parser read only the first shape, so every permanent
concatenated element was **invisible** to `List`, and therefore to `Authorized`, and therefore to `Deny`,
which silently deleted nothing. Every modelled test passed because every modelled fixture fed the shape the
code already understood. It took real `nft` output to see it, and it mattered directly: the surgical
foundation's legacy-parity proof is a comparison of enumerated legacy elements, and against permanent elements
it would have been comparing two empty lists and reporting a match. Fixed, with a regression test on both
shapes.

## 3a. Self-audit — four failure timelines, traced end to end

**1. ACTIVE guest → acctd dies → netd dies → entitlement window expires.** The guest holds a lease of at most
90 seconds, clamped to their window end. Nothing renews it: the producer is gone, the applier is gone, the
database is irrelevant. The kernel removes the element on its own and the forward chain (policy `drop`) stops
forwarding. The tc class is still installed — nothing was alive to remove it — and that is exactly the point:
the class is not what ended the access. Proven modelled in `phase3_lease_test.go` and **in a real kernel** in
`internal/kerneltest` (the lease elapses and the ping stops arriving).

**2. tc + nft enforcement succeeds → the durable ACTIVE result is lost or unreadable.** The guest was admitted
on the 15-second provisional lease, so this state is bounded before anything else happens. `proveActive`
re-reads authoritative state: if the transaction actually committed, the session is proven active and the
lease is extended to full length — a lost acknowledgement never disconnects a correct guest. If it genuinely
did not commit, the provisional lease is re-issued while the promotion is retried, and past the 30-second
grace the session is denied (nft first, proven), torn down and quarantined with a doubling backoff. If netd
dies at any point in that window, the kernel drops the authorization within 15 seconds. **There is no path in
which an unconfirmed activation becomes lasting internet access,** and exactly one Session exists throughout
(the promotion is idempotent and the grant path recovers the same Session by device + Auth Context).

**3. Populated legacy `auth_ipv4` → DARK foundation install → reboot → rollback.** The install is one nft
transaction that adds a set, adds one rule and rewrites the captive rules in place; it snapshots the legacy
elements before and re-reads them after, and rolls itself back if they differ. In the real-kernel suite a
legacy guest is **pinging throughout** and never loses a packet across install, second install and rollback.
The reboot is the one place where an honest statement has to replace an easy one: **nftables state is not
persistent.** A reboot re-applies the generated ruleset as `delete table` + recreate, so *both* authorization
sets come back empty — legacy included — and scd re-establishes legacy authorizations from `public.sessions`
exactly as it does today. What the reboot must preserve is the FOUNDATION, and it does, because the current
render emits the Phase-3 set and rules; a post-reboot install is a verified no-op. That is asserted against a
real kernel, and the runbook says the same thing rather than implying elements survive.

**4. A normal Portal request against the worst healthy reconciliation timing.** A guest who taps Connect one
millisecond after a reconciliation tick waits out the rest of that second, the pass's own work, scd's 100ms
polling granularity and two unix-socket hops — about 1.8 seconds in the worst healthy case. The uniform budget
is 2500ms with a 200ms upstream reserve, leaving 2300ms of usable time, so that guest is told they are
connected rather than reproducibly told they are not. *(HISTORICAL, superseded: the previous composition could
not do this — portald's old 1200ms deadline was passed down as the context, so scd's nominal 8-second wait was
unreachable and the effective wait was whatever was left of that 1200ms. Those values are no longer in the
software; the current budget is 2500ms with a 200ms reserve.)* Pinned by boundary tests on both sides.

## 4. Practical effect

Nothing changes for guests or operators today: every surface is dark. What now exists is a Stay/Checkout model
whose decisions are **reproducible** — the boundary comes from a durable event, eligibility from append-only
history, usage from binding-attributed accounting, and each decision's evidence is frozen at the moment it was
made. When the flags are eventually turned on, a checkout produces a grace period with the operator's approved
policy, keeps working devices online, cuts everything else at the boundary, and leaves an audit trail that can
be re-derived rather than trusted.

## 5. Risks and limitations

- **Live evidence EXISTS, and is separated from software evidence.** Every claim in §3–§5 is software evidence
  from local builds, unit tests and disposable PostgreSQL 16 containers, and is labelled as such. The appliance,
  the production site database and the live PMS **were** contacted under explicit Product-Owner authorization —
  Live Increment 9 on 2026-08-10 (§6b), its re-validation on 2026-08-11 (§6c), and the final closure — and those
  results are recorded there rather than denied here.
  > *Historical, superseded.* An earlier revision of this bullet read "No live evidence exists. Nothing in this
  > repository has touched an appliance, a production database or a PMS." That was true when it was written and
  > stopped being true on 2026-08-10.
- **Paid access is deliberately unimplemented.** Priced packages and settlement methods beyond `NOT_REQUIRED`
  fail closed; the fixtures used in F3 are zero-amount and are not payment evidence.
- **Gate-P privileges are prepared but NOT applied.** Every runtime service role holds zero `iam_v2` table and
  function privileges; the gate asserts it on every run.
- **Device-limit policies other than `REJECT_NEW_DEVICE`** are refused rather than approximated.
- The Phase-2 Commerce (`internal/iamv2`) direct-Entitlement writer is **eliminated** (§5 of this round): the
  admin path no longer sets Entitlement status with a raw `UPDATE`; it terminates a superseded Entitlement
  through `iam_v2.apply_entitlement_transition`, which also appends the transition history the raw path never
  wrote. There is no remaining Phase-3 family whose authoritative writes bypass the controlled-writer boundary,
  and nothing here waits on cutover for that. (Cutover itself remains a separate, unauthorized future step.)

## 6. Acceptance tests

Every row below is executed by the **Phase-3 Software workflow** (`.github/workflows/phase3-software.yml`,
job `phase3-full-software-gate`) on the delivery HEAD recorded in §12, in one run, and its totals are recorded
in the evidence artifact that run uploads (`phase3-software-evidence-<delivery-HEAD>`). "PASS" means the suite
ran to completion with no failing assertion and no skipped required test; nothing here is a workstation number
or inferred from a previous run. The totals below are deterministic for this HEAD and are the ones the
artifact records; the run's numeric run IDs, artifact ID and integrity-manifest SHA-256 are in the PR #6 body
(they cannot be embedded in the commit they describe — the same self-reference rule as the change manifest).

| Test | Result | Evidence |
|---|---|---|
| Offline preflight (build, flags, migration reversibility, zero runtime privilege, control-plane invariants, rollback ordering, accountable-before-forwarding order, bounded kernel lease, DB-enforced accountability, surgical nft foundation, hard-boundary lease truncation, write-ahead durable activation bound, monotonic security time, trusted boot identity, semantic journal validation, assignment-scope binding, canonical key identity, ruleset durability across restart/reboot, verified binary rollback, network lifecycle: confirmed-revision boot + safe rollback + marker truthfulness, pre-convergence rollback boundary, DARK pmsd deployment contract) | **PASS 74/74** | `scripts/phase3-preflight.sh --json` |
| Migration lifecycle gate (apply → behaviour → down → re-apply, disposable PG16) | **PASS 362/362** | `iam_v2_scratch/phase3_0010_lifecycle.sh` |
| PG16 integration suites (pmsd, stayengine, authctx, checkout, staygrant, pmsresolve, enforce, writerguard, edged, acctd, scd) | **PASS** (all eleven) | `scripts/pmsd-pg-integration.sh` |
| Go unit tests, whole module | **PASS** | `go test ./... -count=1` (JSON-counted) |
| Go race suite (pmsd, resolver, authctx, staygrant, checkout, scd, acctd, netd, writerguard, shape/shapeplan) | **PASS** | `go test -race` (CI) |
| F1–F7 named flow suite | **PASS** | `internal/checkout/f_flows_integration_test.go`, `internal/stayengine` |
| ≥24 concurrent checkout handlers / resolutions / grants / device authorizations | **PASS** | integration suites |
| Hotel-Admin component tests (Vitest) | **PASS 63/63** | `npx vitest run` (CI, JSON reporter) |
| Hotel-Admin + guest-portal E2E and accessibility (real browser) | **PASS 49/49** | `npx playwright test` (CI, JSON reporter) |
| TypeScript typecheck | **PASS** | `npx tsc --noEmit` (CI) |
| Production build with Phase-3 flags OFF | **PASS** | `npx next build` (CI) |
| Guest-portal uniform non-success contract (server) | **PASS** | `cmd/portald/pms_phase3_test.go`, `pms_phase3_handlers_test.go`, `pms_phase3_budget_test.go` |
| Guest-portal Phase-3 flow + resilience (real browser, real template) | **PASS** | `hotel-admin/e2e/phase3-guest-portal*.spec.ts` |
| Phase-3 network-enforcement system suite (nft + tc + Session, bounded lease, hard-boundary precision, durable restart/reboot bound) | **PASS** — exact count in the artifact (`counts/enforcement.json`) | `cmd/netd/phase3_enforcement_test.go`, `phase3_lease_test.go`, `phase3_durability_test.go`, `internal/nft` contracts |
| Bounded-lease, hard-boundary-precision and durable-activation suites | **PASS** (counted inside the network-enforcement step above) | `cmd/netd/phase3_lease_test.go`, `cmd/netd/phase3_durability_test.go` |
| nft packet-authorization + lease command contract (Phase-3 set only; legacy never named) | **PASS** | `internal/nft/nft_phase3_test.go` |
| Surgical live-dark nft foundation (install/rollback preserving a populated legacy set) | **PASS** — exact count in the artifact (`counts/foundation.json`) | `internal/nftfoundation/foundation_test.go` |
| Controlled-activation poison tests (fabricated / foreign / stale / contested accounting origin) | **PASS** | `cmd/scd/phase3_activation_integration_test.go` (PG16) |
| Portal/enforcement timing composition (tick boundaries, derived wait, single uniform budget) | **PASS** | `cmd/portald/pms_phase3_budget_timing_test.go`, `cmd/scd/phase3_auth_timing_test.go` |
| **REAL-KERNEL contract suite** — real `nft`, real `tc`, real packets, disposable Linux network namespaces | **PASS** — exact count, kernel and tool versions, and the host-ruleset-unchanged proof in the artifact (`counts/kernel-netns.txt`) | `internal/kerneltest` via `scripts/ci/kernel-netns-suite.sh`; **kernel evidence on a disposable CI machine, NOT live appliance evidence** |
| Full Phase-3 Software CI + Governance CI on the same pushed HEAD, evidence artifact uploaded | **PASS** | §12 |
| Live read-only PMS protocol verification | **PENDING** | operator-executed; not simulated |
| Live-dark deployment, reboot drill, rollback rehearsal, flags-OFF confirmation | **PENDING** | operator-executed; runbook §2–§5 |

**Per-suite counts are deliberately not hard-coded here.** They describe the CI run that this delivery commit
triggers, so a committed document could only ever carry a previous run's numbers — the same reason the run ids
and artifact metadata live in the PR body rather than in this file. The artifact's `counts/` directory carries
the exact totals for the run that produced it, and the Zero-Stale validator fails if numbers that belong to a
run are pasted back in here.

### On retries

Both disposable-PostgreSQL gates now separate an infrastructure failure (exit 2 — the container or the
baseline schema could not be built, and no assertion was ever reached) from a failed assertion (exit 1). CI
retries **only** exit 2, once. A failed assertion is final. The previous policy retried the whole script on
any failure, which would have let an order- or timing-dependent defect pass on a second attempt and be
reported green.

## 6a. Phase-3 Acceptance Matrix

Verdicts used: `PASS — SOFTWARE`, `PASS — LIVE`, `BLOCKED / PARTIAL`, `FAIL`, `NOT PROVEN`, and
`OUT OF SCOPE BY APPROVED CONTRACT`. `PENDING — LIVE INCREMENT 9` no longer appears: Increment 9 was executed
on 2026-08-10 and rows 31–32d carry its real verdicts, including one FAIL and one NOT PROVEN. An earlier draft carried a fourth,
`PENDING — PO SCOPE DECISION`, for the Hotel-Admin surfaces in
`docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md`. **The Product Owner decided (D15, Option C): the proposal was
REJECTED and no scope was reduced.** Those surfaces were built and are now `PASS — SOFTWARE` on the same
footing as the rest — real `edged`→PostgreSQL API tests, RBAC, cross-site refusal, step-up, optimistic
conflict, audit and redaction, plus Vitest, Playwright and accessibility. The fourth verdict no longer
appears.

| # | Dimension | Verdict | Evidence |
|---|---|---|---|
| 1 | Migration 0010 applies, behaves, rolls back and re-applies (disposable PG16) | **PASS — SOFTWARE** | `iam_v2_scratch/phase3_0010_lifecycle.sh` |
| 2 | Durable accounting checkpoints; absolute-counter ingestion; restart bills the gap exactly once | **PASS — SOFTWARE** | migration §4p; `cmd/acctd/phase3_pass_integration_test.go` |
| 3 | Accounting attribution at SAMPLE time across a Grace rebinding; no current-entitlement fallback | **PASS — SOFTWARE** | `cmd/acctd/phase3_boundary_integration_test.go` |
| 4 | Accounting writer boundary: raw INSERT/UPDATE/DELETE refused for a privileged non-owner | **PASS — SOFTWARE** | lifecycle gate §C7; `phase3_boundary_integration_test.go` |
| 5 | Controlled operations: SECURITY DEFINER, pinned `search_path`, PUBLIC EXECUTE revoked, zero runtime grants | **PASS — SOFTWARE** | lifecycle gate §1/§5/§6 |
| 6 | Every Phase-3 writing service refuses to start on an unenforced boundary or as the writer's owner | **PASS — SOFTWARE** | `internal/writerguard` + its PG16 suite |
| 7 | netd is the only Phase-3 tc writer (ADR-0002); acctd holds no tc client | **PASS — SOFTWARE** | `cmd/acctd/phase3_test.go`; preflight |
| 8 | The shaping producer is authenticated by `SO_PEERCRED` against one allowlisted uid | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 9 | A dark netd refuses every plan on its own authority, and discloses no class generations | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 10 | Scoped, versioned, expiring, hashed plan envelope; stale/replayed/out-of-scope plans refused across restart | **PASS — SOFTWARE** | `internal/shapeplan`; `cmd/netd/phase3_shaping_test.go` |
| 11 | Full-state reconciliation removes unclaimed classes, including on a bridge with no sessions | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 12 | Teardown precedes shaping; partial application and an unreadable kernel are reported degraded | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 13 | STRICT multi-interface resolution; ambiguity and indeterminacy grant nothing | **PASS — SOFTWARE** | `internal/pmsresolve`; `cmd/scd/phase3_auth_integration_test.go` |
| 14 | Server-derived identity only: no stay, interface, device, network or price from a guest body | **PASS — SOFTWARE** | `cmd/scd/phase3_auth.go`; `cmd/portald/pms_phase3_handlers_test.go` |
| 15 | One-time Auth Context: consumed exactly once, device-bound, expiring | **PASS — SOFTWARE** | `internal/authctx`; `cmd/scd/phase3_auth_integration_test.go` |
| 16 | Atomic Quote → Purchase → Entitlement → device authorization → Session; no session before its entitlement | **PASS — SOFTWARE** | `internal/staygrant`; `cmd/scd/phase3_auth_integration_test.go` |
| 17 | Paid packages refused even when named directly (no silent free grant) | **PASS — SOFTWARE** | `cmd/scd/phase3_auth_integration_test.go` |
| 18 | Uniform guest non-success contract: every failure identical, byte for byte | **PASS — SOFTWARE** | scd + portald suites; `hotel-admin/e2e/phase3-guest-portal.spec.ts` |
| 19 | Guest Portal Phase-3 flow in a real browser on the real template | **PASS — SOFTWARE** | `hotel-admin/e2e/phase3-guest-portal.spec.ts` |
| 20 | Hotel Admin: Stays, occupants, folios | **PASS — SOFTWARE** | vitest + Playwright |
| 21 | Hotel Admin: Stay-Event review queue and refusal reasons | **PASS — SOFTWARE** | vitest + Playwright |
| 22 | Hotel Admin: Checkout-Grace selector, publication, version conflict | **PASS — SOFTWARE** | vitest + Playwright; `cmd/edged` PG16 suite |
| 23 | Hotel Admin: operational alert triage, bounded actions, concurrent change | **PASS — SOFTWARE** | vitest + Playwright; `cmd/edged` PG16 suite |
| 24 | Hotel Admin: PMS Interfaces, immutable Revisions, current/published publish state | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `hotel-admin/test/phase3-interface-pages.test.tsx`; `e2e/phase3-pms-interfaces.spec.ts` |
| 25 | Hotel Admin: write-only Secret rotation (AES-256-GCM, no read path, refused without a key) | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `internal/pmsd/secret.go` seal path |
| 26 | Hotel Admin: Guest-Network→PMS routing, including the networks mapped to nothing | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `e2e/phase3-pms-interfaces.spec.ts` |
| 27 | Hotel Admin: transport / continuity / sync / occupancy health, ingestion backlog with oldest-waiting age | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go` (derived-health + never-connected) |
| 28 | Hotel Admin: Resolution evidence (no guest PII), source conflicts naming both interfaces | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `e2e/phase3-pms-interfaces.spec.ts` |
| 29 | Flags OFF by default; a child flag without its master is a startup failure | **PASS — SOFTWARE** | `internal/iamv2/pms_config.go`; preflight |
| 30 | Dark appliance issues zero Phase-3 SQL, mounts no Phase-3 route, mutates no tc | **PASS — SOFTWARE** | acctd/netd/scd/edged dark tests |
| 30a | Accountable before forwarding: a managed class carries no guest packet before its origin is registered (staged prepare → register → activate) | **PASS — SOFTWARE** | `cmd/netd/phase3_provision.go`; `phase3_provision_test.go`; `internal/shape/shape_staged_test.go`; preflight |
| 30b | Every provisioning failure fails closed: nothing forwards, no epoch exposed, `Shaped` stays 0, plan admitted-not-converged, retry reuses the generation (no duplicate origin) | **PASS — SOFTWARE** | `phase3_provision_test.go` (15 adversarial paths) |
| 30c | An ordinary re-rate preserves counters and the generation (`tc class change`, never delete+add) | **PASS — SOFTWARE** | `internal/shape/shape_staged_test.go`; `phase3_provision_test.go` |
| 30d | A guest is network-active only when packet authorization AND accountable metering are both confirmed (nft admitted only after verified tc) | **PASS — SOFTWARE** | `cmd/netd/phase3_provision.go`; `phase3_enforcement_test.go`; preflight |
| 30e | Fail-closed denies PACKET ACCESS first and proves it, then tears down tc; teardown/expiry use the same order | **PASS — SOFTWARE** | `cmd/netd/phase3_gate.go`; `phase3_enforcement_test.go` |
| 30f | Session is PENDING_ENFORCEMENT until the kernel is confirmed; promotion is idempotent through the controlled writer; the guest grant waits for real ACTIVE | **PASS — SOFTWARE** | `iam_v2.activate_session_enforcement`; `cmd/scd/phase3_auth.go`; scd PG16 suite |
| 30g | Phase-3 reconciliation removes stray Phase-3 authorizations and can never remove a legacy one (separate nft sets) | **PASS — SOFTWARE** | `internal/nft/nft_phase3_test.go`; `phase3_enforcement_test.go` |
| 30h | Phase-3 packet authorization is a BOUNDED RENEWABLE LEASE: no code path installs a permanent nft element, and a healthy reconciliation is the only thing that renews one | **PASS — SOFTWARE** | `cmd/netd/phase3_lease.go`; `phase3_lease_test.go`; `internal/nft` lease contracts; preflight |
| 30i | Producer/applier death fails closed by itself: with acctd and netd both gone and nothing recovering, the guest loses access within the documented bound — verified in the kernel, with nothing running | **PASS — SOFTWARE** | `phase3_lease_test.go`; `internal/kerneltest` (real nft timeout expiry) |
| 30j | A lease is clamped by the session's hard access boundary and renewal re-derives the clamp, so crashes and restarts can never extend the fixed Entitlement deadline; an already-expired entitlement is never leased | **PASS — SOFTWARE** | `leaseFor`; `phase3_lease_test.go`; `shapeplan` contract `/2` |
| 30k | Renewal refreshes exactly one element and does not rely on a repeated `add element` (which does NOT restart an nft timer) | **PASS — SOFTWARE** | `nft.LeaseIn` atomic delete+add; `internal/kerneltest` proves both halves against a real kernel |
| 30l | An activation whose durable ACTIVE cannot be proven holds only the provisional lease, is re-read against authoritative state, and is failed closed and quarantined rather than renewed indefinitely | **PASS — SOFTWARE** | `proveActive`; quarantine in `phase3_provision.go`; `phase3_lease_test.go` |
| 30m | The controlled activation VERIFIES THE ACCOUNTING ORIGIN ITSELF (session, device, bridge, class minor, exact generation) and refuses a missing, foreign, stale or contested one | **PASS — SOFTWARE** | migration §4u `activate_session_enforcement`; `cmd/scd/phase3_activation_integration_test.go` (PG16 poison tests) |
| 30n | Surgical live-dark nft foundation: installs the Phase-3 set and rules into a RUNNING ruleset in one transaction, never flushes or recreates the table, proves legacy-authorization parity, is idempotent, and rolls itself back on failure | **PASS — SOFTWARE** | `internal/nftfoundation`; `cmd/phase3-foundation`; `foundation_test.go`; `internal/kerneltest` on a populated legacy set |
| 30o | Foundation rollback removes ONLY the Phase-3 foundation and leaves every legacy authorization untouched | **PASS — SOFTWARE** | `foundation_test.go`; `internal/kerneltest` (legacy guest stays online across install and rollback) |
| 30p | REAL-KERNEL execution: real `nft` and `tc`, real packets, disposable Linux network namespaces — including the proof that removing a tc classification denies NOTHING and that the nft element is what decides access | **PASS — SOFTWARE (real kernel, disposable CI machine — NOT live appliance evidence)** | `internal/kerneltest`; `scripts/ci/kernel-netns-suite.sh`; host ruleset proven unchanged |
| 30q | Portal/enforcement timing composes: the guest-visible budget covers the worst healthy convergence at either side of a reconciliation tick, the enforcement wait is derived from the caller's deadline, and there is still exactly ONE uniform non-success budget | **PASS — SOFTWARE** | `cmd/portald/pms_phase3_budget_timing_test.go`; `cmd/scd/phase3_auth_timing_test.go`; existing uniform-budget suite |
| 30r | A boundary-clamped kernel lease can NEVER exceed the exact hard access boundary, at any timestamp precision: the clamp truncates to whole seconds and a sub-second remainder is refused rather than rounded up into `timeout 1s` | **PASS — SOFTWARE** | `leaseFor` (truncate); `nft.ErrLeaseTooShort`; `phase3_lease_test.go` boundary matrix (90s / 1.9s / 1.1s / 1s / 999ms / 100ms / expired / repeated renewal / restart); preflight |
| 30s | The kernel's own timeout REPRESENTATION is proven, not assumed: the installed expiry is never later than the lease requested, and a clamped lease really expires before its boundary | **PASS — SOFTWARE (real kernel, disposable CI machine — NOT live appliance evidence)** | `internal/kerneltest` (installed-timeout, sub-second refusal, clamped-expiry-before-boundary) |
| 30t | The unproven-activation bound is DURABLE: a netd restart or a reboot can never reset or extend the maximum authorization time allowed for an activation that cannot be proven | **PASS — SOFTWARE** | `unprovenRecord` in the durable class state; `restore`/`snapshot`; `phase3_durability_test.go` (restarts every 6s for 3 minutes; reboot with an empty kernel) |
| 30u | An unreadable, missing-as-corrupt or unwritable durable activation clock FAILS CLOSED rather than awarding a fresh grace; a legitimately absent file on a first run does not | **PASS — SOFTWARE** | `classStore.load` (absent vs corrupt); `unprovenUnknown`; `phase3_durability_test.go`; preflight |
| 30v | Recovery still works: a promotion that really committed is recovered by authoritative re-read across a restart and the guest is never disconnected — while a readable-but-PENDING database does not silently reset an exhausted grace | **PASS — SOFTWARE** | `proveActive` re-read; `phase3_durability_test.go` |
| 30w | A session with no wall-clock `AccessEndsAt` still has a finite unproven-activation bound | **PASS — SOFTWARE** | `phase3_durability_test.go` |
| 30x | Activation uncertainty is WRITE-AHEAD durable: the attempt and its bound are fsynced (file AND directory) BEFORE the first provisional nft authorization, and a write that cannot be proven durable denies admission outright | **PASS — SOFTWARE** | `cmd/netd/phase3_journal.go` `beginAttempt`; ordering asserted by preflight; `phase3_durability_test.go` |
| 30y | A class-state / journal write failure remains fail-closed ACROSS A RESTART: an older valid file on disk does not become a clean first run, and the earlier bound continues | **PASS — SOFTWARE** | `phase3_durability_test.go` (write failure with an older valid file; corrupt journal; absent journal) |
| 30z | A crash between nft admission and the end-of-pass persistence cannot reset the grace: 24 crash-restart cycles over two minutes keep the same attempt start and the same total authorized time | **PASS — SOFTWARE** | `phase3_durability_test.go` (repeated crashes just after admission) |
| 30aa | Wall-clock rollback cannot extend provisional access: the bound is boot-relative monotonic (`/proc/uptime`), and −1h, −24h, +24h and NTP-style corrections change nothing, including across a restart | **PASS — SOFTWARE** | `cmd/netd/phase3_securitytime.go`; `phase3_durability_test.go` security-time suite; preflight |
| 30ab | A reboot cannot create a fresh unproven grace: a prior boot's monotonic timeline is not bridged, so the session stays denied until durable state proves it ACTIVE — with the RTC behind, ahead, or the database unreadable | **PASS — SOFTWARE** | `crossBoot` handling in `phase3_provision.go`; `phase3_durability_test.go` reboot cases |
| 30ac | An unreadable monotonic security clock denies admission rather than assuming the bound unspent; quarantine records are pruned by durable resolution, never by wall-clock age | **PASS — SOFTWARE** | `bootNow` fail-closed path; `phase3_durability_test.go` (unreadable clock; pruning across ±1 year of wall jumps) |
| 30ad | PR metadata — the one authoritative surface that is not committed — is Zero-Stale-validated at the CI boundary against `project-state.json`, in the same run, with no hard-coded run identifiers | **PASS — SOFTWARE** | `tools/validate-pr-metadata.sh`; Project Governance workflow |
| 30ae | Boot identity is a fail-closed part of the security clock: it is read with an error, validated for shape, and an unreadable, empty or malformed value denies new or renewed provisional authorization | **PASS — SOFTWARE** | `phase3_securitytime.go` `BootID() (string, error)` + `plausibleBootID`; `phase3_bootid_test.go`; preflight |
| 30af | A reboot cannot be HIDDEN by an untrustworthy boot identity: `crossBoot` treats an empty identity on either side as a boot change, so a prior boot's deadline is never compared with a new boot's uptime | **PASS — SOFTWARE** | `crossBoot`; `phase3_bootid_test.go` (reboot + unreadable id; large prior-boot deadline + small new uptime) |
| 30ag | An untrustworthy boot identity does NOT disconnect a session already proven durably ACTIVE — it withholds provisional access without manufacturing an outage | **PASS — SOFTWARE** | `phase3_provision.go` (untrusted identity carried, not failed closed globally); `phase3_bootid_test.go` |
| 30ah | The security journal is validated SEMANTICALLY, not merely as JSON: identity, boot anchoring, non-negative and bounded monotonic readings, began/deadline ordering, a grace no longer than policy, coherent strike/backoff state and no duplicate keys | **PASS — SOFTWARE** | `validateAttempts`; `phase3_bootid_test.go` poison matrix (17 valid-JSON records, each rejected) |
| 30ai | An incoherent security record becomes UNKNOWN and fail-closed — never silently normalised into a fresh grace — while a coherent one is still honoured and enforces its own recorded deadline | **PASS — SOFTWARE** | `journalLoad.Unreadable`; `phase3_bootid_test.go` |
| 30aj | A transition receipt cannot be dated after the commit that introduced it; the four pre-rule receipts are listed and grandfathered rather than rewritten | **PASS — GOVERNANCE** | `tools/validate-transition-times.sh`; Zero-Stale check 1d; Project Governance workflow |
| 30ak | The activation journal is bound to the ASSIGNED scope: it cannot be read without stating the tenant, site and appliance, and a foreign, partial or empty scope is UNKNOWN — never an empty journal and never a clean first run | **PASS — SOFTWARE** | `validateScope`; `journal.load(tenant, site, appliance)`; `phase3_bootid_test.go` (8 foreign-scope cases, including a foreign envelope carrying an otherwise coherent attempt) |
| 30al | A foreign-scope journal is never silently re-scoped into this appliance's own, and the ONLY clean first run is a file that truly does not exist | **PASS — SOFTWARE** | `phase3_bootid_test.go` (re-read after the refusal; absent-file case) |
| 30am | A record's activation key and session identity are parsed CANONICALLY and proven to describe the same session: exactly one separator, non-empty bridge and session, and key-session == SessionID | **PASS — SOFTWARE** | `parseClassKey` (the single canonical reader, also used by `sessionForKey`); poison cases for mismatch, empty bridge, missing session and an ambiguous two-separator key |
| 30an | Boot identity is validated against the ACTUAL Linux contract — a canonical lowercase 8-4-4-4-12 UUID — so a truncated or corrupted read cannot pass and cannot hide a real reboot | **PASS — SOFTWARE** | `plausibleBootID`; `phase3_bootid_test.go` (3 valid forms, 13 malformed including truncation at a group boundary and an error message copied into place) |
| 30ao | The live ruleset structure is reconciled against a FRESH RENDER of the current binary, never against a stored bundle: an appliance whose active revision predates the current renderer converges by itself on the next start | **PASS — SOFTWARE** | `internal/nftconverge`; `netcfg.RenderFingerprint`; `cmd/netd/nft_reconcile_test.go` (pre-Phase-3 table; obsolete bundle never read) |
| 30ap | A STEADY-STATE restart or reboot issues NO nft mutation at all — asserted on the command log, because re-applying the same render would leave an identical structure while deauthorizing every live guest | **PASS — SOFTWARE** | `cmd/netd/nft_reconcile_test.go` (`SteadyStateRestartIssuesNoMutation`, repeated-restart and reboot-settle cases); `internal/kerneltest` on a real kernel |
| 30aq | The one-time UPGRADE converge is itself non-destructive: live authorization is carried across the atomic replace in the SAME transaction, with the REMAINING lease — expired elements are not resurrected and permanent legacy elements are not given a lease they never had | **PASS — SOFTWARE** | `nftconverge.CarryOverCommands`; `nft_reconcile_test.go` (remaining-lease, expired, permanent-legacy cases); `internal/kerneltest` (real packets across the replace) |
| 30ar | REAL-KERNEL proof of the durability mechanism: the generated ruleset loads, the fingerprint comment round-trips through nftables, reboot reconstruction is idempotent, and a legacy-authorized guest stays online across the upgrade | **PASS — SOFTWARE (real kernel, disposable CI machine — NOT live appliance evidence)** | `internal/kerneltest/converge_kernel_test.go`; `scripts/ci/kernel-netns-suite.sh` |
| 30as | An UNREADABLE live state is never treated as an empty one: set absence is decided by enumerating the table, and any read or parse failure — nft unusable, unparseable enumeration, unreadable table, unreadable or garbled authorization set — ABORTS before `nft -f`, leaving the live ruleset and every authorization untouched | **PASS — SOFTWARE** | `nftconverge.ReadLive` + `ErrLiveStateUntrusted`; `nft_reconcile_test.go` `AnUnreadableLiveStateAbortsBeforeAnyMutation` (6 distinct failure modes, each asserting zero mutations and unchanged elements), `AGenuinelyAbsentSetIsEmptyNotAnError`, `ReadFailureIsReportedNotSwallowed`; `internal/kerneltest` `AnUnreadableSetAbortsWithoutTouchingTheRuleset` (real nft, real packets, byte-compared ruleset) |
| 30av | BOOT RECONCILIATION USES THE CONFIRMED ACTIVE REVISION'S IMMUTABLE INTENT SNAPSHOT, never the editable `guest_networks` rows: an unapplied Hotel-Admin draft cannot become the running network at a restart or reboot, and only an explicit Apply that is then Confirmed changes the active network | **PASS — SOFTWARE** | `store.CurrentActiveIntent`; `ReconcileActiveOnBoot`; `network_lifecycle_test.go` (`UnappliedDraftDoesNotBecomeTheRuntime`, `RepeatedRestartsStayOnTheConfirmedRevision`, `OnlyAConfirmedApplyChangesTheActiveNetwork`); preflight §10a |
| 30aw | A revision whose intent snapshot cannot be read leaves the live ruleset ALONE rather than falling back to the mutable rows, and an appliance that has never confirmed a revision does nothing quietly | **PASS — SOFTWARE** | `ErrNoConfirmedActiveRevision`; `network_lifecycle_test.go` (`UnreadableSnapshotLeavesTheLiveRulesetAlone`, `NoConfirmedRevisionDoesNothing`) |
| 30ax | ROLLBACK NEVER DEGRADES INTO EXECUTING A STORED RULESET FILE. It restores structure by rendering the previous CONFIRMED revision's intent; if that cannot be done safely it stops and records an operator-visible blocker rather than running an old `stayconnect.nft` that begins with `delete table` | **PASS — SOFTWARE** | `applier.rollback` (no fallback path); `network_lifecycle_test.go` (`NeverExecutesAStoredRulesetFile` ×2, `AFailedSafeReconciliationDoesNotDegradeToTheLegacyPath`, `RendersThePreviousConfirmedIntent`); preflight §10b |
| 30ay | The render marker cannot outlive the structure it describes: the operator foundation tool invalidates it on any install or rollback that changes structure, a no-op leaves a truthful marker alone, and no package outside the renderer and that tool emits structural nft commands | **PASS — SOFTWARE** | `nftfoundation.invalidateMarker`; `foundation_test.go` (rollback/install invalidate, no-op preserves, unmarked table issues no delete); preflight §10d/§10e (audit of every structural mutation path) |
| 30at | Binary rollback cannot report success without having changed the RUNNING process image: replacement uses `install(1)`, a failed replacement is immediately fatal, and verification compares the expected hash on disk AND in `/proc/<pid>/exe` | **PASS — SOFTWARE** | `scripts/binary-rollback.sh`; `scripts/ci/binary-rollback-tests.sh` (16 checks incl. the exact Increment-9 false-PASS) |
| 30au | The authoritative migration runner works on this appliance without ad-hoc operator discovery: `--apply-role` executes as the least-privileged schema owner, a superuser-owned ledger is accepted on its own merits, and a missing grant names the exact `GRANT` | **PASS — SOFTWARE** | `scripts/edge-migrate.sh` (`--apply-role`, superuser-owner acceptance, actionable grant message); live-site least-privilege checks unchanged |
| 31 | Live read-only PMS protocol verification (Increment 9, Item 1) | **PASS — LIVE (2026-08-10)** | Hotel ID 3 `150.0.0.18:5003`, reviewed builders only; 149 frames parsed, 0 parse errors, byte-identical across two runs; record/field IDs only, no guest data |
| 32 | Live-dark deployment: migration 0010 + current-render convergence (Increment 9, Item 2) | **PASS — LIVE (re-validated 2026-08-11)** | Originally BLOCKED/PARTIAL on the old deployed HEAD. On the corrected candidate the first start converged once — `from_fp="" → aa3d99d18590b220689cfceab197da0c`, `carried_elements=0` — leaving `phase3_auth_ipv4` present and EMPTY with the legacy `auth_ipv4` digest unchanged, 7 services running / 0 failed. 0010 was already applied and was NOT re-applied |
| 32a | Post-reboot persistence of the required Phase-3 structure (Increment 9, Item 3) | **PASS — LIVE (re-validated 2026-08-11)** | Originally FAIL on the old deployed HEAD. Three controlled reboots (boot ids `4215e610…`, `14189f3e…`, `bad37249…`): each reconstructed the CONFIRMED active revision `0cb0028c…`, fingerprint `aa3d99d1…`, `phase3_auth_ipv4` present and empty, legacy digest unchanged, 0 failed units. The `inet stayconnect` table is byte-identical across cycles |
| 32b | Rollback rehearsal (Increment 9, Item 4) | **PASS — LIVE (re-validated 2026-08-11)** | The corrected `scripts/binary-rollback.sh` rolled all five services back to the previous release and verified each on disk AND in `/proc/<pid>/exe` (5/5), then redeployed the candidate and verified again (5/5). An injected "Text file busy" was FATAL: exit 1, nothing restarted, binaries untouched |
| 32c | Flags-OFF DARK safety on the running unit (Increment 9, Item 5) | **PASS — LIVE (2026-08-10)** | none of the six flags in any env file or unit; `STAYCONNECT_PHASE3_*` count 0 in all five running process environments; netd logs `active=false`; `phase3_auth_ipv4` 0 elements; iam_v2 0 rows across 63 tables |
| 32d | Legacy live-session continuity across the live-dark deployment | **NOT PROVEN** | there were ZERO authorized guests and ZERO active legacy sessions throughout the window, so parity was proven structurally (byte-identical `auth_ipv4`, unchanged rule and DNAT counts) but never with a live guest. No guest or session state was fabricated to satisfy a test |
| 32e | A STEADY-STATE `netd` restart issues NO structural nft mutation on the live appliance — proven on the command log and the recorded event, not on ruleset equality | **PASS — LIVE (2026-08-11)** | three restarts (initial boot and inside each of the first two reboots): no new `nft structure converged` line, and the recorded `boot_reconcile` event reads `nft_changed: false, live_fp_before == desired_fp`. The contrasting converges read `nft_changed: true, live_fp_before: ""` |
| 32f | An UNAPPLIED Hotel-Admin draft cannot become the running network across a reboot | **PASS — LIVE (2026-08-11)** | `guest_networks` was edited (captive flag) without Apply or Confirm and the appliance rebooted: active revision unchanged `0cb0028c…`, fingerprint still `aa3d99d1…`, still 6 captive DNAT rules (the draft would have removed two), max seq still 56, 0 apply/confirm events. The draft was then restored and verified identical to the confirmed intent |
| 32g | ROLLBACK ACROSS THE PRE-`nftconverge` BOUNDARY is automatically permitted only when legacy authorization is EMPTY; with guests online it fails safe, changes nothing, and names the operator action — with no force/override path on the ordinary command | **PASS — SOFTWARE (real kernel, disposable) + LIVE (empty-set case)** | `scripts/binary-rollback.sh` `check_compat_boundary`, capability read from the target ARTIFACT (`netd-render-fp=`); `scripts/ci/binary-rollback-tests.sh` (29 checks); `internal/kerneltest/rollback_boundary_kernel_test.go` — real populated `auth_ipv4`, real packets, refusal proven with the guest still online. The empty-set case is the one exercised live |
| 32h | The DARK `pmsd` deployment contract is COMPLETE: the reviewed unit is installed and enabled, runs as a dedicated non-root account, opens no PMS socket, performs no Phase-3 SQL, does not restart-loop, and survives a reboot in that state | **PASS — LIVE (2026-08-11)** | `deploy/systemd/stayconnect-pmsd.service` + `deploy/env/pmsd.env.dark` + `scripts/install-pmsd-dark.sh` (installs the least-privilege account rather than weakening the unit, then verifies the contract); preflight §11 |
| 33 | Gate-P per-service EXECUTE grants and role separation | **OUT OF SCOPE BY APPROVED CONTRACT** | separately gated; zero runtime grants while dark |
| 34 | Paid access, financial posting, `PS`/`PA`, implicit FX, programmatic reversal | **OUT OF SCOPE BY APPROVED CONTRACT** | refused in code (`ErrSettlementRequired`) |
| 35 | Phase 4 | **OUT OF SCOPE BY APPROVED CONTRACT** | not started |

## 6b. Live Increment 9 — what was actually executed, and what it found

Executed 2026-08-10 against appliance `172.21.60.23` (`radius`), identity proven before any action: appliance
`ef78219b-0d47-4465-9f77-3d0c702c815c`, serial `SC-BEN1-JS4A-0D9C`, signed assignment to tenant Coral Sea
Resorts / site Coral Sea Holiday Resort, licence `active` with `wan_mac` matching the live `ens160` MAC.

| Item | Verdict | What was observed |
|---|---|---|
| 1 — read-only live PMS protocol verification | **PASS** | Hotel ID 3 `150.0.0.18:5003`. The reviewed builders only (`LS/LD/LR/LA/DR/LE`), behind an allowlist that makes a financial record unconstructible. 149 frames parsed with zero parse errors (122 `GI`, 24 `GO`, 1 `DS`, 2 `LS`); two runs byte-identical (149 frames / 8594 bytes). Record ids and 2-character field ids retained; no guest data. The interface closes the link ~10 s after the resync, deterministically. |
| 2 — controlled live-dark deployment | **BLOCKED / PARTIAL** | Migration 0010 applied cleanly under the live-site gate (49→63 tables, 4/4 controlled functions, every object owned by `iam_v2_owner`, **0 rows**). The nft foundation installed surgically: +1 empty set, +1 forward rule, 2 DNAT rules extended with an always-true exemption, legacy `auth_ipv4` **byte-identical**, chain and DNAT counts unchanged, nothing flushed. **Then the next `netd` start deleted all of it.** |
| 3 — controlled reboot + post-reboot verification | **FAIL** (for the required persistence) | One reboot; boot id `b75b8683…` → `8aa6e7c7…`. Services converged with **0 failed units**, delivery binaries running, flags OFF, schema intact (ledger 1, 63 tables, 4 functions, 0 rows), legacy path healthy. But `phase3_auth_ipv4` was **absent**, and the `inet stayconnect` table was **structurally identical to the pre-install baseline (0 diff lines)** — the only ruleset changes anywhere were Docker container addresses outside our table. |
| 4 — documented rollback rehearsal | **PASS** functionally; **tooling defect found** | Foundation install→rollback returned the ruleset exactly to baseline. Migration 0010 down→up round-tripped (63→49→63, ledger 1→0→1, functions 4→0→4, ownership preserved). The binary rehearsal first reported a **FALSE PASS**: `cp` could not overwrite the running executables ("Text file busy"), and the check then read the still-running delivery build as proof of a successful rollback. Re-run with `install(1)`, it restored the exact pre-Increment-9 hashes and proved them in the running processes. |
| 5 — flags-OFF proof on the running unit | **PASS** | None of the six `STAYCONNECT_PHASE3_*` flags in any env file or unit; count 0 in all five running process environments; `netd` logs `phase3 shaping writer active=false`; no `pmsd` process and no `pmsd` unit; `phase3_auth_ipv4` 0 elements; iam_v2 0 rows across all 63 tables. |

**Root cause of items 2 and 3 — a single defect.** `applier.ReconcileActiveOnBoot` re-applied
`<bundle>/stayconnect.nft` from the active network revision. On this appliance that file is
`/etc/stayconnect/generated/network/revision-000056/stayconnect.nft`, rendered **2026-07-14 by the
pre-Phase-3 binary**: it contains `delete table inet stayconnect` and **zero** occurrences of
`phase3_auth_ipv4`. The database was correct and the bundle was intact; the structure was still wrong, because
the renderer version is the one input to ruleset structure that lives only in the running binary. Corrected in
this candidate — see rows 30ao–30as and `internal/nftconverge`.

## 6c. Live Increment-9 RE-VALIDATION (2026-08-11) — the blocked subset, on the corrected candidate

Executed against the same authorized appliance under a separate Product-Owner authorization, on candidate
`7318ac239b5bbbe5ca210fce94089d528e82306f`. Identity was re-proven first (appliance `ef78219b…`, serial
`SC-BEN1-JS4A-0D9C`, signed assignment `assigned`, licence `active` with `wan_mac` equal to the live `ens160`
MAC). Migration 0010 was **not** re-applied and live PMS Item 1 was **not** repeated.

| Item | Verdict | Evidence |
|---|---|---|
| First current-render convergence | **PASS** | one converge: `from_fp="" → aa3d99d1…`, `carried_elements=0`; `phase3_auth_ipv4` present and EMPTY; legacy digest unchanged |
| Steady-state `netd` restart — NO MUTATION | **PASS** (×3) | no new `converged` log line; recorded event `nft_changed: false`, `live_fp_before == desired_fp`; ruleset byte-identical |
| Reboot reconstruction | **PASS** | boots `4215e610…`, `14189f3e…`, `bad37249…` each rebuilt the confirmed revision `0cb0028c…` |
| Repeated reboot / idempotence | **PASS** | the `inet stayconnect` table is byte-identical across cycles; the only ruleset difference anywhere was Docker reordering two of its own rules |
| Unapplied Hotel-Admin draft isolation | **PASS** | draft never became runtime; no Apply/Confirm; no new revision; draft restored afterwards |
| Binary rollback identity verification | **PASS** | 5/5 verified on disk and in `/proc/<pid>/exe`; injected "Text file busy" was fatal |
| Corrected-candidate redeployment | **PASS** | 5/5 verified again after redeploy |
| Final DARK proof | **PASS** | flags OFF everywhere, `phase3_auth_ipv4` empty, 0 rows across 63 iam_v2 tables, no `pmsd` process/unit/socket at that time |
| Legacy live-session continuity | **NOT PROVEN — no real legacy guest available** | `auth_ipv4` held 0 elements at every checkpoint; nothing was fabricated |

Two findings were carried out of that run and are closed in this candidate: the previous release predates
`nftconverge` (row 32g), and the `pmsd` unit had never been installed (row 32h).

**NOT PROVEN — legacy live-session continuity.** Throughout the window there were **zero authorized guests and
zero active legacy sessions**: `auth_ipv4` was empty the entire time. Legacy parity is therefore proven
structurally (byte-identical set, unchanged rule and DNAT counts) and by real-kernel tests on a populated set,
but it was never exercised with a live guest on this appliance. No guest or session state was created to close
that gap.

## 7. Production and guest impact

**No guest was affected, and no financial or PMS state was changed.**

What is true at acceptance:

- **The appliance runs the accepted candidate** `7c8b8cf019c5af3dd2294ee268e8f7137e6ef5d4`, verified on disk and
  in `/proc/<pid>/exe` for all five runtime services, after a controlled reboot.
- **Migration 0010 is applied**; iam_v2 is dark at **63 tables / 0 rows** with 4 controlled functions and
  **zero** grants to any `svc_*` role. `pms_postings`, `posting_outbox` and `payment_transactions` are all 0.
- **The nft deployment/reboot architecture is `nftconverge`** (ADR-0003): `netd` reconciles the live ruleset
  against a fresh render of the running binary, executing nothing when it already matches. The manual surgical
  `phase3-foundation` install is **retired** from deployment and rollback and survives only as a read-only
  diagnostic. `phase3_auth_ipv4` is present and empty; legacy `auth_ipv4` is present and empty.
- **The DARK `pmsd` service is installed** under a dedicated least-privilege account, enabled, reboot-verified:
  it starts, finds every flag OFF, opens no PMS socket, performs no Phase-3 SQL, exits 0 and does not
  restart-loop.
- **Rollback across the pre-`nftconverge` boundary** is automatically permitted only when live legacy
  authorization is empty; a populated or unreadable set fails safe, changes nothing, and names the operator
  action. There is no force path.
- **The PMS was read, never written** — one authorized read-only FIAS session on 2026-08-10. No `PS`, no `PA`,
  no posting, no folio change.
- **No guest lost or gained access.** `auth_ipv4` was empty before, during and after every authorized window.
- **Phase 3 is DARK and NOT CUT OVER.** All six flags OFF; legacy public-schema IAM remains the sole production
  authority. PR #6 is merged and closed; merging changed the repository only and altered nothing on the
  appliance, in the database or at the PMS.

The appliance WAS deliberately contacted under Product-Owner authorization during Increment 9 and its
re-validation — that is recorded in §6b and §6c rather than denied here. The acceptance/closure round itself
required and performed **no appliance action at all**.

## 8. Rollback status

Two independent steps, both rehearsed on every change by the lifecycle gate:

- **Restore the previous release** (binaries + Hotel-Admin bundle). Usually sufficient — the schema is additive
  and inert while dark.
- **Remove the schema**: `0010_phase3_stay_resolution.down.sql` drops every table, trigger and controlled
  function the up script creates and removes its ledger row. The preflight asserts that coverage on every
  build, so a rollback cannot silently leave executable functions behind.

Full procedure and post-rollback confirmation queries: `docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md` §5.

## 9. Security and isolation results

- **Zero runtime privilege while dark**: every service role (`svc_scd`, `svc_edged`, `svc_portald`,
  `svc_acctd`, `svc_pmsd`) holds no `iam_v2` table or function privilege; gate-asserted.
- **Controlled-writer boundary**: a non-owner holding real table DML is refused raw status updates, forged
  history inserts, and direct grace-policy writes even with a correctly computed `config_version + 1`.
- **No guest PII in resolution evidence**: outcome code, guest-network id and a boolean `resolved` only —
  pinned by a test on the response type itself.
- **Guest portal is not an oracle**: every non-success is byte-identical, including HTTP status.
- **RBAC**: Phase-3 resources are gated in edged's authoritative matrix and mirrored in the UI hint matrix;
  read-only roles cannot publish policy, and unrelated roles cannot read the evidence at all.
- **Append-only histories**: entitlement transitions, device intervals, session bindings, watermarks, delayed
  samples and alert actions all reject `UPDATE`/`DELETE` except their one permitted mutation.

## 10. Complete generated changed-file manifest

> Embedded verbatim from `docs/manifests/Phase3-change-manifest.md` at delivery time. The evidence
> artifact's manifest-parity check confirms this equals the standalone generated manifest.

# Changed-file manifest (generated - do not hand-edit)

- **Base commit:** `ffb68e1ad325f5dd6d2096f2e30a782f8caef059`
- **HEAD commit:** `0019d62`
- **Provenance (generation HEAD = inventory_head):** `0019d62bf0360774b49f9278dbf202c1c8b0e328`  ·  path/status set covers the complete `base..delivery_head` diff (delivery_head = this staged content once committed).
- **Branch:** `phase/5-poststay-transfer`
- **Remote branch:** `origin/phase/5-poststay-transfer`
- **Changed files:** 473
- **Generated by:** `tools/generate-change-manifest.py ffb68e1ad325..STAGED`

## Files

| Path | Classification | Git status | Domain | Workstream | Rollback | Purpose (last commit subject in range) |
|---|---|---|---|---|---|---|
| `.github/workflows/phase3-software.yml` | CREATED | `A` | configuration | CI | rollback REMOVES it | Phase 3 Increment-9 correction (inventory_head): ruleset structure is reconciled from the current render, not a stored bundle |
| `.github/workflows/phase4-financial-core.yml` | CREATED | `A` | configuration | CI | rollback REMOVES it | WS-L preflight: reconcile the stale Phase-4 current-state claims and make the same staleness impossible to pass again (T0042) |
| `.github/workflows/phase5-post-stay-transfer.yml` | CREATED | `A` | configuration | CI | rollback REMOVES it | Phase 5 M4: authoritative CI, the derived least-privilege proof, and the DARK guard |
| `.github/workflows/project-governance.yml` | MODIFIED | `M` | configuration | CI | rollback RESTORES prior content | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `.gitignore` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 4: correct the C35 closure test, the C-matrix, the CI-head language and the project state (T0041) |
| `data-plane/cmd/acctd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/acctd/phase3.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/acctd/phase3_accounting.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/acctd/phase3_boundary_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/acctd/phase3_envelope.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/acctd/phase3_envelope_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/acctd/phase3_health_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/acctd/phase3_ingest_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/acctd/phase3_pass_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/acctd/phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/edged/auth.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `data-plane/cmd/edged/health_checks.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/edged/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `data-plane/cmd/edged/phase3_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4: financial-ops API contract tests, extended DARK and authoritative CI, Plan sync |
| `data-plane/cmd/edged/phase3_grace_contract_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 2 items 1-4 (inventory_head): mandatory grace package, ONE shared exact validator, typed package selector, mandatory DB-level preconditions: gate 320/320 |
| `data-plane/cmd/edged/phase3_interfaces_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/phase3_selector_contract_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/cmd/edged/phase4_finops_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `data-plane/cmd/edged/phase4_review_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `data-plane/cmd/edged/phase4_review_evidence.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: harden Manual Review audit inputs, and add payment/settlement governance (migration 0014) |
| `data-plane/cmd/edged/phase4_zeroattempt_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `data-plane/cmd/edged/phase5_poststay_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/cmd/edged/phase5_transfer_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `data-plane/cmd/edged/resources_phase3.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/cmd/edged/resources_phase3_interfaces.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/resources_phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `data-plane/cmd/edged/resources_phase4_finops.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the zero-attempt recovery operator path (DB read model, edged API, Hotel-Admin UI) and extend the DB gate to 0026 |
| `data-plane/cmd/edged/resources_phase4_review.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: harden Manual Review audit inputs, and add payment/settlement governance (migration 0014) |
| `data-plane/cmd/edged/resources_phase5_poststay.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/cmd/edged/resources_phase5_transfer.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M4 fix-forward: the CONTRACTUAL F9-i, and the two defects it found |
| `data-plane/cmd/netd/apply.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/cmd/netd/apply_ops.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/cmd/netd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `data-plane/cmd/netd/network_lifecycle_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/cmd/netd/nft_reconcile.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Increment-9 correction (inventory_head): ruleset structure is reconciled from the current render, not a stored bundle |
| `data-plane/cmd/netd/nft_reconcile_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/cmd/netd/phase3_bootid_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `data-plane/cmd/netd/phase3_classstate.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): write-ahead activation durability, monotonic security time, and true Zero-Stale state |
| `data-plane/cmd/netd/phase3_control.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_durability_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `data-plane/cmd/netd/phase3_enforcement.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): trusted boot identity, semantic security-journal validation, and a corrected T0019 timestamp |
| `data-plane/cmd/netd/phase3_enforcement_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): hard-boundary lease precision, durable activation bound, and a Zero-Stale check that had never run |
| `data-plane/cmd/netd/phase3_gate.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/netd/phase3_journal.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `data-plane/cmd/netd/phase3_lease.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): hard-boundary lease precision, durable activation bound, and a Zero-Stale check that had never run |
| `data-plane/cmd/netd/phase3_lease_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): write-ahead activation durability, monotonic security time, and true Zero-Stale state |
| `data-plane/cmd/netd/phase3_mode.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_mode_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_origin.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/netd/phase3_peer_linux.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_peer_other.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_provision.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): trusted boot identity, semantic security-journal validation, and a corrected T0019 timestamp |
| `data-plane/cmd/netd/phase3_provision_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: make packet authorization, accountable metering and Session state one enforcement contract |
| `data-plane/cmd/netd/phase3_securitytime.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `data-plane/cmd/netd/phase3_shaping.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): write-ahead activation durability, monotonic security time, and true Zero-Stale state |
| `data-plane/cmd/netd/phase3_shaping_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `data-plane/cmd/netd/store.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/cmd/phase3-foundation/main.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/pmsd/main.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/portald/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/cmd/portald/phase5_poststay_handlers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 fix-forward: the PUBLIC guest surface refuses an identity it did not derive, plus the M2 evidence record |
| `data-plane/cmd/portald/phase5_poststay_handlers_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M2 fix-forward: the PUBLIC guest surface refuses an identity it did not derive, plus the M2 evidence record |
| `data-plane/cmd/portald/pms_phase3.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side |
| `data-plane/cmd/portald/pms_phase3_budget.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/portald/pms_phase3_budget_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_budget_timing_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/portald/pms_phase3_handlers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_handlers_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side |
| `data-plane/cmd/portald/social_handlers.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side |
| `data-plane/cmd/portald/templates.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M4 fix: the post-stay tab must not displace the room form |
| `data-plane/cmd/scd/compliance_archive.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4 (0025): zero-attempt recovery retry, marker BEHIND, C27 cross-tenant merchant identity, C35 archive-before-purge |
| `data-plane/cmd/scd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/cmd/scd/otp_handlers.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/scd/phase3_activation_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/scd/phase3_atomicity_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: make packet authorization, accountable metering and Session state one enforcement contract |
| `data-plane/cmd/scd/phase3_auth.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/scd/phase3_auth_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/scd/phase3_auth_timing_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/cmd/scd/phase3_offers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/scd/phase5_poststay.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/cmd/scd/tenant_transition.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model |
| `data-plane/internal/assignment/registry_store.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/assignment/registry_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/authctx/authctx.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M1: POST_STAY_PIN auth contexts, reconciled rather than reused (+ 0027 lifecycle proof) |
| `data-plane/internal/authctx/authctx_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (inventory_head): PG16-green + gate 157/157 |
| `data-plane/internal/authctx/authctx_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Auth Context lock-order + evidence-version enforcement + UUID pin validation (inventory_head): PG16-green + lifecycle-gate 131/131 |
| `data-plane/internal/authctx/poststay_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/internal/checkout/checkout.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/checkout/checkout_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/checkout/f_flows_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/checkout/phase5_transfer_race_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M4 fix-forward: the CONTRACTUAL F9-i, and the two defects it found |
| `data-plane/internal/enforce/enforce.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/internal/enforce/enforce_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 netd shaping plan + acctd expiry enforcement (inventory_head): derived plan, true-time window/quota endings with revocation: PG16-green |
| `data-plane/internal/grace/grace.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green |
| `data-plane/internal/grace/grace_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green |
| `data-plane/internal/hwid/hwid.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/iamv2/commerce_domain.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/iamv2/commerce_engine.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/iamv2/commerce_flow.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/iamv2/commerce_pms_eligibility_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/iamv2/commerce_repo_pg.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/iamv2/commerce_settled_grant.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4 (0021): restricted-role trust boundary - high-level operations only, low-level definer primitives revoked |
| `data-plane/internal/iamv2/commerce_validate.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/iamv2/phase5_config.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/internal/iamv2/phase5_config_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/internal/iamv2/pms_config.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit |
| `data-plane/internal/iamv2/pms_config_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit |
| `data-plane/internal/iamv2/repo_pg.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/identity/identity.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/kerneltest/converge_kernel_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): normalise the kernel ruleset comparison and re-anchor the mutation fixtures |
| `data-plane/internal/kerneltest/kernel_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): hard-boundary lease precision, durable activation bound, and a Zero-Stale check that had never run |
| `data-plane/internal/kerneltest/rollback_boundary_kernel_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed |
| `data-plane/internal/livez/livez.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/internal/localkeys/localkeys.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 CI-stability: fix internal/localkeys.EnsureGeneration concurrent mid-write flake (inventory_head) |
| `data-plane/internal/metrics/metrics.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/netcfg/render_nft.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Increment-9 correction (inventory_head): ruleset structure is reconciled from the current render, not a stored bundle |
| `data-plane/internal/netcfg/render_nft_marker_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Increment-9 correction (inventory_head): ruleset structure is reconciled from the current render, not a stored bundle |
| `data-plane/internal/nft/nft.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 pre-live (inventory_head): hard-boundary lease precision, durable activation bound, and a Zero-Stale check that had never run |
| `data-plane/internal/nft/nft_phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 pre-live (inventory_head): hard-boundary lease precision, durable activation bound, and a Zero-Stale check that had never run |
| `data-plane/internal/nft/parse.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 pre-live safety (inventory_head): permanent concat elements were invisible to the nft parser |
| `data-plane/internal/nftconverge/converge.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/internal/nftfoundation/foundation.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/internal/nftfoundation/foundation_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `data-plane/internal/notifyloader/loader.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/payment/engine.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/payment/export_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/payment/granter.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4 (0021): restricted-role trust boundary - high-level operations only, low-level definer primitives revoked |
| `data-plane/internal/payment/health.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `data-plane/internal/payment/payment.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/payment/pg_c35_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model |
| `data-plane/internal/payment/pg_closure_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4: correct the C35 closure test, the C-matrix, the CI-head language and the project state (T0041) |
| `data-plane/internal/payment/pg_definer_abuse_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/payment/pg_grant_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0021): restricted-role trust boundary - high-level operations only, low-level definer primitives revoked |
| `data-plane/internal/payment/pg_health_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4: unambiguous CI gate runner, marker-driven reconciliation wiring, reviewed definer surface |
| `data-plane/internal/payment/pg_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/payment/pg_recovery_closure_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0022): recovery closure - structural full-rail hold, rail-specific reconciliation, verified release, legacy identity provenance |
| `data-plane/internal/payment/pg_recovery_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0022): recovery closure - structural full-rail hold, rail-specific reconciliation, verified release, legacy identity provenance |
| `data-plane/internal/payment/pg_restricted_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/internal/payment/provider.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: close the payment backend boundary (0018) â€” resolved financial identity, trusted notification boundary, NOT_SENT preflight, operational least privilege |
| `data-plane/internal/payment/recovery.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4 (0023): supported restore-generation model, management marker, signed-manifest restore tool and a real pg_restore drill |
| `data-plane/internal/payment/testdouble.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: close the payment backend boundary (0018) â€” resolved financial identity, trusted notification boundary, NOT_SENT preflight, operational least privilege |
| `data-plane/internal/pms/apaleo_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/fias_wire.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green |
| `data-plane/internal/pms/mews.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/mews_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/pms.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/protel_fias.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/stub.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/adapter_fias.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/adapter_fias_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/adapter_frames_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 CI-stability (inventory_head): align Â§F write-failure + malformed-domain tests with the Â§G initial-DR flow |
| `data-plane/internal/pmsd/applier.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 correction item 1 (inventory_head): pmsd Stay-Event application worker composition root + process-level tests |
| `data-plane/internal/pmsd/applier_supervisor.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/pmsd/applier_supervisor_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/pmsd/applier_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 2 items 5-6 (inventory_head): synchronous fail-closed applier construction + supervised interface reconciliation |
| `data-plane/internal/pmsd/assignment.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/barrier_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/errcodes.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/errcodes_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/events.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/fias_adapter.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK |
| `data-plane/internal/pmsd/lockkey.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green |
| `data-plane/internal/pmsd/lockkey_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green |
| `data-plane/internal/pmsd/pg.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/pmsd/pg_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green |
| `data-plane/internal/pmsd/pmsd.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 2 items 5-6 (inventory_head): synchronous fail-closed applier construction + supervised interface reconciliation |
| `data-plane/internal/pmsd/pmsd_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green |
| `data-plane/internal/pmsd/queue.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green |
| `data-plane/internal/pmsd/queue_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (inventory_head): fix benign measurement race in linearizable-close test |
| `data-plane/internal/pmsd/secret.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/internal/pmsd/secret_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9-Â§16 COMPLETE: owner-bound AES-GCM AAD (inventory_head); connector hardening finished, race-green |
| `data-plane/internal/pmsd/strict_parse.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green |
| `data-plane/internal/pmsd/strict_parse_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green |
| `data-plane/internal/pmsd/supervisor.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/worker.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green |
| `data-plane/internal/pmsd/writer.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green |
| `data-plane/internal/pmsd/writer_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green |
| `data-plane/internal/pmsguard/guard.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/pmsresolve/fanout.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/pmsresolve/fanout_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 strict resolver fan-out + idempotent auth_resolutions (inventory_head): complete-vector concurrency, fail-closed indeterminacy, >=24 concurrent resolutions: PG16-green |
| `data-plane/internal/pmsresolve/resolve.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11 |
| `data-plane/internal/pmsresolve/resolve_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11 |
| `data-plane/internal/posting/config.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: financial execution core â€” migration 0011, Posting domain, P# allocator, outbox lanes, PS/PA, UNKNOWN |
| `data-plane/internal/posting/engine.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: close the construction boundary for real, and deliver the Manual Review operator surface |
| `data-plane/internal/posting/errors.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/internal/posting/evidence.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/internal/posting/export_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4: close the construction boundary for real, and deliver the Manual Review operator surface |
| `data-plane/internal/posting/fias.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/internal/posting/gate.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/internal/posting/pg_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4: close the construction boundary for real, and deliver the Manual Review operator surface |
| `data-plane/internal/posting/posting_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/internal/posting/repo.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: close the production construction boundary and correct the reversal model to the FINAL contract |
| `data-plane/internal/posting/transport.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: close the construction boundary for real, and deliver the Manual Review operator surface |
| `data-plane/internal/poststay/convert.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/internal/poststay/lineage.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/internal/poststay/poststay.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/internal/poststay/poststay_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/internal/poststay/uniformity_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `data-plane/internal/shape/shape.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: make packet authorization, accountable metering and Session state one enforcement contract |
| `data-plane/internal/shape/shape_staged_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: make packet authorization, accountable metering and Session state one enforcement contract |
| `data-plane/internal/shapeplan/plan.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/internal/sms/twilio.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/social/google.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/stayengine/checkout_slice_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M4 fix-forward: the CONTRACTUAL F9-i, and the two defects it found |
| `data-plane/internal/stayengine/pg.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/stayengine/pg_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M4 fix-forward: the CONTRACTUAL F9-i, and the two defects it found |
| `data-plane/internal/stayengine/resolve.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green |
| `data-plane/internal/stayengine/resolve_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 Increment 4 foundation (inventory_head): deterministic Stay-resolution decision core (internal/stayengine) |
| `data-plane/internal/stayengine/sharers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green |
| `data-plane/internal/staygrant/staygrant.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/staygrant/staygrant_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 atomic Auth-Context/Quote/Purchase/Entitlement grant + controlled device authorization (inventory_head): PG16-green + gate 267/267 |
| `data-plane/internal/throttle/throttle.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/internal/transfer/transfer.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M4 fix-forward: the CONTRACTUAL F9-i, and the two defects it found |
| `data-plane/internal/transfer/transfer_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `data-plane/internal/writerguard/writerguard.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/internal/writerguard/writerguard_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/migrations/0010_phase3_stay_resolution.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 3: make packet authorization, accountable metering and Session state one enforcement contract |
| `data-plane/migrations/0010_phase3_stay_resolution.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `data-plane/migrations/0011_phase4_financial_execution.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: financial execution core â€” migration 0011, Posting domain, P# allocator, outbox lanes, PS/PA, UNKNOWN |
| `data-plane/migrations/0011_phase4_financial_execution.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: financial execution core â€” migration 0011, Posting domain, P# allocator, outbox lanes, PS/PA, UNKNOWN |
| `data-plane/migrations/0012_phase4_financial_hardening.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/migrations/0012_phase4_financial_hardening.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `data-plane/migrations/0013_phase4_reversal_ledger.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: close the production construction boundary and correct the reversal model to the FINAL contract |
| `data-plane/migrations/0013_phase4_reversal_ledger.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: close the production construction boundary and correct the reversal model to the FINAL contract |
| `data-plane/migrations/0014_phase4_payment_settlement.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: harden Manual Review audit inputs, and add payment/settlement governance (migration 0014) |
| `data-plane/migrations/0014_phase4_payment_settlement.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: harden Manual Review audit inputs, and add payment/settlement governance (migration 0014) |
| `data-plane/migrations/0015_phase4_payment_hardening.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: migration 0015 â€” 0014's payment bounds were not concurrency-safe |
| `data-plane/migrations/0015_phase4_payment_hardening.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: migration 0015 â€” 0014's payment bounds were not concurrency-safe |
| `data-plane/migrations/0016_phase4_payment_coherence.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles |
| `data-plane/migrations/0016_phase4_payment_coherence.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles |
| `data-plane/migrations/0017_phase4_least_privilege.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles |
| `data-plane/migrations/0017_phase4_least_privilege.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles |
| `data-plane/migrations/0018_phase4_financial_identity_and_privilege.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: close the payment backend boundary (0018) â€” resolved financial identity, trusted notification boundary, NOT_SENT preflight, operational least privilege |
| `data-plane/migrations/0018_phase4_financial_identity_and_privilege.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: close the payment backend boundary (0018) â€” resolved financial identity, trusted notification boundary, NOT_SENT preflight, operational least privilege |
| `data-plane/migrations/0019_phase4_financial_recovery.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `data-plane/migrations/0019_phase4_financial_recovery.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `data-plane/migrations/0020_phase4_financial_observability.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `data-plane/migrations/0020_phase4_financial_observability.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `data-plane/migrations/0021_phase4_trust_boundary.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0021): restricted-role trust boundary - high-level operations only, low-level definer primitives revoked |
| `data-plane/migrations/0021_phase4_trust_boundary.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: unambiguous CI gate runner, marker-driven reconciliation wiring, reviewed definer surface |
| `data-plane/migrations/0022_phase4_recovery_closure.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0022): recovery closure - structural full-rail hold, rail-specific reconciliation, verified release, legacy identity provenance |
| `data-plane/migrations/0022_phase4_recovery_closure.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: extend the DB gate to 0019-0023, make the provenance backfill idempotent |
| `data-plane/migrations/0023_phase4_restore_generation.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0023): supported restore-generation model, management marker, signed-manifest restore tool and a real pg_restore drill |
| `data-plane/migrations/0023_phase4_restore_generation.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4: unambiguous CI gate runner, marker-driven reconciliation wiring, reviewed definer surface |
| `data-plane/migrations/0024_phase4_outcome_authority_and_grant_kernel.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/migrations/0024_phase4_outcome_authority_and_grant_kernel.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel |
| `data-plane/migrations/0025_phase4_recovery_completion_and_compliance.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0025): zero-attempt recovery retry, marker BEHIND, C27 cross-tenant merchant identity, C35 archive-before-purge |
| `data-plane/migrations/0025_phase4_recovery_completion_and_compliance.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0025): zero-attempt recovery retry, marker BEHIND, C27 cross-tenant merchant identity, C35 archive-before-purge |
| `data-plane/migrations/0026_phase4_c35_failclosed_and_operator_retry.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model |
| `data-plane/migrations/0026_phase4_c35_failclosed_and_operator_retry.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model |
| `data-plane/migrations/0027_phase5_poststay_and_transfer.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `data-plane/migrations/0027_phase5_poststay_and_transfer.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `data-plane/migrations/0028_phase5_poststay_throttle_method.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/migrations/0028_phase5_poststay_throttle_method.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/migrations/0029_phase5_reveal_is_at_mint.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `data-plane/migrations/0029_phase5_reveal_is_at_mint.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `deploy/caddy/Caddyfile` | DELETED | `D` | configuration | DEPLOY | rollback RESTORES it | Close five residual findings: cloned host identity, Central-only Edge vhosts, artifact-audit record (T0027) |
| `deploy/caddy/Caddyfile.central` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Close five residual findings: cloned host identity, Central-only Edge vhosts, artifact-audit record (T0027) |
| `deploy/caddy/Caddyfile.edge` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Edge 404 block: drop the per-site log file that made every Caddy start fail |
| `deploy/caddy/README.md` | MODIFIED | `M` | configuration | DEPLOY | rollback RESTORES prior content | Edge 404 block: drop the per-site log file that made every Caddy start fail |
| `deploy/caddy/stayconnect-caddy.central.service` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Ratify T0027 state, renew the appliance licence to 2027-08-08, make the Central reload contract honest (T0028) |
| `deploy/caddy/stayconnect-caddy.service -> deploy/caddy/stayconnect-caddy.edge.service` | RENAMED | `R069 (deploy/caddy/stayconnect-caddy.service -> deploy/caddy/stayconnect-caddy.edge.service)` | configuration | DEPLOY | rollback RESTORES prior content | Ratify T0027 state, renew the appliance licence to 2027-08-08, make the Central reload contract honest (T0028) |
| `deploy/env/pmsd.env.dark` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): the dark pmsd env must not even MENTION a Phase-3 flag name |
| `deploy/scripts/stayconnect-financial-restore.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 4: trusted restore manifest via the pinned registry anchor, proven quiesce, marker excluded from the /etc backup domain, runbook updated |
| `deploy/scripts/stayconnect-site-backup.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | WS-L: make the restore drill exercise the version check the appliance actually performs |
| `deploy/systemd/stayconnect-pmsd.service` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK |
| `docs/BACKUP_AND_RESTORE.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 4: trusted restore manifest via the pinned registry anchor, proven quiesce, marker excluded from the /etc backup domain, runbook updated |
| `docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 (inventory_head): ACCEPTED AND CLOSED at verified DARK maturity, with the audit chronology corrected |
| `docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `docs/PHASE4_DEPENDENCY_TRIAGE.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4: generate the lockfile on Linux so npm ci reproduces it on the CI runner |
| `docs/SYSTEM_OVERVIEW.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Ratify T0027 state, renew the appliance licence to 2027-08-08, make the Central reload contract honest (T0028) |
| `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/acceptance/StayConnect-IAM-Phase4-Live-Dark-Acceptance.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4 closure: record the merge of PR #12 into master (D20 / T0048) |
| `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): record the network-enforcement writers in the privilege inventory |
| `docs/architecture/Phase3-Privilege-Matrix.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/Phase4-Financial-Schema-Gap-Audit.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4 ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC (D19 / T0044) |
| `docs/architecture/Phase4-Readiness-Matrix.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Pre-Phase-4 baseline pass + Phase-4 authorization (D18 / T0029) |
| `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4 post-merge housekeeping: merge state is a fact, not prose (D20 / T0049) |
| `docs/architecture/StayConnect-IAM-Phase4-Plan.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4 post-merge housekeeping: merge state is a fact, not prose (D20 / T0049) |
| `docs/architecture/StayConnect-IAM-Phase5-Plan.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `docs/architecture/adr/ADR-0001-pmsd-connector-ownership.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/adr/ADR-0002-phase3-single-shaping-owner.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity |
| `docs/architecture/adr/ADR-0003-ruleset-structure-from-current-render.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `docs/context/StayConnect-IAM-Handoff.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `docs/evidence/Phase3-CI-Artifact-Exposure-Audit.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Close five residual findings: cloned host identity, Central-only Edge vhosts, artifact-audit record (T0027) |
| `docs/evidence/Phase3-Final-Live-Acceptance-Record.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Ratify T0027 state, renew the appliance licence to 2027-08-08, make the Central reload contract honest (T0028) |
| `docs/evidence/Phase4-Final-Live-Acceptance-Record.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4 ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC (D19 / T0044) |
| `docs/evidence/StayConnect-IAM-Phase3-Schema-Gap-Audit.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `docs/evidence/StayConnect-IAM-Phase5-Evidence.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `docs/evidence/phase4/npm-audit-full.json` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `docs/evidence/phase4/npm-audit-production.json` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `docs/manifests/Phase3-change-manifest.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 5 (delivery_head): M3 pointer, complete staged manifest, rebuilt packs and report-embedded manifest |
| `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/reports/StayConnect-IAM-Phase3-Final-Report.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 5 (delivery_head): M3 pointer, complete staged manifest, rebuilt packs and report-embedded manifest |
| `docs/reports/StayConnect-IAM-Phase4-Final-Report.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 4 closure: record the merge of PR #12 into master (D20 / T0048) |
| `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase-evidence/GIT_STAT_0019d62.txt` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | (no commit subject in range) |
| `exports/chatgpt/phase-evidence/GIT_STAT_9a1f356.txt` | EXPORTED | `D` | export | EXPORT | rollback RESTORES it | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase-evidence/Phase2-change-manifest.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase-evidence/StayConnect-IAM-Phase2-Final-Report.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/governance/decision-register.json` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `exports/chatgpt/phase-evidence/tools/project-state.py` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 4 (delivery_head): correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest |
| `exports/chatgpt/phase-evidence/tools/validate-project-state.sh` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase1b-planning/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/phase1b-planning/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/00-START-HERE.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/Phase2-change-manifest.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/Phase3-Privilege-Matrix.md` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/Phase3-change-manifest.md` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/SYSTEM_OVERVIEW.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): T0028 manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase0-Contract.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1A-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Final-Report.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase3-Plan.md` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | Phase 4 post-merge housekeeping: merge state is a fact, not prose (D20 / T0049) |
| `governance/decision-register.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `governance/dependency-acceptances.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `governance/project-state.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `governance/transitions/T0015.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `governance/transitions/T0016.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `governance/transitions/T0017.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `governance/transitions/T0018.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 pre-live (delivery_head): T0018 + complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `governance/transitions/T0019.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 pre-live (inventory_head): trusted boot identity, semantic security-journal validation, and a corrected T0019 timestamp |
| `governance/transitions/T0020.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 Increment-9 correction (inventory_head): ruleset structure is reconciled from the current render, not a stored bundle |
| `governance/transitions/T0021.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback |
| `governance/transitions/T0022.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 (inventory_head): zero-stale truth sync â€” record current state as DATA, and make contradictions fail the gate |
| `governance/transitions/T0023.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed |
| `governance/transitions/T0024.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 (inventory_head): ACCEPTED AND CLOSED at verified DARK maturity, with the audit chronology corrected |
| `governance/transitions/T0025.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3 closure: record the merge of PR #6 into master (D17 / T0025) |
| `governance/transitions/T0026.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Post-closure hygiene: stop two surfaces presenting superseded state as current (T0026) |
| `governance/transitions/T0027.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Close five residual findings: cloned host identity, Central-only Edge vhosts, artifact-audit record (T0027) |
| `governance/transitions/T0028.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Ratify T0027 state, renew the appliance licence to 2027-08-08, make the Central reload contract honest (T0028) |
| `governance/transitions/T0029.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Pre-Phase-4 baseline pass + Phase-4 authorization (D18 / T0029) |
| `governance/transitions/T0030.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4: financial execution core â€” migration 0011, Posting domain, P# allocator, outbox lanes, PS/PA, UNKNOWN |
| `governance/transitions/T0031.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 hardening (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `governance/transitions/T0032.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): manifest + rebuilt packs + pointer |
| `governance/transitions/T0033.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): manifest + rebuilt packs + pointer |
| `governance/transitions/T0034.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): manifest + rebuilt packs + pointer |
| `governance/transitions/T0035.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): manifest + rebuilt packs + pointer |
| `governance/transitions/T0036.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): manifest + rebuilt packs + pointer |
| `governance/transitions/T0037.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): T0037 receipt + project-state pointers + regenerated manifest |
| `governance/transitions/T0038.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): T0038 receipt + project-state pointers |
| `governance/transitions/T0039.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): T0039 receipt + project-state pointers |
| `governance/transitions/T0040.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): T0040 receipt + project-state pointers |
| `governance/transitions/T0041.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 (delivery_head): T0041 receipt, complete staged manifest, rebuilt packs, pointer and report-embedded manifest |
| `governance/transitions/T0042.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | WS-L preflight: reconcile the stale Phase-4 current-state claims and make the same staleness impossible to pass again (T0042) |
| `governance/transitions/T0043.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | WS-L: record the live-DARK deployment result (T0043) and sync the current-state surfaces |
| `governance/transitions/T0044.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC (D19 / T0044) |
| `governance/transitions/T0045.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `governance/transitions/T0046.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 closure correction (T0046): static current-state prose outside the generated block |
| `governance/transitions/T0047.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 closure correction (T0047): the generated block is the only carrier of current state in 00-START-HERE |
| `governance/transitions/T0048.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 closure: record the merge of PR #12 into master (D20 / T0048) |
| `governance/transitions/T0049.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 4 post-merge housekeeping: merge state is a fact, not prose (D20 / T0049) |
| `governance/transitions/T0050.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `governance/transitions/T0051.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `hotel-admin/app/(app)/checkout-grace/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310 |
| `hotel-admin/app/(app)/financial-health/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `hotel-admin/app/(app)/financial-recovery/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `hotel-admin/app/(app)/financial-review/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the WS-J operator surface, dependency triage, restore drill and gate self-test in CI, Plan sync |
| `hotel-admin/app/(app)/financial-settlements/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the WS-J operator surface, dependency triage, restore drill and gate self-test in CI, Plan sync |
| `hotel-admin/app/(app)/operational-alerts/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310 |
| `hotel-admin/app/(app)/pms-interfaces/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/pms-resolutions/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/pms-routing/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/pms-source-conflicts/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/post-stay/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `hotel-admin/app/(app)/stay-events/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/app/(app)/stay-transfers/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `hotel-admin/app/(app)/stays/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/components/nav.tsx` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `hotel-admin/components/phase3/checkout-grace-form.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `hotel-admin/components/phase3/operational-alerts-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310 |
| `hotel-admin/components/phase4/financial-health-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface |
| `hotel-admin/components/phase4/financial-recovery-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the zero-attempt recovery operator path (DB read model, edged API, Hotel-Admin UI) and extend the DB gate to 0026 |
| `hotel-admin/components/phase4/manual-review-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the WS-J operator surface, dependency triage, restore drill and gate self-test in CI, Plan sync |
| `hotel-admin/components/phase4/settlements-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the WS-J operator surface, dependency triage, restore drill and gate self-test in CI, Plan sync |
| `hotel-admin/components/phase5/post-stay-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `hotel-admin/components/phase5/stay-transfer-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `hotel-admin/e2e/auth-middleware.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `hotel-admin/e2e/phase3-guest-portal-resilience.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `hotel-admin/e2e/phase3-guest-portal.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `hotel-admin/e2e/phase3-pms-interfaces.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/e2e/phase3-stays-grace.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `hotel-admin/e2e/phase4-financial-operator.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the zero-attempt recovery operator path (DB read model, edged API, Hotel-Admin UI) and extend the DB gate to 0026 |
| `hotel-admin/e2e/phase5-guest-post-stay.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 fix-forward: the PUBLIC guest surface refuses an identity it did not derive, plus the M2 evidence record |
| `hotel-admin/e2e/phase5-post-stay.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `hotel-admin/e2e/phase5-stay-transfer.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `hotel-admin/lib/api.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4: complete the zero-attempt recovery operator path (DB read model, edged API, Hotel-Admin UI) and extend the DB gate to 0026 |
| `hotel-admin/lib/roles.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred |
| `hotel-admin/next.config.mjs` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `hotel-admin/package-lock.json` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4: generate the lockfile on Linux so npm ci reproduces it on the CI runner |
| `hotel-admin/package.json` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `hotel-admin/playwright.config.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof |
| `hotel-admin/test/nav.test.tsx` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/test/phase3-interface-pages.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/test/phase3-pages.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `hotel-admin/test/phase4-financial-pages.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the zero-attempt recovery operator path (DB read model, edged API, Hotel-Admin UI) and extend the DB gate to 0026 |
| `hotel-admin/test/phase4-review-settlements.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 4: complete the WS-J operator surface, dependency triage, restore drill and gate self-test in CI, Plan sync |
| `iam_v2_scratch/00_platform_fixture.sql` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `iam_v2_scratch/phase3_0010_lifecycle.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Increment-9 correction (inventory_head): re-anchor the mutation fixtures and give the --apply-role test role the schema it migrates |
| `iam_v2_scratch/phase4_0011_financial.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | WS-L: prove the Phase-4 chain rolls back and re-applies to the SAME SCHEMA STRUCTURE |
| `iam_v2_scratch/phase4_db_invariants.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation |
| `iam_v2_scratch/phase4_financial_fixture.sql` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: close the payment backend boundary (0018) â€” resolved financial identity, trusted notification boundary, NOT_SENT preflight, operational least privilege |
| `iam_v2_scratch/phase4_least_privilege.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles |
| `iam_v2_scratch/phase4_payment_concurrency.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles |
| `iam_v2_scratch/phase5_0027_foundation.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 5 M4: authoritative CI, the derived least-privilege proof, and the DARK guard |
| `iam_v2_scratch/phase5_0027_lifecycle.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence |
| `iam_v2_scratch/phase5_least_privilege.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 5 M4: authoritative CI, the derived least-privilege proof, and the DARK guard |
| `iam_v2_scratch/schema_structure_fingerprint.sql` | CREATED | `A` | other | OTHER | rollback REMOVES it | WS-L: prove the Phase-4 chain rolls back and re-applies to the SAME SCHEMA STRUCTURE |
| `iam_v2_scratch/seed.sql` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Close the reconciliation checkpoint: authorization model, fixture repair, behavioural DB proof |
| `scripts/binary-rollback.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed |
| `scripts/ci/binary-rollback-tests.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed |
| `scripts/ci/dependency-judgement.py` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `scripts/ci/go-test-counted.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/gofmt-check.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate |
| `scripts/ci/gojson_summary.py` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/kernel-netns-suite.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed |
| `scripts/ci/pg-gate.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/phase3_evidence.py` | CREATED | `A` | other | OTHER | rollback REMOVES it | Evidence hygiene: sanitise EVERY metadata copy, not the one that was easiest to find |
| `scripts/ci/phase4-dark-check.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: financial-ops API contract tests, extended DARK and authoritative CI, Plan sync |
| `scripts/ci/phase4-dependency-gate-selftest.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `scripts/ci/phase4-dependency-gate.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk |
| `scripts/ci/phase4_evidence.py` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4: financial execution core â€” migration 0011, Posting domain, P# allocator, outbox lanes, PS/PA, UNKNOWN |
| `scripts/ci/step.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/edge-migrate.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Increment-9 correction (inventory_head): keep the ledger-owner refusal message assertable, and gate the new acceptance path |
| `scripts/install-pmsd-dark.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed |
| `scripts/phase14-tls-test.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Close five residual findings: cloned host identity, Central-only Edge vhosts, artifact-audit record (T0027) |
| `scripts/phase3-evidence.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: downloadable evidence artifact with a SHA-256 integrity manifest |
| `scripts/phase3-preflight.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 DARK acceptance (inventory_head): the dark pmsd env must not even MENTION a Phase-3 flag name |
| `scripts/phase4-least-privilege.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model |
| `scripts/phase4-payment-concurrency.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model |
| `scripts/phase4-pg-integration.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `scripts/phase4-restore-drill.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | WS-L: make the restore drill exercise the version check the appliance actually performs |
| `scripts/phase5-dark-guard.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 5 M4: authoritative CI, the derived least-privilege proof, and the DARK guard |
| `scripts/phase5-iam-live-dark-deploy.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `scripts/phase5-pg-integration.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 5 M4: authoritative CI, the derived least-privilege proof, and the DARK guard |
| `scripts/pmsd-pg-integration.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `tools/embed-report-manifest.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `tools/generate-change-manifest.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 4: point the authoritative CI facts at the current head |
| `tools/project-state.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 4 closure correction: rule D must scan every phase maturity, not only 1B's |
| `tools/tests/current_state_parity/run_negative.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 4 post-merge housekeeping: merge state is a fact, not prose (D20 / T0049) |
| `tools/tests/evidence_artifact/run_artifact_staleness.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Evidence hygiene: sanitise EVERY metadata copy, not the one that was easiest to find |
| `tools/tests/project_state_validator/run_isolation_regression.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them |
| `tools/tests/project_state_validator/run_mutations.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 4 closure correction: convert the last whitespace-pinned mutation fixture to json_set |
| `tools/tests/tooling/run_control_chars.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Correct the Phase-4 current-state contradiction, and repair two silently-broken validator regexes |
| `tools/validate-current-state-parity.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027) |
| `tools/validate-pr-metadata.sh` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 4 closure correction: look the PR up by NUMBER, because GITHUB_REF_NAME is the merge ref |
| `tools/validate-project-state.sh` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 5 M4: the LIVE-DARK acceptance candidate (T0051) |
| `tools/validate-state-parity-selftest.sh` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | WS-L preflight: reconcile the stale Phase-4 current-state claims and make the same staleness impossible to pass again (T0042) |
| `tools/validate-state-parity.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | WS-L preflight: reconcile the stale Phase-4 current-state claims and make the same staleness impossible to pass again (T0042) |
| `tools/validate-transition-times.sh` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 3 pre-live (inventory_head): trusted boot identity, semantic security-journal validation, and a corrected T0019 timestamp |

## Total diff statistics (`git diff --stat`)
```text
 .github/workflows/phase3-software.yml              |  271 ++
 .github/workflows/phase4-financial-core.yml        |  232 ++
 .github/workflows/phase5-post-stay-transfer.yml    |  105 +
 .github/workflows/project-governance.yml           |   67 +-
 .gitignore                                         |   17 +
 data-plane/cmd/acctd/main.go                       |   69 +-
 data-plane/cmd/acctd/phase3.go                     |  374 +++
 data-plane/cmd/acctd/phase3_accounting.go          |  231 ++
 .../cmd/acctd/phase3_boundary_integration_test.go  |  843 ++++++
 data-plane/cmd/acctd/phase3_envelope.go            |   92 +
 data-plane/cmd/acctd/phase3_envelope_test.go       |  176 ++
 data-plane/cmd/acctd/phase3_health_test.go         |  110 +
 .../cmd/acctd/phase3_ingest_integration_test.go    |  163 +
 .../cmd/acctd/phase3_pass_integration_test.go      |  405 +++
 data-plane/cmd/acctd/phase3_test.go                |  129 +
 data-plane/cmd/edged/auth.go                       |   60 +-
 data-plane/cmd/edged/health_checks.go              |    6 +
 data-plane/cmd/edged/main.go                       |   81 +
 .../cmd/edged/phase3_api_integration_test.go       |  472 +++
 data-plane/cmd/edged/phase3_grace_contract_test.go |  219 ++
 .../phase3_interfaces_api_integration_test.go      |  580 ++++
 .../cmd/edged/phase3_selector_contract_test.go     |  211 ++
 .../edged/phase4_finops_api_integration_test.go    |  219 ++
 .../edged/phase4_review_api_integration_test.go    |  656 ++++
 data-plane/cmd/edged/phase4_review_evidence.go     |  193 ++
 .../phase4_zeroattempt_api_integration_test.go     |  488 +++
 .../edged/phase5_poststay_api_integration_test.go  |  306 ++
 .../edged/phase5_transfer_api_integration_test.go  |  281 ++
 data-plane/cmd/edged/resources_phase3.go           |  598 ++++
 .../cmd/edged/resources_phase3_interfaces.go       |  742 +++++
 data-plane/cmd/edged/resources_phase3_test.go      |   74 +
 data-plane/cmd/edged/resources_phase4_finops.go    |  471 +++
 data-plane/cmd/edged/resources_phase4_review.go    |  458 +++
 data-plane/cmd/edged/resources_phase5_poststay.go  |  236 ++
 data-plane/cmd/edged/resources_phase5_transfer.go  |  252 ++
 data-plane/cmd/netd/apply.go                       |   68 +
 data-plane/cmd/netd/apply_ops.go                   |  144 +-
 data-plane/cmd/netd/main.go                        |  100 +-
 data-plane/cmd/netd/network_lifecycle_test.go      |  304 ++
 data-plane/cmd/netd/nft_reconcile.go               |   51 +
 data-plane/cmd/netd/nft_reconcile_test.go          |  630 ++++
 data-plane/cmd/netd/phase3_bootid_test.go          |  443 +++
 data-plane/cmd/netd/phase3_classstate.go           |  299 ++
 data-plane/cmd/netd/phase3_control.go              |  141 +
 data-plane/cmd/netd/phase3_durability_test.go      |  549 ++++
 data-plane/cmd/netd/phase3_enforcement.go          |  262 ++
 data-plane/cmd/netd/phase3_enforcement_test.go     |  863 ++++++
 data-plane/cmd/netd/phase3_gate.go                 |  167 ++
 data-plane/cmd/netd/phase3_journal.go              |  481 +++
 data-plane/cmd/netd/phase3_lease.go                |  142 +
 data-plane/cmd/netd/phase3_lease_test.go           |  620 ++++
 data-plane/cmd/netd/phase3_mode.go                 |   67 +
 data-plane/cmd/netd/phase3_mode_test.go            |   75 +
 data-plane/cmd/netd/phase3_origin.go               |   93 +
 data-plane/cmd/netd/phase3_peer_linux.go           |   37 +
 data-plane/cmd/netd/phase3_peer_other.go           |   16 +
 data-plane/cmd/netd/phase3_provision.go            |  455 +++
 data-plane/cmd/netd/phase3_provision_test.go       |  481 +++
 data-plane/cmd/netd/phase3_securitytime.go         |  190 ++
 data-plane/cmd/netd/phase3_shaping.go              |  659 ++++
 data-plane/cmd/netd/phase3_shaping_test.go         | 1371 +++++++++
 data-plane/cmd/netd/store.go                       |   72 +
 data-plane/cmd/phase3-foundation/main.go           |   71 +
 data-plane/cmd/pmsd/main.go                        |  139 +
 data-plane/cmd/portald/main.go                     |   14 +
 data-plane/cmd/portald/phase5_poststay_handlers.go |  175 ++
 .../cmd/portald/phase5_poststay_handlers_test.go   |  147 +
 data-plane/cmd/portald/pms_phase3.go               |  101 +
 data-plane/cmd/portald/pms_phase3_budget.go        |  140 +
 data-plane/cmd/portald/pms_phase3_budget_test.go   |  319 ++
 .../cmd/portald/pms_phase3_budget_timing_test.go   |   65 +
 data-plane/cmd/portald/pms_phase3_handlers.go      |  231 ++
 data-plane/cmd/portald/pms_phase3_handlers_test.go |  228 ++
 data-plane/cmd/portald/pms_phase3_test.go          |  126 +
 data-plane/cmd/portald/social_handlers.go          |    4 +-
 data-plane/cmd/portald/templates.go                |  195 +-
 data-plane/cmd/scd/compliance_archive.go           |  156 +
 data-plane/cmd/scd/main.go                         |   44 +
 data-plane/cmd/scd/otp_handlers.go                 |   10 +
 .../cmd/scd/phase3_activation_integration_test.go  |  241 ++
 .../cmd/scd/phase3_atomicity_integration_test.go   |  357 +++
 data-plane/cmd/scd/phase3_auth.go                  |  705 +++++
 data-plane/cmd/scd/phase3_auth_integration_test.go |  832 +++++
 data-plane/cmd/scd/phase3_auth_timing_test.go      |   98 +
 data-plane/cmd/scd/phase3_offers.go                |  227 ++
 data-plane/cmd/scd/phase5_poststay.go              |  234 ++
 data-plane/cmd/scd/tenant_transition.go            |   43 +
 data-plane/internal/assignment/registry_store.go   |    8 +-
 data-plane/internal/assignment/registry_test.go    |    7 +-
 data-plane/internal/authctx/authctx.go             |  440 +++
 .../internal/authctx/authctx_integration_test.go   |  726 +++++
 data-plane/internal/authctx/authctx_test.go        |   90 +
 .../internal/authctx/poststay_integration_test.go  |  266 ++
 data-plane/internal/checkout/checkout.go           |  791 +++++
 .../internal/checkout/checkout_integration_test.go |  959 ++++++
 .../internal/checkout/f_flows_integration_test.go  |  174 ++
 .../phase5_transfer_race_integration_test.go       |  375 +++
 data-plane/internal/enforce/enforce.go             |  207 ++
 .../internal/enforce/enforce_integration_test.go   |  279 ++
 data-plane/internal/grace/grace.go                 |  159 +
 data-plane/internal/grace/grace_test.go            |  126 +
 data-plane/internal/hwid/hwid.go                   |    4 +-
 data-plane/internal/iamv2/commerce_domain.go       |  197 +-
 data-plane/internal/iamv2/commerce_engine.go       |    9 +
 data-plane/internal/iamv2/commerce_flow.go         |   37 +-
 .../iamv2/commerce_pms_eligibility_test.go         |  179 ++
 data-plane/internal/iamv2/commerce_repo_pg.go      |  151 +-
 .../internal/iamv2/commerce_settled_grant.go       |   64 +
 data-plane/internal/iamv2/commerce_validate.go     |   67 +-
 data-plane/internal/iamv2/phase5_config.go         |  108 +
 data-plane/internal/iamv2/phase5_config_test.go    |   46 +
 data-plane/internal/iamv2/pms_config.go            |  120 +
 data-plane/internal/iamv2/pms_config_test.go       |  157 +
 data-plane/internal/iamv2/repo_pg.go               |    6 +
 data-plane/internal/identity/identity.go           |   12 +-
 .../internal/kerneltest/converge_kernel_test.go    |  353 +++
 data-plane/internal/kerneltest/kernel_test.go      |  705 +++++
 .../kerneltest/rollback_boundary_kernel_test.go    |  231 ++
 data-plane/internal/livez/livez.go                 |   24 +
 data-plane/internal/localkeys/localkeys.go         |   38 +-
 data-plane/internal/metrics/metrics.go             |   64 +-
 data-plane/internal/netcfg/render_nft.go           |   89 +-
 .../internal/netcfg/render_nft_marker_test.go      |  144 +
 data-plane/internal/nft/nft.go                     |  236 +-
 data-plane/internal/nft/nft_phase3_test.go         |  455 +++
 data-plane/internal/nft/parse.go                   |   29 +-
 data-plane/internal/nftconverge/converge.go        |  309 ++
 data-plane/internal/nftfoundation/foundation.go    |  458 +++
 .../internal/nftfoundation/foundation_test.go      |  585 ++++
 data-plane/internal/notifyloader/loader.go         |    8 +-
 data-plane/internal/payment/engine.go              |  544 ++++
 data-plane/internal/payment/export_test.go         |   27 +
 data-plane/internal/payment/granter.go             |   43 +
 data-plane/internal/payment/health.go              |  256 ++
 data-plane/internal/payment/payment.go             |  232 ++
 .../internal/payment/pg_c35_integration_test.go    |  185 ++
 .../payment/pg_closure_integration_test.go         |  370 +++
 .../payment/pg_definer_abuse_integration_test.go   |  458 +++
 .../internal/payment/pg_grant_integration_test.go  |  219 ++
 .../internal/payment/pg_health_integration_test.go |  128 +
 data-plane/internal/payment/pg_integration_test.go |  620 ++++
 .../pg_recovery_closure_integration_test.go        |  384 +++
 .../payment/pg_recovery_integration_test.go        |  285 ++
 .../payment/pg_restricted_integration_test.go      |  202 ++
 data-plane/internal/payment/provider.go            |   93 +
 data-plane/internal/payment/recovery.go            |  235 ++
 data-plane/internal/payment/testdouble.go          |  161 +
 data-plane/internal/pms/apaleo_test.go             |   25 +-
 data-plane/internal/pms/fias_wire.go               |   73 +
 data-plane/internal/pms/mews.go                    |    6 +-
 data-plane/internal/pms/mews_test.go               |   23 +-
 data-plane/internal/pms/pms.go                     |   49 +-
 data-plane/internal/pms/protel_fias.go             |   12 +-
 data-plane/internal/pms/stub.go                    |    8 +-
 data-plane/internal/pmsd/adapter_fias.go           |  286 ++
 data-plane/internal/pmsd/adapter_fias_test.go      |  542 ++++
 data-plane/internal/pmsd/adapter_frames_test.go    |  257 ++
 data-plane/internal/pmsd/applier.go                |  109 +
 data-plane/internal/pmsd/applier_supervisor.go     |  135 +
 .../internal/pmsd/applier_supervisor_test.go       |  341 +++
 data-plane/internal/pmsd/applier_test.go           |  228 ++
 data-plane/internal/pmsd/assignment.go             |  103 +
 data-plane/internal/pmsd/barrier_test.go           |  150 +
 data-plane/internal/pmsd/errcodes.go               |  215 ++
 data-plane/internal/pmsd/errcodes_test.go          |  136 +
 data-plane/internal/pmsd/events.go                 |  273 ++
 data-plane/internal/pmsd/fias_adapter.go           |   58 +
 data-plane/internal/pmsd/lockkey.go                |   86 +
 data-plane/internal/pmsd/lockkey_test.go           |   74 +
 data-plane/internal/pmsd/pg.go                     |  385 +++
 data-plane/internal/pmsd/pg_integration_test.go    |  613 ++++
 data-plane/internal/pmsd/pmsd.go                   |  541 ++++
 data-plane/internal/pmsd/pmsd_test.go              |  660 ++++
 data-plane/internal/pmsd/queue.go                  |  204 ++
 data-plane/internal/pmsd/queue_test.go             |  266 ++
 data-plane/internal/pmsd/secret.go                 |  144 +
 data-plane/internal/pmsd/secret_test.go            |   62 +
 data-plane/internal/pmsd/strict_parse.go           |  177 ++
 data-plane/internal/pmsd/strict_parse_test.go      |  150 +
 data-plane/internal/pmsd/supervisor.go             |  156 +
 data-plane/internal/pmsd/worker.go                 |  342 +++
 data-plane/internal/pmsd/writer.go                 |  154 +
 data-plane/internal/pmsd/writer_test.go            |  256 ++
 data-plane/internal/pmsguard/guard.go              |   16 +-
 data-plane/internal/pmsresolve/fanout.go           |  228 ++
 .../internal/pmsresolve/fanout_integration_test.go |  327 ++
 data-plane/internal/pmsresolve/resolve.go          |   96 +
 data-plane/internal/pmsresolve/resolve_test.go     |  112 +
 data-plane/internal/posting/config.go              |  127 +
 data-plane/internal/posting/engine.go              |  379 +++
 data-plane/internal/posting/errors.go              |   74 +
 data-plane/internal/posting/evidence.go            |  187 ++
 data-plane/internal/posting/export_test.go         |   33 +
 data-plane/internal/posting/fias.go                |  178 ++
 data-plane/internal/posting/gate.go                |  194 ++
 data-plane/internal/posting/pg_integration_test.go | 1463 +++++++++
 data-plane/internal/posting/posting_test.go        |  498 +++
 data-plane/internal/posting/repo.go                |  331 ++
 data-plane/internal/posting/transport.go           |  156 +
 data-plane/internal/poststay/convert.go            |  214 ++
 data-plane/internal/poststay/lineage.go            |  135 +
 data-plane/internal/poststay/poststay.go           |  431 +++
 .../internal/poststay/poststay_integration_test.go |  502 ++++
 .../poststay/uniformity_integration_test.go        |  234 ++
 data-plane/internal/shape/shape.go                 |  258 +-
 data-plane/internal/shape/shape_staged_test.go     |  188 ++
 data-plane/internal/shapeplan/plan.go              |  194 ++
 data-plane/internal/sms/twilio.go                  |    8 +-
 data-plane/internal/social/google.go               |   14 +-
 .../stayengine/checkout_slice_integration_test.go  |  684 +++++
 data-plane/internal/stayengine/pg.go               |  337 +++
 .../internal/stayengine/pg_integration_test.go     |  201 ++
 data-plane/internal/stayengine/resolve.go          |  131 +
 data-plane/internal/stayengine/resolve_test.go     |  106 +
 data-plane/internal/stayengine/sharers.go          |  190 ++
 data-plane/internal/staygrant/staygrant.go         |  239 ++
 .../staygrant/staygrant_integration_test.go        |  436 +++
 data-plane/internal/throttle/throttle.go           |    4 +-
 data-plane/internal/transfer/transfer.go           |  428 +++
 .../internal/transfer/transfer_integration_test.go |  587 ++++
 data-plane/internal/writerguard/writerguard.go     |  352 +++
 .../writerguard/writerguard_integration_test.go    |  233 ++
 .../0010_phase3_stay_resolution.down.sql           |  215 ++
 .../migrations/0010_phase3_stay_resolution.up.sql  | 2992 ++++++++++++++++++
 .../0011_phase4_financial_execution.down.sql       |   49 +
 .../0011_phase4_financial_execution.up.sql         |  498 +++
 .../0012_phase4_financial_hardening.down.sql       |  171 ++
 .../0012_phase4_financial_hardening.up.sql         |  478 +++
 .../0013_phase4_reversal_ledger.down.sql           |   38 +
 .../migrations/0013_phase4_reversal_ledger.up.sql  |  352 +++
 .../0014_phase4_payment_settlement.down.sql        |   28 +
 .../0014_phase4_payment_settlement.up.sql          |  367 +++
 .../0015_phase4_payment_hardening.down.sql         |  275 ++
 .../0015_phase4_payment_hardening.up.sql           |  468 +++
 .../0016_phase4_payment_coherence.down.sql         |  149 +
 .../0016_phase4_payment_coherence.up.sql           |  233 ++
 .../0017_phase4_least_privilege.down.sql           |   17 +
 .../migrations/0017_phase4_least_privilege.up.sql  |  120 +
 ...hase4_financial_identity_and_privilege.down.sql |   31 +
 ..._phase4_financial_identity_and_privilege.up.sql |  391 +++
 .../0019_phase4_financial_recovery.down.sql        |   53 +
 .../0019_phase4_financial_recovery.up.sql          |  378 +++
 .../0020_phase4_financial_observability.down.sql   |    5 +
 .../0020_phase4_financial_observability.up.sql     |   24 +
 .../migrations/0021_phase4_trust_boundary.down.sql |   18 +
 .../migrations/0021_phase4_trust_boundary.up.sql   |  264 ++
 .../0022_phase4_recovery_closure.down.sql          |  180 ++
 .../migrations/0022_phase4_recovery_closure.up.sql |  382 +++
 .../0023_phase4_restore_generation.down.sql        |   10 +
 .../0023_phase4_restore_generation.up.sql          |  217 ++
 ...se4_outcome_authority_and_grant_kernel.down.sql |  133 +
 ...hase4_outcome_authority_and_grant_kernel.up.sql |  250 ++
 ...se4_recovery_completion_and_compliance.down.sql |   93 +
 ...hase4_recovery_completion_and_compliance.up.sql |  359 +++
 ...ase4_c35_failclosed_and_operator_retry.down.sql |   25 +
 ...phase4_c35_failclosed_and_operator_retry.up.sql |  159 +
 .../0027_phase5_poststay_and_transfer.down.sql     |  126 +
 .../0027_phase5_poststay_and_transfer.up.sql       |  518 ++++
 .../0028_phase5_poststay_throttle_method.down.sql  |   11 +
 .../0028_phase5_poststay_throttle_method.up.sql    |   21 +
 .../0029_phase5_reveal_is_at_mint.down.sql         |   72 +
 .../0029_phase5_reveal_is_at_mint.up.sql           |   94 +
 deploy/caddy/Caddyfile                             |  157 -
 deploy/caddy/Caddyfile.central                     |   57 +
 deploy/caddy/Caddyfile.edge                        |  119 +
 deploy/caddy/README.md                             |   62 +-
 deploy/caddy/stayconnect-caddy.central.service     |   46 +
 ...addy.service => stayconnect-caddy.edge.service} |   11 +
 deploy/env/pmsd.env.dark                           |   28 +
 deploy/scripts/stayconnect-financial-restore.sh    |  206 ++
 deploy/scripts/stayconnect-site-backup.sh          |  140 +
 deploy/systemd/stayconnect-pmsd.service            |   41 +
 docs/BACKUP_AND_RESTORE.md                         |   58 +-
 docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md     |  412 +++
 docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md            |  118 +
 docs/PHASE4_DEPENDENCY_TRIAGE.md                   |  171 ++
 docs/SYSTEM_OVERVIEW.md                            |    2 +-
 .../StayConnect-IAM-Phase2-Live-Dark-Acceptance.md |    2 +-
 .../StayConnect-IAM-Phase4-Live-Dark-Acceptance.md |   82 +
 .../Phase3-Controlled-Writer-Privilege-Manifest.md |  103 +
 docs/architecture/Phase3-Privilege-Matrix.md       |   34 +
 .../Phase4-Financial-Schema-Gap-Audit.md           |  530 ++++
 docs/architecture/Phase4-Readiness-Matrix.md       |   62 +
 .../StayConnect-IAM-Phase0-Contract.md             |   26 +-
 docs/architecture/StayConnect-IAM-Phase1A-Plan.md  |   18 +-
 docs/architecture/StayConnect-IAM-Phase1B-Plan.md  |   16 +-
 docs/architecture/StayConnect-IAM-Phase2-Plan.md   |    2 +-
 docs/architecture/StayConnect-IAM-Phase3-Plan.md   |  260 ++
 docs/architecture/StayConnect-IAM-Phase4-Plan.md   |  229 ++
 docs/architecture/StayConnect-IAM-Phase5-Plan.md   |  127 +
 .../adr/ADR-0001-pmsd-connector-ownership.md       |   53 +
 .../adr/ADR-0002-phase3-single-shaping-owner.md    |  303 ++
 ...R-0003-ruleset-structure-from-current-render.md |  158 +
 docs/context/StayConnect-IAM-Handoff.md            |   25 +-
 docs/evidence/Phase3-CI-Artifact-Exposure-Audit.md |  146 +
 .../Phase3-Final-Live-Acceptance-Record.md         |  113 +
 .../Phase4-Final-Live-Acceptance-Record.md         |  203 ++
 .../StayConnect-IAM-Phase3-Schema-Gap-Audit.md     |  109 +
 docs/evidence/StayConnect-IAM-Phase5-Evidence.md   |  266 ++
 docs/evidence/phase4/npm-audit-full.json           |   22 +
 docs/evidence/phase4/npm-audit-production.json     |   22 +
 docs/manifests/Phase3-change-manifest.md           | 1339 +++++++++
 .../reports/StayConnect-IAM-Phase2-Final-Report.md |    4 +-
 .../reports/StayConnect-IAM-Phase3-Final-Report.md | 2146 +++++++++++++
 .../reports/StayConnect-IAM-Phase4-Final-Report.md |  159 +
 .../StayConnectEnterprise-ChatGPT-Project-Pack.zip |  Bin 250675 -> 305281 bytes
 .../StayConnectEnterprise-Phase-Evidence-Pack.zip  |  Bin 101471 -> 114418 bytes
 ...StayConnectEnterprise-Phase1B-Planning-Pack.zip |  Bin 41921 -> 42175 bytes
 .../chatgpt/phase-evidence/GIT_STAT_0019d62.txt    |    4 +
 .../chatgpt/phase-evidence/GIT_STAT_9a1f356.txt    |    4 -
 exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt |   16 +-
 .../phase-evidence/Phase2-change-manifest.md       |   13 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |    6 +-
 .../StayConnect-IAM-Phase2-Final-Report.md         |    4 +-
 .../StayConnect-IAM-Phase2-Live-Dark-Acceptance.md |    2 +-
 .../governance/decision-register.json              |  536 +++-
 .../chatgpt/phase-evidence/tools/project-state.py  |   96 +-
 .../phase-evidence/tools/validate-project-state.sh |  184 +-
 exports/chatgpt/phase1b-planning/MANIFEST.md       |    2 +-
 .../chatgpt/phase1b-planning/PACK_SHA256SUMS.txt   |    6 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |   18 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |   16 +-
 .../chatgpt/stayconnectenterprise/00-START-HERE.md |   49 +-
 exports/chatgpt/stayconnectenterprise/MANIFEST.md  |   69 +-
 .../stayconnectenterprise/PROJECT-INSTRUCTIONS.md  |   14 +-
 .../Phase2-change-manifest.md                      |   13 +-
 .../Phase3-Privilege-Matrix.md                     |   34 +
 .../Phase3-change-manifest.md                      | 1339 +++++++++
 .../stayconnectenterprise/SYSTEM_OVERVIEW.md       |    2 +-
 .../StayConnect-IAM-Handoff.md                     |   25 +-
 .../StayConnect-IAM-Phase0-Contract.md             |   26 +-
 .../StayConnect-IAM-Phase1A-Plan.md                |   18 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |   16 +-
 .../StayConnect-IAM-Phase2-Final-Report.md         |    4 +-
 .../StayConnect-IAM-Phase2-Live-Dark-Acceptance.md |    2 +-
 .../StayConnect-IAM-Phase2-Plan.md                 |    2 +-
 .../StayConnect-IAM-Phase3-Plan.md                 |  260 ++
 governance/decision-register.json                  |  536 +++-
 governance/dependency-acceptances.json             |   23 +
 governance/project-state.json                      |  543 +++-
 governance/transitions/T0015.json                  |   18 +
 governance/transitions/T0016.json                  |   27 +
 governance/transitions/T0017.json                  |   30 +
 governance/transitions/T0018.json                  |   29 +
 governance/transitions/T0019.json                  |   31 +
 governance/transitions/T0020.json                  |   50 +
 governance/transitions/T0021.json                  |   37 +
 governance/transitions/T0022.json                  |   33 +
 governance/transitions/T0023.json                  |   35 +
 governance/transitions/T0024.json                  |   48 +
 governance/transitions/T0025.json                  |   61 +
 governance/transitions/T0026.json                  |   47 +
 governance/transitions/T0027.json                  |   93 +
 governance/transitions/T0028.json                  |  120 +
 governance/transitions/T0029.json                  |   86 +
 governance/transitions/T0030.json                  |   91 +
 governance/transitions/T0031.json                  |  121 +
 governance/transitions/T0032.json                  |   85 +
 governance/transitions/T0033.json                  |   95 +
 governance/transitions/T0034.json                  |   96 +
 governance/transitions/T0035.json                  |   92 +
 governance/transitions/T0036.json                  |  101 +
 governance/transitions/T0037.json                  |  143 +
 governance/transitions/T0038.json                  |  155 +
 governance/transitions/T0039.json                  |  156 +
 governance/transitions/T0040.json                  |  181 ++
 governance/transitions/T0041.json                  |  124 +
 governance/transitions/T0042.json                  |   61 +
 governance/transitions/T0043.json                  |  147 +
 governance/transitions/T0044.json                  |  120 +
 governance/transitions/T0045.json                  |   98 +
 governance/transitions/T0046.json                  |   82 +
 governance/transitions/T0047.json                  |   55 +
 governance/transitions/T0048.json                  |   85 +
 governance/transitions/T0049.json                  |   93 +
 governance/transitions/T0050.json                  |   49 +
 governance/transitions/T0051.json                  |   97 +
 hotel-admin/app/(app)/checkout-grace/page.tsx      |   19 +
 hotel-admin/app/(app)/financial-health/page.tsx    |   10 +
 hotel-admin/app/(app)/financial-recovery/page.tsx  |   21 +
 hotel-admin/app/(app)/financial-review/page.tsx    |   20 +
 .../app/(app)/financial-settlements/page.tsx       |   10 +
 hotel-admin/app/(app)/operational-alerts/page.tsx  |   21 +
 hotel-admin/app/(app)/pms-interfaces/page.tsx      |  535 ++++
 hotel-admin/app/(app)/pms-resolutions/page.tsx     |  139 +
 hotel-admin/app/(app)/pms-routing/page.tsx         |  125 +
 .../app/(app)/pms-source-conflicts/page.tsx        |   98 +
 hotel-admin/app/(app)/post-stay/page.tsx           |   21 +
 hotel-admin/app/(app)/stay-events/page.tsx         |  112 +
 hotel-admin/app/(app)/stay-transfers/page.tsx      |   20 +
 hotel-admin/app/(app)/stays/page.tsx               |  161 +
 hotel-admin/components/nav.tsx                     |   28 +
 .../components/phase3/checkout-grace-form.tsx      |  234 ++
 .../components/phase3/operational-alerts-view.tsx  |  156 +
 .../components/phase4/financial-health-view.tsx    |  161 +
 .../components/phase4/financial-recovery-view.tsx  |  442 +++
 .../components/phase4/manual-review-view.tsx       |  378 +++
 hotel-admin/components/phase4/settlements-view.tsx |  195 ++
 hotel-admin/components/phase5/post-stay-view.tsx   |  281 ++
 .../components/phase5/stay-transfer-view.tsx       |  300 ++
 hotel-admin/e2e/auth-middleware.spec.ts            |   82 +
 .../e2e/phase3-guest-portal-resilience.spec.ts     |  414 +++
 hotel-admin/e2e/phase3-guest-portal.spec.ts        |  211 ++
 hotel-admin/e2e/phase3-pms-interfaces.spec.ts      |  338 +++
 hotel-admin/e2e/phase3-stays-grace.spec.ts         |  290 ++
 hotel-admin/e2e/phase4-financial-operator.spec.ts  |  358 +++
 hotel-admin/e2e/phase5-guest-post-stay.spec.ts     |  113 +
 hotel-admin/e2e/phase5-post-stay.spec.ts           |  193 ++
 hotel-admin/e2e/phase5-stay-transfer.spec.ts       |  140 +
 hotel-admin/lib/api.ts                             |  301 ++
 hotel-admin/lib/roles.ts                           |   15 +
 hotel-admin/next.config.mjs                        |    8 +
 hotel-admin/package-lock.json                      | 3169 +++++++++++---------
 hotel-admin/package.json                           |   13 +-
 hotel-admin/playwright.config.ts                   |    2 +-
 hotel-admin/test/nav.test.tsx                      |   37 +
 hotel-admin/test/phase3-interface-pages.test.tsx   |  282 ++
 hotel-admin/test/phase3-pages.test.tsx             |  262 ++
 hotel-admin/test/phase4-financial-pages.test.tsx   |  416 +++
 .../test/phase4-review-settlements.test.tsx        |  195 ++
 iam_v2_scratch/00_platform_fixture.sql             |   19 +-
 iam_v2_scratch/phase3_0010_lifecycle.sh            | 1085 +++++++
 iam_v2_scratch/phase4_0011_financial.sh            |  933 ++++++
 iam_v2_scratch/phase4_db_invariants.sh             |  218 ++
 iam_v2_scratch/phase4_financial_fixture.sql        |  139 +
 iam_v2_scratch/phase4_least_privilege.sh           |   81 +
 iam_v2_scratch/phase4_payment_concurrency.sh       |  170 ++
 iam_v2_scratch/phase5_0027_foundation.sh           |  406 +++
 iam_v2_scratch/phase5_0027_lifecycle.sh            |  121 +
 iam_v2_scratch/phase5_least_privilege.sh           |  108 +
 iam_v2_scratch/schema_structure_fingerprint.sql    |   41 +
 iam_v2_scratch/seed.sql                            |   13 +-
 scripts/binary-rollback.sh                         |  239 ++
 scripts/ci/binary-rollback-tests.sh                |  203 ++
 scripts/ci/dependency-judgement.py                 |   90 +
 scripts/ci/go-test-counted.sh                      |   16 +
 scripts/ci/gofmt-check.sh                          |   15 +
 scripts/ci/gojson_summary.py                       |   58 +
 scripts/ci/kernel-netns-suite.sh                   |  265 ++
 scripts/ci/pg-gate.sh                              |   22 +
 scripts/ci/phase3_evidence.py                      |  741 +++++
 scripts/ci/phase4-dark-check.sh                    |  176 ++
 scripts/ci/phase4-dependency-gate-selftest.sh      |  106 +
 scripts/ci/phase4-dependency-gate.sh               |   63 +
 scripts/ci/phase4_evidence.py                      |  102 +
 scripts/ci/step.sh                                 |   26 +
 scripts/edge-migrate.sh                            |  299 ++
 scripts/install-pmsd-dark.sh                       |  130 +
 scripts/phase14-tls-test.sh                        |    4 +-
 scripts/phase3-evidence.sh                         |  127 +
 scripts/phase3-preflight.sh                        |  699 +++++
 scripts/phase4-least-privilege.sh                  |   40 +
 scripts/phase4-payment-concurrency.sh              |   41 +
 scripts/phase4-pg-integration.sh                   |  170 ++
 scripts/phase4-restore-drill.sh                    |  402 +++
 scripts/phase5-dark-guard.sh                       |   46 +
 scripts/phase5-iam-live-dark-deploy.sh             |  144 +
 scripts/phase5-pg-integration.sh                   |   87 +
 scripts/pmsd-pg-integration.sh                     |   94 +
 tools/embed-report-manifest.py                     |   49 +
 tools/generate-change-manifest.py                  |    8 +-
 tools/project-state.py                             |   96 +-
 tools/tests/current_state_parity/run_negative.py   |  664 ++++
 .../evidence_artifact/run_artifact_staleness.py    |  230 ++
 .../run_isolation_regression.py                    |  235 ++
 .../tests/project_state_validator/run_mutations.py |  406 ++-
 tools/tests/tooling/run_control_chars.py           |   58 +
 tools/validate-current-state-parity.py             | 1059 +++++++
 tools/validate-pr-metadata.sh                      |  323 ++
 tools/validate-project-state.sh                    |  184 +-
 tools/validate-state-parity-selftest.sh            |  121 +
 tools/validate-state-parity.py                     |  235 ++
 tools/validate-transition-times.sh                 |   86 +
 473 files changed, 100286 insertions(+), 2245 deletions(-)
```

## Working-tree status (`git status --short --untracked-files=all`)
```text
M  exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip
M  exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip
M  exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip
A  exports/chatgpt/phase-evidence/GIT_STAT_0019d62.txt
D  exports/chatgpt/phase-evidence/GIT_STAT_ffdeef5.txt
M  exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt
M  exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt
M  exports/chatgpt/phase1b-planning/MANIFEST.md
M  exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt
M  exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt
M  exports/chatgpt/stayconnectenterprise/MANIFEST.md
M  governance/project-state.json
```

## Commits in range (`git log --oneline <base>..HEAD`)
```text
HISTORICAL: 0019d62 Phase 5 M4: the LIVE-DARK acceptance candidate (T0051)
HISTORICAL: ffdeef5 Phase 5 M4 fix: the post-stay tab must not displace the room form
HISTORICAL: c077036 Phase 5 M4: the controlled LIVE-DARK deployment script for the development appliance
HISTORICAL: 50f09cd Phase 5 M4: authoritative CI, the derived least-privilege proof, and the DARK guard
HISTORICAL: dc0a0e0 Phase 5 M4 fix-forward: the CONTRACTUAL F9-i, and the two defects it found
HISTORICAL: f874e21 Phase 5 (delivery_head): M3 pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 0aa361a Phase 5 M3: Cross-PMS Transfer â€” typed, staff-confirmed, never inferred
HISTORICAL: dead716 Phase 5 (delivery_head): M2 fix-forward pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 791a543 Phase 5 M2 fix-forward: the PUBLIC guest surface refuses an identity it did not derive, plus the M2 evidence record
HISTORICAL: def9b8a Phase 5 M2 (exposure): guest surface, operator surface, Portal, Hotel Admin, and the uniformity proof
HISTORICAL: b36ae80 Phase 5 M2 (core): the Post-Stay PIN lifecycle, zero-price conversion, and the reveal rule corrected by evidence
HISTORICAL: 50e641b Phase 5 M1: POST_STAY_PIN auth contexts, reconciled rather than reused (+ 0027 lifecycle proof)
HISTORICAL: 0d4548c Phase 5 M1: post-stay identity, transfer invariants and the Phase-5 controlled-writer boundary (0027)
HISTORICAL: d49342c Phase 4 (delivery_head): housekeeping pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: c90faf7 Phase 4 post-merge housekeeping: merge state is a fact, not prose (D20 / T0049)
HISTORICAL: 573cf81 Phase 4 (delivery_head): post-merge pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: d04044a Phase 4 closure: record the merge of PR #12 into master (D20 / T0048)
HISTORICAL: 210154b Merge PR #12: Phase 4 (Financial Execution Layer) â€” Product-Owner ACCEPTED_AND_CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity (D19/T0044)
HISTORICAL: 0d0b6de Phase 4 (delivery_head): T0047 correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: a8cea44 Phase 4 closure correction (T0047): the generated block is the only carrier of current state in 00-START-HERE
HISTORICAL: a2a17db Phase 4 (delivery_head): correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 4c468fb Phase 4 closure correction: look the PR up by NUMBER, because GITHUB_REF_NAME is the merge ref
HISTORICAL: b5dd570 Phase 4 (delivery_head): T0046 correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: aea2b88 Phase 4 closure correction (T0046): static current-state prose outside the generated block
HISTORICAL: b26f24a Phase 4 (delivery_head): correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: e427bca Phase 4 closure correction: the PR-metadata gate must read the LIVE PR body, not the frozen event payload
HISTORICAL: 110f913 Phase 4 (delivery_head): correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 9b18484 Phase 4 closure correction: rule D must scan every phase maturity, not only 1B's
HISTORICAL: f1b6c7e Phase 4 (delivery_head): correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 4cc01c1 Phase 4 closure correction: convert the last whitespace-pinned mutation fixture to json_set
HISTORICAL: e150b1b Phase 4 (delivery_head): T0045 correction pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: a582e0e Phase 4 closure correction (T0045): remove the stale current-state contradictions and close the false-pass classes that hid them
HISTORICAL: 581daa0 Phase 4: record PR #12 (open, unmerged) and the closure-head CI evidence
HISTORICAL: 43b5c29 Phase 4 (delivery_head): D19/T0044 closure pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 38e9426 Phase 4 ACCEPTED AND CLOSED at verified LIVE-DARK / NO-FINANCIAL-TRAFFIC (D19 / T0044)
HISTORICAL: 105af49 Phase 4 (delivery_head): T0043 WS-L receipt, complete staged manifest, rebuilt packs, pointer and report-embedded manifest
HISTORICAL: 1f7607f WS-L: remove a stray zero-content file swept in by git add -A, and make next_authorized_action one action
HISTORICAL: b94112d WS-L: make the restore drill exercise the version check the appliance actually performs
HISTORICAL: bb152dc WS-L: record the live-DARK deployment result (T0043) and sync the current-state surfaces
HISTORICAL: 2030647 WS-L: prove the Phase-4 chain rolls back and re-applies to the SAME SCHEMA STRUCTURE
HISTORICAL: f468dc0 WS-L: make the supported site backup work where PostgreSQL is containerised
HISTORICAL: 26d14de WS-L preflight: reconcile the stale Phase-4 current-state claims and make the same staleness impossible to pass again (T0042)
HISTORICAL: 6bcd968 Phase 4: record the delivery-head CI run as well as the software-candidate run
HISTORICAL: b895f41 Phase 4 (delivery_head): T0041 receipt, complete staged manifest, rebuilt packs, pointer and report-embedded manifest
HISTORICAL: f2771dc Phase 4: generate the lockfile on Linux so npm ci reproduces it on the CI runner
HISTORICAL: 713afcd Phase 4: correct the C35 closure test, the C-matrix, the CI-head language and the project state (T0041)
HISTORICAL: 52ffb92 Phase 4: remove the production dependency risk instead of accepting it, and stop the gate accepting its own risk
HISTORICAL: 02397e1 Phase 4: complete the zero-attempt recovery operator path (DB read model, edged API, Hotel-Admin UI) and extend the DB gate to 0026
HISTORICAL: c3789e1 Phase 4 (0026): C35 fails closed on an external verified receipt; zero-attempt recovery read model
HISTORICAL: 71d7ac0 Phase 4: record the final-delivery-head CI run as the authoritative evidence
HISTORICAL: 35ef249 Phase 4 (delivery_head): regenerate packs and manifest for the final software head
HISTORICAL: 3ad3c78 Phase 4: anchor the accessibility E2E on the held table rather than the banner, removing a two-response race
HISTORICAL: 143afe4 Phase 4 (delivery_head): advance the candidate pointers to the cleaned software head
HISTORICAL: b167361 Phase 4: drop the Next-16 attempt's leftover agent files and restore the original tsconfig formatting
HISTORICAL: 4b0b29a Phase 4: point the authoritative CI facts at the final software candidate
HISTORICAL: 7fb4c5f Phase 4: the restore drill's backup block uses its own pg_dump stand-in, so it tests the /etc exclusion rather than the runner's database socket
HISTORICAL: 4053e36 Phase 4 (delivery_head): T0040 receipt + project-state pointers
HISTORICAL: 49dd109 Phase 4: final software closure - implementation head
HISTORICAL: 692768f Phase 4: extend the DB gate to 0024/0025, measured C1-C38 matrix, Plan and Gap-Audit sync, CI dependency gate and full browser suite
HISTORICAL: 21601ec Phase 4: dependency evidence for both trees, GHSA triage, attempted-and-reverted Next 16, production advisory gate
HISTORICAL: 54806b3 Phase 4: trusted restore manifest via the pinned registry anchor, proven quiesce, marker excluded from the /etc backup domain, runbook updated
HISTORICAL: 4cf8c51 Phase 4 (0025): zero-attempt recovery retry, marker BEHIND, C27 cross-tenant merchant identity, C35 archive-before-purge
HISTORICAL: eb553d0 Phase 4 (0024): independent provider-outcome authority and one shared entitlement grant kernel
HISTORICAL: a80655d Phase 4: regenerate the packs and manifest against the final delivery head
HISTORICAL: 91f2e0c Phase 4: point the authoritative CI facts at the final-head run
HISTORICAL: 0ef40f3 Phase 4: regenerate the Hotel-Admin lockfile from a clean install so npm ci resolves identically on CI
HISTORICAL: c35de89 Phase 4: resynchronize the Hotel-Admin lockfile after the security upgrades so npm ci is deterministic
HISTORICAL: 5d28452 Phase 4 (delivery_head): T0039 receipt + project-state pointers
HISTORICAL: 99f7824 Phase 4: extend the DB gate to 0019-0023, make the provenance backfill idempotent
HISTORICAL: a1c85d7 Phase 4: complete the WS-J operator surface, dependency triage, restore drill and gate self-test in CI, Plan sync
HISTORICAL: 8f5cfb1 Phase 4: unambiguous CI gate runner, marker-driven reconciliation wiring, reviewed definer surface
HISTORICAL: 368de36 Phase 4 (0023): supported restore-generation model, management marker, signed-manifest restore tool and a real pg_restore drill
HISTORICAL: 0ffdfcc Phase 4 (0022): recovery closure - structural full-rail hold, rail-specific reconciliation, verified release, legacy identity provenance
HISTORICAL: d1af118 Phase 4 (0021): restricted-role trust boundary - high-level operations only, low-level definer primitives revoked
HISTORICAL: f22b0cb Phase 4: point the authoritative CI facts at the current head
HISTORICAL: 22ee4f1 Phase 4 (delivery_head): T0038 receipt + project-state pointers
HISTORICAL: 827240b Phase 4: financial-ops API contract tests, extended DARK and authoritative CI, Plan sync
HISTORICAL: 806b4f8 Phase 4: FINANCIAL_RECOVERY_MODE (0019), observability (0020), financial-ops API and the Hotel-Admin operator surface
HISTORICAL: 7b10ec3 Phase 4: close the payment backend boundary (0018) â€” resolved financial identity, trusted notification boundary, NOT_SENT preflight, operational least privilege
HISTORICAL: 7d427c4 Phase 4: point the authoritative CI facts at run 31601463842 on head 1d20ecd2
HISTORICAL: 1d20ecd Phase 4 (delivery_head): T0037 receipt + project-state pointers + regenerated manifest
HISTORICAL: 829444b Phase 4 (inventory_head): payment runtime, Phase-2 entitlement handoff, migrations 0016-0017, least-privilege roles
HISTORICAL: 7db028e Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: edd0e8a Phase 4: migration 0015 â€” 0014's payment bounds were not concurrency-safe
HISTORICAL: d3afa65 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 7560dd0 Phase 4: harden Manual Review audit inputs, and add payment/settlement governance (migration 0014)
HISTORICAL: 3a87b79 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: efdd580 Phase 4: correct Manual Review scope, evidence and role truth
HISTORICAL: 3e2eb20 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 943705f Phase 4: record the authoritative CI evidence for software candidate 82ce2d6
HISTORICAL: d853fe4 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 82ce2d6 Phase 4: close the construction boundary for real, and deliver the Manual Review operator surface
HISTORICAL: 1f65a63 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 8beed1e Phase 4: record the authoritative CI evidence for delivery head 0aa63d77
HISTORICAL: 0aa63d7 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 4ce51dc Phase 4: close the production construction boundary and correct the reversal model to the FINAL contract
HISTORICAL: 102a75c Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 6361f07 Phase 4: record the authoritative CI evidence for head f9f13f97
HISTORICAL: f9f13f9 Phase 4 (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: dd00fac Phase 4: gate the phase branch itself, so authoritative CI can run without an early PR or a merge
HISTORICAL: 795793d Phase 4 hardening (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: e6c7bc5 Phase 4: harden the financial execution core â€” migration 0012, contract lifecycle, freshness axes, real PA correlation
HISTORICAL: 824e49b Phase 4 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 64521ac Phase 4: financial execution core â€” migration 0011, Posting domain, P# allocator, outbox lanes, PS/PA, UNKNOWN
HISTORICAL: 7202152 Phase 4 audit (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: a37df59 Authoritative Phase-4 financial schema-gap audit; correct the plan's pre-measurement assumptions
HISTORICAL: 3ea775c Behaviourally prove the non-IN_HOUSE CHARGE gate through the approved Stay lifecycle
HISTORICAL: 0e6ba30 Close the reconciliation checkpoint: authorization model, fixture repair, behavioural DB proof
HISTORICAL: 24f00ec Phase 4 reconciliation (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 5058005 Phase-4 reconciliation checkpoint: measured schema baseline + current-state correction
HISTORICAL: db856cc Correct the Phase-4 current-state contradiction, and repair two silently-broken validator regexes
HISTORICAL: a835b18 Merge PR #11: pre-Phase-4 baseline READY + Phase-4 authorization (D18/T0029)
HISTORICAL: e5546ed Phase 4 authorization (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: 91a7668 Phase-3 governance guards must not switch off when the current phase moves past 3
HISTORICAL: 82cb3f2 Phase 4 authorization (delivery_head): manifest + rebuilt packs + pointer
HISTORICAL: d62a16d Pre-Phase-4 baseline pass + Phase-4 authorization (D18 / T0029)
HISTORICAL: a4e9519 Merge PR #10: Edge 404 block must not declare its own log file
HISTORICAL: 89cb998 Phase 3 (delivery_head): edge-404 logfix manifest + rebuilt packs + pointer
HISTORICAL: 955042f Edge 404 block: drop the per-site log file that made every Caddy start fail
HISTORICAL: cc4a052 Merge PR #9: final residual closure â€” T0027 ratification, licence renewal, Caddy contracts (T0028)
HISTORICAL: 9b5aa85 Phase 3 (delivery_head): T0028 manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 4ec2a8d Ratify T0027 state, renew the appliance licence to 2027-08-08, make the Central reload contract honest (T0028)
HISTORICAL: 7d16856 Merge PR #8: residual-findings closure â€” host identity, Edge Caddy role split, artifact-audit record (T0027)
HISTORICAL: 6936858 Phase 3 (delivery_head): residual-findings manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 0eddcec Stop the mutation fixture drifting on every governance round
HISTORICAL: 0a7546b Phase 3 (delivery_head): residual-findings manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 8b80184 Close five residual findings: cloned host identity, Central-only Edge vhosts, artifact-audit record (T0027)
HISTORICAL: df55a09 Merge PR #7: post-closure governance hygiene â€” merged-PR metadata and evidence-artifact staleness (T0026)
HISTORICAL: 2e061ed Evidence hygiene: sanitise EVERY metadata copy, not the one that was easiest to find
HISTORICAL: 52927d9 Evidence hygiene: allowlist Playwright's report metadata, and make the PII gate self-diagnosing
HISTORICAL: aa5f966 Phase 3 (delivery_head): hygiene-round manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: f1190fd Evidence hygiene: stop shipping Playwright's embedded repository diff in the artifact
HISTORICAL: 2398f93 Phase 3 (delivery_head): post-closure-hygiene manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 8e465a2 Post-closure hygiene: stop two surfaces presenting superseded state as current (T0026)
HISTORICAL: ea88673 Phase 3 (delivery_head): regenerate the manifest from the STAGED tree, correcting a generation-order defect
HISTORICAL: a98338a Phase 3 (delivery_head): post-merge manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 46a07aa Phase 3 closure: record the merge of PR #6 into master (D17 / T0025)
HISTORICAL: 8a7230a Merge PR #6: Phase 3 (Stay Resolution & Grace) â€” Product-Owner ACCEPTED_AND_CLOSED at DARK maturity (D16/T0024)
HISTORICAL: fb25fd4 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 9d5759b Phase 3 (inventory_head): Â§2 no longer lists "no appliance, Production DB or live PMS contact" as a standing constraint
HISTORICAL: 06e81b4 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 50c92d0 Phase 3 (inventory_head): merge-readiness corrections â€” a cited evidence file that was silently gitignored, and four stale current-state claims
HISTORICAL: 7f6bcb2 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 541004e Phase 3 (inventory_head): authorization provenance must outlive the phase it authorized
HISTORICAL: 02d5567 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 265acf1 Phase 3 (inventory_head): teach the PR-metadata gate that acceptance is a real state
HISTORICAL: ddbeccb Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 86c52e4 Phase 3 (inventory_head): ACCEPTED AND CLOSED at verified DARK maturity, with the audit chronology corrected
HISTORICAL: 7c8b8cf Phase 3 DARK acceptance (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: b9cf833 Phase 3 DARK acceptance (inventory_head): the dark pmsd env must not even MENTION a Phase-3 flag name
HISTORICAL: 88c6deb Phase 3 DARK acceptance (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 11411b9 Phase 3 DARK acceptance (inventory_head): a rollback that cannot preserve authorization now refuses, and pmsd is actually deployed
HISTORICAL: 7318ac2 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 8e4a45b Phase 3 (inventory_head): re-anchor the M32 mutation to the corrected current_maturity wording
HISTORICAL: 3d106b8 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 31cf385 Phase 3 (inventory_head): zero-stale truth sync â€” record current state as DATA, and make contradictions fail the gate
HISTORICAL: 436d2cc Phase 3 network lifecycle (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 42308da Phase 3 network lifecycle (inventory_head): normalise the kernel ruleset comparison and re-anchor the mutation fixtures
HISTORICAL: 86b70a7 Phase 3 network lifecycle (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 95630c0 Phase 3 network lifecycle (inventory_head): unreadable is not empty, boot reconstructs the confirmed revision, and rollback has no destructive fallback
HISTORICAL: be135f2 Phase 3 Increment-9 correction (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 5b89c19 Phase 3 Increment-9 correction (inventory_head): the evidence artifact was still asserting restrictions that stopped being true
HISTORICAL: 5a245a3 Phase 3 Increment-9 correction (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 5433a26 Phase 3 Increment-9 correction (inventory_head): force the kernel carry-over test's converge with a change the renderer actually emits
HISTORICAL: 15f11b6 Phase 3 Increment-9 correction (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 2e3e322 Phase 3 Increment-9 correction (inventory_head): re-anchor the mutation fixtures and give the --apply-role test role the schema it migrates
HISTORICAL: 350bc6f Phase 3 Increment-9 correction (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 8c2b77e Phase 3 Increment-9 correction (inventory_head): keep the ledger-owner refusal message assertable, and gate the new acceptance path
HISTORICAL: abb9131 Phase 3 Increment-9 correction (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 12b43a8 Phase 3 Increment-9 correction (inventory_head): ruleset structure is reconciled from the current render, not a stored bundle
HISTORICAL: 8344920 Phase 3 pre-live (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 03f7a30 Phase 3 pre-live (inventory_head): the PR-metadata check reported a successful match as a failure
HISTORICAL: f1a998a Phase 3 pre-live (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 7bee19b Phase 3 pre-live (inventory_head): bind the security journal to the assigned scope and to one exact session identity
HISTORICAL: b346e6d Phase 3 pre-live (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: d9e9654 Phase 3 pre-live (inventory_head): trusted boot identity, semantic security-journal validation, and a corrected T0019 timestamp
HISTORICAL: 9c09cd6 Phase 3 pre-live (delivery_head): T0019 + complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 41aa0b8 Phase 3 pre-live (inventory_head): write-ahead activation durability, monotonic security time, and true Zero-Stale state
HISTORICAL: ac07ed3 Phase 3 pre-live (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: bc347af Phase 3 pre-live (inventory_head): stop hard-coding CI suite counts in the Final Report
HISTORICAL: 5cb0ca9 Phase 3 pre-live (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: fd9dc37 Phase 3 pre-live (inventory_head): count the durability and parser suites in the enforcement gate step
HISTORICAL: 33ef1ac Phase 3 pre-live (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 34a4b78 Phase 3 pre-live (inventory_head): enforce the delivery-evidence protocol instead of duplicating it, and re-anchor the mutation fixture
HISTORICAL: b830a2a Phase 3 pre-live (delivery_head): T0018 + complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: b1caebb Phase 3 pre-live (inventory_head): hard-boundary lease precision, durable activation bound, and a Zero-Stale check that had never run
HISTORICAL: 75fb848 Phase 3 pre-live safety (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 1520492 Phase 3 pre-live safety (inventory_head): record the network-enforcement writers in the privilege inventory
HISTORICAL: 7bd8acd Phase 3 pre-live safety (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: ee55f89 Phase 3 pre-live safety (inventory_head): state the artifact's real file counts and suite totals
HISTORICAL: 92391db Phase 3 pre-live safety (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 64f5c1a Phase 3 pre-live safety (inventory_head): permanent concat elements were invisible to the nft parser
HISTORICAL: 553d166 Phase 3 pre-live safety (inventory_head): bounded kernel lease, fail-closed activation, DB-verified accountability, surgical nft foundation, real-kernel gate
HISTORICAL: 9a1158a Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 17c9f4c Phase 3: count the network-enforcement suite as its own gate step
HISTORICAL: 2742547 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: be1121c Phase 3: ADR-0002 amended to the real enforcement model, and Zero-Stale checks for the phrases that drifted
HISTORICAL: 092b78b Phase 3: make packet authorization, accountable metering and Session state one enforcement contract
HISTORICAL: 5a2d527 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: d8c23b6 Phase 3: update mutation-suite fixtures for the new project-state values
HISTORICAL: 1b818f7 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 0e1c7dd Phase 3: accept the Live-Increment-9 next-action phrasing in the zero-stale validator
HISTORICAL: c885f98 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 5529f08 Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync
HISTORICAL: 4a9f602 Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation
HISTORICAL: 1f407ca Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 762909c Phase 3 Â§9: stop the Final Report Â§13 from citing a frozen (stale) HEAD
HISTORICAL: e65e254 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 36c9deb Phase 3 Â§7: upload the evidence artifact from the dot-prefixed staging dir
HISTORICAL: 66429e5 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: d57715d Phase 3 Â§7: regenerate the hotel-admin lockfile on Linux so `npm ci` resolves on the runner
HISTORICAL: 7d7e4ea Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: e42f1cd Phase 3 Â§7: fix `npm ci` in the full gate â€” drop the unused, conflicting @vitejs/plugin-react
HISTORICAL: 90f7cc1 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 4184bf3 Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent
HISTORICAL: 3b39a6a Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 650f158 Phase 3 Â§7 correction: make the Software CI the TRUE full same-HEAD gate with an uploaded evidence artifact
HISTORICAL: e836807 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 99e8a1c Phase 3 Â§7: downloadable evidence artifact with a SHA-256 integrity manifest
HISTORICAL: afd4ee3 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 9297379 Phase 3 Â§8/Â§9: self-audit findings, and the authoritative state brought up to date
HISTORICAL: 028d356 Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface
HISTORICAL: 987c5bf Phase 3 Â§6 (backend): the PMS interface admin surface
HISTORICAL: 0e312e5 Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family
HISTORICAL: 7d8d9f2 Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable
HISTORICAL: ae19eb2 Phase 3: fix the rollback ordering defect and stop it recurring
HISTORICAL: 7fe70f2 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: d68ec4d Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants
HISTORICAL: 7c57e42 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 51bafc0 Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority
HISTORICAL: de2829c Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: fb15bf0 Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice
HISTORICAL: df041a2 Phase 3 (C): durable accounting checkpoints + absolute-counter controlled ingestion + netd class epochs
HISTORICAL: 359cb59 @ Phase 3 accounting reopen + shaping closure (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 12f4173 Phase 3 accounting item 6 reopen corrected + shaping 7-8 closed (inventory_head): right session domain, restart-durable sample identity, wiring-level tests, truthful shaping health, legacy loop stands down
HISTORICAL: 7218654 @ Phase 3 correction item 8 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 7dc27e8 Phase 3 correction item 8 (inventory_head): ADR-0002 single shaping owner - netd applies, acctd derives and submits; structural preflight check
HISTORICAL: baa5596 @ Phase 3 correction item 7 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: de24d2c Phase 3 correction item 7 (inventory_head): controlled Phase-3 accounting ingestion wired into acctd with 11 composition-root cases
HISTORICAL: 9c8fbb4 @ Phase 3 corrections round 3 items 1-5 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: b7c7991 Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence
HISTORICAL: 3566d97 @ Phase 3 corrections round 2 items 5-6 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 8ebce2c Phase 3 corrections round 2 items 5-6 (inventory_head): synchronous fail-closed applier construction + supervised interface reconciliation
HISTORICAL: 3c9c73a @ Phase 3 corrections round 2 items 1-4 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: f322c84 Phase 3 corrections round 2 items 1-4 (inventory_head): mandatory grace package, ONE shared exact validator, typed package selector, mandatory DB-level preconditions: gate 320/320
HISTORICAL: c01d361 @ Phase 3 correction item 4 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 4eb9768 Phase 3 correction item 4 (inventory_head): enforce composed into acctd (true-time expiry + derived shaping reconciliation) with composition-root tests
HISTORICAL: a97800d @ Phase 3 correction item 1 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 86bda6b Phase 3 correction item 1 (inventory_head): pmsd Stay-Event application worker composition root + process-level tests
HISTORICAL: cca5c75 @ Phase 3 corrections round 1 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 8aee1f3 Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310
HISTORICAL: 3a95ec1 @ Phase 3 final report (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 32b7701 Phase 3 final report (inventory_head): 17-section dark acceptance candidate report, live Increment-9 evidence recorded PENDING
HISTORICAL: 960ac3e @ Phase 3 Increment-9 offline tooling + runbook (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 5c6d143 Phase 3 Increment-9 offline tooling + runbook (inventory_head): preflight 11/11, evidence collector, deployment/rollback/reboot runbook
HISTORICAL: 23d9d12 @ Phase 3 guest-portal uniform contract (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: b8f49f4 Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side
HISTORICAL: 2cafd9c @ Phase 3 Hotel-Admin E2E + accessibility (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 1592850 Phase 3 Hotel-Admin E2E + accessibility (inventory_head): 7 Playwright specs over mocked edged, named controls and labelled filters proven
HISTORICAL: 834650c @ Phase 3 Hotel-Admin surface (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: a1f0c4e Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48
HISTORICAL: 7f75249 @ Phase 3 netd shaping plan + acctd enforcement (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 30757b1 Phase 3 netd shaping plan + acctd expiry enforcement (inventory_head): derived plan, true-time window/quota endings with revocation: PG16-green
HISTORICAL: 76d2029 @ Phase 3 F1-F7 flow suite (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: d0f57e0 Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green
HISTORICAL: d18e09b @ Phase 3 sharers + folios + source conflicts (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 0927baa Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green
HISTORICAL: 166ff5b @ Phase 3 strict resolver fan-out + idempotent resolutions (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 32b382b Phase 3 strict resolver fan-out + idempotent auth_resolutions (inventory_head): complete-vector concurrency, fail-closed indeterminacy, >=24 concurrent resolutions: PG16-green
HISTORICAL: 09619e3 @ Phase 3 post-boundary revocation + accounting attribution (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: b8aeca1 Phase 3 post-boundary revocation + accounting attribution intervals/watermarks/delayed samples (inventory_head): PG16-green + gate 282/282
HISTORICAL: 1ffba2b @ Phase 3 atomic grant + controlled device authorization (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: d976419 Phase 3 atomic Auth-Context/Quote/Purchase/Entitlement grant + controlled device authorization (inventory_head): PG16-green + gate 267/267
HISTORICAL: 3010a70 @ Phase 3 bitemporal entitlement history (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: c018f84 Phase 3 bitemporal entitlement history (inventory_head): true effective_at + recorded_at, explicit supersession, boundary termination without clamping: PG16-green + gate 254/254
HISTORICAL: cd24425 @ Phase 3 Increment-7 Checkout scorecard-gap closure (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 362aecd Phase 3 Increment-7 Checkout scorecard-gap closure (inventory_head): no old path, fail-closed, mandatory lineage, structural DB lineage, ordering, >=24 integrated concurrency, late-stage rollback: PG16-green + gate 225/225
HISTORICAL: e43bd28 @ Phase 3 one-transaction Checkout slice (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 60191c3 Phase 3 vertical slice: ONE physical Stay-Event->Checkout transaction + exact event lineage (inventory_head): PG16-green + gate 225/225
HISTORICAL: 56b29b7 @ Phase 3 controlled-writer manifest doc sync (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 324dbeb Phase 3 controlled-writer manifest documentation sync (inventory_head): doc-only
HISTORICAL: f82cef2 @ Phase 3 Increment-7 EXECUTE-only caller proof (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: de9c189 Phase 3 Increment-7 EXECUTE-only caller proof for the controlled-writer model (inventory_head): PG16-green + gate 225/225
HISTORICAL: 7d01e72 @ Phase 3 Increment-7 config-DELETE + per-family writer-owner (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: d5263d7 Phase 3 Increment-7 config-DELETE + per-family writer-owner gaps (inventory_head): PG16-green + gate 209/209
HISTORICAL: 73e6b5d @ Phase 3 Increment-7 controlled-writer first-insert + full-policy (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 5bc3978 Phase 3 Increment-7 controlled-writer first-insert + full-policy gaps (inventory_head): PG16-green + gate 196/196
HISTORICAL: 62f7e7a @ Phase 3 Increment-7 controlled-writer boundary (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 8a224b7 Phase 3 Increment-7 TRUE controlled-writer authorization boundary (inventory_head): PG16-green + gate 188/188
HISTORICAL: d8ed476 @ Phase 3 Increment-7 Checkout unspoofable state machine hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 856fb33 Phase 3 Increment-7 Checkout unspoofable state machine + catalog/alert/provenance hardening (inventory_head): PG16-green + gate 181/181
HISTORICAL: 36c5c62 @ Phase 3 Increment-7 Checkout history-integrity corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: eab5f5e Phase 3 Increment-7 Checkout history-integrity + emergency-catalog + alert + provenance corrections (inventory_head): PG16-green + gate 172/172
HISTORICAL: 66f7029 @ Phase 3 CI-stability localkeys flake fix (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 0b334dd Phase 3 CI-stability: fix internal/localkeys.EnsureGeneration concurrent mid-write flake (inventory_head)
HISTORICAL: 8e91fbf @ Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 483d7cc Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (inventory_head): PG16-green + gate 157/157
HISTORICAL: 4296d29 @ Phase 3 Increment-7 Checkout conversion safety corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 1bf4936 Phase 3 Increment-7 Checkout conversion safety + boundary corrections (inventory_head): fail-closed, boundary-eligibility, durable audit â€” PG16-green + gate 141/141
HISTORICAL: 83f4abf @ Phase 3 Increment-7 atomic Checkout conversion (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 2c0df80 Phase 3 Increment-7 atomic Checkout conversion (inventory_head): Stay-first single-tx checkout+grace, PG16-green
HISTORICAL: 20aaccd @ Phase 3 Auth Context lock-order + evidence-version enforcement + UUID validation (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 20980f3 Phase 3 Auth Context lock-order + evidence-version enforcement + UUID pin validation (inventory_head): PG16-green + lifecycle-gate 131/131
HISTORICAL: fb288cc @ Phase 3 Auth Context snapshot pin + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 49a9cff @ Phase 3 Auth Context episode + evidence-snapshot pin + cast-safe freshness + status sync (inventory_head): PG16-green + lifecycle-gate 121/121
HISTORICAL: 06d2ad9 @ Phase 3 Auth Context provenance + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 3dd3713 @ Phase 3 Auth Context provenance + issuance validation + status sync (inventory_head): PG16-green
HISTORICAL: 453998c @ Phase 3 corrections REJECT_NEW_DEVICE + Auth Context pins (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 96d4c7d @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green
HISTORICAL: f703212 @ Phase 3 Increment 6 Auth Context extension (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: da7de53 @ Phase 3 Increment 6 Auth Context consumption extended (inventory_head): full pinned-context verification + atomic ConsumeTx; PG16-green
HISTORICAL: f360d65 @ Phase 3 Increment 7 corrected grace semantics (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: bfa8159 @ Phase 3 Increment 7 CORRECTED grace semantics (inventory_head): entitlement-based eligibility (origin-agnostic), config-invalid Emergency fallback; green
HISTORICAL: 22b2f64 @ Phase 3 Increment 7 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 83253ea @ Phase 3 Increment 7 foundation (inventory_head): Checkout Grace + Emergency Grace decision core (internal/grace), F4â€“F6
HISTORICAL: 66c9ddf @ Phase 3 Increment 6 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: bab09e9 @ Phase 3 Increment 6 foundation (inventory_head): one-time TTL-bounded PMS Auth Context (internal/authctx), PG16-green
HISTORICAL: 125158c @ Phase 3 Increment 5 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 3efe3f5 @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11
HISTORICAL: d2ef30f @ Phase 3 Increment 4 transactional processor (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: c973ab0 @ Phase 3 Increment 4 transactional processor (inventory_head): consume durable inbox â†’ apply Stay op â†’ terminal event, race-green + PG16-green
HISTORICAL: c42fbb5 @ Phase 3 Increment 4 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: c356a29 @ Phase 3 Increment 4 foundation (inventory_head): deterministic Stay-resolution decision core (internal/stayengine)
HISTORICAL: e6db8ea @ Phase 3 increment 3 Â§9-Â§16 complete (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 5cc06b0 @ Phase 3 increment 3 Â§9-Â§16 COMPLETE: owner-bound AES-GCM AAD (inventory_head); connector hardening finished, race-green
HISTORICAL: 9684921 @ Phase 3 increment 3 Â§9 credential_mode + pin coherence (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: a2e733f @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green
HISTORICAL: e0d126f @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: b0ddce3 @ Phase 3 CI-stability (inventory_head): align Â§F write-failure + malformed-domain tests with the Â§G initial-DR flow
HISTORICAL: 6916513 @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 4bc1872 @ Phase 3 CI-stability (inventory_head): fix concurrency bug in localkeys.CreateKeyIfAbsent (mid-write empty O_EXCL file)
HISTORICAL: a80a369 @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable admission (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 11fc3ff @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green
HISTORICAL: 75d30a0 @ Phase 3 increment 3 Â§G data model + persistence (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 2e4c864 @ Phase 3 increment 3 Â§G data model + persistence (inventory_head): durable resync inbox (reuse stay_events), typed resync generation, immutable-rows + atomic publication boundary, ownership-safe append-first admission; lifecycle-gate 121/121 + PG16-green + race-green
HISTORICAL: 2dc0004 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: c5507c6 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green
HISTORICAL: 4d9f138 @ Phase 3 increment 3 hardening items 1-6 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 6d6914d @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green
HISTORICAL: 9c0c0f6 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: cda3836 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (inventory_head): fix benign measurement race in linearizable-close test
HISTORICAL: bbc8e1d @ Phase 3 increment 3 hardening Â§A-Â§D (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 59cd031 @ Phase 3 increment 3 hardening Â§A-Â§D (inventory_head): finalize Event semantics â€” remove connector-owned Stay identity, complete-record fingerprint, no silent truncation; race-green
HISTORICAL: 308d039 @ Phase 3 increment 3 hardening Â§1-Â§4 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: c71f06a @ Phase 3 increment 3 hardening Â§1-Â§4 (inventory_head): Event-identity split (SourceEventFingerprint vs LogicalStayKey) + dedicated keyed HMAC + corrected timestamp semantics; race-green
HISTORICAL: c93d9a4 @ Phase 3 increment 3 REOPENED (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: a1dda4c @ Phase 3 increment 3 REOPENED (inventory_head): authoritative FIAS field map correction (RN=room, G#=reservation, GN/GF, GA/GD) + deterministic Event identity; status back to HARDENING
HISTORICAL: ffb9f0d @ Phase 3 increment 3 CI-stability hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 62ec099 @ Phase 3 increment 3 CI-stability hardening (inventory_head): robust gate readiness + retry-once on flaky in-job postgres container steps
HISTORICAL: c4bcf64 @ Phase 3 increment 3 COMPLETE (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 2b6d250 @ Phase 3 increment 3 COMPLETE (inventory_head): pmsd runtime + both CIs green on a5e2d3a; increments 4-9 remain
HISTORICAL: a5e2d3a @ Phase 3 increment 3 integration-readiness fix (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: aafae76 @ Phase 3 increment 3 integration-readiness fix (inventory_head): robust postgres readiness in pmsd-pg-integration.sh
HISTORICAL: b70ed9a @ Phase 3 increment 3 software-CI scope fix (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 7f662af @ Phase 3 increment 3 software-CI scope fix (inventory_head): gofmt/vet check the Phase-3 pmsd surface (not pre-existing unformatted packages)
HISTORICAL: 7f283fa @ Phase 3 increment 3 coordinated pmsd rewrite (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: 54ee4d7 @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green
HISTORICAL: b0201db @ Phase 3 increment 3 continuation Â§1-Â§5 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: f2b11f9 @ Phase 3 increment 3 continuation Â§1-Â§5 (inventory_head): linearizable queue + typed Events + logging-PII fix + apply/rollback role split + bootstrap target-kind; gate 117/117, pmsd race-green
HISTORICAL: 3b5c1a9 @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: d015f7d @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green
HISTORICAL: d39404a @ Phase 3 increment 3 Part-A final runner corrections (local checkpoint): mandatory positive target identity + canonical dir + structural ledger verify + explicit checksum + separated bootstrap + deployment-parity service roles; gate 114/114
HISTORICAL: b63d18d @ Phase 3 increment 3 hardening PART A (delivery_head): complete staged manifest + rebuilt packs + pointer
HISTORICAL: c770325 @ Phase 3 increment 3 hardening PART A (inventory_head): 0010 secret-generation pin + event-id immutability + atomic lock-then-ledger runner + target-identity fail-closed + deployment-parity ownership; gate 98/98
HISTORICAL: 323697c @ Phase 3 increment 3 (delivery_head): complete manifest + rebuilt packs + pointer
HISTORICAL: 28858dd @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK
HISTORICAL: 7f16628 @ Phase 3 increment 2 final invariants (delivery_head): complete manifest + rebuilt packs + pointer
HISTORICAL: 2dbe4cd @ Phase 3 increment 2 final invariants (inventory_head): event append-first/terminal rules, grace all-or-none, runner scope hardening
HISTORICAL: 7601f40 @ Phase 3 increment 2 hardening (delivery_head): complete manifest + rebuilt packs + pointer
HISTORICAL: 379a85f @ Phase 3 increment 2 hardening (inventory_head): migration 0010 corrections + authoritative runner + 55/55 gate
HISTORICAL: 6116155 @ Phase 3 increment 2 (delivery_head): complete manifest (base..delivery_head, 54 files) + rebuilt packs + pointer
HISTORICAL: 82330dc @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit
HISTORICAL: 5499534 @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer
HISTORICAL: b08b6cc @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards
```
## 11. All commits created

```text
960ac3e @ Phase 3 Increment-9 offline tooling + runbook (delivery_head): complete staged manifest + rebuilt packs + pointer
5c6d143 Phase 3 Increment-9 offline tooling + runbook (inventory_head): preflight 11/11, evidence collector, deployment/rollback/reboot runbook
23d9d12 @ Phase 3 guest-portal uniform contract (delivery_head): complete staged manifest + rebuilt packs + pointer
b8f49f4 Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side
2cafd9c @ Phase 3 Hotel-Admin E2E + accessibility (delivery_head): complete staged manifest + rebuilt packs + pointer
1592850 Phase 3 Hotel-Admin E2E + accessibility (inventory_head): 7 Playwright specs over mocked edged, named controls and labelled filters proven
834650c @ Phase 3 Hotel-Admin surface (delivery_head): complete staged manifest + rebuilt packs + pointer
a1f0c4e Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48
7f75249 @ Phase 3 netd shaping plan + acctd enforcement (delivery_head): complete staged manifest + rebuilt packs + pointer
30757b1 Phase 3 netd shaping plan + acctd expiry enforcement (inventory_head): derived plan, true-time window/quota endings with revocation: PG16-green
76d2029 @ Phase 3 F1-F7 flow suite (delivery_head): complete staged manifest + rebuilt packs + pointer
d0f57e0 Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green
d18e09b @ Phase 3 sharers + folios + source conflicts (delivery_head): complete staged manifest + rebuilt packs + pointer
0927baa Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green
166ff5b @ Phase 3 strict resolver fan-out + idempotent resolutions (delivery_head): complete staged manifest + rebuilt packs + pointer
32b382b Phase 3 strict resolver fan-out + idempotent auth_resolutions (inventory_head): complete-vector concurrency, fail-closed indeterminacy, >=24 concurrent resolutions: PG16-green
09619e3 @ Phase 3 post-boundary revocation + accounting attribution (delivery_head): complete staged manifest + rebuilt packs + pointer
b8aeca1 Phase 3 post-boundary revocation + accounting attribution intervals/watermarks/delayed samples (inventory_head): PG16-green + gate 282/282
1ffba2b @ Phase 3 atomic grant + controlled device authorization (delivery_head): complete staged manifest + rebuilt packs + pointer
d976419 Phase 3 atomic Auth-Context/Quote/Purchase/Entitlement grant + controlled device authorization (inventory_head): PG16-green + gate 267/267
3010a70 @ Phase 3 bitemporal entitlement history (delivery_head): complete staged manifest + rebuilt packs + pointer
c018f84 Phase 3 bitemporal entitlement history (inventory_head): true effective_at + recorded_at, explicit supersession, boundary termination without clamping: PG16-green + gate 254/254
cd24425 @ Phase 3 Increment-7 Checkout scorecard-gap closure (delivery_head): complete staged manifest + rebuilt packs + pointer
362aecd Phase 3 Increment-7 Checkout scorecard-gap closure (inventory_head): no old path, fail-closed, mandatory lineage, structural DB lineage, ordering, >=24 integrated concurrency, late-stage rollback: PG16-green + gate 225/225
e43bd28 @ Phase 3 one-transaction Checkout slice (delivery_head): complete staged manifest + rebuilt packs + pointer
60191c3 Phase 3 vertical slice: ONE physical Stay-Event->Checkout transaction + exact event lineage (inventory_head): PG16-green + gate 225/225
56b29b7 @ Phase 3 controlled-writer manifest doc sync (delivery_head): complete staged manifest + rebuilt packs + pointer
324dbeb Phase 3 controlled-writer manifest documentation sync (inventory_head): doc-only
f82cef2 @ Phase 3 Increment-7 EXECUTE-only caller proof (delivery_head): complete staged manifest + rebuilt packs + pointer
de9c189 Phase 3 Increment-7 EXECUTE-only caller proof for the controlled-writer model (inventory_head): PG16-green + gate 225/225
7d01e72 @ Phase 3 Increment-7 config-DELETE + per-family writer-owner (delivery_head): complete staged manifest + rebuilt packs + pointer
d5263d7 Phase 3 Increment-7 config-DELETE + per-family writer-owner gaps (inventory_head): PG16-green + gate 209/209
73e6b5d @ Phase 3 Increment-7 controlled-writer first-insert + full-policy (delivery_head): complete staged manifest + rebuilt packs + pointer
5bc3978 Phase 3 Increment-7 controlled-writer first-insert + full-policy gaps (inventory_head): PG16-green + gate 196/196
62f7e7a @ Phase 3 Increment-7 controlled-writer boundary (delivery_head): complete staged manifest + rebuilt packs + pointer
8a224b7 Phase 3 Increment-7 TRUE controlled-writer authorization boundary (inventory_head): PG16-green + gate 188/188
d8ed476 @ Phase 3 Increment-7 Checkout unspoofable state machine hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
856fb33 Phase 3 Increment-7 Checkout unspoofable state machine + catalog/alert/provenance hardening (inventory_head): PG16-green + gate 181/181
36c5c62 @ Phase 3 Increment-7 Checkout history-integrity corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
eab5f5e Phase 3 Increment-7 Checkout history-integrity + emergency-catalog + alert + provenance corrections (inventory_head): PG16-green + gate 172/172
66f7029 @ Phase 3 CI-stability localkeys flake fix (delivery_head): complete staged manifest + rebuilt packs + pointer
0b334dd Phase 3 CI-stability: fix internal/localkeys.EnsureGeneration concurrent mid-write flake (inventory_head)
8e91fbf @ Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
483d7cc Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (inventory_head): PG16-green + gate 157/157
4296d29 @ Phase 3 Increment-7 Checkout conversion safety corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
1bf4936 Phase 3 Increment-7 Checkout conversion safety + boundary corrections (inventory_head): fail-closed, boundary-eligibility, durable audit â€” PG16-green + gate 141/141
83f4abf @ Phase 3 Increment-7 atomic Checkout conversion (delivery_head): complete staged manifest + rebuilt packs + pointer
2c0df80 Phase 3 Increment-7 atomic Checkout conversion (inventory_head): Stay-first single-tx checkout+grace, PG16-green
20aaccd @ Phase 3 Auth Context lock-order + evidence-version enforcement + UUID validation (delivery_head): complete staged manifest + rebuilt packs + pointer
20980f3 Phase 3 Auth Context lock-order + evidence-version enforcement + UUID pin validation (inventory_head): PG16-green + lifecycle-gate 131/131
fb288cc @ Phase 3 Auth Context snapshot pin + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
49a9cff @ Phase 3 Auth Context episode + evidence-snapshot pin + cast-safe freshness + status sync (inventory_head): PG16-green + lifecycle-gate 121/121
06d2ad9 @ Phase 3 Auth Context provenance + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
3dd3713 @ Phase 3 Auth Context provenance + issuance validation + status sync (inventory_head): PG16-green
453998c @ Phase 3 corrections REJECT_NEW_DEVICE + Auth Context pins (delivery_head): complete staged manifest + rebuilt packs + pointer
96d4c7d @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green
f703212 @ Phase 3 Increment 6 Auth Context extension (delivery_head): complete staged manifest + rebuilt packs + pointer
da7de53 @ Phase 3 Increment 6 Auth Context consumption extended (inventory_head): full pinned-context verification + atomic ConsumeTx; PG16-green
f360d65 @ Phase 3 Increment 7 corrected grace semantics (delivery_head): complete staged manifest + rebuilt packs + pointer
bfa8159 @ Phase 3 Increment 7 CORRECTED grace semantics (inventory_head): entitlement-based eligibility (origin-agnostic), config-invalid Emergency fallback; green
22b2f64 @ Phase 3 Increment 7 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
83253ea @ Phase 3 Increment 7 foundation (inventory_head): Checkout Grace + Emergency Grace decision core (internal/grace), F4â€“F6
66c9ddf @ Phase 3 Increment 6 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
bab09e9 @ Phase 3 Increment 6 foundation (inventory_head): one-time TTL-bounded PMS Auth Context (internal/authctx), PG16-green
125158c @ Phase 3 Increment 5 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
3efe3f5 @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11
d2ef30f @ Phase 3 Increment 4 transactional processor (delivery_head): complete staged manifest + rebuilt packs + pointer
c973ab0 @ Phase 3 Increment 4 transactional processor (inventory_head): consume durable inbox â†’ apply Stay op â†’ terminal event, race-green + PG16-green
c42fbb5 @ Phase 3 Increment 4 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
c356a29 @ Phase 3 Increment 4 foundation (inventory_head): deterministic Stay-resolution decision core (internal/stayengine)
e6db8ea @ Phase 3 increment 3 Â§9-Â§16 complete (delivery_head): complete staged manifest + rebuilt packs + pointer
5cc06b0 @ Phase 3 increment 3 Â§9-Â§16 COMPLETE: owner-bound AES-GCM AAD (inventory_head); connector hardening finished, race-green
9684921 @ Phase 3 increment 3 Â§9 credential_mode + pin coherence (delivery_head): complete staged manifest + rebuilt packs + pointer
a2e733f @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green
e0d126f @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
b0ddce3 @ Phase 3 CI-stability (inventory_head): align Â§F write-failure + malformed-domain tests with the Â§G initial-DR flow
6916513 @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
4bc1872 @ Phase 3 CI-stability (inventory_head): fix concurrency bug in localkeys.CreateKeyIfAbsent (mid-write empty O_EXCL file)
a80a369 @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable admission (delivery_head): complete staged manifest + rebuilt packs + pointer
11fc3ff @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green
75d30a0 @ Phase 3 increment 3 Â§G data model + persistence (delivery_head): complete staged manifest + rebuilt packs + pointer
2e4c864 @ Phase 3 increment 3 Â§G data model + persistence (inventory_head): durable resync inbox (reuse stay_events), typed resync generation, immutable-rows + atomic publication boundary, ownership-safe append-first admission; lifecycle-gate 121/121 + PG16-green + race-green
2dc0004 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (delivery_head): complete staged manifest + rebuilt packs + pointer
c5507c6 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green
4d9f138 @ Phase 3 increment 3 hardening items 1-6 (delivery_head): complete staged manifest + rebuilt packs + pointer
6d6914d @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green
9c0c0f6 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
cda3836 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (inventory_head): fix benign measurement race in linearizable-close test
bbc8e1d @ Phase 3 increment 3 hardening Â§A-Â§D (delivery_head): complete staged manifest + rebuilt packs + pointer
59cd031 @ Phase 3 increment 3 hardening Â§A-Â§D (inventory_head): finalize Event semantics â€” remove connector-owned Stay identity, complete-record fingerprint, no silent truncation; race-green
308d039 @ Phase 3 increment 3 hardening Â§1-Â§4 (delivery_head): complete staged manifest + rebuilt packs + pointer
c71f06a @ Phase 3 increment 3 hardening Â§1-Â§4 (inventory_head): Event-identity split (SourceEventFingerprint vs LogicalStayKey) + dedicated keyed HMAC + corrected timestamp semantics; race-green
c93d9a4 @ Phase 3 increment 3 REOPENED (delivery_head): complete staged manifest + rebuilt packs + pointer
a1dda4c @ Phase 3 increment 3 REOPENED (inventory_head): authoritative FIAS field map correction (RN=room, G#=reservation, GN/GF, GA/GD) + deterministic Event identity; status back to HARDENING
ffb9f0d @ Phase 3 increment 3 CI-stability hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
62ec099 @ Phase 3 increment 3 CI-stability hardening (inventory_head): robust gate readiness + retry-once on flaky in-job postgres container steps
c4bcf64 @ Phase 3 increment 3 COMPLETE (delivery_head): complete staged manifest + rebuilt packs + pointer
2b6d250 @ Phase 3 increment 3 COMPLETE (inventory_head): pmsd runtime + both CIs green on a5e2d3a; increments 4-9 remain
a5e2d3a @ Phase 3 increment 3 integration-readiness fix (delivery_head): complete staged manifest + rebuilt packs + pointer
aafae76 @ Phase 3 increment 3 integration-readiness fix (inventory_head): robust postgres readiness in pmsd-pg-integration.sh
b70ed9a @ Phase 3 increment 3 software-CI scope fix (delivery_head): complete staged manifest + rebuilt packs + pointer
7f662af @ Phase 3 increment 3 software-CI scope fix (inventory_head): gofmt/vet check the Phase-3 pmsd surface (not pre-existing unformatted packages)
7f283fa @ Phase 3 increment 3 coordinated pmsd rewrite (delivery_head): complete staged manifest + rebuilt packs + pointer
54ee4d7 @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green
b0201db @ Phase 3 increment 3 continuation Â§1-Â§5 (delivery_head): complete staged manifest + rebuilt packs + pointer
f2b11f9 @ Phase 3 increment 3 continuation Â§1-Â§5 (inventory_head): linearizable queue + typed Events + logging-PII fix + apply/rollback role split + bootstrap target-kind; gate 117/117, pmsd race-green
3b5c1a9 @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (delivery_head): complete staged manifest + rebuilt packs + pointer
d015f7d @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green
d39404a @ Phase 3 increment 3 Part-A final runner corrections (local checkpoint): mandatory positive target identity + canonical dir + structural ledger verify + explicit checksum + separated bootstrap + deployment-parity service roles; gate 114/114
b63d18d @ Phase 3 increment 3 hardening PART A (delivery_head): complete staged manifest + rebuilt packs + pointer
c770325 @ Phase 3 increment 3 hardening PART A (inventory_head): 0010 secret-generation pin + event-id immutability + atomic lock-then-ledger runner + target-identity fail-closed + deployment-parity ownership; gate 98/98
323697c @ Phase 3 increment 3 (delivery_head): complete manifest + rebuilt packs + pointer
28858dd @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK
7f16628 @ Phase 3 increment 2 final invariants (delivery_head): complete manifest + rebuilt packs + pointer
2dbe4cd @ Phase 3 increment 2 final invariants (inventory_head): event append-first/terminal rules, grace all-or-none, runner scope hardening
7601f40 @ Phase 3 increment 2 hardening (delivery_head): complete manifest + rebuilt packs + pointer
379a85f @ Phase 3 increment 2 hardening (inventory_head): migration 0010 corrections + authoritative runner + 55/55 gate
6116155 @ Phase 3 increment 2 (delivery_head): complete manifest (base..delivery_head, 54 files) + rebuilt packs + pointer
82330dc @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit
5499534 @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer
b08b6cc @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards
```

## 12. Branch and PR information

- **Branch:** `phase/3-stay-resolution-grace`
- **PR URL:** https://github.com/aibrahiiim1/StayConnectEnterprise/pull/6 (**OPEN, UNMERGED**)
- **PR base ← head:** `master` ← `phase/3-stay-resolution-grace`
- **CI on the delivery HEAD (both on the same pushed HEAD):**
  - **Phase 3 Software CI** — job `phase3-full-software-gate`. One run executes the WHOLE software gate:
    gofmt over the entire Phase-3 Go surface, `go build`, full `go vet`, the whole Go unit suite, the race
    detector over every Phase-3 concurrency-sensitive package, the Migration 0010 lifecycle gate, all eleven
    disposable-PG16 integration suites, the offline preflight, and — under a locked Node install from
    `hotel-admin/` — TypeScript typecheck, Vitest, the production build with Phase-3 flags OFF, and the
    Playwright browser suite (Hotel-Admin pages + guest-portal real template + accessibility). After every
    step passes it assembles and **uploads a downloadable evidence artifact**, `phase3-software-evidence-<HEAD>`
    (retention 90 days), whose `RUN_META.json` records the delivery/inventory/base HEADs, the run id, the UTC
    window, tool versions, lock/migration hashes, per-step exit codes and durations, per-suite test totals and
    skip totals, infrastructure retries, the restrictions confirmation and the Live-Increment-9 pending list,
    plus a `MANIFEST.sha256` over every file in the artifact.
  - **Project Governance** — SUCCESS on the same HEAD.
  - The numeric **run IDs**, the **artifact ID**, the **artifact size/retention** and the
    **integrity-manifest SHA-256** are recorded in the PR #6 body. They are run metadata and cannot be
    embedded in the commit they describe — the same self-reference rule the change manifest already follows.
  - **Correction of record:** an earlier revision of this report and the PR body stated that the Software CI
    proved the Vitest and Playwright suites and published the evidence artifact. That was not true of the
    then-current workflow, which ran only the Go/backend steps; the frontend suites had been run on a
    workstation. This is the corrected, full gate. No historical run is described as having contained steps it
    did not run.

## 13. Remote reachability of HEAD

- **Delivery HEAD:** the current tip of `phase/3-stay-resolution-grace`, pushed to
  `origin/phase/3-stay-resolution-grace` and identical local/remote. A frozen SHA is deliberately **not**
  written here: the delivery-only commit that adds this report cannot cite its own hash, so the authoritative
  delivery HEAD lives in `governance/project-state.json` (`acceptance_candidate_head` = inventory_head, one
  commit below the delivery HEAD) and in the PR #6 body, which also records the same-HEAD run IDs, the
  evidence artifact id/name/size/retention, and the integrity-manifest SHA-256.
- **Match:** local == remote (the push in §11 is fast-forward).

## 14. Full working-tree status

```text
(clean)
```

## 15. Documentation and governance synchronization

- `governance/project-state.json` carries the authoritative Phase-3 narrative and the pointer fields
  (`acceptance_candidate_head`, `inventory_head`) for this delivery; `PROJECT_STATE_GOVERNANCE = PASS`.
- `docs/manifests/Phase3-change-manifest.md` regenerated for the complete `base..delivery_head` path set.
- `docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md` added (deploy / prove-dark / reboot / rollback).
- `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` remains **PREPARED, NOT APPLIED**.
- Export packs rebuilt deterministically with the source commit recorded.

## 16. Project / Evidence Pack paths and checksums

- `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip`
- `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip`
- `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip`

SHA-256 values are recorded in `exports/chatgpt/*/PACK_SHA256SUMS.txt`, regenerated with the packs.

**The authoritative Phase-3 software evidence is the artifact the Software CI uploads on the delivery HEAD**
(`phase3-software-evidence-<HEAD>`), not a repository ZIP. It is downloadable from the exact successful
Software run and contains only Phase-3 evidence.

**Its integrity is stated exactly**, because "N/N verified" is easy to write and easy to get wrong:

- the artifact contains **18 files in total**;
- `MANIFEST.sha256` holds **17 entries**, one for every other file — it cannot list itself, since a file
  cannot contain its own digest;
- `sha256sum -c MANIFEST.sha256` therefore verifies **those 17 payload files**;
- `MANIFEST.sha256` itself is identified separately, by the integrity-manifest SHA-256 recorded in the PR body
  and printed by the workflow.

It is wrong to describe this as "18/18 entries passed `sha256sum -c`". The
committed export packs above are the project/plan packs; **the older Phase-1A live-dark acceptance pack was
NOT reused, renamed or repurposed as Phase-3 evidence** — the Phase-3 artifact is generated fresh, in CI, per
run, and its `RUN_META.json` embeds this delivery HEAD.

`scripts/phase3-evidence.sh` remains as a local offline convenience bundle and is deliberately **not
committed**: a committed bundle is stale the moment the next commit lands yet still reads as current evidence.
It is not the same-HEAD CI artifact and is not cited as acceptance evidence.

## 17. `PROJECT_STATE_GOVERNANCE` result

**PASS** (`python tools/project-state.py validate`), alongside `ZERO_STALE_LEFTOVERS = PASS` and
`GENERATED_BLOCKS = PASS`.
