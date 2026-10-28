# Product-Owner decision list (audit remediation, 2026-10-28)

**What this is.** These are the decisions left open by the 2026-10-28 audit remediation mission. It covers the backend audit (`docs/audit/`), the performance audit (`docs/performance-audit/`) and the 2026-07-11 hardening list.
- Every item comes from the "Decisions needed" sections of the eight workstream records (Central, scd, portald, acctd, pmsd, netd, edged, DB/tooling) and from the coordinator's overrides.
- Duplicates are merged into one item.
- The status of each row is in `AUDIT_BACKLOG.md`. A row is either OPEN-DECISION (nothing fixed yet), or PARTIAL with this item as its remainder.
- The code is on branch `claude/vigilant-keller-sag14r`. **Nothing is deployed.**

Each item gives its finding ids, the risk if it is left, the options and a recommendation. The recommendation is the owning workstream's unless it says otherwise. "SQL ready" means the exact SQL is in a workstream record, not in a migration file on the branch.

## Start here

**The five backend High findings**

| Finding | Status | Item |
|---|---|---|
| P0-2: Go toolchain and libraries carry reachable vulnerabilities | PARTIAL. Libraries updated (`1bacd214`); reachable advisories fell from 38 to 23 on the appliance and from 20 to 16 on Central. The rest needs Go 1.26 | AD-60 |
| P1 H1: a missing CA file makes Central mint a new root CA | FIXED (`a7c35b66`, installer `ce245d10`) | none |
| P1 H2: an appliance is activated on facts anyone can know | OPEN-DECISION | AD-1 |
| P2 H1: licence anti-replay and revocation records can be lost | FIXED (`0020bf2c`) | none |
| P8 H1: a network name can inject firewall rules | ALREADY-FIXED (`60e15df7`) | none |

**The most security-relevant decisions:** AD-1 (activation code), AD-60 (Go 1.26), AD-2 (pin the mTLS trust anchor), AD-3 (erasure-ledger authenticity), AD-22 (which private networks guests can reach), AD-23 (bind authorization to the MAC), AD-15 and AD-16 (licence binding and revocation transport).

---

## 1. Security and identity

### AD-1 · Activation proven by a code shown on the appliance
- **Findings:** P1 H2 (High).
- **Risk if left:** anyone who knows a serial or WAN MAC can squat on, or take over, a waiting appliance row. They then receive the customer's assignment, licence, client certificate and portal certificate.
- **Options:**
  - (a) A code derived from the identity key, shown in the Admin Console, required by Activate and by offline import; key swaps on waiting rows refused.
  - (b) Refuse every key swap on a waiting row, with no code.
  - (c) Keep as is.
- **Recommendation:** (a), done with the Phase 2 appliance work. It touches Central, data-plane and the hotel-admin UI.

### AD-2 · Pin Central's mTLS trust anchor at provisioning
- **Findings:** P2 M8.
- **Risk if left:** the appliance accepts as `ca.crt` whatever Central's response carries, so whoever controls that response controls the trust anchor.
- **Options:**
  - (a) Pin `appliance-root-ca.crt` at provisioning and accept only chains to it.
  - (b) Keep.
- **Recommendation:** (a). It is a trust-root and provisioning change, so it is CONTROLLED.

### AD-3 · Erasure-ledger authenticity (external copy and backup marker)
- **Findings:** P4 M1 (= P3 M4), P4 L14, P3 L3, P3 L8.
- **Risk if left:** whoever controls the external ledger storage can make a restore erase chosen guest records. A backup is authenticated only by a digest marker in the same directory.
- **Options:**
  - (a) Per-appliance MAC key, with a copy sealed to the backup key holders. A MAC on entries and the head note. `ledger_apply` accepts only checked entries; `ledger_confirm` is revoked from `svc_edged`.
  - (b) Keep, and document the trust in external storage.
- **Recommendation:** (a), as the audit says. It needs a migration, Linux restore drills and a plan for existing copies that carry no MAC (CONTROLLED: destructive data).

### AD-4 · What customer users see of Central staff
- **Findings:** P1 M4, P1 M5.
- **Risk if left:** hotel users see Central staff emails, staff ids, IP addresses and internal reasons.
- **Options:**
  - (a) Outside platform scopes, show "OneGate support" and drop reason, IP, user agent and free-text payload fields.
  - (b) Show only the action and the time.
  - (c) Keep.
- **Recommendation:** (a). The code change is small (fleet.go `applianceEvents`, system.go `listAudit`).

### AD-5 · Who may reset a Central administrator
- **Findings:** P1 M15.
- **Risk if left:** peer administrators can take each other over by resetting a password, email or two-step setting.
- **Options:**
  - (a) Resetting an administrator needs the owner role.
  - (b) (a) plus a self-service flow.
  - (c) Keep.
- **Recommendation:** (a).

### AD-6 · Two-step sign-in lock per address on Central
- **Findings:** P1 M6.
- **Risk if left:** anyone who knows the password can lock the real user's second step for up to 24 h.
- **Options:**
  - (a) A new table counting per operator and address, with an account-wide backstop at 4× (an additive Central migration, following the 0056 pattern).
  - (b) Shorter lock minutes only.
  - (c) Keep.
