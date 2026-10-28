# Audit backlog: backend audit and performance audit

**What this is.** Every finding from the two audit report sets, in one place, each with an honest status. The source folders are being removed from the current tree, but they stay in Git history. Each row points back to its source.

**Source of record.** Both folders exist at commit **`1d69508a847248ec63ceed01e06ec8f8f1661fb0`** (HEAD when this backlog was drawn up, 2026-10-28). To read a source file:
`git show 1d69508a847248ec63ceed01e06ec8f8f1661fb0:docs/audit/PHASE8_NETWORKING.md`

| Audit | Folder at that commit | Files | Audited master | Added by |
|---|---|---|---|---|
| Backend audit | `docs/audit/` | `README.md`, `FINAL_REPORT.md`, `PHASE0_BASELINE.md`, `PHASE1_CENTRAL.md`, `PHASE2_APPLIANCE_FOUNDATIONS.md`, `PHASE3_DATABASE_AND_MIGRATIONS.md`, `PHASE4_GUEST_SIGN_IN.md`, `PHASE5_CLIENT_PORTAL.md`, `PHASE6_PAYMENTS_AND_POSTING.md`, `PHASE7_PMS.md`, `PHASE8_NETWORKING.md`, `PHASE9_ADMIN_CONSOLE_API.md`, `PHASE10_CROSS_CUTTING.md` | `0879e839` (last phase) | PR #232, merge `653363d7`, 2026-10-24 |
| Performance audit | `docs/performance-audit/` | `README.md`, `FINAL_REPORT.md`, `PHASE0_BASELINE.md` … `PHASE11_LOAD_TEST_PLAN.md` | `653363d7` | PR #233, merge `6f11f534`, 2026-10-25 |

In the tables below, "P1 B2" means batch B2 in that audit's Phase 1 file. Backend paths are relative to `control-plane/` for Phase 1. For Phases 2–9 they are relative to `data-plane/` unless the path says otherwise. Performance paths are given as the source cites them.

## How each status was decided

This is the state after the **2026-10-28 remediation mission**. Eight workstreams (Central, scd and appliance foundations, portald, acctd and payments, pmsd, netd, edged and hotel-admin, DB and tooling) re-checked every finding against current master (`55c01890`) and fixed what needed no new Product-Owner decision. Their commits are merged on branch **`claude/vigilant-keller-sag14r`**. Appliance migrations 0159–0161 and Central migrations 0057–0058 are on that branch too.

**Nothing is deployed.** The delivery environment cannot reach the test appliance. FIXED means fixed in code (delivered to master through the remediation PR), with a test or cited code evidence. It does not mean deployed to the test appliance.

1. **Evidence.** Each status comes from the workstream result records: the commit SHA on the branch, plus the test name, migration number or file:line. Every cited SHA was checked to be on `origin/master..claude/vigilant-keller-sag14r`. Commits from before the mission (`60e15df7`, `490321c1`, `85f1b1c4`, …) are cited only for ALREADY-FIXED rows and for the first half of a finding.
2. **When workstreams disagree.** A finding reported by several workstreams takes the owning workstream's outcome. A handed-off finding takes the outcome of the workstream that received it. Coordinator decisions override both:
   - 0159–0161 integrated;
   - six SQL changes not integrated (P5 L11 index, P6 M1 backoff, P6 M3 function, D1-L3 fillfactor, P3 L6/CC3 checksum, Central P3 L2);
   - the P8 L2 sysctl change reverted (`128ee02a`).
3. **The status values.**
   - **FIXED:** the whole finding is fixed on the branch.
   - **PARTIAL:** a named part is fixed, with evidence, and a named part remains. The remainder is either a decision (`AD-n`) or open work, with the reason given.
   - **ALREADY-FIXED:** fixed before this mission; the commit or location is cited.
   - **NOT-REPRODUCIBLE:** the defect is not in the current code; the reason is given.
   - **OPEN-DECISION:** nothing is fixed yet, and the fix waits on a Product-Owner decision. `AD-n` is its item in `AUDIT_DECISIONS.md`.
   - **OPEN:** actionable but not done. The reason is one of: deferred-controlled, needs a migration that is not integrated, out of scope, or clean code not taken.
4. **"SQL ready in the remediation record"** means a workstream wrote the exact additive SQL and recorded it, but it is not a migration file on the branch.
5. **Source pointers are unchanged.** "Where" still cites the code at the audit commit; the sources are at `1d69508a`.

---

## Before and after

Before = the statuses in `docs/AUDIT_BACKLOG.md` as drawn up on 2026-10-28 (before this mission). After = this file. Merged rows (P6 M4, X1, X2) are not counted, as before.

### Backend audit (258 rows)

| Severity | Rows | Before: OPEN | OPEN-DECISION | PARTIAL | FIXED | After: FIXED | ALREADY-FIXED | PARTIAL | NOT-REPRODUCIBLE | OPEN-DECISION | OPEN |
|---|---|---|---|---|---|---|---|---|---|---|---|
| High | 5 | 2 | 2 | 0 | 1 | 2 | 1 | 1 | 0 | 1 | 0 |
| Medium | 64 | 47 | 12 | 3 | 2 | 31 | 2 | 12 | 0 | 14 | 5 |
| Low | 146 | 137 | 8 | 1 | 0 | 90 | 3 | 22 | 2 | 22 | 7 |
| Clean code | 43 | 43 | 0 | 0 | 0 | 20 | 0 | 13 | 0 | 3 | 7 |
| **Total** | **258** | **229** | **22** | **4** | **3** | **143** | **6** | **48** | **2** | **40** | **19** |

### Performance audit (82 rows)

| Severity | Rows | Before: OPEN | OPEN-DECISION | PARTIAL | FIXED | After: FIXED | ALREADY-FIXED | PARTIAL | NOT-REPRODUCIBLE | OPEN-DECISION | OPEN |
|---|---|---|---|---|---|---|---|---|---|---|---|
| High | 13 | 10 | 3 | 0 | 0 | 4 | 0 | 2 | 0 | 4 | 3 |
| Medium | 28 | 27 | 1 | 0 | 0 | 9 | 0 | 7 | 0 | 7 | 5 |
| Low | 37 | 30 | 7 | 0 | 0 | 13 | 1 | 3 | 0 | 12 | 8 |
| Clean code | 4 | 4 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 1 | 2 |
| **Total** | **82** | **71** | **11** | **0** | **0** | **27** | **1** | **12** | **0** | **24** | **18** |

### Security hardening list (8 rows)

| Severity | Rows | Before: OPEN | OPEN-DECISION | PARTIAL | FIXED | After: FIXED | ALREADY-FIXED | PARTIAL | NOT-REPRODUCIBLE | OPEN-DECISION | OPEN |
|---|---|---|---|---|---|---|---|---|---|---|---|
| High | 3 | 0 | 0 | 1 | 2 | 0 | 2 | 1 | 0 | 0 | 0 |
| Medium | 3 | 1 | 0 | 1 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| Low | 1 | 1 | 0 | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 |
| — | 1 | 1 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |
| **Total** | **8** | **3** | **0** | **2** | **3** | **1** | **3** | **3** | **0** | **1** | **0** |

Rows that now wait on a Product-Owner decision (OPEN-DECISION, plus PARTIAL rows whose remainder is an `AD-n`): backend 40 + 30, performance 24 + 8, hardening list 1 + 3.

---

## A. Backend audit (`docs/audit/`)

### Phase 0: tooling baseline (`PHASE0_BASELINE.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P0-1 | Medium | No automated security or static-analysis gate (vet, staticcheck, govulncheck) | whole repo; `tools/go-checks.sh` (absent at HEAD) | PARTIAL | `90cdb9ff` `scripts/ci/go-checks.sh` (vet, staticcheck, govulncheck), selftest 3/3. Rest: not yet called from `tools/preflight.sh` (out of scope) |
| P0-2 | High | Go toolchain out of support; reachable vulnerabilities: appliance 37, Central 20 (re-checked as Phase 10 X2) | both `go.mod` files (still `go 1.25.0`); `deploy/scripts/central-build.sh` | PARTIAL | `1bacd214` libraries updated within go 1.25 (reachable: appliance 38→23, Central 20→16). Rest: Go 1.26 toolchain, AD-60 |
| P0-3 | Low | Appliance build toolchain: image pinned by tag, not digest; no `toolchain` line; Makefile builds without the production tag (= Phase 10 X1, lowered from Medium) | `scripts/build-appliance-binaries.sh:46` (`golang:1.25.12`); `Makefile` | PARTIAL | `129f66ee` Go image pinned by digest in both build scripts. Rest: `Makefile` still builds without the production tag (out of scope); digest unproven until the first real build |

