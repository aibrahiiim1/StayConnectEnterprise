# Phase 1A — Corrected Acceptance Classification (scratch)

Statuses: **PASS** (directly proven with evidence) · **FAIL** (tested, failed) · **DEFERRED** (belongs to a later approved phase) · **N/A-SCRATCH** (cannot be proven in a pure-SQL scratch DB; needs the running appliance/services). NOT-APPLICABLE and DEFERRED are **never** reported as PASS.

Evidence lives in `../EVIDENCE.txt` (test IDs referenced below). Catalog fingerprint (fixture == real schema): `bd75026ff6ea5835a1ca8d19051eb257`.

## Contract §19 A-series (engine)

| Req | Requirement | Status | Evidence / reason |
|---|---|---|---|
| A1 | Shared immovable window across devices | **PASS** | `WIN-01` (window move blocked w/o adjustment) + one entitlement bound to multiple devices (`DEV-01/02/03`); window is per-entitlement, shared. |
| A2 | Device over-limit REJECT + surfaced | **PASS** | `DEV-02` (`MAX_DEVICES_REACHED` at max=2); `RACE-01/02` (max=1 race → exactly one). |
| A3 | Same-device reconnect replaces session, no slot burn | **PASS** | `DEV-03` (`RECONNECT`, no new binding). |
| A4 | Duplicate/concurrent closes charge usage once (watermarks) | **PASS** | `ACC-02` (duplicate sample = DUPLICATE), `ACC-05` (2nd close = ALREADY_ENDED). |
| A5 | Aggregate data-cap → one atomic terminal transition, all sessions revoked once | **PASS (DB primitive)** / **DEFERRED (enforcement loop)** | DB proven: `CAP-01` terminal transition to `TERMINATED`/`terminal_reason='DATA'` is atomic and one-way (`ENT-02`). The *decision to terminate at the cap* and *revoking live sessions* is `acctd`/reaper service logic → **DEFERRED** to the accounting-service phase. |
| A6 | SIGKILL/restart/reboot durability; re-auth gets remainder only | **PASS (persistence)** / **N/A-SCRATCH (appliance reboot)** | `RESTART-01/02` prove data + window survive a **container restart**. A real **appliance** reboot with scd/acctd re-attach is **N/A-SCRATCH**. |
| A7 | No exit from TERMINATED | **PASS** | `ENT-02` (trigger rejects TERMINATED→ACTIVE). |
| A8 | Supersession rebind zero churn; cross-subject supersession rejected | **PASS (rejection)** / **N/A-SCRATCH (nft/tc zero-churn)** | `ENT-05` (cross-subject rejected) + same-subject supersession accepted. "Zero nft/tc churn" is a data-plane effect → **N/A-SCRATCH**. |
| A9 | Suspension revokes sessions; window keeps running | **PASS (DB state)** / **N/A-SCRATCH (session revocation side-effect)** | `SUSP-01` (status→SUSPENDED allowed; window unchanged; one-live index still holds). Actual session teardown is scd → **N/A-SCRATCH**. |
| A10 | Late samples ledgered, never reopen | **PASS** | `ACC-03` (older seq = STALE: ledgered, no double count), `REOPEN-01` (sample after TERMINATED does not reactivate). |
| A11 | Capacity counts distinct devices; failure leaves zero session/device/binding rows | **PASS** | `DEV-02` + `CAP-RESID-01` (after a rejected admission, zero residual `entitlement_devices` rows). |
| A12 | Counter-reset epoch handling | **PASS** | `ACC-04` (epoch bump handled; fresh delta). |
| A13 | Reconciliation rebuild; decreases only via audited adjustment | **PASS (audited-only)** / **DEFERRED (rebuild procedure)** | `ENT-03` (direct decrease rejected), `ENT-04` (audited adjustment logs + decreases). Full ledger→counter *reconciliation rebuild* is a later procedure → **DEFERRED**. |
| E4b | Folio `UNSET` fail-closed: CHARGE rejected before outbox/`P#`/transmission | **PASS** | `FOLIO-01` (blocked), `FOLIO-02` (no side-effects), `FOLIO-03` (concrete admits), `FOLIO-04` (non-IN_HOUSE blocked). |

## Migration / schema / isolation

| Req | Status | Evidence |
|---|---|---|
| Migration up / down / re-up | **PASS** | `MIG-01/02/03` |
| MG-0 non-transactional `CONCURRENTLY` + invalid-index recovery + no bare `IF NOT EXISTS` | **PASS** | `MG0-REC-01/02`; `mg0.sh` |
| **Migration idempotency (apply set twice, no down)** | **PASS** | `IDEM-02/03/04` (ledger → 2nd apply skips all 9; fingerprint unchanged) |
| **Exact catalog equality (fresh build vs rebuild)** | **PASS** | `IDEM-05` (identical fingerprint) |
| Object/constraint/trigger inventories | **PASS** | `review/OBJECT_INVENTORY.txt`, `CONSTRAINT_INVENTORY.txt`, `TRIGGER_FUNCTION_INVENTORY.txt` |
| Composite tenant/site/interface ownership FKs; cross-tenant/site rejection | **PASS** | `FK-01/02/03` |
| Immutable revisions / append-only ledgers / one-way posting | **PASS** | `IMM-01..04`, `AO-01/02`, `PA-01/02` |
| **Offline real-schema compatibility (0001..0006)** | **PASS** | `OFR-01..09` (iam_v2 catalog identical on fixture and real committed schema) |

## Roles / least-privilege / safety

| Req | Status | Evidence |
|---|---|---|
| `iam_v2_owner` owns schema + all 49 objects (not superuser) | **PASS** | `ROLE-01/02/03` |
| `iam_v2_migrator` migration-only (member of owner) | **PASS** | `ROLE-04` |
| Service roles (`scd/edged/acctd/portald/hoteladm`) no SELECT/INSERT/UPDATE/DELETE | **PASS** | `ROLE-05/06/07` |
| PUBLIC no privileges; default privileges deny future access | **PASS** | `ROLE-08/09/10` |
| `search_path` excludes `iam_v2` | **PASS** | `ROLE-11` |
| Allowlist safety guard + negatives (live name, alt-live, non-local, wrong port, missing ack, empty/malformed DSN, false/missing marker) | **PASS** | `GUARD-00..11` |

## Explicitly NOT proven in scratch (must not be called PASS)

| Item | Status | Why |
|---|---|---|
| Appliance reboot behavior (real hardware/VM) | **N/A-SCRATCH** | No appliance access; only container-restart proxy proven. |
| Real `scd`/`acctd` integration | **N/A-SCRATCH** | No running services in scratch. |
| nft/tc zero-churn on supersession | **N/A-SCRATCH** | Data-plane effect; no nft/tc in scratch. |
| Running-production-service zero-write evidence | **N/A-SCRATCH** | No production services running; role-isolation + zero repo refs proven instead. |
| Live service DSN / `search_path` behavior | **N/A-SCRATCH** | No live services; scratch `search_path` proven clean. |
| Real traffic accounting (non-zero device usage) | **N/A-SCRATCH** | Needs a real device + data plane. |
| Service session-revocation side effects | **N/A-SCRATCH** | Needs scd session teardown. |
| Aggregate-cap enforcement loop; reconciliation rebuild | **DEFERRED** | acctd/reaper + later reconciliation procedure. |
