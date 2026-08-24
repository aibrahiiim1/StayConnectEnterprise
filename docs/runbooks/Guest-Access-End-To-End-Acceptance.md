# Controlled end-to-end guest-access acceptance — 172.21.60.25 (PRE-LIVE)

**PREPARATION ONLY. NOTHING IN THIS DOCUMENT IS AUTHORIZED TO RUN.**

Prepared against authoritative master `5af2d444b8b498b07058fc3cd5b2805d7a4f6dc4`, D37/T0087, the Phase-0
contract, and read-only evidence collected from the appliance on 2026-08-24.

## What this proves, and what it does not

The chain **Room authentication → eligible offer → package acquisition → entitlement issuance → session
activation → actual network enforcement** has never been exercised on this appliance. `purchases`,
`entitlements` and `sessions` are all zero. Room sign-in is proven only as far as VERIFIED → Auth Context →
eligible offer (2026-08-23T11:59:40Z).

This is the currently unproven end-to-end guest-access acceptance. It is **not** "the last Go-Live
prerequisite" and this document makes no Go-Live claim. Go-Live readiness is assessed separately, after
verified execution, on evidence this test does not by itself supply.

## The non-financial path exists, and it is enforced in code

The site carries one non-system package — **`Free Internet Package`**, published, `GENERAL`,
`price_minor = 0`, `settlement_methods = {NOT_REQUIRED}`, bound to service-plan revision **`Free Internet`**
(enabled, rev 1, `max_concurrent_devices = 1`, `device_limit_policy = REJECT_NEW_DEVICE`,
`data_quota_bytes = 100000000`, `time_accounting_mode = VALIDITY_WINDOW`,
`duration_policy = {"end_mode": "MANUAL_END"}`).

Choosing it is not the safety mechanism. `internal/staygrant` refuses any package whose `price_minor != 0`
or whose settlement is anything other than exactly `NOT_REQUIRED`, returning `ErrSettlementRequired`. Paid
access fails closed in code, so the acceptance cannot drift into a financial path even by
misconfiguration. The PMS revision additionally carries `folio_identity_strategy = UNSET`, which makes
posting impossible regardless.

---

## 1. Proposed acceptance sequence

Every stage is gated: **do not begin a stage until the previous stage is PASS.** A FAIL or UNKNOWN at any
stage stops the run and is reported as-is.

### Stage 0 — preconditions (no appliance change)
Confirm, read-only, that every item in §2 holds. Capture the evidence bundle E0.

### Stage 1 — Protel socket returned to StayConnect
Product-Owner action, not an agent action. The Product Owner reassigns the single PMS client slot from the
existing production Wi-Fi system to StayConnect. The agent performs no reconnection, no dial, no
configuration change; `pmsd`'s existing retry loop reconnects on its own.

### Stage 2 — feed reaches the authorising state
Wait, read-only, for `iam_v2.pms_interface_runtime` to reach
`transport_status = CONNECTED`, `sync_status = IN_SYNC`, `continuity_status = CONTINUOUS`, with
`pinned_revision_id` equal to the interface's published revision and
`COALESCE(last_heartbeat_at, last_connected_at)` within the revision's `heartbeat_timeout_ms` (300000 ms).
Confirm `resync_started_at IS NULL` and `resync_generation_seq = published_resync_generation`.

Then confirm `iam_v2.p3_feed_authorizes(...) = true` for the chosen Stay. This is the same predicate the
authentication path uses; if it is false, Stage 3 cannot succeed and must not be attempted.

### Stage 3 — test Stay selection (§3), read-only
Select the Stay by rule from mirrored PMS data. Record only its internal identifiers and the boolean facts
needed; **no guest name, reservation number or other personal PMS data is recorded anywhere**.

### Stage 4 — Room authentication from the guest network
One real guest-portal sign-in from a test device on `PRELIVE Test Guest`, using the room number and the one
verification value, exactly as a guest would. Expect `outcome = VERIFIED`, an Auth Context issued, and at
least one eligible offer returned.

### Stage 5 — package acquisition
Accept the offered `Free Internet Package` through the portal. This consumes the Auth Context and creates
the Purchase.

### Stage 6 — entitlement issuance and session activation
The same transaction issues the Entitlement, authorizes the device, and opens the Session as
`PENDING_ENFORCEMENT`. `netd` then promotes it to `active` through `iam_v2.activate_session_enforcement`
once the accountable class and the packet-gate authorization are both proven.

### Stage 7 — network-enforcement proof
Prove from the kernel and from the device that access is real (§7).

### Stage 8 — negative / fail-closed checks (§8)

### Stage 9 — bounded cleanup (§9), then final evidence bundle

---

## 2. Preconditions and blockers

### Verified present as of 2026-08-24 (read-only)

