# Backend audit — Phase 4: guest sign-in (scd)

| | |
|---|---|
| Audited commit | `9ffa981b` (master; unchanged when this phase started) |
| Scope | `data-plane/cmd/scd` (78 files, about 21,600 lines; 109 endpoint registrations) and `data-plane/internal`: iamv2, stayengine, staygrant, grace, social, socialloader, deviceselfservice, enforce, namenorm, phone. Also `internal/ledger` and scd's ledger code, to settle Phase 3 M4. About 35,000 lines of non-test Go. |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |
| Open branch checked | `delivery/authenticator-admin-console-branding` (Ahmed, not merged) touches 6 files in scope, all wording only ("Hotel Admin" → "Admin Console"). No finding here conflicts with it. |

## How to use this file

1. **Format.** It is the same as Phases 1–3. Each finding has a problem, its impact, the recommended fix and the test that proves it. Fixes are grouped into batches at the end.
2. **Most of this is CONTROLLED.** It covers guest authentication, entitlement and enforcement (CLAUDE.md §0). Each batch names its area.
3. **Status of a finding:**
   - **Confirmed**: the code path was traced, or reproduced.
   - **Possible**: the code suggests the problem, but it needs a test or deployment facts to prove.
4. **How it was checked.**
   - The auditor read the routing and caller gate (`main.go`, `admin_surface.go`, `peer_identity_linux.go`), room sign-in (`phase3_auth.go`), the remembered-device credential, the throttle, and the erasure ledger (`internal/ledger`, `ledger_service.go`, `ledger_restore.go`).
   - Three independent reviewers read the rest in three slices:
     - accounts, social sign-in, payments and vouchers;
     - backup, restore and the link to Central;
     - the IAM and stay engines.
   - Every claim of theirs listed here was re-checked; the rejected ones are at the end.
5. **Who calls scd.** scd listens only on a unix socket.
   - **Guest routes** are called by portald, which forwards what a guest sends, so their bodies are treated as attacker-controlled.
   - **Admin routes** accept only edged (its uid *and* its exact binary) or root. edged authenticates the operator first; whether it does so correctly is Phase 9.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 3 |
| Low | 15 |
| Clean code | 4 |

### What was run

| Check | Result |
|---|---|
| `go vet` (all Phase 4 packages) | clean |
| `staticcheck` | 10 unused functions or types (CC1), 2 always-false test comparisons (CC2) |
| `gosec` | 134 leads, triaged below. Nothing exploitable beyond the findings: path operations take operator-set or fixed paths; `syscall.Exec` re-executes scd's own binary; `math/rand` is only retry jitter. |
| `govulncheck` | only the Phase 0 P0-2 advisories |
| Unit tests, all Phase 4 packages | pass |
| **Database integration tests** (`-tags integration`) on a throwaway full-schema database, scd's tests also as the real `svc_scd` role: `cmd/scd`, `stayengine`, `staygrant`, `enforce`, `authctx`, `writerguard` | **576 pass, 0 fail**. 17 skipped: 16 restore drills need `LEDGER_DRILL_DSN` (a Linux host with Docker tooling); 1 symlink test cannot run on Windows. |

### What holds up well (keep it this way when fixing)

- **The guest/admin split on the socket.**
  - Every route not on the guest list is admin-only by default (`admin_surface.go:105-121`).
  - An admin route needs edged's uid **and** edged's exact executable (or root), read from the kernel at accept time.
  - portald has its own account.
  - A test walks the real router, so an unclassified route fails the build.
- **Room sign-in (`phase3_auth.go:274-528`):**
  - every exit writes an attempt record;
  - the licence and the method switch are checked first;
  - the device and network protection gates run before any evidence is compared, keyed on the MAC the appliance read itself;
  - an unreadable gate is a refusal, not an allow;
  - the guest sees only a coarse failure class, never the detail.
- **Consuming a sign-in context** locks the stay and re-checks, on the locked row, that it is still checked in, unchanged and fed by a healthy PMS (`internal/authctx/authctx.go:300-345`). A guest who checked out in the meantime is refused.
- **The remembered-device credential:**
  - 256-bit, stored only as an HMAC;
  - tied to the device MAC, and revoked when presented from another device;
  - rate-limited per device, with an expiry from the site setting.
- **Email-account tokens:**
  - 256-bit, HMAC-stored, single use, scoped by purpose;
  - `forgot` answers the same for everyone.
- **OIDC:**
  - RS256 only, known key id;
  - signature, audience, expiry, not-before and issued-at are checked;
  - nonce, PKCE and `email_verified`;
  - the stub provider exists only in non-production builds.
