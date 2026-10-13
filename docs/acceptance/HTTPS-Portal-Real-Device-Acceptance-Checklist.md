# HTTPS Client Portal and Social Login — real-device acceptance checklist (PRE-LIVE)

Appliance: PRE-LIVE `172.21.60.25`, guest network VLAN 90 (`192.168.77.0/24`). Contract:
[`ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md`](../architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md).
Record each result as PASS / FAIL / NOT RUN with the device model and OS version. A test not run is NOT RUN,
never PASS. Never record a guest's name, email or room in this file.

Prerequisite: Admin Console → **Network → Portal HTTPS** shows **HTTPS active** with a **production**
(publicly trusted) certificate. A staging certificate is not trusted by phones and these tests would fail.

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
