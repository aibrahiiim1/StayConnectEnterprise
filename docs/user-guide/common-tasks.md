# Common Tasks & Troubleshooting

Task-oriented how-tos and "something is wrong, where do I look?" checklists. Applies to any role — do what your permissions allow and escalate the rest.

## A guest can't log in

Work through this top-down. Stop at the first match.

### 1. Is the guest actually on your WiFi?

Ask them to **forget the network** and rejoin. Look for their device MAC in **Sessions** with auth=`none` (pre-auth) right after rejoin.

- If their MAC never appears → they're not associated. WiFi radio / SSID issue. Not a StayConnect problem.
- If it appears briefly then disappears → their device got a DHCP lease but can't reach the gateway. Check appliance status.

### 2. Do they see the captive portal?

Modern phones should auto-pop a "Sign in to network" dialog within a few seconds. If not:

- **iPhone**: open **Settings → Wi-Fi → (the SSID)**. If there's no "Log In" button, open Safari and go to `http://nossl.com/` — that forces the portal.
- **Android**: tap the Wi-Fi icon; many phones show "Sign in required" as an inline notification.
- **Windows / Mac**: a "Sign in to network" browser window usually opens automatically. If not, browse to any `http://` site.

If no OS auto-pops the portal at all on any device, the appliance's DHCP option 114 or nftables DNAT is broken. Escalate.

### 3. What auth method are they trying?

- **Voucher** → [Voucher problems](#voucher-not-working)
- **Room + name (PMS)** → [PMS verification failed](#pms-verification-failed)
- **Email / SMS OTP** → [OTP not arriving](#otp-not-arriving)
- **Social login** → [Social login fails](#social-login-fails)

### 4. They authenticated but "nothing loads"

- Check their **Session** — is there a quota exhausted flag?
- Check **Appliances** — is the site's appliance online?
- Try a second device from the same room — if it also fails, site uplink is down. If only their device fails, it's device-side.

## Voucher not working

1. Open **Voucher batches** → search the code.
2. **Not found** → typo, or a voucher from a different tenant. Make sure they're reading the right sheet.
3. **Used** → already redeemed. If by a different MAC, the code was shared or guessed. Reissue a fresh code.
4. **Expired** → generate a new one.
5. **Revoked** → the whole batch is gone. Generate a new batch or hand them a code from a live batch.
6. **Valid but not letting them in** → check quota/data cap on the batch; check concurrent-device limit on the subscription plan.

## PMS verification failed

1. **Menu: PMS providers** → is the provider status green?
   - Red / disconnected → the PMS itself is unreachable. Wait / contact PMS vendor.
2. Double-check the guest's input:
   - Room number **exactly** as the PMS has it (some PMSes store "0214", some "214").
   - Last name spelling — accents and apostrophes matter. "O'Brien" vs "OBrien" are different strings to most PMSes.
3. Check the **stay window**:
   - Too early? Default grace is 2 h before check-in. Guest arriving at noon with 3 PM check-in will fail until 1 PM.
   - Too late? After check-out + grace, PMS rejects. This is correct behaviour.
4. **Per-room lockout**: too many failed attempts on the same room (default 5 in 15 min) triggers a cool-down. Guest will see "too many failed attempts" — tell them to wait 15 min and try again, or verify the correct name with reception.
5. If all 4 above check out and PMS still rejects, the reservation may not be in the PMS correctly. Have reception check the PMS directly.

## OTP not arriving

1. **Menu: Notifications** → is the provider green?
2. **Test notification** from the provider page → does the test email/SMS arrive at your own address?
3. If the test works but guests don't get theirs, the guest mistyped their address/number. Ask them to re-enter carefully.
4. Common traps:
   - Email OTP landing in spam (branded senders + SPF/DKIM help — ask your tenant admin).
   - SMS blocked by carrier (Twilio short-code deliverability is a rabbit hole — check Twilio logs).
   - Typo like `@gmial.com` — no system can fix this; guest needs to re-enter.

## Social login fails

1. **Menu: Social login** → provider status.
2. Credentials expired? OAuth client secrets on Google/Apple/Facebook expire periodically. Rotate and paste the new one in.
3. Guest sees "Redirect URI mismatch" → the provider's OAuth app has an outdated redirect URL. Update it in Google/Apple/Facebook console to match what StayConnect expects (shown on the Social login page).

## Appliance is offline

1. **Menu: Appliances** → the one marked red. Note last-seen time.
2. Call the site. Ask someone to:
   - Check the appliance has power.
   - Check the uplink cable is plugged in.
   - Check the uplink itself works (plug a laptop into the WAN port and try to browse).
3. If physical looks fine but the appliance still won't heartbeat, reboot it (pull power for 10 seconds, plug back in). Wait 2 minutes.
4. Still offline after 10 minutes → escalate to StayConnect support with the appliance ID and last-seen time.

## Sessions count is zero but guests are present

Something is badly wrong. Try in order:

1. **Appliance online?** If no, see above.
2. **Captive portal reachable?** From a phone on the guest SSID, browse to `http://10.10.0.1:8380/` (or your equivalent portal URL). If it doesn't load, the portal daemon is down.
3. **DHCP working?** From the same phone: **Settings → Wi-Fi → (SSID) → i**. Does it show an IP in the expected range (e.g. `10.10.0.x`)? If not, DHCP is broken.
4. **Auth working?** Try a known-good voucher yourself. If you can't log in either, auth backend is down.

Escalate with specifics: which of the four steps above failed.

## Too many alerts

Spam from alerts usually means a threshold is too aggressive. Don't just silence everything — figure out which alert is noisy and tune its threshold with your tenant admin + the platform admin. Silencing across the board hides real problems.

## Someone left the company and still has access

If they had `tenant_admin` or `tenant_operator`:

1. **Menu: Operators** (tenant_admin only) → find them → **Disable** immediately.
2. Check the **Audit log** for the last 30 days of their activity — anything suspicious?
3. If they had PMS or SMTP credentials memorised (not a role, just knowledge), rotate those in the relevant provider config.

If they were a `platform_admin`, contact StayConnect ops directly — you can't disable them yourself.

## Useful saved searches

- `Audit log` filter: **"operators.create"** in the last 7 days — new staff this week.
- `Audit log` filter: **"voucher_batches.revoke"** — batches someone pulled lately.
- `Sessions` filter: auth method = `pms`, status = `failed` — guests failing PMS logins; a spike hints at a broken PMS integration.
