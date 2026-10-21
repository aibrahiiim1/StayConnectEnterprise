# The erasure ledger

**Status:** built on `delivery/gdpr` (migration 0148, `data-plane/internal/ledger`, scd `ledger_service.go`, `ledger_restore.go`).
Authoritative for how erasures and closed requests are recorded, replicated and replayed.

## 1. The promise, and the problem it answers

When the hotel tells a guest *"your data has been erased"*, that has to stay true. The database is the one place that can be
rolled back: a backup restored after a disk failure, a mistake or a migration brings back every guest erased since that backup,
names and all. The ledger is the record that survives a restore, so the erasures are done again before the restored data is served,
and so a request the hotel has already answered is not reopened.

## 2. What is in it (and what is not)

An append-only, hash-chained log. Each entry is one JSON line:

```
{"ledger": <this appliance's ledger id>, "seq": 42, "prev": <hash of 41>, "kind": "erase", "tenant": …, "site": …,
 "at_us": <time>, "body": "<json text>", "hash": sha256(domain | ledger | seq | prev | kind | tenant | site | at | body)}
```

| Kind | When | Body (no guest data, no staff-typed text) |
|---|---|---|
| `genesis` | once, with the migration | nothing |
| `erase` | an erasure, automatic or assisted | the reference `ER-…`, the subject kind, when, the counts, and the **kind and id** of every record that was anonymised |
| `request_answered` | a request in the register is answered | its id and reference, kind, dates, status, the coded ground, the references of the erasure / assisted request that carried it out (never who asked, never the note) |
| `assisted_closed` | an assisted request is completed or cancelled | its id, reference, purpose and status |
| `checkpoint` | about daily | nothing (it makes the head recent, so every backup records a recent head) |

Never in it: a name, an address, a phone, a device address, a reservation number, the reason a member of staff typed, who did it.

## 3. Three copies, two of them outside the database

1. **The database** (`iam_v2.erasure_ledger`), written **in the same transaction** as the erasure. It cannot be updated or deleted
   (a guard), and the chain is checkable in SQL (`iam_v2.ledger_verify()`).
2. **The primary copy**: `/var/lib/stayconnect/ledger/erasure-ledger.jsonl` on the appliance, mode 0600, outside the backup folders.
3. **The secondary copy**: a storage the hotel sets up in Hotel Admin → Backups, and which is **mandatory**:
   * a folder on an **external disk** or a **network share** mounted on the appliance;
   * an **SFTP** server (its host key is pinned when it is set up);
   * an **S3-compatible bucket** the hotel owns (Amazon S3, Backblaze B2, Wasabi, MinIO, a NAS that speaks S3).

   It is encrypted to the hotel's backup key holders (age; the appliance holds public keys only), with a small plain note beside it
   (how far it reaches, its checksum, whether it is encrypted). It is replaced as a whole each time (an atomic put) and read back.

**Independent means independent.** A folder is accepted only if the kernel's own mount table and block-device tree show it is a
separate mounted disk or a network share not served by the appliance, and not a partition of the disk the appliance runs from, nor
LVM/RAID over it (`internal/ledger/independence.go`). An SFTP or S3 endpoint on the appliance itself is refused. Nothing is ever sent
to OneGate Central.

## 4. How an erasure is committed

```
edged: prepare  ── scd reconciles the three copies; erasure may begin only if they are in step
edged: BEGIN
         the erasure, and its ledger entry (advisory lock held: no other erasure can interleave)
         ship the entry to scd ── scd: append to the primary copy, fsync, read back
                                       replace the secondary copy, read it back byte for byte
                                       (secondary failed → the primary entry is taken out again)
         mark the entry confirmed
       COMMIT
edged: only now tell the person
```

* If **either copy cannot be written**, the transaction is rolled back: nothing was erased, nothing was reported, the administrator is
  told which storage to look at, and **guest internet access is not touched**.
* Until the secondary storage is set up and verified, erasure is refused (409), with the instruction.
* If the process dies between "both copies written" and the database commit, an entry exists in the files that the database lacks.
  The next reconciliation adopts it (applies it), which completes the erasure the operator had asked for. Before that, the
  advisory lock is held by the dead transaction until the server drops it, so nothing can interleave.
* The secondary copy is **never overwritten while it is ahead** of the appliance (it holds erasures the appliance has lost): the commit
  refuses and the screen offers *Recover from the secondary storage*.

## 5. Keeping the copies in step (hourly, and before every erasure)

The reconciliation holds the ledger lock for its whole run and compares:

