# Terminal Assignment Delivery — Final Security Closure Evidence

Date: 2026-07-12 · Central `150.0.0.252` (edge control plane) · Appliance
`93f0bb1b-…-a84ba8` (`APP-DEV-0001`, radius `172.21.60.23`) · tenant Harborview
(`e30aa9ef`) / site (`e3c9ecd8`).

Scope: the six terminal-delivery requirements. The previously-accepted
active/verify_only/revoked key lifecycle and offline-safe stale-assignment
behaviour are unchanged and were not re-tested (their unit suites still pass and
are listed under §5).

---

## §1 — Two-phase normal revocation

**Design.** `beginTerminalDelivery` (normal): mint a higher-version signed
*terminal* assignment (`revoked`/`unassigned`/`decommissioned`), record
`terminal_delivery_pending`, and **leave credentials intact**. The still-trusted
appliance fetches the document over mTLS, clears its own tenant/site, and returns a
**signed acknowledgment** (appliance identity key). `AckHandler` verifies the ack
(self-scoped, correct version + document fingerprint) and only then runs **Phase 2**
— revoke the client certificate (NATS Auth-Callout honours it → reconnect denied,
live session dies within JWT TTL) and disable cloud access. Delivery states:
`terminal_delivery_pending → terminal_adopted → credential_revoked`;
`terminal_delivery_failed` on timeout.

**Live proof (single run).**

| step | observation |
|------|-------------|
| Phase 1 POST `/revoke` | `{"phase":"phase1_delivery_pending","assignment_version":9,"delivery_state":"terminal_delivery_pending"}` |
| during Phase 1 (t=6s) | `delivery=terminal_delivery_pending cert=active` — **credentials untouched** |
| appliance | log `sent terminal-adoption ack version 9 state revoked http 200` |
| ack verification | `ack_version=9`, `ack_fingerprint=5786cc6e…`, `ack_signed=t` |
| Phase 2 (t=12s, after ack) | `delivery=credential_revoked cert=revoked`, revocation reason `terminal_ack` |
| appliance after | re-exec'd, cleared tenant/site (`awaiting assignment … revoked`); reconnect → **403 appliance identity revoked**, NATS → `Authorization Violation` |
| lifecycle / emergency | `revoked` / `false` |

Ordering never lied about completion: the box confirmed adoption **before** any
credential was pulled.

**Timeout path.** `ReconcileTerminalTimeouts` (60 s ticker) flips a pending
delivery past `timeout_at` (10 min) to `terminal_delivery_failed`, raises an
`appliance_security_alerts` row, audits `appliance.terminal_delivery_failed`
("not reported as decommissioned"), and **does not** revoke credentials — retirement
stays UNCONFIRMED and an explicit emergency action remains available.

**Guest continuity.** Throughout Phase 1 the certificate stayed `active`
(cert=active while delivery pending, above), so cloud connectivity and the local
guest-serving dataplane (portald/radius/dnsmasq) continued uninterrupted until the
appliance itself adopted the terminal document.

---

## §2 — Emergency compromise flow

**Design.** `emergency_compromise=true` ⇒ do not depend on the compromised box:
revoke the certificate, deny NATS, and deny all API access **immediately**; mint the
terminal document but **wait for nothing**; mark delivery UNCONFIRMED
(`acked_at NULL`, `timeout_at NULL`). Gated by permission
(`platform.appliances.revoke`) + password step-up (`RequireReauth`) + **typed
confirmation** (`confirmation=<serial>`) + reason + immutable audit.

**Live proof.**

| case | result |
|------|--------|
| emergency, **no** confirmation | `400 emergency compromise requires confirmation=<serial>` |
| emergency, **wrong** confirmation | `400` (same guard) |
| emergency, `confirmation=APP-DEV-0001` | `{"phase":"emergency_credentials_revoked","delivery_state":"credential_revoked","note":"credentials revoked immediately; terminal delivery is UNCONFIRMED — local factory reset / controlled recovery required"}` |
| immediate DB state | `credential_revoked · emergency=t · acked_at NULL (unconfirmed) · timeout_at NULL · credential_revoked_at set`; cert `revoked`; lifecycle `revoked` |
| appliance | NATS reconnect `Authorization Violation` (ESTAB 0); API `403 identity revoked` |
| immutable audit | `appliance.revoked_emergency · emergency_compromise=true · reason="proof: emergency compromise"` (operator) |

