# AD-36 review: new-device identity for post-stay sign-in

Scope: engineering review of the CURRENT implementation against the Product Owner's final AD-36 decision (room number
plus reservation number, bound to the original Stay and PMS Interface; extra verification for new or risky devices; a
surname alone never strong proof; quota/Entitlement continuity preserved; no change to approved authentication
semantics). Nothing here changes semantics. Master 2985a88e. No PMS traffic, no shared host contacted.

Legend: **D** = demonstrated by a test I ran (isolated full-schema Postgres 16 in a container, `-tags "integration
phase5"`); **R** = read-only conclusion from code.

## What was run

- Existing `internal/poststay` integration suite: 10 cases, all PASS (lineage sign-in, uniform refusal, cross-stay
  guessing, new device needs the name, lockout per stay and device, revoked/orphaned, window, next occupant, other PMS
  interface).
- Four new demonstration cases in `data-plane/internal/poststay/ad36_review_demo_integration_test.go` (all PASS, they
  pin current behaviour and assert no new semantics). Not run: `cmd/scd`, `cmd/portald`, `cmd/edged` suites.

## 1. What proves a device, and the bounds

| Path | Factors | Code |
|---|---|---|
| Known device (authorised on a CHECKED_OUT/POST_STAY_ACTIVE stay inside the window, max 5 candidates) | room + reservation, compared constant-time with the candidate stays' own records. **No surname, no other factor.** | lineage.go:46-59, 141-147 |
| New device (zero candidates) | room + reservation + last name of ANY guest on the stay, exactly one stay must match | lineage.go:148-188 |

Bounds (Verify charges the throttle before comparing; store failure denies; fixed 1-minute windows, authsec.go:21):

| Scope | Limit | Code |
|---|---|---|
| per device (MAC-derived id) | 10 / min, then 15 min block | lineage.go:94 |
| per IP | 20 / min, then 15 min block | lineage.go:98 |
| per (stay, episode, device); no candidate: per device | 5 / min, then 15 min block | lineage.go:101-107 |
| per appliance (endpoint `post-stay-pin`) | 200 / min, no block | lineage.go:95 |
| per room, per reservation number | **none** | (absent) |

- Surname alone, surname + room, surname + reservation, surname + wrong reservation never find a stay: **D**
  (`SurnameAloneNeverSucceeds`). The surname is never sufficient (lineage.go:151 requires all three non-empty). A wrong
  surname with right room and reservation also fails (existing test). **D/R**
- Enumeration: there is no per-room or per-reservation counter. The device identity is the MAC the kernel reports
  (phase3_auth.go:203-233, a `devices` row is upserted for any MAC on a mapped network), so a client that changes MAC
  gets a fresh 5-attempt budget: 6 identities on 6 addresses made 30 evaluated guesses, 12 throttled (**D**,
  `BudgetIsPerDeviceAndIP`); the per-IP bucket caps one address at 20 evaluated regardless of identities (**D**).
  Effective ceiling for a determined attacker is therefore the appliance-wide 200/min (R), about 288,000 guesses/day,
  and it needs room AND surname right as well as the reservation (a new device cannot enumerate reservation numbers
  without also knowing a surname on that stay). The same 200/min bucket is charged by refused attempts, so one noisy
  client can exhaust post-stay sign-in for every guest in the hotel until the minute rolls (R, availability limit).
- Tuning is not a setting: the limits above are code constants, unlike `post_stay_signin_days` (OPERATIONAL_SETTINGS
  policy, CLAUDE.md 0C). R.

## 2. Original Stay / PMS Interface binding

| Case | Result | Evidence |
|---|---|---|
| Known device | bound by `stay_id`, never by typed text | lineage.go:55-56 (D, existing) |
| New device, same room+reservation(+name) on two PMS interfaces | **refused for both** (LIMIT 2, needs exactly 1); fail-closed, but the query has no `pms_interface_id` predicate, so the interface binding is by uniqueness, not by condition | lineage.go:152-161, 185; D (existing `AnotherPMSInterfaceIsNotTheSameStay`) |
| Later occupant of the same room | a different Stay and reservation; reaches nothing | D (existing) |
| Checked-out stay inside / outside window | `effective_checkout_at > now() - window` in candidate, lookup and profile insert; NULL checkout never matches | lineage.go:57,156; poststay.go:317; D (existing) |
| Reinstated stay | status IN_HOUSE leaves the candidate set; profile of the old episode is orphaned (version join + `p5_post_stay_authenticable`) | poststay.go:322-325, migration 0027:258; D (existing) |

Not enforced (R): post-stay never consults the guest network's PMS routing policy (D74 NONE/DEDICATED/SHARED_STRICT)
nor whether the stay's interface is still ACTIVE; any site stay is reachable from any guest network. A device removed
from a stay by an operator keeps its `entitlement_device_authorizations` history and so remains a candidate
(lineage.go:49-54). I did not test either.

## 3. "Extra verification for new or risky devices"

Today: one extra factor, the last name, applied only when the device has NO candidate stay. There is no risk signal
(doc ONEGATE_POST_STAY_SIGN_IN.md, "New or risky" says so). Where it is weaker than the decision states:

1. The surname is a low-entropy, guessable, often printed-on-the-door fact, and ANY guest's surname on the stay counts
   (lineage.go:158-160); the decision calls for "extra verification", which a surname cannot be said to provide against
   someone who knows the room (the room number is also low-entropy). Real strength rests on the reservation number alone.
2. A device with any other candidate stay is never "new": it is compared only with its own candidates and has no
   new-device route, so it cannot sign in to a different stay even with all three facts (**D**,
   `DeviceWithOtherHistoryHasNoNewDeviceRoute`; the same answers succeed from a history-free device). This is both a
   usability defect for returning guests and an inconsistency (a device with history is treated as safer yet cannot
   reach another stay). Semantics question, not fixed.
3. Known devices (including MAC-randomised re-joins that happen to match history, or a device that merely shared a
   network with the stay) need only room + reservation: no step-up, no check of MAC change, age of the lineage or
   number of recent failures.
4. "Risky" is not defined or implemented (no velocity, no mismatch history, no cross-device sharing signal).
5. No second channel (code to email/SMS) by design (documented).

## 4. Quota / Entitlement continuity

- Conversion does NOT continue the stay's original Entitlement. `ConvertTx` creates a new purchase and a new
  Entitlement from the POST_STAY package revision, with its own window, plan and device limit (convert.go:125-155).
  Quota, speed, window and device slots of the original stay are neither carried nor reconciled. R, and observed in the
  demo (the first conversion produced a new entitlement id). This is the largest gap against "quota / Entitlement
  continuity must be preserved".
- One conversion per episode (convert.go:106-116). **D** (`SecondDeviceVerifiesButCannotConvert`): once the first device
  converts (stay becomes POST_STAY_ACTIVE), a second NEW device still passes Verify and is issued an Auth Context, then
  Convert returns `ErrAlreadyConverted` and the device gets no binding and no access. The guest sees the uniform
  "unavailable" (phase5_poststay.go:192-195). So an AD-36 "new device" can join only if it is the first device to
  convert. Likely unintended for a multi-device guest; not fixed (it is a product-semantics choice).
- Device-slot count: `authorize_entitlement_device` enforces the NEW entitlement's plan limit at conversion; the licensed
  concurrent-guest slot is reserved in the same transaction (phase5_poststay.go:196). max_devices of the original plan is not consulted.
- Replacement occupant: separate Stay, separate lineage and profile; no quota or device slot is shared with the previous
  occupant (D, existing). What happens to the previous guest's POST_STAY entitlement when the room is re-let was not tested.

## 5. Audit and evidence

- Refusals: no persistent per-attempt record. Reason goes to `slog.Info("phase5 post-stay: unavailable")` only
  (phase5_poststay.go:65-68). `sign_in_attempts` (the operator-visible table of migrations 0066-0068) is NOT written by
  the post-stay verify path (grep: only phase3 auth, portald, edged readers). So operators cannot see post-stay guessing
  in Hotel Admin. Throttle buckets keep hashed counters only (R).
- Success: profile row (`issued_via=ROOM_RESERVATION`, created time), auth_context (device, network), purchase
  (`POST_STAY_CONVERSION`), entitlement, session labelled POST_STAY, and `slog` line with stay/entitlement/session ids
  (phase5_poststay.go:217). No row records which factor set (with or without surname) satisfied the check, nor the
  source IP of the verification.
- Revoke is audited and requires reason; unchanged.

## 6. Demonstrated defects vs design limitations

No demonstrated defect of the "missing binding condition / off-by-one" kind was found, so no code was changed. Every
cross-stay, cross-interface, window and reinstatement case behaved fail-closed in the tests run. Items 3.2, 4 (new
entitlement, one conversion per episode) and the audit gap 5 behave as coded; whether they are defects is a PO
semantics question. No fix was attempted for them because each would change semantics.

## Real-PMS field acceptance (NOT performed, NOT authorised)

Nothing below was done; a live-PMS test needs separate PO authorisation naming the environment. A field test with a real
feed must check: (a) the PMS actually supplies the reservation number and room in the form the mirror stores (case,
padding, per-guest vs per-reservation id; Protel vs Mews/Opera) and sharer/multi-room reservations (same reservation,
several stays: new-device lookup needs room too); (b) every accompanying guest's surname is present in `stay_guests`
(first-name/surname swap, double-barrel, diacritics); (c) check-out time source (`effective_checkout_at`) for early/late
checkout and the 7-day window edge in the hotel's timezone; (d) room moves after the stay (normalized room at checkout);
(e) reinstatement and re-let of the room with a live feed (old profile orphaned, new occupant unaffected, old
POST_STAY entitlement ended); (f) two real PMS interfaces with overlapping reservation numbers; (g) MAC-randomising
phones: lineage reuse, rejoin, and the cost of the 5-attempt lockout on honest guests; (h) feed down during sign-in
(local-first mirror trust rules); (i) throttle behaviour under real DHCP churn and guest NAT/shared-IP networks (per-IP 20,
appliance 200/min); (j) that Hotel Admin shows what the operator needs to investigate a complaint.
