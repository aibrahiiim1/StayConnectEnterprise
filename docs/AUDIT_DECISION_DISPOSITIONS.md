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
| 2 | IMPLEMENTED | `appliance-install.sh` pins the appliance Root CA on a fresh install and on `--upgrade`, and **only against a fingerprint**: the one the operator reads from Central out of band (`--root-ca-sha256`), or the one in the trust directory's `FINGERPRINTS.txt` (stated to come from the same place as the certificate). A certificate nothing vouches for is never adopted, a pin that exists is never replaced (a different root is a Root CA rotation, a Product-Owner decision), and an upgrade of an appliance provisioned before the pin existed can pin the self-signed root of the bundle it already holds when that root has the named fingerprint. `install-appliance-root-ca.sh --sha256` refuses a mismatch and pins nothing. An appliance with no pin still pins the root of the first bundle it receives (scd warns): the installer now says so loudly instead of leaving it silent. No Root CA was replaced. |
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
| 14 | DOC-ONLY | Owner checklist in [`runbooks/AUDIT_APPLIANCE_ADMIN_SECURITY.md`](runbooks/AUDIT_APPLIANCE_ADMIN_SECURITY.md). **The Product Owner confirmed on 2026-10-29 that the old Gmail application password has been changed.** That is recorded as the owner's statement: nobody can verify an external credential's revocation from the repository, and this mission does not claim to have. Not asked for again. |

## 2. Licensing and activation

| AD | Disposition | What was done |
|---|---|---|
| 15 | IMPLEMENTED | Install refuses unbound v4 licences, uncomparable identity fields and a licence for another customer or site than the signed assignment. A running unbound licence keeps running (no appliance is bricked). Signed offline licences work with no Central contact until their expiry. |
| 16 | IMPLEMENTED | Revocations are accepted only over mutual TLS. The signed revocation document stays a later protocol change. |
| 17 | IMPLEMENTED | Audited operator reset of the clock high-water mark to Central's authenticated time (`POST /license/clock-reset`), now with a **Licence clock** card on Appliance & licence (reason required, `license` write permission, Central's refusal shown as it is). Rollback protection is unchanged. Licensing semantics are unchanged: Central silence only raises the `CloudStale` warning once the licence's `offline_grace_days` (default 30) pass, and never changes the licence state; 48 hours is the clock-rollback tolerance, a different thing ([`LICENSING_AND_ENTITLEMENTS.md`](LICENSING_AND_ENTITLEMENTS.md) §4, §6, §7; `license/grace_period_acceptance_test.go`). |
| 18 | IMPLEMENTED; verified live (renewal) | The offline-package ledger row is written only with a successful install; first activation is refused for any held signed assignment, including a revoked one. **Live, test pair, 2026-10-29:** a signed offline licence file from Central was imported on the test appliance (200, licence installed, single-use row written, reconciled with Central on the next pass) and the same file was refused the second time (409, already consumed); a file wrapped as Central downloads it, without the console's unwrap, was refused for its signature. A first activation by an offline *activation package* needs an appliance that is not yet activated and was not exercised. |
| 19 | IMPLEMENTED | New registrations with the constant hardware anchor are refused; existing fingerprints are untouched. |
| 20 | IMPLEMENTED | Cap on waiting registrations as an audited Central setting (default 200, 10–5000), 503 and one alert. No auto-prune. The Central console shows it (Team → Appliance registration) with the count waiting now, the bounds and a required reason. |
| 21 | DOC-ONLY | Hostname kept until retirement; `revocation_delivered_at` means "sent"; 204 kept. |

## 3. Networking

