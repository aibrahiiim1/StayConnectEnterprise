# Runbook: the erasure ledger -- storage failures and recovery

Who: the hotel's site administrator or IT manager (Backups is theirs). Background: [privacy/ERASURE_LEDGER.md](../privacy/ERASURE_LEDGER.md).
Nothing here is done to guests: **a ledger problem pauses erasure requests only; guest internet access is never affected.**

## 0. What the screen says

Hotel Admin → System → Backups → **Erasure ledger**.

| It says | It means | Do |
|---|---|---|
| *Both copies are in step* | normal | nothing |
| *Set up the secondary storage* | none is set up, so erasure is refused | section 1 |
| *Needs attention* + a reason | one copy cannot be written, read or is out of step | section 2 |
| *Recovery needed* | the secondary storage holds erasures this appliance does not have | section 3 |
| *The ledger needs support* | the copies disagree about an erasure | section 5 |

A request that was refused for this reason says so, and nothing was erased. Try again once the screen is green.

## 1. Setting up the secondary storage (required, once)

Choose one:

* **External disk or network share.** Mount it on the appliance (an IT task: `/etc/fstab`, a USB disk or an NFS/SMB share), then enter
  the folder, for example `/mnt/ledger`. The appliance refuses a folder on its own disk, a partition of its own disk, or a share it serves
  itself, and says which.
* **SFTP server.** Enter the server, user and folder with a password or a private key, press *Check the server's identity*, compare the
  fingerprint with the server's (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on it) and confirm. The appliance then refuses any
  server that presents another key.
* **S3-compatible bucket.** A bucket the hotel owns, with its own folder (prefix) and a key that may read, write and list only there.
  Turn on versioning if it offers it.

Press **Test** (nothing is saved), then **Save** with your password. The ledger is copied and read back before it says it is saved.
A storage that already holds **another appliance's** ledger is refused; use a folder of its own.

Add a **backup key holder** as well (Backups → Backup encryption): the secondary copy is encrypted to the key holders, and without one it
is stored unencrypted (the screen says so). The ledger holds no guest data, but encrypting it is the default for a reason.

## 2. The secondary storage is unplugged, full, unreachable or refuses the write

Symptoms: *Needs attention*, the secondary shows *not in step* with the error (for example "disk unplugged", "the bucket refused the
write", "the server's host key changed"). Erasure is paused.

1. Fix the storage (reconnect the disk, free space, restore the share, correct the credentials: *Change the secondary storage*).
2. *Check now*. The appliance rebuilds the secondary copy from its own and reads it back. **No file handling by hand.**
3. If an SFTP server's host key really changed (a reinstalled server), re-confirm its fingerprint when you save.

While it is down, **nothing is lost**: every erasure made earlier is on the appliance and in the database, and erasures are simply refused,
not queued.

## 3. Recovery needed (the secondary is ahead)

This happens when the appliance has lost its own copy **and** its database went back to an older state (a disk replaced, a restore
from before), while the secondary kept erasures the appliance no longer knows.

1. Press **Recover from the secondary storage**, paste a **backup key holder's key**, enter your password.
2. The appliance reads the secondary copy with that key, checks the whole chain against what the database and the appliance witness,
   applies the missing erasures and rebuilds its own copy. It refuses, with the reason, anything it cannot verify.
3. *Check now*: both copies in step.

## 4. Replacing the appliance (or rebuilding it after a disaster)

You need: **a backup** of the old appliance (a daily backup file copied off it; the hotel's own offsite copy), **a backup key holder's key**,
and **the secondary storage** (same disk, share, server or bucket, with its credentials).

1. Install and enrol the new appliance as usual (it starts with an empty database and its own new, empty ledger).
2. Copy the backup files (`db-….sql.gz.age`, and the `.meta.json` and `etc-….tgz.age` beside it) into `/opt/stayconnect/backups/db/` and
   verify the backup on the Backups screen (it asks for a key holder's key).
2b. **Configuration and keys.** The restored database holds credentials sealed with the *old* appliance's keys (the secondary-storage
   login, provider secrets). Follow the *Full appliance rebuild* procedure in [BACKUP_AND_RESTORE.md](../BACKUP_AND_RESTORE.md) for the
   configuration archive (`etc-<stamp>.tgz.age`, opened with a key holder's key: `scd backup-crypt decrypt -i <key-file> <file> - | tar xzf -`).
   Identity and licence are bound to the hardware, so the licence follows its own rebinding rules. If the sealing keys are not restored, the
   appliance cannot open the saved secondary-storage login and asks you to enter it again; other sealed secrets (providers, email) must be
   re-entered too. The ledger itself is unaffected: it holds no secret.
3. Backups → Erasure ledger → *Set up the secondary storage* with the **same** location and credentials. The new appliance sees the old
   appliance's ledger there; because its own ledger has nothing in it yet, it keeps the old one instead of overwriting it.
4. Restore the backup (paste the key holder's key). The restore verifies the secondary copy (one verified, complete copy is enough: the
   old appliance's own file is gone), applies every erasure and closed request made after the backup to the restored data **before it is
   served**, and then rebuilds this appliance's own copy from it.
5. Check: Backups shows both copies in step; Guest data requests → Erasures lists the references as before.

If the restore says **no complete, trustworthy copy** was available, read its reasons: for example the secondary storage was not reachable
or the key did not open it, or the backup is older than the ledger. Nothing was replaced. Fix the cause and run it again; do not look for
a way around it, because a restore that skips the ledger brings erased guests back.

## 5. The ledger needs support

*"The copy on the appliance and the database disagree about an erasure"*, *"the secondary copy is a different ledger"* or *"the copies fork"*:
two places recorded different things for the same entry. This is not an operational mistake you can make by pressing buttons; it means
a file was altered or two appliances wrote to one storage. Erasure stays paused. Keep both copies (do not delete anything), note the
screen's text, and escalate to the supplier with the files: `/var/lib/stayconnect/ledger/` and the secondary storage's two objects.

## 6. A second appliance must never share a secondary storage

Each appliance needs its own folder, prefix or share. The appliance refuses to save a storage that already holds another appliance's ledger.

## 7. Moving an appliance to another customer

The ledger is that customer's. When an appliance is reassigned to another customer, the appliance starts a **new ledger** and **forgets
the previous secondary storage** (its setting is removed, and its local file is deleted); the previous customer keeps their own copy.
The new customer sets up their own secondary storage before their first erasure.
