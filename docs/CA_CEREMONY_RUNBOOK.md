# StayConnect Root CA — Offline Export & Ceremony Runbook

## Current status (honest) — reconciled 2026-10-28 (D79, T0265)

### Current production trust universe

The production Central (`41.128.240.133`, since 2026-10-28) was installed fresh by `central-install.sh` and has its
**own** vendor key, assignment key, registry root, appliance Root/Intermediate CA and internal TLS CA (installation
manual F, Appendix 3); the test appliance `172.21.96.97` was provisioned in that universe. Moving its Root key to
cold storage is installation manual F (step 2) and follows the procedure below; whether that step has been done on
the production host is **not recorded** and could not be checked from the repository.

What the repository shows (checked 2026-10-28): it contains **no private key material** and **no compiled-in trust
anchor**. Appliances and Central read every anchor at run time from per-installation files under `/etc/stayconnect`
(`vendor-license.pub`, `assignment-registry-root.pub`, the appliance CA bundle, `central-tls-ca.crt`), installed
from the installing Central's `appliance-trust/` with `FINGERPRINTS.txt` (manual F-08, B.5). Nothing current
depends on the legacy Root CA.

### Legacy trust universe — custody reconciliation

The legacy universe (Root CA fingerprint `D2:60:C6:EE:…:A7:00`, vendor key `2fbdfcbde209ebb5`, assignment key
`027a2c97f6c8fcdb`, registry root `84655767f9834fa2`) was used by the retired Centrals and the retired PRE-LIVE
appliance. **No active system trusts it**, so its private keys are genuinely obsolete: they can no longer
authenticate anything current.

| Copy | Where | Status |
|---|---|---|
| Custody copy taken 2026-09-27 (`root-ca.key.enc`, sha256 `a5aa1944…504c`, `root-ca.crt`) | the Product Owner's workstation custody folder | **No longer held** — Product-Owner confirmation, 2026-10-28. No destruction record exists and none is written here; whether it ever reached offline media is not recorded. |
| Host backup (`root-ca.key.enc`, `root-ca.key.enc.sha256`, `CEREMONY_PASSPHRASE.txt`) | `/opt/stayconnect/ca-ceremony-backup/` on the first Central `150.0.0.252` | **Host permanently removed** — Product-Owner confirmation, 2026-10-28 (the former servers have been permanently removed). The copy was last verified present on 2026-09-27; its secure erasure was not observed or verified by any delivery session, and none is claimed. |
| Legacy runtime keys (Intermediate CA, vendor licence, assignment, registry-root and internal TLS CA private keys) | `/etc/stayconnect/` on the former Central `172.21.96.196` (carried over by the 2026-09-27 restore) and on `150.0.0.252` | **Hosts permanently removed** — same Product-Owner confirmation. Erasure of the disks was not observed or verified by any delivery session. |

**Status:** no legacy copy is known to remain in the Product Owner's possession or on any host the project operates.
What is recorded is the Product Owner's confirmation, not a verified key destruction: if any image, snapshot or backup
of the removed servers survives elsewhere, it still contains these keys. No current system trusts them, so their
residual risk is limited to impersonating the legacy universe, which nothing active relies on.

### Runtime layout (any Central)

The online Central runtime retains only: Root **public** cert (`root-ca.crt`), Intermediate cert + Intermediate
**private** key (`/etc/stayconnect/pki/`), server-TLS cert/key, the appliance CA chain `pki/ca-bundle.crt`
(intermediate + root; `CTRLAPI_CA_BUNDLE`, also written into offline activation packages), and versioned trust
bundles in `appliance_ca_versions`. The Root **private** key belongs on offline media only.

## Backup-area contents on a Central (`/opt/stayconnect/ca-ceremony-backup/`, root, 0700)
| File | Mode | Purpose |
|------|------|---------|
| `root-ca.key.enc` | 0600 | Encrypted Root **private** key (AES-256, PBKDF2 300k) |
| `root-ca.key.enc.sha256` | 0600 | Integrity checksum of the encrypted blob |
| `root-ca.crt` | 0600 | Root **public** certificate (also in runtime + DB) |
| `CEREMONY_PASSPHRASE.txt` | 0400 | **One-time** passphrase for operator pickup |

## One-time operator export procedure
`HOST` is the Central host that holds the backup, named explicitly for the ceremony; there is no default.

1. **Copy** to offline encrypted media / HSM / vault (do this from a trusted admin workstation):
   ```
   HOST=root@<central-host-holding-the-backup>
   scp "$HOST":/opt/stayconnect/ca-ceremony-backup/root-ca.key.enc      ./
   scp "$HOST":/opt/stayconnect/ca-ceremony-backup/root-ca.key.enc.sha256 ./
   ```
2. **Validate checksum** on the offline media:
   ```
   test "$(sha256sum root-ca.key.enc | awk '{print $1}')" = "$(cat root-ca.key.enc.sha256)" && echo OK
   ```
3. **Retrieve the passphrase separately** (different channel from the blob), then **destroy the
   Central copy** of the passphrase file:
   ```
   scp "$HOST":/opt/stayconnect/ca-ceremony-backup/CEREMONY_PASSPHRASE.txt ./   # store in a password manager / vault, NOT next to the blob
   ssh "$HOST" 'shred -u /opt/stayconnect/ca-ceremony-backup/CEREMONY_PASSPHRASE.txt'
   ```
4. **After the offline copy is verified**, remove the remaining Central copy of the encrypted key:
   ```
   ssh "$HOST" 'shred -u /opt/stayconnect/ca-ceremony-backup/root-ca.key.enc'
   ```
   At this point the Root private key exists ONLY on offline media → physically offline achieved.
5. **Store passphrase separately** from the encrypted blob (different vault / different custodian).

## Future Intermediate rotation ceremony (root needed briefly, offline)
Performed on a trusted, preferably air-gapped host — never on the routine Central runtime:
1. Bring the encrypted Root blob + passphrase to the ceremony host.
2. `openssl enc -d -aes-256-cbc -pbkdf2 -iter 300000 -in root-ca.key.enc -pass file:pass.txt -out root-ca.key`
3. Place `root-ca.key` + a new `intermediate-ca.key` and run ctrlapi's first-setup path
   (`CTRLAPI_ROOT_CA_KEY`, `CTRLAPI_INTERMEDIATE_CA_KEY`) with the next version number to sign a new
   intermediate; this yields `intermediate-ca.crt` (+ re-uses the same `root-ca.crt`).
4. Shred the decrypted `root-ca.key`. Re-encrypt/re-store as in step 1.
5. Distribute the new intermediate cert + updated trust bundle (`appliance_ca_versions`) to Central
   and rewrite `/etc/stayconnect/pki/ca-bundle.crt` (intermediate + root) so offline activation packages
   carry the new chain;
   appliances pick up the new bundle on next cert fetch. Overlap old+new intermediates during the
   trust-bundle transition.

## Recovery
- Loss of the encrypted blob **or** the passphrase = permanent loss of the ability to rotate the
  intermediate under the current root → a new root ceremony + re-issuance would be required.
  Keep the blob and passphrase in **separate**, backed-up, offline locations.
