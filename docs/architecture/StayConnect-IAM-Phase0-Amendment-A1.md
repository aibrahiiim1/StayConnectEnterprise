# Phase-0 Contract — Amendment A1: Reservation-targeted FIAS posting (RN + G#)

**Status: FINAL — Product-Owner decision D46 (2026-09-29).** Approved by the Product Owner, including the
corrections of 2026-09-29 (answer-specific stay effects; one unresolved room-charge transaction per stay; no
operator bypass of Protel blocks; the room-move invariant). Its replacement wording is applied to
[`StayConnect-IAM-Phase0-Contract.md`](StayConnect-IAM-Phase0-Contract.md), which carries the normative text;
this document is the record of what changed and why.

**Amends:** the FINAL Phase-0 contract (approved 2026-07-16), including its `folio_identity_strategy`
fail-closed amendment of the same date.

**Does not change:** commerce, quotes and purchases, entitlements, checkout grace, STRICT multi-PMS resolution,
payment, the single FIAS connection owned by pmsd (D45), programmatic reversal (still `capability=false`), or
the rule that real PMS posting stays disabled (`STAYCONNECT_PHASE4_PMS_TRANSMIT` set nowhere) until the vendor
confirmations in §A9 are complete.

---

## A0. Basis

Confirmed by the Product Owner from an existing production-style Protel/FIAS integration, and consistent with
the Gate 3A live evidence (2026-07-16, Hotel ID 3):

- `G#` is the Protel reservation number. It is unique and is not reused.
- The guest data Protel sends (GI/GC/GO) carries the reservation number.
- A direct guest posting uses both the room number and the reservation number.
- Room number alone does not guarantee that the charge reaches the guest folio. Protel may acknowledge a
  posting that carries no reservation number without routing it to the intended guest folio.
- The working integration never posts without a reservation number, and uses no separate external folio
  identifier.
- Protel answers each posting with a result status (success, no guest, no-post restriction, night audit,
  invalid room, retry, …).

Consequence: the posting target is the **reservation**. The FIAS posting used here has no field for selecting a
folio or folio window, so a separate folio identity is neither required by Protel nor usable by this
integration.

## A1. Definitions (new, §1 Glossary)

- **Posting identity:** `(pms_interface_id, G#)` — the PMS interface and the reservation number of the stay the
  purchase was made for. It is pinned on the posting and never re-resolved.
- **Targeting fields:** the `RN` and `G#` sent on a `PS`. `G#` is always the pinned reservation number. `RN` is
  the **current** room of that same reservation, re-read immediately before each attempt is built (§9a rule 7).
- **Definite non-posted outcome:** a posting that provably never reached the PMS (refused before transmission),
  or a `PA` whose status is not `OK`.
- **Posting block:** a recorded, reasoned stop on room charging for one stay (§4.2).

## A2. Summary of changes

| Contract location | Change |
|---|---|
| §2 invariants 2, 8, 9 | Folio removed from the namespace/pin lists; RN refresh defined; posting permission redefined |
| §3 ERD | `stay_folios` / `folios` removed from the posting path; revision carries the posting-target model |
| §4.1 `pms_interface_revisions` + 2026-07-16 folio amendment | `folio_identity_strategy` replaced by `posting_target_model` (fail-closed `UNSET`) |
| §4.2 stays, folios, stay_folios | Posting permission and blocks defined; `folios` / `stay_folios` removed from the contract |
| §4.5 `pms_postings`, `posting_attempts` | Pins the stay and its `G#`, not a folio; attempt `RN` is the refreshed room; `G#` mandatory; only one unresolved room-charge transaction per stay at a time |
| §8 item 6 | "No folio at authentication" replaced by reservation pinning at purchase |
| §9 (freshness, capability matrix) | Stay revalidation instead of stay/folio; `folio_identity` capability replaced |
| §9a rules 1–6 | Rule 6 replaced; new rules 7–10 (refresh before send, answer handling, stay blocks, no windows) |
| §9c Tier 2 | Onboarding records the posting-target model and the vendor confirmations |
| §9d | New deferred limitation: multi-folio/window placement unverified |
| §16 PMS Posting state machine | Precondition, pre-send abort and no automatic retry stated |
| §19 tests E4, E4b, E5, E6, E12 | Replaced; new tests E13–E17 |