- **Recommendation:** (a).

### AD-7 · Record guest-list browsing in the guest-data read audit
- **Findings:** P9 M4 (PARTIAL: missing detail routes recorded and a route-walk test added, `cf3553c9`).
- **Risk if left:** the read log cannot show who browsed the 15 guest, device, session and sign-in lists.
- **Options:**
  - (a) One `guest_data.listed` entry per page view, with the filter but no search text, deduplicated per actor, list and 10 minutes.
  - (b) Record only lists that show contact data.
  - (c) Keep.
- **Recommendation:** (a).

### AD-8 · Operator lockout and step-up details on the appliance
- **Findings:** P9 L4, P4 L8, P9 L6 (PARTIAL), P9 L2 (PARTIAL).
- **Risk if left:**
  - P9 L4: anyone who reaches the console can lock out a named operator.
  - P4 L8: scd keeps no MFA failure count of its own.
  - P9 L6: system-network confirm has no password step-up.
  - P9 L2: a desk reset of a guest password needs no reason.
- **Options:** for each, keep it, or add the control (an address-scoped lock, an scd counter, step-up on confirm, a reason field).
- **Recommendation:**
  - P9 L4: keep and document.
  - P4 L8: rely on edged's limit.
  - P9 L6: no step-up on confirm (apply and rollback already need the password).
  - P9 L2: require a bounded reason, with no step-up.

### AD-9 · Scope of backup downloads
- **Findings:** P9 L7 (PARTIAL: symlinks refused, `c0770d46`).
- **Risk if left:** any file in the backup folder can be downloaded, including deploy and gatep artefacts and unencrypted dumps.
- **Options:**
  - (a) Only `db-*` dumps can be downloaded; other artefacts are listed but not downloadable; unencrypted `.sql.gz` is marked with a warning.
  - (b) (a) and also refuse unencrypted dumps.
  - (c) Keep.
- **Recommendation:** (a).

### AD-10 · Compliance archive without a backup key holder
- **Findings:** P4 L11.
- **Risk if left:** guest data is written to the compliance archive unencrypted.
- **Options:**
  - (a) Block the tenant transition until a key holder exists.
  - (b) Warn only.
  - (c) Keep.
- **Recommendation:** (a).

### AD-11 · Central's appliance-JWT replay cache across restarts and replicas
- **Findings:** P1 L20 (PARTIAL: bounded and O(1), `bea535c9`), SH-7 (PARTIAL). The appliance twin is not reproducible: the appliance has no verifier.
- **Risk if left:** a token replayed right after a ctrlapi restart, or against a second replica, is accepted within its lifetime.
- **Options:**
  - (a) Redis `SET NX EX`.
  - (b) Keep it in-process, now bounded.
- **Recommendation:** (b) while ctrlapi runs as a single replica; (a) before scaling out.

### AD-12 · Tenant isolation in `iam_v2` (no row-level security)
- **Findings:** P3 L11.
- **Risk if left:** isolation rests on every query filtering by tenant and site; one missed filter leaks across tenants.
- **Options:**
  - (a) A catalog test that every `iam_v2` table has tenant/site keys and that every query path filters on them.
  - (b) Enable RLS on `iam_v2` (large, CONTROLLED).
  - (c) Keep.
- **Recommendation:** (a), as the audit recommends. The test belongs to the `internal/iamv2` owners.

### AD-13 · SMTP error text shown to operators
- **Findings:** P5 L7.
- **Risk if left:** a small internal probe: an operator can point the mailer at a LAN service and read its greeting in `last_error`. Operators are step-up gated, so this is Low.
- **Options:**
  - (a) Generic operator text; the server's words go to the journal.
  - (b) (a) plus ports limited to 25, 465, 587 and 2525.
  - (c) Keep (it was deliberate, for internal relays).
- **Recommendation:** (a).

### AD-14 · Credential hygiene outside the code
- **Findings:** SH-1 (PARTIAL), SH-3 (PARTIAL).
- **Risk if left:**
  - SH-1: the Gmail app password once committed stays in Git history; whether it was revoked is not recorded.
  - SH-3: older appliances keep their initdb database password.
- **Options:** SH-1: confirm the revocation in the Google account (an owner action). SH-3: migrate older appliances' passwords, or keep the Overview warning (PO decision 2026-10-19).
- **Recommendation:** confirm the SH-1 revocation now. Keep the SH-3 decision of 2026-10-19 until an upgrade path rotates the password.

## 2. Licensing and activation

### AD-15 · Unbound licences and empty identity fields
- **Findings:** P2 M3, plus the `applianceBound` refusal left over from P2 CC6.
- **Risk if left:** a vendor-signed licence for another hotel, or an unbound one, installs while the local identity fields are blank.
- **Options:**
  - (a) Refuse unbound v4 licences and empty local fields, except at first activation.
  - (b) (a) plus comparing the licence's tenant and site with the assignment.
  - (c) Keep the legacy allowance.
- **Recommendation:** (a)+(b), after confirming that no installed appliance runs on an unbound licence.

