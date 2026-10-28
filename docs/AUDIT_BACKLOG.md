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

1. **PR #217 (D52–D63, T0237) fixed none of these findings.** It addressed an earlier, different audit: the "corrected system audit", whose findings are numbered N-xx and decided as D-A … D-K. Its remediation range `87b32100..d9e23dff` merged on 2026-10-17. Both audits here were written on 2026-10-24, against masters that already contained PR #217 (`0879e839` and `653363d7`). Their findings therefore describe the code as it stood after that remediation. Nothing in T0237, D52–D63 or the two AuditRemediation manifests can close one of them.
2. **Later fixes were looked for by searching.**
   - `git log --all -i --grep=audit` and `--grep=perf`, plus a search for finding ids (`Phase N Xn`, `PN Xn`), over everything after the audit commits (`d6e60abe..HEAD`, `e0cbe0ee..HEAD`).
   - Transitions T0238–T0264 and decisions D64–D78.
   - The post-audit history of every file the performance audit cites, and of the files behind the candidate backend fixes.
3. **Only two commits name a backend audit finding id.** Both are on master, through PR #242 (merge `fa24a913`, recorded by T0248 under D72):
   - `60e15df7`: "Network input validation … (audit Phase 8 H1, M9)".
   - `490321c1`: "netd pipeline integrity … Audit Phase 8 M6, M7, M10".

   Each diff was read against the finding's recommended fix:
   - **FIXED** means the commit names the finding and covers its recommended fix.
   - **PARTIAL** means the commit names the finding, or plainly covers part of it, and a named part is still missing. The missing part is stated in the row.
4. **No commit cites a performance-audit finding.** The files those findings cite are either unchanged since the audit, or changed only for unrelated features. Two were checked by hand and are still open: D7-M2 (`rows.Err()` is still unchecked in `listClientDevices`) and backend P7 M2 (RESYNC rows still have no `ON CONFLICT`).
5. **The status values.**
   - **OPEN-DECISION**: the source marks the finding DECISION, or lists it under "waiting for a decision", or puts its batch in the "after a decision" wave.
   - **OPEN**: everything else.
   - Where a related later change exists but does not name the finding, the row stays OPEN and the Evidence column says so.
   - The phase files have no per-finding status column. Their "status" is the evidence grade (Confirmed, Possible, Measured …), not a fix state. Every phase in both README tables reads "done" for the audit, not for the fix.

---

## A. Backend audit (`docs/audit/`)

### Phase 0: tooling baseline (`PHASE0_BASELINE.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P0-1 | Medium | No automated security or static-analysis gate (vet, staticcheck, govulncheck) | whole repo; `tools/go-checks.sh` (absent at HEAD) | OPEN | P0 B0a |
| P0-2 | High | Go toolchain out of support; reachable vulnerabilities: appliance 37, Central 20 (re-checked as Phase 10 X2) | both `go.mod` files (still `go 1.25.0`); `deploy/scripts/central-build.sh` | OPEN-DECISION | P0 B0b. Phase 10 X2 is the same finding, counted once here |
| P0-3 | Low | Appliance build toolchain: image pinned by tag, not digest; no `toolchain` line; Makefile builds without the production tag (= Phase 10 X1, lowered from Medium) | `scripts/build-appliance-binaries.sh:46` (`golang:1.25.12`); `Makefile` | OPEN | P0 B0c. One row for P0-3 and X1 |