### Phase 1: Central, ctrlapi (`PHASE1_CENTRAL.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P1 H1 | High | A missing CA certificate file makes Central mint a new root CA | `internal/pki/ca.go:50-87,106-142`; `cmd/ctrlapi/main.go:236-264` | FIXED | `a7c35b66` + installer `ce245d10` (`ctrlapi init-ca`); `TestLoadCAMissingFileFailsAndCreatesNothing`, `TestPKIStartupFailsClosed` |
| P1 H2 | High | An appliance is activated on facts anyone can know (serial or WAN MAC; no code shown on the box) | `internal/api/register.go:39-261,267-349` | OPEN-DECISION | AD-1 |
| P1 M1 | Medium | A rate-limit key can lose its expiry (INCR and EXPIRE are separate) | `internal/api/ratelimit.go:35-44` | FIXED | `677fd0f7`; `TestRedisRateCounterAlwaysLeavesATTL` |
| P1 M2 | Medium | "Reissue certificate" with a waiting CSR leaves the old certificate usable for 24 h | `internal/api/appliance_ops.go:608-654`; `certificates.go:136`; `appliance_mtls.go:102` | FIXED | `29acdd7f`; `TestReissueWithWaitingCSRRevokesOldCertificate` |
| P1 M3 | Medium | A failure to re-sign the trust registry is swallowed | `internal/api/assignment_keys.go:45-51`; `cmd/ctrlapi/main.go:323-335` | FIXED | `23d19e00`; `TestKeyStateChangeNeedsTheRegistryRoot` |
| P1 M4 | Medium | Customer users see Central staff emails and internal reasons | `internal/api/fleet.go:295-315` | OPEN-DECISION | AD-4 |
| P1 M5 | Medium | Customer audit view shows platform staff ids and IP addresses | `internal/api/system.go:230-304` | OPEN-DECISION | AD-4 |
| P1 M6 | Medium | Anyone who knows the password can lock the account's code step for up to 24 h | `internal/auth/mfa.go:197-212` | OPEN-DECISION | AD-6 |
| P1 M7 | Medium | WAN MAC rebind accepts any string and is not atomic | `internal/api/appliance_ops.go:534-598` | FIXED | `7cb83fec`, `bea535c9`; `TestRebindWANMACIsValidatedAndAtomic` |
| P1 M8 | Medium | Deleting an appliance runs irreversible steps outside one transaction | `internal/api/appliance_ops.go:682-771` | FIXED | `7cb83fec` + Central migration 0057; `TestDeleteMarksTheApplianceAndAFailureClearsIt` |
| P1 M9 | Medium | No request-body size limits | `internal/http/auth_handlers.go:113,177`; `internal/api/common.go:131-139` | FIXED | `677fd0f7`; `TestDecodeJSONLimits` |
| P1 M10 | Medium | Servers have no read, write or idle timeouts | `internal/http/mtls.go:41-51`; `cmd/ctrlapi/main.go:294-298` | FIXED | `677fd0f7`; `TestMTLSServerLimits` |
| P1 M11 | Medium | Unauthenticated registration has no cap and no field validation | `internal/api/register.go:39-261`; `offline_activation_api.go:331` | PARTIAL | `d9d52a7e` field validation; `TestRegistrationRefusesBadFacts`. Rest: waiting-row cap and prune, AD-20 |
| P1 M12 | Medium | Portal certificate orders can retry for ever after a crash; shutdown can lose a result | `internal/portaltls/worker.go:185-207`; `cmd/ctrlapi/main.go:232` | PARTIAL | `13249cd6` steps 1–2; `TestInterruptedOrderIsFailedAtMaxAttempts`. Rest: step 3 (finalise from the ACME order URL) needs the Pebble E2E |
| P1 M13 | Medium | One mutex serialises every ACME and DNS action for minutes | `internal/portaltls/worker.go:231,511`; `admin.go:141,181` | FIXED | `13249cd6`; `TestACMESlotHonoursTheCallerDeadline` |
| P1 M14 | Medium | Audit rows can be lost after a committed change | `internal/audit/audit.go:32-82` | FIXED | `a49dccc2`; `TestAuditWritesAfterCancelAndKeepsCallerMap` |
| P1 M15 | Medium | Administrators can take over peer administrators | `internal/api/users.go:111-123` | OPEN-DECISION | AD-5 |
| P1 M16 | Medium | Offline request import: no step-up, silent conflicts, double packages | `internal/api/routes.go:68`; `offline_activation_api.go:67-255` | FIXED | `d9d52a7e`, `bea535c9`; `TestOfflineImportConflictAndSinglePackage` |
| P1 M17 | Medium | Signing-key files are trusted without checks | `cmd/ctrlapi/main.go:144-158,323-335`; `cmd/ctrlapi/assignkey.go` | FIXED | `23d19e00`; `TestLoadEd25519FileChecks`, `TestDistinctKeys` |
| P1 L1 | Low | Last-platform-admin guard runs outside the transaction and fails open on a DB error | `api/users.go:378-387` | FIXED | `13249cd6`; `TestLastAdministratorGuardIsAtomic` |
| P1 L2 | Low | Step-up mark ignores a Redis error but answers ok | `api/reauth.go:18-24` | FIXED | `d9d52a7e`; `TestMarkReauthReportsFailure` |
| P1 L3 | Low | `IssueTx` ignores the previous-version read error | `api/assignment_api.go:108` | FIXED | `23d19e00`; code evidence (`IssueTx` counts only `pgx.ErrNoRows` as version 0) |
| P1 L4 | Low | Licence revoke takes no per-appliance lock and keeps the portal hostname; suspend and resume race | `licensing/licensing.go:300-312`; `api/licenses.go:153-217` | PARTIAL | `7cb83fec` revoke/suspend/resume locked; `TestSuspendResumeStateDecidedUnderLock`. Rest: portal hostname on revoke, AD-21 |
| P1 L5 | Low | `revocation_delivered_at` is set on encode, not on appliance confirmation | `api/licenses.go:258-276` | OPEN-DECISION | AD-21 |
| P1 L6 | Low | `ReconcileTerminalTimeouts` changes state and inserts the alert outside one transaction | `api/terminal_delivery.go:239-275` | FIXED | `a49dccc2`; `TestTerminalTimeoutStateAndAlertCommitTogether` |
| P1 L7 | Low | Trust, fleet and detail pages hide DB errors behind 200; `strictMTLSSelf` turns an outage into 403 | `api/system.go:103-212`; `fleet.go`; `terminal_delivery.go:352-378` | FIXED | `13249cd6`; code evidence (`failDB` in system.go, fleet.go, terminal_delivery.go); no outage test |
| P1 L8 | Low | Uncapped pending CSRs; concurrent issuance can leave two active certificates; retiring appliance still rotates | `api/certificates.go:25-80,114-170` | FIXED | `29acdd7f`; `TestPendingCSRCapAndNoRotationWhileRetiring` |
| P1 L9 | Low | `replace` has no row lock or state re-check | `api/appliance_ops.go:490-528` | FIXED | `7cb83fec`; code evidence (replace locks the row and re-checks state); no race test |
| P1 L10 | Low | `valid_days` overflows; licence terms unbounded | `api/licenses.go:69` | FIXED | `13249cd6`, `34692b1a`; `TestLicenseTermsBounds`, cloud-admin `license-terms-bounds.test.ts` |
| P1 L11 | Low | Appliance CSR accepts any key type and size | `pki/ca.go:234-246` | FIXED | `a7c35b66`; `TestSignApplianceCSRKeyTypes` |
| P1 L12 | Low | Failed-auth audit uses a hand-written JSON escaper; a non-uuid `iss` gives an unaudited 500 | `auth/appliance.go:56-79,137-175` | FIXED | `a49dccc2`; `TestAuthFailPayloadEscapesControlCharacters` |
| P1 L13 | Low | Every insert failure reported as "already exists"; email-existence oracle | `api/customers.go:96-99,145-149`; `users.go:187-193` | FIXED | `677fd0f7`; `TestCreateCustomerMapsOnlyDuplicatesToConflict`, `TestDuplicateEmailWording` |
| P1 L14 | Low | Site `code` not validated; site of an archived customer can be restored | `api/customers.go:107-150,647-668` | FIXED | `677fd0f7`; `TestSiteCodeRuleAndRestoreGuard` |
| P1 L15 | Low | Public `/readyz` returns the version; backup health leaks host paths and raw errors | `http/router.go:174-193`; `api/backup_health.go` | PARTIAL | `a7c35b66`; `TestBackupHealthHidesHostPaths`. Rest: retention `status` items keep host paths (kept on purpose for pin choice) |
| P1 L16 | Low | Intermediate not checked against its key and the root; `relocateRootOffline` | `pki/ca.go:61-68`; `cmd/ctrlapi/main.go:345-361` | FIXED | `a7c35b66`; `TestLoadCARefusesMismatchedMaterial`, `TestCheckChainRefusesExpired` |
| P1 L17 | Low | ACME renewal window used without clamping | `portaltls/ari.go:111-115` | FIXED | `13249cd6`; `TestClampARIWindow` |
| P1 L18 | Low | Daily order cap is count-then-insert; worker loops ignore scan errors and `rows.Err()` | `portaltls/orders.go:80-92`; `worker.go` | FIXED | `13249cd6`; code evidence (cap counted in the inserting transaction under a lock; `rows.Err` checked) |
| P1 L19 | Low | Long-lived goroutines have no panic recovery | `cmd/ctrlapi/main.go:177,232,256` | FIXED | `13249cd6`; `TestHousekeepingPassSurvivesAPanic` |
| P1 L20 | Low | In-memory JWT replay cache unbounded, O(n) insert, lost on restart | `applianceauth/jwt.go:177-195` | PARTIAL | `bea535c9` bounded, O(1); `TestReplayCacheExpiresAndIsBounded`. Rest: survives no restart, AD-11 |
| P1 L21 | Low | Operational values hard-coded (rate limits, windows, cert lifetime) | various | OPEN-DECISION | AD-54 |
| P1 L22 | Low | `clientip` trusts forwarding headers only from loopback; Docker bridge would share one bucket | `clientip/clientip.go` | NOT-REPRODUCIBLE | Repo config: `deploy/caddy/Caddyfile.central:72` proxies from loopback. Live Central not read (not authorized) |
| P1 CC1 | CC | Dead code (`issueAssignment`, `pki.LoadOrCreate`, `selfSign`, …) | various | PARTIAL | `bea535c9` removed `issueAssignment`, `rawGet`. Rest: `pki.LoadOrCreate`/`selfSign` kept (tests use them) |
| P1 CC2 | CC | Appliance verifier code in ctrlapi; `Challtestsrv` selectable in production | various | FIXED | `bea535c9`; `TestChalltestsrvRefusedInProduction` |
| P1 CC3 | CC | Duplicates: UUID generators, atomic writers, key loaders, helpers | various | PARTIAL | `66bf4f3c` one UUID and client-IP helper; key loaders merged. Rest: `licensing.newUUID` (needs a new package) |
| P1 CC4 | CC | `serve()` about 240 lines with a `goto`; `activate` about 160 lines | `cmd/ctrlapi/main.go` | PARTIAL | `66bf4f3c`, `13249cd6` no `goto`, helpers extracted. Rest: `serve()` ~210 lines, `activate` not split |
| P1 CC5 | CC | `id::text = $1` defeats the primary-key index | licensing, portal TLS, users | PARTIAL | `66bf4f3c` primary-key lookups. Rest: `($1 = '' OR id::text = $1)` filters |
| P1 CC6 | CC | `customerRows` loads the whole fleet; list limits truncate silently | `api/fleet.go` | OPEN-DECISION | AD-55 |
| P1 CC7 | CC | `WriteErr` duplicates `Fail` | `api` | FIXED | `bea535c9`; code evidence (`WriteErr` had no callers, removed) |
| P1 CC8 | CC | `metrics.statusRecorder` hides Flusher and Hijacker | `metrics` | FIXED | `bea535c9`; `TestMiddlewareKeepsFlusherAndCountsStatus` |
| P1 CC9 | CC | Assignment handler answers every refusal 204; misnamed variable; wrong CA comment | `api` | PARTIAL | `66bf4f3c`, `a7c35b66` rename and comment. Rest: 204-on-refusal wire contract, AD-21 |

