# Licensing & Entitlements

> The signed entitlement model implemented in `license/` (shared Go module),
> issued by `control-plane/internal/licensing`, stored in the cloud `licenses`
> table, verified and enforced entirely offline on the appliance. How an operator
> issues, renews, suspends and revokes a licence in Central is in
> [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md) §4 and §6.

## 1. Model

The cloud signs an entitlement **Document** with the vendor's Ed25519 private
key (`CTRLAPI_VENDOR_KEY`, cloud-only). Appliances hold **only public keys**
and validate entitlements with zero cloud round-trips. The document is
delivered on activation and on every renewal/change; the appliance persists
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

Decoded Document, as Central issues it today (schema_version 3):

```json
{
  "license_id": "0c9f6d4e-8a21-4d3b-9f1e-5b7c2a9e4d10",
  "tenant_id": "d2a7c1e4-...-tenant-uuid",
  "site_id": "f81b3a6c-...-site-uuid",
  "appliance_ids": ["a1..."],
  "commercial_plan_code": "direct",
  "status": "active",
  "issued_at": "2026-09-27T10:00:00Z",
  "valid_until": "2027-09-27T23:59:59Z",
  "offline_grace_days": 30,
  "features": {
    "pms": true, "paid_wifi": true, "sms_otp": true, "email_otp": true,
    "social_login": true, "ha": true, "white_label": true
  },
  "limits": { "max_concurrent_guest_sessions": 500 },
  "appliance_id": "a1...",
  "appliance_serial": "SC-...",
  "hardware_fingerprint": "...",
  "identity_key_fingerprint": "...",
  "wan_mac": "00:50:56:...",
  "valid_from": "2026-09-27T10:00:00Z",
  "signer_key_id": "1a2b3c4d5e6f7a8b",
  "max_concurrent_online_guests": 500,
  "grace_period_days": 30,
  "license_version": 4,
  "supersedes_license_id": "…previous license id…",
  "schema_version": 3
}
```

Field notes: `status` ∈ {`active`, `suspended`} (issuer-declared).
`max_concurrent_online_guests` is the capacity, `0` = unlimited; the legacy
`limits` block mirrors it so pre-v3 appliances enforce the same cap. Every
licence Central issues carries **all** features — the only commercial controls
are the hardware/identity binding, the capacity and the validity window.
`commercial_plan_code` is always `direct`: plans and subscriptions no longer
exist, and the field stays only because the signed format carries it. Central
migration 0047 dropped the database copies of `commercial_plan_code`,
`features` and `limits` from the `licenses` table (nothing read them);
`licenses.signed_envelope` still holds the complete signed document.
`license_version` is monotonic per appliance. Verifiers reject unknown
`schema_version` rather than misreading fields; schema 1 and 2 documents still
verify.

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

Evaluated locally from document time, every minute and at boot. For schema 3
(`grace` = `grace_period_days`):

```
valid_from ─────────────── valid_until ──── +grace ────▶ time
│           Active            │ GracePeriod │   Expired
└─ overridden at any point by:  Suspended (doc status = suspended)
                                Revoked   (revocation list names license_id)
```

Before `valid_from` the state is Expired. **Legacy (schema 1/2) documents only**
keep the historical window `valid_until +grace → +2×grace` as **Restricted**,
with `grace` = `offline_grace_days` when `grace_period_days` is absent.

The binding (identity key, appliance id, serial, hardware fingerprint) is
re-checked on every evaluation, including at boot; a mismatch is a hard reject
(*wrong hardware*). A WAN-MAC-only mismatch is soft: the licence stays in force
and a notice asks for a rebind in Central.

- **Active** — within validity. Everything entitled works.
- **GracePeriod** — `valid_until` passed, within grace. Client functionality
  unchanged; the Admin Console (formerly Hotel Admin) shows a prominent renewal warning. Exists so a renewal
  issued while the appliance was offline never interrupts a site.
- **Restricted** (legacy schema 1/2 only) — grace exhausted (until
  `valid_until + 2×grace`). Existing sessions continue; basic client logins
  still work; entitlement-gated features turn off; creating client access
  plans/voucher batches is blocked.
- **Expired** — beyond `valid_until + grace` (legacy: `+2×grace`). New client
  sessions refused (portal shows a service notice); existing sessions run to
  their natural end; Admin Console stays available.
- **Suspended** — issuer set `status: suspended` (billing hold). New client
  sessions refused, effective immediately on receipt; existing sessions run to
  their natural end.
- **Revoked** — an authenticated revocation notice names this `license_id`.
  New sessions refused immediately; admin locked to the license page.
  Strongest state; never entered by time alone.
- **Unlicensed** — no valid signed licence installed (factory-clean, waiting
  for activation, missing or invalid). A production appliance fails closed:
  no new client sessions.
- **CloudStale** — a **warning flag, not a state**: trips when the last
  successful cloud validation (license fetch) is older than
  `offline_grace_days`. It never degrades client function while the document
  itself is valid.

## 5. Behavior per state