---

## A3. Replacement wording — §2 Product Invariants

**§2 invariant 2.** Replace the first sentence

> Every Room, Stay, Guest, Folio, Event, Purchase, and Posting lives in exactly one PMS-interface namespace.

with

> Every Room, Stay, Guest, Event, Purchase, and Posting lives in exactly one PMS-interface namespace, and a
> reservation number is unique only within its interface.

The rest of invariant 2 is unchanged ("Room Number is evidence, never identity or financial ownership" stays:
the room is a targeting field, the reservation is the identity).

**§2 invariant 8.** Replace

> Financial commands pin at the DB layer: PMS interface, interface revisions (authentication and posting
> separately), secret generation, package revision, settlement-mapping row, Stay, Folio, and the exact
> Settlement/Purchase pair. Retries never re-resolve anything.

with

> Financial commands pin at the DB layer: PMS interface, interface revisions (authentication and posting
> separately), secret generation, package revision, settlement-mapping row, Stay, the Stay's reservation
> number (`G#`), and the exact Settlement/Purchase pair. Nothing is re-resolved, with one exception: the room
> number sent on a `PS` is re-read from the same pinned reservation immediately before each attempt is built
> (§9a rule 7). The reservation is never re-resolved.

**§2 invariant 9.** Replace

> `posting_allowed = true` requires `IN_HOUSE` (DB CHECK), but IN_HOUSE grants nothing by itself: posting
> permission is evaluated from PMS flags, open folios, credit policy, and administrative blocks, with recorded
> reason, source, and check timestamp.

with

> `posting_allowed = true` requires `IN_HOUSE` (DB CHECK). A stay is postable when it is `IN_HOUSE`, carries a
> reservation number (`G#`), and has no active posting block (§4.2); each evaluation records its reason, source
> and check timestamp. IN_HOUSE without a reservation number is never postable. Data freshness is checked
> separately at admission and again before sending (§9, §9a rule 7). The PMS is the authority for no-post and
> credit restrictions at the moment of posting: its `PA` status decides the result and places a posting block
> (§9a rules 8–9). Where the vendor supplies a no-post or credit indicator in the guest feed, it is an
> additional block, never a substitute for the PMS answer.

The final sentence of invariant 9 (checkout ends posting; trusted Reinstatement re-evaluates) is unchanged.

## A4. Replacement wording — §3 Domain Model

In the ERD, replace

> `pms_interface_revisions` … folio identity strategy; MEASURED capabilities)

with

> `pms_interface_revisions` … posting target model; MEASURED capabilities)

and remove the line

> `├──< stay_folios >── folios (identity-strategy aware)`

## A5. Replacement wording — §4 Canonical DDL

**§4.1 `pms_interface_revisions`.** Replace the `folio_identity_strategy` column and its comments with

```sql
  posting_target_model text NOT NULL DEFAULT 'UNSET'           -- FAIL-CLOSED: 'UNSET' blocks every
    CHECK (posting_target_model IN ('UNSET','RESERVATION')),   -- financial CHARGE until property onboarding
                                                               -- records 'RESERVATION' (§9c Tier 2).
    -- 'RESERVATION' = a PS targets RN + G#; G# is the vendor-confirmed, non-reused reservation number.
    -- Recording it creates a NEW immutable interface revision; it never mutates this one.
    -- ('UNSET' is the ONLY unset sentinel — 'UNKNOWN' remains a financial Posting state.)
```

Replace the 2026-07-16 NORMATIVE `folio_identity_strategy` amendment block with

> **NORMATIVE — posting-target fail-closed rule (Amendment A1, supersedes the 2026-07-16
> `folio_identity_strategy` amendment).** `posting_target_model` is `NOT NULL DEFAULT 'UNSET'` with the CHECK
> `('UNSET','RESERVATION')`.
> - An interface revision with `posting_target_model = 'UNSET'` may be used for read-only PMS ingestion, guest
>   lookup and authentication.
> - While it is `UNSET`, every financial CHARGE is rejected fail-closed, before posting-outbox creation, `P#`
>   allocation or any PMS transmission.
> - Recording `RESERVATION` is done by property financial onboarding (§9c Tier 2), requires the vendor's
>   confirmation that the interface's reservation numbers are unique and never reused, and creates a new
>   immutable interface revision. Postings pin the revision they were built against.