### Phase 2: appliance security foundations (`PHASE2_APPLIANCE_FOUNDATIONS.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P2 H1 | High | The licence's anti-replay and revocation records can be silently lost | `license/store.go:38-190` | FIXED | `0020bf2c`; `license/store_integrity_test.go` (concurrent `-race`, damaged state, interrupted write) |
| P2 M1 | Medium | One boot with a wrong future clock can lock the licence for years | `license/store.go:122-143,175-190` | OPEN-DECISION | AD-17 |
| P2 M2 | Medium | A licence issued while Central's clock was ahead blocks every later licence | `license/store.go:107-110,121` | FIXED | `0020bf2c`; `TestStoreNewerVersionWithEarlierIssuedAtInstalls` |
| P2 M3 | Medium | Identity checks skipped on empty fields; unbound licences accepted anywhere | `license/binding.go:30-47`; `internal/offline/offline.go`; `internal/assignment/assignment.go:116-135` | OPEN-DECISION | AD-15 |
| P2 M4 | Medium | Revocations from Central trusted on the transport alone; failed save ignored | `internal/licstate/licstate.go:413-475` | PARTIAL | `0020bf2c` steps 3–4; `TestRevocationSaveFailureFailsFetch`. Rest: steps 1–2, AD-16 |
| P2 M5 | Medium | Mutual-TLS key regenerated after any read error | `internal/appliancecert/cert.go:63-98` | FIXED | `9886a61b`; `TestUnreadableMTLSKeyIsNotRegenerated` |
| P2 M6 | Medium | Identity key regenerated if `ed25519.key` goes missing | `internal/identity/identity.go:52-65,200-218,239-264` | FIXED | `4fcbc66f`; `TestMissingKeyIsDamageNotFirstBoot`, `TestMismatchedKeyIsRefused` |
| P2 M7 | Medium | Calls to Central's HTTPS ingress have no timeout | `internal/appliancecert/cert.go:132,586-616,688,700` | FIXED | `9886a61b`; `TestIngressCallTimesOut` |
| P2 M8 | Medium | Mutual-TLS trust anchor is whatever Central's response says | `internal/appliancecert/cert.go:463-500,640-700` | OPEN-DECISION | AD-2 |
| P2 M9 | Medium | Certificate rotation switches first and verifies afterwards | `internal/appliancecert/cert.go:307-368,560-583` | FIXED | `9886a61b`; `TestRotationVerifiesBeforeSwitching` |
| P2 M10 | Medium | Writer-boundary startup check accepts triggers that never fire | `internal/writerguard/writerguard.go:203-280` | FIXED | `f79027ac`; writerguard integration tests (replica-mode guard, AFTER trigger, table owner) |
| P2 M11 | Medium | Guest code requests can exceed limits; anyone can block a guest's code | `internal/otp/otp.go:75-112` | PARTIAL | `bb340fb8` step 1; `TestParallelIssue*`. Rest: step 2 per-destination limits, AD-30 |
| P2 M12 | Medium | Trust registry falls back to the previous registry (may trust a revoked key) | `internal/assignment/registry_store.go:63-77` | FIXED | `1648fe08`; `TestTrustedNoFallbackOnExpiredCurrent` |
| P2 M13 | Medium | Portal private key and certificate installed in two separate steps | `internal/portaltls/portaltls.go:436-466` | PARTIAL | `d6a78845` failure path; `TestFailedChainWriteKeepsThePreviousPair`. Rest: crash between the two renames (versioned directory read by portald) |
| P2 M14 | Medium | One device can pause room sign-in for a whole guest network | `internal/signinattempt/network_pause.go:34-36` | OPEN-DECISION | AD-31 |
| P2 M15 | Medium | Assignment, registry and certificate files are not crash-safe | `internal/assignment/store.go:68-85`; `registry_store.go:124-140`; `appliancecert/cert.go:492-500` | FIXED | `1648fe08`, `9886a61b`; `TestBadCABundleLeavesInstalledPair` |
| P2 L1 | Low | No persistent assignment anti-rollback | `assignment/resolve.go:148-150` | FIXED | `1648fe08`; `TestResolveRefusesDocumentOlderThanAdopted` |
| P2 L2 | Low | A short key file from a failed create blocks bootstrap | `localkeys/localkeys.go:52-95` | FIXED | `84ee8c2c`; `TestConcurrentCreateLeavesOneCompleteKey` |
| P2 L3 | Low | Key-length rules disagree across packages | `localkeys.go`; `sealbox.go:27-34`; `signinattempt/seal.go`; `otpkey.go`; `throttle.go` | FIXED | `84ee8c2c`; `TestKeyMustBeExactly32Bytes` |
| P2 L4 | Low | `LoadExistingKey` follows a symlink before checking; no owner check | `localkeys/localkeys.go:97-112` | FIXED | `84ee8c2c`; `TestSymlinkRefusedBeforeRead` |
| P2 L5 | Low | `LoadGenerationKeys` ignores the parse error (duplicate generations) | `localkeys/localkeys.go:140-170` | FIXED | `84ee8c2c`; `TestDuplicateOrZeroGenerationRefused` |
| P2 L6 | Low | Hardware id falls back to a constant; weak DMI checks; commands without timeout | `hwid/hwid.go:61-118,138,183,245-260` | PARTIAL | `063fcad4` helper commands time out; `TestHelperCommandsAreBounded`. Rest: constant anchor, AD-19 |
| P2 L7 | Low | Password verify accepts trailing junk; no length cap; no concurrency bound | `passwordhash/passwordhash.go:53-77` | FIXED | `f34e969e`; `internal/passwordhash/limits_test.go` |
| P2 L8 | Low | `throttle` silently drops bad rules; ignores cleanup errors | `throttle/throttle.go:118-285` | FIXED | `b47eb790`; `throttle/rules_test.go` |
| P2 L9 | Low | `authctx` validates trimmed ids but binds untrimmed ones | `authctx/authctx.go:45-60` | FIXED | `4c6f2445`; `TestIntegration_PaddedIdsAreTrimmedForBinding` |
| P2 L10 | Low | Nil protection store or MAC-less device disables sign-in limits | `signinattempt/protection.go`; `network_pause.go` | FIXED | `7b7ed622`; `TestStoreWithoutDatabaseIsLogged` |
| P2 L11 | Low | Licence verifier panics on a wrong-length key; `GenerateVendorKey` overwrites | `license/verify.go:23-31`; `license/sign.go:31-40` | FIXED | `0020bf2c`; `TestVerifierWrongLengthKeyDoesNotPanic`, `TestGenerateVendorKeyRefusesOverwrite` |
| P2 L12 | Low | `Document.Validate` does not bound grace days or check the dates | `license/doc.go:149-171` | FIXED | `0020bf2c`; `TestValidateBoundsGraceAndDates` |
| P2 L13 | Low | `Store.Load` turns every read error into "no licence" | `license/store.go:162-168` | FIXED | `0020bf2c`; `TestLoadDistinguishesMissingFromDamaged` |
| P2 L14 | Low | `licstate` data races; rewrites 14 limit rows every minute | `licstate/licstate.go:136-417` | FIXED | `0020bf2c`; `TestConcurrentLoadInstallFetchNoRace`, `TestUnchangedEvaluationSkipsLimitSync` |
| P2 L15 | Low | Offline packages checked against wall clock only; ledger row written before install | `offline/offline.go`; `cmd/scd/offline_import.go:42-60` | OPEN-DECISION | AD-18 |
| P2 L16 | Low | "Already assigned" decided from `TenantID != ""` | `cmd/scd/offline_first_activation.go` | OPEN-DECISION | AD-18 |
| P2 L17 | Low | Unsigned plain trust file used when the signed registry is missing | `assignment/resolve.go:89-111` | FIXED | `1648fe08`; `TestPlainTrustRefusedAfterSignedRegistryAdopted` (Admin Console alert is a follow-up) |
| P2 L18 | Low | `NoteRefresh` rewrites the assignment record unlocked every poll | `assignment/store.go:109-129` | FIXED | `1648fe08`; `TestNoteRefreshDoesNotOverwriteAdoption` |
| P2 L19 | Low | Portal TLS: stale key temp files; key unreadable without its group; `Inspect` skips host check | `portaltls/portaltls.go:107-218,468-492` | PARTIAL | `d6a78845` stale temporaries, host check; `TestInspectChecksTheHostName`. Rest: missing key group, AD-66 |
| P2 L20 | Low | Central error and success bodies read without a limit | `identity/identity.go:135-147`; `appliancecert/cert.go:611-613` | FIXED | `9886a61b`, `4fcbc66f`; `TestErrorBodyIsCapped`, `TestRegisterErrorBodyCapped` |
| P2 L21 | Low | `backupcrypt.ParseIdentity` ignores Scanner errors; check code an unsalted SHA-256 | `backupcrypt/backupcrypt.go:55-68,112-124` | FIXED | `faf73444`; `TestParseIdentityReportsScannerErrors` |
| P2 L22 | Low | OTP cleanup `DELETE` on the hot path; unused exported `Verify`; dead field | `otp/otp.go:55-66,171-185` | FIXED | `bb340fb8`; code evidence plus the OTP DB tests |
| P2 L23 | Low | OTP lifetime, attempts, cooldown and hourly caps hard-coded | `otp/otp.go:20-26` | OPEN-DECISION | AD-30 |
| P2 L24 | Low | A 200 with a null envelope resets cloud staleness, even off mTLS | `licstate/licstate.go:457-465` | FIXED | `0020bf2c`; `TestNullEnvelopeOffMTLSDoesNotMarkCloudValidated` |
| P2 CC1 | CC | Dead code (`signedDo`, voucher HMAC bootstrap, `otp.Verify`, …) | various | FIXED | `9886a61b`, `1648fe08`, `bb340fb8`, `9d7a6eda`; code evidence (dead code removed) |
| P2 CC2 | CC | Duplicates: three atomic writers, two AEAD seals, a second Crockford alphabet, … | various | PARTIAL | `9d7a6eda` keybootstrap as one table. Rest: atomic writers, AEAD seals, Crockford alphabet, authctx wrappers |
| P2 CC3 | CC | assignment, offline, activation, totp and applianceauth copied line for line in control-plane | both modules | OPEN-DECISION | AD-61 |
| P2 CC4 | CC | Inconsistent key-id encodings (base64url and hex) | `activation` and the others | OPEN-DECISION | AD-61 |
| P2 CC5 | CC | Misleading comments (`Adopt` "verifies", FOR UPDATE claim, verify-before-switch) | `cmd/scd`; `authctx`; `appliancecert` | FIXED | `743ab421`, `4c6f2445`, `9886a61b`; comments corrected |
| P2 CC6 | CC | `writerguard` repeats one catalog query 11 times; `applianceBound` only logs | `writerguard`; `licstate` | PARTIAL | `f79027ac` writerguard checks each operation once. Rest: `applianceBound` refusal goes with P2 M3 (AD-15) |

### Phase 3: database and migrations (`PHASE3_DATABASE_AND_MIGRATIONS.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P3 M1 | Medium | Central's app connects as the superuser that owns every table | `deploy/env/ctrlapi.env.example:24`; `deploy/compose/central-infra.yml:44` | OPEN | Deferred-controlled: Central migration, ctrlapi DSN change and a production Central deployment |
| P3 M2 | Medium | Baseline-built appliance: the runner would re-run a migration the baseline contains | `scripts/edge-migrate.sh:286-347,589`; `scripts/generate-production-baseline.sh` | PARTIAL | `8f0cd829` runner refuses versions the DB already has; edge-migrate-selftest case 10. Rest: baseline ledger rows / one-time adopt, AD-62 |
| P3 M3 | Medium | Ledger row not written in the migration's transaction (both runners); no advisory lock | `deploy/scripts/central-migrate.sh:163-182`; `scripts/edge-migrate.sh` | FIXED | `3a586ee4` (Central), `8f0cd829` (appliance); central-migrate-selftest, edge-migrate-selftest case 11 |
| P3 M4 | Medium | A restore replays erasure-ledger entries that nothing authenticates | `data-plane/migrations/0148_*.up.sql:72-79,408-470,607-613` | OPEN | Deferred-controlled; settled as P4 M1 (AD-3) |
| P3 M5 | Medium | OTP and key-bootstrap DB tests fail against the current schema; nothing runs them | `internal/otp/otp_keyed_test.go`; `otp_device_binding_test.go`; `internal/keybootstrap/keybootstrap_test.go:156` | PARTIAL | `4b5c8f51` `scripts/db-integration-tests.sh`; `bb340fb8` OTP fixture. Rest: keybootstrap test fixture unchanged; harness not wired into FULL CHECK |
| P3 M6 | Medium | 130 foreign keys without an index; 39 cascade | catalog (e.g. `iam_v2.entitlements`, `stay_guests`, `auth_contexts`) | OPEN | Needs an EXPLAIN on synthetic year volume before choosing indexes; no migration written |
| P3 L1 | Low | Seven guard trigger functions read catalogs unqualified with no pinned `search_path` | migrations 0091, 0122, 0124, 0127, 0140, 0143 | OPEN | Needs a function-redefinition migration (`SET search_path`); not written |
| P3 L2 | Low | `central_forget_actor` matches case-sensitively and scans `audit_log` inside the delete | `control-plane/migrations/0055_*.up.sql:22,42-44` | OPEN | Needs a Central function migration, batched with P3 M1 (deferred-controlled); not integrated |
| P3 L3 | Low | Guest erasure rewrites the tenant's `audit_log` under the ledger lock | `data-plane/migrations/0147_*.up.sql:960-1005` | OPEN | Deferred-controlled; with P4 M1 (AD-3) |
| P3 L4 | Low | `central-migrate.sh` interpolates values into SQL; `--through` unvalidated | `deploy/scripts/central-migrate.sh:132,138,178,248-261` | FIXED | `3a586ee4`; central-migrate-selftest (bad `--through` and file name refused) |
| P3 L5 | Low | Central 0040 and 0041 insert their own ledger row without a checksum | `control-plane/migrations/0040_*`, `0041_*` | FIXED | `3a586ee4`; the runner's ledger row commits in the migration's transaction (selftest) |
| P3 L6 | Low | Appliance ledger stores no checksum | `scripts/edge-migrate.sh:432-440,613` | OPEN | Needs migration not integrated (ledger `checksum` column); SQL ready in the remediation record |
| P3 L7 | Low | Baseline's last covered migration in three unchecked places | baseline header; `lib-fullschema-testdb.sh:24`; `sitedb-dev.sh:20` | ALREADY-FIXED | `deploy/scripts/lib-baseline.sh` `baseline_through` (from `36fe7a7e`), used by all three readers |
| P3 L8 | Low | `ledger_confirm` trusts any hash and is granted to `svc_edged` | `0148_*.up.sql:136-146,603` | OPEN | Deferred-controlled; with P4 M1 (AD-3) |
| P3 L9 | Low | Index builds on large tables run inside a transaction | `0149_*.up.sql:13-16` | OPEN-DECISION | AD-62 |
| P3 L10 | Low | Deleting a Central appliance cascades away its cert and lifecycle history | Central 0022, 0023, 0044, 0050 | OPEN-DECISION | AD-65 |
| P3 L11 | Low | No RLS in `iam_v2`; isolation rests on every query filtering | 0 of 161 tables | OPEN-DECISION | AD-12 |
| P3 L12 | Low | Nothing checks shipped binaries lack `-tags devlicense` | `internal/buildprofile/*.go` | FIXED | `129f66ee`; BUILD_TAGS guard refuses `devlicense` and a missing production tag (5 cases) |
| P3 CC1 | CC | Runners and build scripts mostly narrative comments | `scripts/edge-migrate.sh` and others | OPEN | Clean code not taken (moving runner narrative to docs) |
| P3 CC2 | CC | Container-readiness loop copied in three scripts | `lib-fullschema-testdb.sh` and two others | OPEN | Clean code not taken (three readiness loops not merged) |
| P3 CC3 | CC | Two runners, two ledger conventions | both runners | OPEN | Needs migration not integrated (same as P3 L6); SQL ready in the remediation record |
| P3 CC4 | CC | `central-migrate.sh status` always prints "0 awaiting checksum backfill" | `deploy/scripts/central-migrate.sh` | FIXED | `3a586ee4`; central-migrate-selftest |

