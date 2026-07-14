# Licensing & Entitlements

> The signed entitlement model implemented in `license/` (shared Go module),
> issued by `control-plane/internal/licensing`, stored in the cloud `licenses`
> table (migration 0019), verified and enforced entirely offline on the
> appliance.

## 1. Model

The cloud signs an entitlement **Document** with the vendor's Ed25519 private
key (`CTRLAPI_VENDOR_KEY`, cloud-only). Appliances hold **only public keys**
and validate entitlements with zero cloud round-trips. The document is
delivered on enrollment and on every renewal/change; the appliance persists
the latest copy under `/etc/stayconnect/license/` and evaluates its
operational state locally. Entitlement truth is the file — never a database
query against the cloud.

## 2. Document format

The wire/disk form is an **Envelope**: the exact signed payload bytes
(base64), the signature, and the signing key id. The payload is *not*
re-serialized on verify — the embedded bytes are what the signature covers —
so no JSON canonicalization is needed.

```json
{
  "payload": "<base64 of the Document JSON below>",
  "signature": "<base64 Ed25519 signature over those exact bytes>",
  "key_id": "1a2b3c4d5e6f7a8b"
}
```

Decoded Document (schema_version 1):

```json
{
  "license_id": "0c9f6d4e-8a21-4d3b-9f1e-5b7c2a9e4d10",
  "tenant_id": "d2a7c1e4-...-tenant-uuid",
  "site_id": "f81b3a6c-...-site-uuid",
  "appliance_ids": ["a1...", "a2..."],
  "commercial_plan_code": "pro-yearly",
  "status": "active",
  "issued_at": "2026-07-11T10:00:00Z",
  "valid_until": "2027-07-11T10:00:00Z",
  "offline_grace_days": 30,
  "features": {
    "pms": true, "paid_wifi": true, "sms_otp": true, "email_otp": true,
    "social_login": true, "ha": false, "white_label": false
  },
  "limits": {
    "max_appliances_for_site": 2,
    "max_concurrent_guest_sessions": 500,
    "max_local_operators": 25,
    "max_guest_access_plans": 100,
    "accounting_retention_days": 90,
    "audit_retention_days": 365
  },
  "schema_version": 1
}
```

