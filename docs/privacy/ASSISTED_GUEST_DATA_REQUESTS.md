# Assisted guest data requests

*Added 2026-10-20 (migration 0146). Authoritative for what the assisted workflow does and does not do. The automatic
workflow is described in [DATA_OWNERSHIP.md](../DATA_OWNERSHIP.md) section 8; this document adds the supervised
path for the records that workflow leaves alone. These are engineering notes for the hotel's legal review (plan
D-F3), not legal advice, and nothing here says that any hotel is compliant.*

## 1. Why it exists

Migration 0145 (decision D-R5) stopped the automatic export and erasure from reaching internet access that several
people share: the credentials of a room, a voucher, a staff-issued account, or a device used with someone else. The
PMS may name only the reservation holder while several occupants use the room, so **a device address, a room number,
a reservation credential or shared internet access does not show who a person is or what is theirs.**

That left a person with a real right (GDPR Art. 15 and 17) and no way to exercise it for the usage of their own
phone. The assisted request is that way. It is **never automatic**: a member of staff verifies the requester,
states what they saw, reviews the exact records and confirms. The database decides what a record is and refuses
anything that may belong to someone else.

## 2. What it never does

- It never identifies a person from a MAC address, a room number, a reservation credential or shared access.
- It never selects, discloses or changes a record that may belong to another person, even when several guests used
  the same room, device, voucher or account. Those records are shown to staff with the reason and can not be
  selected.
- It never changes an entitlement, a quota, an accounting total, a reservation, PMS data, a financial record or a
  running session. Erasing clears addresses only (see section 6).
- It never reaches another site or tenant, or another PMS interface's reservation unless the stay link says it is
  the same stay continued.
- It never runs without the operator's password, a reason, the request reference typed to confirm, and the review
  signature the operator was shown.
- It is not a retention mechanism: **retention expiry is not a response to a rights request** and this workflow
  does not replace the hotel's own answer to a request it cannot attribute.

## 3. The workflow (Hotel Admin → System → Guest data requests → Assisted request)

Only `site_admin` and the IT manager (the `guest-data-rights` key) reach it.

1. **Verify the requester** yourself. Choose how (identity document in person, control of the account shown, call
   back on a contact the hotel already holds, other) and describe the evidence in a note. Do not copy document
   numbers.
2. **Open the request.** Choose the purpose (hand over a copy, or anonymise), name the reservation, guest, account
   or sign-in identity the records are said to belong to (a device address alone is refused; a returning guest
   names every reservation the device was used under), give one to five device addresses and a range of at most 366
   days that ends now or earlier, and make the statements:
   - *I have verified who the requester is* (required to open);
   - *the requester showed me the device and its address*;
   - *the requester states that nobody else used this device in this range*;
   - *this private (randomised) address is what the device shows for this network* (only for a private address).
   Without the second and third statement, access shared by nature stays unselectable.
3. **Review.** The database lists every record of those addresses in the range, with its class, the reason, and
   (for the ones that can be selected) exactly what each would disclose.
4. **Select and confirm.** Select records (only selectable ones can be ticked). Hand-over: download, then **close**
   the request as completed. Anonymisation: confirm and it runs once and closes. Either way you are shown the
   hotel's request file once; save it with your records.
5. A request left open for **30 days** is cancelled and scrubbed by the hourly staff-retention run: the one-month
   answer period (Art. 12(3)) has passed and the file should not hold an address for ever.

## 4. How a record is classified

| Class | Meaning | Selectable |
|---|---|---|
| **ATTRIBUTABLE** | Already this person's by migration 0145's rules: made with their own self-registered account or sign-in identity, by a one-time code sent to their own address, or on a device only they used | yes |
| **CLAIMABLE** | The access of the stated reservation (one that names **one** guest) or staff-issued account, on an address that **no other reservation, voucher, account or person has ever used**, and all of the statements made | yes |
| **SHARED** | A voucher or open access; the room of a reservation that names **more than one guest** (the PMS itself says identified people share the credentials, and a statement cannot say whose phone is whose); or the device/sole-use statement (or the private-address confirmation) is missing | no |
| **CONFLICTING** | The same address was also used under another reservation, voucher or account, or another person's identity, remembered device, created account or verified address is on it; or the record belongs to another reservation or account | no |
| **UNVERIFIABLE** | Not entirely inside the range (it began earlier, ends later, or is still running); or nothing says whose it is; or it is keyed only by the address (a failed attempt, a restriction) and nothing on the address ties it to the reservation or account the requester named | no |