- **Payment:**
  - price, currency and amount come from the database, never the client;
  - one purchase per stay.
- **Secrets:**
  - provider, notification and MFA secrets are sealed with associated data;
  - TOTP replay is refused, and recovery codes are HMAC-stored and single use.
- **Backups:**
  - never written without a key holder (`backups.go:137`);
  - kept in a root-only directory;
  - the restore swaps by rename and keeps one database aside.

## Medium

### M1 — A forged extension of the erasure ledger's external copy is replayed into the database (Phase 3 M4, settled)

**Status:** Confirmed (traced; not reproduced) · CONTROLLED (guest data, destructive data)

**Where:**
- `data-plane/internal/ledger/store.go:314-395` (`pushExternalLocked`) and `:432-482` (`ReadExternal`).
- `internal/ledger/assess.go` (`Assess`: "if cs > bs … best = c").
- `data-plane/cmd/scd/ledger_service.go:713-790` (`ledgerRecoverFromExternal`), `cmd/scd/ledger_restore.go:130-200` (restore).
- Phase 3 M4's database side: `0148:72-79`, `:408-470`.

**Problem:**
1. The external copy is protected for **secrecy**, not origin.
   - It is age-encrypted to the backup key holders' public keys, and when no key holder is set up it is stored in the clear.
   - The note beside it (`ledger.head.json`) carries plain SHA-256 digests, which anyone can recompute.
   - The chain hash has no key. So anyone who can write the destination (the NAS share, the SFTP account, the S3 bucket) can build a valid-looking copy.
2. When copies differ, `Assess` trusts the **longest** consistent one.
   - A forged copy that extends the real ledger agrees with every witness (the database, the local file, the backup), because the witnesses only vouch for earlier entries.
   - Its extra entries are then applied through `ledger_apply`, and each `erase` entry anonymises the records it names.
3. This happens on two operator-started paths: a backup restore, and "recover from the secondary storage".
   - The hourly check applies only from the local file, which is root-only on the appliance (`ledger_service.go:379-408`).
4. A forged head note with a higher sequence also makes every later push fail (`ErrExternalAhead`, `store.go:337-345`). Every erasure request is then refused until support intervenes.

**Impact:**
1. Someone who compromises the external storage credentials can make the next restore erase guest records of their choosing.
2. Erasure is irreversible, and every later restore replays the same forged ledger.
3. They can also block GDPR erasure on the appliance at will.

**Fix:**
1. Add a per-appliance ledger MAC key in the local-keys store, plus a copy sealed to the backup key holders, so a replacement appliance can still verify.
2. Write an HMAC over each entry (or over the whole object and head note) into the external copy, and verify it in `ReadExternal` before `ParseChain`. A copy without a valid MAC is "untrusted", never Best.
3. Database side (Phase 3 M4): `ledger_apply` and the adopt path accept only entries that have passed that check, passed by scd. Revoke `ledger_confirm` from `svc_edged` (Phase 3 L8).
4. Treat a head note that is ahead but fails MAC verification as an alert, not as `ErrExternalAhead`.

**Test:**
- `ledger` unit tests:
  - an external copy extended by one valid-chain entry without a valid MAC is refused by `Assess`;
  - a genuine longer copy is still accepted;
  - a forged head note does not block the push.
- A restore drill (`restore_drill_integration_test.go`, on Linux) with a forged external extension erases nothing.

### M2 — Digits-only 6-character vouchers can be guessed by a device that changes its MAC

**Status:** Confirmed (from the code and its limits; not run) · CONTROLLED (guest authentication)

**Where:**
- `cmd/scd/voucher_code_format.go:60-65`: lengths 6–8; digits-only uses 7 symbols, so 6 characters give 7^6 = 117,649 codes.
- `cmd/scd/voucher_issue_iamv2.go:128`: up to 500 codes per batch.
- `cmd/scd/main.go:373` charges `throttleGuard("voucher", ip, mac, "")`.
- `cmd/scd/authsec.go:21-28`: 30 attempts per minute per IP or device for 15 minutes, and 600 per minute appliance-wide across every method.
- `authsec.go:106-117`: the throttle exists only when `SCD_DURABLE_THROTTLE` is set; provisioning sets it (`deploy/scripts/provision-fresh-appliance.sh:338`).
- The default format is 8 mixed characters (`0085:62-63`), which is safe.

