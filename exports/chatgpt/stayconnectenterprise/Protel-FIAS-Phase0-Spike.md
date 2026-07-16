# Protel FIAS — Phase 0 Live Spike Record

> **Export note:** guest-linked identifiers redacted for external sharing; technical findings preserved verbatim.

**Spike status: `GATE3A_CLOSED — PASS. PROTOCOL ACCEPTED, CORRECT FOLIO VERIFIED, REVENUE MAPPING VERIFIED, MANUAL CLEANUP VERIFIED (P#900002 / Room «ROOM-REDACTED» / G#«RES-REDACTED»). Gates 3B–3D NOT started.`**

### Gate 3A — CLOSURE (Front Office verified, 2026-07-16)

Product owner completed out-of-band Front Office verification for **Attempt #5 (`P#900002`)** and closed the gate:

| Verification item | Result |
|---|---|
| Room «ROOM-REDACTED» / Reservation G#«RES-REDACTED» received the USD 1.00 debit | **YES** |
| Debit appeared on the **correct guest Folio** | **YES** |
| `SO=WIFI` posted to the **intended Internet revenue mapping** | **YES** |
| Front Office **manually corrected/removed** the USD 1.00 debit | **YES** |
| Folio returned to its **exact original balance** | **YES** |

**Gate 3A verdict: `PASS — PROTOCOL ACCEPTED, CORRECT FOLIO VERIFIED, REVENUE MAPPING VERIFIED, MANUAL CLEANUP VERIFIED`.** End-to-end financial correctness of a StayConnect FIAS `PS` debit against live Protel (Hotel ID 3) is now proven: protocol acceptance (`PA ASOK`), correct-folio placement, correct revenue mapping, and clean reversibility by Front Office to net-zero.

**Production-grounded findings from Gate 3A (carried into the Phase-0 contract §9):**

1. **Verified link-startup sequence:** Server `LS` → Client `LS` → Client `LD` → Client `LR` → `LA` acknowledgments. The client must send `LS/LD/LR` immediately on connect and ack incoming `LS`/`LA` with `LA|`; gating on a client-side "reach LA first" milestone stalls the link.
2. **Single active client slot:** the PMS Interface allows exactly **one** active client connection at a time.
3. **Single-owner lock:** production implementation must enforce a **single-owner lock per PMS Interface** (only one connector/harness may hold a given Interface).
4. **Guaranteed cleanup:** test harnesses and connectors must guarantee socket/process cleanup via **bounded timeouts** and a **finally/defer cleanup path** (no unbounded reads that can orphan the socket).
5. **Orphan prevention:** startup must **detect and prevent an orphan** connector/harness from retaining the PMS slot (reap/verify the slot is free before connecting).
6. **Lock-before-start:** no financial test or production connector may start while **another owner holds the Interface lock**.

---

## Phase-0 Closure Plan & Remaining Validation Matrix (2026-07-16, corrected)

**Planning only — nothing in this section is authorized to execute.** No financial test, no connection, no link interruption, no live-reservation manipulation. Each item below is a written plan; each requires separate explicit product-owner authorization (with an approved Room/Stay and named cleanup owner) before any execution.

**Correction (2026-07-16):** the earlier version of this plan wrongly gated **Phase-0 finalization** on product behaviors that cannot be measured before the corresponding StayConnect components exist. This is corrected into **three distinct tiers**:

1. **Phase-0 protocol & architecture validation** — measurable now, measured; the finalization basis (§1 below).
2. **Per-property deployment validation** — a per-property financial-onboarding checklist; **Aqua Club / Hotel ID 2 lives here**, not as a Phase-0 blocker (§3A below).
3. **Post-implementation acceptance testing** — behaviors requiring code/schema that do not yet exist (UNKNOWN/Manual-Review; Checkout & Checkout-Grace); preserved as **binding acceptance requirements**, not Phase-0 blockers (§3B below).

Contract status is now **FINAL — Phase 0 CLOSED** (Product-Owner approval, 2026-07-16; see contract §9c/§9d). Current operational status: Phase 1A production `iam_v2` **live-dark created + verified** (dark, not cut over). Next authorized activity: **Product-Owner acceptance of Phase 1A, then Phase 1B planning under separate authorization.**

### 1. Exact proven scope — Tier 1 (Phase-0 finalization basis; do NOT generalize)

The live end-to-end debit proof (Gate 3A, `P#900002`) currently covers **only**:

- **Property:** Coral Sea Holiday Village;
- **Hotel ID:** 3;
- **one** active in-house Stay;
- verified **`RN` + mandatory `G#`**;
- **one** USD 1.00 `PS` debit;
- **`PA ASOK`** matched by **PMS Interface + `P#`**;
- **correct Guest Folio** verified by Front Office;
- **correct `SO=WIFI` revenue mapping** verified;
- **manual correction** completed by Front Office;
- **Folio returned to its exact original balance**.

**This single-property, single-stay, happy-path result is NOT** financial validation of: other PMS Interfaces (e.g. Aqua Club / Hotel ID 2), other Properties, **sharers** (two stays one room), **multi-folio** cases, **no-post** cases, or any **error/non-OK `AS` status** (`NG/NA/NP/NR/RY/UR`). Those remain unproven and are covered by the matrix below.

### 2. Gate 3B — programmatic reversal (v1 product decision: DEFERRED)

Recorded v1 decision:

- **`programmatic_reversal` capability = `false`.**
- **`PT=C` and negative-`TA` behavior remain unverified** — do not assume either.
- **Financial corrections are manual Front Office operations** in Protel.
- **Programmatic reversal is NOT a Phase-1A requirement.**
- It may be introduced later **only after a separate capability spike** proves the exact `PT`/`TA`/`SO` reversal semantics.

Gate 3B **does not block v1**, provided the manual-correction limitation is **visible, audited, and operationally documented** (operations manual + admin UI surfacing + `posting_review_actions` audit trail). See contract §15 (`CREATE_REVERSAL` is a new audited ledger row referencing the original) and §9a rule 5.

### 3A. Tier 2 — Per-property financial-onboarding checklist (deployment prerequisite, NOT a Phase-0 blocker)

Financial Posting must be **independently validated per Property** before it is enabled there. This is a **deployment gate**, not a Phase-0 finalization gate. Before PMS Posting is enabled for **any** Property, that Property must confirm:

1. **PMS Interface currency and exponent** (owner/Finance-confirmed — never carried over from another property);
2. **Package currency compatibility** with the interface base currency (contract §9a rule 3; no FX in v1);
3. **`SO=WIFI` revenue mapping** to the intended Internet revenue account (Finance/Protel-confirmed);
4. **`RN` + `G#` folio targeting** (mandatory `G#`; `RN`-only `ASOK` is not proof);
5. **one controlled Debit** (single-owner connection, Gate-3A shape);
6. **actual Folio placement** verified by Front Office;
7. **approved cleanup/correction** to net-zero by a named owner.

**Aqua Club — Coral Sea Aqua Club, Hotel ID 2, `120.0.0.15:5001`:** reclassified from a Phase-0 architecture-finalization blocker to this **mandatory per-property financial-onboarding requirement**. It remains **read-only capable and financially UNAPPROVED** until it passes the checklist above. Additional Aqua-Club-specific notes: prove **namespace isolation** from Hotel ID 3 (identical room numbers across interfaces must never collide); **do not assume USD/exponent-2** from Hotel ID 3; single-client slot must be free (no orphan owner) before any test. No test is sent until separately authorized with an approved Room and named cleanup owner.

### 3B. Tier 3 — Post-implementation acceptance (cannot be measured before the components exist)

These are **binding acceptance requirements** for later phases, **not** Phase-0 blockers. Do not require product state-machine behavior to be measured before its code and schema exist.

**Protocol-spike evidence already known (informs the design; no further Phase-0 test needed):** a `PS` may reach Protel even when the `PA` is not available to the client; a fresh `P#` creates another **independent** posting; therefore **blind retry is unsafe**. (Grounded in Gate 3A behavior — no deliberate lost-ACK test was run.)

**3C — Posting-Engine UNKNOWN safety.** *Requires first:* the Posting Engine, `posting_attempts` + `posting_attempt_events`, `pms_interface_pnumber_seq`, and the Manual-Review workflow (contract §4.5, §9a, §15). *Acceptance:* a transmitted request with **no matching `PA` becomes UNKNOWN**; **no automatic retry**; **no automatically allocated second `P#`**; **Manual-Review** workflow engaged; **external Folio reconciliation**; audited **`CONFIRM_POSTED` / `RETRY_APPROVED` / `ABANDON`** decisions; **no duplicate charge**.

**3D — Checkout & Checkout-Grace.** *Requires first:* Stay/Event persistence, the Checkout handler, the Post-Stay profile, the Checkout-Grace Purchase+Entitlement, session reassignment, accounting cutoff, and idempotent event processing (contract §3, §16). *Acceptance scenarios (all binding):* healthy-link Checkout; link-down Checkout; delayed Checkout; **stale-cache refusal**; reconnect and resync; **mandatory Checkout Grace**; **no intentional guest disconnect or re-authentication**; **effective-checkout-timestamp** accounting split; **repeated-Checkout idempotency**.

### 4. Phase-0 finalization — DONE (FINAL, Phase 0 CLOSED)

Phase-0 finalization rested on the **Tier-1** evidence above (measured and merged into contract §9b/§9c). The single condition is met:

- [x] **explicit final product-owner approval** of the corrected Phase-0 contract — **granted 2026-07-16**. Contract status: **FINAL — Phase 0 CLOSED**.

**Not** finalization blockers (carry forward past FINAL): Tier-2 per-property onboarding (incl. Aqua Club) and Tier-3 post-implementation acceptance (3C/3D). Deferred limitations are enumerated in contract §9d and repeated here:

- Hotel ID 2 (Aqua Club) financial Posting **not yet approved** (read-only until its checklist passes);
- **programmatic reversal disabled** (manual Front Office correction only in v1);
- **UNKNOWN / Manual-Review** behavior **pending Posting-Engine implementation**;
- **Checkout-Grace** behavior **pending PMS/Entitlement implementation**;
- **physical traffic accounting** still requires **live implementation acceptance** (non-zero real-device usage), unprovable at Phase 0.

### 5. Implementation boundary (Phase 0 FINAL; implementation still gated on Phase 1A plan approval)

Phase 0 is FINAL. **Current operational status:** Phase 1A production `iam_v2` **live-dark created + verified** (dark, not cut over); still forbidden without their own separate approvals: service routing/DSN/`search_path` change, IAM data migration, cutover, deployment, PMS connector/posting execution. The next authorized activity is **Product-Owner acceptance of Phase 1A, then Phase 1B planning under separate authorization** — see [../architecture/StayConnect-IAM-Phase1A-Plan.md](StayConnect-IAM-Phase1A-Plan.md).

---


**Attempt #5 (2026-07-16, owner-selected Room «ROOM-REDACTED»):** owner explicitly selected **Room «ROOM-REDACTED»** (no redaction of the room number authorized). Full `LS→LD(IFPB/V#1.13/RT4)→LR(GI/GC/GO)` handshake, incoming `LS`/`LA` acked with `LA|`, read-only `DR` resync returned `DS` + **365 in-house records** ending with an explicit **`DE`**; Room «ROOM-REDACTED» resolved to a valid `G#` from an in-house `GI`, not cleared by any `GO`. Exactly **one** `PS` (`TA100`/`PTD`/`SOWIFI`/`WSSTAYCONNECT`/`CTGate3A Test`/**`P#900002`**) sent with **zero retries**; **`PA ASOK` matched by Interface + `P#` in 93 ms**. `P#900001` (Attempt #4) was NOT reused. Result: `PROTOCOL_ACCEPTED — FOLIO_PLACEMENT_NOT_INDEPENDENTLY_VERIFIED`. The resolved `G#` was returned directly to the product owner and is deliberately NOT stored in Git/Markdown. Guest name never decoded or stored. See "Execution Attempt #5" below.

**Attempt #4 (2026-07-16):**