### AD-16 · Revocations accepted over plain HTTPS
- **Findings:** P2 M4 (PARTIAL: a failed save now fails the fetch; a null envelope off mTLS is ignored; `0020bf2c`).
- **Risk if left:** a TLS-intercepting proxy can revoke the licence before mTLS exists.
- **Options:**
  - (a) Accept revocations only over mTLS.
  - (b) A vendor-signed revocation document (a protocol change, with Central).
- **Recommendation:** (a) now, (b) with the Central licensing work.

### AD-17 · Licence clock high-water mark
- **Findings:** P2 M1.
- **Risk if left:** one boot with a far-future clock locks the licence until the state file is deleted. The approved test `TestClockRollbackCannotResurrectAnExpiredLicence` encodes today's behaviour.
- **Options:**
  - (a) Keep.
  - (b) A bounded step per local observation, raised by Central's authenticated `server_time`.
  - (c) An audited operator reset "clock was wrong", to Central time.
- **Recommendation:** (c). It keeps today's rollback protection; (b) weakens detection after a long power-off.

### AD-18 · Offline activation packages
- **Findings:** P2 L15, P2 L16.
- **Risk if left:** a failed install burns a package; an appliance holding a revoked assignment can do first activation from a file.
- **Options:**
  - (a) Write the ledger row only together with a successful install.
  - (b) Decide "already assigned" from the assignment state, not from `TenantID != ""`.
- **Recommendation:** both, as one offline-activation semantics change.

### AD-19 · Hardware-id constant anchor
- **Findings:** P2 L6 (PARTIAL: helper commands bounded, `063fcad4`).
- **Risk if left:** cloned VMs, or appliances without DMI, share one hardware id.
- **Options:**
  - (a) Refuse registration for new appliances with the constant anchor; leave existing fingerprints alone.
  - (b) Add DMI placeholders everywhere (this changes existing fingerprints, and licences are bound to them).
  - (c) Keep.
- **Recommendation:** (a).

### AD-20 · Cap on unauthenticated waiting-appliance registrations
- **Findings:** P1 M11 (PARTIAL: field validation, `d9d52a7e`).
- **Risk if left:** unlimited waiting rows from token-less self-registration.
- **Options:**
  - (a) A cap N (default 200) as a §0C setting, plus auto-prune of rows older than M days.
  - (b) The cap only (refuse with 503 and one alert).
  - (c) An alert only.
- **Recommendation:** (b) now. Auto-prune deletes records and is a separate choice.

### AD-21 · Central licensing contract details
- **Findings:** P1 L4 (PARTIAL), P1 L5, P1 CC9 (PARTIAL).
- **Risk if left:**
  - L4: a revoked licence keeps its portal hostname.
  - L5: `revocation_delivered_at` means "sent", not "confirmed".
  - CC9: the assignment fetch answers every refusal with 204.
- **Options:** L4: release the hostname on revoke, or keep it until retirement, or release it after a grace period. L5: confirm by protocol, or rename the column (a migration), or document it. CC9: real error codes, or keep 204.
- **Recommendation:**
  - L4: keep the hostname until retirement or delete.
  - L5: document it as "sent" now; confirm by protocol with the Phase 2 work.
  - CC9: keep 204 unless scd changes at the same time.

## 3. Networking

### AD-22 · Which private networks a signed-in guest can reach
- **Findings:** P8 M2.
- **Risk if left:** on a site whose hotel LAN is 10.x, 100.64/10 or 169.254/16, guests reach the PMS, POS and management networks unless an upstream firewall blocks them.
- **Options:**
  - (a) Deny all of RFC 1918, 100.64/10 and 169.254/16 by default, with an audited allow-list setting (§0C). The walled garden and custom DNS stay ahead of the drop.
  - (b) Keep the current blocks and make the management CIDRs configurable per site.
  - (c) Document that an upstream firewall is required.
- **Recommendation:** (a).

### AD-23 · Bind guest authorization to the device's MAC
- **Findings:** P8 M3, P8 L5, and scd's legacy `auth_ipv4` set.
- **Risk if left:** a device on the same network that sets a signed-in guest's IP by hand gets that guest's access.
- **Options:**
  - (a) Key both nft sets as `ifname . ipv4_addr . ether_addr`, with the MAC from the Kea lease; a device with no known MAC is not authorized.
  - (b) Bind only the Phase-3 set, and retire scd's legacy set first.
  - (c) Rely on access-point isolation and DHCP snooping, and document it.
- **Recommendation:** (b), then (a). It is enforcement-core, so CONTROLLED.

### AD-24 · Open connections outlive the end of an authorization
- **Findings:** P8 M5.
- **Risk if left:** a guest's open TCP or QUIC flows keep passing after the lease ends without a revoke.
- **Options:**
  - (a) Split the established accept so that guest→WAN established traffic must match an authorization (intricate; a mistake cuts sign-in or payment flows).
  - (b) Keep the ruleset, and flush conntrack for every address that left the set between passes.
  - (c) A ct mark at authorization, dropping unmarked flows.
- **Recommendation:** (b) now; (a) later, with a real-kernel test of every pre-auth path.