| Precondition | State |
|---|---|
| Appliance enrolled, claimed, assigned | tenant `36f3ba78`, site `0d40f7c8`, appliance `c6faf4eb` |
| Licence | Active, cloud-validated, `valid_until 2026-12-01` |
| Guest network | `PRELIVE Test Guest`, untagged on `ens192`, bridge `br-g-00d1fa1a`, `192.168.77.1/24`, enabled |
| DHCP | Kea DHCP4 active, subnet `192.168.77.0/24` |
| Portal reachable | `portald` on `:8380`, Caddy on `:80`/`:443`; empirically reached from the guest network during earlier Product-Owner testing |
| PMS routing | `PRELIVE Test Guest → Protel`, mode `MAPPED` |
| PMS interface | `protel-fias`, label `Protel`, `ACTIVE`, published revision present |
| Free package | published, price 0, `NOT_REQUIRED` |
| Sign-in method | `room_any` enabled, voucher enabled |
| Services | scd, edged, netd, pmsd, portald, acctd, hotel-admin all active |
| nft objects | `inet stayconnect` with `phase3_auth_ipv4`, `auth_ipv4`, `guest_interfaces = {br-g-00d1fa1a}`, `guest_subnets = {192.168.77.0/24}` |

### Blockers

**B1 — Protel socket unavailable (stated, intentional). BLOCKING.**
The single PMS client slot is assigned to the existing production Wi-Fi system. Feed health is
`DISCONNECTED / RESYNC_REQUIRED / CONTINUOUS`, `DIAL_FAILED`. Room authentication authorises nobody in this
state, by design. Only the Product Owner can clear this, and doing so is Stage 1.

### Other prerequisites examined — none blocking, three worth deciding first

**P1 — the free plan's speed values look like a unit slip.** `down_kbps = up_kbps = 2048000`, i.e. **2 Gbps**
in each direction, on a plan named "Free Internet" with a 100 MB quota. That is almost certainly meant to be
2048 kbps or 2 Mbps. It does not block the run, but the enforcement proof in §7 compares the installed `tc`
class rate against the configured value, so the test will faithfully prove whatever is configured. **Decide
before execution whether to correct the plan** — correcting it is a configuration change and needs its own
authorization; running against 2 Gbps is acceptable if the intent is to prove the mechanism rather than the
commercial shaping.

**P2 — `MANUAL_END` means the test leaves durable state.** The package's `duration_policy` is
`{"end_mode": "MANUAL_END"}`, so the Entitlement does not expire on its own. Cleanup is therefore a
deliberate act, not a wait (§9).

**P3 — the upload-accounting path is the least-proven part.** No IFB device exists on the appliance yet
(`ip link show type ifb` is empty) and no `tc` class exists on `br-g-00d1fa1a`, consistent with zero
sessions. Both are created on demand by `netd`. This is the component with the least live evidence behind
it, and §7 treats its absence as a FAIL rather than a warning.

**Nothing else was found that would prevent execution once B1 is cleared.**

---

## 3. Test guest / Stay selection rules — no invented data

The Stay must be a real mirrored Stay. **No Stay, guest, room or reservation is created, edited or
fabricated for this test.**

Select by rule, in order:

1. `status = 'IN_HOUSE'` on the `Protel` interface.
2. `iam_v2.p3_feed_authorizes(tenant, site, interface, published_revision, occupancy_evidence_at) = true`.
3. Exactly **one** IN_HOUSE Stay shares its `normalized_room_number` — so the room is unambiguous and the
   `room_any` ambiguity rule cannot fire for reasons unrelated to the test.
4. The Stay has at least one `stay_guests` row with a non-empty `last_name_norm`, so a verification value
   exists.
5. Prefer a Stay with **no** existing entitlement or session (all are currently zero, so any qualifying Stay
   satisfies this).

If more than one Stay qualifies, take the lowest `normalized_room_number` for determinism. If none
qualifies, the run stops at Stage 3 with UNKNOWN and the reason recorded.

**Privacy:** the verification value is read from the database into the request and is never written to any
log, report, evidence file or commit. Evidence records only *whether* a factor matched.

---

## 4. Package / service-plan prerequisites

- `Free Internet Package` remains published with `price_minor = 0` and `settlement_methods = {NOT_REQUIRED}`.
- Its `service_plan_revision_id` resolves to the enabled `Free Internet` revision.
- No package or plan is created or modified for this test. If the offer set is empty at Stage 4, that is a
  configuration outcome and is reported as FAIL for that stage — it is not fixed mid-run.

---

## 5. Browser / device and guest-network path

- One test device (laptop or phone) that is **not** a guest's, connected to the `PRELIVE Test Guest`
  network on `ens192`.
- It must obtain a `192.168.77.0/24` lease from Kea. A static address is not acceptable: DHCP is part of the
  path being proven.