### Phase 4: guest sign-in, scd (`PHASE4_GUEST_SIGN_IN.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P4 M1 | Medium | A forged extension of the erasure ledger's external copy is replayed (settles P3 M4) | `internal/ledger/store.go:314-482`; `internal/ledger/assess.go`; `cmd/scd/ledger_service.go` | OPEN-DECISION | AD-3 |
| P4 M2 | Medium | Digits-only 6-character vouchers guessable by a MAC-rotating device | `cmd/scd/voucher_code_format.go:60-65`; `voucher_issue_iamv2.go:128`; `main.go:373` | OPEN-DECISION | AD-29 |
| P4 M3 | Medium | One failing entitlement stops every expiry on the site | `internal/enforce/enforce.go:252-433` | FIXED | `1128da67`, `25e918ca` (duplicate test dropped in `6bb764bb`); `TestIntegration_OneFailingExpiryDoesNotStopTheOthers` |
| P4 L1 | Low | Durable guest throttle is off unless `SCD_DURABLE_THROTTLE` is set | `cmd/scd/authsec.go:106-117,177-179` | OPEN-DECISION | AD-32 |
| P4 L2 | Low | `phone.Normalize` accepts any Unicode digit and counts bytes | `internal/phone/phone.go:31-48` | FIXED | `df10e2b3`; `TestNormalizeDigitScripts` |
| P4 L3 | Low | `namenorm` has no Unicode NFC normalisation | `internal/namenorm/namenorm.go:15-22` | OPEN-DECISION | AD-33 |
| P4 L4 | Low | Social sign-in holds a DB connection and row lock during the provider call | `cmd/scd/social_handlers.go:183-262` | FIXED | `60553c99`; `TestIntegration_SocialExchangeHoldsNoConnection` |
| P4 L5 | Low | JWKS refetch while holding the cache mutex | `internal/social/jwt.go:83-108` | FIXED | `d4df29b9`; `TestJWKSFetchDoesNotHoldTheCache` |
| P4 L6 | Low | scd does not itself require a device on social/start, account/verify and account/reset | `social_handlers.go:111-117`; `self_service_handlers.go:526,647` | PARTIAL | `c068f89d` social/start; `TestSocialStartRequiresTheDevice`. Rest: account/verify and account/reset need portald to send the device first |
| P4 L7 | Low | Most scd routes decode JSON with no size limit; nil writer passed to `MaxBytesReader` | `cmd/scd/main.go:351` and others | FIXED | `4a4c55d4`; `TestBodyLimitCapsGuestBodies` |
| P4 L8 | Low | scd operator MFA keeps no failure count; reset with no proof | `cmd/scd/operator_mfa.go:197-215,298-310` | OPEN-DECISION | AD-8 |
| P4 L9 | Low | Device and appearance rows written before the licence and protection checks | `cmd/scd/phase3_auth.go:226-249,297` | OPEN-DECISION | AD-34 |
| P4 L10 | Low | Remembered-device limiter never prunes idle keys | `cmd/scd/open_access.go:39-60` | ALREADY-FIXED | `cmd/scd/open_access.go:56-63` prunes idle keys past 20,000 (from `4c4ae2f3`) |
| P4 L11 | Low | Compliance archives written unencrypted without a backup key holder | `cmd/scd/compliance_archive.go:96-117` | OPEN-DECISION | AD-10 |
| P4 L12 | Low | Root state files (CA bundle, removed marker, restore flag) not atomic | `offline_first_activation.go:273`; `removed_from_central.go:64-67`; `restore.go:150-154,677` | FIXED | `71a38767`; `TestRemovedMarkerIsReplacedAtomically` |
| P4 L13 | Low | Double rename failure during restore still restarts services | `cmd/scd/restore.go:458-517` | FIXED | `017a3416`; `TestDoubleRenameFailureDoesNotRestartServices` |
| P4 L14 | Low | Backups authenticated only by a digest marker in the same directory; check-then-reopen | `restore.go:107-122,243,368` | OPEN-DECISION | AD-3 |
| P4 L15 | Low | Mixed clock sources (Go `time.Now()` against DB `now()`) | `internal/staygrant/staygrant.go:201` | FIXED | `539abcab`; `TestIntegration_ActivationUsesTheDatabaseClock` |
| P4 CC1 | CC | Dead code (`cloudHTTPClient`, `shq`, `decodeGuestAuthBody`, …) | `cmd/scd` | FIXED | `743ab421`; staticcheck U1000 clean for `cmd/scd` |
| P4 CC2 | CC | Two tests compare a value with itself | `internal/grace/grace_test.go:101`; `internal/stayengine/resolve_test.go:77` | FIXED | `4281aa82` (grace), `f26f9178` (stayengine); both tests now compare against the expected value |
| P4 CC3 | CC | Stale security comment about portald sharing edged's uid | `cmd/scd/main.go:974` | FIXED | `743ab421`; comment corrected |
| P4 CC4 | CC | Comment volume; root SQL built with Sprintf | `restore_financial.go:175`; `ledger_restore.go:188` | OPEN | Deferred-controlled: root SQL on the destructive restore path, needs the Linux restore drills |

### Phase 5: Client Portal, portald (`PHASE5_CLIENT_PORTAL.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P5 M1 | Medium | One guest device can exhaust the portal for every guest (no timeouts, body limits or connection limits) | `cmd/portald/main.go:622-625`; `email_handlers.go:67,134`; `deploy/nftables/stayconnect.nft:68` | FIXED | `30c6dac3` steps 1–2, `fee9b2f6` step 3; `TestPortalServersBoundSlowIdleAndOversizedRequests`, `render_portal_conn_cap_test.go` |
| P5 M2 | Low | Commerce session cookie is a bearer capability, in clear on HTTP (lowered from Medium by Phase 6) | `cmd/portald/commerce.go:51-83`; `iamv2_commerce_session.go:253-283`; `cookies.go:13` | PARTIAL | `b4d49b7e` scd checks the device on quote/confirm; `TestIntegration_CommerceRefusesAnotherConnection`. Rest: portald binding at issue (deferred-controlled) |
| P5 M3 | Medium | A broken notification provider looks healthy while no code is delivered | `internal/notifyloader/loader.go:132-172,228-269,328-355` | PARTIAL | `05a3e331` step 2; `TestIntegration_BrokenNotificationProviderRecordsItsError`. Rest: step 1 stubs (`internal/{mail,sms,whatsapp}`), step 3 AD-35 |
| P5 L1 | Low | MAC lookup IPv4-only, any interface, ignores entry state, reads the whole file | `cmd/portald/arp.go:12-33`; `arp_resolve.go:42-67` | OPEN | Deferred-controlled: changes guest-auth outcomes; netlink lookup needs a Linux host. Perf half D3-L1 fixed |
| P5 L2 | Low | Flood damper misses routes that check secrets or send messages | `cmd/portald/main.go:524-596` | FIXED | `30c6dac3`; `TestEverySecretMessageAndDeviceRouteIsDamped` |
| P5 L3 | Low | Expired commerce sessions never removed | `cmd/portald/commerce.go:51-71` | FIXED | `30c6dac3`; `TestExpiredCommerceSessionsAreRemoved` |
| P5 L4 | Low | Design image fields accept any appliance path; CSS `url()` any https host | `internal/portaldesign/urls.go:91-107`; `design.go`; `css.go` | FIXED | `b9b3f752`; `TestDesignImagesCannotNameAnotherApplianceRoute` (check saved designs before deploying) |
| P5 L5 | Low | Unsealable provider secret silently falls back to the plaintext column | `internal/notifyloader/loader.go:104-110` | FIXED | `05a3e331`; `TestIntegration_BrokenNotificationProviderRecordsItsError` |
| P5 L6 | Low | Cancelled-send failure not recorded; byte truncation breaks UTF-8 | `internal/notifyloader/loader.go:333,357-362` | FIXED | `05a3e331`; `TestHealthRecordedAfterCancelledSend`, `TestTruncateIsRuneSafe` |
| P5 L7 | Low | Admin SMTP host and port unrestricted; server reply shown back | `internal/mail/smtp.go:52-63,114-143` | OPEN-DECISION | AD-13 |
| P5 L8 | Low | Email format: constant boundary, no transfer encoding or Message-ID, raw From name | `internal/mail/smtp.go:215-240` | FIXED | `932e7361`; `TestTheRenderedMessageIsWellFormedWhateverTheBodyHolds` |
| P5 L9 | Low | Twilio and SendGrid do not validate the destination | `internal/sms/twilio.go`; `internal/mail/sendgrid.go` | FIXED | `932e7361`; `TestTwilioRefusesAMalformedNumberWithoutCallingTheProvider`, SendGrid twin |
| P5 L10 | Low | Loose design value checks (unbalanced font quotes, 5- or 7-digit hex) | `internal/portaldesign/design.go:75-77,272` | FIXED | `b9b3f752`; `TestColoursAndFontStacksAreWellFormed` |
| P5 L11 | Low | Provider rows unordered; no one-enabled-per-channel rule; validator mismatch | `internal/notifyloader/loader.go:81-98,214-226` | PARTIAL | `05a3e331` code half; `TestFirstRowPerChannelWins`. Rest: unique index not integrated (aborts an upgrade if duplicates exist), SQL ready in the remediation record |
| P5 CC1 | CC | Dead code (`humanDuration`, `black`, `portal_base`, …) | `cmd/portald`; `notifyloader` | FIXED | `30c6dac3`, `4e9e65bc`, `05a3e331`, `b4d49b7e`; dead code removed (`sms.Message.From` kept: exported API) |
| P5 CC2 | CC | Two cookie setters; codegen ModeComplex equals ModeAlnum | `account_handlers.go:57-69`; `codegen` | PARTIAL | `30c6dac3` one cookie setter. Rest: codegen `ModeComplex`, AD-29 |
| P5 CC3 | CC | Stale security comments (shared service user; Caddy TLS) | `cmd/portald/main.go:132`; `social_handlers.go:150` | FIXED | `30c6dac3`; comments corrected |
| P5 CC4 | CC | 2,272-line `templates.go`; invisible format characters; three wrappers | `cmd/portald/templates.go`; `languages.go` | PARTIAL | `48cc6a36` format characters escaped; `TestSourcesCarryNoInvisibleFormatCharacters`; wrappers `05a3e331`. Rest: `templates.go` split not taken |

