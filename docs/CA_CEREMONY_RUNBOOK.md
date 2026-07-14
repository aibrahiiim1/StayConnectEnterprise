# StayConnect Root CA — Offline Export & Ceremony Runbook

## Current status (honest)
- **Runtime-isolated NOW:** the Root CA **private** key exists on `150.0.0.252` only as an
  AES-256-CBC / PBKDF2 (300k iter) encrypted blob at
  `/opt/stayconnect/ca-ceremony-backup/root-ca.key.enc`. No plaintext Root private key exists
  anywhere on the Central host, Docker volumes, `/tmp`, backups, shell history, logs, or the repo
  (verified with `find` + `grep`). The passphrase is **not** in any report, repo, env file,
  systemd unit, or shell history.
- **Physically offline ONLY AFTER** the operator completes the export below and deletes the two
  Central copies. Until then this is encryption-at-rest, not an air-gap.
- The online Central runtime retains only: Root **public** cert (`root-ca.crt`), Intermediate cert
  + Intermediate **private** key (`/etc/stayconnect/pki/`), server-TLS cert/key, and versioned
  trust bundles in `appliance_ca_versions`.

## Backup-area contents (`/opt/stayconnect/ca-ceremony-backup/`, root, 0700)
| File | Mode | Purpose |
|------|------|---------|
| `root-ca.key.enc` | 0600 | Encrypted Root **private** key (AES-256, PBKDF2 300k) |
| `root-ca.key.enc.sha256` | 0600 | Integrity checksum of the encrypted blob |
| `root-ca.crt` | 0600 | Root **public** certificate (also in runtime + DB) |
| `CEREMONY_PASSPHRASE.txt` | 0400 | **One-time** passphrase for operator pickup |

## One-time operator export procedure
1. **Copy** to offline encrypted media / HSM / vault (do this from a trusted admin workstation):
   ```
   scp root@150.0.0.252:/opt/stayconnect/ca-ceremony-backup/root-ca.key.enc      ./
   scp root@150.0.0.252:/opt/stayconnect/ca-ceremony-backup/root-ca.key.enc.sha256 ./
   ```
2. **Validate checksum** on the offline media:
   ```
   test "$(sha256sum root-ca.key.enc | awk '{print $1}')" = "$(cat root-ca.key.enc.sha256)" && echo OK
   ```
3. **Retrieve the passphrase separately** (different channel from the blob), then **destroy the
   Central copy** of the passphrase file:
   ```
   scp root@150.0.0.252:/opt/stayconnect/ca-ceremony-backup/CEREMONY_PASSPHRASE.txt ./   # store in a password manager / vault, NOT next to the blob
   ssh root@150.0.0.252 'shred -u /opt/stayconnect/ca-ceremony-backup/CEREMONY_PASSPHRASE.txt'
   ```
4. **After the offline copy is verified**, remove the remaining Central copy of the encrypted key:
   ```
   ssh root@150.0.0.252 'shred -u /opt/stayconnect/ca-ceremony-backup/root-ca.key.enc'
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
5. Distribute the new intermediate cert + updated trust bundle (`appliance_ca_versions`) to Central;
   appliances pick up the new bundle on next cert fetch. Overlap old+new intermediates during the
   trust-bundle transition.

## Recovery
- Loss of the encrypted blob **or** the passphrase = permanent loss of the ability to rotate the
  intermediate under the current root → a new root ceremony + re-issuance would be required.
  Keep the blob and passphrase in **separate**, backed-up, offline locations.
