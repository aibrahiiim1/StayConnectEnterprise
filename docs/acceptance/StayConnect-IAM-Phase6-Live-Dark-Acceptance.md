# StayConnect IAM Phase 6 — Live-Dark Acceptance

**Status: PRODUCT-OWNER ACCEPTED_AND_CLOSED — VERIFIED LIVE-DARK (decision D26, closure transition T0061, 2026-08-16 UTC).**

- Phase: 6 — Guest Device Self-Service + the `AGGREGATE_ONLINE_TIME` package time mode. One end-to-end Phase.
- **Authorized** under Product-Owner decision **D25**, authorization transition **T0057** (2026-08-15).
- Milestones closed by **T0058** (M2), **T0059** (M3) and **T0060** (M4).
- **Product-Owner ACCEPTED and CLOSED** by decision **D26**, closure transition **T0061**
  (`transition_accepted: true`) at verified **LIVE-DARK** maturity.
- Branch: `phase/6-device-selfservice-and-time-modes`; **PR #14**.
- Appliance: `radius` / `172.21.60.23` — the **development** appliance. Production was never contacted.
- Dates are **UTC**, matching the transition ledger.

---

## The accepted basis, in three separable parts

Kept apart on purpose. Collapsing them is how a green test run starts being read as a business decision.

### 1. Software-CI evidence — *does the delivered code pass its own gate?*

| | |
|---|---|
| Accepted delivery head (PR #14 head) | **`1bdf9bfbd96b7f0264d634183d5cc8e69904cbb9`** |
| Provenance (`acceptance_candidate_head` = `inventory_head`) | **`5358013eabbabda1ad8d519fa9b93c9c0893d672`** |
| Phase base | **`09e67156fb6cb286fe47fe632a368a3c4e4c6d23`** · 57 commits · **161 paths** |
| Project Governance | **31921408049 — SUCCESS** |
| Phase 3 Software CI | **31921407939 — SUCCESS** |
| Phase 4 Financial Core CI | **31921408036 — SUCCESS** |
| Phase 5 Post-Stay and Transfer CI | **31921407981 — SUCCESS** |

Repository and database gates at that head: rollback rehearsal **65/65** across `0030`–`0047` down and back up
including the `0032` boundary, mutation-proven to fail hard; least privilege **65/65** measured as the real
roles; foundation **50/50**; device self-service **22/22**; aggregate online time **49/49**; backup and restore
**18/18**; the full tagged Go integration suite, unit tests, `go vet` and `go build` green;
`CURRENT_STATE_PARITY` 23/23; `PROJECT_STATE_GOVERNANCE` PASS; `ZERO_STALE_LEFTOVERS` PASS.

### 2. Runtime evidence — *does it behave correctly on a real appliance?*

One supervised controlled run under D25 that enabled the capabilities and restored them: **42 proofs, 0
failures**, then a real reboot. Detail in
[the appliance evidence](StayConnect-IAM-Phase6-Development-Appliance-Evidence.md).

The load-bearing proof is the **safe-disable invariant**, obtained on a live entitlement rather than asserted:
with the aggregate capability **disabled**, the already-durable budget is still accounted. Accrual is
data-driven precisely so that disabling the capability can never turn finite access into unlimited access.

### 3. The business decision — *is this accepted?*

Yes, at **verified LIVE-DARK maturity**, by decision **D26**. That means the capability has been proven to
work and is deliberately **not running**:

- every Phase-6 flag OFF and coherent across `scd`, `acctd` and `edged`;
- the guest device routes **ABSENT** from the running `scd` (404), not merely refusing;
- the Phase-6 operator screens **compiled out** of the deployed Hotel Admin bundle, because
  `NEXT_PUBLIC_PHASE6_ADMIN` is build-time state;
- no synthetic business state live, and no appliance with the per-appliance setting enabled;
- all of the above re-verified after a second reboot.

---

## What acceptance does NOT authorize

Enabling any Phase-6 capability on any environment · IAM-v2 production cutover · production data migration ·
dual read/write · legacy IAM removal · real guest service · real PMS financial posting · real
payment-provider traffic · paid-access activation · per-property financial enablement · programmatic
reversal. Each remains a separate Product-Owner decision.

---

## Limitations preserved without promotion

Nothing here is upgraded from NOT PROVEN to PASS by the act of acceptance.

1. **A full in-place restore of the appliance's `stayconnect_site` is NOT PROVEN.** It requires a restore
   manifest signed off-appliance with the registry root key and verified against the appliance's own pinned
   anchor. What *was* proven is a lesser and different claim: the backup artefact taken with the sanctioned
   script **restores** into a scratch database on the appliance (81 `iam_v2` tables, 18 `p6_*` functions).
2. **Enabling Phase 6 is not a flag flip.** `scd` and `acctd` both refuse to start without `EXECUTE` on
   `iam_v2.begin_controlled_operation`, which `svc_scd` and `svc_acctd` do not hold. That grant is a Phase-3
   provisioning step and a prerequisite of any future enablement decision.
3. **The Phase-6 operator screens were not validated on the appliance**, because the deployed bundle is built
   without `NEXT_PUBLIC_PHASE6_ADMIN`. Validating them needs a bundle built with it.
4. **One appliance, once, under supervision.** The capability has never carried a real guest.
5. The controlled run leaves **terminated** business state and its append-only audit trail under a reserved
   stay on the development appliance. Nothing is live and nothing carries access.

---

## Defects found and fixed before acceptance

Recorded because the phase's value is partly in what it caught.

- **0046** — suspension evidence claimed a termination one level down: devices and sessions were closed as
  `ENTITLEMENT_ENDED` while the parent deliberately recorded no terminal truth.
- **The rollback rehearsal counted failures as passes** — it matched psql output text, so a missing file and
  an unreachable database both produced no `ERROR` line.
- **0047** — the guest surface could not resolve the device asking it a question; device resolution is an
  upsert, and `svc_scd` held `SELECT` only. Found only by running as the real service role.
- **The most serious, caught by CI** — the expiry sweep called Phase-6 functions and named a Phase-6 column
  unconditionally, so on a rolled-back schema **nothing expired at all** and every out-of-budget guest would
  have kept access.
- **The harness itself** — it would have written a test marker into a constrained business vocabulary and
  disabled the product setting with a global owner-level `UPDATE`; and its restoration ratcheted an enabled
  setting forward run after run while the baseline check passed.
- **Two governance mutation cases (M46, M48)** had silently stopped biting after Phase 6 published its own
  change manifest.