Rules worth knowing:

- **Whole history of the address counts, across every device row it has.** If the phone was also used under a second
  reservation, nothing the room gave it is claimable until the second reservation is also stated (a returning
  guest) — an address that is also used with someone else is not established as one person's. "Used" includes a
  session, an authorisation (even one since released), a slot action by staff or the guest, a sign-in, a remembered
  device, an account created on it, a verified address, and a sign-in attempt the PMS matched to a guest other than
  the one stated.
- **Time ranges are the lever for the rest.** A record that straddles the range is left alone. A session that
  ended less than a day ago, and anything still in use, can be handed over but not anonymised (section 6).
- **Room moves** across PMS interfaces follow the stay links the automatic workflow follows, and stop at a stay with
  more than one named guest. Anchors from two PMS interfaces are two requests.
- **MAC randomisation.** A private address names a network profile, not a person; staff must confirm what the
  device shows. A phone that rotates its address is claimed address by address; the system never joins them.
- **Overlapping stays and shared devices** are the conflicts above: an overlap on one address makes the address
  unclaimable.
- An **account** the person registered themselves is theirs by its own credential; a **staff-issued account** (for
  example a conference account) is a shared credential and is claimed phone by phone on the statements.

## 5. What a hand-over contains

`stayconnect.assisted-guest-data-export/1`: the request's reference, purpose, range, the requester's name or
reference, how they were verified and the statements; and the selected records only — sessions (with usage
samples), sign-in attempts, verification access, one-time code bindings, restrictions, passes, social sign-in
redirects, and a device's identity row (with the networks it was seen on; what staff did with the device stays in
the activity log). It does **not** contain the staff member's name or the evidence note: those stay in the hotel's
file, returned when the request is closed. It says how many records of these addresses were not included and why,
without saying who else used them.

## 6. What an anonymisation does

The same primitives retention uses, applied to the selected records only:

| Record | Change |
|---|---|
| ended session | IP and MAC cleared (usage totals and accounting samples stay) |
| sign-in attempt | typed room, IP, MAC and sealed credential cleared |
| verification access (ended) / one-time code binding | MAC replaced by a random placeholder; IP cleared |
| sign-in restriction / pass | MAC replaced; typed room cleared |
| social sign-in redirect | IP, MAC, browser cleared |
| device identity | anonymised like an exclusive device — **only** when every record the device carries is inside the request and selected; otherwise the device row stays (it still carries other records) |

The activity log keeps who and when (decision D-R2); a device address that was anonymised is replaced wherever the
log carries it. A record is refused while it is **live** (a live entitlement, session, restriction or verification
access) or **recent** (under a day old: the sign-in protection may still count it). Reading is never blocked by
recency.

## 7. Authorization, audit and the request file

- The permission is the existing `guest-data-rights` key. Hand-over and anonymisation need the operator's password,
  a reason that names nobody (checked against the stated reservation/guest/account, and refused if it contains a
  device address, an email or a phone number), the request reference typed, and the review signature.
- Activity entries: `guest_data.assisted_opened`, `_reviewed`, `_export_started` (written *before* anything is
  prepared; a hand-over that cannot be recorded is not made), `_export_refused`, `_erased`, `_erase_refused`,
  `_completed`, `_cancelled`. They name the request, never an address or a person.
- The request file holds the device addresses, the requester's reference and the evidence note **only while the
  request is open**. Closing it (completing, cancelling, erasing, or the 30-day expiry) replaces them with a marker
  and removes the links to the person's records (the stated reservation, guest or account). What stays: who, when,
  how it was verified, the four statements, the reason, counts, and — for an anonymisation only — the ids of the
  records anonymised (they identify nobody now; the ids of records that were only handed over are removed, because
  those records are still identifiable). Events are append-only and the table refuses edits to what was stated.
