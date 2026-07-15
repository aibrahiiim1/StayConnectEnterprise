# Protel FIAS — Phase 0 Live Spike Record

**Spike status: `GATE2_PLAN_DRAFTED — AWAITING REAL FIXTURES + OWNER APPROVAL TO EXECUTE GATE 3 (no financial traffic performed)`**

Gate 1 (read-only preflight, 2026-07-15) confirmed TCP reachability and FIAS 2.20 framing on both endpoints but did not reach link-alive. Gate 1B (2026-07-15) established the correct read-only sequence and reached **link-alive on both interfaces without any authentication key**, confirming the Gate-1 `LA` absence was a **sequencing issue (the `LR` subscription records were withheld), not authentication**. No posting, reversal, PS/PA, link interruption, restart, `pms_providers` creation, or database/config change. Guest record VALUES were never decoded or stored — only record-type counts and timing. Contract remains **CONDITIONALLY FROZEN**.

**Owner correction applied:** there is no `IfcAuthKey` on either Protel interface (the prior integration connected by IP + TCP port only). `IfcAuthKey` is removed as a prerequisite and was neither invented, guessed, nor required.

Governing documents: [StayConnect-IAM-Phase0-Contract.md](../architecture/StayConnect-IAM-Phase0-Contract.md) (§9 receives this spike's measured results) and [StayConnect-IAM-Handoff.md](../context/StayConnect-IAM-Handoff.md) (execution gates).

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

Bring the link to alive (`LS→LS→LD(IFPB/V#1.13/RT4)→LR:GI,GC,GO→LA`), receive the resync, and locate **only** the record whose Room = `<ROOM>` **and** Reservation `G#` = `<RESERVATION>` **and** name matches `<NAME>`. Confirm the associated folio corresponds to `<FOLIO>`. Redaction: log only a boolean "approved reservation found + folio matches", `<ROOM>`/`<RESERVATION>` (approved test identifiers only), and record timing — **never** other guests' values; if the approved reservation cannot be isolated from the stream safely, **STOP**. Optionally issue a guest-scoped inquiry (see §3, inquiry form) to confirm the PMS resolves the same reservation/folio live.

### §2 10–15 minute passive Link-Alive observation (read-only)

Hold the alive link ~15 min sending only `LA` on idle, measuring: **client `LA` cadence** (our idle keep-alive interval), **server `LA` cadence** (unprompted server keep-alives, if any), **idle behavior** (does the server drop an idle link?), **reconnect timeout** (if the server closes, time to re-establish + whether a fresh resync replays), and **whether any automatic resync (`DS`/`DE` or a fresh `GI` burst) occurs** (e.g. at night-audit). All values feed the contract §9 freshness axes (heartbeat, feed-continuity, resync cadence). No records other than `LA` are sent.

### §3 Exact FIAS posting + acknowledgment records (from FIAS 2.20 spec, grounded)

Guest-folio postings use the **`PR` (Posting Request)** family, **not `PS`** (`PS` is room-only and "postings to specific guests (G#) are not supported"). Required sequence per spec: **inquiry `PR` → posting `PR` → `PA`**, one at a time, waiting for `PA` before any next posting.

- **Inquiry `PR`** (no `TA`, carries `PI`): e.g. `PR|G#<RESERVATION>|RN<ROOM>|PI<inq-token>|WS<ws-id>|DA<yymmdd>|TI<hhmmss>|` → PMS returns guest/folio match (or `PL` Posting List if multiple/sharers). Confirms the folio and that the guest is in-house.
- **Posting `PR`** (carries `TA` + a **unique** `P#`): e.g. `PR|G#<RESERVATION>|RN<ROOM>|TA<amount-minor>|PT<type>|P#<unique-seq>|WS<ws-id>|DA..|TI..|X1<ref>|` plus the **posting/department code `<CODE>`** in the Protel-designated field. **Field-mapping gap:** the spec carries the charge's article/department via `PT` (Posting Type), `SO` (Sales Outlet), and/or PMS interface configuration; the exact field Protel expects `<CODE>` in must be confirmed with Protel operations before send (do not assume).
- **Acknowledgment `PA`**: `PA|RN<ROOM>|AS<status>|P#<unique-seq>|DA..|TI..|` — `ASOK` = posted; other `AS` codes = failure/reason. The `PA` **echoes the same `P#`**, which is how the charge is correlated.
- **Timeout (spec §5):** minimum 30 s general, **60 s for `PR`**. No `PA` within the timeout ⇒ stop waiting ⇒ command is **UNKNOWN** (never a blind resend).
- **Two distinct identifiers — do not conflate:**
  - **StayConnect internal `idempotency_key`** — stable for the *logical* Posting (derived from the durable `site-stay-purchase-seq`, contract §4.5). It is the anchor for our own state machine, ledger, and manual-review correlation. It never changes across attempts of the same logical Posting.
  - **FIAS `P#` (Posting Sequence Number)** — a *protocol* transaction/reference value. The spec says it "shall be unique as per message sent," but **whether Protel deduplicates on `P#`, and how it treats a replayed or reused `P#`, is NOT a proven idempotency guarantee** — it is unverified for this installation and **must be measured** (see §6 and Unresolved fields). This plan makes no assumption that a given `P#` is safely replayable or that Protel rejects a duplicate `P#`.

### Step gating & separate approvals

The financial scenarios are **not** a single run. Each is a **separately approved step**, executed only after the previous one passed and its evidence was reviewed:

1. **Normal charge** (§4) — must fully succeed (`PA ASOK` + Front Office confirms the single expected line).
2. **Reversal** (§5) — must fully succeed (folio net-zero confirmed).
3. **Lost-ACK** (§6) — only after 1 and 2 passed.
4. **Checkout / stale-occupancy** (§7) — only after 1–3 passed and only if the owner approves altering the test reservation.

**Blocking rule:** if the **normal charge (§4) or the reversal (§5) fails or ends UNKNOWN and is not cleanly reconciled to net-zero, ALL later scenarios are blocked** — no lost-ACK, no checkout/staleness — until the folio is confirmed net-zero and the owner re-approves.

### §4 Normal-charge flow (one charge, immediately reversed)

1. **Pre-test folio evidence:** Front Office reads and records the `<FOLIO>` balance/line-items; StayConnect issues an inquiry `PR` (read-only) and records the pre-state (redacted).
2. **Posting request:** send exactly one posting `PR` for `<AMOUNT>`/`<CURRENCY>` (as `TA<amount-minor>`) with a fresh unique `P#` and `<CODE>`. **Guard:** the computed minor-unit `TA` is asserted equal to the approved `<AMOUNT>` before the socket write; mismatch ⇒ **ABORT** (no send).
3. **Expected acknowledgment:** one `PA` with `ASOK` echoing the same `P#`, within 60 s. Non-`OK` `AS` ⇒ treat as not-posted, record reason, stop.
4. **Post-test folio verification:** Front Office confirms exactly one line of `<AMOUNT>` `<CODE>` appeared; StayConnect re-inquires (read-only) and records the delta = the single expected charge.
5. **Idempotency/reference strategy:** one posting in flight at a time; unique `P#` per attempt; the `PA`'s `P#` is the reference tying ack↔request; no auto-retry (§6).
6. **Immediate rollback:** proceed straight to §5 reversal so the guest's folio nets to its pre-test balance.

### §5 Reversal flow (undo the test charge)

1. **Reversal record:** per Protel's specified method `<METHOD>` (typically a rebate/credit or negative posting via `PR`), referencing the original charge (original `P#` and/or `X1` cross-reference) with its own **new unique `P#`**. Exact `<METHOD>` and the reference field are a **fixture gap** — confirmed with Protel before send; not assumed.
2. **Expected acknowledgment:** one `PA` `ASOK` echoing the reversal's `P#` within 60 s.
3. **Final folio verification:** Front Office confirms the charge and reversal net to zero on `<FOLIO>` and the pre-test balance is restored; StayConnect re-inquires (read-only) to record net-zero.

### §6 Lost-ACK scenario (safe — only after the request is proven transmitted)

1. Send one posting `PR` (its `P#` recorded and linked to the logical Posting's internal `idempotency_key`); **confirm the bytes were transmitted** (socket write flushed / `send()` fully returned for the framed record) — the interruption is applied **only after** transmission is proven, never before.
2. **Interrupt the connection** (close our own client socket) **before** the `PA` is received. No FIAS "interrupt" record is sent — this is a transport drop of our own connection only; the PMS link/other clients are unaffected.
3. No `PA` within the 60 s timeout ⇒ the command is **UNKNOWN** (contract: `posting → SENDING → UNKNOWN`).
4. **Never auto-retry — with either the same or a new `P#`.** A resend of the same `P#` may double-post if Protel does not dedup on it; a resend with a new `P#` definitely creates a second charge if the first actually posted. Since Protel's `P#` replay/dedup behavior is unverified (§3), **any** automatic retry is unsafe. The command routes to **MANUAL_REVIEW** and waits for external evidence.
5. **External reconciliation:** Front Office inspects `<FOLIO>` for a line matching the amount/reference and reports whether the charge reached Protel.
6. **Audited Manual-Review decision** (contract §15): `CONFIRM_POSTED` (folio shows it → mark POSTED, then reverse via §5) / `CONFIRM_NOT_POSTED_ABANDON` / **`CONFIRM_NOT_POSTED_RETRY`**. A manually-approved retry creates a **new protocol attempt linked to the same internal `idempotency_key`** (same logical Posting, new ledger attempt row). **Whether that new attempt reuses the same `P#` or allocates a new `P#` is itself unresolved** and must be grounded in the previously working integration, Protel configuration/spec, or measured behavior before it is exercised — it is not decided by this plan. Whatever the outcome, the test folio is left net-zero.

### §7 Checkout-while-link-down and stale-occupancy (only the approved test reservation)

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

- One posting in flight at a time; wait up to 60 s for `PA`; unique `P#` per attempt; no auto-retry.
- Every financial step is bracketed by Front Office pre/post folio evidence and a StayConnect read-only re-inquiry.
- The single test charge is **immediately reversed** (§5) so the guest folio returns to net-zero; if reversal fails, escalate to Front Office for manual correction.
- Connections are brief, occupancy-checked before connect, and gracefully closed; all evidence redacted; guest values never stored beyond the approved test identifiers.
- Everything pins the Hotel 3 (pms1) namespace; no crossing to Hotel 2.

### Redacted planned message sequence (templates only — no real values, nothing sent)

```
# link up (read-only)
S→C  LS|DA<..>|TI<..>|
C→S  LS|DA<..>|TI<..>|
C→S  LD|DA<..>|TI<..>|IFPB|V#1.13|RT4|
C→S  LR|RIGI|FLRNG#GNGFGAGD|   LR|RIGC|FLRNG#GNGFGAGD|   LR|RIGO|FLRNG#|
S→C  LA|                       # link alive
S→C  GI.. / GC..               # resync (redacted; isolate ONLY <RESERVATION>)

# inquiry (read-only, no TA)
C→S  PR|G#<RESERVATION>|RN<ROOM>|PI<inq>|WS<ws>|DA<..>|TI<..>|
S→C  PA|... (or PL| for sharers)     # confirm folio = <FOLIO>

# charge (Gate 3 only)   — TA minor-unit encoding UNVERIFIED; P# = FIAS protocol ref (dedup UNVERIFIED)
C→S  PR|G#<RESERVATION>|RN<ROOM>|TA<amount-minor>|PT<type?>|P#<seqA>|WS<ws>|<CODE-field?>|DA<..>|TI<..>|
S→C  PA|RN<ROOM>|ASOK|P#<seqA>|DA<..>|TI<..>|

# reversal (Gate 3 only) — <METHOD> UNVERIFIED; references original attempt; P# reuse-vs-new per grounded decision
C→S  PR|...|TA<amount-minor>|<METHOD-fields ref seqA>|P#<seqB?>|WS<ws>|DA<..>|TI<..>|
S→C  PA|RN<ROOM>|ASOK|P#<seqB?>|DA<..>|TI<..>|

# The logical Posting is keyed by StayConnect internal idempotency_key; P# is ONLY the FIAS protocol ref.
# <CODE-field?>, <type?>, TA encoding, <METHOD>, and P# reuse-vs-new are UNRESOLVED — see below.
```

### Unresolved Protel-specific fields (must be grounded/confirmed before Gate 3)

None of these are backed by a prior working StayConnect posting integration (the connector is lookup-only); each is spec-derived and **must be confirmed from Protel configuration/spec or measured** before any charge:

1. **Posting Code field** — which FIAS field carries `<CODE>` (e.g. `PT` Posting Type, `SO` Sales Outlet, or PMS interface/department configuration).
2. **Posting Type `PT`** value for a direct internet charge to a folio.
3. **Amount encoding** — minor-unit/no-separator confirmed for this installation, and the correct currency exponent for `<CURRENCY>`.
4. **`P#` behavior** — uniqueness requirement, and whether Protel deduplicates/rejects a replayed or reused `P#` (drives the §6 retry `P#` decision). To be measured, not assumed.
5. **Reversal/correction method `<METHOD>`** — negative posting vs rebate/credit vs correction record, and the exact field referencing the original charge.
6. **Response-timeout behavior** — actual `PA` latency and whether Protel ever answers late after our timeout (affects UNKNOWN handling); spec default is 60 s for `PR`.
7. **`PL` (Posting List) handling** — sharer/multi-folio selection response shape for this property.

### Gate 3 authorization

Gate 3 executes **only** after: (a) all fixture GAPs are filled with real values; (b) every "Unresolved Protel-specific field" above is confirmed with Protel operations or measured read-only; (c) each financial scenario is **separately** approved per "Step gating," with a failed/unreconciled charge or reversal blocking all later scenarios; and (d) the owner explicitly approves this written plan. Until then, nothing financial runs.

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
