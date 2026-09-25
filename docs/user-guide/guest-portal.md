# Velonet Guest portal — What Guests See

The **guest portal** is the page that opens on a guest's phone, tablet or laptop when it joins the hotel's
guest Wi-Fi. It is served by the appliance itself, before the guest has internet, so it carries no external
fonts, scripts or tracking and works inside the small "sign in to network" browser that phones open
automatically. Guests have no Velonet account; they only ever see this page.

Everything here is configured in **Velonet Hotel Admin** under the **Guest portal** menu group — see
[hotel-admin-reference.md](hotel-admin-reference.md#guest-portal).

---

## The sign-in page

Top to bottom:

1. **Hero area** (only in some layouts): photograph, logo, hotel name and welcome line.
2. **Language selector** in the top corner (top-left in Arabic).
3. **The card:** logo and/or hotel name and the welcome line.
4. **Notices** when they apply: an amber notice that the guest's access has ended (data or time used up),
   an amber notice that no internet package is available here, and a red message from the last attempt.
5. **Two tabs:** **Guest Login** (room sign-in and post-stay PIN) and **Account Login** (voucher, personal
   account, email code, phone code, social login). When only one group has anything enabled, the tabs are
   hidden.
6. **The form** for the chosen method, and **"Or sign in with"** links to the group's other methods.
7. **Extras:** the hotel's help line and custom HTML block.
8. **Footer:** a **Terms of use** link (if the hotel set one; it is a link only, there is no acceptance
   checkbox) and a **Device information** button that shows the device's IP and MAC address — *"Reception
   may ask for these if you need help connecting."*

If no method is enabled the page says: *"There is no way to sign in on this network yet. Please contact
reception."*

## Sign-in methods

Which methods appear is set in **Hotel Admin → Sign-in methods** and is also limited by the appliance's
license.

| Method | What the guest enters | Notes |
|---|---|---|
| **Room** | Room Number, plus one detail: last name, first name, reservation number, or any of them (the hotel chooses; a hint under the field says which) | Checked against the hotel's PMS through the appliance's guest list. If the guest qualifies for more than one internet package, the page offers **Choose your internet package**; with one package it is granted directly. |
| **Voucher** | Voucher Code | A **Use Personal Account** switch changes to the account form when both are enabled. |
| **Personal account** | Username, Password | Accounts created by staff under **Hotel Admin → Guest accounts**. |
| **Email** | Email address → **Send code**, then the 6-digit code → **Verify** | Needs a sender under **Email & SMS**. *Try a different email* starts again. |
| **Phone** | Phone number with country code → **Send code**, then the 6-digit code → **Verify** | Needs a text-message sender under **Email & SMS**. |
| **Social** | **Continue with Google / Apple / Facebook** | Leaves for the provider and comes back. Providers are set up under **Social login**. |
| **Post-stay** | Post-stay PIN given at checkout → **Reconnect** | Lets a departed guest reconnect for a limited time (see **Post-stay access**). |

## Messages guests can see

The wording is deliberately vague for room sign-in: it never reveals whether a room exists or who is staying
in it, and a wrong room and a wrong name give the same message. Examples (English):

| Situation | Message |
|---|---|
| Wrong room or guest detail | "The room number or guest detail you entered is incorrect. Check the room number and enter the full first name, family name, or reservation number." |
| Stay cannot be checked right now | "We are unable to verify your stay right now. Please try again or contact Reception." |
| Too many attempts | "Too many attempts. Please wait N seconds and try again." with a live countdown, then "You can try again now." |
| Wrong voucher | "That voucher code didn't work. Check the code and try again." |
| Wrong account | "The username or password is incorrect." |
| Voucher or account at its device limit | "This voucher (account) has reached its device limit. Disconnect another device and try again." |
| Hotel at its licensed number of online guests | "The guest network is at capacity. Please try again shortly." |
| Device not on the guest network | "Your device isn't on the guest network." |
| Access ended | "Your Internet package has ended because the data allowance was used." / "…because the access time expired." |
| No internet package configured | "Internet access is not available here at the moment…please let reception know." |
| Wrong or expired email/SMS code | "That code isn't right…" / "That code can no longer be used. Please ask for a new one." |
| Post-stay PIN not accepted | "We could not verify your stay. Please check your details or contact reception." |
| Social sign-in did not finish | "Sign-in didn't work" page with **Back to sign-in** |

How many wrong tries a device gets, and how long it must wait, is set under **Sign-in methods → Guest
sign-in protection**. Reception can release a waiting device on **Guest sign-in attempts** — which lets it
try again but does not sign the guest in.

## Choose your package

Shown after sign-in when more than one internet package is available to the guest: *"Choose your package —
You're signed in. Select a package to get online."*, with one button per package showing its name and speed
and time. Packages are free to the guest; there is no payment step. If the guest waits too long they are
asked to sign in again.

## "You're online"

A tick, **You're online**, the time remaining (or *No time limit*), a **Status** link and a **Disconnect**
button. Where they apply:

- **Time left** for online-time budgets: *"… of internet time left. This counts down only while you are
  connected."* and *"Your access ends on {date}, whether or not the time is used."*
- **Your devices**: *Device 1, Device 2…*, each *Connected now* or *Not connected · Last used …*, with
  **Remove this device** to free its place for another device. A device that is online cannot be removed.
  This panel appears only when **Hotel Admin → Guest devices** is switched on and available on the
  appliance.

## Languages

Six languages are built in: **English, Arabic, German, French, Italian and Russian**. Arabic is shown right to
left, with the layout mirrored. The guest's language is picked from their device's language settings, or
remembered if they chose one from the selector. The sign-in page, the package page, the "You're online" page
and the server's messages are all translated. The hotel name, welcome line and help line are single texts and
are not translated per language.

## Layout templates

Six layouts arrange the same sign-in page differently:

| Template | Look |
|---|---|
| **Classic** (default) | A centred card over the hotel's photograph |
| **Split** | Photograph and welcome on one side, sign-in on the other; stacks on a phone |
| **Immersive** | Full-screen photograph, large type, frosted-glass sign-in panel |
| **Header bar** | A business layout: top bar with the logo, sign-in beside a help column, terms in a footer |
| **Resort** | A tall banner with the welcome as the headline, the sign-in card overlapping it |
| **Kiosk** | No imagery and large controls, for a lobby tablet |

## What Hotel Admin controls

**Guest portal → Sign-in methods** (Site admin, Hotel IT manager):

- Which methods are offered — voucher, guest account, room sign-in, email code, SMS code, social login (per
  provider). Changes apply to the next guest who opens the page; guests already online are not disconnected.
- For room sign-in, what the guest types besides the room number.
- Guest sign-in protection: maximum failed attempts, observation window and waiting time.

**Guest portal → Portal settings** (Site admin, Hotel IT manager; saved changes reach guests immediately):

- **Template** and its options (hero photograph, photo darkening, panel position, banner height, frosted or
  solid panel, spacing, heading typeface).
- **Brand:** logo and background photograph (PNG, JPEG, WebP or GIF up to 8 MB, stored on the appliance; no
  SVG), brand colour, button shade, text colour, corner radius and typeface.
- **Content:** hotel name (120 characters), welcome line (280), help line (600), terms of use link.
- **Sign-in page text:** any built-in string can be reworded per language.
- **Languages:** which of the six are offered (English is always available), and adding another language
  with the hotel's own wording.
- **Advanced HTML & CSS:** custom CSS and HTML (password confirmation; scripts are refused). Hotel CSS can
  restyle the page but cannot hide the sign-in controls.
- **History:** restore an earlier saved design (password confirmation).

The other **Guest portal** pages supply what the methods need: **Allowed sites** (addresses reachable before
sign-in), **Social login** (provider apps) and **Email & SMS** (code senders). Guest accounts, vouchers and
internet packages are managed under **Guests** and **Internet offering**.
