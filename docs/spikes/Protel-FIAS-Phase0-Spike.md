# Protel FIAS — Phase 0 Live Spike Record

**Spike status: `BLOCKED_PENDING_READ_ONLY_PREFLIGHT`**

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
| Protel version | _unknown — capture during preflight_ |
| FIAS/IFC version | _unknown — capture during preflight (spec on file: FIAS 2.20, `docs/FIAS_2.20.24.pdf`)_ |
| Property/Hotel code | _to be supplied / captured during preflight_ |

### PMS Interface 2 (`pms2`)

| Field | Value |
|---|---|
| Host/IP | `120.0.0.15` |
| TCP port | `5001` |
| TLS | disabled |
| Protel version | _unknown — capture during preflight_ |
| FIAS/IFC version | _unknown — capture during preflight_ |
| Property/Hotel code | _to be supplied / captured during preflight_ |

> Note: two independent interfaces were supplied — this also enables live validation of the
> multi-PMS namespace and duplicate-source-detection requirements during later phases.

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

## Gate 1 — Read-Only Preflight Checklist (not yet started)

- [ ] TCP connectivity to `150.0.0.18:5003` (pms1)
- [ ] TCP connectivity to `120.0.0.15:5001` (pms2)
- [ ] FIAS link handshake (LS/LA) and record framing verified
- [ ] Heartbeat/keepalive cadence observed and recorded
- [ ] Protel + FIAS/IFC versions identified
- [ ] Approved test reservation lookup succeeds (room + name / reservation number)
- [ ] Folio identification for the test reservation
- [ ] Confirmed: **no posting sent, no link interruption performed**

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
| Heartbeat cadence | — | — |
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
