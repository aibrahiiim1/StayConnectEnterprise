# Common Tasks & Troubleshooting

Task-oriented how-tos and "something is wrong, where do I look?" checklists. Applies to any role — do what your permissions allow and escalate the rest. Pages are named as they appear in the menus: **Hotel Admin** (on the appliance) or **Central** (the vendor console).

## A guest can't log in

Work through this top-down. Stop at the first match.

### 1. Is the guest actually on your Wi-Fi?

Ask them to **forget the network** and rejoin. In **Hotel Admin → Networking → DHCP & leases → Active leases**, look for their device's MAC address right after they rejoin (IT roles; the guest can read their MAC from **Device information** at the bottom of the sign-in page).

- If their MAC never appears → they're not associated. Wi-Fi radio / SSID issue on the hotel's access points, not a Velonet problem.
- If it appears but the sign-in page never loads → check the appliance's health pill in the Hotel Admin top bar and **System → Diagnostics**.

### 2. Do they see the sign-in page?

Modern phones should pop up a "Sign in to network" window within a few seconds. If not:

- **iPhone**: open **Settings → Wi-Fi → (the SSID)**. If there's no "Log In" button, open Safari and go to `http://nossl.com/` — that forces the portal.
- **Android**: tap the Wi-Fi icon; many phones show "Sign in required" as an inline notification.
- **Windows / Mac**: a "Sign in to network" browser window usually opens automatically. If not, browse to any `http://` site.

If no device shows the sign-in page at all, check that the guest network has **Sign-in page** enabled (**Networking → Guest networks**) and look at **System → Diagnostics**. Escalate.

### 3. How are they trying to sign in?