### AD-25 · IPv6 forwarding on the appliance (sysctl)
- **Findings:** P8 L2. The change `b86a7446` was reverted by the coordinator in `128ee02a`.
- **Risk if left:**
  - The risk of the leftover is small: `net.ipv6.conf.all.forwarding=1` is set although no IPv6 is routed, and the firewall drops guest IPv6.
  - Turning forwarding off has its own effect: the kernel then accepts router advertisements, so the WAN can configure a public IPv6 address. That networking change cannot be verified from the delivery environment.
- **Options:**
  - (a) Forwarding off, plus `accept_ra=0` on the WAN, verified on the test appliance.
  - (b) Forwarding off and accept the RA behaviour.
  - (c) Keep forwarding on (status quo).
- **Recommendation (coordinator):** (c) until (a) can be verified live. Also decide `disable_ipv6` on guest bridges (a netd netplan change; every live network re-applies).

### AD-26 · Portal per-source limits as settings; a new-connection rate limit
- **Findings:** P8 M1 (PARTIAL: 256 connections per source, `fee9b2f6`), P5 M1 step 3 (done).
- **Risk if left:** one device can still open new portal connections fast, up to the connection cap.
- **Options:**
  - (a) Keep the cap as a protection constant; add a rate limit only after measurement, because a captive-check burst could trip it.
  - (b) Make both edged settings (§0C; needs a settings row and edged code).
- **Recommendation:** (a). Revisit (b) if operators ask.

### AD-27 · Client-isolation switch
- **Findings:** P8 L1.
- **Risk if left:** the switch suggests isolation that the appliance does not enforce.
- **Options:**
  - (a) Relabel it as depending on the access point.
  - (b) Enforce `bridge link set … isolated on` on multi-port bridges.
- **Recommendation:** relabel now; enforce on multi-port bridges later.

### AD-28 · Remaining system-network checks and netd hardening
- **Findings:** P8 L14 (PARTIAL: mode and address classes `8031a3d2`; edged sends the revision, `7681873f`).
- **Risk if left:** the gateway and DNS are not checked through the new path; confirm is not yet tied to a revision on the netd side; the netd unit runs without systemd hardening.
- **Options:**
  - (a) netd reports and honours `revision_id` (edged and the console are ready), plus the gateway and DNS checks.
  - (b) (a) plus systemd hardening of the netd unit (`deploy/`).
- **Recommendation:** (a) now. Decide (b) separately, after a test on the appliance.

## 4. Guest authentication and PMS semantics

### AD-29 · Voucher format
- **Findings:** P4 M2, P5 CC2 (codegen `ModeComplex`).
- **Risk if left:** short digits-only vouchers can be guessed by a device that rotates its MAC. `ModeComplex` silently equals `ModeAlnum`.
- **Options:**
  - (a) Length floor 8, a voucher-wide pause setting and per-method global budgets.
  - (b) Floor 8 only.
  - (c) Keep. For codegen: add symbols, use mixed case, or make it an alias.
- **Recommendation:** (a). For codegen: make `ModeComplex` an alias of `ModeAlnum` and fix the comment.

### AD-30 · Guest code (OTP) limits
- **Findings:** P2 M11 (PARTIAL: parallel requests now hold the limits, `bb340fb8`), P2 L23.
- **Risk if left:** anyone who knows a guest's number can use up that guest's 5 codes an hour. The lifetime, attempts and caps are fixed in code.
- **Options:**
  - (a) Count per destination and device, with a higher destination-wide ceiling.
  - (b) Make the caps and the lifetime site settings (§0C).
- **Recommendation:** (a)+(b) together.

### AD-31 · Network-wide room sign-in pause
- **Findings:** P2 M14.
- **Risk if left:** one device can pause room sign-in for a whole guest network.
- **Options:**
  - (a) Keep the pause.
  - (b) Keep it, exempt recently successful devices, raise the default and alert staff.
  - (c) Remove it.
- **Recommendation:** (b).

### AD-32 · Durable guest throttle on by default
- **Findings:** P4 L1.
- **Risk if left:** an appliance installed without `SCD_DURABLE_THROTTLE` has no durable guest rate limit. Provisioning sets it today.
- **Options:**
  - (a) Always on in production builds; the test scripts set a key.
  - (b) Keep the flag.
- **Recommendation:** (a), updating `scripts/*` in the same change.

### AD-33 · NFC normalisation of guest names
- **Findings:** P4 L3.
- **Risk if left:** a name stored in decomposed form never matches what the guest types.
- **Options:**
  - (a) NFC on both sides, plus a migration that re-normalises the `*_norm` columns (it rewrites data).
  - (b) Keep.
- **Recommendation:** (a), and nothing looser.

### AD-34 · Device rows written before the sign-in gates
- **Findings:** P4 L9.
- **Risk if left:** a MAC-rotating device grows `iam_v2.devices` without bound.
- **Options:**
  - (a) Move the upsert after the gates (restructures the room sign-in core).
  - (b) A device-retention setting (§0C), with pruning.
- **Recommendation:** (b).