### Phase 6: payments, posting, accounting (`PHASE6_PAYMENTS_AND_POSTING.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P6 M1 | Medium | Room-charge outbox retries with no backoff; one refused charge blocks the PMS lane | `cmd/scd/posting_worker.go:68-110`; `internal/posting/repo.go:61-82`; `engine.go:309-361` | OPEN-DECISION | AD-41. Backoff columns not integrated; SQL ready in the remediation record |
| P6 M2 | Medium | acctd's plan counter is not durable; a lost counter freezes enforcement | `cmd/acctd/phase3_envelope.go:38-78`; `internal/shapeplan/plan.go:157-164` | FIXED | `1d5ac1fc`; `TestFirstPlanAfterLostCounterIsNotStaleOnNetd` |
| P6 M3 | Medium | Provider reversal errors swallowed; a refused chargeback never recorded | `internal/payment/checkout.go:473-517` | PARTIAL | `44fd44cb` Go half; `TestProviderRefundsFailedReadRecordsNothing`. Rest: cumulative function not integrated (financial path, SQL ready in the remediation record); review case AD-42 |
| P6 M4 | — | Shared-allowance re-rate (merged) | see P8 M4 | — | Counted once as P8 M4 (FIXED, `cb24e7b1`) |
| P6 L1 | Low | A capture after a checkout was marked failed is never noticed | `internal/payment/checkout.go:313-400`; `paymob/paymob.go:538-547` | OPEN-DECISION | AD-43 |
| P6 L2 | Low | Dormant execute and notification path grants without an amount check; test double in the prod build | `internal/payment/engine.go:300-354`; `provider.go:15-19`; `testdouble.go:74` | PARTIAL | Test doubles out of the production build (`//go:build !stayconnect_production` on `testdouble.go`; the file keeps its path because T0037/T0038 cite it). Rest: dormant execute path, AD-44 |
| P6 L3 | Low | Stripe ambiguous-create resolution fails on busy accounts (50×100 cap) | `internal/payment/stripe/stripe.go:41-44` | FIXED | `abcc80bc`; `TestResolveByClientRefOnABusyAccount` |
| P6 L4 | Low | Paymob ownership unchecked when the inquiry omits order fields | `internal/payment/paymob/paymob.go:564-569` | FIXED | `abcc80bc`; `TestTransactionWithoutOrderFieldsFailsClosed` |
| P6 L5 | Low | Checkout start edge cases (redirect save failure; idempotency 500) | `internal/payment/checkout.go:216-221`; `engine.go` | FIXED | `135956d6`; `TestClassify_ConcurrentCheckoutStartIsNotExecutable` |
| P6 L6 | Low | Check-out boundary inputs unvalidated; old PMS check-out time trusted | `internal/checkout/checkout.go:148-157,342-349` | OPEN-DECISION | AD-45 |
| P6 L7 | Low | One slow PMS stalls the other posting lanes | `cmd/scd/posting_worker.go:94-107` | FIXED | `3650520e`; `TestLaneSchedulerRunsLanesIndependently` |
| P6 L8 | Low | A charge accepted during shutdown becomes UNKNOWN | `internal/posting/engine.go:197-204,327` | FIXED | `cd2695c9`; `TestIntegrationPosting_AnswerDuringShutdownIsStillRecorded` |
| P6 L9 | Low | Attempt and requeue updates lack state guards | `internal/posting/repo.go:612-616`; `engine.go:417-424` | ALREADY-FIXED | Baseline trigger `posting_attempts.outcome is terminal` and `p4_consume_retry_authorization`; `TestIntegrationPosting_RetryAuthorizationIsConsumedExactlyOnce` |
| P6 L10 | Low | Post-stay PIN edge cases (raw error, lockout by id, expired reset) | `internal/poststay/poststay.go:248-354` | PARTIAL | `d69a1cdc` malformed id; `TestIntegration_F8_MalformedProfileIsTheSameAnswer`. Rest: lockout key and expired reset, AD-36 |
| P6 L11 | Low | Transfer reason not stored; no tie-break; raw DB errors | `internal/transfer/transfer.go:142,157,233-244` | PARTIAL | `93b8e6ae` deterministic landing package; `TestIntegration_F9_LandingPackageChoiceIsDeterministic`. Rest: reason storage, AD-46 |
| P6 L12 | Low | Shaping input trusted (zero rate = 1 Gbit, IFB name truncation, group-minor collisions) | `internal/shape/shape.go:78-110,181-189,311-318`; `internal/shapeplan/plan.go:135-166` | PARTIAL | `4779ff77` bridge names and IFB collisions; `internal/shapeplan/plan_test.go`. Rest: group-minor collisions = D4-M3 (deferred-controlled) |
| P6 L13 | Low | acctd watcher baseline race; missing class read as 0 bytes | `cmd/acctd/main.go:111-123`; `phase3_accounting.go:128-140` | FIXED | `4c7fd5e3`; `TestIntegration_Acct_MissingClassIsNotZeroUsage`, `TestWatchBaselineIsTheResolvedVersion` |
| P6 L14 | Low | Default DB URLs carry a password and disable TLS (acctd, scd, edged, netd) | `cmd/{acctd,scd,edged,netd}/main.go` | PARTIAL | `4c7fd5e3` acctd requires its URL; installer backfill `03650f13`. Rest: scd, edged, netd fallbacks, AD-64 |
| P6 L15 | Low | Card status limiter wiped when full | `cmd/scd/card_checkout.go:270-282` | FIXED | `3650520e`; `TestCardStatusLimiterPrunesInsteadOfResetting` |
| P6 CC1 | CC | Dead code (acctd revoke path, dormant payment execute, `FormatCT`, `cidClass`) | various | PARTIAL | `4c7fd5e3`, `4c66bc73`, `4779ff77` dead code removed. Rest: dormant payment path, AD-44 |
| P6 CC2 | CC | Duplicated provider transport helpers and hash helpers | `payment/stripe`, `paymob`; `postinghandoff` | PARTIAL | `4c66bc73` shared body hash. Rest: Stripe/Paymob transport helpers (clean code not taken) |
| P6 CC3 | CC | Ignored errors; `iam_v2.stays` updated by id alone; `AssignmentGe` typo | `internal/checkout`; `phase3_envelope.go` | FIXED | `78039178`, `44fd44cb`; checkout phase4/phase5 suites |
| P6 CC4 | CC | No tests for `shapeplan` and `shapeproducer`; phase5-tag suites never run | `internal/shapeplan`; `shapeproducer` | FIXED | `4779ff77`; new `internal/shapeplan/plan_test.go`, `internal/shapeproducer/producer_test.go` |

### Phase 7: PMS, pmsd (`PHASE7_PMS.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P7 M1 | Medium | A FIAS frame has no length limit | `internal/pms/protel_fias.go:373-391`; `internal/pmsd/adapter_fias.go:203` | FIXED | `81b6a380`; `TestReadFramedRecord_*`, `TestFrames_UnterminatedFrameTerminatesAsFraming` |
| P7 M2 | Medium | A repeated record inside one resync can make every resync fail | `internal/pmsd/pg.go` `insertInboxRow` (only LIVE has `ON CONFLICT`) | FIXED | `6040c3af`; `TestBarrier_RepeatedRecordInsideResyncIsSkipped`, `TestIntegration_LiveVsResyncIdempotency` |
| P7 L1 | Low | Live-event barrier does not close on a gap | `internal/pmsd/worker.go:356-414`; `adapter_fias.go:228-247` | FIXED | `6040c3af`; `TestBarrier_GapClosesLiveAdmissionUntilResyncPublishes` |
| P7 L2 | Low | FIAS TCP dial has no timeout | `internal/pmsd/adapter_fias.go:106`; `cmd/pmsd/main.go:90-92` | FIXED | `25c2d146`; `TestFIASDial_TCPConnectHonoursDialTimeout` |
| P7 L3 | Low | A certificate pin without TLS is silently ignored (pmsd side; edged holds) | `internal/pmsd/pg.go:117-118`; `pmsd.go` | FIXED | `6ebea1ca`; `TestRevision_TLSPinCoherence` |
| P7 L4 | Low | REST clients follow redirects with credentials; private hosts allowed | `internal/pmsrest/client.go`; `mews.go`; `internal/pmsprovider/config.go:120-134` | PARTIAL | `765b33d0` redirects never followed; `TestRESTHardening_RedirectNotFollowed`. Rest: private provider hosts, AD-40 |
| P7 L5 | Low | REST paging: stuck cursor up to 10,000 requests; partial list at the cap | `internal/pmsrest/mews.go:102-135`; `apaleo.go`; `operacloud.go` | FIXED | `765b33d0`; `TestMewsHardening_StuckCursorFailsFast` |
| P7 L6 | Low | REST records with blank fields pass through | `internal/pmsrest/mews.go:124-138`; `client.go` | NOT-REPRODUCIBLE | Every consumer refuses a blank id or room (`internal/pmsd/adapter_rest.go` `event()`); blank names/dates stored as NULL |
| P7 L7 | Low | Legacy provider layer still loaded and served (`/cache` returns guest rows) | `internal/{pms,pmsguard,pmsloader}`; `cmd/scd/pms_admin.go` | OPEN-DECISION | AD-39 |
| P7 L8 | Low | pmsd key settings fail late, unnamed; no minimum length | `cmd/pmsd/main.go:80-89` | FIXED | `6ebea1ca`; `TestDecodeKeyEnv` |
| P7 L9 | Low | A FIAS drop mid-roster is not recorded as interrupted | `internal/pmsd/worker.go:337`; `adapter_fias.go` | FIXED | `6040c3af`; `TestFrames_LinkDropMidRosterReportsDisconnect`, `TestBarrier_DisconnectMidWindowIsInterrupted` |
| P7 L10 | Low | Reconnect jitter can be near zero | `internal/pmsd/supervisor.go:134-150` | FIXED | `25c2d146`; `TestBackoff_EqualJitterBounds` |
| P7 L11 | Low | Lock housekeeping has no timeout; default pool size shared | `internal/pmsd/pg.go:388-415` | FIXED | `5785a90b`, `789cd846`; `TestPoolConfig` |
| P7 L12 | Low | Applier errors logged in full (may carry key values) | `internal/pmsd/applier.go:63` | FIXED | `5785a90b`; `TestApplierLog_DatabaseErrorTextNotLogged` |
| P7 L13 | Low | Cached resolution replayed by request id alone | `internal/pmsresolve/fanout.go:187-203` | FIXED | `4281aa82`; `TestIntegration_AResolutionIsNotReplayedOnAnotherNetwork` |
| P7 L14 | Low | Relay in-memory guards bounded loosely | `internal/pmsd/financial_relay.go:116-141,196-217` | FIXED | `5785a90b`; `TestRelay_CarriedSetEvictsOldestFirst` |
| P7 CC1 | CC | Dead code (`BoundedQueue`, Opera `Changes` stub, the legacy layer) | `internal/pmsd`; `internal/pms` | PARTIAL | `447d88ae` unused accessor and field removed. Rest: legacy layer goes with P7 L7 (AD-39) |
| P7 CC2 | CC | Tests that cannot fail; no tests for `pmsguard` or `cmd/pmsd` | `internal/pmsd/secret_test.go:49`; `pmsresolve/resolve_test.go:59` | FIXED | `f26f9178`, `6ebea1ca`, `4281aa82`; tests pinned to expected values; `cmd/pmsd/main_test.go` |
| P7 CC3 | CC | Stale comments (root-only socket; barrier) | `financial_relay.go`; `adapter_fias.go` | FIXED | `6040c3af`; comments corrected |
| P7 CC4 | CC | Long `Serve` state machine; duplicate REST providers; unchecked close errors | `adapter_fias.go`; `internal/pms`; `fias_tls.go:51,62` | PARTIAL | `25c2d146` close errors handled. Rest: `Serve` split (deferred-controlled), duplicate REST providers with P7 L7 |