**§4.2 `stays`.** Add after the `posting_allowed` columns:

```sql
  -- external_reservation_id is the PMS reservation number. For a FIAS interface it is the G# used on every PS.
  -- posting_allowed = IN_HOUSE ∧ external_reservation_id present ∧ no active posting block (§2 inv. 9).
  -- posting_block_reason ∈ ('NO_RESERVATION','PMS_NO_POST','PMS_DATA_SUSPECT',
  --                         'POSTING_UNRESOLVED','ADMIN_BLOCK', …) with posting_permission_source
  --                         ('PMS_FEED','PMS_ANSWER','POSTING_LEDGER','OPERATOR') and posting_checked_at.
```

**§4.2 `folios` and `stay_folios`.** Replace the `folios` table, its identity-strategy comment and the
`stay_folios` comment with

> **Folios are not modelled (Amendment A1).** The posting target is the reservation (`G#`) within its
> interface. There is no `folios` or `stay_folios` table in the posting path, no external folio identifier, no
> folio discovery and no default-folio selection. How Protel places a charge inside a reservation that holds
> several folios or windows is an open vendor question (§9d); this integration never models or selects a
> folio window.

**§4.5 `pms_postings`.** In the comment block, replace

> plus composite FKs (all tenant/site/interface-scoped) to stays, folios, stay_folios(stay_id, folio_id), …
> INSERT trigger re-reads stays (IN_HOUSE ∧ posting_allowed, except REVERSAL)

with

> plus composite FKs (all tenant/site/interface-scoped) to stays, package_settlement_mappings (incl.
> package_revision_id), pms_interface_revisions (posting_interface_revision_id),
> pms_interface_secret_generations; `g_number` snapshotted from the stay at creation and immutable; INSERT
> trigger re-reads the stay (IN_HOUSE ∧ posting_allowed ∧ `g_number` = the stay's reservation number, except
> REVERSAL). Only one unresolved / in-flight / UNKNOWN room-charge transaction may exist for a stay at a time:
> a new CHARGE is refused while the same stay has a CHARGE in PENDING, SENDING, UNKNOWN or MANUAL_REVIEW. A
> CHARGE that has reached a terminal state (POSTED or FAILED_FINAL) does not count, so a guest may make any
> number of legitimate sequential purchases during the same stay.

**§4.5 `posting_attempts`.** Replace the `rn, g_number` column line with

```sql
  rn text NOT NULL,                          -- the stay's CURRENT room, re-read when this attempt is built (§9a rule 7)
  g_number text NOT NULL,                    -- must equal pms_postings.g_number (trigger-enforced); never room-only
  outcome text NOT NULL DEFAULT 'SENDING'
    CHECK (outcome IN ('SENDING','ACKED','UNKNOWN','NOT_SENT')),
    -- SENDING  = durable, handed towards pmsd; from here the PS MAY have been transmitted.
    -- NOT_SENT = pmsd proved no byte was written (refused before its single writer wrote the first byte),
    --            with the reason in posting_attempt_events. It is not a PMS failure and not UNKNOWN.
    -- ACKED    = a PA matched by interface + P#; UNKNOWN = transmitted or possibly transmitted, not proven.
```

and extend the trigger comment:

> An attempt refused before transmission (provably not written) is `NOT_SENT` with a pre-send reason and
> consumes no guest charge. Only a `NOT_SENT` attempt may be followed automatically by a new attempt; it is not a
> retry, because nothing was transmitted (§9a rule 7). Historical attempts are never rewritten.

## A6. Replacement wording — §8 item 6

Replace

> **No folio at authentication.** Folio selection, freshness revalidation, and pinning happen inside the
> purchase transaction: lock Stay → verify IN_HOUSE ∧ posting_allowed → fresh stay/folio revalidation per the
> revision's policy → select the OPEN default posting target from `stay_folios` → pin folio + posting
> interface revision + secret generation atomically.

with

> **No posting target at authentication.** Revalidation and pinning happen inside the purchase transaction:
> lock Stay → verify IN_HOUSE ∧ posting_allowed ∧ reservation number present ∧ no unresolved room-charge transaction on the stay →
> fresh stay revalidation per the revision's policy → pin the Stay, its reservation number (`G#`), the posting
> interface revision and the secret generation atomically. The room number is not pinned; it is re-read before
> sending (§9a rule 7).