### AD-35 · What a guest sees when a backend is busy or a provider is broken
- **Findings:**
  - P5 M3 step 3 (PARTIAL: health recorded honestly, `05a3e331`; production stubs still to return `ErrNotDelivered`).
  - D3-L5 (PARTIAL: scd answers 503 with Retry-After, `28bebc5f`).
  - D2-H1 step 2.
- **Risk if left:** guests are told "code sent" when nothing can arrive; a busy appliance reads as "service unavailable"; a queued hash can wait up to 10 s.
- **Options:**
  - (a) Answer "unavailable" at code issue; a translated "one moment" notice with one automatic retry after Retry-After; a "busy" answer after about 2 s.
  - (b) Notices only, no retry.
  - (c) Keep.
- **Recommendation:** (a). It needs wording in every portal language.

### AD-36 · Post-stay PIN lockout and reset
- **Findings:** P6 L10 (PARTIAL: malformed id, `d69a1cdc`).
- **Risk if left:** anyone who knows a profile id can lock it for 15 minutes; a reset re-extends an expired profile.
- **Options:**
  - (a) Key the lockout by profile and device.
  - (b) Keep it per profile.
  - For reset: refuse reset of an expired profile, or keep re-extending.
- **Recommendation:** (a). Keep reset, refusing only when `ValidFor` is 0.

### AD-37 · Room sign-in feed rule while PMS events wait
- **Findings:** D6-H1 steps 2 and 4 (PARTIAL: step 1 applier wake, `69f08295`; step 3 batched apply is deferred-controlled).
- **Risk if left:** one waiting event for any room refuses room sign-in hotel-wide.
- **Options:**
  - Step 4: (a) keep it hotel-wide and measure after steps 1 and 3; (b) refuse only while that stay's or room's event waits; (c) per room for RESYNC rows only.
  - Step 2: do not skip unchanged roster records; or a cheap path that re-stamps occupancy evidence and records lineage; or skip them and change the freshness rule.
- **Recommendation:** step 4 (a) for now, (b) only if closures stay above 1 s after deployment. Step 2: the cheap path that keeps freshness and lineage.

### AD-38 · Superseded RESYNC payloads
- **Findings:** D6-L1.
- **Risk if left:** every reconnect keeps another full copy of every in-house guest's record.
- **Options:**
  - (a) Keep.
  - (b) Reduce payloads of generations older than the latest applied to their evidence hash, keeping rows that changed a stay.
  - (c) Shorter `pms_stay_days` retention for RESYNC rows only.
- **Recommendation:** (b), as a CONTROLLED historical-data migration.

### AD-39 · Retire the legacy PMS provider layer
- **Findings:** P7 L7, plus the legacy remainder of P7 CC1 and CC4.
- **Risk if left:** plaintext provider credentials stay in use, and scd's `/cache` route returns guest rows.
- **Options:**
  - (a) Retire it: remove scd's three admin routes, then `pmsguard`, `pmsloader` and the legacy REST providers, then drop the plaintext columns (a CONTROLLED migration).
  - (b) Keep.
- **Recommendation:** (a), after a read-only check that `pms_providers` is empty on the test appliance.

### AD-40 · Private, loopback and link-local REST PMS hosts
- **Findings:** P7 L4 (PARTIAL: redirects never followed, `765b33d0`). P7 L6 is NOT-REPRODUCIBLE; no action is recommended for it.
- **Risk if left:** a base URL set by an operator can reach internal appliance services.
- **Options:**
  - (a) Refuse them unless a new per-provider `on_site` flag is set.
  - (b) Refuse loopback and link-local only, and allow RFC 1918 (on-premises PMS).
  - (c) Keep.
- **Recommendation:** (b) now; (a) if an on-site flag becomes a product setting.

## 5. Financial

### AD-41 · A refused room charge at the head of a posting lane
- **Findings:** P6 M1. The backoff columns are not integrated; SQL ready.
- **Risk if left:** one refused row blocks every later charge on that PMS interface. An outage burns about 43,000 P#s and attempt rows a day.
- **Options:**
  - (a) Backoff only (2 s doubling to 5 min), with order unchanged.
  - (b) Backoff, plus parking in HELD_RECOVERY after N declines so the lane moves on.
  - (c) Per-row backoff, claiming past backed-off rows.
- **Recommendation:** (b), with N = 10 as a §0C setting. It needs the migration (CONTROLLED: financial and schema).

### AD-42 · A reversal the ledger refuses
- **Findings:** P6 M3 (PARTIAL: the Go half is fixed, `44fd44cb`; the cumulative function is not integrated, SQL ready).
- **Risk if left:** a lost dispute after a partial refund is only logged. Finance never sees the missed chargeback.
- **Options:**
  - (a) Open a `card_reversal_reviews` case.
  - (b) Report it in `FinancialHealth` only.
  - (c) Log only (the state after `44fd44cb`).
- **Recommendation:** (a), shipped with the cumulative-reversal function.

### AD-43 · Recheck failed and unknown card charges for a late capture
- **Findings:** P6 L1.
- **Risk if left:** a capture that happens after a checkout was marked failed is never noticed.
- **Options:**
  - (a) A slow read-only provider re-query over the last N days that opens a review case on a capture.
  - (b) Operator-triggered only.
  - (c) None.
