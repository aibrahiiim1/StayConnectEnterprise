# AD-36 closure: post-stay sign-in, continuity, audit, settings and abuse bounds

Closes the gaps recorded in [`AD36_NEW_DEVICE_IDENTITY_REVIEW.md`](AD36_NEW_DEVICE_IDENTITY_REVIEW.md) (master 4924d509).
Mission authorised by the Product Owner. No PMS traffic, no shared host, no deployment; everything below ran in a throwaway
full-schema PostgreSQL container. Classification: CONTROLLED (guest authentication and Entitlement core, one additive
migration) - nothing else about the approved flow changed.

## What did not change

The approved flow is intact: **room number + reservation number**, bound to the original Stay and its PMS interface (found
by the device's own stay lineage, never by typed text); a device with no stay of its own also gives a last name on that stay;
the surname is **never** identity proof (the surname-alone cases still refuse); no OTP or contact-verification authority was
added; every refusal is still the same answer to the guest.

## What changed

### 1. A device cannot reset a stay's Entitlement or its device limit (`internal/poststay/convert.go`)

The review found each conversion created a new Entitlement and that only the first device could convert at all. Now:

| Situation | Before | Now |
|---|---|---|
| Stay has a live Entitlement (the post-stay one, or its own while it still runs) | second device verified, then refused (`ErrAlreadyConverted`); a stay-own live Entitlement made the insert fail on `ent_live_stay` | the device **joins** it: same window, plan, allowance and consumed usage; the plan's device limit is enforced under the entitlement lock (`ErrDeviceLimit` beyond it); no purchase, no Entitlement, no window created |
| Returning device (new session, or a retry whose reply was lost) | refused | same Entitlement; if it still has a live session on it, that session is returned (scd) - no second session, no second licence slot |
| Stay has no live Entitlement and no post-stay benefit yet | new Entitlement from the POST_STAY package | unchanged: **the Post-Stay benefit**, granted once per stay |
| The benefit was granted and has ended or run out | refused | refused (`ErrAlreadyConverted`): no device can ask for a fresh allowance |
| A live Entitlement exists but cannot be joined (not ACTIVE, or its window has passed) | insert failure | refused (`ErrEntitlementNotJoinable`); replacing it would hand out a fresh allowance |

**The Post-Stay benefit is the only place a new Entitlement can come from, and it is distinguished from a device's
convenience in code, database, audit and tests:** it is the designed zero-price POST_STAY package, granted by the Stay and
the package and never by a device, once per stay (`one_conversion_per_episode` already enforced it in the database; the
code check gives a clear answer first). `Converted.Joined` tells the two outcomes apart; the audit log records
`post_stay.benefit_granted` and `post_stay.entitlement_joined`. One-live-Entitlement-per-Stay (`ent_live_stay`) is preserved
and, for the first time, honoured by the code instead of surfacing as a constraint failure.

Found by the new concurrency test and fixed (`poststay.go`): two devices of one stay verifying at the same moment could
make the second fail in the profile guard after the first had already converted; `ensureProfile` now reads before it writes
and treats that race as "take the profile that exists".

### 2. Persistent refusal audit (`internal/poststay/audit.go`)

Every refusal - wrong or unknown answer, throttled (with the scope that stopped it), malformed body, licence refusal,
unresolved device, no grantable package, conversion refusals, capacity, enforcement - is a row in `public.audit_log`
(`post_stay.signin_refused`; the table svc_scd already holds INSERT on; no new grant). A row holds a closed reason code, the
route and number of the device's stays, the throttle scope, the source address (the table's own `ip` column), and keyed
pseudonyms (`throttle.Store.Fingerprint`, the same HMAC the throttle buckets use) of the device, the room and the
reservation as typed. It never holds the surname or the room or reservation in clear, a stay id, or any request or error
text. Rows are capped (3 per device and reason per minute, 120 per minute) with the rest counted in one
`post_stay.signin_refusals_suppressed` row, so the endpoint cannot be used to fill the table. Best effort, like
`sign_in_attempts`: a guest is treated identically if a row cannot be written. Grants and joins are written inside the
conversion transaction (ids only).

### 3. Throttle limits are settings (migration 0190, `internal/poststay/limits.go`)

Following 0179 exactly: columns on `iam_v2.site_signin_capacity_settings`, CHECK bounds, the same read function
(`signin_capacity_settings_get`, new columns appended) and the same audited writer (`signin_capacity_settings_set`: operator,
mandatory 4-500 character reason, append-only change log, `config_version`); scd reads them with the other sign-in limits
(cached, cleared by the database notification); an unreadable or out-of-range value means the default, never "off".

| Setting | Default | Range | Unit |
|---|---|---|---|
| `post_stay_device_attempts_per_minute` | 10 | 3-60 | attempts per minute |
| `post_stay_ip_attempts_per_minute` | 20 | 5-200 | attempts per minute |
| `post_stay_wrong_answers_before_lockout` | 5 | 3-20 | attempts per minute |
| `post_stay_lockout_minutes` | 15 | 1-240 | minutes (never longer than 4 h) |
| `post_stay_new_device_room_attempts_per_minute` | 10 | 3-60 | attempts per minute |
| `post_stay_appliance_attempts_per_minute` | 200 | 20-5000 | attempts per minute |

Permission and step-up are the existing ones for the sign-in limits (`auth-methods`, operator password, reason). **Hotel
Admin UI: done** - the existing Sign-in limits card is generic over the bounds the appliance returns, so the six settings
(with unit and explanation from `signinCapacityFields`) appear there with labels added in `signin-limits-card.tsx`; nothing
else remains. Migration: additive, idempotent, reversible (`.down.sql` restores the 0179 function shapes and drops the
columns), `iam_v2` only (not a platform migration), owner and grants as 0179 (`svc-signin-capacity-grants.sql` is by
signature, unchanged). Tests pin the code's bounds and defaults to the migration text and the console's list.