## A7. Replacement wording — §9, §9a, §9c, §9d, §16

**§9, paragraph "Authentication requires axis 4…".** Replace "fresh stay/folio revalidation" with "fresh stay
revalidation (same reservation, IN_HOUSE, not blocked, room resolvable)".

**§9, capability paragraph.** Replace

> folio-number reuse behavior (drives `folio_identity_strategy`). Results populate the per-revision capability
> matrix: `can_post, supports_idempotency, read_back, reversal, folio_identity, room_only_posting, safe_retry`.
> … **A new interface revision starts with `folio_identity_strategy = 'UNSET'` …**

with

> reservation-number uniqueness and non-reuse (drives `posting_target_model`, vendor-confirmed). Results
> populate the per-revision capability matrix: `can_post, supports_idempotency, read_back, reversal,
> reservation_target, room_only_posting, safe_retry` (`room_only_posting` is always false: a room-only `PS` is
> never sent). **A new interface revision starts with `posting_target_model = 'UNSET'` (fail-closed): every
> financial CHARGE is rejected until property onboarding records `RESERVATION` in a new revision (§4.1, §9a
> rule 6).**

**§9a rule 1** is unchanged. Add to it: "An UNKNOWN charge places a `POSTING_UNRESOLVED` block on its stay
(rule 9)."

**§9a rule 6.** Replace entirely with

> 6. **Fail-closed posting target (`posting_target_model = 'UNSET'`) blocks all financial CHARGE (Amendment
> A1).** While `UNSET`, read-only PMS ingestion, guest lookup and authentication are permitted, but every
> CHARGE is rejected before posting-outbox creation, before `P#` allocation and before any transmission.
> Posting becomes possible only once property onboarding (§9c Tier 2) records `RESERVATION` in a new
> immutable revision.

**§9a new rule 7 — room refresh before sending.**

> 7. **G# is stable. RN is mutable. RN is refreshed before sending. After sending begins, the attempt is
> immutable.**
> - **At purchase** the posting pins the interface and the reservation (`G#`). The room is not pinned.
> - **Before an attempt is created** the worker locks the pinned stay and re-reads it by interface + `G#`. It
>   continues only if it is the same reservation, IN_HOUSE, the interface's freshness axes are green, the stay
>   has no posting block and its current room resolves. The attempt is then built with **the current room + the
>   same `G#`**, and that `RN`, the exact `PS` bytes and their hash are recorded immutably. A room move between
>   purchase and attempt creation therefore does not fail the purchase. It is aborted — definitely not posted,
>   purchase FAILED, no access — only when the reservation is no longer IN_HOUSE, its data is stale, the stay is
>   blocked, or its current room cannot be resolved.
> - **Immediately before the socket write** pmsd revalidates, inside its single serialized writer, that the same
>   `G#` still exists and is postable and that its current room — from the database and from any newer guest
>   record already read on the link — still equals the `RN` in the prepared command. If not, it writes nothing
>   and answers not-transmitted with the reason; the attempt becomes **`NOT_SENT`** (audited, not a PMS failure
>   and not UNKNOWN), fresh state is read, and a new attempt may be built with the new room and the same `G#`.
>   That is not a retry, because nothing was transmitted.
> - **Once the first byte of the `PS` is written** the attempt is immutable. A later room move never rebuilds it
>   and never causes another `PS` to be sent automatically. The `RN`/`G#` it carried remain its record; its `PA`
>   or its timeout decides the outcome, and an outcome that cannot be proven is UNKNOWN, which blocks further
>   room charging until manual resolution.
> - **A room move** (GC) updates the current room of the existing reservation atomically. It never creates a
>   second stay; the same `G#` continues to identify the stay. A `PS` is never sent without `G#`.
> - **What cannot be seen.** A guest record that is still in transit on the link when the `PS` is written cannot
>   be seen by anyone; that attempt is decided by its `PA` (or is UNKNOWN), never by a guess. **"Stale" includes
>   a disconnected link and a resync in progress**: a queued charge found in either state is aborted (definitely not
>   posted, purchase FAILED) rather than held, and the guest can buy again once the link is fresh.

