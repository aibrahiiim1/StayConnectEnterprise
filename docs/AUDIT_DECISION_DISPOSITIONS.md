# Audit decisions AD-1 to AD-68: dispositions

**What this is.** The Product Owner's remediation mission of 2026-10-29 acted on every item in [`AUDIT_DECISIONS.md`](AUDIT_DECISIONS.md). This
file records, per item, what was done. It never calls an accepted risk fixed.

Dispositions:

* **IMPLEMENTED**: code and a test. "Code" means merged to master; the deployed state is in the last section.
* **ACCEPTED-RISK**: the risk is kept on purpose, with the reason.
* **DOC-ONLY**: the disposition is a documented rule or checklist.
* **EVIDENCE-DEPENDENT**: needs a measurement or an environment the delivery could not reach; what to run is stated.

Product-Owner overrides of the list's recommendations are marked **PO**. Test names are in the commit messages of the delivery branch
`delivery/audit-decisions-ad1-68`; the migration numbers are the renumbered ones (appliance 0162–0178, Central 0059–0062).

## 1. Security and identity

| AD | Disposition | What was done |
|---|---|---|
| 1 | IMPLEMENTED | Activation code derived from the identity key, shown in the Admin Console, required by Activate and offline import; a waiting row's key is never replaced. TPM2 is not required (PO). |
| 2 | IMPLEMENTED | The appliance pins the appliance Root CA installed at provisioning. An appliance with no pin file pins the root of the bundle it holds (weaker, until the file is installed). |
| 3 | IMPLEMENTED | Per-appliance ledger MAC key (0173) sealed to the backup key holders; `ledger_apply` accepts only checked entries; `ledger_confirm` revoked from edged. Unauthenticated old entries are sealed only when the database and both copies agree. Restore drills on Linux: EVIDENCE-DEPENDENT. |
| 4 | IMPLEMENTED | Customer users see "OneGate support", with no staff identity, address or reason. |
| 5 | IMPLEMENTED | Resetting another administrator needs `platform_owner`. |
| 6 | IMPLEMENTED | Second-step lock per operator and address with an account-wide backstop (Central 0060). |
| 7 | IMPLEMENTED | `guest_data.listed` per list page view, filter without search text, deduplicated. |
| 8 | IMPLEMENTED (L2) / ACCEPTED-RISK (L4, L6, P4 L8) | A desk guest-password reset needs a bounded reason. Operator-lockout by name, no step-up on network confirm and scd's own MFA count are kept as decided and documented. |
| 9 | IMPLEMENTED (**PO**) | Only encrypted `db-*` dumps are downloadable; other files are listed, not downloadable. Unencrypted database backups are refused. |
| 10 | IMPLEMENTED | A tenant move is refused without a backup key holder; the compliance archive is never written unencrypted. |
| 11 | DOC-ONLY | The replay cache stays in process (single ctrlapi); Redis is required before scaling out. |
| 12 | IMPLEMENTED | Catalogue test that every `iam_v2` table has tenant and site keys, plus a ratchet on queries without a tenant filter. 150 existing sites are recorded for review, not claimed safe; the list may only shrink. |
| 13 | IMPLEMENTED | Operators see generic SMTP failure text; the server's words go to the journal. |
| 14 | DOC-ONLY / EVIDENCE-DEPENDENT | Owner checklist in [`runbooks/AUDIT_APPLIANCE_ADMIN_SECURITY.md`](runbooks/AUDIT_APPLIANCE_ADMIN_SECURITY.md). Whether the old Gmail app password was revoked is an owner action nobody can verify from the repository. |

## 2. Licensing and activation

