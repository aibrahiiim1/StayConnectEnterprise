# OneGate post-stay sign-in: room number and reservation number

Status: current contract (audit decision AD-36, Product-Owner replacement, migration 0165). It replaces the Post-Stay PIN
contract of `StayConnect-IAM-Phase5-Plan.md` for everything that concerns how a guest proves themselves. The profile,
auth-context and conversion rules of that plan (episode binding, zero-price packages, one-time context, enforced session)
are unchanged.

## What the guest does

After checkout a guest opens the portal, chooses **Post-stay**, and types:

- the **room number** and the **reservation number** of the stay they left;
- from a device that was **never on that stay**, also the **last name** on the reservation.

No PIN is shown at checkout, issued, reset or typed. Nothing is given to the guest to lose.

## How the appliance decides

1. The device comes from the connection (kernel neighbour table), never from the request.
2. The **candidate stays** are the stays this device was authorised on that are `CHECKED_OUT` or `POST_STAY_ACTIVE`
   (at most five). The typed room and reservation are compared with **those stays' own records** in constant time
   (both sides are reduced to fixed-length digests first; an empty stored or typed value never matches). Every candidate
   is always compared; nothing short-circuits.
3. **No candidate (a new device):** the stay is looked up by room, reservation **and last name together**
   (`stays.normalized_room_number`, `stays.external_reservation_id`, `stay_guests.last_name_norm`), among `CHECKED_OUT` /
   `POST_STAY_ACTIVE` stays of this site, and must match **exactly one** stay. Typing another guest's room and
   reservation from a device that was not on that stay finds nothing without their last name as well, and the throttle
   below limits how often it can be tried.
4. On a match the **post-stay profile of that stay episode** is created if it does not exist
   (`issued_via = ROOM_RESERVATION`, no PIN, valid for 24 hours) and must be authenticable
   (`iam_v2.p5_post_stay_authenticable`: active, unexpired, same episode, stay checked out). A one-time auth context bound
   to that profile and that device is issued; the conversion to a zero-price package is as before.
5. A reinstatement orphans the episode's profile in the database (UNIQUE `(origin_stay_id, origin_lifecycle_version)`), so
   nothing carries over to the room's next occupant. A revoked profile never authenticates again.

## Refusals reveal nothing

A wrong room, a wrong reservation, no stay, a revoked or expired profile, a moved-on episode, a locked-out device and a
malformed request all give **the same answer** (HTTP 200, one fixed message, the same response-time budget). The reason
goes only to the log. The request body is decoded strictly: a stay, profile, interface or device in the body is refused,
never forwarded.

## Lockout

The durable throttle (`post_stay_pin` method, the name is historical) is charged **before** any comparison. Every number
below is the **default of a per-site setting** (Hotel Admin, sign-in limits; migration 0190; bounded, audited, with a
mandatory reason), not a constant:

- per device: 10 attempts a minute (3-60), then a lockout;
- per source address: 20 (5-200), then a lockout;
- per **stay identity and device** (the profile of the episode plus the device): 5 (3-20), then a lockout;
- a device with no candidate stay: 5 per device, and **also per room and per reservation number typed** (10, 3-60), shared
  by every device identity, then a lockout;
- appliance-wide: 200 a minute (20-5000); it is charged only for attempts every other rule allowed, so a refused client
  cannot use it up for everybody;
- the lockout is 15 minutes (1-240). It ends by itself; there is no permanent lockout and nothing for staff to release.

Because the identity bucket includes the device, one device's wrong guesses do not lock another device out of the same
stay. A client that changes its device identity (MAC) on every attempt gets a fresh device budget each time, so for devices
with no stay of their own the **room and the reservation number they must guess** are counted across identities: guessing
at one room, or spraying one reservation number across rooms, stops at the cap whatever identities are used. Devices that
already have a stay are compared with their own stays only, cannot enumerate anything, and are not counted there, so a
flood against a room does not lock out the guests already on it. The residual: a sustained flood naming one room holds
**new** devices out of that room for the lockout, only while it keeps sending. An unreachable throttle store denies the attempt.

## One Entitlement, never reset by a device

A Stay has one live Entitlement. A device that signs in after check-out never gets one of its own:

- if the Stay has a live Entitlement (the post-stay one a first device converted into; or the stay's own while it still
  runs), the device **joins** it: same window, same plan, same allowance and whatever it has already spent; the plan's
  device limit decides how many devices (a further device over the limit is refused);
- only when the Stay has none does the zero-price **POST_STAY package** grant a new one. This is the one **Post-Stay
  benefit**: a designed allowance of the package, granted once per stay (database index `one_conversion_per_episode`), by
  the stay and not by a device. It is recorded as `post_stay.benefit_granted`; a join is `post_stay.entitlement_joined`;
- once that benefit has ended or run out, no device can obtain another (`ErrAlreadyConverted`).

A returning device (a new session after the old one ended, or a retry whose answer was lost) gets the same Entitlement and,
if it still has a live session on it, that session.

## Audit

Every refusal is a row in the audit log (`post_stay.signin_refused`): a closed reason code, the route (known device / new
device) and how many stays the device had, the throttle scope that stopped it, the source address, and **keyed pseudonyms**
of the device, the room and the reservation as typed. Never the last name, or the room or reservation in clear. Rows are
capped per device and minute and the rest are summarised in one `post_stay.signin_refusals_suppressed` row. A guest is
refused or admitted the same way if a row cannot be written.

## "New or risky" devices

A device with no history on any eligible stay is **new** and must also give the last name from the stay record. Risk
signals beyond that are not used today; the extra factor is evaluated in one place (`internal/poststay/lineage.go`,
`Verify`) so another signal can be added without touching the portal. An emailed or texted code is not used: the stay
record does not reliably hold a verified address, and sending to an address typed by the person asking would defeat the
point.

## What was removed, and what was kept

Removed: `POST /poststay/issue` (portal) and `/v1/phase5/poststay/issue` (scd), the operator **Reset PIN** route and
screen, the PIN generator, hasher and verifier, the "post-stay PIN" wording in every portal language, and the database's
ability to set or change a PIN (`p5_post_stay_profile_guard` refuses it).

Kept as **historical evidence**, never read for a decision: `post_stay_profiles.pin_hash`, `pin_generation`,
`pin_set_at`, `pin_revealed_at`, `issued_via` (`GUEST_AUTHENTICATED_SESSION`, `OPERATOR_RESET`) and
`issued_by_operator` on every profile issued before 0165. Nothing was dropped, rewritten or deleted. `pin_hash` became
nullable so a profile created by the new route can exist. The audit-log rows `poststay.pin_reset` remain readable.

Retained operator action: **End access** (revoke), password step-up, reason, audit, terminal for the stay episode.

## Tests

`internal/poststay/post_stay_room_reservation_integration_test.go` (tags `integration phase5`, `PHASE3_TEST_DSN`):
lineage sign-in, uniform refusal, no cross-stay guessing, new device needs the last name, per-stay-and-device lockout,
revoked and orphaned profiles. `cmd/scd/phase5_poststay_integration_test.go` runs the whole path to an enforced session.
`cmd/portald/phase5_poststay_*_test.go` pin the portal bodies and the uniform answers. `cmd/edged/phase5_poststay_api_integration_test.go`
covers revoke and the absence of a reset route.