Certificate revocation was **not** weakened to deliver a document: the cert was
revoked at once and the terminal document (v11) was still minted, but nothing
waited on the compromised box.

---

## §3 — Strict terminal assignment endpoint (`GET /v1/appliance/assignment`)

**Design.** Served only on the mutual-TLS listener (`:9443`). Layers:
TLS-required client cert → `RequireAppliance` signed request JWT → `mtlsCertBinding`
(cert appliance-id == JWT id, cert not revoked) → `strictMTLSSelf` (URI-SAN == self,
exact **fingerprint + serial** match to an `active` certificate issued to this
appliance). Returns only this appliance's current signed document with
`version > last_acked_version`, identity-bound, signed by a currently-trusted
(`active|verify_only`) key. `GET` only, rate-limited (30/min), every outcome written
to `appliance_assignment_fetch_log`. The legacy JWT/bootstrap mount on `:443` is
**removed**.

**Live negative matrix.**

| attempt | result |
|---------|--------|
| no client cert (`:9443`) | TLS handshake refused — `tlsv13 alert certificate required` |
| valid client cert, **no** appliance JWT | `401` (`RequireAppliance`) |
| plain bearer, no mTLS (`:9443`) | TLS handshake refused |
| JWT/bootstrap on old `:443/v1/appliance/assignment` | `404 page not found` (fallback removed) |
| serial/fingerprint mismatch | logged `denied:mtls`, `204`/deny (see fix note) |

**Positive / self-scoped.** After the serial-format fix (below) the appliance's own
polls log `served`; a document already acknowledged logs `not-modified:acked`
(`version <= last_acked` ⇒ 204). Fetch-log excerpt: `served / served / served`.

**Fix landed during closure.** `strictMTLSSelf` compared `cert.SerialNumber.String()`
(decimal) against the DB `cert_serial` stored as lowercase hex (`pki.SerialHex =
"%x"`); the serial never matched, so every genuine poll was `denied:mtls` and a
terminal document could never have reached the box. Corrected to
`fmt.Sprintf("%x", cert.SerialNumber)`; polls now succeed and §1 delivery completes.

### §3.1 Certificate-only authentication (final closure)

`GET /v1/appliance/assignment` now authenticates **exclusively** from the verified
client certificate. It is mounted **outside** the `RequireAppliance` group, so no
appliance JWT, bearer, bootstrap or enrollment token is required or consulted — any
`Authorization` header is ignored. Identity flows: client cert → URI-SAN
appliance_id → exact serial → exact fingerprint → Central appliance record
(`strictMTLSSelf`). Because the appliance_id is taken from the cert, a request can
only ever address its own document — there is no parameter to name another
appliance. The production `scd` client was updated to send no bearer on this call.
The `ack` POST and other endpoints keep JWT + cert-binding as defence in depth.

Full live matrix (genuine cert from the appliance; crafted CA-signed certs and DB
mutations from Central):

| # | check | result |
|---|-------|--------|
| 1 | valid client cert, **no JWT** | `200`, signed doc for self |
| 2 | JWT bearer, **no client cert** | TLS handshake refused (`http 000`) |
| 3 | valid cert + **bogus bearer** | `200`, bearer ignored, serves self |
| 4 | cert-A + `?appliance_id=B` override; and appliance-B's own cert | override ignored → serves A; B's cert `403` |
| 5 | unknown CA-signed cert (id not in record) | `403` "not an active credential" |
| 6 | fingerprint mismatch (impostor cert; and isolated DB flip) | `403` |
| 7 | serial mismatch (isolated DB flip) | `403` |
| 8 | `terminal_delivery_pending` fetch | `200`, higher-version **terminal** doc (v13, `state=revoked`, tenant/site cleared) |
| 9 | equal/lower version (`last_acked = version`) | `204` not-modified |
| 10 | assigned doc via terminal path | never — the served doc is the terminal (revoked) doc, not an assigned one |
| 11 | after signed ack + cert revocation | `403` |
| 12 | emergency-compromised appliance | `403` immediately after emergency revoke |
| 13 | GET-only / read-only / rate-limited / audited | `POST → 405`; 40 rapid GETs → 25×200 + 15×429; every outcome in `appliance_assignment_fetch_log` |

---

## §4 — Assignment key private-key custody (`c63f848bf5ded3f6`)

Full record: `docs/ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md`. Summary:

- **Identified** the revoked key on Central as `/etc/stayconnect/assignment-signing-old.key` (derived key_id `c63f848bf5ded3f6`); the three other on-host key files are all the **active** `027a2c97`.
- **Verified before removal:** ctrlapi loads `027a2c97` (not the old key); no systemd/env/config references `signing-old`; whole-host scan found exactly one copy; the key is absent from `bash_history`/`zsh_history`/`/var/log`.
- **Checksums:** plaintext `d59510a9…0940d5a`; encrypted offline copy `662547b7…e41126`.
- **Offline recovery copy:** AES-256-CBC / PBKDF2 (200k), stored **off Central** outside the online trust domain; passphrase held only in the custodian's offline store (not in repo/host/secret). Decryption reproduces the plaintext byte-for-byte (verified).
- **Removed:** plaintext `shred -u`'d; post-removal re-scan shows **0** copies of `c63f848bf5ded3f6` on Central.
- **Rollback** uses binary rollback, DB rollback where safe, and the `verify_only` overlap window — never the revoked key. The revoke guard already ensures a key only reaches `revoked` once nothing depends on it.

---

## §5 — Signed versioned trust registry

**Design.** The unauthenticated plain JSON registry is replaced by a signed envelope
(`registry_version`, `issued_at`, `not_before`/`not_after`, `keys[]` with per-key
state, `signer_key_id`, `signature`) bound to a manufacture-time **registry-root**
key (`84655767f9834fa2`) whose public half is baked into the appliance
(`/etc/stayconnect/assignment-registry-root.pub`, 32 B, read-only). The store keeps
`current` + `previous` atomically and only ever replaces current with a
validly-signed, higher-or-identical version.

**Proofs.**

- **Crypto / rollback (unit, both planes):** `TestSignedRegistryVerify` (valid accepted; unknown root rejected; tamper rejected), `TestRegistryStoreRollback` (higher accepted, previous retained; lower rejected; version-reuse-with-different-content rejected; unknown signer rejected; last-known-good persists across reload), `TestAckRoundTrip`, `TestRegistryTrustedFallback` — **all pass**.
- **Live: valid higher accepted** — appliance fetched, verified against the baked-in root, and persisted `registry.json` (signer `84655767…`, keys `c63f848b:revoked`, `027a2c97:active`); log `adopted signed trust registry`.
- **Live: reboot uses last verified** — with the certificate revoked (registry endpoint unreachable), scd restart still loaded and verified the persisted registry from disk.
- **Live: offline verification** — same condition (no reachable Central) is itself the Central-outage case: verification succeeds entirely from the on-disk envelope + baked-in anchor.
- **Live: tamper rejected + no downgrade** — corrupting the on-disk signature (and removing `previous`) produced `on-disk signed registry failed verification — refusing to downgrade to the unsigned trust file` and `no trusted registry — refusing to trust the persisted assignment` (fail-safe deny), instead of silently adopting anything.

**Fix landed during closure.** The original fallback dropped to the legacy
*unauthenticated* plain trust file whenever the signed registry failed to verify —
a downgrade attack (anyone who could write `registry.json` could plant a plain trust
file authorising a rogue key). Now, once a root-anchored signed-registry file exists
on disk, scd uses `Trusted()` (current, else the last-known-good `previous`) and
**refuses** the plain-file fallback; the legacy path applies only pre-rollout when no
signed registry has ever been persisted. Covered by `TestRegistryTrustedFallback`.

---

## §6 — Artifacts

- Code (control-plane): `terminal_delivery.go` (two-phase, emergency, strict endpoint, `strictMTLSSelf` serial fix), `assignment_registry.go`, `assignment_keys.go`, `appliance_mtls.go`, `appliance_lifecycle.go`, `assignment/{terminal,signed_registry,registry_store,registry,assignment}.go`, migrations `0032`/`0033`.
- Code (data-plane): `cmd/scd/assignment.go` (mTLS-only fetch, signed-registry fetch/verify/persist, `currentRegistryOnDisk` no-downgrade, terminal-ack sender), `internal/assignment/{registry_store,…}.go`.
- Tests (both planes): `terminal_registry_test.go` (incl. `TestRegistryTrustedFallback`), `registry_test.go`, `offline_test.go` — all pass.
- Runbook: `docs/ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md`.
- Deployment: migration `0033` applied; registry root `84655767f9834fa2` published (signed registry v3); ctrlapi + scd redeployed; appliance left healthy (`assigned` v12, mTLS up, NATS up, license Active).
