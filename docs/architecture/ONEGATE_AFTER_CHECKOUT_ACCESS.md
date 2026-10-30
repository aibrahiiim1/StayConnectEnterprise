# OneGate After Check-out Access: one feature, one policy, one grant

Status: current contract (Product Owner decision, 2026-10-30; migration 0193). It replaces the separate **Checkout Grace** and
**Post-Stay** behaviours and supersedes the parts of [`ONEGATE_POST_STAY_SIGN_IN.md`](ONEGATE_POST_STAY_SIGN_IN.md) and
[`../reviews/AD36_POST_STAY_CLOSURE.md`](../reviews/AD36_POST_STAY_CLOSURE.md) that conflict with it (the "new device with a
surname" route, the separate Post-Stay benefit, the settings-driven sign-in window). Everything else in those documents (room +
reservation, uniform refusals, throttling, audit without guest data, episode binding) stands and is restated below.

## 1. The contract

**One feature.** In the Hotel module, "Checkout Grace Period" and "Post-Stay access" are one thing, **After Check-out Access**,
configured in one place (`/edge/v1/after-checkout-access`, section 6): switch, duration after the verified checkout (hours),
download and upload speed, one **shared** data quota, device allowance and device policy. The policy is the site's existing grace
configuration (`iam_v2.site_checkout_grace_config`), published through the existing audited boundary
(`iam_v2.publish_checkout_grace_policy`) with its immutable history (`iam_v2.checkout_grace_policy_publications`). There is no
second duration, package or default.

**One grant, at the checkout.** At a verified PMS checkout, if the Stay held an active valid Entitlement at the boundary, the
checkout conversion (`internal/checkout`) writes exactly **one** After Check-out Entitlement (`end_mode = GRACE_AFTER_CHECKOUT`)
from a **snapshot** of the policy (`entitlements.policy_snapshot`, pinned package revision), in the same transaction that
terminates the pre-checkout entitlements. It starts at the verified effective checkout timestamp (`window_ends_at = boundary +
duration`), never at a reconnection. One Entitlement per Stay episode (database: the one-per-episode audit row, `ent_live_stay`,
one purchase per episode). Changing the policy applies to **future** checkouts only; an issued entitlement keeps its snapshot.

**Eligibility: previously authorised devices only.** A device is carried over only if it was authenticated and authorised against
the **same Stay** (same tenant, site, PMS interface, stay and lifecycle episode) and its authorisation interval contains the
checkout boundary: a device online at the boundary (its live session is moved to the new Entitlement without a logout), and a
device authorised earlier on that stay that is offline at the boundary. Not eligible: a new device; a device merely on the Wi-Fi or
holding a DHCP lease without a stay authorisation; a device revoked, released or removed before the boundary (or during the window);
a device authorised only on another stay (including an earlier stay in the same room); a device with a rotated MAC and no
authorisation lineage. A MAC is never treated as a guest identity. Room numbers are interface-scoped and may be reused or shared:
nothing is ever found from a room number; the candidates are the stays the **device** holds a place on.

**One allowance.** All eligible devices share the one Entitlement: one window, one total quota, one speed, one device list. Usage
of every device is summed on the Entitlement; the quota and the window end access for all devices **exactly once** (the
accounting expiry writer, `iam_v2.p6_expire_entitlement`, is idempotent). Reconnecting, a new session, a new address or signing in
repeatedly never resets the duration, the quota or the device list.

**Reconnection.** During the window an eligible returning device signs in with the **room number and the reservation number
only**: no surname, first name, OTP or other factor. The sign-in is `POST /auth/post-stay {room, reservation}` on the portal. It
**grants nothing**: it verifies, then lets the device back onto the stay's Entitlement. Correct credentials never authorise a
device that lacks the lineage.

**No financial posting after checkout.** The Stay is `CHECKED_OUT` with `posting_allowed = false`; the After Check-out
Entitlement is zero-price (`amount_minor = 0`, settlement `NOT_REQUIRED`). Local-first operation and licensing enforcement are
unchanged: the sign-in answers to the same licence decision as every guest method (`pmsLicenseRefusal`), uses the appliance's last
good roster, and reserves a licensed slot like any session.

