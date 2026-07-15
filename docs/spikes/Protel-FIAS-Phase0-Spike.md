# Protel FIAS — Phase 0 Live Spike Record

**Spike status: `GATE1B_HELD — PRECONDITIONS_NOT_MET (no IfcAuthKey / no property attestation / route + IFC-safety unconfirmed)`**

Gate 1 (read-only preflight) executed 2026-07-15 against both approved endpoints. TCP reachability and FIAS 2.20 framing confirmed on both; the FIAS link could not be brought to the "alive" state read-only because the interface authentication key and PMS-side interface registration are not yet provided (correctly not attempted/guessed). No posting, no reversal, no link interruption, no guest data received, no network scanning.

**Gate 1B (authenticated read-only) was requested 2026-07-15 but NOT started:** the five mandatory "before connecting" preconditions are unsatisfied (see "Gate 1B — Precondition Hold" below). No authenticated connection was opened. Contract remains **CONDITIONALLY FROZEN**.

Governing documents: [StayConnect-IAM-Phase0-Contract.md](../architecture/StayConnect-IAM-Phase0-Contract.md) (§9 receives this spike's measured results) and [StayConnect-IAM-Handoff.md](../context/StayConnect-IAM-Handoff.md) (execution gates).

Rules of engagement:
- **Gate 1 (read-only preflight)** is the only currently authorized action, and it must be explicitly started by the product owner: connectivity, FIAS handshake/framing, heartbeat, version identification, approved test reservation/folio lookup. **No posting. No link interruption. No network scanning.**
- **Gate 2:** present the exact live test plan; wait for explicit approval.
- **Gate 3 (after approval only):** live posting scenarios listed below.
- No passwords, interface auth keys, or other secrets are ever recorded in this document.

## Supplied Connection Information

### PMS Interface 1 (`pms1`)

| Field | Value |
|---|---|
| Host/IP | `150.0.0.18` |
| TCP port | `5003` |
| TLS | disabled |
| Protel version | _not revealed pre-authentication (no V# exposed in LS)_ |
| FIAS/IFC version | _not revealed pre-authentication; spec on file: FIAS 2.20, `docs/FIAS_2.20.24.pdf`_ |
| Property/Hotel code | _not revealed pre-authentication_ |

### PMS Interface 2 (`pms2`)

| Field | Value |
|---|---|
| Host/IP | `120.0.0.15` |
| TCP port | `5001` |
| TLS | disabled |
| Protel version | _not revealed pre-authentication (no V# exposed in LS)_ |
| FIAS/IFC version | _not revealed pre-authentication_ |
| Property/Hotel code | _not revealed pre-authentication_ |

> Note: two independent interfaces were supplied — this also enables live validation of the
> multi-PMS namespace and duplicate-source-detection requirements during later phases.

## Gate 1B — Precondition Hold (2026-07-15, no connection opened)

Gate 1B (authenticated, strictly read-only) was requested. Its own instruction defines five mandatory checks **before connecting**, plus abort triggers. None of the five were satisfiable from the information available, and two abort triggers are already active. **No authenticated FIAS connection was opened; no traffic of any kind was sent this step.** Reasoning, per precondition:

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

## Current Blocker (after Gate 1)

The FIAS link cannot reach the data-streaming state read-only, so no reservation/folio can be queried yet. To unblock, the following are required **per interface** (secrets supplied out-of-band, never stored in this repo):

1. **Interface authentication key (IfcAuthKey)** for the FIAS `LD`/`CG`/`RT4` authentication — provided out-of-band; will live only as an AEAD secret generation, never in Markdown.
2. **PMS-side interface registration/enable** so Protel accepts our `LD` and begins the database resync + guest feed (an operator action on the Protel/IFC side).
3. **Expected interface name / IFC number** the PMS is configured to accept (the harness currently sends the default `IFPB`).
4. **Expected `V#` (version) and `RT` (record-transfer type)** values, if the property mandates specific ones.
5. Owner **confirmation of the routing path for pms2** (`120.0.0.15`, a public-range address currently routed over the WAN `ens160`).

Once the link comes up read-only, a further read-only step can confirm a test reservation and its folio from the in-house cache before any Gate-2/Gate-3 planning.

## Gate 2 — Required Inputs Still Needed (per interface, before any live plan)

- IfcAuthKey + interface registration (items 1–4 above);
- test room + reservation number + guest/family name (for the read-only lookup match);
- folio id to be used as the posting target;
- posting code (test article) + test amount + currency;
- reversal method confirmation (expected: negative posting);
- Front Office contact + approved maintenance window;
- explicit owner approval of the written Gate-2 live test plan.

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
- [ ] Protel + FIAS/IFC versions identified — **not revealed pre-authentication** (blocked)
- [ ] Approved test reservation lookup — **blocked** (link not alive → no in-house cache; requires IFC auth + registration + approved fixtures)
- [ ] Folio identification — **blocked** (same cause)
- [x] Confirmed: **no posting sent, no reversal, no link interruption, no guest data received, no network scanning**

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