Field notes: `status` ∈ {`active`, `suspended`} (issuer-declared);
limits use `0` = unlimited (issuance converts the plan system's `-1`/missing);
`offline_grace_days` must be 0–365; verifiers reject unknown `schema_version`
rather than misreading fields.

## 3. Signing, verification, keys

- **Sign (cloud):** `Signer.Sign` validates then signs `json.Marshal(doc)`;
  `key_id` = first 8 bytes of SHA-256 of the public key, hex.
- **Verify (edge):** `Verifier` maps key_id → trusted public key; unknown key
  ⇒ `ErrUnknownKey`; bad signature ⇒ `ErrBadSignature`; then structural
  `Validate()`. Verification says nothing about time — that is `Evaluate`.
- **Key rotation:** the Verifier holds multiple public keys (`AddKey`). Roll-out:
  distribute the new public key to appliances (config/update) while still
  signing with the old key → start signing with the new key (envelopes carry
  the new `key_id`, old licenses keep verifying) → retire the old public key
  once no current license references it. The private key never leaves the
  cloud; `GenerateVendorKey` writes it 0600.

## 4. State machine

Evaluated locally from document time (`grace` = `offline_grace_days`):

```
issued_at ──────────────── valid_until ──── +grace ──── +2×grace ────▶ time
│           Active            │ GracePeriod │ Restricted │   Expired
└─ overridden at any point by:  Suspended (doc status = suspended)
                                Revoked   (revocation notice names license_id)
```

- **Active** — within validity. Everything entitled works.
- **GracePeriod** — `valid_until` passed, within grace. Guest functionality
  unchanged; Hotel Admin shows a prominent renewal warning. Exists so a renewal
  issued while the appliance was offline never interrupts a hotel.
- **Restricted** — grace exhausted (until `valid_until + 2×grace`). Existing
  sessions continue; basic guest logins (voucher/PMS/email OTP) still work;
  entitlement-gated features (paid WiFi, SMS OTP, social) turn off; creating
  GuestAccessPlans/voucher batches is blocked; admin is directed to the
  license page.
- **Expired** — beyond `+2×grace`. New guest sessions refused (portal shows a
  service notice); existing sessions run to their natural end; Hotel Admin is
  read-only plus license upload.
- **Suspended** — issuer set `status: suspended` (billing hold). Enforcement
  identical to Restricted, effective immediately on receipt.
- **Revoked** — an authenticated revocation notice names this `license_id`.
  New sessions refused immediately; admin locked to the license page.
  Strongest state; never entered by time alone.
- **CloudStale** — a **warning flag, not a state**: trips when the last
  successful cloud validation (license fetch / ack) is older than
  `offline_grace_days`. It never degrades guest function while the document
  itself is valid — it only signals sync trouble in Hotel Admin and telemetry.

## 5. Behavior per state

| State | New guest sessions | Existing sessions | Provisioning (plans/batches) | Entitled features (paid WiFi, SMS OTP, social…) | Hotel Admin |
|---|---|---|---|---|---|
| Active | yes | run | yes | per document | full |
| GracePeriod | yes | run | yes | per document | full + renewal banner |
| Restricted | yes (basic: voucher/PMS/email OTP) | run to natural end | **no** | **off** | full read, license-directed |
| Suspended | yes (basic) | run to natural end | **no** | **off** | full read, license-directed |
| Expired | **no** (portal service notice) | run to natural end | no | off | read-only + license upload |
| Revoked | **no**, immediately | run to natural end | no | off | locked to license page |

Two invariants encoded in `license/doc.go`: *a billing dispute must not take a
hotel's WiFi down* (Restricted/Suspended keep basic guest access), and
*existing sessions always run to their natural end* in every state.

`FeatureEnabled(state, entitled)`: a feature works iff it is entitled in the
document **and** the state is Active/GracePeriod.

## 6. Offline grace in practice

`GET /v1/appliance/license` succeeding (or an explicit ack) calls
`MarkCloudValidated`. If the cloud is unreachable, nothing changes until
`valid_until` — an appliance with a 1-year license can run offline for the
year. Grace only matters when validity lapses while offline; the recommended
default (`offline_grace_days: 30`, issuance default) gives 30 days of full
service + 30 days of restricted service before expiry. Worked example:
[OFFLINE_OPERATION.md](OFFLINE_OPERATION.md).

## 7. Rollback protections (`license/store.go`)

- **License rollback:** installing a document whose `issued_at` is older than
  the currently installed one fails with `ErrRollback` — an old, more generous
  license cannot be replayed after a downgrade or revocation. `issued_at` is
  monotonic per appliance (persisted in `state.json`).
- **Clock rollback:** the store persists a **high-water mark** of the highest
  wall-clock time observed. If the clock is set back by more than the 48h
  tolerance, evaluation uses the high-water time instead and flags
  `clock_rollback` in the evaluation (surfaced in Hotel Admin and telemetry).
  Winding the clock back cannot resurrect an expiring license.
- Files (`current.json`, `state.json`, `revoked.json`) are written 0600 with
  atomic tmp+rename in a 0700 directory (default `/etc/stayconnect/license`).

## 8. Issuance, delivery, revocation

- **Issue:** `POST /cloud/v1/licenses` (platform_admin) — projects the
  tenant's active subscription + effective limits into a Document, signs,
  supersedes the site's previous current license, persists envelope +
  projection. 402 if no subscription. Flow diagram:
  [CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md) §5.
- **Deliver:** appliance pulls `GET /v1/appliance/license` (Ed25519 appliance
  JWT, ≤60s lifetime) → `{license_id, envelope, revoked[], server_time}`.
  Manual path for offline sites: download the envelope in cloud-admin, upload
  via Hotel Admin `POST /edge/v1/license`.
- **Revoke:** `POST /cloud/v1/licenses/{id}/revoke` sets the cloud row
  `revoked`; the edge learns via the `revoked[]` list on its next fetch (or a
  pushed notice) and records the id in its local revocation store —
  `revoked.json` persists across restarts and new-license installs.
- **Suspend/resume:** re-issue with `status: suspended` (billing hold), then
  re-issue `active` to restore — the issued_at monotonicity makes the ordering
  unambiguous.

## 9. Enforcement bridge on the edge

On every verified (re)load, scd/edged rewrite the local
`tenant_effective_limits` **table** from the document (`source='license'`):
features → `feature.*` bool rows, limits → int rows. Existing data-plane
queries (concurrency check, operator/plan caps) required no changes — they
read the same keys they always did, now fed by the license instead of the
cloud view. See `data-plane/migrations/0001_edge_init.up.sql`.