| Found | Done |
|---|---|
| primary copy missing or damaged | kept aside as `.damaged-…`, rebuilt from the database |
| database behind the primary copy (restore from an older backup; a crash leftover) | the missing entries are applied to the database |
| database ahead of the primary copy | the primary is brought up to date |
| the two disagree about an entry they both have | **blocked**, erasure paused, support needed |
| secondary missing, corrupt, behind, or encrypted to other key holders | replaced from the primary, read back |
| secondary **ahead** | **recovery needed**, nothing is overwritten |

Torn last lines (an interrupted write, never acknowledged) are ignored and removed.

## 6. Restore: one verified, complete copy is enough

A restore loads the backup into a **staging database** first. The ledger is checked and applied **to that staging database before
the swap**, so the restored data is never served un-erased:

1. *Before anything is touched:* the live database's own entries are made durable in the primary copy.
2. The primary and the secondary copies are read and verified (`internal/ledger.Assess`). A copy that cannot be reached is simply not
   used; **both need not be online**.
3. A copy is trusted only if it is an **intact chain** and reaches every **independent witness**: the ledger inside the backup, the
   live database, and the head recorded in the note of every backup on the appliance.
   * both copies intact and agreeing → complete;
   * **one** copy → complete only if at least one witness exists and every witness is satisfied;
   * a copy cut short, a witness the copy does not contain, two copies that fork, a damaged copy, nothing reachable → **refused**,
     with the reasons, and nothing is replaced;
   * a backup older than the ledger is refused once the ledger holds erasures (restoring it could not bring them back);
   * an unwitnessed ledger longer than its first entry is refused.
4. The entries after the backup are applied to the staging database (erasures done again, answered requests closed again, the erasure
   proof the guest was given put back, the activity-log traces of erased device addresses removed). An entry that cannot be applied stops
   the restore.
5. The secondary-storage setting of *this* appliance is carried across the swap (it belongs to the appliance, not the backup).
6. After the swap, the missing copy is rebuilt and synchronised; an unreachable secondary does not undo the restore, it is reported and
   retried hourly.

An encrypted secondary copy needs a **backup key holder's key** to be read (the same key that opens the backups). The local copy needs none.

## 7. The daily backup

Every night (the hour is a setting) scd takes **one** backup of the database and of `/etc/stayconnect` (configuration and keys, without
the financial restore marker), both encrypted to the key holders as they are written. It is **not taken without a key holder**. The dump
is verified while it is made: the same stream is loaded into a scratch database, which must accept it without one error and hold the
tables and an intact ledger. The note beside it records the SHA-256 of what is stored and the ledger head it contains. The newest *N*
(the database-backups setting, default 7) are kept; older sets are removed only after a newer one verified. Each day the stored files are
checked against their notes, so silent corruption is found while other days remain.

## 8. Limits, stated plainly

* A ledger copy cut short **can only be noticed against a witness**. An erasure made after the newest backup, whose entry was then lost
  from the only copy available, is invisible. The dual write exists to make that nearly impossible; it cannot make it impossible.
* The hash chain proves integrity, not authorship: someone who can write the secondary storage **and** knows the (public) key holder
  keys could forge a longer chain. The threat model is failure, not a malicious storage administrator.
* Requests that are still **open** are not in the ledger (they hold who asked); a restore brings back an open request as it was at
  the backup, and it is closed again by whoever answers it.
* An erasure interrupted in the last instant (the process dies between the two copies and the commit) is completed at the next
  reconciliation rather than rolled back.
* The ledger is per appliance. Moving an appliance to another customer does not carry it: see DATA_OWNERSHIP.md section 10.

## 9. How it is verified (all repeatable)

| What | How |
|---|---|
| the chain, the assessment, both copies, the stores | `go test ./internal/ledger` (and `scripts/ledger-linux-checks.sh` for the mount-table and block-device walk, on Linux) |
| SFTP and S3 against real servers | `scripts/ledger-destinations-compat.sh` (OpenSSH's SFTP, Scality CloudServer) |
| the database side, as the real runtime roles | the appliance integration suites on a factory-clean schema (`TestIntegration_Ledger_*`, `TestIntegration_RestoreReplay_*`, `TestIntegrationLedger_*`), including a whole erasure as `svc_edged` and the reconciliation as `svc_scd` |
| the real restore, end to end | `scripts/ledger-restore-drill.sh`: both copies; the secondary unplugged; the secondary alone; forked copies; nothing trustworthy; an interrupted write; a copy cut short; a backup older than the ledger; a replacement appliance |
| rollback | migration 0148 down, then up again, with data and a non-empty ledger (the ledger tables stay; the 0147 journal functions come back) |