| AD | Disposition | What was done |
|---|---|---|
| 15 | IMPLEMENTED | Install refuses unbound v4 licences, uncomparable identity fields and a licence for another customer or site than the signed assignment. A running unbound licence keeps running (no appliance is bricked). Signed offline licences work with no Central contact until their expiry. |
| 16 | IMPLEMENTED | Revocations are accepted only over mutual TLS. The signed revocation document stays a later protocol change. |
| 17 | IMPLEMENTED | Audited operator reset of the clock high-water mark to Central's authenticated time (`POST /license/clock-reset`); rollback protection is unchanged. No UI button yet. |
| 18 | IMPLEMENTED | The offline-package ledger row is written only with a successful install; first activation is refused for any held signed assignment, including a revoked one. |
| 19 | IMPLEMENTED | New registrations with the constant hardware anchor are refused; existing fingerprints are untouched. |
| 20 | IMPLEMENTED | Cap on waiting registrations as an audited Central setting (default 200), 503 and one alert. No auto-prune. No console screen for the setting yet (API only). |
| 21 | DOC-ONLY | Hostname kept until retirement; `revocation_delivered_at` means "sent"; 204 kept. |

## 3. Networking

| AD | Disposition | What was done |
|---|---|---|
| 22 | IMPLEMENTED; kernel behaviour EVIDENCE-DEPENDENT | Signed-in guests cannot reach RFC 1918, 100.64/10 or 169.254/16 through the appliance by default; audited allow-list; walled garden, custom DNS, WAN gateway and resolvers stay reachable. No Admin Console screen for the allow-list yet; it is set through `enforcement_policy_set()`. |
| 23 | IMPLEMENTED (b); (a) ships OFF | The legacy `auth_ipv4` accept is gone. MAC binding of the Phase-3 set exists, off by default (`mac_binding_enabled`) until proven on the test appliance; IP-only is the fallback. |
| 24 | IMPLEMENTED | Conntrack flows of an address that left the set without a revoke are flushed, scoped to that address (off-switch `NETD_FLUSH_LAPSED=false`). |
| 25 | ACCEPTED-RISK | IPv6 forwarding is left as it is (PO: no IPv6 redesign); recorded in the handbook §10.4. |
| 26 | IMPLEMENTED; load test EVIDENCE-DEPENDENT | The per-source connection cap stays a protection constant. The global bottleneck found was scd's pool of 4 connections and an instant refusal at 64 in flight: pool 16 and a 2 s queue before "busy". `scripts/signin-burst-test.sh 500` is the load test for 300–500 simultaneous clients. |
| 27 | IMPLEMENTED (label) / deferred | The isolation switch is labelled as depending on the access point. Enforcing isolated bridge ports is not done. |
| 28 | IMPLEMENTED (a) / EVIDENCE-DEPENDENT (b) | netd honours `revision_id`; verify pings the DHCP-given gateway. Systemd hardening of the netd unit needs a test on the appliance. |

## 4. Guest authentication and PMS semantics

| AD | Disposition | What was done |
|---|---|---|
| 29 | **ACCEPTED-RISK** (**PO**) | The voucher format and behaviour are unchanged. Residual risk: short digits-only vouchers can be guessed by a device that rotates its MAC; limits are the existing per-device throttle. Only the `ModeComplex` alias is fixed. Text in the `codegen` package comment. |
| 30 | IMPLEMENTED | Code limits counted per destination and device with a destination-wide ceiling; caps and lifetime are settings. |
| 31 | IMPLEMENTED | Recently successful devices are exempt from the room sign-in pause; default raised to 60; staff get an audit entry when a pause starts. |
| 32 | IMPLEMENTED | The durable throttle is always on in production builds. |
| 33 | IMPLEMENTED | NFC on both sides; migration 0164 re-normalises stored names. |
| 34 | IMPLEMENTED | Device-retention setting with pruning that skips referenced devices. |
| 35 | IMPLEMENTED | Production stubs report "not delivered"; a "one moment" notice with one retry; "busy" after about 2 s; in all six portal languages. |
| 36 | IMPLEMENTED (**PO**: replaced) | The Post-stay PIN is replaced by room number plus reservation number bound to the original stay; a new device also gives the last name; lockout by stay and device; one generic refusal. PIN paths removed; historical `pin_*` data kept. [`architecture/ONEGATE_POST_STAY_SIGN_IN.md`](architecture/ONEGATE_POST_STAY_SIGN_IN.md). |
| 37 | IMPLEMENTED (step 2) / ACCEPTED (step 4) | Unchanged GI/GC records only re-stamp the evidence and keep lineage. The hotel-wide feed rule stays; measure after deployment. |
| 38 | IMPLEMENTED | Middle RESYNC copies keep their hash, not their text (0171); reversible. |
| 39 | IMPLEMENTED | The legacy PMS provider layer and scd's three admin routes are removed; the plaintext columns drop only if every row is empty (0170). Run the count in the migration comment on each appliance. |
| 40 | IMPLEMENTED | Loopback, link-local and localhost REST PMS hosts are refused; RFC 1918 is allowed. |

