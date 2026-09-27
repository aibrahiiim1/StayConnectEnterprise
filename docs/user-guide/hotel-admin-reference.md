# OneGate Admin Console — Page-by-Page Reference

**OneGate Admin Console** (formerly Hotel Admin) is the console that runs on the appliance at the site. Site staff use it for
everything day to day: client networks, sign-in methods, vouchers, internet packages, the PMS, sessions and
reports. It is reached over HTTPS on the site's management network and keeps working when OneGate Central
cannot be reached. Operator accounts are local to the appliance: they are not OneGate cloud accounts and do
not work at any other property.

This document describes every page in the menu, in menu order: what it is for, what it shows, what an
operator can do, which roles can change it, and which actions ask for a reason, password confirmation, typed
confirmation, a one-time reveal, or the apply → confirm → automatic rollback flow. For step-by-step setup see
[hotel-admin-config-manual.md](hotel-admin-config-manual.md). The visual language (colours, components,
states) is defined once in the [OneGate design system](../../design-system/README.md).

---

## Things that apply to every page

- **Sign-in.** The login page is titled *OneGate Admin Console*: **Email or username**, **Password**,
  **Sign in**. There is no single sign-on, no multi-factor step and no "forgot password"; an operator with
  the Site admin role changes passwords under **System → Operators**. The session is re-checked every
  30 seconds; if it has ended you are returned to the login page.
- **The sidebar and "Find a screen…".** The menu has seven groups: **Overview · Internet offering ·
  Clients · Client Portal · Hotel · Networking · System** (*Clients* and *Client Portal* were formerly *Guests* and
  *Guest portal*; **Hotel** replaces the former *Property management system* and *Charges* groups and also holds
  Stays, Grace Period and Post-stay access). Hospitality and PMS functions are grouped under Hotel; the rest
  of the console is industry-neutral. At the top is a
  **Find a screen…** filter (press `/` from anywhere outside a text field) that matches page names, group
  names and everyday words ("wifi speed", "room sign in"). The button beside the OneGate mark collapses the
  sidebar to an icon rail and expands it again; collapsed, every icon shows its name as a tooltip, and the
  choice is remembered in that browser. On a phone or narrow window the menu is a full, labelled drawer
  behind the ☰ button. Your email, your role names and **Sign out** are at the bottom of the sidebar.
- **Top bar.** Shows where you are (*Group / Page*), an **appliance health pill** — *Healthy*, *Degraded*
  or *Attention*, with the reasons on hover — and the theme switch.