- **Recommendation:** (a). It reads only, but it is a new provider-traffic pattern in LIVE.

### AD-44 · The dormant payment execute and notification path
- **Findings:** P6 L2 (PARTIAL: test doubles are out of the production build, `eb2c314b`), the remainder of P6 CC1.
- **Risk if left:** an unreachable path that would grant without checking the amount stays in the production build.
- **Options:**
  - (a) Delete `Engine.Execute`, `HandleProviderNotification`, the intents and `applyTrusted`, with their tests.
  - (b) Keep it, with amount and currency required in a signed notification.
  - (c) Leave it.
- **Recommendation:** (a), as a separate CONTROLLED change.

### AD-45 · The check-out boundary
- **Findings:** P6 L6.
- **Risk if left:** check-out events are not re-validated, and an old PMS check-out time is trusted. Both decide when paid access ends.
- **Options:**
  - (a) Validate the event, and clamp the boundary to at least the arrival or the last applied event, marking it suspect.
  - (b) Validate only.
  - (c) Leave it.
- **Recommendation:** (a). It is CONTROLLED (entitlement).

### AD-46 · Storing the operator's transfer reason
- **Findings:** P6 L11 (PARTIAL: deterministic landing package, `93b8e6ae`).
- **Risk if left:** the operator's reason for a transfer is not stored with the transfer.
- **Options:**
  - (a) Relax the 0027 trigger so `reason` holds the operator's text (it then falls under 0147's staff-notes erasure).
  - (b) A separate `operator_reason` column under the same retention.
  - (c) Audit log only.
- **Recommendation:** (b). Also decide whether the landing package is filtered by the destination interface.

## 6. Performance targets and settings

### AD-47 · How soon a used-up allowance cuts a guest off (acctd tick and loop split)
- **Findings:** D1-H2, D5-H2, D4-H2 (the acctd half; the redesign itself is deferred-controlled).
- **Risk if left:** acctd writes every online device every second, in one serial loop.
- **Options:**
  - (a) Cut off within 5 s, as a setting; then a newest-plan submitter, plus separate expiry and accounting tickers.
  - (b) Keep 1 s.
- **Recommendation:** (a). It is an enforcement-core redesign and needs AD-53.

### AD-48 · Usage-detail grain, retention and disk budget
- **Findings:** D1-H3, D1-L4, D1-L7, D7-H2, D10-M4, D10-M2 (with D10-D1).
- **Risk if left:** accounting detail outgrows the disk in weeks; Overview charts sum raw per-second rows; the purge cap and the journald sizes are fixed.
- **Options:**
  - (a) Hourly totals for 12 months and per-minute for 7 days; an 80 GB reference disk.
  - (b) Hourly totals only; a 40 GB disk.
  - (c) Keep.
- **Recommendation:** (a), decided jointly with privacy. Then the Overview rollup (D7-H2), the retirement of `public.accounting_records`, and a purge time budget (an interim setting meanwhile).

### AD-49 · What anonymised per-event rows are kept for, and how long
- **Findings:** D1-L5, D11-L1.
- **Risk if left:** several per-event tables are never trimmed, and the guest-data export reads the whole audit log.
- **Options:**
  - (a) Set a purpose and retention for each table, then trim and index (a function redefinition).
  - (b) Keep.
- **Recommendation:** (a), as in performance Phase 1 B7.

### AD-50 · Database, pool and memory budget, and the design targets behind it
- **Findings:** D1-M2, D1-M3, D1-L1, D1-L2, D10-M3; Phase 0 D1/D2 = D10-D2.
- **Risk if left:** Postgres is tuned as if it owned the box; per-role connection limits add up to 85 against 50 allowed; services have no memory limits; there are no DB timeouts.
- **Options:**
  - (a) Apply the audit's starting values now.
  - (b) Measure Phase 11 S1/S2/S10 on a reference VM, then write the values.
- **Recommendation:**
  - (b), keeping the targets (2,000 devices, 300 sign-ins a minute on 2 vCPU / 4 GB; 4 GB minimum, 8 GB for stress load).
  - Exception: ship `log_parameter_max_length=0` and `log_parameter_max_length_on_error=0` with any logging change.

### AD-51 · Sign-in capacity: appliance-wide ceiling and Argon2 cost
- **Findings:** D2-M3, D2-L1; the later Argon2 question of D2-H1.
- **Risk if left:** the ceiling is fixed at 600 a minute, and OTP is charged twice; the durable throttle costs 6–9 statements per attempt.
- **Options:**
  - (a) A ceiling setting whose default follows the licence guest cap (never below 600 a minute), and a single-round-trip DB function.
  - (b) Keep.
  - Argon2: keep 64 MiB, or move to OWASP's lighter setting.
- **Recommendation:** (a). Keep Argon2id 64 MiB, now that the concurrency limiter exists (`f34e969e`).

