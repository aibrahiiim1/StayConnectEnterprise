# Protel FIAS — Phase 0 Live Spike Record

**Spike status: `GATE1B_COMPLETE — READ-ONLY LINK-ALIVE CONFIRMED ON BOTH INTERFACES (no auth key; no financial traffic)`**

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

## Test Fixtures (placeholders — to be supplied before Gate 2)

| Item | pms1 | pms2 |
|---|---|---|
| Test room | _TBD_ | _TBD_ |
| Reservation number | _TBD_ | _TBD_ |
| Guest / family name | _TBD_ | _TBD_ |
| Folio | _TBD_ | _TBD_ |
| Posting code (test article) | _TBD_ | _TBD_ |
| Test amount + currency | _TBD_ | _TBD_ |
| Reversal method | _negative posting (confirm during preflight)_ | _TBD_ |
| Front Office contact | _TBD_ | _TBD_ |
| Approved maintenance window | _TBD_ | _TBD_ |

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

**Interpretation.** Both endpoints are live FIAS 2.20 peers with correct framing, reachable and stable. The link will not advance to the data-streaming ("alive") state — and thus the in-house guest cache required for a read-only reservation/folio lookup will not populate — until the interface presents a valid **IfcAuthKey** and the PMS side has our interface **registered/enabled**. This is the expected, safe result of connecting without credentials; no secret was guessed or brute-forced.

**Separate properties?** The two endpoints are distinct hosts/ports answering independently (independent `LS` timestamps), consistent with two separate PMS interfaces. This **cannot be positively confirmed as two separate properties from the protocol pre-authentication**, because no property/hotel/IFC identifier is exposed before link-up. Confirmation requires either owner attestation or post-authentication `LD`/link data (Gate 2+).

## Gate 3 — Live Spike Scenarios (requires explicit approval after Gate 2)

1. Small test charge posted; verified on the Protel folio.
2. Reversal of the test charge; verified on the folio.
3. Lost-ACK drill: link interrupted mid-post → command lands `UNKNOWN`; verified against the folio (posted or not) → manual-review path.
4. Checkout while link down: staleness behavior of lookups and the financial fresh-validation block.
5. Stale occupancy: room move during staleness → occupancy re-verification aborts posting.
6. Heartbeat/keepalive and night-audit/resync cadence measurements.
7. Folio-number reuse behavior (drives `folio_identity_strategy` for the connector revision).

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