- **Opening and reviewing a request need no password.** They show staff the records that the Sessions and
  Usage screens already show the same two roles; each is recorded in the activity log. The password is asked before
  anything leaves the database or changes.
- The erasure register lists an assisted erasure (kind `assisted`) with its `ER-…` reference.

## 8. Concurrency and consistency

- The review signature covers every record, its class and reason, and — for the records that can be selected — the
  content that would be disclosed. If anything changes before the operator confirms (a new session on the address,
  an automatic erasure, retention, a record edited), the confirmation is refused with *changed* and the screen
  reloads; nothing is processed.
- Confirmation locks the request, then the address (an advisory lock taken only by assisted requests, so two of them
  on an address that has no device row still run one after the other) and the device rows, recomputes the review under
  the locks and compares the signature. Two confirmations of one request, or two requests on one address, run once;
  the other is refused. A session that begins meanwhile waits for the device row; other records keyed by the address
  (attempts, grants, restrictions) are not locked out, and a change in them shows in the signature.
- A device is selected only together with every record it carries.

## 9. Known limits and residual risks

1. **Sole use rests on staff's statement.** The database can show that no other reservation, voucher, account or
   identity ever used an address; it can not show that two people in one room did not share one phone. Disclosing a
   device's usage record to the person who showed the device is the risk the hotel accepts by making the
   statement. The statement is recorded with the request.
2. A person who used only an address the hotel never saw under any access leaves no record to claim.
3. Shared access that conflicts or is a voucher stays the hotel's manual and legal responsibility, exactly as in
   DATA_OWNERSHIP.md section 8. The assisted request does not widen what may be disclosed about other people.
4. A partly processed device keeps its identity row; its records outside the range are other records.
5. One-time codes, client accounts and sign-in identities themselves, the PMS reservation and the folio are the
   **automatic** request's; this workflow does not touch them.
6. A restore from a backup taken before an anonymisation brings the records back. Repeat the request.
7. Two operators approving each other's work (four eyes) is not enforced; whether to require it is a product
   decision (plan section 4).
8. The 30-day expiry and the 2,000-record and 366-day bounds are fixed request limits, not hotel settings.
9. Not covered by an assisted anonymisation: DHCP reservations and hostnames, PMS connection attempts (`pms_attempts`),
   the IP and browser kept with one-time codes, and DHCP data outside the database. Retention covers some of them.
   An account the person registered on an anonymised device loses that address; nothing else is rewritten.
10. A session that is admitted at the instant a device is anonymised can attach to the anonymised row with the real
    address; the next review shows it, and a repeat request clears it. The erasure holds a device-row lock for its
    duration, which briefly queues new admissions of that device.
11. A session's earlier binding to another entitlement (`session_entitlement_bindings`) is not read; production rebinds only
    at checkout within one stay. Up to five contexts can be stated in one request; they should be one person's, and the
    statement that they are is the member of staff's.
12. Erasing a device invalidates an active remembered-device credential of the person's identity; a request that
    names the identity should be made when that is acceptable.
13. The activity log is cleaned of the anonymised devices' addresses (any spelling), not of an address of a device
    that stayed. The automatic erasure still cleans only the colon spelling; that is outside this change.

## 10. Operations

- Migration `0146_staff_can_assist_a_request_about_records_no_rule_can_attribute` is additive and reversible. Its
  `down` cancels and scrubs open requests, restores the staff-retention function, revokes its column grants and
  drops the functions; the request tables stay (proof that a request ran, with no address or text once closed).
- Grants are mirrored in `deploy/gatep/svc-guest-data-rights-grants.sql`. `svc_edged` holds no table privilege on
  the request tables; it reaches them only through the definer functions.
- Tests: `guest_data_assisted_integration_test.go`, `guest_data_assisted_adversarial_integration_test.go` and
  `guest_data_assisted_review_integration_test.go` (the regressions of the independent review) (edged, `-tags integration`); `guest-data-assisted-page.test.tsx` and `guest-data-assisted-words.test.ts`
  (Hotel Admin).