### AD-52 · Settings cache and NOTIFY on the sign-in path
- **Findings:** D2-L2, D2-CC2, D2-L4.
- **Risk if left:** 5–8 settings reads per sign-in, and a 0.1–1 s poll of netd.
- **Options:**
  - (a) A TTL cache with version invalidation, plus a NOTIFY from netd with the poll kept as fallback.
  - (b) A TTL cache only: an Admin Console "off" then takes effect seconds later.
  - (c) Keep.
- **Recommendation:** (a). Never (b) without version invalidation.

### AD-53 · netd skipping an unchanged plan
- **Findings:** D4-M2.
- **Risk if left:** acctd sends the whole plan every second, and netd walks it every time.
- **Options:**
  - (a) Split lease renewal from the plan walk (netd renews on its own timer under the last plan's `ExpiresAt`); an identical hash only refreshes validity, with a full walk every 30 s.
  - (b) Make each step of the walk change-only (the D4-H2 redesign).
  - (c) Keep.
- **Recommendation:** (a), together with AD-47. It changes the acctd–netd contract (enforcement core).

### AD-54 · Central operational values and the fetch log
- **Findings:** P1 L21, D9-M1 step 1 (D9-M1 is PARTIAL: index 0058 and step 3 are fixed).
- **Risk if left:** a hotel administrator cannot change values that §0C says should be settings; the fetch log records every poll.
- **Options:**
  - (a) Make the replacement window (72 h) and the retirement acknowledgement timeout (10 min) §0C settings, keeping the security limits fixed. Log only served and denied fetches, plus hourly not-modified counts.
  - (b) Keep.
- **Recommendation:** (a) for both.

### AD-55 · Server paging of the Central fleet list
- **Findings:** P1 CC6, D9-M2 step 2, D8-M2 server half (both PARTIAL).
- **Risk if left:** the list loads the whole fleet, and list limits truncate silently.
- **Options:**
  - (a) Pages of 100 with SQL filters and counts, in line with `DeriveState` (changes the `/cloud/v1/appliances` contract).
  - (b) Keep it unpaged; the render cost is mostly solved client-side (`34692b1a`).
- **Recommendation:** (a) when the fleet passes about 300 appliances.

### AD-56 · Default and maximum list windows in the Admin Console
- **Findings:** P9 L12 (PARTIAL), the D7-H1 query follow-ups, the D7-M3 list summaries.
- **Risk if left:** the ended and all-sessions tabs, the audit list and the usage list aggregate all history on every view.
- **Options:**
  - (a) A 30-day default with older history on request, and no hard maximum.
  - (b) A 30-day default plus a maximum custom window.
  - (c) Keep.
- **Recommendation:** (a), after the 0159 indexes are deployed.

### AD-57 · Portal: ask for access status only when it can matter
- **Findings:** D3-M2 step 2 (PARTIAL: single-flight design fetch, `30c6dac3`).
- **Risk if left:** every page view makes extra calls that reach scd and its database.
- **Options:**
  - (a) Ask only when a resume cookie is present.
  - (b) Resume cookie or portald's memory of a sign-in.
  - (c) Keep asking, and cache scd's negative answer per IP briefly.
- **Recommendation:** (c). It keeps the "access ended" notice exact.

### AD-58 · Re-encode uploaded portal images
- **Findings:** D3-L2 (PARTIAL: immutable cache header, `30c6dac3`).
- **Risk if left:** hotel images up to 8 MB are served as uploaded.
- **Options:**
  - (a) Decode, limit to 2,560 px on the long edge and re-encode JPEG/PNG; keep GIF under a byte cap; refuse WebP above the cap. Size and quality as administrator settings.
  - (b) A byte cap only.
- **Recommendation:** (a). It needs a WebP decoder dependency (`golang.org/x/image`).

### AD-59 · Route the console API straight from Caddy to edged
- **Findings:** D8-L3.
- **Risk if left:** every console API call passes through Node.
- **Options:**
  - (a) A Caddy route to edged (a CONTROLLED vhost change).
  - (b) Keep.
- **Recommendation:** decide after the Phase 11 S8 measurement.

## 7. Platform and toolchain

### AD-60 · Go 1.26 toolchain
- **Findings:** P0-2 (= X2; PARTIAL: libraries updated within go 1.25, `1bacd214`).
- **Risk if left:** 23 reachable advisories on the appliance and 16 on Central stay. They are mostly stdlib (net/http, crypto/tls, crypto/x509, html/template), plus x/net and x/crypto, whose fixed versions declare `go 1.26.0`.
- **Options:**
  - (a) `go 1.26` and `toolchain go1.26.10` in all three go.mod files; `GO_IMAGE golang:1.26.10@<digest>`; x/net v0.62.0, x/crypto v0.56.0; full suites; deploy to the test appliance.
  - (b) Wait for a 1.25 point release (it does not fix the 1.26-only advisories).
  - (c) Accept and record the risk.
- **Recommendation:** (a), on its own branch with no other backend branch open (go.sum conflicts).

### AD-61 · Code shared between the appliance and Central modules
- **Findings:** P2 CC3, P2 CC4.
- **Risk if left:** assignment, offline, activation, totp and applianceauth are copied line for line, so a one-sided change breaks verification. Key-id encodings differ.
- **Options:**
  - (a) A shared module, like `license/`.
  - (b) A golden-vector test file run in both modules.
- **Recommendation:** (b) first. The shared module is an architecture choice with no urgency.

### AD-62 · Migration runner: baseline ledger and concurrent indexes
- **Findings:** P3 M2 (PARTIAL: the runner refuses versions the database already has, `8f0cd829`), P3 L9.
- **Risk if left:** a baseline-built appliance has no ledger rows for 0001–NNNN; index builds on large tables hold a write lock inside the transaction.
- **Options:**
  - Ledger: (a) the baseline writes ledger rows, plus a one-time audited adopt on existing appliances; (b) keep the new marker-based refusal as the only guard.
  - Indexes: (a) a `-- edge-migrate: no-transaction` mode with a separate ledger step; (b) maintenance windows.
- **Recommendation:**
  - Ledger: (b) now, (a) at the next baseline regeneration.
  - Indexes: (a), before large sites take 0159-style indexes.

### AD-63 · Redis on the appliance
- **Findings:** D10-L2.
- **Risk if left:** the appliance runs a Redis that nothing uses.
- **Options:**
  - (a) Put it behind a compose profile `central-dev`.
  - (b) Move dev Central to `central-infra.yml` and delete the service.
- **Recommendation:** (a).

## 8. Operations

### AD-64 · Remove the published default DB URLs from scd, edged and netd
- **Findings:** P6 L14, X3 (both PARTIAL).
  - Done: ctrlapi `bea535c9`, acctd `4c7fd5e3`.
  - The installer backfills a missing `*_DB_URL` into an existing env file (`03650f13`).
- **Risk if left:** a service with no env value connects with a published password and TLS off. If the variable is required too early, netd stops on an older appliance, and enforcement stops with it.
- **Options:**
  - (a) Require the variables now.
  - (b) Keep the fallback with a warning.
  - (c) After one upgrade with `03650f13`, and a read-back on the test appliance (`grep -c '^EDGED_DB_URL=' /etc/stayconnect/edged.env`, and the same for scd and netd), require them.
- **Recommendation:** (c).

### AD-65 · Keep a deleted Central appliance's certificate and lifecycle history
- **Findings:** P3 L10. P1 M8 is already safe without it: migration 0057, `7cb83fec`.
- **Risk if left:** deleting an appliance cascades away its certificate and lifecycle history.
- **Options:**
  - (a) `RESTRICT` plus soft delete.
  - (b) Keep the cascade.
- **Recommendation:** (a), decided together with the retention rules.

### AD-66 · Portal TLS install when the key group is missing
- **Findings:** P2 L19 (PARTIAL: stale temporaries removed and the host checked, `d6a78845`).
- **Risk if left:** the key is installed root-only, portald cannot serve it, and there is only a warning.
- **Options:**
  - (a) Fail the install and alert.
  - (b) Keep the warning.
- **Recommendation:** (a).

### AD-67 · Impact preview before shortening a retention period
- **Findings:** P9 L14 (PARTIAL: an unreadable old value now refuses the change, `eedc777c`).
- **Risk if left:** an operator shortens a period without seeing what will be removed.
- **Options:**
  - (a) A dry-run count per category, using the purge functions' own rules, with a second confirmation.
  - (b) A warning text only.
  - (c) Keep.
- **Recommendation:** (a). It touches CONTROLLED retention SQL.

### AD-68 · Re-assess the 2026-07-11 hardening list's target-architecture items
- **Findings:** SH-8.
- **Risk if left:** a stale checklist stays "open" next to the installation manual's verification checklist.
- **Options:**
  - (a) Re-assess it against the manual (Part D, F.5) and close or carry each item.
  - (b) Declare it superseded by the manual.
- **Recommendation:** (b), recorded as a docs task.

---

## Not decisions: controlled work that needs an authorization

These rows are OPEN, not OPEN-DECISION. The fix is known; it waits for a mission that names the controlled boundary.

| Work | Findings | Boundary |
|---|---|---|
| Re-commit migration 0160's up script | D6-M1 | Migration. The committed up script is a comment only; the SQL is in the pmsd record |
| `public.audit_log` index | D7-H1 (part) | A platform-role step on a live site |
| SQL ready, not integrated | P5 L11 index, P6 M1 backoff (AD-41), P6 M3 function (AD-42), D1-L3 fillfactor, P3 L6/CC3 ledger checksum | Migration, financial (P6) |
| Provider and guest-device settings step-up | P9 M2, P9 L16 | Guest authentication |
| Restore checks | P9 M3, P4 CC4 | Destructive data, restore drills |
| Central least privilege | P3 M1, P3 L2 | Central migration plus production Central deployment |
| Enforcement-core redesign | D4-H1, D4-H2, D4-H3, D4-M1, D4-M3, D4-L2, D4-CC1, D5-M1, D6-H1 step 3, D6-M2 step 2 | Enforcement core, PMS protocol; reference-VM benchmarks |
| Measurements first | D2-M4, D2-L3, P3 M6 | A named test environment (Phase 11) |