### Phase 1: Central, ctrlapi (`PHASE1_CENTRAL.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P1 H1 | High | A missing CA certificate file makes Central mint a new root CA | `internal/pki/ca.go:50-87,106-142`; `cmd/ctrlapi/main.go:236-264` | OPEN | P1 B1. CONTROLLED (trust) |
| P1 H2 | High | An appliance is activated on facts anyone can know (serial or WAN MAC; no code shown on the box) | `internal/api/register.go:39-261,267-349` | OPEN-DECISION | Activation-code decision. D75/`d799ff5f` changed the CSR handling only, not this |
| P1 M1 | Medium | A rate-limit key can lose its expiry (INCR and EXPIRE are separate) | `internal/api/ratelimit.go:35-44` | OPEN | P1 B2 |
| P1 M2 | Medium | "Reissue certificate" with a waiting CSR leaves the old certificate usable for 24 h | `internal/api/appliance_ops.go:608-654`; `certificates.go:136`; `appliance_mtls.go:102` | OPEN | P1 B3. `d799ff5f` (D75) touched Reissue's CSR choice, not the 24 h overlap |
| P1 M3 | Medium | A failure to re-sign the trust registry is swallowed | `internal/api/assignment_keys.go:45-51`; `cmd/ctrlapi/main.go:323-335` | OPEN | P1 B4 |
| P1 M4 | Medium | Customer users see Central staff emails and internal reasons | `internal/api/fleet.go:295-315` | OPEN-DECISION | Hide them (recommended) |
| P1 M5 | Medium | Customer audit view shows platform staff ids and IP addresses | `internal/api/system.go:230-304` | OPEN-DECISION | Hide them (recommended) |
| P1 M6 | Medium | Anyone who knows the password can lock the account's code step for up to 24 h | `internal/auth/mfa.go:197-212` | OPEN-DECISION | Waiting list: per-address lock; needs a migration (CONTROLLED) |
| P1 M7 | Medium | WAN MAC rebind accepts any string and is not atomic | `internal/api/appliance_ops.go:534-598` | OPEN | P1 B5 |
| P1 M8 | Medium | Deleting an appliance runs irreversible steps outside one transaction | `internal/api/appliance_ops.go:682-771` | OPEN | P1 B5 (decide with P3 L10) |
| P1 M9 | Medium | No request-body size limits | `internal/http/auth_handlers.go:113,177`; `internal/api/common.go:131-139` | OPEN | P1 B2 |
| P1 M10 | Medium | Servers have no read, write or idle timeouts | `internal/http/mtls.go:41-51`; `cmd/ctrlapi/main.go:294-298` | OPEN | P1 B2 |
| P1 M11 | Medium | Unauthenticated registration has no cap and no field validation | `internal/api/register.go:39-261`; `offline_activation_api.go:331` | OPEN | P1 B7 |
| P1 M12 | Medium | Portal certificate orders can retry for ever after a crash; shutdown can lose a result | `internal/portaltls/worker.go:185-207`; `cmd/ctrlapi/main.go:232` | OPEN | P1 B12 |
| P1 M13 | Medium | One mutex serialises every ACME and DNS action for minutes | `internal/portaltls/worker.go:231,511`; `admin.go:141,181` | OPEN | P1 B12 |
| P1 M14 | Medium | Audit rows can be lost after a committed change | `internal/audit/audit.go:32-82` | OPEN | P1 B6 |
| P1 M15 | Medium | Administrators can take over peer administrators | `internal/api/users.go:111-123` | OPEN-DECISION | Only owners reset administrators (recommended) |
| P1 M16 | Medium | Offline request import: no step-up, silent conflicts, double packages | `internal/api/routes.go:68`; `offline_activation_api.go:67-255` | OPEN | P1 B8 |
| P1 M17 | Medium | Signing-key files are trusted without checks | `cmd/ctrlapi/main.go:144-158,323-335`; `cmd/ctrlapi/assignkey.go` | OPEN | P1 B4 |
| P1 L1 | Low | Last-platform-admin guard runs outside the transaction and fails open on a DB error | `api/users.go:378-387` | OPEN | P1 B10 |
| P1 L2 | Low | Step-up mark ignores a Redis error but answers ok | `api/reauth.go:18-24` | OPEN | P1 B10 |
| P1 L3 | Low | `IssueTx` ignores the previous-version read error | `api/assignment_api.go:108` | OPEN | P1 B4 |
| P1 L4 | Low | Licence revoke takes no per-appliance lock and keeps the portal hostname; suspend and resume race | `licensing/licensing.go:300-312`; `api/licenses.go:153-217` | OPEN | P1 B9 |
| P1 L5 | Low | `revocation_delivered_at` is set on encode, not on appliance confirmation | `api/licenses.go:258-276` | OPEN | P1 B9 (the rename is a migration) |
| P1 L6 | Low | `ReconcileTerminalTimeouts` changes state and inserts the alert outside one transaction | `api/terminal_delivery.go:239-275` | OPEN | P1 B6 |
| P1 L7 | Low | Trust, fleet and detail pages hide DB errors behind 200; `strictMTLSSelf` turns an outage into 403 | `api/system.go:103-212`; `fleet.go`; `terminal_delivery.go:352-378` | OPEN | P1 B11 |
| P1 L8 | Low | Uncapped pending CSRs; concurrent issuance can leave two active certificates; retiring appliance still rotates | `api/certificates.go:25-80,114-170` | OPEN | P1 B3. Related but not this finding: `d799ff5f` (D75) voids a replaced identity's pending CSRs and closes the other requests at issuance. No cap, lock or retirement check |
| P1 L9 | Low | `replace` has no row lock or state re-check | `api/appliance_ops.go:490-528` | OPEN | P1 B5 |
| P1 L10 | Low | `valid_days` overflows; licence terms unbounded | `api/licenses.go:69` | OPEN | P1 B9 |
| P1 L11 | Low | Appliance CSR accepts any key type and size | `pki/ca.go:234-246` | OPEN | P1 B3 |
| P1 L12 | Low | Failed-auth audit uses a hand-written JSON escaper; a non-uuid `iss` gives an unaudited 500 | `auth/appliance.go:56-79,137-175` | OPEN | P1 B6 |
| P1 L13 | Low | Every insert failure reported as "already exists"; email-existence oracle | `api/customers.go:96-99,145-149`; `users.go:187-193` | OPEN | P1 B10 |
| P1 L14 | Low | Site `code` not validated; site of an archived customer can be restored | `api/customers.go:107-150,647-668` | OPEN | P1 B10 |
| P1 L15 | Low | Public `/readyz` returns the version; backup health leaks host paths and raw errors | `http/router.go:174-193`; `api/backup_health.go` | OPEN | P1 B11 |
| P1 L16 | Low | Intermediate not checked against its key and the root; `relocateRootOffline` | `pki/ca.go:61-68`; `cmd/ctrlapi/main.go:345-361` | OPEN | P1 B1 (with H1) |
| P1 L17 | Low | ACME renewal window used without clamping | `portaltls/ari.go:111-115` | OPEN | P1 B12 |
| P1 L18 | Low | Daily order cap is count-then-insert; worker loops ignore scan errors and `rows.Err()` | `portaltls/orders.go:80-92`; `worker.go` | OPEN | P1 B12 |
| P1 L19 | Low | Long-lived goroutines have no panic recovery | `cmd/ctrlapi/main.go:177,232,256` | OPEN | P1 B13 |
| P1 L20 | Low | In-memory JWT replay cache unbounded, O(n) insert, lost on restart | `applianceauth/jwt.go:177-195` | OPEN | P1 B13 (mirror in data-plane) |
| P1 L21 | Low | Operational values hard-coded (rate limits, windows, cert lifetime) | various | OPEN-DECISION | Which become §0C settings |
| P1 L22 | Low | `clientip` trusts forwarding headers only from loopback; Docker bridge would share one bucket | `clientip/clientip.go` | OPEN | Read-only check on Central (on the source's waiting list; a verification, not a product decision) |
| P1 CC1 | CC | Dead code (`issueAssignment`, `pki.LoadOrCreate`, `selfSign`, …) | various | OPEN | P1 B14 |
| P1 CC2 | CC | Appliance verifier code in ctrlapi; `Challtestsrv` selectable in production | various | OPEN | P1 B14 |
| P1 CC3 | CC | Duplicates: UUID generators, atomic writers, key loaders, helpers | various | OPEN | P1 B14 |
| P1 CC4 | CC | `serve()` about 240 lines with a `goto`; `activate` about 160 lines | `cmd/ctrlapi/main.go` | OPEN | P1 B14 |
| P1 CC5 | CC | `id::text = $1` defeats the primary-key index | licensing, portal TLS, users | OPEN | P1 B14 |
| P1 CC6 | CC | `customerRows` loads the whole fleet; list limits truncate silently | `api/fleet.go` | OPEN | P1 B14 |
| P1 CC7 | CC | `WriteErr` duplicates `Fail` | `api` | OPEN | P1 B14 |
| P1 CC8 | CC | `metrics.statusRecorder` hides Flusher and Hijacker | `metrics` | OPEN | P1 B14 |
| P1 CC9 | CC | Assignment handler answers every refusal 204; misnamed variable; wrong CA comment | `api` | OPEN | P1 B14 |

### Phase 2: appliance security foundations (`PHASE2_APPLIANCE_FOUNDATIONS.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P2 H1 | High | The licence's anti-replay and revocation records can be silently lost | `license/store.go:38-190` | OPEN | P2 B1. CONTROLLED (licensing) |
| P2 M1 | Medium | One boot with a wrong future clock can lock the licence for years | `license/store.go:122-143,175-190` | OPEN | P2 B1 |
| P2 M2 | Medium | A licence issued while Central's clock was ahead blocks every later licence | `license/store.go:107-110,121` | OPEN | P2 B1 |
| P2 M3 | Medium | Identity checks skipped on empty fields; unbound licences accepted anywhere | `license/binding.go:30-47`; `internal/offline/offline.go`; `internal/assignment/assignment.go:116-135` | OPEN-DECISION | P2 B2: drop the legacy unbound allowance |
| P2 M4 | Medium | Revocations from Central trusted on the transport alone; failed save ignored | `internal/licstate/licstate.go:413-475` | OPEN-DECISION | P2 B2: signed revocations need a protocol decision |
| P2 M5 | Medium | Mutual-TLS key regenerated after any read error | `internal/appliancecert/cert.go:63-98` | OPEN | P2 B3 |
| P2 M6 | Medium | Identity key regenerated if `ed25519.key` goes missing | `internal/identity/identity.go:52-65,200-218,239-264` | OPEN | P2 B4 |
| P2 M7 | Medium | Calls to Central's HTTPS ingress have no timeout | `internal/appliancecert/cert.go:132,586-616,688,700` | OPEN | P2 B3 |
| P2 M8 | Medium | Mutual-TLS trust anchor is whatever Central's response says | `internal/appliancecert/cert.go:463-500,640-700` | OPEN-DECISION | P2 B3: pin the root at provisioning |
| P2 M9 | Medium | Certificate rotation switches first and verifies afterwards | `internal/appliancecert/cert.go:307-368,560-583` | OPEN | P2 B3 |
| P2 M10 | Medium | Writer-boundary startup check accepts triggers that never fire | `internal/writerguard/writerguard.go:203-280` | OPEN | P2 B8 |
| P2 M11 | Medium | Guest code requests can exceed limits; anyone can block a guest's code | `internal/otp/otp.go:75-112` | OPEN | P2 B9 |
| P2 M12 | Medium | Trust registry falls back to the previous registry (may trust a revoked key) | `internal/assignment/registry_store.go:63-77` | OPEN | P2 B5 |
| P2 M13 | Medium | Portal private key and certificate installed in two separate steps | `internal/portaltls/portaltls.go:436-466` | OPEN | P2 B7 |
| P2 M14 | Medium | One device can pause room sign-in for a whole guest network | `internal/signinattempt/network_pause.go:34-36` | OPEN-DECISION | Keep it, exempt recent successes, alert staff (recommended) |
| P2 M15 | Medium | Assignment, registry and certificate files are not crash-safe | `internal/assignment/store.go:68-85`; `registry_store.go:124-140`; `appliancecert/cert.go:492-500` | OPEN | P2 B3 + B5 |
| P2 L1 | Low | No persistent assignment anti-rollback | `assignment/resolve.go:148-150` | OPEN | P2 B5 |
| P2 L2 | Low | A short key file from a failed create blocks bootstrap | `localkeys/localkeys.go:52-95` | OPEN | P2 B10 |
| P2 L3 | Low | Key-length rules disagree across packages | `localkeys.go`; `sealbox.go:27-34`; `signinattempt/seal.go`; `otpkey.go`; `throttle.go` | OPEN | P2 B10 |
| P2 L4 | Low | `LoadExistingKey` follows a symlink before checking; no owner check | `localkeys/localkeys.go:97-112` | OPEN | P2 B10 |
| P2 L5 | Low | `LoadGenerationKeys` ignores the parse error (duplicate generations) | `localkeys/localkeys.go:140-170` | OPEN | P2 B10 |
| P2 L6 | Low | Hardware id falls back to a constant; weak DMI checks; commands without timeout | `hwid/hwid.go:61-118,138,183,245-260` | OPEN | P2 B4 |
| P2 L7 | Low | Password verify accepts trailing junk; no length cap; no concurrency bound | `passwordhash/passwordhash.go:53-77` | OPEN | P2 B11. See perf D2-H1 |
| P2 L8 | Low | `throttle` silently drops bad rules; ignores cleanup errors | `throttle/throttle.go:118-285` | OPEN | P2 B11 |
| P2 L9 | Low | `authctx` validates trimmed ids but binds untrimmed ones | `authctx/authctx.go:45-60` | OPEN | P2 B11 |
| P2 L10 | Low | Nil protection store or MAC-less device disables sign-in limits | `signinattempt/protection.go`; `network_pause.go` | OPEN | P2 B11 |
| P2 L11 | Low | Licence verifier panics on a wrong-length key; `GenerateVendorKey` overwrites | `license/verify.go:23-31`; `license/sign.go:31-40` | OPEN | P2 B1 |
| P2 L12 | Low | `Document.Validate` does not bound grace days or check the dates | `license/doc.go:149-171` | OPEN | P2 B2 |
| P2 L13 | Low | `Store.Load` turns every read error into "no licence" | `license/store.go:162-168` | OPEN | P2 B1 |
| P2 L14 | Low | `licstate` data races; rewrites 14 limit rows every minute | `licstate/licstate.go:136-417` | OPEN | P2 B2 |
| P2 L15 | Low | Offline packages checked against wall clock only; ledger row written before install | `offline/offline.go`; `cmd/scd/offline_import.go:42-60` | OPEN | P2 B6 |
| P2 L16 | Low | "Already assigned" decided from `TenantID != ""` | `cmd/scd/offline_first_activation.go` | OPEN | P2 B6 |
| P2 L17 | Low | Unsigned plain trust file used when the signed registry is missing | `assignment/resolve.go:89-111` | OPEN | P2 B5 |
| P2 L18 | Low | `NoteRefresh` rewrites the assignment record unlocked every poll | `assignment/store.go:109-129` | OPEN | P2 B5 |
| P2 L19 | Low | Portal TLS: stale key temp files; key unreadable without its group; `Inspect` skips host check | `portaltls/portaltls.go:107-218,468-492` | OPEN | P2 B7 |
| P2 L20 | Low | Central error and success bodies read without a limit | `identity/identity.go:135-147`; `appliancecert/cert.go:611-613` | OPEN | P2 B3 + B4 |
| P2 L21 | Low | `backupcrypt.ParseIdentity` ignores Scanner errors; check code an unsalted SHA-256 | `backupcrypt/backupcrypt.go:55-68,112-124` | OPEN | P2 B11 |
| P2 L22 | Low | OTP cleanup `DELETE` on the hot path; unused exported `Verify`; dead field | `otp/otp.go:55-66,171-185` | OPEN | P2 B9 |
| P2 L23 | Low | OTP lifetime, attempts, cooldown and hourly caps hard-coded | `otp/otp.go:20-26` | OPEN-DECISION | P2 B9: which become site settings |
| P2 L24 | Low | A 200 with a null envelope resets cloud staleness, even off mTLS | `licstate/licstate.go:457-465` | OPEN | P2 B2 (with M4) |
| P2 CC1 | CC | Dead code (`signedDo`, voucher HMAC bootstrap, `otp.Verify`, …) | various | OPEN | P2 B13 |
| P2 CC2 | CC | Duplicates: three atomic writers, two AEAD seals, a second Crockford alphabet, … | various | OPEN | P2 B13 |
| P2 CC3 | CC | assignment, offline, activation, totp and applianceauth copied line for line in control-plane | both modules | OPEN | P2 B12 |
| P2 CC4 | CC | Inconsistent key-id encodings (base64url and hex) | `activation` and the others | OPEN | P2 B12 |
| P2 CC5 | CC | Misleading comments (`Adopt` "verifies", FOR UPDATE claim, verify-before-switch) | `cmd/scd`; `authctx`; `appliancecert` | OPEN | P2 B13 |
| P2 CC6 | CC | `writerguard` repeats one catalog query 11 times; `applianceBound` only logs | `writerguard`; `licstate` | OPEN | P2 B8 / B13 |

### Phase 3: database and migrations (`PHASE3_DATABASE_AND_MIGRATIONS.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P3 M1 | Medium | Central's app connects as the superuser that owns every table | `deploy/env/ctrlapi.env.example:24`; `deploy/compose/central-infra.yml:44` | OPEN | P3 B4. CONTROLLED |
| P3 M2 | Medium | Baseline-built appliance: the runner would re-run a migration the baseline contains | `scripts/edge-migrate.sh:286-347,589`; `scripts/generate-production-baseline.sh` | OPEN-DECISION | P3 B2. Fix 4 (one-time adopt of 0001–0149) needs the decision |
| P3 M3 | Medium | Ledger row not written in the migration's transaction (both runners); no advisory lock | `deploy/scripts/central-migrate.sh:163-182`; `scripts/edge-migrate.sh` | OPEN | P3 B2 + B3 |
| P3 M4 | Medium | A restore replays erasure-ledger entries that nothing authenticates | `data-plane/migrations/0148_*.up.sql:72-79,408-470,607-613` | OPEN | P3 B5, done as one batch with P4 B1. Settled as P4 M1 (each counted in its own phase) |
| P3 M5 | Medium | OTP and key-bootstrap DB tests fail against the current schema; nothing runs them | `internal/otp/otp_keyed_test.go`; `otp_device_binding_test.go`; `internal/keybootstrap/keybootstrap_test.go:156` | OPEN | P3 B1 (do first) |
| P3 M6 | Medium | 130 foreign keys without an index; 39 cascade | catalog (e.g. `iam_v2.entitlements`, `stay_guests`, `auth_contexts`) | OPEN | P3 B7 |
| P3 L1 | Low | Seven guard trigger functions read catalogs unqualified with no pinned `search_path` | migrations 0091, 0122, 0124, 0127, 0140, 0143 | OPEN | P3 B6 |
| P3 L2 | Low | `central_forget_actor` matches case-sensitively and scans `audit_log` inside the delete | `control-plane/migrations/0055_*.up.sql:22,42-44` | OPEN | P3 B4 |
| P3 L3 | Low | Guest erasure rewrites the tenant's `audit_log` under the ledger lock | `data-plane/migrations/0147_*.up.sql:960-1005` | OPEN | P3 B5 |
| P3 L4 | Low | `central-migrate.sh` interpolates values into SQL; `--through` unvalidated | `deploy/scripts/central-migrate.sh:132,138,178,248-261` | OPEN | P3 B3 |
| P3 L5 | Low | Central 0040 and 0041 insert their own ledger row without a checksum | `control-plane/migrations/0040_*`, `0041_*` | OPEN | P3 B3 |
| P3 L6 | Low | Appliance ledger stores no checksum | `scripts/edge-migrate.sh:432-440,613` | OPEN | P3 B2 |
| P3 L7 | Low | Baseline's last covered migration in three unchecked places | baseline header; `lib-fullschema-testdb.sh:24`; `sitedb-dev.sh:20` | OPEN | P3 B2 |
| P3 L8 | Low | `ledger_confirm` trusts any hash and is granted to `svc_edged` | `0148_*.up.sql:136-146,603` | OPEN | P3 B5 |
| P3 L9 | Low | Index builds on large tables run inside a transaction | `0149_*.up.sql:13-16` | OPEN | P3 B7 |
| P3 L10 | Low | Deleting a Central appliance cascades away its cert and lifecycle history | Central 0022, 0023, 0044, 0050 | OPEN-DECISION | `RESTRICT` + soft delete (recommended); decide with P1 M8 |
| P3 L11 | Low | No RLS in `iam_v2`; isolation rests on every query filtering | 0 of 161 tables | OPEN | No batch; catalog test recommended |
| P3 L12 | Low | Nothing checks shipped binaries lack `-tags devlicense` | `internal/buildprofile/*.go` | OPEN | With P0-3 (P0 B0c) |
| P3 CC1 | CC | Runners and build scripts mostly narrative comments | `scripts/edge-migrate.sh` and others | OPEN | P3 B8 |
| P3 CC2 | CC | Container-readiness loop copied in three scripts | `lib-fullschema-testdb.sh` and two others | OPEN | P3 B8 |
| P3 CC3 | CC | Two runners, two ledger conventions | both runners | OPEN | P3 B2 |
| P3 CC4 | CC | `central-migrate.sh status` always prints "0 awaiting checksum backfill" | `deploy/scripts/central-migrate.sh` | OPEN | P3 B3 |

### Phase 4: guest sign-in, scd (`PHASE4_GUEST_SIGN_IN.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P4 M1 | Medium | A forged extension of the erasure ledger's external copy is replayed (settles P3 M4) | `internal/ledger/store.go:314-482`; `internal/ledger/assess.go`; `cmd/scd/ledger_service.go` | OPEN | P4 B1 (with P3 B5). Highest priority in phase |
| P4 M2 | Medium | Digits-only 6-character vouchers guessable by a MAC-rotating device | `cmd/scd/voucher_code_format.go:60-65`; `voucher_issue_iamv2.go:128`; `main.go:373` | OPEN-DECISION | P4 B2: minimum length 6 → 8 |
| P4 M3 | Medium | One failing entitlement stops every expiry on the site | `internal/enforce/enforce.go:252-433` | OPEN | P4 B3 |
| P4 L1 | Low | Durable guest throttle is off unless `SCD_DURABLE_THROTTLE` is set | `cmd/scd/authsec.go:106-117,177-179` | OPEN | P4 B2. Provisioning already sets it (pre-audit N-27, T0237); the default-off code is the finding |
| P4 L2 | Low | `phone.Normalize` accepts any Unicode digit and counts bytes | `internal/phone/phone.go:31-48` | OPEN | P4 B4 |
| P4 L3 | Low | `namenorm` has no Unicode NFC normalisation | `internal/namenorm/namenorm.go:15-22` | OPEN-DECISION | P4 B4: NFC (recommended); needs a migration |
| P4 L4 | Low | Social sign-in holds a DB connection and row lock during the provider call | `cmd/scd/social_handlers.go:183-262` | OPEN | P4 B5 |
| P4 L5 | Low | JWKS refetch while holding the cache mutex | `internal/social/jwt.go:83-108` | OPEN | P4 B5 |
| P4 L6 | Low | scd does not itself require a device on social/start, account/verify and account/reset | `social_handlers.go:111-117`; `self_service_handlers.go:526,647` | OPEN | P4 B4 |
| P4 L7 | Low | Most scd routes decode JSON with no size limit; nil writer passed to `MaxBytesReader` | `cmd/scd/main.go:351` and others | OPEN | P4 B4 |
| P4 L8 | Low | scd operator MFA keeps no failure count; reset with no proof | `cmd/scd/operator_mfa.go:197-215,298-310` | OPEN | P9 found edged holds; optional scd backstop counter remains |
| P4 L9 | Low | Device and appearance rows written before the licence and protection checks | `cmd/scd/phase3_auth.go:226-249,297` | OPEN | P4 B7 |
| P4 L10 | Low | Remembered-device limiter never prunes idle keys | `cmd/scd/open_access.go:39-60` | OPEN | P4 B7 |
| P4 L11 | Low | Compliance archives written unencrypted without a backup key holder | `cmd/scd/compliance_archive.go:96-117` | OPEN-DECISION | P4 B6: block the transition until a key holder exists |
| P4 L12 | Low | Root state files (CA bundle, removed marker, restore flag) not atomic | `offline_first_activation.go:273`; `removed_from_central.go:64-67`; `restore.go:150-154,677` | OPEN | P4 B6 |
| P4 L13 | Low | Double rename failure during restore still restarts services | `cmd/scd/restore.go:458-517` | OPEN | P4 B6 |
| P4 L14 | Low | Backups authenticated only by a digest marker in the same directory; check-then-reopen | `restore.go:107-122,243,368` | OPEN | P4 B6 |
| P4 L15 | Low | Mixed clock sources (Go `time.Now()` against DB `now()`) | `internal/staygrant/staygrant.go:201` | OPEN | P4 B7 |
| P4 CC1 | CC | Dead code (`cloudHTTPClient`, `shq`, `decodeGuestAuthBody`, …) | `cmd/scd` | OPEN | P4 B8 |
| P4 CC2 | CC | Two tests compare a value with itself | `internal/grace/grace_test.go:101`; `internal/stayengine/resolve_test.go:77` | OPEN | P4 B8 |
| P4 CC3 | CC | Stale security comment about portald sharing edged's uid | `cmd/scd/main.go:974` | OPEN | P4 B8 |
| P4 CC4 | CC | Comment volume; root SQL built with Sprintf | `restore_financial.go:175`; `ledger_restore.go:188` | OPEN | P4 B8 |

### Phase 5: Client Portal, portald (`PHASE5_CLIENT_PORTAL.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P5 M1 | Medium | One guest device can exhaust the portal for every guest (no timeouts, body limits or connection limits) | `cmd/portald/main.go:622-625`; `email_handlers.go:67,134`; `deploy/nftables/stayconnect.nft:68` | OPEN | P5 B1. Step 3 goes with P8 M1 (P8 B6) |
| P5 M2 | Low | Commerce session cookie is a bearer capability, in clear on HTTP (lowered from Medium by Phase 6) | `cmd/portald/commerce.go:51-83`; `iamv2_commerce_session.go:253-283`; `cookies.go:13` | OPEN | P5 B2. Counted as Low |
| P5 M3 | Medium | A broken notification provider looks healthy while no code is delivered | `internal/notifyloader/loader.go:132-172,228-269,328-355` | OPEN | P5 B3 |
| P5 L1 | Low | MAC lookup IPv4-only, any interface, ignores entry state, reads the whole file | `cmd/portald/arp.go:12-33`; `arp_resolve.go:42-67` | OPEN | P5 B4. See perf D3-L1 |
| P5 L2 | Low | Flood damper misses routes that check secrets or send messages | `cmd/portald/main.go:524-596` | OPEN | P5 B5 |
| P5 L3 | Low | Expired commerce sessions never removed | `cmd/portald/commerce.go:51-71` | OPEN | P5 B2 |
| P5 L4 | Low | Design image fields accept any appliance path; CSS `url()` any https host | `internal/portaldesign/urls.go:91-107`; `design.go`; `css.go` | OPEN | P5 B6 |
| P5 L5 | Low | Unsealable provider secret silently falls back to the plaintext column | `internal/notifyloader/loader.go:104-110` | OPEN | P5 B3 |
| P5 L6 | Low | Cancelled-send failure not recorded; byte truncation breaks UTF-8 | `internal/notifyloader/loader.go:333,357-362` | OPEN | P5 B3 |
| P5 L7 | Low | Admin SMTP host and port unrestricted; server reply shown back | `internal/mail/smtp.go:52-63,114-143` | OPEN | P5 B6 |
| P5 L8 | Low | Email format: constant boundary, no transfer encoding or Message-ID, raw From name | `internal/mail/smtp.go:215-240` | OPEN | P5 B6 |
| P5 L9 | Low | Twilio and SendGrid do not validate the destination | `internal/sms/twilio.go`; `internal/mail/sendgrid.go` | OPEN | P5 B6 |
| P5 L10 | Low | Loose design value checks (unbalanced font quotes, 5- or 7-digit hex) | `internal/portaldesign/design.go:75-77,272` | OPEN | P5 B6 |
| P5 L11 | Low | Provider rows unordered; no one-enabled-per-channel rule; validator mismatch | `internal/notifyloader/loader.go:81-98,214-226` | OPEN | P5 B3 (the index is a CONTROLLED migration) |
| P5 CC1 | CC | Dead code (`humanDuration`, `black`, `portal_base`, …) | `cmd/portald`; `notifyloader` | OPEN | P5 B7 |
| P5 CC2 | CC | Two cookie setters; codegen ModeComplex equals ModeAlnum | `account_handlers.go:57-69`; `codegen` | OPEN | P5 B7 |
| P5 CC3 | CC | Stale security comments (shared service user; Caddy TLS) | `cmd/portald/main.go:132`; `social_handlers.go:150` | OPEN | P5 B7 |
| P5 CC4 | CC | 2,272-line `templates.go`; invisible format characters; three wrappers | `cmd/portald/templates.go`; `languages.go` | OPEN | P5 B7 |

### Phase 6: payments, posting, accounting (`PHASE6_PAYMENTS_AND_POSTING.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P6 M1 | Medium | Room-charge outbox retries with no backoff; one refused charge blocks the PMS lane | `cmd/scd/posting_worker.go:68-110`; `internal/posting/repo.go:61-82`; `engine.go:309-361` | OPEN | P6 B1. CONTROLLED (financial, schema) |
| P6 M2 | Medium | acctd's plan counter is not durable; a lost counter freezes enforcement | `cmd/acctd/phase3_envelope.go:38-78`; `internal/shapeplan/plan.go:157-164` | OPEN | P6 B2 with P8 B5 (netd half is P8 M8) |
| P6 M3 | Medium | Provider reversal errors swallowed; a refused chargeback never recorded | `internal/payment/checkout.go:473-517` | OPEN | P6 B3. CONTROLLED |
| P6 M4 | — | Shared-allowance re-rate (merged) | see P8 M4 | — | Counted once, as P8 M4 (FINAL_REPORT) |
| P6 L1 | Low | A capture after a checkout was marked failed is never noticed | `internal/payment/checkout.go:313-400`; `paymob/paymob.go:538-547` | OPEN | P6 B3 |
| P6 L2 | Low | Dormant execute and notification path grants without an amount check; test double in the prod build | `internal/payment/engine.go:300-354`; `provider.go:15-19`; `testdouble.go:74` | OPEN | P6 B5 |
| P6 L3 | Low | Stripe ambiguous-create resolution fails on busy accounts (50×100 cap) | `internal/payment/stripe/stripe.go:41-44` | OPEN | P6 B5 |
| P6 L4 | Low | Paymob ownership unchecked when the inquiry omits order fields | `internal/payment/paymob/paymob.go:564-569` | OPEN | P6 B5 |
| P6 L5 | Low | Checkout start edge cases (redirect save failure; idempotency 500) | `internal/payment/checkout.go:216-221`; `engine.go` | OPEN | P6 B5 |
| P6 L6 | Low | Check-out boundary inputs unvalidated; old PMS check-out time trusted | `internal/checkout/checkout.go:148-157,342-349` | OPEN | P6 B6. CONTROLLED (entitlement) |
| P6 L7 | Low | One slow PMS stalls the other posting lanes | `cmd/scd/posting_worker.go:94-107` | OPEN | P6 B1 |
| P6 L8 | Low | A charge accepted during shutdown becomes UNKNOWN | `internal/posting/engine.go:197-204,327` | OPEN | P6 B1 |
| P6 L9 | Low | Attempt and requeue updates lack state guards | `internal/posting/repo.go:612-616`; `engine.go:417-424` | OPEN | P6 B1 |
| P6 L10 | Low | Post-stay PIN edge cases (raw error, lockout by id, expired reset) | `internal/poststay/poststay.go:248-354` | OPEN | P6 B7 |
| P6 L11 | Low | Transfer reason not stored; no tie-break; raw DB errors | `internal/transfer/transfer.go:142,157,233-244` | OPEN | P6 B7 (P9 confirmed edged step-up holds) |
| P6 L12 | Low | Shaping input trusted (zero rate = 1 Gbit, IFB name truncation, group-minor collisions) | `internal/shape/shape.go:78-110,181-189,311-318`; `internal/shapeplan/plan.go:135-166` | OPEN | P6 B4 |
| P6 L13 | Low | acctd watcher baseline race; missing class read as 0 bytes | `cmd/acctd/main.go:111-123`; `phase3_accounting.go:128-140` | OPEN | P6 B2 |
| P6 L14 | Low | Default DB URLs carry a password and disable TLS (acctd, scd, edged, netd) | `cmd/{acctd,scd,edged,netd}/main.go` | OPEN | P6 B8. Extended to ctrlapi as Phase 10 X3 |
| P6 L15 | Low | Card status limiter wiped when full | `cmd/scd/card_checkout.go:270-282` | OPEN | P6 B5 |
| P6 CC1 | CC | Dead code (acctd revoke path, dormant payment execute, `FormatCT`, `cidClass`) | various | OPEN | P6 B9 |
| P6 CC2 | CC | Duplicated provider transport helpers and hash helpers | `payment/stripe`, `paymob`; `postinghandoff` | OPEN | P6 B9 |
| P6 CC3 | CC | Ignored errors; `iam_v2.stays` updated by id alone; `AssignmentGe` typo | `internal/checkout`; `phase3_envelope.go` | OPEN | P6 B9 |
| P6 CC4 | CC | No tests for `shapeplan` and `shapeproducer`; phase5-tag suites never run | `internal/shapeplan`; `shapeproducer` | OPEN | P6 B9 |

### Phase 7: PMS, pmsd (`PHASE7_PMS.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P7 M1 | Medium | A FIAS frame has no length limit | `internal/pms/protel_fias.go:373-391`; `internal/pmsd/adapter_fias.go:203` | OPEN | P7 B1. CONTROLLED (PMS) |
| P7 M2 | Medium | A repeated record inside one resync can make every resync fail | `internal/pmsd/pg.go` `insertInboxRow` (only LIVE has `ON CONFLICT`) | OPEN | P7 B2. Re-checked at HEAD (pg.go:289): still LIVE-only |
| P7 L1 | Low | Live-event barrier does not close on a gap | `internal/pmsd/worker.go:356-414`; `adapter_fias.go:228-247` | OPEN | P7 B2 |
| P7 L2 | Low | FIAS TCP dial has no timeout | `internal/pmsd/adapter_fias.go:106`; `cmd/pmsd/main.go:90-92` | OPEN | P7 B1 |
| P7 L3 | Low | A certificate pin without TLS is silently ignored (pmsd side; edged holds) | `internal/pmsd/pg.go:117-118`; `pmsd.go` | OPEN | P7 B3 |
| P7 L4 | Low | REST clients follow redirects with credentials; private hosts allowed | `internal/pmsrest/client.go`; `mews.go`; `internal/pmsprovider/config.go:120-134` | OPEN | P7 B4 |
| P7 L5 | Low | REST paging: stuck cursor up to 10,000 requests; partial list at the cap | `internal/pmsrest/mews.go:102-135`; `apaleo.go`; `operacloud.go` | OPEN | P7 B4 |
| P7 L6 | Low | REST records with blank fields pass through | `internal/pmsrest/mews.go:124-138`; `client.go` | OPEN | P7 B4 |
| P7 L7 | Low | Legacy provider layer still loaded and served (`/cache` returns guest rows) | `internal/{pms,pmsguard,pmsloader}`; `cmd/scd/pms_admin.go` | OPEN | P7 B6 |
| P7 L8 | Low | pmsd key settings fail late, unnamed; no minimum length | `cmd/pmsd/main.go:80-89` | OPEN | P7 B3 |
| P7 L9 | Low | A FIAS drop mid-roster is not recorded as interrupted | `internal/pmsd/worker.go:337`; `adapter_fias.go` | OPEN | P7 B2 |
| P7 L10 | Low | Reconnect jitter can be near zero | `internal/pmsd/supervisor.go:134-150` | OPEN | P7 B1 |
| P7 L11 | Low | Lock housekeeping has no timeout; default pool size shared | `internal/pmsd/pg.go:388-415` | OPEN | P7 B5 |
| P7 L12 | Low | Applier errors logged in full (may carry key values) | `internal/pmsd/applier.go:63` | OPEN | P7 B5 |
| P7 L13 | Low | Cached resolution replayed by request id alone | `internal/pmsresolve/fanout.go:187-203` | OPEN | P7 B5 |
| P7 L14 | Low | Relay in-memory guards bounded loosely | `internal/pmsd/financial_relay.go:116-141,196-217` | OPEN | P7 B5 |
| P7 CC1 | CC | Dead code (`BoundedQueue`, Opera `Changes` stub, the legacy layer) | `internal/pmsd`; `internal/pms` | OPEN | P7 B6 |
| P7 CC2 | CC | Tests that cannot fail; no tests for `pmsguard` or `cmd/pmsd` | `internal/pmsd/secret_test.go:49`; `pmsresolve/resolve_test.go:59` | OPEN | P7 B7 |
| P7 CC3 | CC | Stale comments (root-only socket; barrier) | `financial_relay.go`; `adapter_fias.go` | OPEN | P7 B7 |
| P7 CC4 | CC | Long `Serve` state machine; duplicate REST providers; unchecked close errors | `adapter_fias.go`; `internal/pms`; `fias_tls.go:51,62` | OPEN | P7 B7 |

### Phase 8: networking, netd (`PHASE8_NETWORKING.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P8 H1 | High | A network name can inject rules into the firewall | `internal/netcfg/render_nft.go:211-214`; `validate.go:37-39`; `cmd/edged/resources_network*.go` | FIXED | `60e15df7` "(audit Phase 8 H1, M9)": closed-charset validation in netcfg and edged; renderer reduces the name and bridge to a safe alphabet; fuzz test. On master via PR #242 (`fa24a913`, T0248, D72) |
| P8 M1 | Medium | Portal has no per-device connection or rate limit at the firewall | `internal/netcfg/render_nft.go:154-158,164-167` | OPEN | P8 B6 (with P5 B1). New §0C settings |
| P8 M2 | Medium | Guests can reach hotel networks in 10.0.0.0/8 and other private ranges | `internal/netcfg/model.go:111-118`; `render_nft.go:206-208` | OPEN-DECISION | P8 B6: deny all private ranges with an allow list (recommended) |
| P8 M3 | Medium | Authorization tied to the IP address, not the device | `render_nft.go:84,89,216,218`; `cmd/netd/phase3_address_owner.go` | OPEN | P8 B7. Not closed by `2a179df9` (scd session re-address on a network move; does not name M3 or bind MAC in nft) |
| P8 M4 | Medium | Shared-allowance re-rate gives every device the whole allowance (= P6 M4) | `internal/shape/shape.go:306-326,445-451`; `cmd/netd/phase3_provision.go:148-160` | OPEN | P8 B4 + P6 B4. One row for both (FINAL_REPORT merge) |
| P8 M5 | Medium | Open connections outlive the end of a guest's authorization | `render_nft.go:196`; `cmd/netd/phase3_gate.go:404-449` | OPEN | P8 B6 |
| P8 M6 | Medium | A network change that dies mid-apply blocks every later change | `cmd/netd/store.go:367-380`; `main.go:461-475` | FIXED | `490321c1` names M6: a revision left "applying" is rolled back at netd start. Its M10 one-operation lock covers the in-process path. PR #242 / T0248 |
| P8 M7 | Medium | Rollbacks report success when they failed | `cmd/netd/apply_ops.go:564-571`; `sysnet.go:331-335,470-490` | PARTIAL | `490321c1` names M7: guest and system rollbacks now record failures, plus a durable marker for guest networks. Still open: a failed system-network revert still clears its pending marker (`clearPendingFiles`), so the watchdog does not retry (fix step 2) |
| P8 M8 | Medium | Accepted-plan generation not saved durably (netd half of P6 M2) | `cmd/netd/phase3_control.go:80-95`; `phase3_shaping.go:186-188` | OPEN | P8 B5 |
| P8 M9 | Medium | Other config values written without validation or escaping | `render_nft.go:141`; `render_netplan.go:46`; `apply_ops.go:200,285`; `internal/sysnet/sysnet.go:60-77,155-170,295` | PARTIAL | `60e15df7` names M9: bridge, parent interface, domain and hostnames validated in netcfg. Still open: `internal/sysnet` WAN and LAN interface names (unchanged since the audit) and YAML scalar quoting |
| P8 M10 | Medium | A rejected apply can still reach the live firewall | `cmd/netd/apply.go:224-252` | FIXED | `490321c1` names M10: one operation lock; every error path restores the intent; a second apply gets 409. PR #242 / T0248 |
| P8 L1 | Low | Client-isolation switch has no effect | `render_nft.go:188-195,209`; `model.go:57` | OPEN-DECISION | P8 B8: relabel and/or enforce on multi-port bridges |
| P8 L2 | Low | IPv6 leftovers (forwarding=1; link-local on guest bridges) | `deploy/sysctl/99-stayconnect.conf:3`; netplan render | OPEN | P8 B6 |
| P8 L3 | Low | One failed class-state save denies every new guest until restart | `phase3_shaping.go:418-426`; `phase3_journal.go:329` | OPEN | P8 B5 |
| P8 L4 | Low | Journal bookkeeping (strike history lost; invalid record persisted) | `phase3_journal.go:350-356`; `phase3_provision.go:145` | OPEN | P8 B5 |
| P8 L5 | Low | A plan without a MAC skips the ownership check | `phase3_address_owner.go:59`; `phase3_provision.go:62` | OPEN | P8 B7 |
| P8 L6 | Low | A class clash lets a third session through | `phase3_shaping.go:258-269` | OPEN | P8 B4 |
| P8 L7 | Low | Stray sweep matches by class number, not IP | `phase3_shaping.go:340-345` | OPEN | P8 B4 |
| P8 L8 | Low | Locking details (`endFlows` without defer; unlocked `mode.Active`) | `flows.go:486-506`; `phase3_shaping.go:620-626`; `phase3_scope_watch.go:45` | OPEN | P8 B5 |
| P8 L9 | Low | Plan pass runs under the request context while holding the lock | `phase3_shaping.go:153-156`; `main.go:264` | OPEN | P8 B5 |
| P8 L10 | Low | conntrack dump held in memory; DUMP_INTR unchecked | `internal/conntrack/netlink_linux.go:41-98` | OPEN | P8 B8. See perf D4-M1 |
| P8 L11 | Low | `ifaceAddrs`, `ifaceHasIP` and `ifaceHasAnyIP` run commands without a timeout | `cmd/netd/apply_ops.go` (still `exec.Command` at HEAD) | OPEN | P8 B8 |
| P8 L12 | Low | Config files not written atomically (netplan, Unbound, Kea, system network) | `apply_ops.go`; `kea_bootstrap.go:86`; `sysnet.go:283,289` | PARTIAL | `490321c1`: netd now writes the confirmed Kea config atomically (`kea_persist.go`). netplan, Unbound, the Kea bootstrap and system-network files still use in-place `os.WriteFile` |
| P8 L13 | Low | nft watch failures only logged; no backoff or event | `cmd/netd/nft_watch.go` | OPEN | P8 B2. `490321c1` added only the operation lock there |
| P8 L14 | Low | Smaller system-network checks (WAN verify path, DHCP mode, address classes, confirm lock) | `cmd/netd/sysnet.go:326-364,500-515`; `internal/sysnet/sysnet.go:95` | OPEN | P8 B3. Unit hardening is a separate decision |
| P8 CC1 | CC | Dead code; no-op `sanitize()` | `cmd/netd`; `sysnet.go:822-824` | OPEN | P8 B8 |
| P8 CC2 | CC | Kernel gate loads a hand-written ruleset, not `RenderNftables` | `scripts/ci/kernel-netns-suite.sh` | OPEN | P8 B6 |
| P8 CC3 | CC | Ignored errors and inconsistent file-write patterns | `apply.go:214` and others | OPEN | P8 B8 |
| P8 CC4 | CC | Comment volume | `cmd/netd` | OPEN | P8 B8 |

### Phase 9: Admin Console API, edged (`PHASE9_ADMIN_CONSOLE_API.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| P9 M1 | Medium | Request bodies have no size limit; server sets only `ReadHeaderTimeout` | `cmd/edged/common.go:40-48`; `resources_sysnetwork.go`; `resources_cert.go:44`; `main.go:468` | OPEN | P9 B1 |
| P9 M2 | Medium | Email, SMS and social sign-in settings change without the password step-up | `cmd/edged/resources_providers.go:274-480,759-1016` | OPEN | P9 B2. CONTROLLED |
| P9 M3 | Medium | A restore does not check the backup's schema against the installed software | `cmd/scd/restore.go:617-645`; `cmd/edged/resources_backups.go:395-445` | OPEN | P9 B5. CONTROLLED |
| P9 M4 | Medium | Guest-data read audit misses routes; no test catches a new one | `cmd/edged/guest_data_reads.go:30-52,192-196` | OPEN-DECISION | P9 B4. List browsing needs the decision; missing routes and test do not |
| P9 M5 | Medium | edged stores network names, domains and hostnames unchecked (edged side of P8 H1, M9) | `cmd/edged/resources_network.go:233-259,751,778`; `resources_network_replace.go:76-92` | PARTIAL | `60e15df7`: edged create, batch, update and reservation endpoints refuse hostile names, domains and hostnames, and netcfg re-checks. Still open: no edged-side check that a reserved IP is inside the subnet (netd checks it only at apply) |
| P9 L1 | Low | Raw DB and transport errors returned to operators | many `resources_*.go` | OPEN | P9 B6 |
| P9 L2 | Low | Secrets in responses lack `Cache-Control: no-store`; desk password reset has no reason | `guest_accounts_iamv2_lifecycle.go:482-570`; `resources_vouchers.go:469-525`; `resources_guest_accounts.go:105` | OPEN-DECISION | P9 B6. The reason requirement needs the decision; no-store does not |
| P9 L3 | Low | `verifyPassword` trusts the hash's own argon2 parameters | `cmd/edged/auth.go:39-60` | OPEN | P9 B7 |
| P9 L4 | Low | Anyone reaching the console can lock a named operator out | `operator_signin.go:235-270`; `operator_signin_settings.go:40-49` | OPEN-DECISION | P9 B7: keep it and document (recommended) |
| P9 L5 | Low | `middleware.RealIP` lets a client set `RemoteAddr` (True-Client-IP) | `main.go:294`; `common.go:80-95` | OPEN | P9 B7 |
| P9 L6 | Low | System-network confirm has no step-up and starts an unbounded detached task | `resources_sysnetwork.go:55-66` | OPEN | P9 B7 |
| P9 L7 | Low | Backup download serves any file in the folder; follows symlinks | `resources_backups.go:~268,332`; `backup_download.go` | OPEN | P9 B5 |
| P9 L8 | Low | Listing backups re-hashes every dump on each call | `resources_backups.go:~190` | OPEN | P9 B5. See perf D5-M2 |
| P9 L9 | Low | Portal asset upload written in place; single Read; 8 MB per request | `resources_portal_assets.go:~110-121` | OPEN | P9 B7 |
| P9 L10 | Low | One-time-code reveal skips `validReason`; audit after the reveal | `resources_code_requests.go:102-133` | OPEN | P9 B6 |
| P9 L11 | Low | Old `?q=` search fallback puts guest search text in URLs | `resources_usage.go:98,622`; `guest_data_rights.go:~58` | OPEN | P9 B4 |
| P9 L12 | Low | Paging overflow; unbounded date ranges; unchecked uuid casts; `?fresh=1` probe | `resources_helpers.go:35` and others | OPEN | P9 B6 |
| P9 L13 | Low | Free-text bounds missing in the data-rights register and client groups | `guest_data_register.go:49-55`; `resources_client_groups.go:249,322,388` | OPEN | P9 B6 |
| P9 L14 | Low | Shortening a retention period shows no impact; old-value read ignores errors | `data_retention_admin.go:131-200` | OPEN | P9 B6 |
| P9 L15 | Low | Failed secret store after update leaves the provider half changed, unaudited | `resources_providers.go:456-466` | OPEN | P9 B2 |
| P9 L16 | Low | Guest device self-service switch changes without step-up | `resources_phase6_setting.go:97` | OPEN | P9 B2 |
| P9 CC1 | CC | Dead code; two SA4000 self-comparing tests | `cmd/edged` | OPEN | P9 B7 |
| P9 CC2 | CC | Four step-up helpers, per-file error writers, duplicate hashing | `cmd/edged` | OPEN | P9 B7 |
| P9 CC3 | CC | Large mixed-resource files (over 1,000 lines) | `resources_overview.go` and others | OPEN | P9 B7 |
| P9 CC4 | CC | Comment volume; integration suites cannot be re-run on one DB | `cmd/edged` tests | OPEN | P9 B7 |

### Phase 10: cross-cutting (`PHASE10_CROSS_CUTTING.md`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| X1 | Low | (= P0-3, corrected and lowered) | see P0-3 | — | Merged into P0-3 |
| X2 | High | (= P0-2, re-checked on `0879e839`) | see P0-2 | — | Counted once in Phase 0 by the source |
| X3 | Low | Five services, ctrlapi included, fall back to a DB URL with a published password and TLS off | `control-plane/internal/config/config.go:29`; `cmd/{scd,acctd,edged,netd}/main.go` | OPEN | P6 B8 extended to ctrlapi. Overlaps P6 L14 |

Not findings in the source, so no rows: X4 (no real hard-coded credentials), X5 (race detector, no races), and the ten cross-phase patterns P1–P10. The patterns group the findings above; they add none.

---

## B. Performance audit (`docs/performance-audit/`)

| ID | Sev | Title | Where | Status | Evidence or batch |
|---|---|---|---|---|---|
| B0-1 | Medium | Nothing to measure performance with (no benchmarks, per-route timings or profiler) | whole repository | OPEN | P0 B0 (Wave 0) |
| B0-2 | CC | Measurement code scattered; each service builds its own `http.Server` | `data-plane/internal/metrics`; `internal/reqlog` | OPEN | P0 B0 |
| D1-H1 | High | Every controlled operation scans an hour of bookkeeping rows | `iam_v2.begin_controlled_operation` (`controlled_operation_scope` janitor) | OPEN | P1 B1 (index migration) |
| D1-H2 | High | acctd records every online device every second, one transaction each | `cmd/acctd/main.go:181` | OPEN-DECISION | P1 B2 with P5 B2. Tick depends on decision D1-H2 |
| D1-H3 | High | Accounting detail grows faster than anything removes it (disk fills in weeks) | `iam_v2.accounting_records` | OPEN-DECISION | P1 B3: grain decision D1-H3 with D10-D1 |
| D1-M1 | Medium | Finding live sessions reads every session ever recorded | `cmd/acctd/phase3_accounting.go:44-48` | OPEN | P1 B1 (index migration) |
| D1-M2 | Medium | Postgres tuned as if it owned the whole appliance | `deploy/compose/infra.yml:12-24` | OPEN | P1 B4 (deployment configuration) |
| D1-M3 | Medium | No service chooses its pool size; every pool has 4 connections | every appliance `pgxpool.New` | OPEN | P1 B5 |
| D1-L1 | Low | Per-role connection limits add up to more than Postgres allows | `deploy/gatep/gatep-roles.sql:31-38` | OPEN | P1 B5 |
| D1-L2 | Low | No DB guard rails or slow-query evidence (timeouts 0) | Postgres settings | OPEN | P1 B4 |
| D1-L3 | Low | Rows updated every second miss HOT updates (fillfactor 100) | `iam_v2.sessions`, `entitlements`, `accounting_checkpoints` | OPEN | P1 B6. CONTROLLED |
| D1-L4 | Low | The accounting hypertable is the one nothing writes any more | `public.accounting_records` | OPEN-DECISION | P1 B3 (Wave 3, after D1-H3) |
| D1-L5 | Low | Several per-event tables are never trimmed | migrations / Go (no DELETE) | OPEN-DECISION | P1 B7: what anonymised rows are kept for |
| D1-L6 | Low | edged builds a payment engine (could open a pool) on every financial request | `cmd/edged/resources_phase4_finops.go:422` | OPEN | P1 B5 |
| D1-L7 | Low | Accounting purge's hourly cap fixed in code | `cmd/acctd/phase3.go:121-123` | OPEN-DECISION | P1 B3 (Wave 3, after D1-H3) |
| D2-H1 | High | Password hashing has no limit on how many run at once (about 30 concurrent = 4 GB) | `internal/passwordhash/passwordhash.go:17-22` | OPEN | P2 B1. CONTROLLED. The later Argon2 setting decision blocks nothing. See backend P2 L7 |
| D2-M1 | Medium | Slow work runs inside DB transactions and holds scd's connections | `internal/iamv2/adapters.go:18-46` | OPEN | P2 B2 |
| D2-M2 | Medium | Every session activation counts every session ever, under an appliance-wide lock | `cmd/scd/iamv2_session_ops.go:111-128` | OPEN | P2 B3 (index migration) |
| D2-M3 | Medium | Appliance-wide sign-in ceiling fixed in code; OTP charges it twice | `cmd/scd/authsec.go:21-28` | OPEN-DECISION | P2 B4: ceiling default (decision D2-M3) |
| D2-M4 | Medium | Room sign-ins at one PMS run one at a time (runtime-row lock) | `iam_v2.issue_or_return_pms_context` | OPEN | P2 B5. Waits for the Phase 11 S3 measurement, not a decision |
| D2-L1 | Low | Durable throttle costs 6–9 statements per attempt and shares one row | `internal/throttle/throttle.go:172-245` | OPEN-DECISION | P2 B4 (with D2-M3) |
| D2-L2 | Low | Settings read from the DB on every sign-in | `guest_method_switch.go:48`; `tenantcfg.go:73` | OPEN | P2 B6 |
| D2-L3 | Low | Room offer list runs a few queries per package | `cmd/scd/phase3_offers.go:102` | OPEN | P2 B5 (after S3) |
| D2-L4 | Low | Last sign-in step waits for netd by polling | `cmd/scd/phase3_auth.go:972-1010` | OPEN | P2 B6 |
| D2-L5 | Low | scd accepts any number of requests at once | `cmd/scd/main.go:975` | OPEN | P2 B6 |
| D2-CC1 | CC | Argon2 parameters defined three times | `internal/passwordhash`; `internal/poststay/poststay.go:41-43`; `cmd/edged/auth.go:20-22` | OPEN | P2 B1 |
| D2-CC2 | CC | Each sign-in method repeats the same lookups | scd handlers | OPEN | P2 B6 |
| D3-M1 | Medium | Landing page rebuilt and compressed for every request, including unknown paths | `cmd/portald/main.go:155-182,589-598` | OPEN | P3 B1. Step 3 (CSP) is CONTROLLED |
| D3-M2 | Medium | Each page view makes three more calls, two reaching scd and its DB | `templates.go:1117`; `email_handlers.go:27-55` | OPEN | P3 B1 |
| D3-L1 | Low | MAC lookup reads the whole neighbour table | `cmd/portald/arp.go:13-34` | OPEN | P3 B2. See backend P5 L1 |
| D3-L2 | Low | Hotel images: no cache lifetime, no size limit beyond 8 MB | `cmd/portald/main.go:570-571` | OPEN | P3 B2 (has an edged half) |
| D3-L3 | Low | Every TLS handshake stats the certificate files | `cmd/portald/portal_https.go:356-386` | OPEN | P3 B2 |
| D3-L4 | Low | scd client keeps only two idle connections | `cmd/portald/main.go:92-106` | OPEN | P3 B2 |
| D3-L5 | Low | Busy scd shown as "service unavailable" with no retry | `cmd/portald/main.go:235,258`; `client_resume.go:160` | OPEN | P3 B3 (with P2 B1) |
| D3-L6 | Low | Per-request work that is the same for every request | `portal_page.go:123` and others | OPEN | P3 B1 |
| D4-H1 | High | One guest network cannot enforce more than 2,047 devices | `internal/shape/shape.go:458` | OPEN | P4 B1. CONTROLLED |
| D4-H2 | High | Enforcement pass does per-device process and DB work every second | `cmd/acctd/main.go:178,201`; `cmd/netd/phase3_shaping.go:154,202` | OPEN | P4 B2 (acctd and netd per-second redesign) |
| D4-H3 | High | Every packet matched against device filters one by one | `internal/shape/shape.go:458` | OPEN | P4 B1 |
| D4-M1 | Medium | Revoking a device reads the whole conntrack table | `internal/conntrack/netlink_linux.go:16`; `cmd/netd/phase3_gate.go:117` | OPEN | P4 B3. See backend P8 L10 |
| D4-M2 | Medium | acctd sends the whole plan every second; netd never skips an unchanged one | `internal/enforce/enforce.go:114-125` | OPEN | P4 B2 |
| D4-M3 | Medium | Shared-allowance groups collide by chance and are refused | `internal/shape/shape.go:75-95` | OPEN | P4 B4. See backend P6 L12 |
| D4-L1 | Low | Class counters dumped four times a second per guest network | `internal/shape/shape.go:494`; `cmd/acctd/phase3_accounting.go:107` | OPEN | P4 B2 |
| D4-L2 | Low | Structure check dumps the whole nftables table every 10 s | `internal/nftconverge/converge.go:152`; `cmd/netd/nft_watch.go:59` | OPEN | P4 B2 |
| D4-L3 | Low | Guest networks wider than /20 alias device classes | `internal/shape/shape.go:58` | OPEN | P4 B4 |
| D4-CC1 | CC | Every kernel operation is a separate process | `internal/shape/shape.go:45`; `internal/nft/nft.go:38,243` | OPEN | P4 B2 |
| D5-H1 | High | Sessions cannot be found by entitlement; expiry sweep looks them up that way every second | `iam_v2.sessions` (no `entitlement_id` index) | OPEN | P5 B1 (index migration) |
| D5-H2 | High | acctd runs every step in one serial loop | `cmd/acctd/main.go:182-206` | OPEN | P5 B2 (Wave 2). Its tick follows decision D1-H2 |
| D5-M1 | Medium | Sweep rewrites rows every second whether or not anything changed | `p6_advance_data_crossing` and others (baseline) | OPEN | P5 B3 |
| D5-M2 | Medium | Every 15 min scd re-reads and re-hashes every stored backup | `cmd/scd/backups.go:450-475,575` | OPEN | P5 B4. See backend P9 L8 |
| D5-L1 | Low | With no guests online, acctd still runs about a dozen statements a second | `internal/enforce/enforce.go:82` | OPEN | P5 B5 |
| D5-L2 | Low | Daily log cutting copies each whole log, then truncates it in place | `cmd/scd/log_retention.go:5` | OPEN | P5 B5 |
| D6-H1 | High | Room sign-in closed hotel-wide while any PMS event waits; applier slow | `iam_v2.p3_feed_authorizes` (migration 0150) | OPEN | P6 B2, steps 1–3. Step 4 (scope the rule per stay or room) is a decision |
| D6-M1 | Medium | Roster reconciliation scans every event the interface has ever received | `iam_v2.pms_roster_of_generation`; `pms_roster_reconcile` | OPEN | P6 B1 |
| D6-M2 | Medium | Each PMS record is its own transaction; the socket waits for it | `internal/pmsd/pg.go:256` (`insertInboxRow`) | OPEN | P6 B3 |
| D6-L1 | Low | Every resync keeps another full copy of every in-house guest's record | `stay_events` RESYNC rows | OPEN-DECISION | P6 B4: reduce superseded payloads to their evidence hash |
| D7-H1 | High | Session, PMS stays and device pages look sessions up with no index, per row | `cmd/edged/resources_phase3.go:191-263` and others | OPEN | P7 B1 (index migration) + P7 B2 |
| D7-H2 | High | Overview traffic charts sum raw per-second detail | `cmd/edged/resources_overview.go:690-770` | OPEN-DECISION | P7 B3 (Wave 3, after D1-H3) |
| D7-M1 | Medium | PMS revisions page joins the whole audit log to itself | `cmd/edged/resources_phase3_interfaces.go:332-349` | OPEN | P7 B1 + B2 |
| D7-M2 | Medium | Client device list reports a failed read as "no devices" | `cmd/edged/client_devices_admin.go:192-206` | OPEN | P7 B2. Re-checked at HEAD: no `rows.Err()` |
| D7-M3 | Medium | Polled pages pay whole-table aggregates on every refresh | `hotel-admin/app/(app)/dashboard/page.tsx:79` | OPEN | P7 B2 |
| D7-L1 | Low | Three handlers have no request timeout of their own | `resources_usage.go:101,448` | OPEN | P7 B2 |
| D7-L2 | Low | Health loop trims its event table with a full anti-join every 10 s | `cmd/edged/health.go:144` | OPEN | P7 B2 |
| D8-M1 | Medium | Raw timers keep polling in hidden tabs | hotel-admin components (bare `setInterval`) | OPEN | P8 B1 |
| D8-M2 | Medium | Central appliance list re-renders the whole fleet on every refresh | `cloud-admin/app/(app)/appliances/page.tsx:62-78` | OPEN | P8 B2 |
| D8-L1 | Low | Polls can overlap; replaced requests never cancelled | both apps' `usePoll` | OPEN | P8 B1 |
| D8-L2 | Low | Same data fetched twice (health badge and page) | `components/appliance-status.tsx:31`; `dashboard/page.tsx:53` | OPEN | P8 B1 |
| D8-L3 | Low | Every console API call passes through Node | `hotel-admin/next.config.mjs:39-43` | OPEN-DECISION | Decide after Phase 11 S8 |
| D8-L4 | Low | hotel-admin package ships 27 MB it never uses | `hotel-admin-deploy.tgz` (`deploy/scripts/deploy-hotel-admin.sh`) | OPEN | P8 B3 |
| D9-M1 | Medium | Hourly fetch-log prune reads the whole 30-day log | `control-plane/internal/api/terminal_delivery.go:390-393` | OPEN | P9 B1 (index; Central migration) + P9 B2. Step 1 (log only served or denied fetches) is a decision |
| D9-M2 | Medium | Fleet list and Overview scan every licence once per appliance | `control-plane/internal/api/fleet.go:78-118` | OPEN | P9 B2 |
| D9-L1 | Low | Appliance detail page reads the whole audit log for a quiet appliance | `control-plane/internal/api/fleet.go:295-315` | OPEN | P9 B1 |
| D9-L2 | Low | Appliance calls have no jitter and all start at boot | `cmd/scd/main.go:1069-1070` and others | OPEN | P9 B3 |
| D9-L3 | Low | Every signed call writes `appliances.last_seen_at` | `control-plane/internal/auth/appliance.go:119-122` | OPEN | P9 B2 |
| D10-H1 | High | edged's health loop leaks a connection to scd and netd on every check (about 1.5 GB a day) | `cmd/edged/health_checks.go:17-34` | OPEN | P10 B1 (Wave 1). File unchanged since the audit |
| D10-M1 | Medium | A verified backup loads a second copy of the DB into the live Postgres | `cmd/scd/backups.go:121-255` | OPEN | P10 B2. CONTROLLED |
| D10-M2 | Medium | System logs kept by size, not by the hotel's log period | `docs/privacy/DPA_ANNEX_II_SECURITY_MEASURES.md` §9; journald, Caddy, Kea config | OPEN | P10 B3 (Kea part CONTROLLED) |
| D10-M3 | Medium | No appliance service has a memory limit or kill order (except hotel-admin) | `deploy/systemd/stayconnect-*.service` | OPEN | P10 B4 (deployment configuration) |
| D10-M4 | Medium | Disk budget set by the accounting grain; backups nearly double it | `/opt/stayconnect/backups/db`; `deploy/scripts/backup-retention.conf` | OPEN | P10 B5. Sizing depends on decision D10-D1 |
| D10-L1 | Low | Every service runs a 64 MiB Argon2 hash at start | `internal/passwordhash/passwordhash.go:80-85` | OPEN | P10 B1 |
| D10-L2 | Low | The appliance runs a Redis that nothing uses | `deploy/compose/infra.yml:42-51` | OPEN | P10 B4 |
| D10-L3 | Low | A manual backup gives up after 20 min, a limit that comes into reach as the DB grows | `cmd/scd/backup.go:97` | OPEN | P10 B2 |
| D11-L1 | Low | Guest data export reads the whole audit log every time | `data-plane/migrations/0147_*.up.sql:2134,2178-2184` | OPEN-DECISION | P1 B7 with D11-L1 (after decision D1-L5) |

Not rows: the decision ids (Phase 0 D1/D2, D10-D1, D10-D2) are listed below. "Outside this audit" items in the perf FINAL_REPORT (server timeouts, cloud-admin Next 14.2.5 advisory and `unsafe-eval`, portal per-device limit = backend P8 M1) are not counted by the source; the Next.js advisory has no backend-audit row either and is noted under "Unresolved" below.

---

## C. Summary

### Backend audit: rows by status and severity

| Severity | Rows | OPEN | OPEN-DECISION | PARTIAL | FIXED |
|---|---|---|---|---|---|
| High | 5 | 2 | 2 | 0 | 1 |
| Medium | 64 | 47 | 12 | 3 | 2 |
| Low | 146 | 137 | 8 | 1 | 0 |
| Clean code | 43 | 43 | 0 | 0 | 0 |
| **Total** | **258** | **229** | **22** | **4** | **3** |

- **FIXED (3):** P8 H1, P8 M6, P8 M10.
- **PARTIAL (4):** P8 M7, P8 M9, P9 M5, P8 L12.
- **OPEN-DECISION (22):**
  - High: P0-2, P1 H2.
  - Medium: P1 M4, M5, M6, M15; P2 M3, M4, M8, M14; P3 M2; P4 M2; P8 M2; P9 M4.
  - Low: P1 L21; P2 L23; P3 L10; P4 L3, L11; P8 L1; P9 L2, L4.

**Row count against FINAL_REPORT** (0 / 5 / 64 / 147 / 43):
- **Low is 146, not 147.** The source counts P0-3 (Phase 0 Low) and X1 (Phase 10 Low) separately, but they are one finding. Here they are one row, under P0-3.
- **Medium is 64, matching the source.** P6 M4 is merged into P8 M4, as FINAL_REPORT does.
- **Kept as separate rows, as the source counts them, with cross-references:**
  - P3 M4 and P4 M1 (the same ledger defect, settled in Phase 4);
  - P6 L14 and X3;
  - P9 M5 and P8 H1/M9.

### Performance audit: rows by status and severity

| Severity | Rows | OPEN | OPEN-DECISION | PARTIAL | FIXED |
|---|---|---|---|---|---|
| High | 13 | 10 | 3 (D1-H2, D1-H3, D7-H2) | 0 | 0 |
| Medium | 28 | 27 | 1 (D2-M3) | 0 | 0 |
| Low | 37 | 30 | 7 (D1-L4, D1-L5, D1-L7, D2-L1, D6-L1, D8-L3, D11-L1) | 0 | 0 |
| Clean code | 4 | 4 | 0 | 0 | 0 |
| **Total** | **82** | **71** | **11** | **0** | **0** |

The rows match the source totals exactly (0 / 13 / 28 / 37 / 4). Three OPEN rows have one step that waits for a decision; the rest of each finding can go ahead without it:
- D6-H1, step 4;
- D9-M1, step 1;
- D5-H2, whose tick is set by decision D1-H2.

### The five backend High findings

| # | Finding | Status | Evidence |
|---|---|---|---|
| 1 | P0-2: Go toolchain and libraries carry reachable vulnerabilities (appliance 37, Central 20) | OPEN-DECISION | Both `go.mod` files still say `go 1.25.0`; both build scripts use `golang:1.25.12` |
| 2 | P1 H1: a missing CA certificate file makes Central mint a new root CA | OPEN | No commit since the audit touches `internal/pki` for this |
| 3 | P1 H2: an appliance is activated on facts anyone can know | OPEN-DECISION | Activation-code decision not taken. D75 (`d799ff5f`) is a different defect (a stale CSR on reinstall) |
| 4 | P2 H1: licence anti-replay and revocation records can be silently lost | OPEN | No commit since the audit touches `license/store.go` |
| 5 | P8 H1: a guest network's name can add firewall rules | **FIXED** | `60e15df7` (names Phase 8 H1), merged in PR #242 (`fa24a913`), T0248 / D72 |

### Product-Owner decisions the audits name

**Backend: the FINAL_REPORT list (17).**

| # | Decision | Recommended |
|---|---|---|
| 1 | P0-2: Go 1.26 and module updates | Yes, first backend branch |
| 2 | P1 H2: activation code shown on the appliance console | Yes |
| 3 | P1 M4, M5: hide staff emails, internal reasons, staff ids and IPs from customer users | Yes |
| 4 | P1 M15: may administrators reset peer administrators? | No, owners only |
| 5 | P2 M3: remove the legacy allowance for unbound licences | Yes, after confirming none is deployed |
| 6 | P2 M8: pin the mTLS trust anchor at provisioning | Yes |
| 7 | P2 M14: keep the network-wide room sign-in pause | Keep; exempt recent successes; raise the default; alert |
| 8 | P3 L10: keep a Central appliance's cert and lifecycle history after deletion | Yes, `RESTRICT` + soft delete |
| 9 | P3 M2, fix 4: one-time audited adopt of 0001–0149 on baseline-built appliances | Yes, with P3 B2 |
| 10 | P4 M2: digits-only voucher minimum 6 → 8 | Yes |
| 11 | P4 L3: normalise guest names to NFC | Yes, nothing looser |
| 12 | P4 L11: block a tenant transition until a backup key holder exists | Yes |
| 13 | P8 M2: which internal networks guests may reach | Deny private, CGNAT and link-local by default, with an allow list |
| 14 | P8 L1: relabel client isolation, or enforce it on multi-port bridges | Relabel now; enforce on multi-port bridges |
| 15 | P9 M4: record guest-list browsing in the read audit | Yes, one entry per page view, no search text |
| 16 | P9 L2: desk reset of a guest password needs a reason? | A reason; no step-up |
| 17 | P9 L4: keep operator lockout as it is | Keep; document the trade-off |

**Backend: also marked DECISION in the phase files but not in FINAL_REPORT's list.**
- P1 M6: per-address two-step lock; needs a migration.
- P1 L21: which Central values become §0C settings.
- P2 M4: should revocations become a vendor-signed document?
- P2 L23: which OTP limits become site settings.
- P8 L14: netd unit hardening, called "a separate decision".
- P1 L22 is on Phase 1's waiting list, but it is a read-only check on Central, not a product decision.

**Performance: from FINAL_REPORT (12).**

| # | Decision | Recommended |
|---|---|---|
| 1 | Phase 0 D1: are the design targets right (2,000 devices, 300 sign-ins a minute on 2 vCPU / 4 GB)? | Yes for now |
| 2 | D1-H2: how soon must a used-up allowance or time limit cut a guest off (acctd tick)? | Within 5 s, as a setting |
| 3 | D1-H3: usage-detail grain and retention | Hourly totals for 12 months, per-minute for 7 days (decide with D10-D1) |
| 4 | D1-L5: what anonymised per-event rows are kept for, and how long | See Phase 1 |
| 5 | D2-M3: default appliance-wide sign-in ceiling | Follow the licence guest cap, never below 600 a minute |
| 6 | D2-H1 (later): keep Argon2id 64 MiB or move to OWASP's lighter setting | Keep and add the limiter |
| 7 | D6-H1 step 4: refuse room sign-in only while that stay's or room's event waits | See Phase 6 (changes guest-auth semantics) |
| 8 | D6-L1: reduce superseded RESYNC payloads to their evidence hash | See Phase 6 |
| 9 | D8-L3: route console API straight from Caddy to edged | Decide after S8 |
| 10 | D9-M1 step 1: log only served and denied assignment fetches | Yes |
| 11 | D10-D1: reference disk given the D1-H3 grain | 80 GB with 7 days per-minute; 40 GB hourly only |
| 12 | D10-D2 (= Phase 0 D2): is 4 GB still the minimum? | Yes after Wave 1; 8 GB for stress load |

Phase 11's two read-only PRE-LIVE counts (P1 probe rate, P2 resync pace) also need their own authorization. PRE-LIVE `172.21.60.25` was permanently retired on 2026-10-28, so they need a new target.

### Unresolved or low-confidence mappings

- **`2a179df9`** ("Room sign-in: a device that moved network gets a session at its new address…") says its two defects were "found by the networking audit". It cites no finding id, and neither defect matches a Phase 8 or perf finding text. P8 M3 is about binding the MAC in nft and is not addressed by it. It is not mapped to any row.
- **`d799ff5f` (D75, F1)** is related to P1 L8 and P1 M2 (CSR handling) but fixes neither as written. Both stay OPEN.
- **Partial fixes:** the PARTIAL statuses on P8 M7, P8 M9, P8 L12 and P9 M5 rest on reading the diffs of `60e15df7` and `490321c1` at HEAD. None of them was run.
- **The Next.js advisory:** the performance FINAL_REPORT's "Outside this audit" mentions cloud-admin's Next 14.2.5 advisory and `unsafe-eval` CSP. Neither audit counts it as a finding, so it has no row and no status.

## Security hardening list of 2026-07-11 (folded in)

`docs/SECURITY_HARDENING.md` (source: `git show 1d69508a847248ec63ceed01e06ec8f8f1661fb0:docs/SECURITY_HARDENING.md`) was a
pilot-era list. Its statuses below come from the system handbook §23 assessment and the current tree; nothing is
closed on a guess.

| ID | Sev | Title | Where | Status | Evidence |
|---|---|---|---|---|---|
| SH-1 | High | Gmail app password once committed in plaintext | `deploy/observability/alertmanager/alertmanager.yml` | PARTIAL | No longer in the tree (read from `smtp_password_file`, `secrets/` ignored); revocation in the Google account is not recorded, and the value stays in Git history. Also GDPR plan C12. |
| SH-2 | High | ctrlapi `:8080` / console `:3000` open on the WAN | appliance nftables | FIXED | Handbook §23: no 8080/3000 exposure; the appliance runs no ctrlapi; Central console on loopback behind Caddy. |
| SH-3 | Medium | Dev database credentials | appliance and Central | PARTIAL | Handbook §23: database passwords generated on fresh installs; an older appliance keeps its initdb password until migrated (Overview warns; PO decision 2026-10-19). |
| SH-4 | High | IPv6 client bypass of the captive portal | netd ruleset | FIXED (interim) | Handbook §23: IPv6 dropped on client networks. Dual-stack capture (the real fix) is not implemented. |
| SH-5 | Medium | Secure cookies off by default | ctrlapi / edged | FIXED | Handbook §23: cookies Secure by default. |
| SH-6 | Medium | Grafana not behind Caddy | `deploy/observability` | OPEN | Optional stack, not installed by the installers (handbook §21); unchanged if deployed. |
| SH-7 | Low | Appliance-JWT replay cache is in-process | ctrlapi | OPEN | Not re-assessed. |
| SH-8 | — | "Additional hardening (target architecture)" list and the pilot-cutover checklist | — | OPEN | Not re-assessed; superseded in practice by the installation manual's verification checklist (Part D, F.5). |