| AD | Disposition | What was done |
|---|---|---|
| 22 | IMPLEMENTED; verified live | Signed-in guests cannot reach RFC 1918, 100.64/10 or 169.254/16 through the appliance by default; audited allow-list; walled garden, custom DNS, WAN gateway and resolvers stay reachable. Admin Console: **Network → Client access protection** (the five enforcement settings, bounds, reason and the operator's password required, history; turning the denial off needs the typed confirmation). Verified on the test appliance (installation manual A3.10). |
| 23 | IMPLEMENTED (b); (a) ships OFF; verified live | The legacy `auth_ipv4` accept is gone. MAC binding of the Phase-3 set exists, off by default (`mac_binding_enabled`); IP-only is the fallback. Verified live on, against a spoofed MAC and off (A3.10). Switched from Client access protection. |
| 24 | IMPLEMENTED; verified live | Conntrack flows of an address that left the set without a revoke are flushed, scoped to that address (off-switch `NETD_FLUSH_LAPSED=false`). Live, 2026-10-29: after a client's allowance ended, no forwarded flow to or from its address remained in conntrack (only the gateway's own DNS entry, which expires by timeout). |
| 25 | ACCEPTED-RISK | IPv6 forwarding is left as it is (PO: no IPv6 redesign); recorded in the handbook §10.4. |
| 26 | IMPLEMENTED; verified live | The per-source connection cap stays a protection constant. The global bottleneck found was scd's pool of 4 connections and an instant refusal at 64 in flight: pool 16 and a 2 s queue before "busy". Live on the 1-vCPU test appliance, 2026-10-29: 500 and 300 simultaneous attempts, all answered, 0 busy, 0 timeouts, p95 66 ms and 57 ms, at most 15 database connections of 100 allowed in total and 7 for scd (limit 20). The attempts used unknown synthetic devices, so they measure the request path and the database, not successful sign-ins (the journey test proves those). |
| 27 | IMPLEMENTED (label) / deferred | The isolation switch is labelled as depending on the access point. Enforcing isolated bridge ports is not done. |
| 28 | IMPLEMENTED | netd honours `revision_id`; verify pings the DHCP-given gateway. (b) The netd unit is hardened with the directives it provably never needs (`ProtectHome`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectClock`, `LockPersonality`, `RestrictRealtime`, `RestrictSUIDSGID`, `SystemCallArchitectures`): systemd exposure 9.6 → 7.8, netd active, no denied operation, the client journey 12/12 on the hardened unit. `NoNewPrivileges` stays off and netd stays root, because it starts helpers; a stricter profile needs the full helper list and is not attempted. |

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
| 36 | IMPLEMENTED (**PO**: replaced) | The Post-stay PIN is replaced by room number plus reservation number bound to the original stay and its PMS interface. A device that was never on the stay also gives the last name; **the last name is an extra check, not identity proof** ([`architecture/ONEGATE_POST_STAY_SIGN_IN.md`](architecture/ONEGATE_POST_STAY_SIGN_IN.md)). Found and fixed on the way: nothing limited how long after check-out the sign-in worked, so a stay left months ago opened a fresh profile. **New setting `post_stay_signin_days`** (migration 0179; default 7, 1–30, per site, audited, in Hotel Admin → sign-in limits). Isolated tests (throwaway full-schema database, no PMS traffic): lineage sign-in, uniform refusal, no cross-stay guessing, new device needs the name, lockout per stay and device, revoked and orphaned profiles, **a stay past the window, the room's next occupant, and the same room and reservation on another PMS interface**; scd's end-to-end test covers the conversion into an enforced session with its entitlement. Lockout by stay and device; one generic refusal. PIN paths removed; historical `pin_*` data kept. **Closure 2026-10-30** ([`reviews/AD36_POST_STAY_CLOSURE.md`](reviews/AD36_POST_STAY_CLOSURE.md)): a returning or further device joins the stay's one Entitlement (no reset; the Post-Stay benefit is granted once per stay); refusals are audited without guest data; the throttle limits are settings (migration 0190); a rotating device identity is bounded by room and reservation. |
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
| 47 | IMPLEMENTED; verified live | Cut-off within 5 s as a setting; acctd runs accounting, expiry and a newest-plan submitter as separate tickers. Live, 2026-10-29: a client with a 3 MB allowance downloaded past it; acctd ended the access 2.7 s after the allowance was spent (`reason: DATA`, effective time recorded), and the next new connection was refused. |
| 48 | IMPLEMENTED (partial) | Per-minute (7 days) and hourly roll-ups; purge time budget as a setting (0175–0177). `public.accounting_records` is not retired (still referenced). Raw delete throughput was measured at design load on the 1 vCPU test appliance (2026-10-29, scratch database, 3,000,000 samples): about 9,700 samples a second, 95,000 to 100,000 per 10 s run, so the purge removes at least 14 million a day against 5.8 million arriving (`docs/runbooks/DB_BUDGET_MEASUREMENT_PLAN.md`). **Found by that run and fixed:** acctd called the roll-up with its one-day default window under a 30 s statement timeout, so a roll-up stopped for about 10 hours at design load could not catch up; acctd now reads two-hour windows. **Usage after the raw purge:** 0180 keeps an Entitlement's usage when raw samples are purged; 0181 closes what review of it found (a sample-sequence restart that let an idle-then-active session pass its data quota, conservative reads where history is folded, an audited integrity trail), see [`reviews/ACCOUNTING_INTEGRITY_CLOSURE.md`](reviews/ACCOUNTING_INTEGRITY_CLOSURE.md). |
| 49 | IMPLEMENTED (partial) | Scrubbed one-time codes, room attempts and redirect states are deleted (0178); purposes documented. The guest export's audit read is not bounded (needs a platform-role index). |
| 50 | IMPLEMENTED (measured; no change needed) | `log_parameter_max_length=0` shipped. **Per-role timeouts already exist** in Gate-P (`statement_timeout`, `lock_timeout`, `idle_in_transaction_session_timeout` for scd, edged, acctd, netd, pmsd; 30 s on the payment roles): the measurement plan's assumption that there were none was stale. Measured on the 1-vCPU / 8 GiB test appliance (2026-10-29, burst of 500 then 300): PostgreSQL `max_connections` 100 against per-role limits adding to 85, so the "85 against 50" finding does not hold; at most 15 connections in use; no transaction open longer than the 0.4 s sampling step; resident memory 13–27 MiB for scd, portald and acctd, 25–156 MiB for edged (garbage collection moves it) and 18–90 MiB for netd (restarted in between), PostgreSQL backends about 0.7 GiB with shared buffers 2.5 GiB. No pool, timeout or memory limit is changed: nothing measured is near a limit. `MemoryMax` is deliberately not set (an OOM kill of netd would cut enforcement). The 2,000-client simulated run was done on 2026-10-29/30: see section 13 and `runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md`. |
| 51 | IMPLEMENTED | Sign-in ceiling follows the licence guest cap (floor 600/min); one-round-trip throttle; Argon2id 64 MiB kept. |
| 52 | IMPLEMENTED | 5 s settings cache invalidated by NOTIFY, TTL as fallback. |
| 53 | IMPLEMENTED | An unchanged plan only refreshes validity; full walk every 30 s (off-switch `NETD_PLAN_FASTPATH=false`). Unit-tested; exercised on every acctd pass during the live tests; no separate latency measurement was made. |
| 54 | IMPLEMENTED | Replacement window and retirement-ack wait are audited Central settings; not-modified fetches are counted per hour. |
| 55 | IMPLEMENTED | `/cloud/v1/appliances` pages of 100; callers without `limit` or `page` are unchanged. |
| 56 | IMPLEMENTED | 30-day default windows for ended sessions and the audit list; older history on request. |
| 57 | IMPLEMENTED | Per-IP 15 s negative cache of the access-status answer. |
| 58 | IMPLEMENTED (**PO**); verified live | Portal logo and background: 1 MB upload limit, background optimisation (long edge 2,560 px by default, quality kept, both settings). Live through the operator API that the console page calls, 2026-10-29: a 3200×1800 PNG of 232 KB was accepted (202), resolved by the optimiser, and stored as 2560×1440; a 1.05 MB image was refused (400, "1 MB or smaller"); an SVG was refused. The console page's own behaviour (polling the id its upload returned) is covered by its UI tests; it was not driven in a browser here. |
| 59 | DECIDED: keep (b) | Measured on the test appliance (1 vCPU), 2026-10-29: a console API call costs about 8–10 ms through Caddy and Node against 3–4 ms straight to edged (one caller), and 16 concurrent callers get about 110 requests a second through Node against about 270 direct, with the load generator on the same single core. A hotel console is a handful of staff; the extra few milliseconds are invisible, and Node carries the session and request checks. A direct Caddy route would be a controlled vhost change for no visible gain. Revisit only if console API load ever grows by orders of magnitude. |

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
| 65 | IMPLEMENTED, **not as decided; ACCEPTED by the Product Owner (2026-10-29)** | A delete archives the lifecycle events, certificates, certificate requests and assignment moves in the same transaction (Central 0062), and keeps its historical-security protections and regression tests. `RESTRICT` plus soft delete was not used: it needs a filter in about 60 queries and would block the serial from ever registering again. The archival approach is final; it is not to be replaced. |
| 66 | IMPLEMENTED | A missing portal key group fails the install and raises an alert. |
| 67 | IMPLEMENTED | Shortening a retention period shows a dry-run count and needs the number confirmed (0174). |
| 68 | DOC-ONLY | The 2026-07-11 hardening list is marked superseded by the installation manual. |

## 9. Items from the "Not decisions" table

| Work | Disposition |
|---|---|
| Migration 0160 up script | already restored in master (PR #264) |
| P5 L11 index, P6 M1, P6 M3, D1-L3 fillfactor, P3 L6/CC3 checksum | M1 and M3 IMPLEMENTED (AD-41, AD-42); the rest not done |
| Provider and guest-device settings step-up (P9 M2, L16) | IMPLEMENTED (password step-up) |
| Restore checks (P9 M3, P4 CC4) | IMPLEMENTED (schema comparison before swap); restore drills run on Linux containers, see section 10 |
| Central least privilege (P3 M1, P3 L2) | IMPLEMENTED, section 11 |
| Enforcement-core redesign (D4-*, D5-M1, D6-H1 step 3, D6-M2 step 2) | partly addressed by AD-47 and AD-53; the rest is not done |
| Measurements first (D2-M4, D2-L3, P3 M6) | measured on the test appliance for AD-50 and AD-59 (section 10, rows 50 and 59); the database half of the 2,000-device run is done (S10 insert, roll-up and purge rates, and the roll-up window defect it found); real devices on the network are not |

## 10. Deployment and verification (2026-10-29)

Two rounds. Round 1 (PRs #265–#267) was deployed to the two authorised environments and read back from the installed artifacts; the full
record is installation manual A3.10. Round 2 (the closure round, D82) added the items marked here.

| Environment | Release | Read back |
|---|---|---|
| OneGate Central `41.128.240.133` (private `172.21.96.96`) | round 1: `onegate-central-ee2faf881d89` | `DEPLOYED.json`: ctrlapi and console source commit `ee2faf881d89`; migrations 0059–0062; smoke 10 PASS |
| Test OneGate Appliance `172.21.96.97` | round 1: `onegate-appliance-1cd37145c468` | `RELEASE.json` and all six binaries: `1cd37145c4687d337fe922bf9c23ca3e786ec1c3`; migrations through 0178 |
| OneGate Central | round 2: `onegate-central-9613fb52679d` | `DEPLOYED.json` release_commit `9613fb52679d904ca0320c5cd9eeac23f06350b1`; migrations through 0063; smoke 9 PASS; ctrlapi's database sessions run as `ctrlapi_rt` (section 11) |
| Test OneGate Appliance | round 2: `onegate-appliance-9613fb52679d` | `DEPLOYED_BINARIES.sha256`: all eight binaries embed `9613fb52679d904ca0320c5cd9eeac23f06350b1`; migrations through 0179; the Root CA pin installed against its fingerprint during the first round-2 upgrade (`02d20f96c54b`) |

**Verified live on the test appliance** (round 1): the upgrade (after a safe stop at 0162 that PR #266 fixed), the licence over mutual TLS with the
pinned Root CA, the client journey (12/12), private-network denial (AD-22), MAC binding on, off and against a spoof (AD-23), 300 and 500
simultaneous sign-in attempts without a busy answer (AD-26).

**Verified in round 2** (2026-10-29, test pair unless stated):

| Item | Result |
|---|---|
| AD-47 cut-off | allowance of 3 MB spent at 07:14:01.6, access ended by acctd at 07:14:04.3 (2.7 s, reason DATA); the next new connection was refused |
| AD-24 conntrack | no forwarded flow to or from the client's address remained after the cut-off (only the gateway's own DNS entry, which expires) |
| AD-58 image optimiser | 3200×1800 PNG (232 KB) → stored 2560×1440; 1.05 MB refused with the 1 MB message; SVG refused (through the operator API the console page calls) |
| AD-18 offline licence | signed offline licence file imported (200, installed, single-use row, reconciled with Central), replay refused (409) |
| AD-28 (b) | netd on the hardened unit: active, no denied operation, client journey 12/12 |
| AD-3 restore drills (Linux, disposable PostgreSQL containers) | `scripts/ledger-restore-drill.sh`: 16 of 16 scenarios PASS (older backup, secondary storage unavailable, forked copies, no trustworthy copy, interrupted write, cut-short copy, backup older than the ledger, replacement appliance, retention applied after the backup, retention failure stops the restore, pre-retention backup refused, stale maintenance flag). `scripts/phase4-restore-drill.sh`: 38 of 38 checks PASS |
| AD-36 post-stay (isolated database, no PMS) | 10 integration cases PASS, three of them new (see row 36) |
| Root CA pin (AD-2) on `--upgrade` | with the pin file moved aside, `appliance-install.sh --upgrade --root-ca-sha256 <fingerprint read from Central's FINGERPRINTS.txt>` pinned the self-signed root of the CA bundle the appliance already held, byte-identical to the pin it had; a wrong fingerprint was refused and pinned nothing; a second upgrade with the pin present reported "already pinned" and matched |
| New console endpoints | enforcement policy: no password 401, wrong password 401, out-of-range 400, bad CIDR 400, change applied and audited with the operator's label, history shown; licence clock reset: no reason 400, reset 200 |
| Final release (`9613fb52679d`) | client journey 12 of 12; 500 simultaneous sign-in attempts, 0 busy, p95 61 ms; the new endpoints again |
| Migration 0179 on an upgrade | applied as `iam_v2_owner` on a database built from the previous baseline: the column is present and both settings functions are owned by `iam_v2_owner` |
| Platform-migration mechanism (PR #266) | reviewed and hardened, see section 12 |
| AD-50 / AD-59 | measured, see rows 50 and 59 and [`runbooks/DB_BUDGET_MEASUREMENT_PLAN.md`](runbooks/DB_BUDGET_MEASUREMENT_PLAN.md) |

**Still not verified, and why**

* 2,000 real devices moving counters through the whole path (AD-50 S1) and the 2 vCPU / 4 GB reference machine: no such generator or machine was available; the test appliance has 1 vCPU. The database half (S10 insert, roll-up and purge rates at 2,000 devices) was measured in a scratch database on the test appliance.
* A first activation *by package* that succeeds: it needs a second, waiting appliance, which is not available. What was done: the appliance's offline-request is refused on the activated appliance (409), a forged package is refused (403, signature invalid), and the Central and scd package tests pass (Central offline accept matrix and golden vector; scd, on Linux, verify-before-adopt and the revoked-assignment refusal).
* The room-and-reservation Post-stay flow against a real PMS feed: the PMS interface is not enabled on the test appliance and live PMS traffic was not to be started for evidence; it is covered by the isolated database tests and scd's end-to-end test.
* The image upload page in a browser (its API and optimiser were exercised). Driven in a browser on 2026-10-29: Client access protection (a wrong password was refused with 401 and changed nothing; the right one saved, showed version and operator, and was restored), Appliance and licence with the licence clock card, Sign-in methods, and Central's Team page with the registration limits: each rendered with no console error and no failed API call.

## 11. Central least privilege (P3 M1, P3 L2): Central migration 0063

| Item | Disposition | What was done |
|---|---|---|
| P3 M1 | IMPLEMENTED | ctrlapi no longer connects as the database superuser that owns every table. Migration 0063 adds the role `ctrlapi_rt` (LOGIN, no superuser, no create-database, no create-role, no replication, no bypass of row security; **no password in the migration**): connect and `USAGE` on the schema, no `CREATE` on `public`, `SELECT/INSERT/UPDATE/DELETE` on the application tables and `USAGE/SELECT/UPDATE` on sequences, **`SELECT` and `INSERT` only** on the append-only history tables (`audit_log`, `portal_tls_events`, `appliance_lifecycle_events`, `appliance_certificate_events`, `appliance_certificate_revocations`, `appliance_assignments`, `appliance_assignment_history`, `appliance_history_archive`, `retired_appliance_identities`, `appliance_ca_versions`) and `SELECT` only on `schema_migrations`. The migration owner (`stayconnect`) keeps running migrations, `pg_dump`/`pg_restore` and first-admin seeding. The only way the role changes append-only history is four `SECURITY DEFINER` functions owned by the migration owner (retention, forget actor, forget audit, correct email); the guards on `audit_log` and `portal_tls_events` now need the owner as current user, so setting `central.retention = 'on'` does nothing for the role. |
| P3 L2 | IMPLEMENTED | `central_forget_actor` rewrites only the small history columns in the delete and queues the email in an owner-only table; `central_forget_audit_apply()` scrubs `audit_log` case-insensitively, one week per statement, outside the delete (ctrlapi calls it after a removal and on each housekeeping pass). |
| Upgrade | IMPLEMENTED | `central-deploy.sh` and `central-install.sh`: generate `ctrlapi_db_password` (0600, only if absent, never printed), apply the grants, set the role password, **prove a login** (non-superuser, can read, cannot create), and only then rewrite `CTRLAPI_DB_URL`, keeping `ctrlapi.env.bak-dbrole-<stamp>`. If any step fails the old DSN is kept and the deploy says `DATABASE LEAST PRIVILEGE IS NOT ACTIVE`; a fresh install aborts instead. Rollback: copy the backup env file back and restart ctrlapi ([`runbooks/CENTRAL_DB_LEAST_PRIVILEGE.md`](runbooks/CENTRAL_DB_LEAST_PRIVILEGE.md)). |
| Tests | IMPLEMENTED | The control-plane suites pass as the owner and as the role (`scripts/ci/central-restricted-role-tests.sh`); `TestRuntimeRoleHasOnlyTheLeastPrivilege` asserts 29 refused statements by SQLSTATE and message (CREATE/ALTER/DROP TABLE, TRUNCATE, disabling or dropping a trigger, UPDATE/DELETE on audit rows, CREATE ROLE, reading `pg_authid`, `COPY ... PROGRAM`, server-file reads, setting the retention flag). |

Residual, stated plainly: a **future table defaults to full DML for the role** (fail-open) and a migration that adds an append-only table must list it in
`central_apply_runtime_grants()`; the role can still call the four definer functions with arbitrary arguments (that is the sanctioned maintenance path);
after a binary-only rollback to a release older than this one, queued emails stay in old audit payloads until the new binary runs again. Live result (2026-10-29, Central `onegate-central-20df43313f43`, then `9613fb52679d`): migration 0063 applied (63 applied, 0 modified); `ctrlapi_rt` logs in with the generated password and is not a superuser, cannot create objects; ctrlapi's two database sessions are `ctrlapi_rt`; as that role CREATE TABLE, UPDATE and DELETE on `audit_log`, TRUNCATE, CREATE ROLE, reading `pg_authid` and disabling a trigger were each refused; smoke PASS; the test appliance kept its Central contact and its licence. **Found live:** the deploy's rollback copy of the env file was removed by the retention sweep minutes after the deploy (copies made with `cp -a`/`cp -p` tie on modification time and the tie swept the newest); fixed by PR #270 (order by change time) and deployed in `9613fb52679d`.

## 12. Review of the platform-migration mechanism (PR #266), 2026-10-29

What it is: a migration whose first 20 lines carry `-- edge-migrate: platform` is applied by `apply-pending-migrations.sh` as the
site's platform role through `edge-migrate.sh --platform-migration`, instead of as `iam_v2_owner`. Five migrations use it (0163, 0166, 0173,
0174, 0178), because they change `public`, which the least-privilege migration role cannot write by design.

| Question | Finding |
|---|---|
| How wide is the exception? | One gate only: the live-site "apply role is a non-superuser with no `public` CREATE / ledger UPDATE" check, lifted for a run that names exactly one migration (`--only`), whose file carries the marker, with `--expect-sha256` pinning its content, on a `live-site` target that already passed the identity and acknowledgement checks. The pre-change backup (a refusal without one is exit 4), the advisory locks, the gap check and the ledger row are the same as for any migration. Every other migration still runs as `iam_v2_owner`. |
| Privilege restoration | Nothing is left elevated. The role is applied per `psql` connection with `SET ROLE`; each migration is its own connection, and the Gate-P reconcile that follows re-owns what a platform migration created and re-asserts the grants. |
| **Defect found: the marker was trusted from the installed tree.** | The hash pinned for a platform migration was computed from the same file that was then run, so it guarded only the moment between hashing and running. A marker added to a pending migration, or a pending migration edited, after the installer had verified the release would have run as the platform role. **Fixed:** `apply-pending-migrations.sh` now requires the file's hash to equal its entry in the installed release's `SHA256SUMS` (written by the release the installer just verified) and refuses, applying nothing, a marked migration that the record does not list or whose content differs. `scripts/ci/apply-pending-migrations-selftest.sh` has three new cases (no record, a different hash, a listed file). |
| Abuse resistance | Writing the files needs root on the appliance, and root there already holds the platform role, so the check is defence in depth against a damaged or substituted release tree, not against a hostile root. A release that was edited before installation is refused earlier, by `SHA256SUMS` at install time. The marker is only honoured for a pending migration (a migration already in the ledger is never run again). |
| Not changed | The platform role is still the cluster superuser of a site appliance; no new superuser access was added anywhere else. The marker detection (first 20 lines, exact text) is unchanged. |


## 13. Capacity, offline activation, roll-up and AD-36 review (closure round, 2026-10-30)

Full record: [`runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md`](runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md). Simulated acceptance in an isolated container lab at the approved 2 CPU / 4 GB; physical-device and real-PMS field acceptance are not performed.

| Item | Disposition |
|---|---|
| Roll-up (PR #272) | **Defect found and fixed:** one poisoned window could stall catch-up for ever; the window is now adaptive. Usage that depended on raw rows lost them at the purge; **migration 0180** keeps entitlement usage past the raw purge (tested on a real PostgreSQL). After an acctd outage raw, minute and hour totals reconcile exactly. |
| netd enforcement at 1,700 to 2,000 sessions | **Defects found and fixed:** a pass that grew with the square of the sessions; a pass budget that started before the lock wait; lease renewal behind other work; ended sessions re-recorded every pass and listed for an hour. Long sessions keep their authorization under churn in the simulator (268 of 300 lost it before, 0 after). The kernel limit of 2,047 shaped sessions per bridge or IFB is documented. |
| 300 sign-ins/min sustained | **MET in the lab, twice, on two releases:** 17,998 of 18,000 enforced over 60 minutes (2f687f24 and again on the final release dbf86d28), 1,700 long sessions held, 60-second churn, p99 5.3 to 5.8 s, no device with two sessions, raw = minute = hour = session = entitlement bytes. Root causes: kernel neighbour table, scd pool polling, licence lock across the commit, admission waiting for the next acctd tick, a writer-guard janitor that deadlocked (0192), missing indexes (0191). Simulated on a 2 vCPU / 4 GB container: CPU peaks reach 100% of a 5 s window, so the margin is thin; hardware acceptance is outstanding. See [`reviews/SIGNIN_CAPACITY_CLOSURE.md`](reviews/SIGNIN_CAPACITY_CLOSURE.md). |
| Fresh install | **Defect found and fixed:** the enforcement-policy rows were missing from the production baseline (the policy page returned 500). |
| Offline first activation | **Defect found and fixed:** the package now carries the signed assignment registry. Verified on a factory-clean appliance against an isolated Central: activation, assignment, licence binding, single use, tampered and truncated packages refused. |
| Plan lease bound | **Unchanged at 90 s (fail-closed).** Revocation, quota exhaustion and licence changes reach the kernel only through acctd, so nothing can safely renew a lease without it. Recovery was shortened instead: plan submission has a 15 s deadline and a systemd watchdog (`WatchdogSec=30`) restarts a hung acctd; measured in the lab: hang restarted at +23 s with no guest losing access, crash +2.5 s, a 4-minute stop still empties the set at 90 s. See [`reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md`](reviews/AVAILABILITY_DURING_ACCTD_INTERRUPTION.md). |
| AD-36 (post-stay new device) | Review found: new Entitlement per conversion, refusals only logged, throttle constants. **Closed afterwards** by [`reviews/AD36_POST_STAY_CLOSURE.md`](reviews/AD36_POST_STAY_CLOSURE.md); one PO decision remains there. |
| Shaping beyond 2,047 sessions per bridge | **Fixed:** one `flower` filter per session direction (no per-device priority limit), class ids for subnets up to /17; real-kernel test 8,000 sessions. Client networks are capped at **/19** (8,190 addresses) by `internal/netcfg` since the Product Owner decision of 2026-10-30 (was /20): see the row below. [`reviews/SHAPING_SCALE_BEYOND_2047.md`](reviews/SHAPING_SCALE_BEYOND_2047.md). |
| Client networks up to /19 (PO decision 2026-10-30) | **Implemented (not deployed; lab acceptance pending):** `MinSubnetPrefix` 20 to 19, one size rule shared by the apply-time validator and the Admin API (create, batch, update, replace, trunk plan), WAN/management overlap refused (netd from the live address list; API when the WAN is known; the LAN base follows the same rule and overlap by containment), the neighbour table sized for three full /19 networks (`8192/32768/65536`), hotel-admin forms say the limit and that it is address capacity, not 8,190 concurrent clients. **In-place widening of a switched-on network is refused** (`subnet_widen_refused`); an existing /20 is never expanded and management addresses do not change. Legacy `u32` fallback on a /19: supported with its 2,047-session limit, failing closed beyond it. Evidence: real-kernel /19 run (6,300 sessions at the top of the range, rate accuracy within 4-5 % of configured, no cross-bridge counting), neighbour sweep of all 8,189 addresses, Kea serving the top of a /19. Still for the lab: [`runbooks/NETWORK_19_LAB_PLAN.md`](runbooks/NETWORK_19_LAB_PLAN.md). |
| Accounting integrity (0181) | **Fixed:** a sample-sequence restart after a raw purge let an idle-then-active session pass its data quota; uncertain usage is now conservative and recorded. Lab: purge fold did not regain quota. [`reviews/ACCOUNTING_INTEGRITY_CLOSURE.md`](reviews/ACCOUNTING_INTEGRITY_CLOSURE.md). |
| Central disconnect | **Run (lab, 22 min, 150 sign-ins/min):** 3,291 of 3,300 enforced, licence stayed valid, services restarted during the cut admitted from the on-disk licence, no lost or duplicate sessions on restore. The licence grace boundary is proven by code only. |
| Not performed | Physical devices at scale, reference hardware, real PMS post-stay traffic, a host reboot drill in the lab. |


## 14. Final engineering round (2026-10-30): what it found and what it left

| Item | Disposition |
|---|---|
| Integration fixtures on one shared database | **Defect found and fixed (test infrastructure).** scd's fixtures took client subnets from a counter over 200 `/24`s and never released one, so a later run or package either reused a subnet another tenant still held (a sign-in resolved into the wrong tenant: `no pending session to activate`) or, once the pool was spent, **silently skipped** the tests (119 of the scd package on the fourth run) and still reported `ok`. Fixed with a locked, recycled claim from 8,192 subnets; the event-table trim test's fixed primary key and three stale catalogs that failed on master were corrected. Proof and numbers: [`runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md`](runbooks/CAPACITY_AND_OFFLINE_ACCEPTANCE.md) section 7.6. `scripts/db-integration-combined.sh` repeats the proof. |
| Reserved migration numbers 0182-0189 | **Made permanent and enforced.** One declaration (`data-plane/migrations/RESERVED_NUMBERS`, also for Central) read by CI and by the runtime; shipped migrations are pinned in `IDENTITIES`; a release that renames or lacks an applied number is refused. No schema change. See `data-plane/migrations/README.md` and the installation manual (upgrade). |
| Central link diagnostics | **Defect found and fixed.** A certificate loaded from disk at restart was recorded as a successful contact, so `last_contact_at` moved to the restart time and the link read connected with the network cut. `last_contact_at` is now the last successful exchange (null until one), and `identity_not_loaded` is a separate link state. Licence semantics are unchanged. |
| Cross-PMS Transfer and usage continuity | **Defect found and fixed:** the transfer left the session binding on the terminated source, so the destination's data limit was never charged. **Also fixed (migration 0201):** with the production grants the role edged runs as could not perform the transfer at all (no `EXECUTE` on the entitlement state writer, no `INSERT` on the commerce and lineage tables); it failed closed. The transfer is now one definer function that `svc_edged` may execute, with no table privilege added. [`reviews/ACCOUNTING_INTEGRITY_CLOSURE.md`](reviews/ACCOUNTING_INTEGRITY_CLOSURE.md) section 9. |
| Accounting integrity visibility | **Added** `GET /edge/v1/accounting-integrity` (read-only, counts and bytes). No screen. |
| Licence-slot admission paths | **Reviewed, no defect found.** Every route that creates a live session (voucher and account activation, the room grant, the post-stay conversion) takes a licensed slot in the inserting transaction (shared path far from the limit, exclusive and exact near it; the shared path's bound is argued in `reserveLicensedSlotShared` and tested). A session is created only as `PENDING_ENFORCEMENT` and becomes `active` only through `activate_session_enforcement`, which refuses without an accounting origin. One observation: a pending session that is never enforced holds its licence slot until it is enforced or ended, which is conservative (never admits beyond the limit). |
| Final merged code against the tested candidates | **No untested behavioural delta after `dbf86d28`.** A diff of `dbf86d28` against the merged `53f0b990` over `data-plane`, `deploy`, `scripts`, `hotel-admin` and `control-plane` is empty; the only non-documentation file that changed is `tools/validate-migration-location.py` (the reserved-gap allowance, CI only). The 60-minute run on `2f687f24` predates the AD-36 closure, the flower classifier and the acctd watchdog; the same run was repeated on `dbf86d28`, which contains all of them and the tenant filter in the post-stay live-session lookup (section 7 of the capacity record). |


## 15. Final end-to-end mission (2026-10-30/31): the Product Owner's final decisions, and what the lab found

| Item | Disposition |
|---|---|
| After Check-out Access (one feature) | **Implemented** (migration 0193; [`architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md`](architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md)). One policy, one package snapshot, one entitlement per eligible stay episode; eligibility is previous authorization against the same Stay (a database lineage trigger); reconnection with room number and reservation number only; Checkout Grace, Post-Stay Sign-in and the POST_STAY package are retired as separate mechanisms without deleting history. Proven by the real-database suites (worked example, 10 cases; whole `checkout` and `poststay` suites). **Not exercised:** a live PMS stay on the lab appliance, and the Admin pages against a live PMS feed. |
| Continuity during internal failures | **Implemented** (0198, 0199; [`architecture/ONEGATE_OUTAGE_CONTINUITY.md`](architecture/ONEGATE_OUTAGE_CONTINUITY.md)). Setting `outage_continuity_minutes`, default 60, bounds 0 to 120, audited. The 90 s kernel lease is unchanged; netd bounds the sessions the last plan covered, meters bytes from its own class counters and cuts at the budget, and keeps a revocation journal and a database probe so that disconnects, expiry and quota exhaustion still end access. **Lab (real 60, 65 and 120 minute runs): PASS**: acctd stopped 60 and 120 minutes, acctd hung 60 minutes, PostgreSQL down 65 minutes (new sign-ins refused), netd restart, kill and stop; no unmetered consumption; accounted bytes matched the clients within 3%. **Not run:** clock jumps (unit test only), a real host reboot (a container restart only), a failed health check. Evidence: `runbooks/evidence/final-mission-2026-10-30/outage`. |
| Defects the long runs found | **F1 fixed:** netd nil-map panic at minute 43. **F3 fixed:** the pruned revocation journal was never written back, so a netd restart could cut guests on addresses reused after a staff disconnect. **Remaining, by design, stated plainly:** a session admitted less than about 25 s before plans stop gets no continuity (F2); a netd stalled for more than 90 s in an outage loses the authorizations it did not renew (F4); one outage started 50 s after an acctd restart showed fewer elements than the record and did not repeat in two attempts, cause not established (F5). While PostgreSQL is down no operator can sign in, so a staff disconnect is not possible until it returns. |
| Client networks up to /19 | **Implemented and lab-accepted** ([`runbooks/NETWORK_19_LAB_PLAN.md`](runbooks/NETWORK_19_LAB_PLAN.md) Results). Staged apply in 7 s with five health checks and no management gap, watchdog rollback, operator rollback and a hard stop in the pending window all restore the old state; an enabled /20 is never widened in place; Kea 2.0.2 leased 6,000 clients to the top of the pool; 8,189 neighbour entries without overflow; flower and the `u32` fallback both behave. **Defect found and fixed: migration 0200.** The database re-derived the class minor with the old rule, so every session in the upper half of a /19 was refused (604 of 700 in the lab). Not claimed: 8,190 concurrent clients (the most sessions on one /19 were 2,150). |
| Cross-PMS Transfer | **Defect fixed (0201):** the transfer re-pointed sessions without moving the open binding, and the Admin Console role could not complete it under the production grants. It is now one definer function that `svc_edged` may execute; no table privilege was added. |
| Capacity | **MET again on the integrated build:** offered exactly 300 sign-ins in each of 60 minutes, 18,001 of 18,001 enforced (289 to 309 a minute), p50 3.1 s, p99 5.1 s, 1,700 long sessions, 60-second churn, forwarding 122,400 of 122,400, accounting byte totals equal at every level. CPU mean 81%, p99 99.6%, maximum 102% of 2 vCPU: the margin on the 2 vCPU reference is thin. Sizing: [`runbooks/SIZING_GUIDANCE_DRAFT.md`](runbooks/SIZING_GUIDANCE_DRAFT.md) is derived only from the measured lab; larger hardware is labelled extrapolation. |
| Remaining engineering | Fixture isolation (proven by three runs on one database), reserved migration numbers 0182 to 0189 and 0194 to 0197 (one declaration, enforced by CI and the runtime), pinned migration identities, Central link diagnostics, licence-slot admission review, stale documentation: see section 14. |
| Not performed | Real PMS traffic, physical devices, reference hardware beyond the 2 vCPU lab, a real host reboot drill, clock-jump drills, Go-Live. |
