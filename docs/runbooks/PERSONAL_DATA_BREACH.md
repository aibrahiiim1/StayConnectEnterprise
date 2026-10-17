# Personal-data breach response

GDPR remediation plan item **F2** (Art. 28(3)(f), 33 and 34). An engineering draft: the legal reviewer named
under plan decision D-F3 signs it off before a customer relies on it.

> **Still to complete (plan decision D-F2):** the values marked **TBD** — our breach contact and the number of
> hours within which we tell a hotel. Until they are filled in, this runbook is not given to a customer.

## 1. Where the guest data is, and when we are involved

All guest data stays on the hotel's own appliance. **We have no standing access to it.** Checked in the code
(2026-10-17):

- the appliance's link to OneGate Central carries identity, activation, licensing and the portal certificate
  only; there is no remote command, shell or data channel (CLAUDE.md §0E);
- installation creates no vendor login: no SSH key, support account, tunnel or VPN; the break-glass procedures
  (docs/VENDOR_BREAKGLASS_RUNBOOK.md) all require root on the appliance, which the hotel has to give us;
- the email, SMS, WhatsApp, identity-provider and payment accounts are the hotel's own.

So the hotel is the **controller** and normally handles a breach on its appliance itself. We are involved in
three cases only:

| Case | Our role | What we owe the hotel |
|---|---|---|
| **A. A support session the hotel opened** (it gave us SSH, root or a Hotel Admin login) | processor, for that session | Tell the hotel **within TBD hours** of becoming aware of a breach caused during or through the session (Art. 33(2)) |
| **B. Files the hotel sent us** (logs, screenshots, exports, backups) | processor, for those files | The same, if those files are lost, exposed or reach the wrong person |
| **C. A security flaw in our software** that exposes, or could expose, guest data | supplier | Tell **every affected hotel within TBD hours** of confirming the flaw, with what is at risk and what to do |

In every other case (a hotel employee misusing their access, a stolen appliance, a breach of the hotel's own
accounts) the hotel decides and reports. We help when it asks.

**Remember: the hotel's 72 hours start when the hotel becomes aware**, which in cases A to C is when we tell it.
Every hour we take comes out of the hotel's time.

**Our breach contact: TBD** (name, role, phone and email, plus a deputy). Anyone who suspects a case A, B or C
breach calls them first, at any hour, and does not try to settle it alone.

## 2. Keeping cases A and B small

- **Support access is opened by the hotel, for one purpose, and closed afterwards.** Record in the ticket who
  had access, when, and why (as the break-glass runbook already requires). Ask the hotel to remove the login or
  key when the case is done.
- **Hotels send logs, so logs reach us.** Logs from this system can contain device MAC and IP addresses and
  sign-in identifiers. Ask for the smallest extract that answers the question.
- **Files from a hotel are kept only in the support case**, with access limited to the people working on it.
  They are never copied into tickets elsewhere, chat, commits, pull requests or delivery evidence (CLAUDE.md
  §0B). Delete them when the case is closed.

## 3. The first hours (cases A to C)

1. **Write down the time you became aware**, and start the timeline (section 5).
2. **Call the breach contact.** They lead from here on.
3. **Contain it, without destroying evidence:**
   - case A: ask the hotel to remove our access, or remove it ourselves if the hotel agrees; rotate any
     credential of ours that was exposed;
   - case B: take the files back, delete the copies, or recover the device they were on;
   - case C: work out a fix or a workaround the hotel can apply.

   Do not wipe, reinstall or restore anything, and do not delete logs, before the breach contact agrees. Anything
   that changes a hotel's appliance still needs the hotel's agreement and, for this repository's controlled
   actions, the Product Owner's authorisation (CLAUDE.md §12).
4. **Tell the hotel** within the promised hours, even if little is known yet; the details can follow in stages
   (Art. 33(4)):
   - what happened;
   - which kinds of data, and roughly how many guests;
   - what has been done to contain it;
   - the likely consequences;
   - who to contact (our breach contact);
   - when the next update will come.
5. **Help the hotel decide** whether to tell the authority (within its 72 hours) and the guests. The appliance
   can find and export one guest's records (Hotel Admin → Guest data requests, plan C4), which helps answer
   "whose data, and what exactly".

## 4. Helping a hotel with its own breach

When a hotel asks for help, point it to what the appliance records:

| Where | What to look for |
|---|---|
| Hotel Admin → **Activity** (the audit log) | sign-ins and changes by unexpected staff accounts or at odd hours; backup downloads; guest-data exports and erasures; changes to roles, backup key holders, retention settings or the portal design |
| Hotel Admin → Backups → **Backup encryption** | a key holder whose key may be lost or exposed: remove it and add a new one (docs/BACKUP_AND_RESTORE.md) |
| Guest sign-in attempts | a burst of failures across many rooms or codes (guessing) |
| The appliance itself | unexpected services, accounts, SSH keys or outbound connections |

Helping this way does not make it a case A breach, unless the hotel gives us access to do it. If it does, it is a
support session, and section 2 applies.

## 5. The record

Keep one record per case A, B or C incident, **whether or not it was reported**. It holds:

- the timeline: when it happened, when we became aware, when we told the hotel, and each later update;
- what happened and its cause;
- the data and the people affected (categories and approximate numbers; no records copied in);
- the containment and the fixes;
- what the hotel decided about telling the authority and the guests;
- follow-up actions and who owns them.

## 6. Afterwards

- Fix the cause, and check that the fix works.
- Review this runbook; add what would have caught the breach sooner.
- Raise anything that needs a product or architecture decision with the Product Owner as a decision, not as a
  side fix.