### 4. MAC rotation, enumeration, denial of service (`lineage.go`)

- **Rotation.** The device identity is the MAC and is free to change; the per-device and per-stay-and-device budgets
  therefore only bound an honest guest's mistakes. Devices with **no stay of their own** are additionally counted against the
  **room** and the **reservation number** they typed, shared by every identity. 25 fresh identities on 25 addresses got 10
  guesses at one room (was 125+), and one reservation sprayed across 25 rooms got 10 (tested).
- **No permanent lockout.** Every lockout ends by itself after the setting (maximum 4 h); a new device with the right answers
  signs in once it ends (tested). **The guest's own devices are not counted in the room/reservation rules** (they are
  compared with their own stays only and cannot enumerate), so a flood against a room does not lock out guests already on
  it (tested). Residual: a flood that keeps sending holds *new* devices out of that room for as long as it keeps sending.
  That is inherent to any per-target limit and bounded by the per-address and appliance limits.
- **Appliance budget.** The appliance-wide rule is now `Global`: it is charged only for attempts every other rule allowed, so
  one refused client can no longer use up post-stay sign-in for the whole hotel (the review's availability finding).
- **Uniformity.** Verify still reads candidates, compares every candidate and runs the new-device lookup (or an equal-cost
  dummy) on every path; throttled refusals return before comparing, as before, and reveal only the caller's own typed values.
  Refusal rows for an existing stay and a non-existent room have the same shape (tested). The guest-facing answer is unchanged.
- **Bounded work.** Candidates `LIMIT 5`, new-device lookup `LIMIT 2`, one throttle round trip, typed values capped at 64
  characters, body 64 KB, audit rows capped.

## Tests run (all executed here; results are the real output)

Environment: `golang:1.26.10` container, repo mounted; PostgreSQL 16 + TimescaleDB 2.16.1 built by
`scripts/lib-fullschema-testdb.sh` (Gate-P roles, production baseline, every migration above it **including 0190**, Gate-P
grants), tags `integration phase5`.

| Package / selection | Result |
|---|---|
| `./internal/poststay/` (all, 3 consecutive runs on the same database) | `ok` x3 (8.2-8.5 s); 26 tests PASS: 6 existing AD-36 cases + `sameText`, 3 window cases, 2 retained review demos, 11 new closure cases, 3 new unit tests (limits, bounds pinned to the migration) |
| `./internal/throttle/`, `./internal/authctx/` | `ok` |
| `./cmd/scd/ -run "PostStay|Settings|Capacity|Ceiling"` | `ok` (end-to-end conversion into an enforced session, retry, new returning-device/audit case, capacity, notification of changed settings) |
| `./cmd/edged/ -run "PostStay|SigninCapacity"` | `ok` (post-stay API, settings round trip, new bounds-pinning test) |
| `go build ./...`, `go vet` (tags integration phase5) on poststay, scd, edged | clean |
| Migration 0190 on the test database: up, up again (idempotent), down, up | all succeeded |

New cases: further devices join one entitlement and the plan's limit refuses the next; returning device keeps the same
entitlement and its spent usage; the stay's own live entitlement is continued, not replaced; a spent benefit cannot be
re-granted by any device; five concurrent devices end on one entitlement within the limit; refusals persisted without guest
data and with identical shape; audit bounded and summarised; rotating identities capped, own device unaffected, lockout
finite; reservation spraying capped; limits come from settings (3 answers, 2-minute lockout); settings bounded, defaulted and
audited in the change log; the code's bounds equal the migration's CHECKs and the console's list.

Test changes: two review demonstrations that pinned the *old* behaviour (a rotating identity multiplies guesses; a second
device verifies but cannot convert) were removed and replaced by the cases above. The shared test request helper now derives
the source address from the device (the per-address bucket is real database state; sharing one address made the suite
fail on its second run within 15 minutes).

## Not verified

- No live appliance, no deployment, no real PMS (Protel/Mews/Opera) data: the reservation/room/surname forms a real feed
  stores, sharer and multi-room reservations, and the field-acceptance list at the end of the review are still open.
- The `cmd/portald` post-stay tests and the full `cmd/scd` / `cmd/edged` suites were not run (targeted selection only).
- Gate-P grants were exercised through the standard fresh build only; no separate privilege probe was written for the new
  columns (they ride the existing function grants).
- In scd's test harness, a returning device after its session ended gets a new session row on the same entitlement, but the
  harness's enforcement stand-in does not promote it (it registers one accounting origin per device minor), so the reply
  there stays UNAVAILABLE; the test asserts the database state. Real netd closes the origin when the session ends; not
  exercised on a live appliance.
- Concurrency was tested with five goroutines, not under load.

## Remaining decision for the Product Owner

**Do you want any authority stronger than "room + reservation (+ last name from a device with no stay)" for a device that was
never on the stay?** After this change the reservation number is the only real secret (room and surname are guessable or
printed), and guessing it is limited to 10 tries per room per minute with a 15-minute lockout, all settings. Making it
genuinely stronger needs a new verification authority that the approved flow rules out and the stay record cannot supply
(a code to a PMS-held verified e-mail/phone, or a front-desk approval step). Nothing was invented; if the answer is "accept
the current strength", AD-36 is complete as it stands.

Two non-blocking confirmations, both implemented as the conservative reading and easy to change:
(a) the Post-Stay benefit is a fresh allowance by package design and is **not** reduced by what the stay consumed before
check-out; (b) a device that has history on one eligible stay still cannot reach a *different* stay by the new-device route
(unchanged from the review; it affects only a guest who re-books within the window and reuses a device).