**§9a new rule 8 — the PMS answer decides; our safety policy.**

> 8. **`PA` status is authoritative for the posting result.** `OK` settles the charge and grants access. A
> **definite non-posted** answer — a status the vendor has confirmed means nothing was posted (the only codes
> that can be confirmed so are `NP`, `NG`, `NR`, `NA` and `RY`; the confirmation is recorded per interface,
> §A9) — **always fails that purchase**: no access is granted, the
> answer is recorded, and the client may choose again. Whether it also affects the **stay** depends on the
> status (rule 9); most do not. An answer whose posting effect cannot be proven — no answer, a status outside
> the catalogue, or a catalogue status not yet vendor-confirmed as definitely not posted (for example `UR`) —
> is **UNKNOWN** (rule 1), never success and never failure.
>
> **Safety policy (OneGate's, not a Protel protocol requirement):** a definite non-posted answer — including
> night audit and "retry" — is never retried automatically. The purchase fails and a later purchase is a new
> charge. This may be revisited only by a later amendment once the vendor has confirmed which answers
> guarantee that nothing was posted.

**§9a new rule 9 — stay blocks.**

> 9. **Stay effect depends on the answer.** A failed purchase does not by itself block the stay.
>
> | Answer | Purchase | Stay |
> |---|---|---|
> | `NP` (no-post restriction) | FAILS | Blocked for room charge (`PMS_NO_POST`) until fresh authoritative Protel data explicitly allows posting again |
> | `NG` / `NR` (guest or room not found) | FAILS | Data marked suspect (`PMS_DATA_SUSPECT`), resync requested, room charge blocked until fresh valid PMS data confirms the same reservation is IN_HOUSE with a resolvable current room |
> | `NA` (night audit) | FAILS | No stay block; a later new purchase may be attempted when the PMS/interface is available again |
> | `RY` / vendor-confirmed "definitely not posted, try later" | FAILS, never retried automatically | No persistent stay block, unless the answer specifically identifies a stay-level data problem, in which case it is handled as `NG` / `NR` |
> | UNKNOWN, or an answer whose posting effect cannot be proven | Held for manual review | Blocked (`POSTING_UNRESOLVED`) until the manual-review decision on that charge resolves it |
>
> A stay without a reservation number carries `NO_RESERVATION` and is never postable. While any block is
> active, room charge is neither offered nor admitted for that stay. **An operator cannot lift
> `PMS_NO_POST` or `PMS_DATA_SUSPECT`** to permit another charge: only fresh authoritative Protel data clears
> them, as the table states. `ADMIN_BLOCK` is set and cleared administratively, because its source is
> administrative. `POSTING_UNRESOLVED` clears only when its charge reaches a terminal state (manual-review
> decision, or a reviewed retry that concludes). Every block and clearance records reason, source and
> timestamp. The code-to-effect mapping applies to a status once the vendor has confirmed its
> meaning (§A9); until then that status is handled as UNKNOWN (rule 8).

**§9a new rule 10 — no folio windows.**

> 10. **The integration never models or selects Protel folio windows.** It posts `RN + G#` with `SO=WIFI` and
> records exactly what it sent. Where the charge lands inside a reservation holding several folios or windows
> is decided by Protel and is an open vendor question (§9d). A single `SO=WIFI` sales outlet is used for every
> package.

**§9c Tier 2.** Replace

> **`RN`+`G#`** folio targeting; **one controlled debit**; **actual Folio placement**; **approved
> cleanup/correction**; and **record one concrete `folio_identity_strategy`** (`GLOBALLY_UNIQUE` /
> `UNIQUE_PER_STAY` / `REUSED_SEQUENTIAL`) — this is what moves the interface **out of fail-closed `UNSET`** …
> Until that concrete strategy is recorded, the interface stays `UNSET` …

with

> **`RN`+`G#`** reservation targeting; **vendor confirmation that the interface's reservation numbers are unique
> and never reused**; **vendor confirmation of the `PA` status meanings** (before real posting is enabled);
> **one controlled debit**; **actual folio placement confirmed by Front Office**; **approved
> cleanup/correction**; and **record `posting_target_model = 'RESERVATION'`** — this is what moves the
> interface **out of fail-closed `UNSET`**, applied as a new immutable interface revision. Until it is
> recorded, the interface stays `UNSET` and **no financial CHARGE is permitted**.

and replace "(and `folio_identity_strategy = 'UNSET'`)" in the Aqua Club sentence with "(and
`posting_target_model = 'UNSET'`)".