## 5. Financial (no live provider call, no PMS posting)

| AD | Disposition | What was done |
|---|---|---|
| 41 | IMPLEMENTED | Backoff and parking in HELD_RECOVERY after N declines (N=10, a setting) (0167). |
| 42 | IMPLEMENTED | A refused reversal opens a `REVERSAL_REFUSED` review case; the running total is subtracted (0168). |
| 43 | IMPLEMENTED, dormant | A read-only late-capture re-query, off unless its execution switches are set; opens a `LATE_CAPTURE` case and grants nothing. Never exercised live. |
| 44 | IMPLEMENTED | `Engine.Execute`, `HandleProviderNotification`, the intents and `applyTrusted` are deleted. |
| 45 | IMPLEMENTED | A check-out boundary before arrival or before the last applied event is clamped and marked suspect. |
| 46 | IMPLEMENTED | `operator_reason` column under staff-notes retention; the landing package honours the destination interface (0169). |

## 6. Performance targets and settings

| AD | Disposition | What was done |
|---|---|---|
| 47 | IMPLEMENTED | Cut-off within 5 s as a setting; acctd runs accounting, expiry and a newest-plan submitter as separate tickers. |
| 48 | IMPLEMENTED (partial) | Per-minute (7 days) and hourly roll-ups; purge time budget as a setting (0175–0177). `public.accounting_records` is not retired (still referenced); raw delete throughput at design load is EVIDENCE-DEPENDENT. |
| 49 | IMPLEMENTED (partial) | Scrubbed one-time codes, room attempts and redirect states are deleted (0178); purposes documented. The guest export's audit read is not bounded (needs a platform-role index). |
| 50 | PARTIAL / EVIDENCE-DEPENDENT | `log_parameter_max_length=0` shipped. Timeouts, pools and memory limits wait for the measurement plan ([`runbooks/DB_BUDGET_MEASUREMENT_PLAN.md`](runbooks/DB_BUDGET_MEASUREMENT_PLAN.md)). |
| 51 | IMPLEMENTED | Sign-in ceiling follows the licence guest cap (floor 600/min); one-round-trip throttle; Argon2id 64 MiB kept. |
| 52 | IMPLEMENTED | 5 s settings cache invalidated by NOTIFY, TTL as fallback. |
| 53 | IMPLEMENTED | An unchanged plan only refreshes validity; full walk every 30 s (off-switch `NETD_PLAN_FASTPATH=false`). |
| 54 | IMPLEMENTED | Replacement window and retirement-ack wait are audited Central settings; not-modified fetches are counted per hour. |
| 55 | IMPLEMENTED | `/cloud/v1/appliances` pages of 100; callers without `limit` or `page` are unchanged. |
| 56 | IMPLEMENTED | 30-day default windows for ended sessions and the audit list; older history on request. |
| 57 | IMPLEMENTED | Per-IP 15 s negative cache of the access-status answer. |
| 58 | IMPLEMENTED (**PO**) | Portal logo and background: 1 MB upload limit, background optimisation (long edge 2,560 px), quality kept. |
| 59 | EVIDENCE-DEPENDENT | Decided after the Phase 11 S8 measurement; not started. |