Gate 3A executed 2026-07-16 as an owner-approved **technical-only** test (Option A: the product owner owns the out-of-band Front Office folio verification and cleanup). One connection, slot free, `LA` reached, read-only `DR` resync obtained the in-house roster, exactly **one** `PS` (`TA100`, `PTD`, `SOWIFI`, `WSSTAYCONNECT`, `CTGate3A Test`, `P#900001`) sent with **zero retries**; **`PA ASOK` matched by Interface + `P#` in 78 ms**. Reported only as `PROTOCOL_ACCEPTED — FOLIO_PLACEMENT_NOT_INDEPENDENTLY_VERIFIED`. No claim is made that the charge reached the correct folio, was corrected, or returned to net zero — the product owner confirms that out-of-band. See "Execution Attempt #4" below. (Prior attempts #1–#3 aborted before any `PS` — placeholders, unconfirmable currency, and no Front-Office channel respectively.)

The legacy-server (`172.21.96.150`) SSH inspection is **cancelled** — not required. Socket-Server collision safety is handled **in-band at test start**: accept + opening `LS` = free slot; keep that connection for the whole run; refusal / no `LS` ⇒ abort without displacing (see "Socket-Server collision clearance"). Gate 3A is now blocked **only** on the real financial/test fixtures.

Gate 1 (read-only preflight, 2026-07-15) confirmed TCP reachability and FIAS 2.20 framing on both endpoints but did not reach link-alive. Gate 1B (2026-07-15) established the correct read-only sequence and reached **link-alive on both interfaces without any authentication key**, confirming the Gate-1 `LA` absence was a **sequencing issue (the `LR` subscription records were withheld), not authentication**. No posting, reversal, PS/PA, link interruption, restart, `pms_providers` creation, or database/config change. Guest record VALUES were never decoded or stored — only record-type counts and timing. *(At that time the contract was CONDITIONALLY FROZEN; it is now **FINAL — Phase 0 CLOSED**, per the status header above.)*

**Owner correction applied:** there is no `IfcAuthKey` on either Protel interface (the prior integration connected by IP + TCP port only). `IfcAuthKey` is removed as a prerequisite and was neither invented, guessed, nor required.

Governing documents: [StayConnect-IAM-Phase0-Contract.md](StayConnect-IAM-Phase0-Contract.md) (§9 receives this spike's measured results) and [StayConnect-IAM-Handoff.md](StayConnect-IAM-Handoff.md) (execution gates).

Rules of engagement:
- **Gate 1 (read-only preflight)** is the only currently authorized action, and it must be explicitly started by the product owner: connectivity, FIAS handshake/framing, heartbeat, version identification, approved test reservation/folio lookup. **No posting. No link interruption. No network scanning.**
- **Gate 2:** present the exact live test plan; wait for explicit approval.
- **Gate 3 (after approval only):** live posting scenarios listed below.
- No passwords, interface auth keys, or other secrets are ever recorded in this document.

## Supplied Connection Information

### PMS Interface 1 (`pms1`) — owner-attested

| Field | Value |
|---|---|
| Property | **Coral Sea Holiday Village** |
| Hotel ID | **3** |
| Host/IP | `150.0.0.18` |
| TCP port | `5003` |
| Device mode | Socket Server |
| TLS | disabled |
| Route | internal hotel network only (owner-attested) |
| FIAS/IFC version | client sends `V#1.13`, `RT4`, interface id `IFPB`; spec on file: FIAS 2.20 (`docs/FIAS_2.20.24.pdf`) |

### PMS Interface 2 (`pms2`) — owner-attested

| Field | Value |
|---|---|
| Property | **Coral Sea Aqua Club** |
| Hotel ID | **2** |
| Host/IP | `120.0.0.15` |
| TCP port | `5001` |
| Device mode | Socket Server |
| TLS | disabled |
| Route | internal hotel network only (owner-attested) |
| FIAS/IFC version | client sends `V#1.13`, `RT4`, interface id `IFPB`; spec on file: FIAS 2.20 |

> Both properties are served by one StayConnect Edge as two independent PMS Interface
> namespaces (Hotel ID 3 = `pms1`; Hotel ID 2 = `pms2`). This also enables live validation of
> the multi-PMS namespace and duplicate-source-detection requirements during later phases.

## Gate 1B — Precondition Hold (2026-07-15, earlier — now RESOLVED)

> **RESOLVED 2026-07-15** by owner attestation (property identities + internal-route confirmation) and the correction that **no `IfcAuthKey` exists** on either interface. The hold below is retained as the historical record of why the first Gate-1B request was stopped; see **Gate 1B — Results** further down for the completed read-only validation.

Gate 1B (authenticated, strictly read-only) was first requested without the mandatory inputs. Its instruction defined five mandatory checks **before connecting**, plus abort triggers; at that time none of the five were satisfiable and two abort triggers were active, so **no connection was opened** on that request. Reasoning, per precondition:

| # | Required before connecting | Status | Why held |
|---|---|---|---|
| 1 | Owner-approved **Property/Hotel identity** for each endpoint | **Missing** | No property/hotel names or codes have been attested for `150.0.0.18:5003` or `120.0.0.15:5001`. Without an attestation there is nothing to match the post-auth identity against — and "Property identity does not match the owner attestation" is an explicit **abort** trigger, so connecting blind is disallowed. |
| 2 | Confirm both routes are **intentional internal** hotel routes despite public-range IPs | **Unconfirmed → active abort trigger** | Gate 1 established both endpoints are reached over the WAN uplink `ens160` via the default gateway `172.21.60.1`; `120.0.0.15` and `150.0.0.18` are both **public IANA-range** addresses (not RFC-1918). "Either endpoint appears reachable through the public Internet rather than the intended internal route" is an explicit **abort** trigger. This must be affirmatively confirmed as internal (e.g., private WAN / MPLS / static internal routing) before any authenticated attempt. |
| 3 | Protel-operations confirmation that the dedicated IFC registration **cannot disconnect/replace/disturb** an existing FIAS interface | **Missing** | No Protel-ops sign-off provided. Some Oracle/FIAS deployments allow a newer IFC to bump an existing session; "an existing production FIAS connection is displaced" is an explicit **abort** trigger. Cannot be self-certified. |
| 4 | A **dedicated test interface identity / IFC number** for StayConnect | **Missing** | None supplied. The Gate-1 harness used the connector default `IFPB`; reusing a default or unknown identity risks colliding with a real interface (ties to #3). |
| 5 | **IfcAuthKey** via an out-of-band secret mechanism | **Missing** | No auth key and no pointer to an out-of-band mechanism (env var, staged file, secret store, path) were provided. Guessing, brute-forcing, or hunting the filesystem for a secret is forbidden and was not attempted. An authenticated connection is impossible without it. |

**Decision:** halt Gate 1B before any connection. Proceeding would require either connecting without an auth key (impossible) or connecting despite unmet safety attestations and two active abort triggers (disallowed). This is the correct fail-closed outcome.

### To release the Gate 1B hold, the product owner must provide

1. **Per endpoint: the attested Property/Hotel identity** (name and/or code) that the connection is expected to represent — recorded here as attestation, matched against post-auth link data.
2. **Written confirmation** that `150.0.0.18` and `120.0.0.15` are intentional **internal** hotel routes (how they are carried — private WAN/MPLS/VPN/static internal), not Internet-exposed paths.
3. **Protel-operations confirmation** that registering a new, dedicated StayConnect IFC will not disconnect, replace, or disturb any existing/production FIAS interface on either endpoint.
4. **The dedicated StayConnect IFC identity / interface number** (and any mandated `V#` version and `RT` record-transfer type) to use for the test link.
5. **The IfcAuthKey per interface**, delivered out-of-band (e.g., placed in a named file on the appliance with a path told to the operator, or an environment variable, or a secret store reference) — **never** pasted into chat, Git, Markdown, logs, shell history, or command output. This document will only ever reference the mechanism/location, never the value.

Once all five are in hand, Gate 1B can run: authenticate the dedicated read-only IFC, confirm `LA`/alive, capture negotiated identity/version and safely-exposed property identifiers, receive the approved resync, resolve only the approved test reservation + folio (redacted), and measure heartbeat/reconnect/resync cadence — with immediate abort on any of the defined triggers.

## Gate 1B — Results (2026-07-15, authenticated-free read-only, redacted)

Executed from the Hotel Appliance `172.21.60.23` using an ephemeral Python harness that reuses the connector's FIAS framing and the previously-working sequence (`data-plane/internal/pms/protel_fias.go`). One endpoint at a time, Hotel ID 3 first. No `pms_providers`, service, config, or database change. **No `PS`/`PA`, charge, reversal, checkout manipulation, lost-ACK, link interruption, or service restart.** Guest record VALUES were never decoded or stored — only record-type counts and timing.

**Existing-client / occupancy check.** With "allow new connection" unchecked, a Socket Server that already has a client refuses newcomers (it protects, not displaces). On both endpoints the server **accepted the TCP connection and sent an unsolicited opening `LS`** (server inviting a client) — the signal of a **free client slot**. Therefore **no existing production client was connected to either Socket Server**, and nothing was displaced. Each test connection was brief (~40 s) and gracefully closed to release the slot. The appliance held no prior connection to either endpoint (`ss` clean; `pms_providers`/`pms_attempts` empty).

**Correct working FIAS handshake/initialization sequence (verified on both):**

1. Client dials the Socket Server; server sends opening `LS|DA(YYMMDD)|TI(HHMMSS)|`.
2. Client sends `LS|DA..|TI..|` (link start).
3. Client sends `LD|DA..|TI..|IFPB|V#1.13|RT4|` — interface id `IFPB`, version `V#1.13`, record-transfer `RT4`, **no authentication field**.
4. Client sends the `LR` subscription records: `LR|RIGI|FLRNG#GNGFGAGD|`, `LR|RIGC|FLRNG#GNGFGAGD|`, `LR|RIGO|FLRNG#|` (subscribe to Guest-In / Guest-Change / Guest-Out with the room/reservation/name/arrival/departure fields).
5. Server transitions the link to **alive** and sends `LA` (Link Alive); observed at **~5.1 s** on both interfaces.
6. Server streams the **initial in-house resync** as `GI`/`GC` records (~11 s after connect).
7. Link is maintained by sending `LA` on idle; the peer keeps the connection open (no `LE`, no drop across the 40 s window).

**Reason `LA`/data streaming did not occur in Gate 1.** Gate 1 sent only `LS` + `LD` and deliberately **omitted the `LR` subscription records**. Without `LR`, the Socket Server does not complete link setup or start the feed, so it re-issued `LS` and never sent `LA`. Adding the `LR` records brought the link to alive on the first attempt **with no authentication key** — confirming the Gate-1 `LA` absence was a **FIAS sequencing/configuration issue, not authentication failure**.

**Observed record types, heartbeat, resync (redacted; counts/timing only):**

| Interface | Occupancy | First `LA` | Resync begins | Guest records (type→count) | Heartbeat |
|---|---|---|---|---|---|
| pms1 — Hotel 3, Coral Sea Holiday Village (`150.0.0.18:5003`) | slot free | ~5.1 s | ~11.2 s | `GI`=7, `GC`=2 | client `LA` on idle keeps link up; server `LA` received |
| pms2 — Hotel 2, Coral Sea Aqua Club (`120.0.0.15:5001`) | slot free | ~5.1 s | ~11.1 s | `GI`=2 | same |

No `DS`/`DE` database-resync envelope records were seen — Protel streamed `GI` records directly. No `GO`/`LE` during the window. Guest field **values were never read**; only the record-id and arrival time were counted.

**Property mapping — confirmed.** By owner attestation and endpoint: **`pms1` → Hotel ID 3 → Coral Sea Holiday Village** and **`pms2` → Hotel ID 2 → Coral Sea Aqua Club**. Corroborated operationally by two independent Socket Servers with **distinct occupancy** (Hotel 3 = 9 in-house records vs Hotel 2 = 2), consistent with two separate properties. The protocol-level hotel-id field was **not independently decoded** (that would require reading record field values); if the owner wants an independent hotel-id confirmation, it can be captured in a targeted, redaction-safe read of the link/property field only.

**Interfaces unaffected — evidence.** No existing client was present to disturb (both slots were free); connections were read-only, brief, and gracefully closed; no `pms_providers` row, service restart, config, or database write occurred.

**Two independent namespaces.** Treated as two independent PMS Interface namespaces on one StayConnect Edge (separate Socket Servers, separate occupancy, separate hotel ids) — matching the contract's per-interface namespace model.

## Test-Room Details Still Required (for Gate 2)

Gate 1B confirmed the read-only feed but was **not** given an approved test reservation, so no specific guest/folio was resolved (correctly — none was surfaced). Before any Gate-2 planning, per interface:

- test **room + reservation number + guest/family name** (to match one specific in-house reservation read-only);
- **folio id** to be used as the posting target, and confirmation postings are permitted on it;
- **posting code** (test article) + **test amount + currency**;
- **reversal method** confirmation (expected: negative posting);
- **Front Office contact** + approved **maintenance window**;
- explicit owner approval of the written **Gate-2 live test plan**.

(No `IfcAuthKey` or interface-registration items remain — resolved: the interfaces accept `IFPB`/`V#1.13`/`RT4` with no key, and both routes are owner-attested internal.)

## Test Fixtures — Gate 2 (first property: Coral Sea Holiday Village / Hotel ID 3 / pms1)

**All fixture values are still UNSUPPLIED.** The owner's Gate-2 message carried literal placeholder tokens (`<ROOM>`, `<RESERVATION>`, …), not values. Gate-2 *planning* is complete (below); **Gate-2/Gate-3 execution is blocked until these are provided with real values.** No values were invented.

| Fixture | Value | Status |
|---|---|---|
| Test room | `<ROOM>` | **GAP** |
| Reservation number | `<RESERVATION>` | **GAP** |
| Guest / family name | `<NAME>` | **GAP** |
| Expected Folio | `<FOLIO>` | **GAP** |
| Posting permission confirmed | `<YES>` | **GAP** (must be explicit YES + which folio) |
| Posting code (PMS transaction/department for internet charge) | `<CODE>` | **GAP** (+ confirm which FIAS field carries it — see plan §3) |
| Test amount | `<AMOUNT>` | **GAP** |
| Currency | `<CURRENCY>` | **GAP** |
| Reversal method expected by Protel | `<METHOD>` | **GAP** (negative/rebate vs correction — Protel-specific) |
| Front Office contact | `<CONTACT>` | **GAP** |
| Maintenance window | `<WINDOW>` | **GAP** |

## Gate 1 — Read-Only Preflight Checklist

- [x] TCP connectivity to `150.0.0.18:5003` (pms1) — **OK**, connect ~1 ms
- [x] TCP connectivity to `120.0.0.15:5001` (pms2) — **OK**, connect ~1 ms
- [x] FIAS record framing verified — **STX (0x02) … ETX (0x03)** confirmed on both; opening `LS` record received
- [x] Handshake behavior observed — link **did not reach alive** without interface auth/registration (see results)
- [x] Heartbeat/keepalive cadence observed and recorded — see results
- [x] FIAS/IFC handshake identified in Gate 1B — client `V#1.13` / `RT4` / `IFPB` accepted; link reaches alive with no key
- [ ] Approved test reservation lookup — pending Gate-2 fixtures (link-alive + feed confirmed in Gate 1B; no test reservation supplied yet)
- [ ] Folio identification — pending Gate-2 fixtures
- [x] Confirmed: **no posting sent, no reversal, no link interruption, no service restart, no network scanning; guest values never decoded/stored**

## Gate 1 — Results (2026-07-15, read-only, redacted)

**Environment.** Executed from the Hotel Appliance `172.21.60.23`. Preflight used an ephemeral Python harness reusing the connector's FIAS framing (`data-plane/internal/pms/protel_fias.go`: STX+body+ETX; `LS`/`LD` record formats). No changes to `pms_providers`, services, config, or the database.

**Routing / source interface (both endpoints).**

| Endpoint | Route | Source IP | Interface |
|---|---|---|---|
| `150.0.0.18:5003` (pms1) | via gateway `172.21.60.1` | `172.21.60.23` | `ens160` (WAN) |
| `120.0.0.15:5001` (pms2) | via gateway `172.21.60.1` | `172.21.60.23` | `ens160` (WAN) |

Both endpoints are reached over the WAN uplink `ens160` via the default gateway (no dedicated PMS interface/route). Note: `120.0.0.15` is a public-range address routed out the WAN — confirm with the owner that this is the intended path for pms2.

**TCP reachability & framing.**

| Endpoint | TCP connect | On connect (passive, no transmit) | Framing |
|---|---|---|---|
| pms1 `150.0.0.18:5003` | OK (~1 ms) | one unsolicited `LS` record (23 bytes), then silent | STX…ETX confirmed |
| pms2 `120.0.0.15:5001` | OK (~1 ms) | one unsolicited `LS` record (23 bytes), then silent | STX…ETX confirmed |

**Opening record (redacted — link-level only, no guest data).** Both peers send a single Link-Start:
`LS | DA(YYMMDD) | TI(HHMMSS) |` — i.e. record-id `LS` with a 6-char date field and a 6-char time field, nothing else. No version (`V#`), interface, or property/hotel identifier is present in the pre-authentication `LS`.

**Minimum-safe handshake (transmitted `LS` then `LD` only; NO `LR` subscription).** Using the connector's `LD` format (`LD|DA..|TI..|IFPB|V#1.13|RT4|`). Result on both endpoints: the peer responded by **re-issuing `LS`** and did **not** send `LA` (link-alive/accept), did **not** send `LE` (explicit reject), and — critically — sent **no `GI/GC/GO/DS` guest or database-resync records**. The harness was armed to abort and redact on any guest record; it never triggered (`guest_records_seen = 0`).

**Heartbeat / cadence (receive-only, 20 s, no transmit).** Each peer emits exactly **one `LS` at connect (~0.1 s) then stays silent** for the full 20 s window with the connection held open. Therefore the "second `LS`" seen during the handshake step was the peer **reacting to our `LD` by restarting negotiation**, not a periodic keep-alive. No independent server-driven heartbeat interval was observed pre-link-up.

**Interpretation (Gate 1 — later corrected by Gate 1B).** Both endpoints are live FIAS 2.20 peers with correct framing, reachable and stable. At Gate 1 the link did not advance to the data-streaming ("alive") state. The initial hypothesis (authentication/registration required) was **disproven by Gate 1B**: the true cause was that Gate 1 sent only `LS`+`LD` and **omitted the `LR` subscription records**. With `LR` included, the link reaches alive and streams the feed **with no authentication key** (see Gate 1B — Results). No secret was guessed or brute-forced at any point.

**Separate properties?** The two endpoints are distinct hosts/ports answering independently (independent `LS` timestamps), consistent with two separate PMS interfaces. This **cannot be positively confirmed as two separate properties from the protocol pre-authentication**, because no property/hotel/IFC identifier is exposed before link-up. Confirmation requires either owner attestation or post-authentication `LD`/link data (Gate 2+).

## Gate 2 — Live-Spike Plan (PROPOSED — awaiting real fixtures + explicit owner approval)

Documentation only. **Nothing in this section has been executed. No FIAS posting/reversal/state-changing record has been sent. No production connection was opened for this plan.** First property: **Coral Sea Holiday Village (Hotel ID 3, pms1, `150.0.0.18:5003`, `IFPB`/`V#1.13`/`RT4`, no auth key)**. Coral Sea Aqua Club (Hotel 2) is planned only as an independent read-only repeat; no financial test is planned for Hotel 2 in this cycle.

Amount convention (spec §Posting): all FIAS amount fields (`TA`, `S1-S9`, `T1-T9`, `TP`) are **minor units with no decimal separator** (e.g. 10.50 → `TA1050`) — matching the contract's ISO-4217 minor-unit rule. `<AMOUNT>`/`<CURRENCY>` fixtures must be given so the minor-unit value is computed exactly once and confirmed against the approved amount before send. **This encoding is spec-derived and remains UNVERIFIED against this Protel installation (see Grounding below).**

### Grounding — read-only inspection of the previously working integration (2026-07-15, no messages sent)

Inspected `data-plane/internal/pms/protel_fias.go` and `.../defaults.go` (read-only). Findings:

- The existing StayConnect Protel integration is **lookup-only**. It implements the link handshake and the guest feed (`LS`/`LD`/`LR`→`GI`/`GC`/`GO`) with field map **RN, G#, GN, GF, GA, GD** (room, reservation, last/first name, arrival, departure) and identity `IFPB` / `V#1.13` / `RT4`, no auth key.
- **There is NO posting code anywhere:** no `PS`/`PR`/`PA`/`PL`, no posting/department-code field, no amount encoding, no currency-exponent handling, no reversal/correction logic, and no `P#` generation or dedup. (The `currency`/`amount_cents` hits elsewhere in the tree are Stripe/voucher pricing, unrelated to FIAS posting.)
- **Consequence:** the read-only link sequence is *verified* (Gate 1B + connector). The **entire posting/inquiry/acknowledgment/reversal sequence in this plan is derived only from the FIAS 2.20 specification (`docs/FIAS_2.20.24.pdf`)** and is **not** backed by any prior working StayConnect posting integration. Every Protel-specific behavior below is therefore **UNVERIFIED** and must be confirmed from Protel configuration/spec or measured before Gate 3 — never assumed. See "Unresolved Protel-specific fields" at the end.

### §0 One approved persistent test connection per run (no probe-then-reconnect race)

A separate free-slot probe followed by a later reconnect is **not** used: it opens a race where another client could occupy the single-client Socket Server between the probe and execution. Instead, each Gate-3 run opens **exactly one persistent connection** and holds it for the whole run:

1. Open one connection to `150.0.0.18:5003` and complete the read-only link (`LS→LS→LD→LR→LA`). Because "allow new connection" is unchecked, a busy server refuses newcomers, so **accept + server `LS` + reaching `LA` = we hold the sole client slot**; refusal/reset/failure to reach `LA` ⇒ an existing client is present or the slot is contended ⇒ **ABORT** (do not displace, do not retry into a race).
2. **Do not disconnect between steps.** All of §1–§7 for a run execute on this same held connection; if it drops at any point, the run **ABORTS** and any in-flight charge is treated as UNKNOWN (§6) — it is not silently re-established.
3. **Property identity match**: confirm Hotel ID 3 before any posting (owner attestation + endpoint; optionally a redaction-safe read of the link/property field only). Mismatch ⇒ **ABORT**.
4. **Front Office reachable** (`<CONTACT>`) and inside the **`<WINDOW>`** window; **posting permission** on `<FOLIO>` explicitly confirmed (`<YES>`). Either missing ⇒ **ABORT**.

### §1 Redaction-safe read-only lookup of ONLY the approved test reservation

Bring the link to alive (`LS→LS→LD(IFPB/V#1.13/RT4)→LR:GI,GC,GO→LA`), receive the resync, and locate **only** the record whose Room = `<ROOM>` **and** Reservation `G#` = `<RESERVATION>` **and** name matches `<NAME>`. Confirm the associated folio corresponds to `<FOLIO>`. Redaction: log only a boolean "approved reservation found + folio matches", `<ROOM>`/`<RESERVATION>` (approved test identifiers only), and record timing — **never** other guests' values; if the approved reservation cannot be isolated from the stream safely, **STOP**. (The production `PS` flow has **no** `PR` inquiry/answer step, so folio pre-confirmation comes from the resync cache **plus** Front Office reading the folio — not from a protocol inquiry.)

### §2 10–15 minute passive Link-Alive observation (read-only)

Hold the alive link ~15 min sending only `LA` on idle, measuring: **client `LA` cadence** (our idle keep-alive interval), **server `LA` cadence** (unprompted server keep-alives, if any), **idle behavior** (does the server drop an idle link?), **reconnect timeout** (if the server closes, time to re-establish + whether a fresh resync replays), and **whether any automatic resync (`DS`/`DE` or a fresh `GI` burst) occurs** (e.g. at night-audit). All values feed the contract §9 freshness axes (heartbeat, feed-continuity, resync cadence). No records other than `LA` are sent.

### §3 FIAS posting + acknowledgment records (grounded in the production wire evidence)

**Authoritative source:** the accepted production-implementation review + Protel wire-log findings (the legacy Coral Sea integration), which supersede the generic spec derivation. FidServ/Protel accounting-configuration facts (e.g. what `SOWIFI` maps to) remain **subject to confirmation by the property's Protel administrator / Finance** (see §4-note and Gate-3A fixtures).

- **Financial record is `PS`** (not `PR`). The production wire posts a guest-folio charge with `PS` including `G#`. (The generic FIAS spec note that "`PS` cannot target `G#`" does **not** match this installation's observed behavior; the production wire is authoritative for legacy behavior, and `G#`-folio targeting semantics are a Protel-admin confirmation item.)
- **Exact production field order:** `RN, G#, TA, PT, SO, CT, P#, WS`.
  - `RN` = room; **`G# = reservation, MANDATORY`** (an `ASOK` on an `RN`-only post does **not** prove a Guest-Folio posting — it may hit a room account);
  - `TA` = **integer minor units, exponent 2, no currency code on the FIAS wire** (e.g. 10.50 → `TA1050`);
  - **`PT = D`** (debit); do **not** assume `PT=C`;
  - **`SO = WIFI`** (sales outlet);
  - `CT` = clear text, **max 20 characters**;
  - `P#` = unique protocol-attempt sequence (see below);
  - **`WS = STAYCONNECT`** (workstation id).
- **Acknowledgment `PA` fields:** `RN, AS, P#, CT`. Known **`AS` outcomes: `OK, NG, NA, NP, NR, RY, UR`** (`OK` = accepted; the others are failure/retry/unknown-reason statuses whose exact meaning is a Protel-admin confirmation item). **Match the `PA` to its `PS` by PMS Interface + `P#`** — **not** by Room Number. Legacy `PA`-matching by `RN` is unsafe (sharers / concurrent rooms) and is **not** carried forward.
- **Two distinct identifiers — do not conflate:**
  - **StayConnect internal `idempotency_key`** — stable for the *logical* Posting (derived from `site-stay-purchase-seq`, contract §4.5). Anchors our state machine, ledger and manual-review correlation; never changes across attempts of the same logical Posting.
  - **FIAS `P#`** — a *unique protocol-attempt sequence*, **not** business idempotency. Whether Protel deduplicates on `P#` is unverified and **must be measured**; this plan assumes no `P#`-based dedup guarantee.
- **No auto-retry.** The legacy behavior of **automatically retrying after 3 minutes with a new `P#` is unsafe (it can double-post) and is NOT carried forward.** A transmitted `PS` with no matched `PA` becomes **UNKNOWN** and is never automatically retried (contract rule 1).
- **Reversal is not solved.** Programmatic reversal was **not implemented or production-proven** in the legacy system; operational correction is **manual in Protel**. Programmatic reversal stays `capability=false` until a supervised test proves the exact `PT`/`TA`/`SO` semantics (contract rule 5); do not assume `PT=C` or negative `TA`.

### Step gating & separate approvals

The financial scenarios are **not** a single run. Each is a **separately approved step**, executed only after the previous one passed and its evidence was reviewed:

1. **Normal charge** (§4) — must fully succeed (`PA ASOK` + Front Office confirms the single expected line).
2. **Reversal** (§5) — must fully succeed (folio net-zero confirmed).
3. **Lost-ACK** (§6) — only after 1 and 2 passed.
4. **Checkout / stale-occupancy** (§7) — only after 1–3 passed and only if the owner approves altering the test reservation.

**Blocking rule:** if the **normal charge (§4) or the reversal (§5) fails or ends UNKNOWN and is not cleanly reconciled to net-zero, ALL later scenarios are blocked** — no lost-ACK, no checkout/staleness — until the folio is confirmed net-zero and the owner re-approves.

### §4 Normal-charge flow — **Gate 3A**, one debit only (manual correction on standby)

> **Finance/Protel confirmation required first:** an `ASOK` on `SO=WIFI` proves the wire was accepted, **not** that it landed on the correct revenue/transaction account. The FidServ `WIFI` (`SOWIFI`) revenue mapping must be confirmed by property Finance/Protel **before** this runs (contract rule 4).

1. **Pre-test folio evidence:** Front Office reads and records the `<FOLIO>` balance/line-items; StayConnect records the pre-state from the resync cache (redacted). (No `PR` inquiry exists in the `PS` flow.)
2. **Posting record:** send exactly one **`PS`** with field order `RN<ROOM>|G#<RESERVATION>|TA<amount_minor>|PTD|SOWIFI|CT<=20|P#<seqA>|WSSTAYCONNECT|`. **`G#` mandatory.** **Guards before the socket write:** computed `TA` (minor units, exponent 2) == approved `amount_minor`; package currency == pinned interface base currency (contract rule 3); `CT` ≤ 20 chars; a fresh unique `P#`; else **ABORT** (no send). Record the `posting_attempts` row (internal_posting_id, attempt#, interface, `P#`, `RN`, `G#`, sent_at) before/at send.
3. **Expected acknowledgment:** one `PA` (`RN, AS, P#, CT`) with **`AS=OK`**, **matched by PMS Interface + `P#`** (not by `RN`), within the timeout. Any non-`OK` `AS` (`NG/NA/NP/NR/RY/UR`) ⇒ treat as not-cleanly-posted, record `AS` + `response_at`, **stop** (do not retry).
4. **Post-test folio verification:** Front Office confirms exactly one `<AMOUNT>` line on the correct folio with the expected revenue mapping; `RN`-only appearance is **not** acceptance of a guest-folio posting.
5. **Reference strategy:** one posting in flight at a time; unique `P#` per attempt; correlation is internal `idempotency_key` ↔ `posting_attempts.P#`; **no auto-retry** (§6).
6. **Rollback:** the first debit is corrected **manually in Protel by Front Office** per the approved manual-correction procedure (programmatic reversal is Gate 3B only, and only after capability proof — §5). Front Office confirms the folio returns to net-zero.

### §5 Reversal flow — **Gate 3B**, only after separate Protel capability proof

Programmatic reversal was **not implemented or production-proven** in the legacy system; it stays **`capability=false`** (contract rule 5). It is **not** attempted in Gate 3A. Before any programmatic reversal:

1. **Capability proof (supervised, separate approval):** with Protel-admin/Finance supervision, establish the exact reversal semantics — record type, `PT`, `TA` sign/encoding, `SO`, and the reference to the original attempt. **Do not assume `PT=C` or a negative `TA`.** Until proven, the field `<METHOD>` is unresolved.
2. Only after the semantics are proven and separately approved: send one reversal (its own new `P#`, linked to the same internal `idempotency_key`), expect one `PA` `AS=OK` matched by **Interface + `P#`**, and confirm net-zero on the folio.
3. **Until then, correction of any Gate-3A debit is manual in Protel** by Front Office (the approved manual-correction procedure).

### §6 Lost-ACK / UNKNOWN — **Gate 3C**, only after Gate 3A is reconciled

1. Send one **`PS`** (its `P#` recorded in `posting_attempts`, linked to the logical Posting's internal `idempotency_key`); **confirm the bytes were transmitted** (socket write flushed / `send()` fully returned for the framed record) — the interruption is applied **only after** transmission is proven, never before.
2. **Interrupt our own client socket** **before** the `PA` is received. No FIAS "interrupt" record is sent — a transport drop of our own connection only; the PMS link/other clients are unaffected.
3. No matched `PA` (by Interface + `P#`) within the timeout ⇒ the command is **UNKNOWN** (contract: `posting → SENDING → UNKNOWN`).
4. **Never auto-retry — with either the same or a new `P#`.** Resending the same `P#` may double-post if Protel does not dedup on it; resending with a new `P#` definitely double-charges if the first actually posted. The legacy "**retry after 3 minutes with a new `P#`**" is exactly this unsafe behavior and is **removed** (contract rule 1). The command routes to **MANUAL_REVIEW** and waits for external evidence.
5. **External reconciliation:** Front Office inspects `<FOLIO>` for a line matching the amount/reference and reports whether the charge reached Protel.
6. **Audited Manual-Review decision** (contract §15): `CONFIRM_POSTED` (folio shows it → mark POSTED, then correct per §5/manual) / `CONFIRM_NOT_POSTED_ABANDON` / **`CONFIRM_NOT_POSTED_RETRY`**. A manually-approved retry is a **new protocol attempt linked to the same internal `idempotency_key`** (new `posting_attempts` row). **Whether it reuses the same `P#` or allocates a new `P#` is unresolved** and must be grounded in Protel configuration/spec or measured behavior first — not decided by this plan. Whatever the outcome, the test folio is left net-zero.

### §7 Checkout-while-link-down and stale-occupancy — **Gate 3D**, separate (only the approved test reservation)

No unrelated guest is ever touched; these use **only** the `<RESERVATION>` test fixture and require explicit owner + Front Office coordination. StayConnect sends **no** `XC`/checkout or state-changing record — Front Office performs the PMS action; StayConnect only observes read-only.

- **Checkout while link down:** with our client disconnected, Front Office checks out the **test** reservation. Expected: (a) StayConnect's cached occupancy is now stale; (b) an attempted posting is **blocked by the financial fresh-validation rule** (occupancy re-verification fails → refuse, no send); (c) on reconnect, the resync/`GO` reflects the checkout. Confirms `posting_allowed=false` after checkout is honored.
- **Stale occupancy (room move):** with our client disconnected, Front Office moves the **test** reservation to a different room. Expected on reconnect: occupancy re-verification detects room mismatch vs the pre-move cache and **aborts any posting** until re-resolved. Confirms the room-move-is-not-identity rule and stale-occupancy abort.

These two scenarios are **optional** and only run if the owner approves altering the designated test reservation; otherwise they are documented and deferred.

### §8 Explicit stop conditions (any ⇒ stop immediately, send nothing further)

**Do not begin, or halt at once, if any of these hold:**

- **no verified test Stay/Folio** — the approved reservation cannot be isolated read-only, or resolved reservation/folio ≠ `<RESERVATION>`/`<FOLIO>`;
- **no Front Office confirmation** — contact `<CONTACT>` unavailable, outside `<WINDOW>`, or not confirming pre/post folio state;
- **unsupported or uncertain Posting Code mapping** — the FIAS field carrying `<CODE>` is not confirmed with Protel;
- **uncertain amount encoding** — minor-unit/exponent handling for `<AMOUNT>`/`<CURRENCY>` not confirmed, or the computed `TA` ≠ approved amount;
- **uncertain reversal semantics** — Protel's `<METHOD>` and its original-charge reference not confirmed;
- **any UNKNOWN charge not externally reconciled** — a timed-out/ambiguous posting is outstanding without Front Office folio evidence;
- **Folio not returned to net zero** — a prior test charge/reversal has not been confirmed net-zero;
- **unexpected client/socket occupancy** — the single-client Socket Server is (or becomes) occupied, or our held connection drops mid-run (do not displace, do not race);
- property identity ≠ Hotel ID 3; any unexpected/unrecognized FIAS record on the link; any duplicate-posting risk (`PA` missing but folio shows the charge ⇒ never resend).

### §9 Safety & rollback summary

- One posting in flight at a time; wait for the `PA` matched by **Interface + `P#`**; unique `P#` per attempt; **no auto-retry** (the legacy 3-min/new-`P#` retry is removed).
- Every financial step is bracketed by Front Office pre/post folio evidence; `ASOK`/`RN`-only is not proof of a correct guest-folio revenue posting.
- Gate 3A's single debit is corrected **manually in Protel** (programmatic reversal is Gate 3B, capability-gated); folio confirmed net-zero.
- One held persistent connection per run (§0); drop mid-run ⇒ abort + UNKNOWN handling; all evidence redacted; guest values never stored beyond the approved test identifiers.
- Everything pins the Hotel 3 (pms1) namespace; package currency must equal the interface base currency (no FX); no crossing to Hotel 2.

### Redacted planned message sequence (production-grounded templates — no real values, nothing sent)

```
# link up (read-only) — verified in Gate 1B
S→C  LS|DA<..>|TI<..>|
C→S  LS|DA<..>|TI<..>|
C→S  LD|DA<..>|TI<..>|IFPB|V#1.13|RT4|
C→S  LR|RIGI|FLRNG#GNGFGAGD|   LR|RIGC|FLRNG#GNGFGAGD|   LR|RIGO|FLRNG#|
S→C  LA|                       # link alive
S→C  GI.. / GC..               # resync (redacted; isolate ONLY <RESERVATION>); no PR inquiry in the PS flow

# charge (Gate 3A only) — PS, production field order; G# MANDATORY; TA integer minor units exp2, no currency;
#                         PT=D debit; SO=WIFI; CT<=20; P#=unique protocol attempt; WS=STAYCONNECT
C→S  PS|RN<ROOM>|G#<RESERVATION>|TA<amount_minor>|PTD|SOWIFI|CT<=20chars>|P#<seqA>|WSSTAYCONNECT|
S→C  PA|RN<ROOM>|AS<OK|NG|NA|NP|NR|RY|UR>|P#<seqA>|CT<..>|     # MATCH by Interface + P# (NOT by RN)

# reversal (Gate 3B only, after Protel capability proof) — record/PT/TA-sign/SO UNRESOLVED; do NOT assume PT=C or negative TA
C→S  <reversal record per proven <METHOD>>|P#<seqB>|WSSTAYCONNECT|
S→C  PA|...|AS<OK|..>|P#<seqB>|

# Logical Posting keyed by StayConnect internal idempotency_key; P# is ONLY the FIAS protocol attempt ref.
# UNRESOLVED (Protel-admin/Finance): SOWIFI revenue mapping, G# folio-target semantics, AS-code meanings,
#   P# dedup behavior, reversal semantics <METHOD>, currency/exponent confirmation.
```

### Unresolved Protel-specific fields (must be grounded/confirmed before Gate 3)

The **wire format is now grounded** in the production evidence (`PS` record, field order `RN,G#,TA,PT,SO,CT,P#,WS`, `PT=D`, `SO=WIFI`, `WS=STAYCONNECT`, `CT≤20`, `TA` minor-units exp2 no-currency, `PA` fields `RN,AS,P#,CT`, `AS∈{OK,NG,NA,NP,NR,RY,UR}`). The remaining items require **property Protel-admin / Finance confirmation or supervised measurement** before any charge:

1. **`SOWIFI` revenue mapping** — Finance/Protel must confirm what FidServ department/transaction/revenue account `WIFI` posts to. `ASOK` does not prove revenue-account correctness (contract rule 4).
2. **`G#` folio-target semantics** — confirm `PS`+`G#` posts to the intended **guest folio** on this installation (the generic spec says `PS` is room-only; production differs — Protel-admin to confirm which folio a `G#` post lands on, incl. multiple-folio guests).
3. **`AS` code meanings** — exact semantics of `NG/NA/NP/NR/RY/UR` (which are hard failures vs retry-advisory vs unknown) for correct MANUAL_REVIEW routing.
4. **`P#` dedup behavior** — whether Protel rejects/deduplicates a replayed/reused `P#` (drives the §6 retry `P#` decision). Measure under supervision; assume nothing.
5. **Reversal semantics `<METHOD>`** — record type, `PT`, `TA` sign/encoding, `SO`, original-attempt reference. `capability=false` until proven (Gate 3B). Do **not** assume `PT=C` or negative `TA`.
6. **`PA` latency / late-answer behavior** — real response time and whether Protel ever answers after our timeout (affects UNKNOWN handling).
7. **Currency/exponent confirmation** — Protel/Folio base currency + exponent; and that the package currency equals it (contract rule 3; no FX in v1).

### Gate 3 authorization + execution split

Gate 3 executes **only** after: (a) all Gate-3A mandatory fixtures (below) are supplied with real values; (b) the Protel-admin/Finance confirmation items above are resolved; (c) the **in-band Socket-Server collision check** at test start passes (accept + opening `LS` = free slot, held for the whole run); and (d) the owner explicitly approves. Execution is split and separately approved:

- **Gate 3A** — one normal **debit** only (§4); corrected manually in Protel.
- **Gate 3B** — programmatic **reversal** only, and only after a separate Protel **capability proof** (§5).
- **Gate 3C** — **lost-ACK / UNKNOWN** only after Gate 3A is reconciled (§6).
- **Gate 3D** — **checkout / staleness** separately (§7).

A failed or unreconciled Gate 3A blocks 3B/3C/3D until the folio is confirmed net-zero and the owner re-approves.

### Gate-3A blockers (the ONLY things that block Gate 3A)

Gate 3A (one normal debit) is blocked **only** by these mandatory financial/test items — all still outstanding. (Socket-Server collision safety is **not** a pre-run blocker: it is proven **in-band at test start** — accept + opening `LS` = free slot, held for the whole run; refusal / no `LS` ⇒ abort — see "Socket-Server collision clearance".)

- confirmed Protel **Folio/base currency and exponent**;
- **Package currency equals the PMS Interface currency**;
- **Finance confirmation of the `SOWIFI` revenue mapping**;
- approved **Room** (`<ROOM>`);
- **verified Reservation `G#`** (`<RESERVATION>`);
- **verified open Folio** (`<FOLIO>`);
- **`posting_allowed` confirmation**;
- approved **`amount_minor` and currency** (`<AMOUNT>`/`<CURRENCY>`);
- **Front Office contact** (`<CONTACT>`);
- **maintenance window** (`<WINDOW>`);
- **manual Front Office correction procedure** for the first debit.

**Do NOT block Gate 3A on** (these belong to later, separately-approved gates):

- `PT=C` or programmatic reversal behavior → **Gate 3B**;
- `P#` replay/dedup testing → **Gate 3C**;
- lost-ACK behavior → **Gate 3C**;
- checkout / staleness behavior → **Gate 3D**.

The "Unresolved Protel-specific fields" above that concern reversal semantics, `P#` dedup, and checkout are **not** Gate-3A blockers; they gate 3B/3C/3D only. (`AS`-code meanings and `G#` folio-target semantics are needed to *interpret* the 3A result and so are confirmed as part of the 3A Finance/Protel sign-off.)

## Socket-Server collision clearance — in-band, at test start (legacy-server SSH inspection CANCELLED)

The legacy-server (`172.21.96.150`) SSH investigation is **cancelled and out of scope** — it is not required for the new StayConnect implementation. No access to, or modification of, that server is attempted. There is no SSH-access blocker and no credential/key request.

**Collision clearance is an in-band check performed at the start of the approved Gate-3A run, using the already-verified Socket-Server behavior** (§0 of the plan):

- Open **one** connection to the Protel Socket Server and complete the read-only link (`LS→LS→LD→LR→LA`).
- **If the server accepts the connection and sends its opening `LS`, the single-client slot is FREE** (with "allow new connection" unchecked, a server that already had a client would refuse the newcomer). Reaching `LA` confirms we hold the slot.
- **Keep that same accepted connection for the entire approved test run** — do not disconnect/reconnect (avoids a slot race).
- **If the connection is refused, or no opening `LS` is received, ABORT immediately** — do not displace, reconnect into a race, or modify any existing client.

This replaces any pre-run legacy-connector reconciliation: collision safety is proven at test time by the server's own admission control, not by inspecting the old server.

## Gate 3A — Execution Attempt #5 (2026-07-16): EXECUTED — owner-selected Room «ROOM-REDACTED», one debit, PA ASOK

Owner-directed test: the product owner **explicitly selected Room «ROOM-REDACTED»** and authorized recording the room number in this document (guest name still forbidden in Git/Markdown; the resolved `G#` returned directly to the owner, not stored here).

**Handshake fix vs earlier attempts:** the PMS retransmits its opening `LS` and does **not** advance to `LA` unless the client (a) sends `LS→LD(IFPB/V#1.13/RT4)→LR` immediately on connect — as the production connector does — and (b) acks incoming `LS`/`LA` with a bare `LA|`. Gating on a client-side "reach LA before proceeding" milestone stalled the link; mirroring the connector and driving a `DR` resync completed it. (A prior harness process also had to be reaped because it held the PMS's single-client slot open.)

**Sequence:**

- Connect; sent `LS`, `LD (IFPB/V#1.13/RT4)`, `LR(GI/GC/GO)` immediately. Peer `LS`/`LA` acked with `LA|`.
- Read-only **`DR` resync** → `DS` + **365 in-house records** → explicit **`DE`** (full roster, complete).
- **Room «ROOM-REDACTED»** matched in an in-house `GI` with a valid `G#`; **not** cleared by any `GO`/checkout in the roster.

**Pre-send verification (all satisfied):** `RN==«ROOM-REDACTED»`; in-house; valid `G#`; `RN`+`G#` same current Stay on Hotel ID 3; not cleared by `GO`/checkout; no prior UNKNOWN posting for Room «ROOM-REDACTED» (Attempt #4 targeted a different room); `TA==100`.

**The one debit (exactly one `PS`, zero retries):**

```
PS|RN«ROOM-REDACTED»|G#<resolved-G#-returned-to-owner-only>|TA100|PTD|SOWIFI|CTGate3A Test|P#900002|WSSTAYCONNECT|
PA|RN«ROOM-REDACTED»|SOWIFI|P#900002|WSSTAYCONNECT|ASOK|      # matched by PMS Interface + P# (NOT by RN)
```

- `TA100` = **USD 1.00** (currency USD, exponent 2, owner-confirmed); `PT=D`; `SO=WIFI`; `WS=STAYCONNECT`; `CT="Gate3A Test"`; **`P#=900002`** (new durable attempt; `P#900001` NOT reused).
- **`PA` status `AS=OK`**; **response time 93 ms**; matched **by PMS Interface + `P#`**, not by `RN`.
- **Exactly one `PS`; zero retries**; link closed with `LE`. No second debit, no `PT=C`, no negative `TA`, no reversal, no lost-ACK, no link interruption, no checkout, no DB/config/service change.

**Result: `PROTOCOL_ACCEPTED — FOLIO_PLACEMENT_NOT_INDEPENDENTLY_VERIFIED`.** `ASOK` proves Protel accepted the record at the protocol level; it is **not** proof of correct-folio placement. This is a **real** posting on the live PMS.

**Owner cleanup correlation keys** (used to locate + remove in Protel out-of-band): `RN=«ROOM-REDACTED»`, `P#=900002`, `WS=STAYCONNECT`, `SO=WIFI`, `CT="Gate3A Test"`, amount USD 1.00 (`TA100`), posted ~`2026-07-16T05:53:03Z`, `PA ASOK`.

**Folio verification: COMPLETE (2026-07-16).** Front Office confirmed the USD 1.00 debit landed on the correct guest Folio (Room «ROOM-REDACTED» / G#«RES-REDACTED»), `SO=WIFI` mapped to the intended Internet revenue account, the debit was manually removed, and the Folio returned to its exact original balance. See "Gate 3A — CLOSURE" at the top of this document. **Gate 3A verdict: PASS.**

## Gate 3A — Execution Attempt #4 (2026-07-16): EXECUTED — one debit, PA ASOK (protocol accepted; folio NOT independently verified)

Owner-approved **Option A** (technical-only send; product owner owns out-of-band folio verification + cleanup; pre-send Front Office evidence waived). Executed against **Coral Sea Holiday Village / Hotel ID 3 / `150.0.0.18:5003`**.

**Sequence (redacted):**

- One connection opened; **accepted + opening `LS` ⇒ slot free**; `LS → LD (IFPB/V#1.13/RT4) → LR(GI/GC/GO) → LA` reached. Same connection kept for the whole run.
- The in-house feed did **not** auto-stream (as in a prior session), so a read-only **`DR` (Database Resync request)** was sent; PMS returned `DS` + the in-house `GI` list (**355 rooms carrying a `G#`**; `GO`-cleared rooms excluded). *(The explicit `DE` end-marker had not arrived before the collection window closed, but a full 355-record in-house roster was received and the `PS` was sent only after that window — never interleaved with resync records.)*
- **Selected exactly one currently in-house room** with a verified `RN` **and** `G#` from the same `GI` record (not cleared by any `GO`). Guest name/personal data were **never** decoded or stored.

**Selected reservation (redacted — no PII):**

| Field | Redacted evidence |
|---|---|
| `RN` | 5-digit room, SHA-256[:8] = `«redacted»` |
| `G#` | 6-digit reservation, SHA-256[:8] = `«redacted»` |
| In-house | **yes** (DR-resync `GI`, not cleared by `GO`) |

**The one debit (exactly one `PS`, zero retries):**

```
PS|RN<redacted>|G#<redacted>|TA100|PTD|SOWIFI|CTGate3A Test|P#900001|WSSTAYCONNECT|
PA|RN<redacted>|ASOK|P#900001|CT|          # matched by PMS Interface + P# (NOT by RN)
```

- `TA100` = **USD 1.00** (currency USD, exponent 2, owner-confirmed); `PT=D`; `SO=WIFI`; `WS=STAYCONNECT`; `CT="Gate3A Test"` (11 chars ≤ 20); **`P# = 900001`** (one unique protocol attempt allocated for this run).
- **`PA` status: `AS=OK`**; **response time: 78 ms**; matched **by PMS Interface + `P#`**, not by `RN`.
- **Exactly one `PS` sent; zero retries**; connection closed gracefully. No second debit, no `PT=C`, no negative `TA`, no reversal, no lost-ACK, no link interruption, no checkout, no DB/config/service change.

**Result: `PROTOCOL_ACCEPTED — FOLIO_PLACEMENT_NOT_INDEPENDENTLY_VERIFIED`.** `ASOK` proves Protel accepted the posting record at the protocol level; it is **not** proof the correct guest folio was charged. This is a **real** posting on the live PMS.

**Owner cleanup correlation keys** (to locate + remove the test debit in Protel out-of-band, without exposing PII here): `P#=900001`, `WS=STAYCONNECT`, `SO=WIFI`, `CT="Gate3A Test"`, amount USD 1.00 (`TA100`), posted ~`2026-07-16T05:17:26Z`, `PA ASOK`. **Folio verification, manual removal, and net-zero confirmation are owned by the product owner and remain AWAITING PRODUCT-OWNER CONFIRMATION.**

## Gate 3A — Execution Attempt #3 (2026-07-16): ABORTED at the Front-Office verification gate (no connection, no PS sent)

Currency/exponent are now resolved (**USD, exponent 2 ⇒ `TA100`**) and guest selection is auto-from-feed, so those prior blockers are cleared. Execution still **aborts before any connection or `PS`** on the **one remaining, fundamental blocker: there is no channel from this execution environment to a human Front Office**, which the runbook makes a hard gate.

- **Pre-send gate cannot be met:** the runbook step 4 requires me to *"Obtain Front Office pre-test Folio evidence for the selected reservation,"* and states *"If Front Office cannot verify the selected Folio before sending, abort without PS."* No phone/chat/email/PMS-folio channel to Front Office is available to me (the auth-gated mail/calendar connectors are not usable here). I cannot capture the pre-test folio balance.
- **Required deliverables are impossible for me to produce:** "Front Office Folio verification," "manual correction result," and "confirmation that the Folio returned to its exact original balance" all require a human Front Office loop I cannot reach or observe.
- Consequence: posting a real USD 1.00 debit to a real, non-consenting in-house guest while unable to verify placement **or** confirm the mandatory net-zero cleanup would violate the runbook's own pre-send abort clause. **Abort without PS.** (Connecting first was declined: it cannot create a Front Office channel and would needlessly expose a real guest's personal data for a posting that cannot proceed.)

**This is the last gap. One sentence from the product owner clears it — choose one:**

- **(A) Owner owns the human loop, technical-only send:** explicitly restructure the run so *the owner/Front Office* captures the pre-test folio balance and performs verification + correction out-of-band, and authorize StayConnect to send **only** the single `PS` and report the `PA`, with folio placement reported as `PROTOCOL_ACCEPTED — FOLIO_PLACEMENT_NOT_INDEPENDENTLY_VERIFIED` and cleanup/net-zero owned and confirmed by the owner. (Ideally also relay the **pre-test folio balance** so net-zero can be cross-checked from the returned evidence.)
- **(B) Close the loop to me:** relay, through the prompts, the selected reservation's **pre-test folio balance** and the **post-correction confirmation**, or wire a folio-read channel — so I can complete the full verify→post→verify→net-zero sequence end-to-end.

Until (A) or (B) is given, no `PS` is sent. **Requested return items:** redacted RN/G# — none (no selection/connection); in-house confirmation — N/A; allocated `P#` — none; `PS`/`PA` — none; `PA` status/response time — N/A; Front Office folio verification — **cannot perform (no channel)**; correction/net-zero — N/A (nothing posted); one-PS/zero-retries — N/A (zero PS).

## Gate 3A — Execution Attempt #2 (2026-07-16): ABORTED at the currency guard (no connection, no PS sent)

A second Gate-3A execution was requested — this time authorizing auto-selection of any in-house room and a 1.00 debit — but it **aborts before opening any connection**, on the owner's own §2 currency guard, and with a flagged safety regression. **No FIAS connection opened, no guest selected, no `PS` built or sent.**

**Primary blocker — Protel-side exponent/currency is NOT determinable (owner's §2 guard: "if the exponent cannot be determined safely, abort before PS"):**

- the FIAS wire carries **no currency field** and no accounting metadata (the `GI/GC` feed is room/reservation/name/dates only) — so currency/exponent cannot be read from a FIAS session;
- the appliance has **no PMS configuration** (`pms_providers` = 0) — nothing to read there;
- the legacy Protel server is out of scope and there is no Protel-admin access;
- the values supplied in the request are the placeholder tokens `<CURRENCY>` / `<EXPONENT>`, **not** confirmed values, and the instruction is conditional ("*if* the confirmed exponent is 2, TA must be 100").

Guessing exponent = 2 and sending `TA100` would be an unsafe assumption (if the real exponent were 3, 1.00 = `TA1000`; if 0, `TA1`) — exactly what §2 forbids ("Do not send `TA1` when the intention is 1.00"). Therefore: **abort before PS.** Connecting first was declined because it cannot resolve the exponent and would needlessly expose a real guest's personal data.

**Secondary concern (flagged):** this run auto-targets a **random real in-house guest**, **forbids reversal** (§6), and provides **no Front Office correction path** — so the mandatory net-zero restoration from the approved design is absent, and a real, non-consenting guest would carry an uncorrected `Gate3A Test` charge. This is a material safety regression versus the frozen plan.

**Requested return items:** redacted RN/G# — **none** (no guest selected, no connection); in-house confirmation — **N/A**; confirmed currency/exponent — **NOT determinable** (see above); allocated `P#` — **none**; `PS`/`PA` — **none sent**; `PA` status/response time — **N/A**; folio placement — **N/A (nothing posted)**; only-one-PS/no-retry — **N/A (zero PS)**.

**To run Gate 3A safely, supply the two missing essentials:** (1) the **confirmed Protel Folio currency and exponent** (a Finance/Protel fact, not a placeholder), so `TA` for 1.00 is exact; and (2) a **correction/cleanup path** for the real guest's folio (Front Office contact to record before/after and remove the test debit), restoring net-zero. With those, the run proceeds per the readiness pack below.

## Gate 3A — Execution Attempt #1 (2026-07-16): ABORTED at fixture pre-gate (no PS sent)

Gate-3A execution was requested. It **aborted before any connection** because the required test fixtures were supplied as **placeholder tokens, not real values**. No FIAS connection was opened, no reservation resolved, no `PS` built or transmitted, and no Front Office coordination occurred. This is the correct, safe outcome: a real financial debit must never be posted against invented/placeholder room/reservation/amount values or without live Front Office supervision.

**Fixture check (what was received):**

| Fixture | Received | Usable? |
|---|---|---|
| Test Room | `<ROOM>` | ✗ placeholder |
| Guest family name | `<NAME>` | ✗ placeholder |
| Reservation number | `<RESERVATION_OR_UNKNOWN>` | ✗ placeholder |
| Expected Folio | `<FOLIO>` | ✗ placeholder |
| `amount_minor` | `<AMOUNT_MINOR>` | ✗ placeholder |
| Currency | `<CURRENCY>` | ✗ placeholder |
| Protel Folio/base currency | `<CURRENCY>` | ✗ placeholder |
| Protel-side exponent | `<EXPONENT>` | ✗ placeholder |
| Front Office contact | `<CONTACT>` | ✗ placeholder |
| Maintenance window | `<WINDOW>` | ✗ placeholder |
| Posting permitted | `YES` | ✓ (but moot without a folio) |
| Finance `SOWIFI` mapping confirmed | `YES` | ✓ |
| Front Office manual-correction approved | `YES` | ✓ |

**Abort conditions triggered (§4/§E):** approved reservation cannot be resolved/verified (`RN`/`G#` not real); Folio not verifiable; amount not a real value; currency/exponent not real (step-10 assertions cannot be evaluated); Front Office not reachable (placeholder contact/window) so mandatory pre/post folio evidence and manual correction cannot be arranged. Any one of these mandates abort; here all apply.

**Result of the requested return items:** verified `RN`/`G#` resolution — **not performed** (no real reservation, no connection); exact `PS`/`PA` — **none sent**; `PA` status/response time — **N/A**; Front Office folio verification — **N/A**; manual correction — **N/A**; folio net-zero — **N/A (no charge ever posted)**.

**To run Gate 3A, re-issue with real values** for: Test Room, Guest family name, Reservation number (or an explicit "resolve from room+name"), Expected Folio, `amount_minor`, Currency, Protel Folio/base currency, Protel-side exponent, Front Office contact, and Maintenance window — with Front Office standing by to record the folio before/after and perform the manual correction. Everything else (technical wire facts, runbook, template, aborts) is ready below.

## Gate 3A — Readiness Pack (prepared, NOT executed)

For one controlled debit against **Coral Sea Holiday Village · Hotel ID 3 · `150.0.0.18:5003` · TLS off**. Nothing here is executed; **no `PS` or state-changing record is sent.**

### A. Automatically confirmed technical facts (read-only)

Measured via read-only FIAS sessions (Gate 1B 2026-07-15 and a fresh 2026-07-15/16 session), plus the accepted production wire evidence:

- **Slot free / admission:** connect is accepted and the server sends its opening `LS` (single-client slot free); framing **STX (0x02) … ETX (0x03)**.
- **Link identity/version accepted, no auth key:** client `LD` `IFPB` / `V#1.13` / `RT4` → **`LA` (link alive) at ~5.1 s**.
- **Heartbeat:** client `LA` on idle keeps the link up; the peer holds the connection (no `LE`/drop within the observation window).
- **Resync/feed timing is variable and not instant:** Gate 1B streamed `GI`×7 + `GC`×2 at ~11 s; a later 20 s session streamed none. **Do not assume the approved reservation can be resolved from the cache within any fixed wait** — resolution is gated on the approved `RN`/`G#` actually being present and verified (§D step 6); **Front Office folio evidence is authoritative** for pre/post state.
- **`PS` debit wire format (production-grounded):** field order `RN, G#, TA, PT, SO, CT, P#, WS`; `PT=D` (debit); `SO=WIFI`; `WS=STAYCONNECT`; `CT` ≤ 20 chars; **`G#` mandatory** (`RN`-only `ASOK` ≠ guest-folio posting).
- **`TA` encoding — confirmed:** the **legacy StayConnect implementation encodes `TA` using exponent 2 (major amount × 100)**, as an integer with no currency on the wire. (The *Protel-side* interpretation is a Finance/Protel confirmation item — see below.)
- **`PA` acknowledgment:** fields `RN, AS, P#, CT`; `AS ∈ {OK, NG, NA, NP, NR, RY, UR}`; **matched by PMS Interface + `P#`**, never by `RN`.
- **`P#` — legacy observed:** the previous implementation used an **epoch-seeded increasing `P#`**.

**Design requirement (NOT wire-discovered or production-confirmed):**

- **`P#` allocation for new StayConnect** will use a **durable atomic sequence per PMS Interface** (contract §9a rule 2) — a design decision, not something observed on the wire or confirmed in production.

**NOT technically visible on the FIAS wire → require Finance/Protel confirmation (not guessed):**

- the **Protel Folio/base currency**;
- the **Protel-side currency interpretation / exponent** (i.e. how Protel reads the `TA` integer);
- **whether the Protel currency matches the Internet Package currency**;
- the **`SO=WIFI` revenue/transaction-account mapping** (FidServ) — the FIAS wire carries no currency and no accounting mapping; an `ASOK` on `SO=WIFI` proves wire acceptance only, not revenue-account correctness.

### B. Owner / Finance test-fixture form (human approvals — all still UNSUPPLIED)

Only the approvals that cannot be discovered automatically. Do **not** pick an arbitrary real guest.

| Approval | Value | Status |
|---|---|---|
| Approved test **Room** | `<ROOM>` | **needed** |
| **Reservation `G#`** (or enough approved info to resolve it read-only) | `<RESERVATION>` | **needed** |
| Expected **open Folio** | `<FOLIO>` | **needed** |
| **Posting permitted** on that folio | `<YES>` | **needed** |
| Approved **`amount_minor`** + **currency** | `<AMOUNT>` / `<CURRENCY>` | **needed** |
| **Front Office contact** | `<CONTACT>` | **needed** |
| **Maintenance window** | `<WINDOW>` | **needed** |
| **Finance: `SOWIFI` → intended Internet revenue code** | — | **needed** |
| Approval for **manual Front Office correction** after the debit | — | **needed** |
| PMS Interface **base/folio currency + exponent** | — | **needed** |

Once the approved reservation is supplied, StayConnect resolves and verifies **RN, G#, Folio read-only** (link-alive + in-house feed / Front Office) **before** any financial execution.

### C. Final redacted `PS`/`PA` template (no real values; nothing sent)

```
# link up (read-only) — verified
S→C  LS|DA<..>|TI<..>|
C→S  LS|DA<..>|TI<..>|   ·   LD|DA<..>|TI<..>|IFPB|V#1.13|RT4|   ·   LR|RIGI|..| LR|RIGC|..| LR|RIGO|..|
S→C  LA|                 # link alive (~5.1s); then in-house GI/GC feed (redacted; isolate ONLY <RESERVATION>)

# one debit (Gate 3A) — G# MANDATORY; TA integer minor units exp2 (no currency on wire); durable P#
C→S  PS|RN<ROOM>|G#<RESERVATION>|TA<amount_minor>|PTD|SOWIFI|CT<=20chars>|P#<durable-seq>|WSSTAYCONNECT|
S→C  PA|RN<ROOM>|AS<OK|NG|NA|NP|NR|RY|UR>|P#<durable-seq>|CT<..>|      # MATCH by Interface + P# (NOT RN)
```

### D. Gate 3A execution runbook (prepared; execute only on explicit approval)

1. At the approved test start, **open one connection** to `150.0.0.18:5003`.
2. **Accept + opening `LS` ⇒ Socket Server slot is FREE.**
3. **Keep the same connection for the entire run** (no disconnect/reconnect).
4. **Refusal, timeout, or no opening `LS` ⇒ immediate ABORT** (do not displace/modify any client).
5. Complete `LS → LD → LR` and **reach `LA`**.
6. **Resolve the approved `RN` and `G#` read-only — never on a fixed resync wait.** Proceed only once the approved `RN` **and** `G#` are **actually present and verified** against the owner-approved reservation and the Front Office folio evidence (from the in-house feed and/or Front Office). There is **no timed "assume-resolved" step**. **If the approved reservation cannot be resolved and verified, ABORT without sending any `PS`.**
7. Obtain **Front Office pre-posting folio evidence** (this is the authoritative confirmation of `RN`/`G#`/Folio, independent of feed timing).
8. Build **exactly one `PS` debit** — only after step 6/7 verification: verified `RN`; verified `G#`; approved integer `TA`; `PTD`; `SOWIFI`; redacted `CT` ≤ 20 chars; **durable allocated `P#`**; `WSSTAYCONNECT`. Guard: computed `TA` == approved `amount_minor`; package currency == interface currency (Finance-confirmed); else ABORT (no send). Write the `posting_attempts` row at send.
9. **Wait for the matching `PA` by PMS Interface + `P#`.**
10. **No automatic retry under any circumstance.**
11. **No `PA` after transmission ⇒ UNKNOWN ⇒ Manual Review** (external folio evidence required).
12. **Front Office verifies the actual folio, not `ASOK` alone** (correct guest folio + intended revenue code).
13. **Front Office performs the approved manual correction** of the debit.
14. **Verify the folio returns to net zero.**
15. **Close the same connection gracefully.**

### E. Abort conditions (any ⇒ stop immediately, send nothing further)

Socket Server slot unavailable · wrong Property (≠ Hotel 3) · **approved reservation cannot be resolved/verified** (`RN`/`G#` not actually present & verified) · wrong `RN`/`G#`/Folio · missing posting permission · currency mismatch or Protel currency/exponent not Finance-confirmed · `SOWIFI` mapping not Finance-confirmed · Front Office unavailable · amount mismatch (`TA` ≠ approved) · unexpected FIAS response · unresolved UNKNOWN transaction · any duplicate-posting risk (e.g. no `PA` but folio shows the charge ⇒ never resend).

### F. Manual-correction procedure (after the single debit)

Programmatic reversal is `capability=false` (Gate 3B, capability-gated). For Gate 3A, the debit is corrected **manually in Protel by Front Office** per the approved procedure; StayConnect observes read-only and confirms **net-zero** on the folio. If the transaction is UNKNOWN, Front Office reconciles the folio first (posted or not) and the manual correction is applied only to a confirmed-posted debit; nothing is auto-retried.

## Measured Results (empty until the spike runs)

| Measurement | pms1 | pms2 |
|---|---|---|
| Pre-auth server heartbeat | none observed (single LS at connect, then silent) | none observed (single LS at connect, then silent) |
| Post-auth heartbeat cadence | — (blocked: link not alive) | — (blocked: link not alive) |
| Resync/night-audit behavior | — | — |
| `can_post` | — | — |
| `supports_idempotency` | — | — |
| `read_back` | — | — |
| `reversal` | — | — |
| `folio_identity` | — | — |
| `room_only_posting` | — | — |
| `safe_retry` | — | — |
| Auth cache age bound (measured) | — | — |
| Financial freshness bound (measured) | — | — |

On completion, these values replace the spike-gated defaults in the contract's §9 and the spike status becomes `COMPLETE`; the contract then goes to the product owner for FINAL approval.