Add to Tier 2, as recorded evidence: *Hotel ID 3 (`150.0.0.18:5003`, the PRE-LIVE interface): currency USD,
exponent 2 (owner-confirmed) and `SO=WIFI` → Internet revenue account (Front Office-confirmed) at Gate 3A,
2026-07-16. Reservation-number non-reuse and `PA` status meanings are not yet vendor-confirmed.*

**§9d Deferred limitations.** Add:

> - **Multi-folio / window placement is unverified.** Open vendor question: *"When posting through FIAS with
>   RN + G# and SO=WIFI, if the reservation contains multiple folio/windows, does Protel apply its configured
>   transaction/routing rules automatically, and which window receives the charge?"* Until answered, the
>   integration does not model or select folio windows (§9a rule 10).

**§16 PMS Posting state machine.** Replace

> **precondition (fail-closed):** a financial CHARGE is admitted only when the pinned interface revision's
> `folio_identity_strategy ≠ 'UNSET'` … Then `PENDING → SENDING → POSTED | FAILED_RETRYABLE | FAILED_FINAL |
> UNKNOWN`; `UNKNOWN → MANUAL_REVIEW → POSTED | FAILED_FINAL`; …

with

> **precondition (fail-closed):** a financial CHARGE is admitted only when the pinned interface revision's
> `posting_target_model ≠ 'UNSET'`, the stay is postable (§2 inv. 9) and it has no unresolved / in-flight / UNKNOWN room-charge transaction
> (terminal charges do not count); otherwise
> it is rejected before `PENDING` — no outbox row, no `P#`, no transmission. Then `PENDING → SENDING → POSTED |
> FAILED_FINAL | UNKNOWN`; `PENDING → FAILED_FINAL` when the pre-send revalidation aborts (§9a rule 7);
> `UNKNOWN → MANUAL_REVIEW → POSTED | FAILED_FINAL`, with the accepted `CONFIRM_NOT_POSTED_RETRY` decision
> creating one new attempt under the same posting; reversal is a new REVERSAL row. No state is retried
> automatically after transmission (§9a rules 1 and 8). *(`UNKNOWN` is a Posting state, distinct from the
> `UNSET` sentinel.)*

## A8. Replacement wording — §19 Acceptance matrix (E. Financial)

Unchanged: E1, E2, E3, E7, E8, E9, E10, E11.

- **E4** — replace with: *posting on a non-IN_HOUSE, blocked or reservation-less stay is rejected before
  `PENDING`; a stay that becomes checked out, stale or blocked between purchase and sending aborts before
  transmission (FAILED_FINAL, no grant, no `P#` on the wire).*
- **E4b** — replace with: *`posting_target_model = 'UNSET'` fail-closed: a CHARGE is rejected with no outbox
  row, no `P#` allocation and no transmission; read-only ingestion/lookup/auth still succeed; recording
  `RESERVATION` (new revision) then admits CHARGE.*
- **E5** — replace with: *posting permission is evaluated and recorded (reason, source, timestamp): IN_HOUSE
  without a reservation number is never postable; each block reason is set and cleared as §9a rule 9 states;
  a feed-supplied no-post indicator, where configured, blocks; IN_HOUSE alone grants nothing.*
- **E6** — replace with: *reservation pinned at purchase: a posting's `G#` equals the stay's reservation number
  at creation and never changes; an attempt whose `G#` differs from the posting's is refused by the database.*
- **E12** — replace with: *no folio identity: no posting, attempt or admission path reads or writes a folio,
  folio window or external folio identifier, and a room-only `PS` cannot be built.*
- **E13 (new)** — *room move before sending: the attempt carries the reservation's current room and the same
  `G#`, the purchase is not failed, and a room change detected at the moment of writing produces a new attempt
  with a new `P#` while the first is recorded as provably not written.*
- **E14 (new)** — *missing `G#`: room charge is never offered, never admitted and never built for a stay
  without a reservation number.*
