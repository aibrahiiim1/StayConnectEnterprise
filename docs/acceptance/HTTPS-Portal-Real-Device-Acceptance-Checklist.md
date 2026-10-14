# HTTPS Client Portal and Social Login — real-device acceptance checklist (PRE-LIVE)

Appliance: PRE-LIVE `172.21.60.25`, guest network VLAN 90 (`192.168.77.0/24`). Contract:
[`ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md`](../architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md).
Record each result as PASS / FAIL / NOT RUN with the device model and OS version. A test not run is NOT RUN,
never PASS. Never record a guest's name, email or room in this file.

Prerequisite: Admin Console → **Network → Portal HTTPS** shows **HTTPS active** with a **production**
(publicly trusted) certificate. A staging certificate is not trusted by phones and these tests would fail.

**Prerequisite met on 2026-10-14:** PRE-LIVE serves a production Let's Encrypt certificate for
`t89b76xm.gw.onegate.work` (issuer YE1, valid to 2027-01-12); HTTPS active. Automated guest-device checks passed
18/18 (HTTPS path), 12/12 (sign-in), 15/15 (social offering). The rows below are for real phones and are NOT RUN
until a person runs them.

## A. Joining and the login window

| # | Device | Steps | Expected |
|---|---|---|---|
| A1 | iPhone, current iOS | Forget the network, join the guest SSID | The login sheet opens by itself on `https://<hostname>/`, padlock, no warning |
| A2 | iPhone, older iOS (14–16) if available | Same | Same |
| A3 | Android 12+ (updated Google Play system) | Same | Login opens in a **Chrome tab** (not the plain system window) |
| A4 | Android 11 or a device without the 2026 module | Same | The system login window opens on the HTTPS page |
| A5 | Android, Private DNS = `dns.google` (strict) | Same | Login window still opens and the page loads |
| A6 | iPhone with an encrypted-DNS profile, if available | Same | Login sheet opens |
| A7 | Windows or macOS laptop | Join | Browser opens the login page (HTTPS) |
| A8 | Any phone after sign-in | Open the login page again | Shows signed-in status (captive API reports not captive) |

## B. Sign-in methods over HTTPS

| # | Steps | Expected |
|---|---|---|
| B1 | Email code: request a code | The email has the code **and** a link starting `https://<hostname>/connect` |
| B2 | Enter the code | Signed in; internet works |
| B3 | Tap the email link on the same phone | Signed in |
| B4 | Voucher / account / room sign-in | Unchanged behaviour, over HTTPS |

## C. Social login (needs the hotel's own test apps — see the Admin Console help per provider)

| # | Provider | Login window | Expected |
|---|---|---|---|
| C1 | Microsoft | iPhone sheet | Completes, signed in |
| C2 | Microsoft | Android | Completes |
| C3 | Facebook | Android 12+ Chrome tab | Completes |
| C4 | Facebook | Old Android system window | "Open in your browser" page instead of Facebook |
| C5 | Apple | iPhone sheet | Completes |
| C6 | Google (needs the hotel domain) | Android 12+ Chrome tab | Completes |
| C7 | Google | iPhone sheet | Record what happens (Google may refuse inside the sheet) |
| C8 | Google | Old Android system window | "Open in your browser" page |
| C9 | Any provider | Start on the phone, finish after switching to the full browser | Completes (no cookie needed) |

## D. Networks and failures

| # | Steps | Expected |
|---|---|---|
| D1 | A second guest VLAN (if configured) | Same hostname, same certificate, login works |
| D2 | Cut the appliance's internet (WAN) for 10 min, join a fresh phone | HTTPS portal loads; code entry works only if the code was already sent; social sign-in unavailable |
| D3 | Stop Central (or block it) for 30 min | No change for guests |
| D4 | Force fallback (operator test on PRE-LIVE only) | Portal served over HTTP; email has **code only**; social hidden; Admin Console shows the alert |
| D5 | Restore | HTTPS returns by itself within 5 minutes; alert clears |

## E. Findings from the first real-device round (2026-10-14)

| Device | What happened | Evidence | Outcome |
|---|---|---|---|
| Android (A71) | Login opened by itself on the HTTPS page | captive API requested at 15:07:51 UTC right after DHCP | PASS (A3/A4 behaviour) |
| iPhone | Login sheet opened by itself on the HTTPS page | captive API requested at 15:09:09 UTC right after DHCP | PASS (A1) |
| MacBook Air | No login window after forgetting and rejoining; `http://neverssl.com` redirected to the portal | packet capture of a fresh join (15:58:59 UTC): the Mac REQUESTED option 114 and OneGate's ACK CARRIED `https://<host>/captive-portal/api`; macOS then made NO captive check at all -- no lookup of `captive.apple.com`, no Apple probe on port 80, no captive-API request. Its DNS goes to `8.8.8.8` (manually configured, not the DHCP-provided resolver). The only automatic portal traffic was Firefox's own check, which OneGate redirected to the HTTPS portal. | NO ONEGATE DEFECT: macOS on this Mac did not run its captive-network detection. Likely Mac-side configuration (captive network support disabled, a management profile, or a remembered per-network choice); see E2 |
| iPhone, email code | Code sent, but the mailbox could not be opened on the same Wi-Fi | the 10-minute window carried only 60 KB; every captive-API answer during it said `captive: true` | DEFECT, fixed (below) |

**Temporary internet during email verification — root cause and fix.** The appliance admitted the device for
its window, but the RFC 8908 captive API kept answering `captive: true`, so iOS (and Android) held the Wi-Fi as
captive and kept ordinary apps such as Mail off it. Before the option-114 fix (2026-10-13) phones relied on the
HTTP probe, which a device in its window already bypasses, so the problem only appeared once option 114 really
reached phones. The API now reports `captive: false` with `seconds-remaining` equal to the window while the
device holds a live, unexhausted window and no session; when the window ends the device is captive again. The
firewall's admission, duration, data and rate limits, device binding and abuse limits are unchanged. Deployed to
PRE-LIVE (scd and portald dbafb5fa). Guest-device test `scripts/portal-temporary-access-e2e-test.sh`: before the
request the mail host is unreachable and the API says captive; during the window the API says not captive
(598 s), `https://outlook.live.com/` answers, the OS probe answers 204, and the device is in the verification set
only.

**To retest on a phone (E1):** a client that has not signed in before; request an email code; when the sheet
shows **Done**, tap it, open the mail app ON THE SAME Wi-Fi (mobile data off), read the code; then either tap the
link in the email (it opens the HTTPS portal and connects this device) or reopen the portal and enter the code.
Result: NOT RUN.

**MacBook (E2), to confirm on the Mac itself:** run `ipconfig getpacket en0` (the reply should list option 114 with
the HTTPS API URL) and `curl -s https://<host>/captive-portal/api` (should answer `"captive":true`). Check whether the
Mac is managed (System Settings -> Privacy & Security -> Profiles) or has captive detection turned off
(`defaults read /Library/Preferences/SystemConfiguration/com.apple.captive.control Active` prints 0). On a Mac
whose detection is off, opening any `http://` site (as with neverssl.com) or the portal hostname reaches the
HTTPS portal; that is the documented workaround. Result: NOT RUN.
