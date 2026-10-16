# OneGate — Client device management (contract)

Status: **authoritative contract** for device limits, client device replacement and staff device actions (branch
`delivery/email-accounts-self-service`, PR #216). Product behaviour from the Product Owner's mission of 2026-10-15
("Client Device Management, reliable captive portal re-authentication"); migration 0118 explicitly approved for
this scope. Where code and this document disagree, the code is wrong.

## 1. The model

* A package's service plan says how many devices may use it: `service_plan_revisions.max_concurrent_devices`
  (1, 3, 4 …). It is the package's, not the credential's, and it is never changed by anything in this contract.
* Every device that has used a package **holds a place** on it: an open interval in
  `iam_v2.entitlement_device_authorizations` and `entitlement_devices.status = 'AUTHORIZED'`. A device beyond the
  limit is refused by the database itself (`authorize_entitlement_device`, MAX_DEVICES_REACHED).
* All devices on one package share **one** entitlement: its window, data quota and online time. Adding, removing
  or replacing a device never creates, duplicates, extends or resets an entitlement.
* **Signing out does not free a place.** Otherwise "sign out, pass the password on" would make the limit
  meaningless. A place moves only by one of the explicit, limited, audited acts below.

## 2. Ways a place moves (migration 0118)

| Who | Act | Function (SECURITY DEFINER) | Proof / limits |
|---|---|---|---|
| Client, new device at the limit | **Replace** one of their devices | `client_device_action(…,'REPLACE',…)` (scd) | An unexpired auth context issued to *this* device for the package's subject (the sign-in it just completed). The freed place is given to this device **in the same transaction**. |
| Client, online device | **Release** another of their devices | `client_device_action(…,'RELEASE',…)` (scd) | The requesting device has a live session on the package. |
| Staff | **Release place** of a device on one package | `staff_release_device_slot` (edged) | Operator from the session, reason 4–500 chars. |
| Staff | **Require fresh sign-in** | `staff_require_device_signin` (edged) | Revokes the device's remembered sign-in (`principal_device_credentials`); reason required. |

Common rules:

* The package is **derived from proof**, never named by the caller. A device id that is not the client's matches
  nothing and answers like an id that never existed.
* The target must be **offline**. scd/edged first take it off the network by the operator-disconnect path
  (session ended `DEVICE_REPLACED` / `DEVICE_RELEASED` / `ADMIN` through the controlled session-end writer
  `end_session_enforcement` — which can also end a session still `PENDING_ENFORCEMENT` — then nft deny and shaping
  class removed), then call the
  function. A session that came back in between makes the function refuse (`REFUSED_ONLINE`).
* **Daily limit** (clients only): at most `client_device_changes_per_day` successful changes per package per 24 h
  (setting, default 3, hard bounds 1–10 — the protection against passing one package around cannot be switched
  off). Refused attempts do not count against it; they are recorded.
* Every attempt, refusals included, is a row in the append-only `iam_v2.device_slot_actions` (actor, operator,
  action, outcome, reason). edged also writes its operator audit (`client_device.*`).
* Nothing deletes a record, resets a quota, or changes the package's window, consumption or owner.
* `deauthorize_entitlement_device` stays unreachable by every runtime role; the definer functions are the only
  callers. The dark Phase-6 guest release (`p6_guest_release_device_policy`) is untouched and no longer called
  by the portal.

## 3. The client (portal)

* **At the limit:** a client who signs in on a new device whose package has every place taken is sent to
  **Choose a device to replace** (`GET/POST /devices/replace`): the package's devices (Device 1, 2 … — never a MAC),
  online or not, "N of N devices in use", the changes left today, and **Use this device instead**. Choosing one
  disconnects it, moves its place to this device and brings this device online on the same package. "Not you? Sign
  out" leads back to the sign-in page.
* **Online:** the "Your devices" panel on the online page (`POST /devices/list`, `/devices/release`) shows the
  package's devices, "This device", places in use and changes left, and lets the client disconnect and remove
  another device (online ones with an explicit "it goes offline now" confirmation).
* Refusals are one sentence, except today's limit, which is a rule the client must know.

## 4. Staff (Admin Console → Clients → Devices)

* Search by MAC, address or account name (`X-Device-Search` header, never the URL). Offline devices included.
* A device's view: online state, the package(s) it uses and whether it holds a place there, places in use / allowed,
  recent sessions with end reasons, sign-in protection restriction, remembered sign-in, whether it has used a free
  package, recent device actions.
* Distinct actions, each saying what it changes and keeps: **Disconnect** (sessions only), **Release this place**
  (one package), **Require fresh sign-in**, and **Prepare for a sign-in test** (all three, for a device reused in
  testing). The account's Manage view links each of its devices here.
* Setting **Device changes a client may make per day** (`/edge/v1/device-settings/`, auth-methods key, reason +
  password step-up, change log `iam_v2.device_settings_changes`).
* Permissions: the `sessions` key (read to see, write to act) — the same live-access state as Active sessions.

## 5. Why a previously used device "could not reach the login page"

Diagnosed on PRE-LIVE, 2026-10-15. Four causes, none of them enforcement or captive detection (both were correct:
the nft authorization sets were empty, the captive API answered captive for a device without a session):

1. **Sign-out left the portal's own sign-in behind.** The landing page continues an unfinished sign-in at package
   selection for ten minutes (the commerce session). `/logout` ended the session and the remembered sign-in but not
   that, so after any sign-in that ended without a package — refused at the device limit, a suspended account, a
   client who signed out from the package page — every visit went straight back to the packages of the account
   just left. **Fixed:** sign-out ends the commerce session and the account token; the package and replacement
   pages carry "Not you? Sign out".
2. **The device limit had no way out.** A 1-device package held by a device that had signed out refused every other
   device with a sentence and nothing to act on. **Fixed:** the replacement page (§3).
3. **The free package is once per device** (`PRIOR_PURCHASE also_by_device`). A phone that has had the free package
   is not offered it again under a new account. This is the intended protection against repeated free access and
   is **unchanged**; the device view says so ("Free package already used on this device"), and Prepare for a
   sign-in test does not reset it.
4. **A stranded entitlement.** The test phone still held the only place on an ACTIVE 1-device package acquired with
   the (since removed) email code, whose subject can no longer sign in. Staff can now release that place.

## 6. HTTP fallback

Unchanged by decision of the Product Owner (not redesigned at this stage). While HTTPS is not active the portal is
served over plain HTTP on the guest network; device and account pages work there too. **HTTP is not secure**: a
party on the guest network can observe or alter portal traffic, including sign-in. The HTTPS portal is the secure
path; see ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §14.

## 7. Verified on PRE-LIVE, and what remains

See §8 of the acceptance checklist (docs/acceptance/HTTPS-Portal-Real-Device-Acceptance-Checklist.md, section G)
and `scripts/device-management-e2e-test.sh`. Not changed here and recorded as a product decision: whether a
package should allow automatic slot recycling for long-idle devices.

**The replacement page is offered for every sign-in** — account, email account, voucher (including one-step
redemption), code/social, open selection and **room sign-in** (portal audit H-6, 2026-10-16). A room guest whose
stay has every place taken has already proved the stay (scd refused the *grant*, not the sign-in), so being told
"every place is in use" discloses nothing a wrong room could learn. scd's DEVICE_LIMIT refusal names, for portald
only, the device it placed and its network; portald holds the verified room sign-in in a commerce session exactly
as for every other method and sends the guest to **Choose a device to replace**. `client_device_action` already
derives the package from the stay (`auth_contexts.stay_id`), and activation admits the replacing device on the
strength of its own room sign-in for the same stay (`iamv2_session_activate.go`). The daily-change limit, the
offline-target rule and the audit are the same as for every client. Where the portal has nowhere to hold the
sign-in (the Phase-2 portal surface is off) the sentence remains, and it now says how a place is actually freed:
"…remove one under “Your devices” on a device that is online with it, or contact the site team" — signing out
does not free a place (§1).

A retry on the replacement page after the place has moved (activation failed, a double tap, a reload) activates
on the place this device already holds; it never replaces a second device or spends a second change. Devices are
numbered as in the online page's "Your devices" panel.

## 8. A client's device on the Wi-Fi: what can go wrong, and what the product does

Investigated on PRE-LIVE 2026-10-15 with the Product Owner's iPhone, by packet capture and the portal's own logs.
The appliance answered correctly at every step; each trap below is where a client could stop, and how the product
now keeps them moving.

| # | What the client experiences | Cause | What the product does |
|---|---|---|---|
| 1 | The sign-in sheet does not open again after they closed it, even after reconnecting | iOS/Android decide when to re-check a network. A phone that closed its sheet ("Cancel", "Use without Internet") may keep that verdict across quick reconnects and ask nothing (captured: three reconnects with DHCP, no captive-API request; a 5-second Wi-Fi off/on, then the API request and the sheet) | Every device on the guest network gets the captive-API URL by DHCP option 114 and the API answers `captive: true` until it signs in; every `http://` page redirects to the portal. Staff device actions now tell staff what to ask the client: turn Wi-Fi off and on, or open any `http://` site. The portal now logs the OS probes it answers, so "did the phone check?" is answerable from the journal. |
| 2 | Typing an `https://` website before signing in just spins | Pre-sign-in HTTPS is dropped, never intercepted (no certificate spoofing); dropping, not resetting, is what makes desktop Chrome/Edge open their login tab | Unchanged by design. Client guidance is the `http://` site above. |
| 3 | "Email or password incorrect" for an account that was removed | A removed account can no longer sign in; the answer stays generic so it never reveals which addresses have accounts | The refusal now says what to do: "New here, or was your account removed? Tap Create an account. Forgot your password? Tap Forgot password?" (all languages). Staff's Remove dialog says the client must register again. |
| 4 | A new account on the same phone is not offered the free package | The free package is once per device (`PRIOR_PURCHASE also_by_device`), the protection against repeated free access | Unchanged by design. The package page says "You've used your free access here"; the staff device view says "Free package already used on this device". |
| 5 | A device keeps holding a place on a package nobody can use | Removing an account left its devices' places on its live packages | Removing an account now frees its devices' places (the audited staff release; the package and its history stay). |
| 6 | All places on a package are taken | One-entitlement device limit | The client chooses which of their devices this one replaces (§3), limited per day. |
| 7 | Sign-out, then the portal shows the old account's packages | The ten-minute package-selection session outlived sign-out | Fixed: sign-out ends it. |
| 8 | A phone appears as a new device | iOS/Android private Wi-Fi addresses change when the network is forgotten, or on rotation | A new address is a new device: it takes a free place or uses the replacement choice. Known limit: a client who changes their phone's address also looks like a new device to the once-per-device free package rule. |
| 9 | After the free package, "no other package is available — contact the site team", on the sign-in page with the Room form selected | A paid package sold only by card, voucher or room charge, priced in a currency the card account does not settle (PRE-LIVE: OneDay in USD, the Stripe test account in GBP, no conversion): card is never converted, a voucher is entered as a code, room charge needs a room sign-in -- so an email or account client had nothing to buy | The client now stays on the package page, signed in, with the reason, a voucher-code field (when the site takes vouchers) and Sign out. The Admin Console dashboard raises "OneDay cannot be bought by card", naming both currencies and the fix. The configuration itself (price the package in the card account's currency, or a card account in the package's) is the site's decision and was not changed. |