**Switch.** `after_checkout_enabled` (default `true`: existing sites keep today's behaviour, a grant at every checkout). Off means
**nothing is granted** (no fallback either: "off" is a decision, the Emergency fallback answers a broken configuration). Turning it
off or on is versioned and audited like a policy change and affects later checkouts only.

## 2. Data model

| Object | Role |
|---|---|
| `iam_v2.site_checkout_grace_config` | The one policy row per site: `grace_duration_seconds`, `grace_down_kbps`, `grace_up_kbps`, `grace_data_quota_bytes`, `grace_device_limit`, `grace_device_limit_policy` (`REJECT_NEW_DEVICE`), `config_version`, **`after_checkout_enabled`** (0193). Written only by the definers `publish_checkout_grace_config` and `set_after_checkout_enabled`. |
| `iam_v2.checkout_grace_policy_publications` | Immutable version history (actor, reason code, snapshot). A switch change is a row with `policy_snapshot.change = AFTER_CHECKOUT_ENABLED`. |
| `iam_v2.entitlements` (`end_mode = GRACE_AFTER_CHECKOUT`) | The one After Check-out Entitlement per stay episode; `policy_snapshot` = the terms it was granted with. |
| `iam_v2.entitlement_devices` / `entitlement_device_authorizations` | The device list. Rows carried over at checkout have `grandfathered = true`. |
| `iam_v2.checkout_grace_audit` | One row per checkout decision (grant / `AFTER_CHECKOUT_DISABLED` / `NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT` / emergency fallback), with the boundary provenance. |
| `iam_v2.post_stay_profiles` | The per-episode identity used only to mint the one-time sign-in context. `valid_until` = the Entitlement's `window_ends_at` (created on the first successful check). Unchanged table; no PIN. |

Database rules added by 0193:

* `after_checkout_device_guard` (trigger on `entitlement_devices`): on a `GRACE_AFTER_CHECKOUT` entitlement a new row must be
  `grandfathered` (the checkout conversion); a device that left (staff release, guest self-service, transfer) cannot come back; only
  a system suspension is reversible. `authorize_entitlement_device` therefore cannot add a new device by any route.
* `post_stay_conversion_retired` (trigger on `purchases`): no new purchase may carry `POST_STAY_CONVERSION`.
* `after_checkout_switch_guard` + the extended `p3_grace_config_version_guard`: the switch changes only inside the open
  `grace_publication` operation and moves `config_version` by exactly one.

## 3. State machine

```
IN_HOUSE --(verified GO event)--> CHECKED_OUT
   checkout conversion (one transaction, Stay-first lock order):
     switch off ........................ no grant (reason AFTER_CHECKOUT_DISABLED)
     no ACTIVE valid entitlement ....... no grant (NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT)
     several ACTIVE ..................... no grant, manual review
     otherwise ......................... ONE After Check-out entitlement (published policy; Emergency fallback only if the
                                         published policy is invalid), devices authorised at the boundary carried over,
                                         their live sessions rebound without a logout, every other pre-checkout entitlement
                                         terminated, devices not carried over revoked at the boundary
After Check-out entitlement:  ACTIVE --(window end | shared quota reached | staff termination)--> TERMINATED  (once)
Reconnection (device D, room+reservation):
   candidate stays = stays whose LIVE After Check-out entitlement holds an open place for D
   compare room + reservation (constant time) -> profile of the episode -> one-time context -> join:
     entitlement live? (ACTIVE, window not over)   else  refuse
     D holds an open place on it?                  else  refuse (lineage re-checked at the join)
     -> D's session on the same entitlement (existing live session returned; no second one)
```

The Stay stays `CHECKED_OUT`; `POST_STAY_ACTIVE` is no longer entered (it remains a valid historical status).

## 4. What was retired, and how history is preserved

Nothing was deleted, rewritten or invented. Obsolete operational paths were closed:

| Retired | How | History kept |
|---|---|---|
| Separate Post-Stay benefit (zero-price POST_STAY package converted into a new Entitlement; `one_conversion_per_episode`) | `internal/poststay` no longer grants (join only); `GrantableOffers`, the package choice, `ErrAlreadyConverted`, `post_stay.benefit_granted` removed; trigger `post_stay_conversion_retired` refuses new purchases of that kind | Existing `POST_STAY_CONVERSION` purchases/entitlements/profiles untouched; an entitlement already issued keeps working until it ends and is never extended (candidate query and join honour it) |
| "New device + surname" sign-in route | removed from `Verify`; `last_name` is no longer a field of scd or portal requests (strict decoders refuse it); portal form field and `poststay.lasthint` string removed in all six languages | `stay_guests` untouched |
| `post_stay_signin_days` setting (0179, 1-30 days) | no longer read; removed from the sign-in-limits API/bounds; changing it is refused; the window is the Entitlement's own duration | Column, function column and change log kept (history) |
| Package choice in the portal / `package_revision_id` in the convert call | removed; the portal verifies and reconnects in one request | none needed |
| Independent Checkout Grace editor | `/checkout-grace` PUT and `/grace` PUT remain as **deprecated aliases of the same record** (`Deprecation: true`, `Link: successor-version`); the console moves to `/after-checkout-access` | same record, same history |
| `POST_STAY` package type | stays valid in the schema for history; no code reads or offers it | rows untouched |

Existing settings map as follows: grace policy rows are the policy (no change); `after_checkout_enabled` defaults to `true`;
`post_stay_signin_days` is retired as above; the six throttle settings (0190) are unchanged and still apply.

## 5. Settings bounds (operator publication)

The database keeps its wider structural CHECKs (so a policy published earlier stays valid); a **new** publication is held to:

| Field | Unit | Min | Max | Default |
|---|---|---|---|---|
| `duration_hours` | whole hours | 1 | 72 | 12 |
| `down_kbps` | kbps | 64 | 1,000,000 | 5000 |
| `up_kbps` | kbps | 64 | 1,000,000 | 2000 |
| `data_quota_bytes` (shared by all devices) | bytes | 10 MiB | 100 GiB | 1 GiB |
| `device_limit` | devices | 1 | 20 | 5 |
| `device_policy` | - | `REJECT_NEW_DEVICE` only | | |

`device_limit` is the plan's concurrent-device ceiling; the devices carried over at checkout are never cut to it (existing grace
rule, `ExistingDevicesOverLimit`), and no device is ever added to it after checkout.