- **Voucher** → [Voucher not working](#voucher-not-working)
- **Room + name (PMS)** → [Room sign-in failed](#room-sign-in-failed)
- **Email / SMS code** → [Code not arriving](#code-not-arriving)
- **Social login** → [Social login fails](#social-login-fails)

### 4. They signed in but "nothing loads"

- Find them in **Hotel Admin → Guests → Active sessions** — is the data or time allowance used up (see the allowance meters), or has the session ended with a reason?
- Look at the Hotel Admin top bar health pill and **Overview** — is the appliance healthy and its internet uplink up?
- Try a second device from the same room — if it also fails, the uplink is down. If only their device fails, it's device-side.

## Voucher not working

1. **Hotel Admin → Internet offering → Vouchers** → search by the last characters of the code.
2. **Not found** → typo, or a card from another property. Make sure they're reading the right card.
3. **Used** → already redeemed. Hand them a fresh card.
4. **Expired never used** / **Not yet valid** → outside the card's validity; hand them a fresh card.
5. **Cancelled** → the card was cancelled. Hand them a fresh card.
6. **Available but not letting them in** → if the guest sees *"This voucher has reached its device limit"*, the card is at its service plan's **devices at once** — disconnect one of its devices in **Active sessions**, or hand them a separate card. A reconnect from the *same* device never uses an extra place. *"The guest network is at capacity"* instead means the whole appliance is at its licensed **max concurrent online guests**.

If you need to read the full code on a card (e.g. a smudged card), open it and use **Show full code** — it asks for a reason and your password, and is recorded.

## Room sign-in failed

1. **Hotel Admin → Property management system → PMS connection** → does room sign-in say it is working?
   - Not working → the PMS link or guest list is the problem. Open the connection for details; contact the PMS vendor if the PMS is unreachable.
2. **Guest sign-in attempts** → find the attempt by room and read **Why**. Roles allowed to see guest credentials can open **Details** to compare what was entered with what would have been accepted.
   - Room number **exactly** as the PMS has it (some PMSes store "0214", some "214").
   - Name spelling — the guest should enter the full first name, family name, or reservation number, depending on the mode set under **Sign-in methods**.
3. **Guest sign-in checks** → if every attempt on ONE guest network fails while others work, that network points at the wrong PMS or none: fix it under **Network routing**.
4. **Too many attempts**: after too many wrong tries (by default 5 within 60 seconds) the device is asked to wait (by default 60 seconds) and sees a countdown. Reception can **Release** it on **Guest sign-in attempts → Active restrictions** — this lets the device try again; it does not sign the guest in.
5. If all of the above check out and the PMS still rejects, the reservation may not be in the PMS correctly. Have reception check the PMS directly; **PMS activity** shows whether the check-in message ever arrived.

## Code not arriving

1. **Hotel Admin → Guest portal → Email & SMS** → is the sender's **Delivery** *Sending*, *Failing* or *Never used*? Is **Offered** on?
2. On **Sign-in methods**, the Email code / SMS code method says *Not available* until a sender exists and is switched on.
3. If the sender works but a guest doesn't get their code, they probably mistyped their address or number. Ask them to use *Try a different email* / *Use a different number*.
4. Common traps:
   - Email codes landing in spam (branded senders + SPF/DKIM help).
   - SMS blocked by the carrier — check the Twilio logs.
   - Typo like `@gmial.com` — no system can fix this; the guest needs to re-enter.

## Social login fails

1. **Hotel Admin → Guest portal → Social login** → is the provider **Offered**, and when was it last used?
2. Client secret expired? OAuth client secrets on Google/Apple/Facebook/Microsoft expire periodically. Paste the new one in with **Edit** (secrets are write-only).
3. Guest sees a redirect error → the provider's OAuth app has an outdated redirect URL. Update it in the provider's console to match the **Redirect URI** on the Social login page.

## Appliance is offline

1. **Central → Appliances** → find it; note its Status and **Last seen**.
2. Call the site. Ask someone to:
   - Check the appliance has power.
   - Check the uplink cable is plugged in.
   - Check the uplink itself works (plug a laptop into the WAN port and try to browse).
3. If physical looks fine but the appliance still isn't heard from, reboot it (pull power for 10 seconds, plug back in). Wait 2 minutes.
4. Still offline after 10 minutes → escalate to Velonet support with the appliance serial and last-seen time.

An appliance that cannot reach Central keeps serving guests — Central is used for licensing only. On site, **Hotel Admin → System → Appliance & licence** shows whether Central is reachable.

## Sessions count is zero but guests are present

Something is badly wrong. Try in order:

1. **Appliance healthy?** Check the Hotel Admin health pill and **System → Diagnostics**. If Hotel Admin itself does not load, see above.
2. **Sign-in page reachable?** From a phone on the guest SSID, browse to `http://<guest network gateway>:8380/` (e.g. `http://10.20.0.1:8380/`). If it doesn't load, the portal service is down (Diagnostics).
3. **DHCP working?** From the same phone: **Settings → Wi-Fi → (SSID) → i**. Does it show an address in the guest network's range? If not, check **DHCP & leases** and Diagnostics.
4. **Sign-in working?** Try a known-good voucher yourself. If you can't sign in either, check the license on **System → Appliance & licence** (expired, suspended or at capacity refuses new sign-ins) and Diagnostics.

Escalate with specifics: which of the four steps above failed.

## Too many alerts

Noisy alerts usually mean a policy or threshold needs tuning. Don't just acknowledge everything — figure out which alert is noisy. In Hotel Admin, **System → Alerts** lists checkouts the checkout grace policy could not handle (tune it on **Checkout grace**); in Central, **Security alerts** must each be investigated, because they block activation while open.

## Someone left the company and still has access

For a **Hotel Admin** account (on the appliance):

1. **Hotel Admin → System → Operators** (Site admin only) → find them → **Disable** immediately.
2. Check **System → Activity** (filter by the **Security** chip, or search their name) for their recent actions.
3. If they knew PMS, email/SMS or social-login credentials (not a role, just knowledge), replace those under **PMS connection**, **Email & SMS** or **Social login**.

For a **Central** account: **Central → Operators** → **Disable**, and check the **Audit log**.

If they were a platform admin, contact Velonet operations directly — you can't disable them yourself.

## Useful places to look

- **Central → Audit log**, filter by action (e.g. `site.created,operator.disabled`) — what changed in Central this week.
- **Hotel Admin → System → Activity**, **Security** chip — sign-ins, code reveals and other security events on the appliance.
- **Hotel Admin → Vouchers → Access log** — who has read voucher codes, and why.
- **Hotel Admin → Guest sign-in checks** — refusals by reason and by network; a spike hints at a broken PMS connection or routing.