**Problem:**
1. Voucher sign-in has no limit tied to the voucher space or the site. Only per-IP and per-device rules apply, and a device on guest Wi-Fi can change its MAC (and with it its DHCP address) freely.
2. Once those rules are sidestepped, the only bound is the appliance-wide 600 attempts per minute.
3. With 500 live 6-digit codes, each guess has about a 1-in-235 chance, so an attacker finds about 2.5 working vouchers per minute.
4. While doing so they use up the shared limit, and every real guest sign-in (room, code, account, social) is refused with `TOO_MANY_ATTEMPTS`.

**Impact:**
1. A hotel that chooses digits-only short codes (an option the Admin Console offers) gives away paid internet.
2. Any guest can deny every other guest sign-in on the appliance.

**Fix:**
1. Add a voucher-wide failure limit, the way room sign-in has a network pause (`signinattempt` `CheckNetwork`).
   - Many wrong voucher codes on one guest network within a window pause voucher sign-in on that network.
   - It is a site setting under §0C, with a default, unit, bounds, permission and audit.
2. Raise the floor: 8 characters for digits-only codes (7^8 ≈ 5.8 million). **DECISION**, since it changes what hotels may print.
3. Do not let the appliance-wide damper be used up by one method. Give each method its own global budget, so a voucher flood cannot block room sign-in.
4. Make `DurableThrottle` default on, so an appliance without the variable fails closed (L1).

**Test:**
- An integration test: wrong voucher codes from 50 rotating MACs on one network pause voucher sign-in on that network, while room sign-in on the same network still works.
- The settings test covers the new limit's bounds and audit.

### M3 — One failing entitlement stops every expiry on the site

**Status:** Confirmed (traced) · CONTROLLED (enforcement core)

**Where:** `data-plane/internal/enforce/enforce.go:252-433`. The site sweep is one transaction, and any error at `:302-428` (including from `p6_expire_entitlement`, `:394-402`) returns and rolls back everything.

**Problem:**
- The sweep ends every entitlement whose time or data has run out, in a single transaction for the whole site.
- If one entitlement errors every time (a bad row, a constraint, a function bug), the sweep fails and rolls back, and so does the next one.

**Impact:**
1. Expiry stops for the whole site, not just for the broken row. Every guest keeps access past their time or data limit, which fails open on what the hotel sells.
2. Paid packages and quotas stop being enforced until someone notices the log.

**Fix:**
1. Process each entitlement under a savepoint (or its own short transaction).
2. On an error, log it with the entitlement id, count it in a metric, skip it and continue.
3. Raise a staff-visible alert when the same entitlement fails three sweeps in a row.