## 7. Platform and toolchain

| AD | Disposition | What was done |
|---|---|---|
| 60 | IMPLEMENTED | Go 1.26 and toolchain go1.26.10 in all three modules; `golang:1.26.10` pinned by digest; x/net v0.62.0 and x/crypto v0.58.0 (v0.56.0 conflicts with x/net). |
| 61 | IMPLEMENTED | Golden-vector tests run in both modules. |
| 62 | IMPLEMENTED | `-- edge-migrate: no-transaction` mode for index builds; the marker guard stays the ledger guard. |
| 63 | IMPLEMENTED | Redis behind the compose profile `central-dev`. |

## 8. Operations

| AD | Disposition | What was done |
|---|---|---|
| 64 | IMPLEMENTED | `*_DB_URL` is required; the installer backfills and reads back before any restart. |
| 65 | IMPLEMENTED, **not as decided** | A delete archives the lifecycle events, certificates, certificate requests and assignment moves in the same transaction (Central 0062). `RESTRICT` plus soft delete was not used: it needs a filter in about 60 queries and would block the serial from ever registering again. |
| 66 | IMPLEMENTED | A missing portal key group fails the install and raises an alert. |
| 67 | IMPLEMENTED | Shortening a retention period shows a dry-run count and needs the number confirmed (0174). |
| 68 | DOC-ONLY | The 2026-07-11 hardening list is marked superseded by the installation manual. |

## 9. Items from the "Not decisions" table

| Work | Disposition |
|---|---|
| Migration 0160 up script | already restored in master (PR #264) |
| P5 L11 index, P6 M1, P6 M3, D1-L3 fillfactor, P3 L6/CC3 checksum | M1 and M3 IMPLEMENTED (AD-41, AD-42); the rest not done |
| Provider and guest-device settings step-up (P9 M2, L16) | IMPLEMENTED (password step-up) |
| Restore checks (P9 M3, P4 CC4) | IMPLEMENTED (schema comparison before swap); Linux restore drills EVIDENCE-DEPENDENT |
| Central least privilege (P3 M1, P3 L2) | EVIDENCE-DEPENDENT (role split and a production Central deployment) |
| Enforcement-core redesign (D4-*, D5-M1, D6-H1 step 3, D6-M2 step 2) | partly addressed by AD-47 and AD-53; the rest is not done |
| Measurements first (D2-M4, D2-L3, P3 M6) | EVIDENCE-DEPENDENT |

## 10. Deployment and verification (2026-10-29)

Deployed to the two authorised environments and read back from the installed artifacts; the full record is installation manual A3.10.

| Environment | Release | Read back |
|---|---|---|
| OneGate Central `41.128.240.133` (private `172.21.96.96`) | `onegate-central-ee2faf881d89` | `DEPLOYED.json`: ctrlapi and console source commit `ee2faf881d89`; migrations 0059–0062; smoke 10 PASS |
| Test OneGate Appliance `172.21.96.97` | `onegate-appliance-1cd37145c468` | `RELEASE.json` and all six binaries: `1cd37145c4687d337fe922bf9c23ca3e786ec1c3`; migrations through 0178 |

Verified live: the appliance upgrade (after a safe stop at 0162 that PR #266 fixed), the licence over mutual TLS with the pinned Root CA,
the client journey (12/12), private-network denial (AD-22), MAC binding on, off and against a spoof (AD-23), 300 and 500 simultaneous
sign-in attempts without a busy answer (AD-26). **Not** verified live: the 5-second cut-off (AD-47) and plan fast path (AD-53) under
load, the conntrack flush (AD-24), the room-and-reservation Post-stay flow (AD-36, dark), the image optimiser through the Admin Console
(AD-58), Linux restore drills (AD-3), offline activation (AD-18), and every EVIDENCE-DEPENDENT row above.