## 6. API for the console (edged, `/edge/v1`)

Permission key `after-checkout-access` (module `hospitality`): `site_admin` write; desk/read roles read. Publication needs the
operator's password (step-up), a bounded reason code (`^[A-Z][A-Z0-9_]{0,63}$`) and the version last read.

### `GET /after-checkout-access`

```json
{
  "enabled": true,
  "configured": true,
  "config_version": 3,
  "policy": {"duration_hours": 12, "duration_seconds": 43200, "down_kbps": 5000, "up_kbps": 2000,
             "data_quota_bytes": 1000000000, "device_limit": 5, "device_policy": "REJECT_NEW_DEVICE"},
  "effective": {"source": "PUBLISHED", "policy": {"...": "same shape as policy"}},
  "bounds": {"duration_hours": {"min": 1, "max": 72, "default": 12, "unit": "hours"},
             "down_kbps": {"min": 64, "max": 1000000, "default": 5000, "unit": "kbps"},
             "up_kbps": {"min": 64, "max": 1000000, "default": 2000, "unit": "kbps"},
             "data_quota_bytes": {"min": 10485760, "max": 107374182400, "default": 1073741824, "unit": "bytes"},
             "device_limit": {"min": 1, "max": 20, "default": 5, "unit": "devices"}},
  "supported_device_policies": ["REJECT_NEW_DEVICE"],
  "emergency_history": {"count": 0, "last_at": "..."},
  "eligibility": "text for the page", "sign_in": "text for the page"
}
```

`policy` is `null` and `configured` false until a policy is published. `effective.source` is `PUBLISHED`, `EMERGENCY_FALLBACK`
(enabled, nothing valid published: 60 min, 5000/2000 kbps, 500 MB, as today) or `DISABLED` (no `policy` member).
`duration_hours` is fractional only for a policy published before the whole-hour rule.

### `PUT /after-checkout-access`