**Test:** an integration test with one entitlement made to fail (a test trigger that raises for one id). The other expired entitlements in the same sweep are still ended, and the failure is reported.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | The durable guest throttle is off unless `SCD_DURABLE_THROTTLE` is set. Every `throttleGuard` call is then an allow, with no log on each request. Provisioning sets it, but an appliance installed any other way has no guest rate limit. | `cmd/scd/authsec.go:106-117`, `:177-179` | Default on; refuse to start without the key, as already happens when it is on. With M2. |
| L2 | `phone.Normalize` accepts any Unicode digit and counts length in **bytes**. "+" followed by four Arabic-Indic digits passes the 8-digit minimum, and the same number in two digit scripts becomes two destinations, so per-destination code limits count them separately. | `internal/phone/phone.go:31-48` | Accept only `'0'..'9'` (optionally map other digit scripts to ASCII first) and count runes. Unit test with Arabic-Indic and fullwidth digits. |
| L3 | `namenorm` compares names without Unicode normalisation. A name stored in decomposed form (é as e + accent) never matches the same name typed in composed form, so the real guest cannot sign in. Everything else it omits is a documented Product-Owner choice. | `internal/namenorm/namenorm.go:15-22` | Apply NFC (`golang.org/x/text/unicode/norm`) on both sides, then re-normalise the stored `*_norm` columns in a migration. **DECISION** if anything looser is wanted. |
| L4 | Social sign-in holds a database connection and a row lock on the state row while it calls the provider (up to 10 s). Many callbacks, or one slow provider, take connections away from every other guest route. | `cmd/scd/social_handlers.go:183` (`FOR UPDATE`) to `:235` (`prov.Exchange`) to `:262` (commit) | Claim the state with `UPDATE … SET consumed_at = now() WHERE consumed_at IS NULL RETURNING …` in a short transaction, call the provider outside it, then write the result in a second one. |
| L5 | A JWKS refetch runs while the cache mutex is held, so one slow key fetch stalls every social sign-in for up to 10 s. | `internal/social/jwt.go:83-108` | Fetch outside the lock (single-flight), then swap under it. |
| L6 | scd itself does not require a device on `social/start`, `account/verify` and `account/reset`. An empty IP or MAC skips the device binding and the IP throttle. portald always sends both, from the kernel, so this is not reachable today. | `social_handlers.go:111-117`; `self_service_handlers.go:526`, `:647` | Refuse a guest request without a valid IP and MAC in scd too (defence in depth). |
| L7 | Most guest and admin routes decode JSON with no size limit. Some pass a nil `ResponseWriter` to `MaxBytesReader`, so an oversized body does not close the connection. | `main.go:351`, `social_handlers.go:43`, `:149`, `commerce_handlers.go:51`, `:87`, `:162`, `card_checkout.go:197`, `client_resume.go:232`, `voucher_admin.go`, `voucher_issue_iamv2.go`, `notify_admin.go`; nil writer at `self_service_handlers.go:28`, `temp_access_admin.go:156` | One helper, `decodeBody(w, r, &v, 64<<10)`, used everywhere. Test that a 1 MB body is refused. |
| L8 | Operator MFA in scd keeps no count of wrong codes, and `reset` removes an authenticator with no proof. That is correct only if edged rate-limits and step-up-guards these routes. | `cmd/scd/operator_mfa.go:197-215` (verify), `:298-310` (reset) | Verify edged in Phase 9. If it holds, add only a per-operator failure counter in scd as a backstop. |
| L9 | A device row and a network-appearance row are written for every room sign-in request before the licence or protection checks, so a MAC-rotating device grows `iam_v2.devices` without bound. | `cmd/scd/phase3_auth.go:226-249`, called at `:297` | Resolve the device read-only first, and write it after the protection gate; or prune devices never seen with a credential after N days (a retention setting). |
| L10 | The in-memory limiter for remembered devices keeps a slice per MAC with no pruning of idle keys. | `cmd/scd/open_access.go:39-60` | Drop keys whose newest hit is older than the window, on each call or on a ticker. |
| L11 | Compliance archives of a departing tenant, which hold guest data, are written unencrypted (mode 0600) when no backup key holder is set up. Backups refuse in that case. | `cmd/scd/compliance_archive.go:96-117` | Refuse, like backups, until a key holder exists. **DECISION** if a tenant transition must never block. |
| L12 | Several root-written state files are not written atomically or synced: the Central CA bundle (a truncated file breaks mutual TLS), the "removed from Central" marker, and the restore maintenance flag. | `offline_first_activation.go:273`; `removed_from_central.go:64-67`; `restore.go:150-154`, `:677` | Use the existing atomic writer (`offline_first_activation.go:349-360`: temp, fsync, rename, directory fsync). |
| L13 | When both renames fail during a restore swap, scd restarts the services anyway, against a database name that may not exist. They crash-loop and the appliance looks broken rather than "in maintenance". | `cmd/scd/restore.go:458-466`, `:489-493`, `:514-517` | Stay in maintenance mode and raise a critical alert; do not call `startDependents` on the double-failure paths. |
| L14 | Backups are authenticated only by a digest marker in the same root-only directory, and age does not prove who wrote a file. Planting one needs root today, so this is defence in depth. The digest is also checked minutes before the file is reopened for loading. | `restore.go:107-122`, `:243`, `:368` | Sign the marker with the same appliance key as M1. Hash and load from one open file descriptor. |
| L15 | Clock sources mix: the window start is Go's `time.Now()` while eligibility uses the database's `now()`. They are the same machine today, but the code would drift if the database moved. | `internal/staygrant/staygrant.go:201`, against the SQL `now()` checks | Take the time from the database in the same transaction (`SELECT now()`), or pass one clock through. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code** (staticcheck): `(*server).cloudHTTPClient`, `shq`, `cardVerifier.run`, `decodeGuestAuthBody`, `authorizeResp`, `iamv2.graceFields` and `splitGraceConfig`, `currencyMetaVersion`; the non-Linux `peerCred`/`slogWarnPeerRefused` stubs. | Delete them. |
| CC2 | Two tests compare a value with itself, so they can never fail: `internal/grace/grace_test.go:101`, `internal/stayengine/resolve_test.go:77`. | Compare against the intended value. |
| CC3 | **A stale security comment.** `main.go:974` says "portald shares edged's uid, so this stops a mistake, not a compromise", but portald now has its own account, and `admin_surface.go` and `peer_identity_linux.go` say so. The wrong one is next to the listener a reader checks first. | Correct it to one line. |
| CC4 | **Comment volume.** Most files carry long narrative comments (incidents, history), against the project's "no comments unless critical" rule. Root-run SQL is built with `Sprintf` and hand-escaping in two places: `restore_financial.go:175` (validated and escaped, so safe) and `ledger_restore.go:188` (a dollar-quote delimiter guarded by a check). | Trim comments when a batch touches a file. Pass values with `psql -v` and `:'var'` instead of formatting SQL. |