- **E15 (new)** — *definite non-posted answers, by status: each fails its purchase with no grant and no
  automatic retry, and records the status. `NP` blocks room charge on the stay until a fresh Protel update
  explicitly re-allows posting. `NG` / `NR` mark the stay data suspect, request a resync and block room charge
  until fresh valid stay data arrives. `NA`, and a vendor-confirmed `RY`, place no stay block: the same guest
  can make a new room-charge purchase at once. A later purchase is always a new charge.*
- **E16 (new)** — *one unresolved transaction per stay: while a stay has a CHARGE in PENDING, SENDING, UNKNOWN
  or MANUAL_REVIEW, a second room charge is neither offered nor admitted for it; once that charge reaches
  POSTED or FAILED_FINAL, a new purchase on the same stay is admitted, so sequential purchases in one stay
  succeed. An UNKNOWN charge keeps the stay blocked until its manual-review decision.*
- **E18 (new) — room-move races.** Each case is deterministic and proves that no automatic duplicate posting
  can occur:
  1. *move before purchase*: the purchase pins `G#`; the first attempt carries the new room;
  2. *move after purchase, before attempt creation*: the attempt is built with the new room and the same `G#`;
     the purchase is not failed;
  3. *move after attempt creation, before the socket write*: `RN101/G#5000` built → GC moves `G#5000` to
     `RN205` → pmsd receives the prepared command: the `RN101` `PS` is never written, the attempt is `NOT_SENT`
     with a pre-send stale reason, fresh state is read, and the next attempt carries `RN205/G#5000`;
  4. *move while the `PS` is being written*: the attempt is immutable once its first byte is written; its `PA`
     or timeout decides; nothing is resent;
  5. *move after the write, before the `PA`*: `RN101/G#5000` transmitted → GC moves `G#5000` to `RN205` → no
     `PA` yet: `RN205` is never sent automatically, the original attempt stays authoritative, its `PA` or
     timeout decides, and UNKNOWN blocks any new charge until resolved;
  6. *move after the `PA`*: the outcome stands; the attempt's `RN` is never rewritten; a later purchase uses
     the new room.
- **E17 (new)** — *unprovable answer: a status outside the catalogue, or a catalogue status not yet
  vendor-confirmed as definitely not posted, is UNKNOWN — never success or failure — and blocks further room
  charging on the stay until manual review resolves it.*

## A9. Vendor confirmations still required (before real posting is enabled)

1. Reservation numbers (`G#`) are unique and never reused on this interface, including across hotels that share
   a Protel database.
2. The exact meaning of `OK, NG, NA, NP, NR, RY, UR`: which guarantee nothing was posted, and whether any
   non-OK answer ever posts to a room or house account.
3. A posting to a reservation that has checked out, or whose folio is closed, is refused rather than placed on
   another account.
4. Whether Protel rejects or de-duplicates a reused `P#` (governs `CONFIRM_NOT_POSTED_RETRY`).
5. Whether a `PA` can arrive after our timeout, and how late.
6. Night-audit behaviour: refused (with which status) or queued.
7. Whether GI/GC can carry a no-post or credit indicator (field code and meaning) — optional earlier block.
8. *"When posting through FIAS with RN + G# and SO=WIFI, if the reservation contains multiple folio/windows,
   does Protel apply its configured transaction/routing rules automatically, and which window receives the
   charge?"*

Real posting remains disabled until at least items 1–3 are confirmed and one supervised controlled debit has
passed on the property.

## A10. Implementation consequences (for the later implementation step; no code in this amendment)

- A migration replaces `folio_identity_strategy` with `posting_target_model`. Existing values are all `UNSET`
  on PRE-LIVE; no interface is onboarded, so nothing is re-interpreted.
- `pms_postings` loses its `folio_id` FK and gains an immutable `g_number`. `posting_attempts.rn`/`g_number`
  become NOT NULL with the equality trigger, and the rule of one unresolved room-charge transaction per stay
  (terminal charges excluded) is added.
- `folios` and `stay_folios` are removed. Both hold 0 rows on PRE-LIVE.
- The posting worker gains the pre-send stay re-read (rule 7). pmsd's authorisation check compares the
  attempt's `RN` with the stay's current room at the moment of writing.
- Stay blocks (rule 9) are written from the posting ledger and cleared from the PMS feed or by operator
  release.
- Onboarding records `RESERVATION` together with the currency, instead of a folio strategy.
