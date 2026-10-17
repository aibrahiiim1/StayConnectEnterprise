# Personal-data breach response

GDPR remediation plan item **F2** (Art. 33 and 34). An engineering draft: the legal reviewer named under plan
decision D-F3 signs it off before a customer relies on it.

> **Still to complete (plan decision D-F2):** the two values marked **TBD** below — our breach contact, and the
> number of hours within which we promise to tell the hotel. Until they are filled in, this runbook must not be
> given to a customer.

## 1. Who is responsible for what

| | The hotel | We (the system supplier) |
|---|---|---|
| GDPR role | **Controller** of its guests' data | **Processor**, acting on the hotel's instructions |
| Tells the supervisory authority | Yes, within **72 hours** of becoming aware, unless the breach is unlikely to put anyone at risk (Art. 33) | No; we tell the hotel |
| Tells the guests | Yes, without undue delay, when the risk to them is high (Art. 34) | No; we help the hotel to |
| Tells the other party | — | The hotel, **without undue delay**, and in any case within **TBD hours** of becoming aware (Art. 33(2)) |

The hotel's 72 hours start when **the hotel** becomes aware, which in practice is when we tell it. Every hour
we take comes out of the hotel's time.

**Our breach contact: TBD** (name, role, phone and email, plus a deputy). Anyone who suspects a breach calls them
first, at any hour, and does not try to settle it alone.

## 2. What counts as a breach

A breach is any security failure that leads to personal data being destroyed, lost, changed, disclosed or
accessed when it should not have been. It is a breach even if nobody outside has seen the data yet, and even if
it was an accident. Examples on this system:

- someone without the right gets into Hotel Admin, the appliance or its database;
- a backup or a guest-data export ends up somewhere it should not (an email, a USB stick, a shared drive);
- a backup key holder's key is lost or exposed (encrypted backups, plan C5);
- guest data is written somewhere it should not be: a log, a ticket, a commit, a pull request;
- a credential or key is exposed (as with the Gmail app password, plan C12);
- data is erased or anonymised by mistake, or a restore brings back data that was meant to be gone;
- the appliance is stolen, or its disks leave the hotel without being wiped.

When in doubt, treat it as a breach and call the breach contact. Deciding it was not one comes later, and is
recorded (section 6).

## 3. Where a breach shows up

| Source | What to look for |
|---|---|
| Hotel Admin **Activity** (the audit log) | sign-ins and changes by unexpected staff accounts or at odd hours; backup downloads; guest-data exports and erasures (plan C3 and C4); changes to roles, backup key holders, data-retention settings or the portal design |
| Repeated password confirmations (step-up) | refused or throttled step-ups before a sensitive action |
| Guest sign-in attempts | a burst of failures across many rooms or codes (guessing) |
| The appliance | unexpected services, accounts, SSH keys or outbound connections; full disks; missing backups |
| People | a guest, a hotel employee or a third party reporting something; a lost laptop, phone or printed key sheet |
| Our own repositories and tickets | guest data or secrets in a commit, pull request, issue or chat |

## 4. The first hours

1. **Write down the time you became aware**, and keep a timeline from that moment (section 6).
2. **Call the breach contact.** They lead from here on and decide whether the hotel must be told now.
3. **Contain it, without destroying evidence.** Typical steps:
   - disable the staff account involved, or change its password;
   - remove an exposed backup key holder and add a new one (docs/BACKUP_AND_RESTORE.md, encryption section);
   - rotate an exposed credential or key (docs/SECURITY_HARDENING.md; Root CA matters follow
     docs/CA_CEREMONY_RUNBOOK.md);
   - take the copy offline: delete it from the mailbox or share, or recover the device.

   Do **not** wipe, reinstall or restore the appliance, and do not delete logs, before the breach contact agrees.
   That would destroy the evidence of what was taken. Anything that changes the appliance's configuration,
   network, PMS link or data still needs the Product Owner's authorisation (CLAUDE.md §12); a breach does not
   suspend that rule, it makes it more important.
4. **Tell the hotel** within the promised time (section 1), even if you know little yet. Article 33(4) allows
   the details to follow in stages. Say:
   - what happened, as far as known;
   - which kinds of data and roughly how many guests;
   - what has been done to contain it;
   - the likely consequences;
   - who to contact (our breach contact);
   - when the next update will come.
5. **Help the hotel decide** whether to tell the authority (within its 72 hours) and the guests. This system can
   find and export one guest's records (plan C4), which helps answer "whose data, and what exactly".

## 5. Evidence

- Keep the audit log entries, appliance logs and any copies involved **as they are**. Export them to a place
  with restricted access. Note who took each copy, and when.
- **Guest data never goes into tickets, chat, commits, pull requests or delivery evidence** (CLAUDE.md §0B).
  Describe what was observed; do not paste the records. If a record must be shown to the hotel, send it over the
  channel the hotel chooses, and only to the people who need it.
- A copy of evidence that contains guest data is itself personal data. Delete it when the case is closed and
  the retention the hotel decides has passed.

## 6. The record

Keep one record per incident, **whether or not it was reported**. Article 33(5) requires the hotel to keep one,
and ours supports it. It holds:

- the timeline: when the breach happened, when we became aware, when we told the hotel, and each later update;
- what happened and its cause;
- the data and the people affected (categories and approximate numbers; no records copied in);
- the containment and the fixes;
- the decision on telling the authority and the guests, who took it, and why;
- follow-up actions and who owns them.

## 7. After the incident

- Fix the cause, and check that the fix works.
- Review this runbook and the detection sources in section 3; add what would have caught the breach sooner.
- Raise anything that needs a product or architecture decision with the Product Owner as a decision, not as a
  side fix.