## Fix batches

A batch is done when its tests pass and the package tests, including the `integration`-tagged ones from Phase 3 B1's harness, still pass.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| **B1** Erasure ledger authenticity | M1, (Phase 3 M4, L8) | `internal/ledger/{store,assess,entry}.go`, `cmd/scd/ledger_service.go`, `ledger_restore.go`, `keybootstrap` (new key), a new appliance migration | CONTROLLED (guest data). Do it as one batch with Phase 3 B5. **Highest priority.** |
| **B2** Voucher guessing and throttle defaults | M2, L1 | `cmd/scd/authsec.go`, `main.go`, `voucher_code_format.go`, `internal/signinattempt` (network pause), a new settings migration, Admin Console settings screen (edged) | CONTROLLED (guest authentication). The length floor needs a decision. The settings screen overlaps Phase 9: coordinate. |
| **B3** Enforcement sweep | M3 | `internal/enforce/enforce.go` | CONTROLLED (enforcement core). Small; do early. |
| **B4** Input handling | L2, L3, L6, L7 | `internal/phone`, `internal/namenorm` (+ a migration for L3), `cmd/scd` decoders | L3 needs a migration and the decision. L7 touches many scd files: do it alone. |
| **B5** Social sign-in | L4, L5 | `cmd/scd/social_handlers.go`, `internal/social/jwt.go` | Guest authentication. |
| **B6** Restore and state files | L11, L12, L13, L14 | `cmd/scd/{restore,compliance_archive,offline_first_activation,removed_from_central}.go` | L14 shares the key from B1: run after it. |
| **B7** Growth and timing | L9, L10, L15 | `phase3_auth.go`, `open_access.go`, `staygrant.go` | Guest authentication core. |
| **B8** Clean code | CC1–CC4 | various | After the behaviour batches, never alongside them (same files). |

L8 waits for Phase 9's finding on edged.

**Waiting for a decision**

| Item | Decision | Recommended |
|---|---|---|
| M2 | Raise the minimum digits-only voucher length from 6 to 8? | Yes. |
| L3 | Normalise names to NFC (a guest whose name is stored decomposed can then sign in)? | Yes; nothing looser. |
| L11 | Block a tenant transition until a backup key holder exists, rather than write the archive unencrypted? | Yes. |

## Checked and rejected

- **"A grant does not re-check that the stay is still checked in."** Consuming the sign-in context locks the stay and re-checks `IN_HOUSE`, lifecycle, evidence and feed health on the locked row (`authctx.go:300-345`). The grant runs inside that transaction.
- **"Social sign-in can be started without a device, enabling login CSRF."** portald always sends the source IP and the MAC it read from the kernel, and refuses the guest when either is missing (`cmd/portald/social_handlers.go:33-56`). What remains is L6.
- **"The backup schedule can inject into a systemd unit."** It must match `^([01][0-9]|2[0-3]):([0-5][0-9])$` (`backup_settings.go:73`, `:164`).
- **"Backups fall back to plaintext."** `takeBackup` refuses without a key holder (`backups.go:137-138`).
- **"Set-aside databases accumulate."** Exactly one is kept (`restore.go:568-575`).
- **"The payment return address is an open redirect."** portald takes it from the guest's own `Host` only when HTTPS is not active, and it affects only that guest's own redirect. It is recorded for Phase 5 (portald).
- **"An unauthenticated restore can re-enable revoked state."** Restoring an older database restoring older rows is what a restore is. The erasure ledger, retention and financial-recovery steps are re-applied (`restore.go`). Session and operator handling after a restore is in edged: Phase 9.
- **gosec G404** (`registration.go:50`): `math/rand` is only retry jitter. **G702** (`assignment.go:373`): `syscall.Exec` of scd's own executable.

## Not verified

- **The 16 restore drills.** They need `LEDGER_DRILL_DSN` on a Linux host with Docker tooling; they include the ledger cases M1 relies on.
- **edged's side of every admin route** (operator authentication, permission, step-up, rate limits; L8). Phase 9.
- **portald's device identity** (how it reads the IP and MAC it forwards). Phase 5.
- **The SQL bodies of the controlled writers** (`authorize_entitlement_device`, `apply_entitlement_transition`, `p6_expire_entitlement`) were exercised by the integration tests but not read line by line.
- **Race-detector runs.** These still need a Linux build host.
- **Phase 2 M10 (writer guard).** The `writerguard` integration tests pass on the real schema, but they do not cover the trigger-state case M10 describes, so M10 stands.