```json
{"enabled": true, "duration_hours": 12, "down_kbps": 5000, "up_kbps": 2000, "data_quota_bytes": 1000000000,
 "device_limit": 5, "device_policy": "REJECT_NEW_DEVICE",
 "expected_config_version": 3, "reason_code": "SEASON_CHANGE", "password": "<operator password>"}
```

* `expected_config_version` and `reason_code` required; `password` is the step-up.
* Policy fields are published **whole** (all of `duration_hours, down_kbps, up_kbps, data_quota_bytes, device_limit`) or not at all;
  `device_policy` optional. `enabled` optional. At least one of the switch or the policy must be present.
* One request that changes both produces two versions (policy first, then the switch); `200 {"config_version": N}` is the last.
* Errors (`{"error": code, "message": text}`): `400` `bad_request`, `expected_version_required`, `reason_required`,
  `reason_invalid`, `nothing_to_change`, `validation` (bounds, partial policy, unsupported device policy), `policy_unsupported`,
  `package_invalid`; `401/403` step-up refused (`reauth`) or `actor_invalid`; `409` `version_conflict` (reload and retry);
  `503` `phase2_disabled`.
* Applies to **future checkouts only**.

### `GET /after-checkout-access/history`

`{"data": [{"config_version": 4, "published_at": "...", "actor": "Name", "reason_code": "SEASON_CHANGE", "policy": {...snapshot...},
"derived_package_revision_id": "..."}], "available": true}`, newest first. A switch change has
`policy.change = "AFTER_CHECKOUT_ENABLED"` and `policy.after_checkout_enabled`.

### Retired in the sign-in-limits API

`GET/PUT /signin-capacity-settings` no longer lists or accepts `post_stay_signin_days`. The six `post_stay_*` throttle settings
remain (they bound guessing, not the window).

### Operator profile list

`/post-stay-profiles` (list, get, revoke) is unchanged: it shows who reconnected and lets staff end that stay's reconnection
(terminal for the episode). It does not end the Entitlement; staff end a device with the existing device-release action.

## 7. Audit events

| Event | Where | Content |
|---|---|---|
| `after_checkout_access.updated` | `public.audit_log`, edged | operator (session), `config_version`, `reason_code`, `changed` {policy terms, `enabled`} |
| publication rows | `checkout_grace_policy_publications` | actor, reason code, exact terms in force, derived package revision |
| checkout decision | `checkout_grace_audit` | grant / no-grant reason code, boundary provenance, config version |
| `post_stay.signin_refused` | `public.audit_log`, scd | closed reason code (`NOT_AUTHENTICABLE`, `THROTTLED`, `NO_AUTHORIZATION_LINEAGE`, `ENTITLEMENT_NOT_JOINABLE`, ...), route, candidate count, throttle scope, source address, keyed pseudonyms; never the room, reservation or any name |
| `post_stay.signin_refusals_suppressed` | same | one summary row per minute under flood |
| `post_stay.entitlement_joined` | same | stay id and entitlement id only |

## 8. Tests

`internal/checkout/after_checkout_access_integration_test.go` runs the real converter and the real sign-in: the worked example
(checkout 12:00, 8 h, 5/2 Mbps, shared 1 GB; iPhone online + Samsung offline eligible; Samsung back at 15:00; new iPad refused with
correct credentials; 600 MB + 400 MB exhaust the shared quota, access ends for both exactly once; expiry at the window end
regardless of bytes left), online / offline-previously-authorised / new / revoked / MAC-change / LAN-only devices, removed device
cannot rejoin, room reuse, the same room and reservation on two PMS interfaces, shared occupancy, concurrent reconnections and
concurrent checkouts, repeated checkouts, PMS-outage (late) processing, settings (switch) changes affecting later checkouts only,
the retired purchase trigger. `internal/poststay/after_checkout_signin_integration_test.go` pins the sign-in properties (uniform
refusals, per-stay-and-device lockout, rotation and spraying caps, audit without guest data, bounded settings, retired-conversion
entitlements honoured but not extended). `cmd/scd/phase5_poststay_integration_test.go` runs verify -> rejoin -> enforced session.
`cmd/edged/after_checkout_access_integration_test.go` covers the API. `cmd/portald` tests pin the request bodies (room and
reservation only; surname, context and package fields refused).
