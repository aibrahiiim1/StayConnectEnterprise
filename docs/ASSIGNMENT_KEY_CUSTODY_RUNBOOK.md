# Assignment Signing Key — Custody & Retirement Runbook

Custody record for retired/revoked **assignment-signing** private keys. A revoked
signing key is **not** a production rollback mechanism (see *Rollback*, below); it
is removed from every online host and, only if retention is required, preserved as
a single encrypted offline copy outside the online Central trust domain.

## Key registry (state, not custody)

| key_id | role | state | notes |
|--------|------|-------|-------|
| `027a2c97f6c8fcdb` | assignment-signing (current) | **active** | live signer; loaded by ctrlapi from `/etc/stayconnect/assignment-signing.key` |
| `c63f848bf5ded3f6` | assignment-signing (previous) | **revoked** | rotated out; plaintext removed from Central (this runbook) |

The signed trust registry (root `84655767f9834fa2`) carries both keys with their
states, so appliances still verify documents already issued under a `verify_only`
predecessor but reject anything a `revoked` key signs.

## Retirement of `c63f848bf5ded3f6` — 2026-07-12

Performed on Central (`150.0.0.252`, host trust domain `sc-central-*`).

### 1. Pre-removal verification (all confirmed)

- **Active signer is NOT this key.** ctrlapi log: `assignment signing key loaded key_id=027a2c97f6c8fcdb path=/etc/stayconnect/assignment-signing.key`.
- **No service references it.** No systemd unit, `ctrlapi.env`, or config under `/etc/systemd`, `/etc/stayconnect`, `/opt/stayconnect` names `assignment-signing-old`.
- **No deployment secret contains it.** Whole-host scan (any filename, 64-byte ed25519 files matched by derived key_id) found exactly **one** copy: `/etc/stayconnect/assignment-signing-old.key`.
- **No shell history / log leak.** `/root/.bash_history`, `/root/.zsh_history`, and `/var/log/**` contain neither the base64 nor hex form of the private key.

### 2. Checksums

| artifact | sha256 |
|----------|--------|
| plaintext private key (64 B, ed25519) | `d59510a95e9565b2dad310d76aaad328a2447ffc5ea18c9ea5013680c0940d5a` |
| encrypted offline recovery copy (AES-256-CBC, PBKDF2 200k, 96 B) | `662547b7c43b9dada6b4588787cc7d252e2ebb01a9bfaaa383e8aaa0a0e41126` |

Recovery integrity verified: decrypting the offline copy reproduces the plaintext
byte-for-byte (sha256 match above) and re-derives key_id `c63f848bf5ded3f6`.

### 3. Custody of the encrypted offline copy

- **File:** `assignment-signing-old.c63f848b.key.enc`
- **Location:** operator key-custody store **off Central** (outside the online
  Central trust domain). It is **not** on any appliance, control-plane host, or in
  this repository.
- **Passphrase:** high-entropy, generated at retirement, held **only** in the
  security custodian's offline password store. It is **not** recorded in this
  runbook, the repo, any host, or any deployment secret. Without it the copy is
  inert ciphertext.
- **Cipher:** `openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt`.

### 4. Removal from the runtime host

- Plaintext `/etc/stayconnect/assignment-signing-old.key` **securely erased** (`shred -u -n3`).
- Post-removal whole-host re-scan: **0** copies of `c63f848bf5ded3f6` remain on Central.
- The active key (`027a2c97`) and its legitimate on-host copies
  (`assignment-signing.key`, `assignment-signing-new.key`, `assignment-signing.key.good`)
  are untouched — they are the live signer, legitimately online.

### 5. Recovery procedure (break-glass only)

Recovery is **not** a rollback path (see below). Use only for forensic or legal
retention needs, on an **offline** host:

```
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 \
  -in assignment-signing-old.c63f848b.key.enc -out recovered.key \
  -pass pass:<custodian passphrase>
# verify: sha256sum recovered.key == d59510a9...  (key_id c63f848bf5ded3f6)
```

A recovered key **must never** be re-introduced to an online Central host or used
to sign a production document — it is `revoked` in the signed trust registry and
every appliance will reject its signatures.

## Rollback — the right way (not the revoked key)

> Do not keep `assignment-signing-old.key` online merely as a convenient rollback.
> A revoked signing key is not a valid production rollback mechanism.

If a rotation or release must be reverted:

1. **Binary rollback** — redeploy the previous ctrlapi/scd binary.
2. **DB rollback where safe** — revert the migration/state change.
3. **verify_only overlap** — before a key is ever `revoked`, the outgoing key is
   held `verify_only` (still trusted for documents already issued under it) while
   the new `active` key signs. Reverting during that window means re-signing on the
   still-trusted key, never resurrecting a `revoked` one.

Because the guard in `assignment_keys.go` refuses to revoke a key that still signs
any appliance's current assignment (except under an audited emergency override),
a key only reaches `revoked` once nothing depends on it — so its removal can never
strand an appliance, and its retention can never be needed for rollback.