- **Light, dark or system theme.** Three states: **Light**, **Dark** and **System** (the default, which
  follows your computer's setting). The choice is remembered in that browser and is also offered on the
  login page.
- **Roles decide what you see.** A menu item is hidden when your role cannot read it. On a page you can
  read but not change, a grey **read-only notice** says so (for example *"Your role can view this but not
  change it."*) and the buttons that would change something are **not shown at all** rather than shown and
  refused. The appliance enforces the same rules on every request. The role for each page is listed below;
  a summary table is at the end of this document.
- **"Not enabled on this appliance".** Some features are switched on per appliance (for example the Hotel
  charge pages, Post-stay access, Online-time budgets or Client devices). A switched-off feature is left out of the menu;
  if you open it by address, the page says **"Not enabled on this appliance"**, explains that this is a
  configuration and not a fault, and states that client internet, sign-in, the PMS connection, sessions and
  accounting are unaffected. Turning a feature on is a deployment decision for Semantics support, not an
  operator setting.
- **Blocks you may not see.** Where part of a page is not available to your role or not reported by the
  appliance, it shows a dashed **"Not available"** panel with the reason instead of an empty result.
- **Live figures.** Pages that refresh on their own show **"Updated x ago"** (with the refresh interval
  where there is one) and a refresh button. While refreshing, the content dims instead of disappearing; if
  a refresh fails the page says *"Could not refresh — showing the last answer"*.
- **Confirmation dialogs.** Every destructive or sensitive action opens a dialog that states what will
  happen (a red *"This cannot be undone"* list where it applies). Depending on the action the dialog also
  asks for:
  - a **reason**, which is written to the activity log with your name;
  - **password confirmation** — re-entering your own password in a masked field;
  - a **typed confirmation** — typing an exact word or name (for example `ROTATE`, `REVOKE` or the backup's
    name) before the button enables.

  No action uses a browser pop-up. Add and edit forms open in dialogs or side sheets over the list, so you
  keep your place; nothing is saved until you press the confirming button.
- **One-time reveals.** A new client-account password, a reset post-stay PIN and a newly issued batch of
  voucher codes are shown **once**, in a window with a *"Shown once. It cannot be looked up again"* warning,
  a large value, **Copy**, and an acknowledgement button (such as **I have it**). Closing it is final.
  Voucher codes are the one exception that can be read again later — only with a reason and password
  confirmation, and every such read is recorded (see Vouchers).
- **Pending network change banner.** On the Networking pages, an applied network change is live but
  *pending*: a banner with a live countdown offers **Keep** (confirm) and **Roll back now**. If nobody
  confirms before the countdown ends, the appliance puts the previous configuration back on its own. The
  countdown turns red in the last 30 seconds.
- **Secrets are never shown back.** API keys, client secrets, PMS credentials and tokens are write-only:
  a form shows whether one is stored, never its value.
- **"PMS offline" does not mean guests are cut off.** The appliance keeps its own copy of the in-house guest
  list, so room sign-in, vouchers, client accounts and sessions in progress continue while the PMS link is
  down. What is lost is news: a guest who checked in during the outage cannot sign in by room number until
  the link is back.
- **Licence state.** When the appliance's licence is expired, suspended, revoked or missing, new client
  sign-ins are refused — by every method, room sign-in included — and some creation actions are blocked for
  every role; existing client sessions are not dropped. The licence also caps how many clients may be online at
  once, again across every method: a room guest's second device takes a place just like a voucher guest.
  **System → Appliance & licence** says why.

**Role names used below:** Site admin, Site IT manager, Client services operator, Client relations operator,
Voucher operator, Payments operator, Site viewer.

---

## OVERVIEW

### Overview — `/dashboard`
The shift view: does anything need attention, how busy is the site, and are the PMS, networks and
appliance healthy.

- **Header:** the time range (**24h / 7 days / 30 days**), **Updated x ago** and **Refresh**. The page
  refreshes itself every 30 seconds. Figures are in the appliance's local time zone, which the header names.
- **Needs attention** — a callout listing each problem with a link to the page that fixes it, or *"All
  systems normal — nothing needs attention right now."* A separate **For information** callout carries
  notes that need no action.
- **Headline tiles** (each opens the related page): **Clients online** (one room, account or voucher is one
  client; the device count is underneath), **Sign-ins**, **Data used** (down/up split) and **Room sign-in**
  (*Ready*, *x of y ready* or *Not in use*).
- **Charts and cards:** Internet traffic; Connected devices (peak and average); Sign-in outcomes (by method,
  room-check success and failure reasons); When clients sign in; Packages in use; **Property Management
  System** (per connection, occupancy and — where charging is enabled — room charges); Client networks (address pool
  use, devices, traffic); Services; Addresses and names (DHCP and DNS); Appliance (license, versions,
  WAN/LAN, uptime, CPU, memory, disk).
- **Actions:** none besides refresh; the page is read-only.
- **Who can see it:** every role. Blocks a role may not see show a *Not available* note.

---

## INTERNET OFFERING

A **service plan** is the technical recipe (speed, devices, data, time). An **internet package** is what a
client is offered: it uses one service plan and adds who gets it and for how long. **Vouchers** are printed
cards that hand out a package. What a guest keeps for a short time after PMS checkout is the **Grace
Period**, under **Hotel**.

### Internet packages — `/internet-packages`
What clients are offered on the portal, and what those packages are doing for clients right now.

- **Tabs:** **Packages** and **Client activity**.
- **Packages tab:** tiles *Offered to clients*, *Disabled*, *Clients on a package now*, *Service plans*; a
  warning when two packages overlap by stay length; filter (All / Active / Disabled) and search. Table:
  Package, Status (*Not configured*, *Active*, *Disabled*), Price, Speed, Data, Time, Devices, Clients now.
  Clicking a package opens a side sheet with what it gives, its saved versions and a support reference.
- **Add package / Edit** (large dialog): Name (what the client sees), Short code (fixed once created),
  Service plan (required, with a summary of what it gives), **How long access lasts**, **Data allowance**
  (the service plan's allowance or an amount per night of the stay, with minimum/maximum), **Who this package is
  offered to** (conditions; empty means everyone who signs in — the condition types are grouped **General**
  and **Hotel (PMS stay)**, the Hotel group holding the conditions that only mean something for a PMS stay:
  nights staying, room type, rate plan, VIP guest, travel agent and which PMS the stay came from), and advanced options (offer from/until,
  speed steps). Saving records a new permanent version; clients already online keep the terms they connected
  under. Packages are free to the client.
- **Disable** ("Stop offering it") — **reason + password confirmation**. Clients stop being offered it at
  once; anyone online keeps their access; it can be enabled again at any time. **Enable** needs no dialog.
- **Delete** (from the side sheet) — first checks whether anything still refers to the package. If so, the
  dialog explains *why it can't be deleted* and offers **Disable instead**. If not, it asks for a **reason
  (at least 4 characters) + password confirmation** and removes the package and its saved versions
  permanently.
- **Client activity tab:** who got access and how — period (including a custom From/To), package, how it was
  given, status and room/reservation search; tiles, a table (When, Package, Client, How it was given,
  Status, …) 25 rows per page, and a detail sheet with *What happened* and *The grant*. Read-only.
- **Who can change it:** Site admin. **Read-only:** Site viewer. Other roles do not see this page.

### Service plans — `/service-plans`
The technical service that packages hand out.

- **Shows:** tiles *Service plans*, *Used by active packages*, *Packages on older settings*, *Not used by any
  package*; filter and search; table Plan, Speed, Devices, Time, Data, Used by. A side sheet shows what the
  service plan grants, the packages using it and its saved versions.
- **Add plan / Edit** (dialog): Plan code (fixed), Display name, Download and Upload speed (Mbps; empty =
  unlimited), Devices at once, **When the device limit is reached** (*Refuse the new device*, *Disconnect
  the oldest device*, *Ask an operator to approve*), Total time allowance (hours or days), Data allowance
  (GB), Disconnect after inactivity (minutes), Maximum single session (hours), **How the speed is shared**
  (per device or shared across the client's devices) and **How time is counted** (currently only *Validity
  window — time runs from purchase*). Saving creates a new version.
- **After saving:** an **"Apply these settings to packages?"** card lists the packages using the service plan with a
  checkbox each; **Apply to selected packages** or **Not now**. Only future clients are affected; nobody's
  access changes mid-session. The same card is offered later for packages still on older settings.
- **Delete** — **reason + password confirmation**; refused, with the reason, while a package still uses the
  service plan.
- **Who can change it:** Site admin. **Read-only:** Site viewer.

### Vouchers — `/vouchers`
Printed cards a client redeems for internet access. Showing or exporting a code needs your password and is
recorded.

- **Header:** **Code format** (for roles that can see it) and **Issue vouchers**.
- **Tiles:** *Available now*, *Used*, *Expired unused*, *Cancelled*, *Issued this week*.
- **Tabs:** **Vouchers**, **Batches**, **Access log** (roles that may read codes) and **Code security**
  (roles that may see the code format).
- **Vouchers tab:** search by the last characters of a code, filter by package, batch and status. The
  table shows the masked code, package, status, validity, batch and issue date. A card's side sheet shows
  its details and history, plus:
  - **Show full code** — **reason + password confirmation**; the view is recorded permanently with your name
    and reason, even for a used or expired card. The code is hidden again when the sheet closes.
  - **Cancel card** — **reason + password confirmation**. Only an unused, unexpired card can be cancelled;
    it stops working immediately and cannot be reinstated.
- **Issue vouchers** (three steps: **Package → Quantity & validity → Review**): 1 to 500 cards, optional
  valid from / valid until, optional note. Issuing does **not** ask for a password. The codes are then shown
  **once** under *"Keep these codes now"* with **Copy all**, **Download CSV** and **Print cards** (a card
  sheet with a heading of up to 60 characters printed on every card; *Wi-Fi Access* when left empty). Getting them back later is an export
  on the Batches tab.
- **Batches tab:** every print run with its counts; a batch sheet with **View cards** and **Export codes**
  (whole batch or unused only) — **reason + password confirmation**, recorded as one entry naming how many
  codes were taken.
- **Access log tab:** who read a code, when and why. Nobody can edit or remove it.
- **Code security tab:** the code format (*Digits only* or *Letters and digits*; length 6, 7 or 8, with a
  sample) and its history. Changing it goes through a review step with a **reason**; it applies to the next
  batch only. **Code keys** (Site admin, Site IT manager): **Retire** a key — **reason + password
  confirmation**, cannot be undone; new batches use a fresh key and printed cards keep working.
- **Who can change it:**
  - Issue and cancel cards: Site admin, Site IT manager, Client services, Client relations, Voucher operator.
  - Show and export codes, Access log: Site admin, Client services, Client relations, Voucher operator (**not**
    the Site IT manager).
  - Change the code format and retire keys: Site admin, Site IT manager.
  - **Read-only** (cards only, never codes): Payments operator, Site viewer.

---

## CLIENTS

### Client accounts — `/guest-accounts`
A username and password a client can sign in with, instead of a room number or voucher. Which package the
client may take is decided by the rules on Internet packages.

- **Shows:** tiles *Accounts*, *Able to sign in*, *Devices online*, *Locked out*; the switch **Offer
  username-and-password sign-in** on the portal; search; table Account, Devices (active of max, *At the
  limit*), Status, Valid until, Last sign-in, Sign-ins.
- **Add account:** Username (what the client types; one character is allowed), Name (staff reference only),
  Password (typed, with show/hide and a soft warning for short passwords, or generated), Valid from, Valid
  until, Notes.
- **Row actions:** **Edit**, **Password** (type or generate a new one; optionally *Disconnect this account's
  devices now*), **Disable / Enable**, **Disconnect** (when devices are online — confirmation dialog),
  **Delete** (confirmation dialog that suggests Disable instead).
- **One-time reveal:** after creating an account or setting a password, *"Password for {username}"* is shown
  once with **Copy** and **I have it**. It cannot be looked up again. No reason or password confirmation is
  asked on this page.
- **Who can change it:** Site admin, Site IT manager, Client services, Client relations, Voucher operator.
  **Read-only:** Site viewer.

### Active sessions — `/sessions`
Which devices are online, whose they are, and disconnecting one. A session is one device; a client may have
several.

- **Shows:** **Online now / Recent** switch; live status, refreshed every 10 seconds while *Online now*;
  tiles *Devices online*, *Clients online*, *Data in this list*, plus *Rooms online* only while someone is
  signed in with a room; search (room, name, username,
  IP, MAC) and a filter by how the client signed in. Rows lead with the client, then how they signed in, the
  internet package, allowance used (data and time meters where the service plan sets a limit), network and device,
  data down/up and status (with the end reason for ended sessions).
- **Detail dialog:** usage, when access ends, package and service plan, network, IP and MAC, and for a room
  sign-in the room and a link to the stay.
- **Disconnect** — a confirmation dialog that names the client and how many of their other devices stay
  online. The client can sign in again.
- **Who can change it:** Site admin, Site IT manager, Client services, Client relations. **Read-only:** Voucher
  operator, Payments operator, Site viewer.

### Usage explorer — `/usage`
Settles data-usage questions by drilling from an access source or a device down to sessions and the
accounting samples behind them.

- **Tabs:** **By access source** and **By device**.
- **By access source:** an access source is whatever granted the access — a **Client account**, a
  **Voucher** or a **Hotel room/stay**. Filter by type (*All*, *Client account*, *Voucher*, *Hotel
  room/stay*) and search by username, card reference, room number or reservation, or leave the search empty
  for the heaviest users; the table shows each source's downloaded, uploaded and total data and its
  allowance. Email, phone and social sign-ins are not listed as sources; the devices that used them still
  appear under **By device**. Room and stay detail appears only for a Hotel room/stay.
- **By device:** look up a MAC address; a note reminds you that a device is not a person.
- **Drill-down:** the source's totals and allowance, the devices used, the sessions, and **Show evidence**
  to load the raw samples, with their own total next to the session's. Read-only.
- **Who can see it:** Site admin, Site IT manager, Client services, Client relations, Payments operator, Site
  viewer.

### Client devices — `/guest-device-self-service`
Whether a signed-in client may remove one of their own devices that is not connected, to free its place for
another, from the portal's *"You're online"* page.

- **Shows:** two tiles kept separate — **This site offers it** (On/Off) and **Available in this
  release** (Yes/Not yet) — and a sentence explaining what clients can do with that combination.
- **Switch on / Switch off** — an inline confirmation with an **optional reason**.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Client services, Client relations,
  Payments operator, Site viewer. May be *Not enabled on this appliance*.

### Online-time budgets — `/online-time`
For packages sold as an amount of connected time: how much time is left. Time counts down only while a
device is connected, but the end date applies regardless.

- **Shows:** tiles *Budgets in use*, *Devices connected on them*, *Ended*; table Time left, of budget, Ends
  on, Devices, State. No client identity is shown. Read-only.
- **Who can see it:** the roles that can see Active sessions. Usually *Not enabled on this appliance*.

---

## CLIENT PORTAL

These pages control what clients see on the sign-in page — see [guest-portal.md](guest-portal.md).

### Sign-in methods — `/sign-in-methods`
How clients prove who they are on the portal. Each switch applies immediately; turning a method off does not
disconnect clients already online.

- **Method cards, each with an on/off switch:** **Voucher code**; **Client account**; **Room sign-in** —
  with a one-line warning when room sign-in is not working and a link, *Room sign-in settings — under Hotel*,
  to the page where what the guest types is chosen (see **Hotel → Room sign-in**); **Email code** and **SMS code** (*Not available* until
  a sender exists and is switched on under Email & SMS); **Social login** (a checkbox per configured
  provider).
- **Client sign-in protection:** *Maximum failed attempts*, *Observation window*, *Wait after too many
  attempts* — each with its default and allowed range — the last change (who, when, from → to), **Save** and
  **Discard**.
- Which methods can be offered is also limited by the license.
- **Who can change it:**
  - Methods: Site admin, Site IT manager. **Read-only:** Client services, Client relations, Site viewer.
  - Protection thresholds: Site admin, Site IT manager. The desk (Client services, Client relations) and Site
    viewer see them read-only; the desk releases single devices on Guest sign-in attempts instead.

### Portal settings — `/portal-branding`
The designer for the client sign-in page, with a live preview. One **Save changes**; clients see the result as
soon as it is saved.

- **Header:** *Unsaved changes* / *All changes saved*, **Discard**, **Save changes**. A strip shows the
  Layout, Languages offered, Custom code and Checks (*N to fix* / *All clear*).
- **Sections** (left rail) with a live preview (Desktop, Tablet, Mobile) beside them:
  - **Template** — six layouts: **Classic** (the default), **Split**, **Immersive**, **Header bar**,
    **Editorial**, **Kiosk**, each with only the options it uses (hero photograph, photo darkening 0–90%,
    sign-in panel position, banner height, panel surface, spacing, heading typeface).
  - **Brand** — logo and background photograph (PNG, JPEG, WebP or GIF up to 8 MB, stored on the appliance;
    SVG is refused), brand colour, button shade, text colour, corner radius, typeface, with contrast
    warnings.
  - **Content** — site name (120 characters), welcome line (280), help line (600), terms of use link.
  - **Sign-in page text** — per-language wording for every built-in string, each marked *Customised* with a
    reset; Arabic fields are right to left.
  - **Languages** — which built-in languages are offered (English is always available) and **Add another
    language**.
  - **Advanced HTML & CSS** — custom CSS and HTML (64 KB each) with live checks and **Use the cleaned
    version**; scripts are refused.
  - **History** — every save, *Clients see this* on the current one, and **Restore** — **password
    confirmation**.
- Saving a change to custom CSS or HTML asks for **password confirmation**; ordinary edits (name, colours,
  text) do not.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Site viewer.

### Allowed sites — `/walled-garden`
Addresses a client device may reach before it has signed in. Keep it to what the sign-in page itself needs.

- **Table:** Type (Domain name, Single address, Address range), Address, Ports (every port if empty), Why,
  Added. **Allow a site** dialog: type, address, ports (comma separated), why it is needed. **Remove** — a
  confirmation dialog.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Client services, Client relations, Site
  viewer.

### Social login — `/social-providers`
Lets clients sign in with an account they already have (Google, Apple, Facebook, Microsoft).

- **Table:** Provider, Client ID, Redirect URI, Last used, Offered. **Add / Edit** dialog: provider (fixed
  once created), name on the portal, Client ID, Client secret (write-only), Redirect URI, scopes, **Offer
  this provider**. **Remove** — a confirmation dialog.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Site viewer.

### Email & SMS — `/notifications`
How the appliance delivers one-time sign-in codes. Without a working sender, the email and SMS code methods
cannot be used.

- **Table:** Sender, Service (*Test only (nothing is sent)*, SendGrid, Amazon SES, Twilio), Sends as,
  Delivery, Offered. **Add / Edit** dialog: channel and service (fixed once created), name, API key
  (write-only), account SID or API user, from address and name (email), **Use this sender**. **Remove** — a
  confirmation dialog.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Site viewer.

---

## HOTEL

Everything whose meaning depends on hospitality or a PMS is grouped here; the rest of the Admin Console is
industry-neutral and speaks of *clients*. Inside Hotel, *guest*, *stay*, *room* and *check-in/checkout* keep
the PMS's own meaning. The section appears only when the appliance serves these screens.

The PMS is the site's reservation system. The appliance keeps a local copy of who is in house (the guest
list) so room sign-in keeps working when the PMS link drops.

### PMS connection — `/pms-interfaces`
(In-page title *PMS connections*.) The links to the PMS: whether guests can sign in with their room number
right now, and where the guest list comes from.

- **Shows:** tiles *Connections*, *Room sign-in* (*Working*, *Partly working*, *Not working*, *Not in use*),
  *Guests in house*, *Last heard from a PMS*. One card per connection with its state, the checks room
  sign-in depends on, guest-list state and warnings such as messages needing a decision. The page refreshes
  every 20 seconds (every 4 seconds while a guest-list refresh runs). An **Advanced diagnostics** section
  links to *Roster reconciliation* and *Unresolved departures*, with a warning when there is something to
  investigate.
- **Add connection** (wizard): **Provider → Connection → Credentials** (only when the provider needs one) **→
  Review**. The result is a saved draft; *"Publish and activate"* are separate steps, and both ask for your
  password.
- **Manage** opens a side sheet with tabs:
  - **Overview** — the checks, guest list and backlog, links to investigate further.
  - **Configuration** — the live version and drafts. **Put live** — **password confirmation** with a reason
    chosen from a list. Also *Connection recovery* numbers with *Currently* and *Change it when* guidance,
    saved with a reason.
  - **Credentials** — whether one is stored; **Store / Replace credential** — **password confirmation**.
    Nothing typed here is shown again.
  - **Client networks** using this connection.
  - **History** — every version, with the option to put an earlier one back (the same **Put live** dialog).
  - **Actions** — **Activate**, **Pause room sign-in**, **Wind down**: each a dialog stating the consequence,
    with a **reason** (from a list) and **password confirmation**. **Test the connection** (reads a sample,
    writes nothing). **Refresh the guest list now** — **password confirmation** and a reason from a list; the
    current list stays in use until the new one is complete, and progress is shown in stages with the
    record count (there is no percentage because the PMS does not say how many records will come).
    Retiring a connection permanently is not offered here.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Client services, Client relations, Site
  viewer.

Two diagnostic pages are reached from **Advanced diagnostics** and are not in the menu; both are read-only
and have no buttons, on purpose:

- **Unresolved departures** — `/pms-reconciliation`: departures that could not be matched to exactly one
  stay, rooms with several stays, and stays past their departure date. The PMS resolves these, not this
  screen. Visible to Site admin, Site IT manager, Client services, Client relations, Site viewer.
- **Roster reconciliation** — `/roster-reconciliation`: the automatic process that keeps the guest list
  identical to the PMS — what blocks it, what the next run will do, and past runs. Visible to Site admin,
  Site IT manager, Client services.

### Room sign-in — `/room-sign-in`
What a guest types besides the room number when signing in with a room. It sits under Hotel because its
meaning depends on the PMS stay record; whether room sign-in is offered at all is still switched on or off in
**Client Portal → Sign-in methods**, which this page links to (*Offered on the Client Portal*, *On* / *Off*).

- **What the guest types** — always the room number plus exactly one detail from the reservation; the
  choice saves as soon as it is picked:
  - **Room number + any one of: first name, surname or reservation number (recommended)** — one box; the
    guest is not asked which detail it is, and the appliance compares the value against all three. If it
    matches more than one guest in that room, sign-in is refused rather than guessed.
  - **Room number + surname**
  - **Room number + first name**
  - **Room number + reservation number**
- A site still on the older *either* setting (surname or reservation number, guessed from what was typed)
  sees a warning that some surnames are rejected; choosing one of the options replaces it.
- A warning appears while room sign-in is offered but not working because the PMS is unavailable. Which PMS
  a guest is checked against is decided by their network, in **PMS routing**.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Client services, Client relations, Site
  viewer.

### Stays — `/stays`
What the PMS reports about who is in house and which internet package each room has. Read-only: stays are
changed in the PMS.

- **Shows:** tiles *In-house stays*, *With an internet package*, *Devices online*, *Arriving*; search (room,
  guest, reservation, package) and a status filter (In house by default). Table: Room, Guest, Stay, Status,
  Internet package (package, speed, devices of the limit), Charges.
- **View** opens the stay: *Internet for this room* (package, service plan, speed, devices online), arrival,
  departure, charges to room, when the PMS last confirmed the stay, occupants, room type, rate plan, travel
  agent and folios.
- If the guest list has arrived and no in-house room has a package, a warning points to Internet packages
  and PMS routing.
- **Who can see it:** Site admin, Site IT manager, Client services, Client relations, Site viewer. No actions.

### PMS activity — `/stay-events`
Every message the PMS sent (check-ins, check-outs, stay changes) and whether the guest list was updated from
it. Answers "has Wi-Fi seen that check-in yet?"

- **Shows:** live status; tiles *Last message*, *Applied*, *Needs a decision*, *Not matched to a stay*;
  search and a filter by result; table About (room and guest), What happened, Result, times, and **Details**
  (including the PMS's own message identifier to quote to the PMS vendor). Read-only.
- **Who can see it:** Site admin, Site IT manager, Client services, Client relations, Site viewer.

### Guest sign-in checks — `/pms-resolutions`
(Formerly *Client sign-in checks*.)
Recent room sign-in checks against the PMS and why they were refused. Deliberately names no guest.

- **Shows:** live status; tiles *Checks recorded*, *Let online*, *Refused*, *Networks involved*; **Why clients
  were refused** (each outcome with what it means and what to do); **By Wi-Fi network**, which calls out the
  pattern where one network fails while others work; **Recent attempts** (newest first, up to 200:
  time, network, result). Read-only.
- **Who can see it:** Site admin, Site IT manager, Site viewer.

### Guest sign-in attempts — `/guest-signin-attempts`
(Formerly *Client sign-in attempts*.)
The desk's "why can't this client get online?" tool, and releasing a device that has been asked to wait
after too many wrong tries.

- **Sign-in attempts tab:** tiles *Attempts*, *Did not connect*, *Details did not match*, *System-side
  failures*; search, room, result, credential type and period (24 hours to 30 days); table When, Room,
  Network, Result, Why, Entered as, Guest-list age, Device (up to 200 rows). **Details** shows the
  diagnostics and — only for roles allowed to see client credentials — *what was entered, and what would have
  been accepted*. Other roles see an explanation instead. Two results come from the licence rather than the
  client: *Licence refused new clients* (the licence did not admit anyone new at that moment) and *Licensed
  capacity full* (the client's details were right, but the appliance was at its licensed number of clients
  online).
- **Active restrictions tab:** devices currently asked to wait, with the last room typed (marked
  unverified), failures and a live countdown. **Release** — **reason required** (at least 3 characters), no
  password; the dialog states that **releasing does not sign the client in**.
- **Who can do what:**
  - See the attempts list: Site admin, Site IT manager, Client services, Client relations, Site viewer.
  - See what the client typed: Site admin, Site IT manager, Client services, Client relations (not Site viewer).
  - Release a restriction: Site admin, Site IT manager, Client services, Client relations. **Read-only:** Site
    viewer.
  - The thresholds themselves are set on **Client Portal → Sign-in methods**.

### Grace Period — `/checkout-grace`
(Formerly *Checkout grace*.) Keeps a guest online for a short, capped time after checkout so leaving the site does not cut them off.

- **Shows:** tiles *Policy in force*, *Published version*, *Last changed*, *Emergency fallback used*; any
  warnings; **what a departing client receives** in plain words (grace time, speeds, data, devices, who
  qualifies); and the policy history (each version opens with who published it, when and why).
- **Edit policy / Create hotel policy** (side sheet, two steps): **Terms** — grace time, download/upload
  speed, data allowance (MB), device handling and limit, stay rules after checkout, with a live *"Client will
  receive…"* sentence — then **Review**: every change shown old → new, a **reason** (chosen from a list) and
  **password confirmation**. Publishing creates a new version.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** Client services, Client relations, Site
  viewer.

### Post-stay access — `/post-stay`
After checkout a guest can reconnect with a PIN for a limited time. A PIN belongs to one stay, never to a
room.

- **Shows:** tiles *Can reconnect now*, *Active, not usable*, *Ended by staff*; table Room, Reservation, Stay,
  State, PIN, Valid until. A row opens its details.
- **Reset PIN** — **reason (at least 4 characters) + password confirmation**, then a **one-time reveal**:
  *"New PIN — shown once"* with **I have given it to the client**. The PIN is not stored in readable form.
- **End access** — **reason + typed `REVOKE` + password confirmation**. Permanent for that stay; no
  replacement PIN is issued.
- **Who can change it:** Site admin, Site IT manager, Client services, Client relations. **Read-only:** Site
  viewer. May be *Not enabled on this appliance*.

**Charges.** Charge health, Manual review, Settlements and Recovery cover posting internet charges to a
guest's room bill in the PMS, and online payments. Selling internet is not switched on today, so these pages
are usually quiet or *Not enabled on this appliance*. Every decision here is an audited statement about real
money.

**Who can change it (all four charge pages):** Site admin, Payments operator. **Read-only:** Site IT manager,
Client services, Site viewer.

### Charge health — `/financial-health`
Whether money is moving and, if not, why: an overall status with reasons, tiles for **PMS posting** (queued,
in flight, held, oldest waiting, unknown outcomes, review queue), **Online payment** (created, pending,
unknown, settlements) and **Configuration**. Action: **Refresh** only.

### Manual review — `/financial-review`
Decide what happened to a room charge whose outcome is unknown. The queue opens into the evidence: what the
charge was attached to, every attempt, decisions already recorded. **Record decision** — what you
established (from a list), why, the evidence source and a reference to it (never the evidence itself), and
**password confirmation**.

### Settlements — `/financial-settlements`
Whether a client was actually charged, and what has been given back. Status filter (Required, In progress,
Settled, Manual review, Failed, Partially reversed, Reversed) and the payment history of each. Read-only;
there is no refund button.

### Recovery — `/financial-recovery`
After a database restore, reconcile the money that was in flight before charging resumes. Nothing here
re-sends anything by itself. When recovery is active: per held item, record what you established (already
completed / never completed / abandon / escalate) with evidence; **Authorize one attempt** for a posting that
must still go out (with a reason and evidence); finally **Release financial recovery** with a note on why it
is safe to resume. Every decision needs **password confirmation** (a password field on the page).

### PMS routing — `/pms-routing`
(Formerly *PMS routing*. In-page title *Which PMS each network checks*.) Which PMS each client network's room sign-ins are checked
against.

- **Shows:** a *Why this matters* note (a network pointed at the wrong PMS produces no error — guests simply
  cannot sign in); **Networks that can offer room sign-in** (network, the connection it is checked against,
  and scope: that one PMS or *Every active PMS*); **Networks with no PMS** (a legitimate setup, because
  vouchers and client accounts do not use the PMS).
- **Change / Point at a PMS** — a dialog to choose the connection and scope. **Remove mapping** — a
  confirmation dialog.
- **Who can change it:** Site admin only. **Read-only:** Site IT manager, Client services, Client relations,
  Site viewer.

### Duplicate sources — `/pms-source-conflicts`
Two PMS connections claiming the same rooms. Until one is given authority, guests in the contested rooms
cannot be verified.

- **Shows:** Connection, Conflicts with, Severity, Resolution. Read-only.
- **Who can see it:** Site admin, Site IT manager, Client services, Client relations, Site viewer.

### Cross-PMS transfer — `/stay-transfers`
Moves a guest's live access from a stay on one PMS to a stay on another (for example a guest moved to the
sister property). Not for normal room moves.

- **Transfer a guest:** From stay, To stay, **Preview** (what will move, or why it cannot), then **Transfer
  access** — **reason (at least 4 characters) + password confirmation**.
- **Also shows:** *Review signals* (ambiguous sign-ins in the last 7 days) and *Recorded transfers*.
- **Who can change it:** Site admin, Site IT manager, Client services, Client relations. **Read-only:** Site
  viewer.

---

## NETWORKING

A wrong network change could cut off the admin or every client, so network changes are staged, validated,
applied, and then must be **confirmed before a countdown ends or they roll back automatically**. The
appliance does not broadcast Wi-Fi: each client network is a VLAN the site's wireless controller maps an
SSID to.

**Who can change it (all Networking pages):** Site admin, Site IT manager. **Read-only:** Site viewer.
Other roles do not see this group.

<a id="guest-networks--network"></a>
### Client networks — `/network`
The Wi-Fi networks clients join, each with its own addresses and sign-in page.

- **Header:** **Validate**, **Apply changes**, **New client network**.
- **Pending banner** after Apply: *"Revision #N — confirm or it rolls back automatically"* with the
  deadline, a live countdown, the validation and health-check results, **Keep this change** and **Roll back
  now**.
- **Table:** Name, SSID label, Type (VLAN n / untagged), Parent interface, Gateway, Subnet, DHCP, Pool,
  Portal (*Sign-in page* / *Open*), Status, Clients. **Edit** opens the network; **Disable** (enabled
  networks) and **Delete** (disabled networks) are confirmation dialogs that explain nothing reaches clients
  until the change is applied.
- Applying, keeping and rolling back client-network changes do **not** ask for a password.

**New client network** — `/network/new` (seven steps): **Identity → Interface / VLAN → Subnet & gateway →
DHCP & DNS → Captive portal → Review** (with a *"Wireless controller action required"* reminder to map the
SSID to the VLAN) **→ Apply** (*"Create, validate & apply"*, then the pending banner). A role that cannot
change networks sees *"You cannot create client networks"*.

**Client network detail** — `/network/{id}`: the topology (type, VLAN, parent, bridge) is read-only and
cannot change after creation; name, SSID label, addresses, pools, DNS, lease times and portal/internet/NAT/
isolation settings are editable (*"Saved — not applied yet"* until applied on Client networks). Also the
network's **DHCP reservations** (add, edit, remove — each in a dialog).

### DHCP & leases — `/network/dhcp`
Which client devices hold an address now, and which always get the same one. Tabs **Active leases** and
**Reservations** (across all networks): **New reservation**, edit, and **Remove** — a confirmation dialog.

### WAN / LAN settings — `/network/system`
The appliance's own internet uplink and management address. Client Wi-Fi is configured on Client networks, not
here.

- **Shows:** the **WAN / Management** card (interface, MAC, link, IP, gateway, DNS, management URL,
  connectivity, a warning when the running address differs from the saved one), a pointer to Client
  networks, and a collapsed **Advanced · Base LAN / Legacy bridge** card.
- **Change configuration:** WAN IP, prefix, gateway, DNS; base LAN gateway and prefix. **Validate &
  preview** shows before → after and the new management URL. **Apply change…** — **password
  confirmation**. A warning explains that changing the WAN IP changes the admin address and you must
  reconnect there to confirm.
- **Pending banner:** *"Change applied — confirmation required"*, rolling back automatically in N seconds;
  **Keep this configuration** or **Roll back now** (rolling back also asks for **password
  confirmation**).
- **Also:** **Run diagnostics** with **Download report**, and the change history.

### Config history — `/network/revisions`
Every network validate and apply ever made: number, state (active, pending confirmation, rolled back,
failed), summary, applied, confirmed, failure. Filter *Needs attention* / all. A revision opens with its
validation issues, apply events and health checks; a pending one can be kept or rolled back here too.
Nothing is ever deleted.

### TLS certificate — `/network/certificate`
The HTTPS certificate the Admin Console itself is served with. Renewal is automatic.

- **Shows:** a status badge (Healthy, Renewal due, Warning, Critical, Expired, with days left), the
  certificate details, and any renewal error or configuration mismatch.
- **Check certificate**; **Rotate** — **reason + typed `ROTATE` + password confirmation**. A new certificate
  is issued; you cannot upload a key.

---

## SYSTEM

### Diagnostics — `/health`
Whether each service on the appliance is running.

- **Shows:** live status (refreshed every 10 seconds), a *"still starting after boot"* banner when relevant,
  count tiles by state, and the services table (state, health check, restarts, backoff, last failure,
  uptime). A service opens a panel with details, recent logs (secrets and guest details removed) and
  recovery history.
- **Actions:** **Recheck**; **Logs**; **Restart** — a dialog describing the impact for that service,
  **reason + password confirmation**.
- **Who can change it:** Site admin, Site IT manager. **Read-only:** every other role.

### Alerts — `/operational-alerts`
Checkouts the configured policy could not handle on its own (for example when an emergency grace was used).
Table: alert, state, trigger, reason, boundary time, raised. **Acknowledge** and **Resolve**. Empty state:
*"No open alerts"*.

- **Who can change it:** Site admin, Site IT manager, Client services, Client relations. **Read-only:** Site
  viewer.

### Activity — `/audit`
Every change made to this appliance, by staff and by the system itself, written as it happens and never
edited or removed.

- **Filters:** search, period (last 24 hours, 7 days, 30 days, everything), and chips **Everything**,
  **Security** (with a count), and categories *Sign-in & access, Client Portal, Internet offering, Property
  management system, Networks, Licence & cloud, Backups, Diagnostics*.
- **List:** a plain-language title, category and security badges, who, when and from which address; expand
  for the recorded details. Up to 500 entries.
- **Who can see it:** Site admin, Site IT manager, Client services, Client relations, Payments operator, Site
  viewer.

### Appliance & licence — `/appliance`
Whether this appliance is activated, what its licence allows, and whether OneGate Central is reachable. One
page, fed by one appliance status (contract: [CENTRAL_CONTROL_PLANE.md §8](../CENTRAL_CONTROL_PLANE.md#8-admin-console--central)).
It refreshes by itself (every 15 seconds, every 5 while activating) and has **Check now**, which makes the
appliance register if needed and fetch its activation and licence from Central immediately.

- **Problems, in words** — banners for a licence in its grace period; expired, suspended or revoked (*new client
  sign-ins are refused; clients already online are not disconnected; the sign-in page, DHCP, DNS and this admin
  keep working*); a licence for a different appliance; a changed WAN network adapter; licensed capacity
  reached; an activation the appliance cannot verify; a blocked attempt to switch off licence enforcement.
- **Status card**, three parts:
  - **Activation** — *Not registered yet* (Central not reached yet; it keeps retrying by itself), *Waiting for
    activation* (with the copyable **Serial number** to give your OneGate vendor; nothing needs to be typed
    here), *Finishing activation…*, *Activated* (licensed to customer · site), *Retired*, or *Removed from
    OneGate Central* — Central deleted this appliance after it had served a customer: it keeps its data, admits
    no new clients (clients already online are not disconnected), refuses licence and activation files and never
    registers again by itself. To use it again, it must be factory-reset and activated by your OneGate vendor.
  - **Licence** — *No licence yet*, *Active*, *Expires soon*, *Grace period*, *Expired*, *Suspended*, *Revoked*
    or *Wrong appliance*, with the end date and days left, and **Clients online, all client networks** against
    the licensed maximum with a meter.
  - **OneGate Central** — *Connected*, *Temporarily unreachable* or *Not configured*, **Last answered** and
    **Last problem**. Losing Central changes only this part; the licence keeps being checked on the appliance
    and clients are unaffected.
- **Files from your OneGate vendor** — shown for an appliance that is not activated or cannot reach Central:
  **Offline activation** (**Download activation request** while the appliance has never reached Central, then
  **Upload activation package**) and **Upload licence file** (renewals normally arrive by themselves; the
  appliance refuses a file for another appliance or an older licence).
- **Technical details** (collapsed) — serial, appliance ID, identity key fingerprint, client certificate,
  assignment version and verification, licence version, WAN and LAN MAC, Central endpoint, software version.
- **Who can change it** (upload files): Site admin, Site IT manager. **Read-only:** every other role.
- Old addresses `/license`, `/network/cloud` and `/setup/enrollment` redirect here. There is no enrollment
  token or code: an appliance registers itself.

### Backups — `/backups`
A complete copy of the site's data, taken nightly and on demand.

- **Recovery readiness:** whether the site can be recovered from its latest backup; **Verify it**; **Back
  up now** — **password confirmation**.
- **Available backups:** date, Verified / Not checked, size; **Verify** (or Re-verify), **Download**, and
  **Restore** (verified backups only).
- **Restore** — a dialog listing what is replaced and the six steps the appliance takes, **typed backup name +
  password confirmation**. Progress is shown until it finishes, then a *Last restore* card.
- **Storage and retention** (collapsed): disk used, backups kept, nightly sweep time and how long things are
  kept; changing them needs **password confirmation**.
- **Who can change it:** Site admin. **Read-only:** Site IT manager, Client services, Client relations, Site
  viewer. (Download is offered to every role that can see the page.)

### Operators — `/operators`
Site staff accounts for this appliance.

- **Table:** operator (with a *you* marker), role badges (remove with ×; add with **+ role**), status (*Can
  sign in* / *Disabled*). **Change password** (typed twice, at least 10 characters) and **Disable**.
  Adding a role, removing a role and disabling an operator are each confirmed in a dialog first.
- **Add operator:** email or username, name, password (at least 10 characters), role (the seven roles;
  *Site viewer* by default).
- You cannot remove your own Site admin role or disable yourself. There is no action to re-enable a disabled
  operator.
- **Who can change it:** Site admin. **Read-only:** Site IT manager.

---

## Who can use which page

W = can change, R = read-only, — = not shown. The appliance enforces these on every request.

| Page | Site admin | Site IT manager | Client services | Client relations | Voucher operator | Payments operator | Site viewer |
|---|---|---|---|---|---|---|---|
| Overview | R | R | R | R | R | R | R |
| Internet packages, Service plans | W | — | — | — | — | — | R |
| Vouchers (cards) | W | W | W | W | W | R | R |
| Vouchers (show / export codes) | W | — | W | W | W | — | — |
| Vouchers (code format, keys) | W | W | R | R | R | — | R |
| Client accounts | W | W | W | W | W | — | R |
| Active sessions | W | W | W | W | R | R | R |
| Usage explorer | R | R | R | R | — | R | R |
| Client devices | W | W | R | R | — | R | R |
| Online-time budgets | R | R | R | R | R | R | R |
| Sign-in methods | W | W | R | R | — | — | R |
| Sign-in protection thresholds | W | W | R | R | — | — | R |
| Portal settings, Social login, Email & SMS | W | W | — | — | — | — | R |
| Allowed sites | W | W | R | R | — | — | R |
| PMS connection | W | W | R | R | — | — | R |
| Room sign-in | W | W | R | R | — | — | R |
| Stays | R | R | R | R | — | — | R |
| PMS activity, Duplicate sources | R | R | R | R | — | — | R |
| Guest sign-in checks | R | R | — | — | — | — | R |
| Guest sign-in attempts (list) | R | R | R | R | — | — | R |
| Guest sign-in attempts (what was typed) | R | R | R | R | — | — | — |
| Guest sign-in attempts (release) | W | W | W | W | — | — | R |
| Grace Period | W | W | R | R | — | — | R |
| Post-stay access | W | W | W | W | — | — | R |
| Charge health, Manual review, Settlements, Recovery | W | R | R | — | — | W | R |
| PMS routing | W | R | R | R | — | — | R |
| Cross-PMS transfer | W | W | W | W | — | — | R |
| Networking (all pages) | W | W | — | — | — | — | R |
| Diagnostics | W | W | R | R | R | R | R |
| Alerts | W | W | W | W | — | — | R |
| Activity | R | R | R | R | — | R | R |
| Appliance & licence | W | W | R | R | R | R | R |
| Backups | W | R | R | R | — | — | R |
| Operators | W | R | — | — | — | — | — |

---

## Actions that need more than a click

| Action | Page | What is asked |
|---|---|---|
| Disable an internet package | Internet packages | Reason + password |
| Delete a package or service plan | Internet packages, Service plans | Reason + password (refused while in use) |
| Publish a Grace Period policy | Grace Period | Reason (from a list) + password |
| Show a voucher code / export a batch | Vouchers | Reason + password, recorded |
| Cancel a voucher card | Vouchers | Reason + password |
| Change the voucher code format | Vouchers → Code security | Reason |
| Retire a code key | Vouchers → Code security | Reason + password |
| Issue vouchers | Vouchers | One-time reveal of the codes (no password) |
| Create an account / set a password | Client accounts | One-time reveal of the password |
| Reset a post-stay PIN | Post-stay access | Reason + password, then one-time reveal |
| End post-stay access | Post-stay access | Reason + type `REVOKE` + password |
| Put a PMS configuration live | PMS connection | Reason (from a list) + password |
| Store or replace a PMS credential | PMS connection | Password |
| Activate / pause / wind down a PMS connection | PMS connection | Reason (from a list) + password |
| Refresh the guest list | PMS connection | Reason (from a list) + password |
| Release a sign-in restriction | Guest sign-in attempts | Reason (no password) |
| Transfer access between PMSs | Cross-PMS transfer | Reason + password |
| Record a charge decision / recovery decision | Manual review, Recovery | Password (plus the decision's own fields) |
| Save custom CSS or HTML / restore a portal save | Portal settings | Password |
| Apply a client-network change | Client networks | Apply → confirm → automatic rollback (no password) |
| Apply or roll back a WAN / LAN change | WAN / LAN settings | Password; apply → confirm → automatic rollback |
| Rotate the TLS certificate | TLS certificate | Reason + type `ROTATE` + password |
| Restart a service | Diagnostics | Reason + password |
| Back up now / change retention | Backups | Password |
| Restore a backup | Backups | Type the backup name + password |