- Reach the portal as a guest does. Do not bypass the captive-portal flow and do not call scd's socket
  directly — the API-level path is already proven; what is unproven is the guest-visible one.
- Record the device MAC and leased IP; both appear in the Session row and are needed for the enforcement
  proof.
- Only **one** device participates. The plan allows one concurrent device with `REJECT_NEW_DEVICE`, and the
  second-device case is a deliberate negative check (§8), not an accident.

---

## 6. Expected database and runtime evidence

| Object | Expected |
|---|---|
| `iam_v2.auth_resolutions` | one new row, `outcome_code = VERIFIED`, `resolved_stay_id` set |
| `iam_v2.auth_contexts` | one new row; `consumed_at` **set** after Stage 5 |
| `iam_v2.purchases` | exactly one new row, price 0, settlement `NOT_REQUIRED` |
| `iam_v2.entitlements` | exactly one new row bound to that Purchase and Stay; exactly one live entitlement for the Stay |
| `iam_v2.sessions` | exactly one new row: `PENDING_ENFORCEMENT` → **`active`**, `ended IS NULL`, correct `device_id`, `ip`, `mac`, `ingress_interface = br-g-00d1fa1a` |
| `iam_v2.entitlement_state_transitions` | the issuance transition recorded |
| scd log | `phase3 auth` success; no `not verified` for the accepted attempt |
| netd log | accountable class installed, then packet-gate authorization, then promotion |
| PostgreSQL log | **zero** `permission denied` and zero `ERROR` for the run window |

---

## 7. Network-enforcement proof

Session state alone is not proof. All four must hold:

1. **Packet gate** — the device's leased IP is an element of `nft` set `inet stayconnect phase3_auth_ipv4`
   (and `auth_ipv4` where the design places it).
2. **Accountable download class** — a `tc` class exists on `br-g-00d1fa1a` for this session, with a rate
   matching the plan revision's `down_kbps` (see P1).
3. **Accountable upload class** — the per-bridge IFB device exists and carries the matching upload class at
   `up_kbps`. Its current absence is expected only because there is no session; after Stage 6 its absence is
   a **FAIL**.
4. **Actual reachability from the device** — from the test device, a destination *outside* the walled garden
   is reachable. The walled garden currently holds only `1.1.1.1`, `8.8.4.4`, `8.8.8.8`, so those prove
   nothing; use an unrelated public destination. Confirm both DNS resolution and a TCP connection.

**Before/after contrast is required.** Capture 1–4 *before* Stage 4 (expected: not authorized, no classes,
no off-garden reachability) and again after Stage 7. A test that only observes the "after" state cannot
distinguish enforcement from a network that was open all along.

Optionally, confirm `iam_v2.accounting_records` receives byte counters for the session — the metering half of
"accountable".

---

## 8. Negative / fail-closed checks

Each is expected to FAIL SAFELY. None may be skipped.

| # | Check | Expected |
|---|---|---|
| N1 | Second device on the same Stay | Refused — plan allows 1 device, `REJECT_NEW_DEVICE` |
| N2 | Replay the consumed Auth Context | Refused — one-time semantics |
| N3 | Present the Auth Context from a different device/MAC | Refused — context is device-pinned |
| N4 | Second grant against the same Stay | Refused — exactly one live entitlement per Stay |
| N5 | Room + deliberately wrong verification value | Uniform guest failure message; no context, no purchase |
| N6 | Unauthenticated device on the guest network | No off-garden access; not in `phase3_auth_ipv4` |
| N7 | Guest-facing failure text | Identical uniform message throughout; no PMS/internal detail, no PMS selector |

N5 and N6 use the test device only. N5 must not be run against a real guest's Stay with a real wrong value
in a way that could lock anything — throttling behaviour should be observed, not provoked repeatedly.

---

## 9. Rollback and cleanup — bounded, and honest about what exists

**This test creates durable state that does not expire.** `MANUAL_END` means the Entitlement persists until
ended deliberately.

Available, using existing approved mechanisms only:

1. **End the session** — through the product's own session-end path, so `sessions.ended` is set and `netd`
   withdraws the packet-gate authorization and the accountable classes.
2. **Terminate the entitlement** — through `iam_v2.terminate_entitlement_at_boundary` or the approved
   admin path, producing a recorded state transition.
3. **Confirm withdrawal** — the device's IP is gone from `phase3_auth_ipv4`, the `tc` classes are gone, and
   off-garden reachability from the test device has stopped.
4. **Disconnect the test device.**

**What cleanup cannot do, stated plainly:**

- **The Purchase row is not reversible.** Phase 4's reversal model is a passive ledger row required by
  contract; there is no executable reversal sender, and inventing one is forbidden. A zero-price Purchase
  with settlement `NOT_REQUIRED` will remain in `iam_v2.purchases` permanently as a true record that this
  acceptance happened. That is the intended behaviour and must be accepted before execution, not discovered
  after.