### Phase 8: networking, netd (`PHASE8_NETWORKING.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P8 H1 | High | A network name can inject rules into the firewall | `internal/netcfg/render_nft.go:211-214`; `validate.go:37-39`; `cmd/edged/resources_network*.go` | ALREADY-FIXED | `60e15df7` (PR #242, `fa24a913`, T0248/D72); re-confirmed by the netd workstream |
| P8 M1 | Medium | Portal has no per-device connection or rate limit at the firewall | `internal/netcfg/render_nft.go:154-158,164-167` | PARTIAL | `fee9b2f6` 256 connections per source; `render_portal_conn_cap_test.go`. Rest: new-connection rate limit and settings, AD-26 |
| P8 M2 | Medium | Guests can reach hotel networks in 10.0.0.0/8 and other private ranges | `internal/netcfg/model.go:111-118`; `render_nft.go:206-208` | OPEN-DECISION | AD-22 |
| P8 M3 | Medium | Authorization tied to the IP address, not the device | `render_nft.go:84,89,216,218`; `cmd/netd/phase3_address_owner.go` | OPEN-DECISION | AD-23 |
| P8 M4 | Medium | Shared-allowance re-rate gives every device the whole allowance (= P6 M4) | `internal/shape/shape.go:306-326,445-451`; `cmd/netd/phase3_provision.go:148-160` | FIXED | `cb24e7b1`; `TestSpeedAllocation_SharedReRateKeepsTheGroupAndFollowsTheAllowance` |
| P8 M5 | Medium | Open connections outlive the end of a guest's authorization | `render_nft.go:196`; `cmd/netd/phase3_gate.go:404-449` | OPEN-DECISION | AD-24 |
| P8 M6 | Medium | A network change that dies mid-apply blocks every later change | `cmd/netd/store.go:367-380`; `main.go:461-475` | ALREADY-FIXED | `490321c1` (PR #242, T0248) |
| P8 M7 | Medium | Rollbacks report success when they failed | `cmd/netd/apply_ops.go:564-571`; `sysnet.go:331-335,470-490` | FIXED | `490321c1` + `8031a3d2` (failed revert kept and retried); `TestSysnetFailedRevertKeepsTheMarkerAndTheWatchdogRetries` |
| P8 M8 | Medium | Accepted-plan generation not saved durably (netd half of P6 M2) | `cmd/netd/phase3_control.go:80-95`; `phase3_shaping.go:186-188` | FIXED | `c4393c53`; `TestAcceptedGenerationSaveFailureIsDegraded` |
| P8 M9 | Medium | Other config values written without validation or escaping | `render_nft.go:141`; `render_netplan.go:46`; `apply_ops.go:200,285`; `internal/sysnet/sysnet.go:60-77,155-170,295` | FIXED | `60e15df7` + `8031a3d2` (sysnet names, quoted YAML); `TestHostileInterfaceNamesAreRefused` |
| P8 M10 | Medium | A rejected apply can still reach the live firewall | `cmd/netd/apply.go:224-252` | ALREADY-FIXED | `490321c1` (PR #242, T0248) |
| P8 L1 | Low | Client-isolation switch has no effect | `render_nft.go:188-195,209`; `model.go:57` | OPEN-DECISION | AD-27 |
| P8 L2 | Low | IPv6 leftovers (forwarding=1; link-local on guest bridges) | `deploy/sysctl/99-stayconnect.conf:3`; netplan render | OPEN-DECISION | AD-25. Sysctl change `b86a7446` reverted in `128ee02a` |
| P8 L3 | Low | One failed class-state save denies every new guest until restart | `phase3_shaping.go:418-426`; `phase3_journal.go:329` | FIXED | `c4393c53`; `TestClassStateSaveFailureClearsAfterTheNextSave` |
| P8 L4 | Low | Journal bookkeeping (strike history lost; invalid record persisted) | `phase3_journal.go:350-356`; `phase3_provision.go:145` | FIXED | `c4393c53`; `TestDurable_AFailedWriteAheadKeepsTheStrikeHistory` |
| P8 L5 | Low | A plan without a MAC skips the ownership check | `phase3_address_owner.go:59`; `phase3_provision.go:62` | OPEN-DECISION | AD-23 |
| P8 L6 | Low | A class clash lets a third session through | `phase3_shaping.go:258-269` | FIXED | `c4393c53`; `TestAThirdSessionOnAContestedClassIsRefused` |
| P8 L7 | Low | Stray sweep matches by class number, not IP | `phase3_shaping.go:340-345` | FIXED | `c4393c53`; `TestStrayAuthorizationWithTheSameClassNumberIsRemoved` |
| P8 L8 | Low | Locking details (`endFlows` without defer; unlocked `mode.Active`) | `flows.go:486-506`; `phase3_shaping.go:620-626`; `phase3_scope_watch.go:45` | FIXED | `cc356df2`; `TestFlowsEndReleasesTheLockOnPanic` |
| P8 L9 | Low | Plan pass runs under the request context while holding the lock | `phase3_shaping.go:153-156`; `main.go:264` | FIXED | `6d736bb2`; `TestShapingPassIsNotCancelledWithTheRequest` |
| P8 L10 | Low | conntrack dump held in memory; DUMP_INTR unchecked | `internal/conntrack/netlink_linux.go:41-98` | FIXED | `1a05b409`; `TestInterruptedDumpIsRecognised`, flow-teardown netns test |
| P8 L11 | Low | `ifaceAddrs`, `ifaceHasIP` and `ifaceHasAnyIP` run commands without a timeout | `cmd/netd/apply_ops.go` (still `exec.Command` at HEAD) | FIXED | `6d736bb2`; `TestInterfaceQueriesAreBounded` |
| P8 L12 | Low | Config files not written atomically (netplan, Unbound, Kea, system network) | `apply_ops.go`; `kea_bootstrap.go:86`; `sysnet.go:283,289` | FIXED | `490321c1` + `8031a3d2` (all boot files atomic); `TestSysnetNetplanFileIsReplacedAtomically` |
| P8 L13 | Low | nft watch failures only logged; no backoff or event | `cmd/netd/nft_watch.go` | FIXED | `6d736bb2`; `TestPeriodicReassertionFailuresAreReportedAndBackedOff` |
| P8 L14 | Low | Smaller system-network checks (WAN verify path, DHCP mode, address classes, confirm lock) | `cmd/netd/sysnet.go:326-364,500-515`; `internal/sysnet/sysnet.go:95` | PARTIAL | `8031a3d2` netd checks, `7681873f` edged sends the revision; `TestModeAndAddressClassesAreValidated`. Rest: AD-28 |
| P8 CC1 | CC | Dead code; no-op `sanitize()` | `cmd/netd`; `sysnet.go:822-824` | FIXED | `8031a3d2`, `6d736bb2`; dead code and no-op `sanitize` removed |
| P8 CC2 | CC | Kernel gate loads a hand-written ruleset, not `RenderNftables` | `scripts/ci/kernel-netns-suite.sh` | OPEN | Deferred: needs a kerneltest render helper and CI kernel modules (veth, dummy, ifb, fq_codel) |
| P8 CC3 | CC | Ignored errors and inconsistent file-write patterns | `apply.go:214` and others | FIXED | `49cb5d1e`; code evidence (revision writes logged) |
| P8 CC4 | CC | Comment volume | `cmd/netd` | OPEN | Clean code not taken (comment volume) |

### Phase 9: Admin Console API, edged (`PHASE9_ADMIN_CONSOLE_API.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| P9 M1 | Medium | Request bodies have no size limit; server sets only `ReadHeaderTimeout` | `cmd/edged/common.go:40-48`; `resources_sysnetwork.go`; `resources_cert.go:44`; `main.go:468` | FIXED | `3f4e8760`; `TestRequestBodiesAreBounded` (Caddy `request_body` layer is a deploy follow-up) |
| P9 M2 | Medium | Email, SMS and social sign-in settings change without the password step-up | `cmd/edged/resources_providers.go:274-480,759-1016` | OPEN | Deferred-controlled (guest-auth batch with P9 L16); needs PO authorization of that batch |
| P9 M3 | Medium | A restore does not check the backup's schema against the installed software | `cmd/scd/restore.go:617-645`; `cmd/edged/resources_backups.go:395-445` | OPEN | Deferred-controlled: fix is in `cmd/scd/restore.go` (destructive path) |
| P9 M4 | Medium | Guest-data read audit misses routes; no test catches a new one | `cmd/edged/guest_data_reads.go:30-52,192-196` | PARTIAL | `cf3553c9` missing routes recorded; `TestEveryGuestDataRouteIsRecordedOrClassified`. Rest: list browsing, AD-7 |
| P9 M5 | Medium | edged stores network names, domains and hostnames unchecked (edged side of P8 H1, M9) | `cmd/edged/resources_network.go:233-259,751,778`; `resources_network_replace.go:76-92` | FIXED | `60e15df7` + `e9b04b9e` (reserved IP inside subnet); `TestReservedIPMustBeInsideTheSubnet` |
| P9 L1 | Low | Raw DB and transport errors returned to operators | many `resources_*.go` | FIXED | `0477ac7b`; `TestInternalErrHidesDetailButKeepsOperatorRefusals` |
| P9 L2 | Low | Secrets in responses lack `Cache-Control: no-store`; desk password reset has no reason | `guest_accounts_iamv2_lifecycle.go:482-570`; `resources_vouchers.go:469-525`; `resources_guest_accounts.go:105` | PARTIAL | `d60ab97c` no-store; `TestAPIAnswersAreNotCacheable`. Rest: reason for desk reset, AD-8 |
| P9 L3 | Low | `verifyPassword` trusts the hash's own argon2 parameters | `cmd/edged/auth.go:39-60` | FIXED | `de69aa4a`; `TestVerifyPasswordRefusesAHashWithHostileParameters` |
| P9 L4 | Low | Anyone reaching the console can lock a named operator out | `operator_signin.go:235-270`; `operator_signin_settings.go:40-49` | OPEN-DECISION | AD-8 |
| P9 L5 | Low | `middleware.RealIP` lets a client set `RemoteAddr` (True-Client-IP) | `main.go:294`; `common.go:80-95` | FIXED | `3f4e8760`; `TestClientIPIgnoresForwardingHeadersFromANonLoopbackPeer` |
| P9 L6 | Low | System-network confirm has no step-up and starts an unbounded detached task | `resources_sysnetwork.go:55-66` | PARTIAL | `83889fb6` renewal after netd confirms; `TestHotelAdminCertRenewalRunsOnceAtATime`. Rest: step-up on confirm, AD-8 |
| P9 L7 | Low | Backup download serves any file in the folder; follows symlinks | `resources_backups.go:~268,332`; `backup_download.go` | PARTIAL | `c0770d46` no symlinks; `TestResolveArtifactRefusesSymlinks`. Rest: download scope, AD-9 |
| P9 L8 | Low | Listing backups re-hashes every dump on each call | `resources_backups.go:~190` | FIXED | `c0770d46`; `TestVerificationDigestIsReusedUntilTheFileChanges` |
| P9 L9 | Low | Portal asset upload written in place; single Read; 8 MB per request | `resources_portal_assets.go:~110-121` | FIXED | `85f9789f`; `TestAssetIsWrittenAtomically`, `TestJPEGNeedsAMarkerAfterTheStartOfImage` |
| P9 L10 | Low | One-time-code reveal skips `validReason`; audit after the reveal | `resources_code_requests.go:102-133` | FIXED | `8aeb73a5`; `TestCodeReveal_ReasonIsBoundedLikeOtherReveals` |
| P9 L11 | Low | Old `?q=` search fallback puts guest search text in URLs | `resources_usage.go:98,622`; `guest_data_rights.go:~58` | FIXED | `f740c17f`; integration test asserts a `?q=` search gets 400 |
| P9 L12 | Low | Paging overflow; unbounded date ranges; unchecked uuid casts; `?fresh=1` probe | `resources_helpers.go:35` and others | PARTIAL | `5acaeef0` page cap, ordered dates, rationed probe; `TestParsePage_DefaultsAndBounds`. Rest: maximum window, AD-56 |
| P9 L13 | Low | Free-text bounds missing in the data-rights register and client groups | `guest_data_register.go:49-55`; `resources_client_groups.go:249,322,388` | FIXED | `db718536`; `TestIntegration_GuestDataRegister_*` |
| P9 L14 | Low | Shortening a retention period shows no impact; old-value read ignores errors | `data_retention_admin.go:131-200` | PARTIAL | `eedc777c` unreadable old value refuses. Rest: impact preview, AD-67 |
| P9 L15 | Low | Failed secret store after update leaves the provider half changed, unaudited | `resources_providers.go:456-466` | FIXED | `683e6cba`; `TestIntegration_NotifyProviderSecretRefusalChangesNothingAndIsAudited` |
| P9 L16 | Low | Guest device self-service switch changes without step-up | `resources_phase6_setting.go:97` | OPEN | Deferred-controlled (with P9 M2); needs a console password field |
| P9 CC1 | CC | Dead code; two SA4000 self-comparing tests | `cmd/edged` | FIXED | `0bab433e`, `e5f65d69`; staticcheck U1000/SA4000 clean |
| P9 CC2 | CC | Four step-up helpers, per-file error writers, duplicate hashing | `cmd/edged` | FIXED | `9e1f70ff`; one shared step-up helper |
| P9 CC3 | CC | Large mixed-resource files (over 1,000 lines) | `resources_overview.go` and others | OPEN | Clean code not taken (split when a batch touches the files) |
| P9 CC4 | CC | Comment volume; integration suites cannot be re-run on one DB | `cmd/edged` tests | FIXED | `3f69cc89`; suites pass back to back on one database |

### Phase 10: cross-cutting (`PHASE10_CROSS_CUTTING.md`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| X1 | Low | (= P0-3, corrected and lowered) | see P0-3 | — | Merged into P0-3 |
| X2 | High | (= P0-2, re-checked on `0879e839`) | see P0-2 | — | Counted once as P0-2 |
| X3 | Low | Five services, ctrlapi included, fall back to a DB URL with a published password and TLS off | `control-plane/internal/config/config.go:29`; `cmd/{scd,acctd,edged,netd}/main.go` | PARTIAL | `bea535c9` ctrlapi (`TestProductionRefusesTheDefaultDatabaseURL`), `4c7fd5e3` acctd, `03650f13` installer backfill. Rest: scd, edged, netd, AD-64 |

Not findings in the source, so no rows: X4 (no real hard-coded credentials), X5 (race detector, no races), and the ten cross-phase patterns P1–P10. The patterns group the findings above; they add none.

---

## B. Performance audit (`docs/performance-audit/`)

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| B0-1 | Medium | Nothing to measure performance with (no benchmarks, per-route timings or profiler) | whole repository | OPEN | Out of scope for all workstreams (service code: histograms, pprof, benchmarks) |
| B0-2 | CC | Measurement code scattered; each service builds its own `http.Server` | `data-plane/internal/metrics`; `internal/reqlog` | OPEN | Out of scope (shared HTTP server package) |
| D1-H1 | High | Every controlled operation scans an hour of bookkeeping rows | `iam_v2.begin_controlled_operation` (`controlled_operation_scope` janitor) | FIXED | Migration 0159 (`47ed5065`), `controlled_operation_scope(opened_at)`; verified on a full-schema test DB |
| D1-H2 | High | acctd records every online device every second, one transaction each | `cmd/acctd/main.go:181` | OPEN-DECISION | AD-47 |
| D1-H3 | High | Accounting detail grows faster than anything removes it (disk fills in weeks) | `iam_v2.accounting_records` | OPEN-DECISION | AD-48 |
| D1-M1 | Medium | Finding live sessions reads every session ever recorded | `cmd/acctd/phase3_accounting.go:44-48` | FIXED | Migration 0159 (`47ed5065`), live-sessions partial index; verified on a full-schema test DB |
| D1-M2 | Medium | Postgres tuned as if it owned the whole appliance | `deploy/compose/infra.yml:12-24` | OPEN-DECISION | AD-50 |
| D1-M3 | Medium | No service chooses its pool size; every pool has 4 connections | every appliance `pgxpool.New` | OPEN-DECISION | AD-50 (pmsd pool already sized in `789cd846`) |
| D1-L1 | Low | Per-role connection limits add up to more than Postgres allows | `deploy/gatep/gatep-roles.sql:31-38` | OPEN-DECISION | AD-50 |
| D1-L2 | Low | No DB guard rails or slow-query evidence (timeouts 0) | Postgres settings | OPEN-DECISION | AD-50 |
| D1-L3 | Low | Rows updated every second miss HOT updates (fillfactor 100) | `iam_v2.sessions`, `entitlements`, `accounting_checkpoints` | OPEN | Needs migration not integrated (fillfactor; rebuild is a maintenance-window action); SQL ready in the remediation record |
| D1-L4 | Low | The accounting hypertable is the one nothing writes any more | `public.accounting_records` | OPEN-DECISION | AD-48 |
| D1-L5 | Low | Several per-event tables are never trimmed | migrations / Go (no DELETE) | OPEN-DECISION | AD-49 |
| D1-L6 | Low | edged builds a payment engine (could open a pool) on every financial request | `cmd/edged/resources_phase4_finops.go:422` | FIXED | `dfd269f9`; `TestPaymentEngineIsBuiltOnceAndFailuresAreRetried` |
| D1-L7 | Low | Accounting purge's hourly cap fixed in code | `cmd/acctd/phase3.go:121-123` | OPEN-DECISION | AD-48 |
| D2-H1 | High | Password hashing has no limit on how many run at once (about 30 concurrent = 4 GB) | `internal/passwordhash/passwordhash.go:17-22` | FIXED | `f34e969e` steps 1, 5; `TestConcurrentHashesAreBounded` (optional step 2 "busy" answer: AD-35) |
| D2-M1 | Medium | Slow work runs inside DB transactions and holds scd's connections | `internal/iamv2/adapters.go:18-46` | FIXED | `60553c99`; `TestIntegration_SocialExchangeHoldsNoConnection`, `TestAccountWrongPasswordOpensOneTransaction` |
| D2-M2 | Medium | Every session activation counts every session ever, under an appliance-wide lock | `cmd/scd/iamv2_session_ops.go:111-128` | FIXED | Migration 0159 (`47ed5065`), open-sessions-by-device index |
| D2-M3 | Medium | Appliance-wide sign-in ceiling fixed in code; OTP charges it twice | `cmd/scd/authsec.go:21-28` | OPEN-DECISION | AD-51 |
| D2-M4 | Medium | Room sign-ins at one PMS run one at a time (runtime-row lock) | `iam_v2.issue_or_return_pms_context` | OPEN | Deferred-controlled: waits for the Phase 11 room-sign-in measurement |
| D2-L1 | Low | Durable throttle costs 6–9 statements per attempt and shares one row | `internal/throttle/throttle.go:172-245` | OPEN-DECISION | AD-51 |
| D2-L2 | Low | Settings read from the DB on every sign-in | `guest_method_switch.go:48`; `tenantcfg.go:73` | OPEN-DECISION | AD-52 |
| D2-L3 | Low | Room offer list runs a few queries per package | `cmd/scd/phase3_offers.go:102` | OPEN | Deferred: after the Phase 11 S3 measurement |
| D2-L4 | Low | Last sign-in step waits for netd by polling | `cmd/scd/phase3_auth.go:972-1010` | OPEN-DECISION | AD-52 |
| D2-L5 | Low | scd accepts any number of requests at once | `cmd/scd/main.go:975` | FIXED | `28bebc5f`; `TestGuestCapacityRefusesBeyondTheCap` |
| D2-CC1 | CC | Argon2 parameters defined three times | `internal/passwordhash`; `internal/poststay/poststay.go:41-43`; `cmd/edged/auth.go:20-22` | FIXED | `c3476d7d` (poststay), `de69aa4a` (edged); `TestPINHashesStayCompatible` |
| D2-CC2 | CC | Each sign-in method repeats the same lookups | scd handlers | OPEN-DECISION | AD-52 |
| D3-M1 | Medium | Landing page rebuilt and compressed for every request, including unknown paths | `cmd/portald/main.go:155-182,589-598` | PARTIAL | `30c6dac3` unknown paths get a small redirect; `TestUnknownPathsAreASmallRedirectAndTheAppleProbeStillAPage`. Rest: page cache with CSP hashes (deferred-controlled) |
| D3-M2 | Medium | Each page view makes three more calls, two reaching scd and its DB | `templates.go:1117`; `email_handlers.go:27-55` | PARTIAL | `30c6dac3` single-flight design fetch; `TestADesignExpiryUnderLoadAsksScdOnce`. Rest: step 2, AD-57 |
| D3-L1 | Low | MAC lookup reads the whole neighbour table | `cmd/portald/arp.go:13-34` | FIXED | `30c6dac3`; `TestTheNeighbourLookupMatchesExactlyWithoutSplittingEveryLine` |
| D3-L2 | Low | Hotel images: no cache lifetime, no size limit beyond 8 MB | `cmd/portald/main.go:570-571` | PARTIAL | `30c6dac3` immutable cache header; `TestUploadedImagesAreCacheableAndMissesAreNot`. Rest: edged re-encode, AD-58 |
| D3-L3 | Low | Every TLS handshake stats the certificate files | `cmd/portald/portal_https.go:356-386` | FIXED | `30c6dac3`; `TestCertificatesAreServedBySNIAndReloadedWhenReplaced` |
| D3-L4 | Low | scd client keeps only two idle connections | `cmd/portald/main.go:92-106` | FIXED | `30c6dac3`; `TestTheScdClientKeepsIdleConnectionsForABurst` |
| D3-L5 | Low | Busy scd shown as "service unavailable" with no retry | `cmd/portald/main.go:235,258`; `client_resume.go:160` | PARTIAL | `28bebc5f` scd answers 503 with Retry-After. Rest: portald "busy" wording and retry, AD-35 |
| D3-L6 | Low | Per-request work that is the same for every request | `portal_page.go:123` and others | OPEN | Deferred-controlled: folded into the D3-M1 page cache |
| D4-H1 | High | One guest network cannot enforce more than 2,047 devices | `internal/shape/shape.go:458` | OPEN | Deferred-controlled: enforcement-core redesign; needs reference-VM kernel benchmarks |
| D4-H2 | High | Enforcement pass does per-device process and DB work every second | `cmd/acctd/main.go:178,201`; `cmd/netd/phase3_shaping.go:154,202` | OPEN | Deferred-controlled: acctd/netd redesign; tick follows AD-47 |
| D4-H3 | High | Every packet matched against device filters one by one | `internal/shape/shape.go:458` | OPEN | Deferred-controlled: same batch as D4-H1 |
| D4-M1 | Medium | Revoking a device reads the whole conntrack table | `internal/conntrack/netlink_linux.go:16`; `cmd/netd/phase3_gate.go:117` | PARTIAL | `1a05b409` filters while reading (memory). Rest: kernel-side filter or batched flush (deferred-controlled) |
| D4-M2 | Medium | acctd sends the whole plan every second; netd never skips an unchanged one | `internal/enforce/enforce.go:114-125` | OPEN-DECISION | AD-53 |
| D4-M3 | Medium | Shared-allowance groups collide by chance and are refused | `internal/shape/shape.go:75-95` | OPEN | Deferred-controlled: durable group-class allocation, with D4-H1 |
| D4-L1 | Low | Class counters dumped four times a second per guest network | `internal/shape/shape.go:494`; `cmd/acctd/phase3_accounting.go:107` | FIXED | `4779ff77` netd half; `TestARenewalPassReadsEachBridgeOnce`. acctd half not reproducible (already one read per pass) |
| D4-L2 | Low | Structure check dumps the whole nftables table every 10 s | `internal/nftconverge/converge.go:152`; `cmd/netd/nft_watch.go:59` | OPEN | Deferred-controlled (batch B2) |
| D4-L3 | Low | Guest networks wider than /20 alias device classes | `internal/shape/shape.go:58` | ALREADY-FIXED | `85f1b1c4`: `internal/netcfg/validate.go` refuses subnets wider than /20 |
| D4-CC1 | CC | Every kernel operation is a separate process | `internal/shape/shape.go:45`; `internal/nft/nft.go:38,243` | OPEN | Deferred-controlled (batch B2) |
| D5-H1 | High | Sessions cannot be found by entitlement; expiry sweep looks them up that way every second | `iam_v2.sessions` (no `entitlement_id` index) | FIXED | Migration 0159 (`47ed5065`), `sessions(entitlement_id)`; verified on a full-schema test DB |
| D5-H2 | High | acctd runs every step in one serial loop | `cmd/acctd/main.go:182-206` | OPEN-DECISION | AD-47 |
| D5-M1 | Medium | Sweep rewrites rows every second whether or not anything changed | `p6_advance_data_crossing` and others (baseline) | OPEN | Deferred-controlled: enforcement-core SQL functions via a migration, with WAL proof |
| D5-M2 | Medium | Every 15 min scd re-reads and re-hashes every stored backup | `cmd/scd/backups.go:450-475,575` | FIXED | `097eb272`; `TestStoredBackupCheckRotates` |
| D5-L1 | Low | With no guests online, acctd still runs about a dozen statements a second | `internal/enforce/enforce.go:82` | PARTIAL | `b6182cad` catalog probes cached; `TestProbeCacheReusesWithinTTLAndNeverCachesFailures`. Rest: skip the idle sweep transaction |
| D5-L2 | Low | Daily log cutting copies each whole log, then truncates it in place | `cmd/scd/log_retention.go:5` | OPEN | Deferred-controlled: Caddy and Kea log rotation in `deploy/` |
| D6-H1 | High | Room sign-in closed hotel-wide while any PMS event waits; applier slow | `iam_v2.p3_feed_authorizes` (migration 0150) | PARTIAL | `69f08295` step 1 applier wake; `TestApplierWake_IdleLoopWakesOnNotify`. Rest: steps 2, 4 AD-37; step 3 deferred-controlled |
| D6-M1 | Medium | Roster reconciliation scans every event the interface has ever received | `iam_v2.pms_roster_of_generation`; `pms_roster_reconcile` | FIXED | Migration 0160 (`47ed5065`, up script completed in the fix commit after it): both roster functions add `admission_kind = 'RESYNC'`; verified on a full-schema DB, pmsd integration suite passes |
| D6-M2 | Medium | Each PMS record is its own transaction; the socket waits for it | `internal/pmsd/pg.go:256` (`insertInboxRow`) | PARTIAL | `e47ed27a` steps 1, 3 (500 records 1.92 s → 0.44 s); `TestIntegration_StageResyncEventsBatch`. Rest: step 2 socket reader (deferred-controlled) |
| D6-L1 | Low | Every resync keeps another full copy of every in-house guest's record | `stay_events` RESYNC rows | OPEN-DECISION | AD-38 |
| D7-H1 | High | Session, PMS stays and device pages look sessions up with no index, per row | `cmd/edged/resources_phase3.go:191-263` and others | PARTIAL | Migration 0159 (`47ed5065`) sessions indexes. Rest: `public.audit_log` index OPEN (needs a platform-role step); list follow-ups AD-56 |
| D7-H2 | High | Overview traffic charts sum raw per-second detail | `cmd/edged/resources_overview.go:690-770` | OPEN-DECISION | AD-48 |
| D7-M1 | Medium | PMS revisions page joins the whole audit log to itself | `cmd/edged/resources_phase3_interfaces.go:332-349` | FIXED | `f740c17f` filtered provenance query; `TestIntegration_API_PmsRevisionProvenanceSQL…`; device index in 0159 |
| D7-M2 | Medium | Client device list reports a failed read as "no devices" | `cmd/edged/client_devices_admin.go:192-206` | FIXED | `f740c17f`; code plus `TestIntegration_ClientDevices_*` (about 30 other loops: follow-up) |
| D7-M3 | Medium | Polled pages pay whole-table aggregates on every refresh | `hotel-admin/app/(app)/dashboard/page.tsx:79` | FIXED | `75605893`, `cd8c187c`; `TestIntegration_OverviewSessionBlocksAreSharedBetweenPolls` |
| D7-L1 | Low | Three handlers have no request timeout of their own | `resources_usage.go:101,448` | FIXED | `f740c17f`; code evidence (`dbCtx` on the three handlers) |
| D7-L2 | Low | Health loop trims its event table with a full anti-join every 10 s | `cmd/edged/health.go:144` | FIXED | `61f32006`; `TestIntegration_RecoveryEventTrimKeepsTheNewest2000` |
| D8-M1 | Medium | Raw timers keep polling in hidden tabs | hotel-admin components (bare `setInterval`) | FIXED | `75605893`; vitest `polling.test.tsx` |
| D8-M2 | Medium | Central appliance list re-renders the whole fleet on every refresh | `cloud-admin/app/(app)/appliances/page.tsx:62-78` | PARTIAL | `34692b1a` rows kept across refreshes; `poll-and-rows.test.tsx`. Rest: server paging, AD-55 |
| D8-L1 | Low | Polls can overlap; replaced requests never cancelled | both apps' `usePoll` | FIXED | `34692b1a` (cloud-admin), `75605893` (hotel-admin); vitest |
| D8-L2 | Low | Same data fetched twice (health badge and page) | `components/appliance-status.tsx:31`; `dashboard/page.tsx:53` | FIXED | `75605893`, `d463d376`; full vitest |
| D8-L3 | Low | Every console API call passes through Node | `hotel-admin/next.config.mjs:39-43` | OPEN-DECISION | AD-59 |
| D8-L4 | Low | hotel-admin package ships 27 MB it never uses | `hotel-admin-deploy.tgz` (`deploy/scripts/deploy-hotel-admin.sh`) | FIXED | `b3369d5d`; `next build` standalone 42 MB without sharp/typescript |
| D9-M1 | Medium | Hourly fetch-log prune reads the whole 30-day log | `control-plane/internal/api/terminal_delivery.go:390-393` | PARTIAL | `13249cd6` + Central migration 0058; `TestFetchLogPruneUsesAnIndex`. Rest: step 1, AD-54 |
| D9-M2 | Medium | Fleet list and Overview scan every licence once per appliance | `control-plane/internal/api/fleet.go:78-118` | PARTIAL | `13249cd6` step 1; `TestFleetLicenceMatchUsesTheIndex`. Rest: step 2 paging, AD-55 |
| D9-L1 | Low | Appliance detail page reads the whole audit log for a quiet appliance | `control-plane/internal/api/fleet.go:295-315` | FIXED | `13249cd6` + Central migration 0058; `TestApplianceEventsUnion` |
| D9-L2 | Low | Appliance calls have no jitter and all start at boot | `cmd/scd/main.go:1069-1070` and others | OPEN | Not taken by any workstream (scd appliance-call jitter) |
| D9-L3 | Low | Every signed call writes `appliances.last_seen_at` | `control-plane/internal/auth/appliance.go:119-122` | FIXED | `a49dccc2`; `TestRequireApplianceIssuerAndLastSeen` |
| D10-H1 | High | edged's health loop leaks a connection to scd and netd on every check (about 1.5 GB a day) | `cmd/edged/health_checks.go:17-34` | FIXED | `fe3e3a93`; `TestUnixGetLeavesNoConnectionOpen` |
| D10-M1 | Medium | A verified backup loads a second copy of the DB into the live Postgres | `cmd/scd/backups.go:121-255` | OPEN | Deferred-controlled: second DB container for backup verification (deployment) |
| D10-M2 | Medium | System logs kept by size, not by the hotel's log period | `docs/privacy/DPA_ANNEX_II_SECURITY_MEASURES.md` §9; journald, Caddy, Kea config | OPEN-DECISION | AD-48 |
| D10-M3 | Medium | No appliance service has a memory limit or kill order (except hotel-admin) | `deploy/systemd/stayconnect-*.service` | OPEN-DECISION | AD-50 |
| D10-M4 | Medium | Disk budget set by the accounting grain; backups nearly double it | `/opt/stayconnect/backups/db`; `deploy/scripts/backup-retention.conf` | OPEN-DECISION | AD-48 |
| D10-L1 | Low | Every service runs a 64 MiB Argon2 hash at start | `internal/passwordhash/passwordhash.go:80-85` | OPEN | Out of scope; not taken (`internal/passwordhash`, `internal/poststay`) |
| D10-L2 | Low | The appliance runs a Redis that nothing uses | `deploy/compose/infra.yml:42-51` | OPEN-DECISION | AD-63 |
| D10-L3 | Low | A manual backup gives up after 20 min, a limit that comes into reach as the DB grows | `cmd/scd/backup.go:97` | OPEN | Out of scope; not taken (`cmd/scd/backup.go`) |
| D11-L1 | Low | Guest data export reads the whole audit log every time | `data-plane/migrations/0147_*.up.sql:2134,2178-2184` | OPEN-DECISION | AD-49 |

Not rows: the decision ids (Phase 0 D1/D2, D10-D1, D10-D2) are decisions, not findings. They are in `AUDIT_DECISIONS.md` (AD-48 for D10-D1, AD-50 for Phase 0 D1/D2 = D10-D2). "Outside this audit" items in the perf FINAL_REPORT (server timeouts, cloud-admin Next 14.2.5 advisory and `unsafe-eval`, portal per-device limit = backend P8 M1) are not counted by the source; the Next.js advisory has no backend-audit row either and is noted under "Unresolved" below.

---

## C. Summary

### The five backend High findings

| # | Finding | Before | After | Evidence |
|---|---|---|---|---|
| 1 | P0-2: Go toolchain and libraries carry reachable vulnerabilities | OPEN-DECISION | PARTIAL | `1bacd214` updated the libraries within go 1.25: reachable advisories fell from 38 to 23 on the appliance and from 20 to 16 on Central (library-only: 24→8 and 10→5). Every remaining advisory needs Go 1.26: AD-60 |
| 2 | P1 H1: a missing CA certificate file makes Central mint a new root CA | OPEN | FIXED | `a7c35b66`: CA startup fails closed and `ctrlapi init-ca` creates the CA once. `ce245d10`: `central-install.sh --mode new` runs init-ca. Tests: `TestLoadCAMissingFileFailsAndCreatesNothing`, `TestPKIStartupFailsClosed` |
| 3 | P1 H2: an appliance is activated on facts anyone can know | OPEN-DECISION | OPEN-DECISION | Activation code: AD-1. M11 field validation (`d9d52a7e`) narrows the input but is not this fix |
| 4 | P2 H1: licence anti-replay and revocation records can be silently lost | OPEN | FIXED | `0020bf2c`: locked read-modify-write, atomic writes, `.prev` fallback, damaged files fail closed. Tests: `license/store_integrity_test.go` |
| 5 | P8 H1: a guest network's name can add firewall rules | FIXED | ALREADY-FIXED | `60e15df7` (PR #242), re-confirmed by the netd workstream |

### Product-Owner decisions

All open decisions are in **`AUDIT_DECISIONS.md`**: 68 items, `AD-1` … `AD-68`, grouped by theme with risk, options and a recommendation. That file replaces the two decision lists the earlier version of this section carried (FINAL_REPORT's 17 backend and 12 performance decisions). Every one of those is in the new list.

### Points to check before release (from the workstream records)

- **Central fresh install:** `a7c35b66` means `ctrlapi serve` never creates a CA. `ce245d10` updates `central-install.sh`. Restores and upgrades are unaffected.
- **Central migration 0058** builds three indexes on `audit_log` hypertable chunks. Writes block while they build, so apply it in a quiet period on a large Central.
- **Upgrade preconditions on the test appliance:**
  - P7 L8: the `pmsd.env` keys are hex of at least 32 bytes.
  - P7 L3: the published revision has no TLS pin with TLS off.
  - P5 L4: no saved portal design has a non-`/assets/` image path.
  - P1 M11: the appliance serial uses only `[A-Za-z0-9._-]`.
- **D7-H1's `public.audit_log` index** needs a step run under a platform role. A live site applies migrations as `iam_v2_owner`, which cannot index a public table.

### Unresolved or low-confidence mappings

- **`2a179df9`** (room sign-in on a network move) is still not mapped to any row. P8 M3 is now OPEN-DECISION (AD-23).
- **P8 L2:** the sysctl change (`b86a7446`) was reverted in `128ee02a`. On the branch, `deploy/sysctl/99-stayconnect.conf` is the same as master.
- **P1 L22:** NOT-REPRODUCIBLE from the repository configuration only. The production Central was not read.
- **P3 L2:** the coordinator listed it as "SQL ready", but the Central record has no SQL for it. It needs a `CREATE OR REPLACE FUNCTION` migration, batched with P3 M1.
- **Found during remediation, not an audit finding:**
  - pmsd: a malformed record inside an open DS..DE window is published without that record. Follow-up is CONTROLLED (PMS protocol).
  - The flaky `TestIntegrationPosting_InterfaceLaneIsSerializedAcrossDifferentPostings`.
  - About 30 edged loops without `rows.Err()`.
- **The Next.js advisory** (cloud-admin Next 14.2.5, `unsafe-eval` CSP) is still outside both audits and has no row.


## Security hardening list of 2026-07-11 (folded in)

> **SUPERSEDED (AD-68, option b).** The 2026-07-11 hardening list, its "Additional hardening (target architecture)" items and the pilot-cutover checklist are superseded by the verification checklist of the installation manual ([ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md](runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md), Part D and F.5). The list below is kept unchanged as a historical record; it is no longer a live checklist, and no item here is deleted or closed by this note.

`docs/SECURITY_HARDENING.md` (source: `git show 1d69508a847248ec63ceed01e06ec8f8f1661fb0:docs/SECURITY_HARDENING.md`) was a
pilot-era list. Its statuses below come from the system handbook §23 assessment and the current tree; nothing is
closed on a guess.

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| SH-1 | High | Gmail app password once committed in plaintext | `deploy/observability/alertmanager/alertmanager.yml` | PARTIAL | Not in the tree (`smtp_auth_password_file`). Rest: revocation in the Google account, AD-14 |
| SH-2 | High | ctrlapi `:8080` / console `:3000` open on the WAN | appliance nftables | ALREADY-FIXED | Handbook §23 (not re-tested in this mission) |
| SH-3 | Medium | Dev database credentials | appliance and Central | PARTIAL | Fresh installs generate passwords. Rest: older appliances' initdb password, AD-14 |
| SH-4 | High | IPv6 client bypass of the captive portal | netd ruleset | ALREADY-FIXED | Interim: IPv6 dropped on client networks (handbook §23); dual-stack capture is roadmap |
| SH-5 | Medium | Secure cookies off by default | ctrlapi / edged | ALREADY-FIXED | `cmd/edged/main.go:52` cookies Secure unless `EDGED_COOKIE_SECURE=false` |
| SH-6 | Medium | Grafana not behind Caddy | `deploy/observability` | FIXED | `924fe874`; Grafana admin password required (`docker compose config` fails unset) |
| SH-7 | Low | Appliance-JWT replay cache is in-process | ctrlapi | PARTIAL | `bea535c9` Central cache bounded; appliance twin not reproducible (no verifier). Rest: shared store, AD-11 |
| SH-8 | — | "Additional hardening (target architecture)" list and the pilot-cutover checklist | — | OPEN-DECISION | AD-68 |