| State | New client sessions | Existing sessions | Provisioning (plans/batches) | Entitled features (paid WiFi, SMS OTP, social…) | Admin Console |
|---|---|---|---|---|---|
| Active | yes | run | yes | per document | full |
| GracePeriod | yes | run | yes | per document | full + renewal banner |
| Restricted (legacy only) | yes (basic) | run to natural end | **no** | **off** | full, licence banner |
| Suspended | **no** | run to natural end | **no** | **off** | full, licence banner |
| Expired | **no** (portal service notice) | run to natural end | no | off | full, licence banner |
| Revoked | **no**, immediately | run to natural end | no | off | full, licence banner |
| Unlicensed | **no** | — | no | off | full, *No licence yet* |

The invariant encoded in `license/doc.go`: *existing sessions always run to
their natural end* in every state, and DHCP, DNS, the portal and the Admin Console
keep running — only **new** authorization is refused
(`State.AllowsNewSessions`).

`FeatureEnabled(state, entitled)`: a feature works iff it is entitled in the
document **and** the state is Active/GracePeriod.

**One gate, one capacity, every Client Access method.** Voucher, client account,
OTP, social login and **PMS room sign-in** all pass the same licence refusal
(`cmd/scd` `licenseRefusal`: removed from Central, tenant transition pending,
no licence, a state that does not allow new sessions, or the method's feature
not entitled) before anything about the client is looked at, and every new client
session takes a slot from the same atomic, appliance-scoped reservation of
`max_concurrent_online_guests` (`reserveLicensedSlot`, counted inside the
session-opening transaction). For room sign-in that includes a second device
joining the stay and a device rejoining after its session ended; a device that
signs in again while its session is still open keeps it and takes no slot. A
room sign-in refused by the licence is recorded as `LICENSE_REFUSED` or
`LICENSE_CAPACITY_REACHED` in `iam_v2.sign_in_attempts` (appliance migration
0092) and shown on the Admin Console's **Client sign-in attempts**; the client sees the
same refusal the other methods give.

## 6. Offline grace in practice

`GET /v1/appliance/license` succeeding calls `MarkCloudValidated`. The
appliance fetches every minute until it holds a usable licence, then every 6
hours, and at once on **Check now**. If the cloud is unreachable, nothing
changes until `valid_until` — an appliance with a 1-year license can run
offline for the year. Grace only matters when validity lapses while offline:
`grace_period_days` of full service, then Expired. Worked example:
[OFFLINE_OPERATION.md](OFFLINE_OPERATION.md).

## 7. Rollback protections (`license/store.go`)

- **License rollback:** installing a document whose `license_version` is lower
  than the highest accepted (or equal, under a different license id), whose
  `issued_at` is older than the installed one, or whose id was revoked, fails
  with `ErrRollback` (`LICENSE_ROLLBACK_REJECTED`) — an old, more generous
  license cannot be replayed after a downgrade or revocation. The high-water
  marks persist in `state.json`.
- **Clock rollback:** the store persists a **high-water mark** of the highest
  wall-clock time observed. If the clock is set back by more than the 48h
  tolerance, evaluation uses the high-water time instead and flags
  `clock_rollback` in the evaluation (surfaced in the Admin Console).
  Winding the clock back cannot resurrect an expiring license.
- Files (`current.json`, `state.json`, `revoked.json`) are written 0600 with
  atomic tmp+rename in a 0700 directory (default `/etc/stayconnect/license`).

## 8. Issuance, delivery, revocation

- **Issue / renew / change terms:** at activation (`POST
  /cloud/v1/appliances/{id}/activate`), then `POST
  /cloud/v1/appliances/{id}/license` (platform admin + step-up) — signs a new
  Document bound to that appliance's registered hardware and identity, with a
  higher `license_version`, superseding the appliance's current licence in the
  same transaction. There is no plan or subscription input.
- **Deliver:** appliance pulls `GET /v1/appliance/license` (Ed25519 appliance
  JWT, ≤60s lifetime; over mTLS once it holds a certificate) →
  `{license_id, envelope, revoked[], server_time}`. Manual path for offline
  sites: **Offline license file** (`POST
  /cloud/v1/appliances/{id}/offline-license`) in Central, uploaded in Admin
  Console (`POST /edge/v1/license`).
- **Revoke:** `POST /cloud/v1/licenses/{id}/revoke` sets the cloud row
  `revoked`; the edge learns via the `revoked[]` list on its next fetch and
  records the id in its local revocation store — `revoked.json` persists
  across restarts and new-license installs.
- **Suspend/resume:** `POST /cloud/v1/licenses/{id}/suspend|resume` re-issues
  with `status: suspended` (billing hold), then `active` — the version
  monotonicity makes the ordering unambiguous.
- Suspending or revoking a licence never changes the appliance's lifecycle in
  Central: the appliance keeps its identity and keeps fetching, which is how
  it receives the suspension and the revocation list.
- **Move** (same customer only): the licence is re-issued for the new site with
  the same terms, as the next version, in the same transaction as the new
  signed assignment. The move is refused (`503 licensing_unavailable`) when
  Central cannot sign or cannot read the licence, and (`409 license_expired`)
  when the licence is already past its end date. **Retire**, **Delete** and
  completion of a **replacement** revoke the appliance's bound licence.

## 9. Enforcement bridge on the edge

On every verified (re)load, scd/edged rewrite the local
`tenant_effective_limits` **table** from the document (`source='license'`):
features → `feature.*` bool rows, limits → int rows. Existing data-plane
queries (concurrency check, operator/plan caps) required no changes — they
read the same keys they always did, now fed by the license instead of the
cloud view. See `data-plane/migrations/0001_edge_init.up.sql`.