- `auth_resolutions` and `auth_contexts` rows are append-only history and remain.
- Accounting records for the session remain.

Cleanup restores *access*, not *history*. Nothing in this plan deletes or rewrites a durable record.

---

## 10. PASS / FAIL / UNKNOWN matrix

**UNKNOWN** means the stage could not be evaluated — it is never reported as PASS, and never as FAIL either.

| Stage | PASS | FAIL | UNKNOWN |
|---|---|---|---|
| 0 Preconditions | every §2 item holds | any verified item no longer holds | a check could not be read |
| 1 Socket returned | PO confirms reassignment | PO declines / not done | — |
| 2 Feed healthy | CONNECTED + IN_SYNC + CONTINUOUS + pinned + live; `p3_feed_authorizes = true` | any axis wrong after a reasonable settle period | runtime unreadable |
| 3 Stay selection | exactly one Stay meets all five rules | no Stay qualifies | mirrored data unreadable |
| 4 Room auth | `VERIFIED` + Auth Context + ≥1 offer | NOT_VERIFIED, or verified with zero offers | portal/network fault prevents the attempt |
| 5 Acquisition | one Purchase, price 0, `NOT_REQUIRED`; context consumed | refused, or any settlement demanded | request outcome not observable |
| 6 Entitlement + session | one Entitlement; Session `PENDING_ENFORCEMENT` → `active` | no entitlement, or session stuck pending | promotion not observable in the window |
| 7 Enforcement | all four proofs in §7, with before/after contrast | any one missing — including the IFB upload class | kernel state unreadable |
| 8 Negative checks | all seven refuse as specified | any one permits what it must refuse | a check could not be run |
| 9 Cleanup | access withdrawn and confirmed | access persists after cleanup | withdrawal not observable |

**Overall PASS requires every stage PASS.** Any FAIL, or any UNKNOWN at stages 4–7, means the guest-access
chain remains unproven and is reported as such.

---

## 11. Financial-safety boundaries

Throughout, and without exception:

- **Only** the zero-price `NOT_REQUIRED` package. `staygrant` refuses anything else in code
  (`ErrSettlementRequired`); this is structural, not a matter of choosing carefully.
- **No PMS posting.** `folio_identity_strategy = UNSET` makes it impossible; no PS or charge frame is sent.
- **No payment-provider traffic**, no settlement, no refund, no FX, no reversal.
- **No paid package** is created, published, offered or accepted.
- If any stage produces a demand for settlement, the run **stops immediately** and reports FAIL. It is not
  worked around.

---

## 12. Evidence to capture

- **E0 (before):** full precondition read-out; feed state; nft set contents; `tc`/IFB state; row counts for
  purchases, entitlements, sessions; off-garden reachability from the test device.
- **E1:** feed state at the moment of the attempt; `p3_feed_authorizes` result.
- **E2:** portal request/response outcome codes only — never the verification value.
- **E3:** the new rows, by id and bounded field values.
- **E4:** nft set membership, `tc` classes on bridge and IFB, session state transitions, netd log lines.
- **E5:** device-side reachability before and after.
- **E6:** each negative check and its refusal.
- **E7:** post-cleanup state.
- **E8:** PostgreSQL and service logs for the window; a count of `permission denied` and `ERROR`, expected zero.

**Privacy rules for all evidence:** no guest name, surname, first name, reservation number, folio number or
other personal PMS data. Record only whether a factor matched, and internal identifiers.

---

## 13. What remains prohibited throughout

No application code change, migration, database mutation, grant or configuration change, deployment,
restart, networking change beyond attaching the test device, PMS protocol traffic beyond what the normal
read-only connector performs on its own, PS or financial traffic, package or service-plan modification,
Go-Live, and no change to PR #24.

The agent does not reconnect the Protel socket. That is the Product Owner's action.

---

## 14. Authorization boundary

**Everything above is preparation. The boundary is Stage 1.**

Execution requires a Product-Owner decision (proposed **D38**) that explicitly states each of:

1. The Protel socket is being returned to StayConnect for the test, and by whom.
2. One real Room authentication from a test device on the guest network is authorized.
3. One package acquisition of the zero-price `Free Internet Package` is authorized, creating a **permanent,
   non-reversible** Purchase row plus an Entitlement and a Session.
4. Network enforcement may be applied to the test device.
5. The negative checks in §8 are authorized.
6. Cleanup ends access but does **not** remove the Purchase, and that is accepted.
7. The decision on P1 — run against the configured 2 Gbps values, or correct the plan first under a separate
   authorization.
8. Confirmation that this is an acceptance of the guest-access chain only, and **not** a Go-Live decision.

Absent that decision, this document remains a plan and nothing in it is performed.
