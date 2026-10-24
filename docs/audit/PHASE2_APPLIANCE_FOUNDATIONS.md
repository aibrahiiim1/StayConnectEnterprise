# Backend audit — Phase 2: appliance security foundations

| | |
|---|---|
| Audited commit | `9ffa981b` (master after PR #229) |
| Scope | the shared `license/` module, `data-plane/cmd/keybootstrap`, and these `data-plane/internal` packages: identity, applianceauth, appliancecert, localkeys, keybootstrap, sealbox, backupcrypt, hwid, crockford, otp, otpkey, totp, passwordhash, throttle, authctx, signinattempt, writerguard, licstate, activation, assignment, offline, portaltls. About 8,000 lines of non-test Go. |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |

## How to use this file

1. **Format.** It is the same as Phase 1. Each finding has a problem, its impact, the recommended fix and the test that proves it. Fixes are grouped into batches at the end.
2. **Most of these packages are CONTROLLED.** They are licensing and assignment identity, guest authentication, certificates and trust (CLAUDE.md §0). A fix batch here needs the authorization that area requires, not only a routine review. Each batch says which area it touches.
3. **Status of a finding:**
   - **Confirmed**: the code path was traced.
   - **Possible**: the code suggests the problem, but it needs a test or deployment facts to prove.
4. **How it was checked.** The auditor read every package. Two independent reviewers each read half. The auditor re-checked their findings, and their line numbers were corrected where they were wrong.
5. **Not covered here.** Callers in `cmd/scd`, `cmd/edged` and `cmd/portald` belong to Phases 4, 5 and 9. Where a finding depends on a caller, it says so.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 1 |
| Medium | 15 |
| Low | 24 |
| Clean code | 6 |

Tool results (latest master, Phase 2 packages only):
- `go vet`: clean.
- `staticcheck`: one dead function, `appliancecert.(*Manager).signedDo`.
- `govulncheck`: the same standard-library and module advisories as Phase 0 P0-2. No new ones.
- `gosec`: no finding beyond those listed below. The rest are configured file paths, and the SHA-1 that RFC 6238 TOTP requires.
- Unit tests of every Phase 2 package pass.

What holds up well (keep it this way when fixing):
- **Signatures.** Every signature covers exact bytes. The licence signs the raw payload, and the assignment, registry, offline and activation formats use fixed-order structs with golden tests. Signer keys are selected by key id, and an unknown key fails.
- **Licence binding.** It is re-checked on every evaluation, not only at install, so a licence copied from another box is refused after a reboot.
- **Assignments and Central outages.** Assignment expiry only marks the assignment stale, and a Central outage never unassigns a hotel.
- **First activation from a file.** `activation.AcceptForFirstActivation` fails closed on every empty field. It is the model the other verifiers should follow (M3).
- **Code and secret handling:**
  - one-time codes are counted and consumed in one locked transaction;
  - keyed-HMAC codes are length-prefixed with domain separation;
  - TOTP replay is refused by the caller's last-used step;
  - password hashing bounds every argon2 parameter;
  - the throttle fails closed on a database error and keeps blocks across windows;
  - sealed secrets are bound to their owner through the AEAD associated data.
- **PMS auth context.** It is consumed exactly once, against the full server-derived pin set, under the Stay lock.
- **Portal TLS.** A certificate is installed only after its name, its key, its validity and its chain to a public root are checked.

---

## High

### H1 — The licence's anti-replay and revocation records can be silently lost

**Status:** Confirmed · CONTROLLED (licensing)

**Where:** `license/store.go`:
- `readState` :38-45
- `writeState` :47-53
- `readRevoked` :56-67
- `Install` :79-129
- `AddRevocation` :146-158
- `Evaluate` :175-190

Callers: `data-plane/internal/licstate/licstate.go` `Load` :136, the minute ticker :479-490, and `FetchFromCloud` :413.

**Problem.** `state.json` holds the highest accepted licence version, the identical-document fingerprint and the clock high-water mark. `revoked.json` holds the revoked licence ids. Four things break them:
1. **No lock.**
   - `Evaluate` does read-modify-write on `state.json` every minute from the ticker goroutine, without the manager's lock.
   - `Install` and `MarkCloudValidated` do the same from the fetch loop and from HTTP handlers.
   - If `Evaluate` reads the state, then `Install` writes version N+1, `Evaluate` writes its stale copy back and the highest version returns to N.
   - All writers also share one temporary file name, `state.json.tmp`.
2. **Errors are read as "empty".** `readState` and `readRevoked` ignore read and parse errors and return zero values. A truncated or corrupt file silently switches off version-rollback protection, clock-rollback protection and every stored revocation.
3. **`revoked.json` is rewritten in place.** `os.WriteFile` truncates first, so a crash mid-write leaves it empty, and every revocation is forgotten.
4. **No `fsync`.** No write is synced before its rename, so after a power cut `state.json` or `current.json` can be zero-length.

**Impact:**
- An older, superseded or revoked licence that was signed by the vendor key can be installed again. For example, an older offline licence file imported by an operator could restore larger limits or removed modules.
- After a power cut the appliance can lose its licence and stay unlicensed until Central answers.

**Recommended fix:**
1. **Add a `sync.Mutex` to `license.Store`.** Hold it in `Install`, `Evaluate`, `MarkCloudValidated` and `AddRevocation`, so each read-modify-write is atomic.
2. **One write helper for every file:** a unique temp file (`os.CreateTemp` in the same directory), write, `fsync`, rename, then `fsync` the directory. Use it for `state.json`, `current.json` and `revoked.json`.
3. **Fail closed on unreadable files.** A file that exists but cannot be read or parsed must be an error, never a zero value.
   - `Install` and `Evaluate` return that error.
   - `licstate` reports "licence state damaged" (unlicensed, with an alert) instead of silently resetting.
   - A missing file still means a first install.
4. **Keep a backup copy.** Keep `current.json.prev` and `state.json.prev`. On a damaged current file, fall back to `.prev` and log it.

**Test:** unit tests in `license`:
- **Concurrency:** concurrent `Install` and `Evaluate` (with `-race` on a Linux build host) never lower `HighestVersion`.
- **Damaged files:** a truncated `state.json` or `revoked.json` makes `Install` fail, and does not accept a lower version.
- **Atomic revocations:** a revocation write interrupted between temp file and rename leaves the old list intact.

---

## Medium

### M1 — One boot with a wrong future clock can lock the licence for years

**Status:** Confirmed · CONTROLLED (licensing)

**Where:** `license/store.go:175-190` (`Evaluate`), `:122-127` (`Install`), `:131-143` (`MarkCloudValidated`)

**Problem:**
- `HighWater` is raised to any later `now`, with no upper bound.
- `Evaluate` uses `HighWater` instead of the real clock when the clock is more than 48 h behind it.

**Impact:**
1. The appliance boots once with its clock in 2030 (a dead RTC battery, a bad NTP answer).
2. `HighWater` becomes 2030.
3. After the clock is corrected, every evaluation uses 2030, so the licence reads Expired or Restricted and no new guest can sign in.
4. There is no reset except deleting the state file, which also removes the rollback protection.

**Fix:** trust only a bounded clock.
1. Raise `HighWater` only to the time Central signed or answered: `server_time` from the licence fetch, which arrives over the authenticated channel. Otherwise move it by at most a small step (for example 1 hour) per evaluation.
2. Add an operator action ("clock was wrong") that resets `HighWater` to the current verified Central time. It is audited.

**Test:**
- Unit test: an evaluation at `now = +5 years` followed by one at the real time stays Active.
- A reset from a Central time restores the state.

### M2 — A licence issued while Central's clock was ahead blocks every later licence

**Status:** Confirmed · CONTROLLED (licensing)

**Where:** `license/store.go:107-110,121`

**Problem:** `InstalledIssuedAt` is taken from the licence's `issued_at` without a bound. A later licence with an earlier `issued_at` is refused as a rollback.

**Impact:** a vendor-side clock error makes every correct licence issued after it fail to install. The appliance cannot recover by itself.

**Fix:** `license_version` already prevents rollback. Choose one:
- drop the `issued_at` comparison; or
- refuse a licence whose `issued_at` is more than 24 h ahead of the appliance clock at install, and never store a future `issued_at`.

**Test:** unit test: a v5 licence issued at T+10 days then a v6 licence issued at T installs v6.

### M3 — Identity checks are skipped when a field is empty, and unbound licences are accepted anywhere

**Status:** Confirmed · CONTROLLED (licensing, assignment) · **DECISION** (unbound licences)

**Where:**
- `license/binding.go:30-47` (`CheckBinding`)
- `data-plane/internal/offline/offline.go` (`AcceptFor`)
- `data-plane/internal/assignment/assignment.go:116-135` (`AcceptFor`), `resolve.go:140-150`
- `data-plane/internal/licstate/licstate.go:276-310`

**Problem:**
- Each check runs only when both sides are non-empty. A blank appliance-side value, for example an identity fingerprint not loaded yet, or a key that failed to decode in `resolve.go`, disables the check.
- A licence with no binding fields at all is accepted on any appliance ("legacy").
- The licence's `TenantID` and `SiteID`, and the offline package's tenant and site, are never compared with the appliance's signed assignment. `syncLimits` writes the licence's limits into the appliance's tenant rows whatever tenant the licence names.
- `ApplianceIDs` only produces a warning.

**Impact:** a valid vendor-signed licence for another hotel, or any unbound legacy licence, installs on any appliance whose own identity fields are blank at that moment.

**Fix:**
1. Follow `AcceptForFirstActivation`. An empty value on the document side is a refusal; an empty local value is a refusal unless the caller passes an explicit "identity not yet known" flag, used only by first activation.
2. When the appliance holds an accepted assignment, require the licence's and the offline package's `TenantID` and `SiteID` to equal it.
3. Refuse a v4 licence with no binding fields. Decision: confirm no deployed appliance still runs on an unbound licence, so the legacy allowance can be removed.
4. Make the `ApplianceIDs` mismatch a refusal instead of a warning.

**Test:**
- Table tests for each verifier, covering an empty field on either side.
- A licence for another tenant is refused when an assignment exists.

### M4 — Revocations from Central are trusted on the transport alone, and a failed save is ignored

**Status:** Confirmed · CONTROLLED (licensing)

**Where:** `data-plane/internal/licstate/licstate.go:413-475` (`FetchFromCloud`)

**Problem:**
- The `revoked` list in the fetch response is not signed.
- Before the mutual-TLS certificate exists, the request goes over plain HTTPS with the system roots.
- Revocations are applied before the envelope is checked, and `AddRevocation` errors are ignored.
- A response with `envelope: null` also resets cloud staleness.

**Impact:**
- Anyone who can answer that HTTPS request (a TLS-intercepting proxy at the hotel, a misrouted DNS name) can permanently revoke the installed licence. There is no un-revoke.
- A failed save loses a real revocation without anyone knowing.

**Fix:**
1. Accept revocations only over the mutual-TLS transport, which is pinned to Central's CA.
2. Better: carry them in a vendor-signed revocation document. This is a protocol change with Central, so it is a decision on the wire format.
3. Stop on an `AddRevocation` error and retry on the next fetch.
4. Mark cloud validation only after a fully verified response.

**Test:** unit tests:
- a revocation over the non-mTLS client is ignored;
- an `AddRevocation` failure returns an error;
- a null envelope over the non-mTLS client does not reset staleness.

### M5 — The mutual-TLS key is regenerated after any read error

**Status:** Confirmed · CONTROLLED (certificates)

**Where:** `data-plane/internal/appliancecert/cert.go:63-98` (`ensureMTLSKey`)

**Problem:** the key is loaded only when `os.ReadFile` succeeds. Any other error (permission denied after a restore or a chown, an I/O error) falls through, and a new key is generated over the existing file.

**Impact:** the installed client certificate no longer matches its key, so the appliance loses its mutual-TLS channel (assignment, licence, portal TLS) and has to obtain a new certificate.

**Fix:**
- Generate only when `errors.Is(err, os.ErrNotExist)`; return any other error.
- On load, refuse a key file that is group- or world-readable.

**Test:** unit test: an unreadable key file returns an error and leaves the file unchanged.

### M6 — The identity key is regenerated if `ed25519.key` goes missing

**Status:** Confirmed · CONTROLLED (identity)

**Where:** `data-plane/internal/identity/identity.go:200-218` (`load`), `:239-264` (`EnsureLocalKeypair`), `:52-65` (`LoadBound`)

**Problem:**
- `load` wraps a missing key file as `os.ErrNotExist`. When `identity.json` exists but the key does not, `LoadBound` reports "not registered", and `EnsureLocalKeypair` mints a new key and rewrites `identity.json` without the appliance id.
- `load` never checks that the key matches the stored `public_key`, or the key file's mode.
- There is no lock between concurrent first-time callers.
- The directory is not fsynced after the rename.

**Impact:** a lost key file turns an activated appliance into a new, unknown identity. Central refuses it as `hardware_reused`, and an operator has to retire and re-activate it.

**Fix:**
1. Distinguish "nothing exists" (first boot) from "identity.json exists but the key is missing or does not match" (damaged). The damaged case is an error that stops registration and is reported in the Admin Console. Never regenerate it silently.
2. Check that the key's public half equals `public_key` in `identity.json`.
3. Check the key file mode (0600).
4. Take a file lock (`flock`) on the identity directory while creating.
5. `fsync` the directory after the renames.

**Test:**
- With `identity.json` present and the key removed, `EnsureLocalKeypair` and `Register` return an error and write nothing.
- A mismatched key is refused.

### M7 — Calls to Central's HTTPS ingress have no timeout

**Status:** Confirmed

**Where:** `data-plane/internal/appliancecert/cert.go:132,586,688,700` (`http.DefaultClient`), `:589-616` (`signedDoWith`); called from `cmd/scd/main.go:1098,1128` with the service-lifetime context

**Problem:**
- The certificate bootstrap, the reconcile and the ingress CSR use `http.DefaultClient`, which has no timeout, and the context has no deadline.
- The response body is read without a limit and copied whole into the error text.

**Impact:** one stalled TCP or TLS connection (a firewall that drops packets silently is common on hotel networks) blocks that goroutine for good. Before the first certificate, the appliance never gets one. After it, the reconcile and rotation loop stops, and the certificate eventually expires.

**Fix:**
1. Use a dedicated `http.Client{Timeout: 15 * time.Second}` for ingress calls, as the mTLS client already does.
2. Wrap each call in `context.WithTimeout`.
3. Read bodies through `io.LimitReader(resp.Body, 1<<20)` and put at most the first 512 bytes in errors.

**Test:** unit test against an `httptest` server that never answers: the call returns within the timeout.

### M8 — The mutual-TLS trust anchor is whatever Central's response says

**Status:** Possible · CONTROLLED (trust roots) · **DECISION**

**Where:** `data-plane/internal/appliancecert/cert.go:463-500` (`installPEM`, `store`), `:640-700` (`Reconcile`)

**Problem:**
- `ca.crt`, the root set for every mutual-TLS call to Central, is written from the `ca_chain` field of the ingress response. That response is trusted only through the system roots.
- It is rewritten on every reconcile and rotation, and never compared with a root provisioned at install.
- The installed client certificate is checked only for matching its key: not for validity dates, client-auth usage, or the appliance id.

**Impact:** a wrong or hostile ingress response, or a Central bug, silently and permanently replaces the appliance's trust anchor for Central.

**Fix (needs the decision):**
1. Install `appliance-root-ca.crt` at provisioning; Central already publishes it, see `central-install.sh`.
2. Accept a `ca_chain` only if it chains to that pinned root.
3. Check the client certificate:
   - it is valid now;
   - it has `ExtKeyUsageClientAuth`;
   - it carries the appliance id in its URI SAN or CN.

**Test:** unit tests: a chain to another root is refused, a certificate for another appliance is refused, and the pinned root survives a reconcile.

### M9 — Certificate rotation switches first and verifies afterwards

**Status:** Confirmed

**Where:** `data-plane/internal/appliancecert/cert.go:325-368` (`rotateCert`), `:560-583` (`MaybeRotate`), `:307-318` (`installCollected`)

**Problem:**
- `rotateCert` stores and loads the new certificate, and clears the pending marker. Only then does `MaybeRotate` test it with an mTLS hello.
- If the hello fails, for example on a network blip, it rolls back to the old certificate. The marker is already cleared, so the next tick files another CSR.
- The comment says "verify-before-switch".
- The five-minute reconcile later installs the newer certificate, so the appliance recovers. But each flaky rotation costs extra CSRs and a window on a superseded certificate.

**Fix:**
1. Build a candidate client from the new pair and run the hello with it before calling `store`.
2. Clear the marker only after the hello succeeds.
3. Correct the comment.

**Test:** unit test with a fake Central whose hello fails once: no rollback churn, and one CSR.

### M10 — The writer-boundary startup check accepts triggers that never fire

**Status:** Confirmed (trigger mode) · Possible (role checks) · CONTROLLED (guest authentication, entitlement core)

**Where:** `data-plane/internal/writerguard/writerguard.go:203-245` (`verifyFunction`), `:262-280` (`verifyTrigger`)

**Problem:**
- `verifyTrigger` accepts any enabled state except `'D'`. A trigger set to `'R'` fires only in replica mode, so it does nothing in normal operation, yet passes the check.
- It does not check the trigger type (BEFORE, ROW, INSERT/UPDATE/DELETE).
- `verifyFunction` checks only that the service role is not a member of the function owner's role. It does not check whether the role owns the guarded tables, which would let it disable the trigger, or is a superuser or `BYPASSRLS`.

**Impact:** scd can report "boundary in force" at startup while raw writes to entitlement, session and stay tables are accepted.

**Fix:**
1. Require `tgenabled IN ('O','A')`.
2. Decode `tgtype`: BEFORE, ROW, and INSERT, UPDATE and DELETE all set.
3. Fail when `current_user` owns any guarded table, or has `rolsuper`, `rolbypassrls` or `rolreplication`.

**Test:** a database test (Phase 3 harness):
- a trigger switched to `ENABLE REPLICA` makes `Verify` fail;
- an AFTER trigger makes it fail;
- a service role that owns a guarded table makes it fail.

### M11 — Guest code requests can exceed their limits, and anyone can block a guest from getting a code

**Status:** Confirmed · CONTROLLED (guest authentication)

**Where:** `data-plane/internal/otp/otp.go:75-112` (`Issue`)

**Problem:**
- The 60-second cooldown, the 5-per-hour cap per destination and the 20-per-hour cap per address are counted with a `SELECT`, then a separate `INSERT`, with no transaction or lock. Parallel requests all pass.
- The per-destination cap counts requests from anyone, whatever device asked.

**Impact:**
- A burst of parallel requests sends many messages, so SMS cost and inbox flooding go past the limit.
- Anyone who knows a guest's phone number or email can use up that guest's five codes an hour, so the real guest cannot get one.

**Fix:**
1. Run the checks and the insert in one transaction under `pg_advisory_xact_lock(hashtext(tenant||channel||destination))`. Take a second lock for the address cap.
2. Count the per-destination cap per (destination, device), keeping a higher destination-wide ceiling against flooding.
3. Normalise `channel` to lower case like `destination`.

**Test:** a database test:
- 10 parallel `Issue` calls for one destination produce one code;
- requests from device A do not stop device B getting a code.

### M12 — The trust registry falls back to the previous registry, which may still trust a revoked key

**Status:** Confirmed · CONTROLLED (assignment trust)

**Where:** `data-plane/internal/assignment/registry_store.go:63-77` (`Trusted`)

**Problem:**
- `Trusted()` uses the previous registry whenever the current one fails verification. The current one fails as "not yet valid" whenever the appliance clock is more than 5 minutes behind its issue time: Central sets `NotBefore` to issue time − 5 minutes.
- The previous registry is the one before the latest key change, so it can still list a key that the current registry revoked.

**Impact:** an appliance whose clock drifts back (an RTC reset, an NTP glitch) trusts a revoked, possibly compromised, assignment-signing key again.

**Fix:**
1. Verify an already-adopted current registry without the `NotBefore` check. `NotBefore` matters only at adoption.
2. Fall back to the previous registry only when the current file cannot be read or its signature fails, never for a validity-window reason.
3. Never fall back to a registry with a lower version than the highest ever adopted. Store that number separately.

**Test:** unit test: with the clock 1 hour behind, `Trusted()` returns the current registry; a revoked key in the current registry is never trusted through the previous one.

### M13 — The portal private key and certificate are installed in two separate steps

**Status:** Confirmed

**Where:** `data-plane/internal/portaltls/portaltls.go:436-466` (`Install`)

**Problem:**
- The new key is renamed into place first, then the certificate.
- A crash, or a failed certificate write, between the two leaves a new key with the old certificate.
- `portald` keeps its in-memory pair, but after a restart the pair does not match, and the guest portal stops serving HTTPS.

**Fix:** pick one:
- write the key and the chain into a new versioned directory (`<host>/<serial>/`) and switch a `current` symlink atomically; or
- store key and chain in one file.

If the certificate write fails after the key, restore the previous key. Leave the pending key until both are installed.

**Test:** unit test with a failing certificate write: the previously installed pair is still on disk and matches.

### M14 — One device can pause room sign-in for a whole guest network

**Status:** Confirmed (by design) · CONTROLLED (guest authentication) · **DECISION**

**Where:** `data-plane/internal/signinattempt/network_pause.go:34-36` (defaults 30 wrong attempts in 600 s pause 900 s; minimum threshold 10); enforced by `iam_v2.guest_signin_note_network_failure`

**Problem:** the network-wide counter exists because the per-device counter is defeated by changing MAC address. The consequence is that anyone on a guest network who submits 30 wrong room sign-ins in 10 minutes stops every guest on that network from signing in by room for 15 minutes. They can repeat it.

**Decision needed:** is a network-wide pause acceptable?

**Recommended:**
- Keep the pause, but exempt devices that already signed in successfully on that network in the last N days.
- Raise the default threshold.
- Alert staff on the first pause.

**Test:** follows the decision.

### M15 — Assignment, registry and certificate files are not crash-safe

**Status:** Confirmed · CONTROLLED (assignment)

**Where:**
- `data-plane/internal/assignment/store.go:68-85` (`save`)
- `data-plane/internal/assignment/registry_store.go:124-140` (`save`)
- `data-plane/internal/appliancecert/cert.go:492-500` (`store`: certificate and CA written separately)

**Problem:**
- These writes rename a temp file without syncing it.
- The certificate and its CA bundle are two separate writes, so a crash between them leaves a mismatched pair.

**Impact:** after a power cut the assignment can be zero-length. The appliance then refuses to authorise anybody (fail closed) until Central answers, so a hotel with no route to Central stays dark.

**Fix:**
1. Use the same write helper as H1: unique temp file, write, `fsync`, rename, `fsync` the directory.
2. Keep a `.prev` copy of the assignment and fall back to it if the current file is damaged.
3. Write the certificate and the CA as one file, or with the versioned-directory approach of M13.

**Test:** unit tests: a zero-length `assignment.json` with a valid `.prev` resolves from `.prev`; a failed CA write leaves the old pair.

---

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | There is no persistent assignment anti-rollback: `Resolve` accepts the stored document against version − 1, so an older signed document written to disk (by root, an old image or a restored backup) is accepted again. | `assignment/resolve.go:148-150` | Keep a separate "highest accepted version" file, synced, and refuse a stored document below it. |
| L2 | A key file left short by a failed create blocks every later bootstrap ("too short") until someone deletes it by hand. | `localkeys/localkeys.go:52-95` (`createExclKey`) | Write to a temp file and `link()` it into place (atomic and no-overwrite); on failure remove the temp file. |
| L3 | Key-length rules disagree: `localkeys` accepts 32 bytes or more; `sealbox` and `signinattempt` require exactly 32; `otpkey` and `throttle` accept 16. A 33-byte key (a trailing newline) passes startup and fails at first use. | `localkeys.go`, `sealbox.go:27-34`, `signinattempt/seal.go:84-90`, `otpkey.go:25-45`, `throttle.go:77-90` | `localkeys` refuses any key that is not exactly 32 bytes; the others check 32. |
| L4 | `LoadExistingKey` reads the file before the symlink and mode check, so a symlink is followed first. There is no owner check. | `localkeys/localkeys.go:97-112` | `Lstat` first, then open with `O_NOFOLLOW`; check that the owner is root (or the service user). |
| L5 | `LoadGenerationKeys` ignores the number-parse error: `otp_hmac_01.key` and `otp_hmac_1.key` both become generation 1, and the later file wins. | `localkeys/localkeys.go:140-170` | Refuse a duplicate generation or a generation below 1. |
| L6 | The hardware id falls back to a constant ("stayconnect-unknown-anchor"), so every appliance without DMI data, machine-id or NIC gets the same serial. Cloned VMs share serial and fingerprint. `isBadDMI` misses all-zero UUIDs and "Unknown". `ethtool` and `ip` run without a timeout. | `hwid/hwid.go:61-118,138,183,245-260` | Refuse to register with the constant anchor (report it in the Admin Console); add the missing placeholder values; run commands with `exec.CommandContext` and a 5 s timeout. |
| L7 | Password verification accepts trailing junk in the parameter string, has no password-length cap in `Hash`/`Verify` (only in `CheckSelfService`), and has no concurrency bound (64 MiB per check). | `passwordhash/passwordhash.go:53-77` | Parse the parameters with exact matching; cap input at 1024 bytes in `Hash` and `Verify`; bound concurrent checks with a semaphore as Central does (`control-plane/internal/auth/password.go`). Whether the guest sign-in rate limits already prevent memory exhaustion is checked in Phase 4. |
| L8 | `throttle` silently drops a rule with an empty value or a zero limit; if all are dropped, `Allow` allows and counts nothing. Cleanup errors are ignored. The reported reason can differ from the longest retry. | `throttle/throttle.go:118-160,172-260,271-285` | Return an error for a non-endpoint rule with an empty value or a limit ≤ 0; log cleanup failures; take the reason from the rule with the longest retry. |
| L9 | `authctx` validates trimmed ids but binds the untrimmed ones, so a padded id gives a raw database cast error instead of the uniform refusal. | `authctx/authctx.go:45-60` and the SQL binds | Trim into local variables once and use them for both the check and the query. |
| L10 | A nil protection store or a device with no MAC silently disables the per-device and network sign-in limits. | `signinattempt/protection.go` (`Check`, `NoteFailure`), `network_pause.go` (`CheckNetwork`) | Log an error when the store is nil; confirm in Phase 4 that a submission without a resolved MAC is always refused. |
| L11 | The licence verifier panics on a wrong-length public key (`NewVerifier`, `AddKey`). `GenerateVendorKey` overwrites an existing key. | `license/verify.go:23-31`, `license/sign.go:31-40` | Refuse keys that are not 32 bytes; write with `O_CREATE|O_EXCL`. |
| L12 | `Document.Validate` does not bound `GracePeriodDays` (overflow in `Evaluate`) or check `ValidFrom` ≤ `ValidUntil`. | `license/doc.go:149-171` | Bound grace days 0–365 and check the dates; Central already validates, so this is a second line. |
| L13 | `Store.Load` turns every read error into "no licence", and `licstate` does not log it. | `license/store.go:162-168` | Return `ErrNoLicense` only for "not exist"; return other errors and log them. |
| L14 | `licstate` data races: `m.applianceID` and `m.local` are read and written without the lock. Two `Load` calls can interleave. Every minute it logs at info level and rewrites 14 limit rows even when nothing changed. | `licstate/licstate.go:136-186,276-310,354-410,417` | Take the lock for those fields; run `Load` under a separate mutex; skip the log and the limit rewrite when the evaluation is unchanged. |
| L15 | Offline packages are checked against the wall clock only (no high-water); `IssuedAt` is not checked. Without a database the single-use ledger is skipped, and in the import path the ledger row is written before the licence installs, so a failed install burns the package. | `offline/offline.go` (`AcceptFor`); `cmd/scd/offline_import.go:42-60` | Check against the licence high-water clock and refuse a future `IssuedAt`; refuse import without a database; record the ledger row in the same step as a successful install. (The `cmd/scd` part overlaps Phase 4.) |
| L16 | "Already assigned" is decided from `TenantID != ""`, so an appliance holding a signed revoked or decommissioned assignment can be first-activated from a file. A Central-signed package is still required. | callers of `activation.AcceptForFirstActivation` (`cmd/scd/offline_first_activation.go`) | Decide from the assignment state (`assignment.Grants` or `Clears`), not the tenant field. (Overlaps Phase 4.) |
| L17 | Without the signed registry file, or with an unreadable root key, the unsigned plain trust file is used, with only a log line. | `assignment/resolve.go:89-111` | Once a signed registry has ever been adopted, refuse the plain file (a one-way marker); raise an Admin Console alert when the plain file is in use. |
| L18 | `NoteRefresh` reads, changes and rewrites the assignment record on every poll, without a lock, and can overwrite a concurrent adoption. | `assignment/store.go:109-129` | Keep refresh bookkeeping in a separate small file, or lock the store. |
| L19 | Portal TLS: temporary files with key bytes are left behind after a crash. If the key group is missing, a successful install writes a key `portald` cannot read, with only a warning. `Inspect` does not check that the certificate names the host. | `portaltls/portaltls.go:107-218,468-492` | Remove stale `.tmp-*` files at start; fail the install (and alert) when the group is missing; check the host name in `Inspect`. |
| L20 | Error bodies from Central are read with one `Read` call (possibly truncated); success bodies are decoded without a limit; `appliancecert` puts the whole body in its error. | `identity/identity.go:135-147`, `appliancecert/cert.go:611-613` | Use `io.LimitReader` everywhere (1 MiB), with at most 512 bytes in errors. |
| L21 | `backupcrypt.ParseIdentity` ignores `Scanner` errors (a line over 64 KB gives "no key found"). The check code is stored as an unsalted SHA-256 of about 39 bits of entropy. | `backupcrypt/backupcrypt.go:55-68,112-124` | Check `sc.Err()`; hash the check code with HMAC under an appliance key, or accept that it is not a secret and say so. |
| L22 | `otp`: every code request runs a cleanup `DELETE` on the hot path. `Verify` (no device check) is exported but unused. `Verified.TemplateID` is never filled. | `otp/otp.go:55-66,182-185,171-173` | Move the cleanup to the periodic job; unexport `Verify`; remove `TemplateID`. |
| L23 | Code lifetime (10 min), attempts (5), cooldown (60 s) and hourly caps (5 per destination, 20 per address) are hard-coded. Rule §0C asks for settings where a hotel administrator may need to change them. | `otp/otp.go:20-26` | **DECISION.** Recommended: make the hourly caps and the lifetime site settings; keep the attempt count fixed. |
| L24 | A 200 response with a null envelope resets cloud staleness, even when it came over the non-mTLS client. | `licstate/licstate.go:457-465` | Part of M4: mark validation only after a verified mTLS response. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code:** `appliancecert.(*Manager).signedDo`; `keybootstrap.BootstrapVoucherHMACKey` and `VoucherHMACKeyFile` (no caller, and the comment says scd needs the key, which it does not); `otp.Verify`; `assignment.(*RegistryStore).recPath` (two identical branches); `applianceauth.Sign` (deprecated); `hwid.Detect`'s discarded `anchor`. | Delete them. |
| CC2 | **Duplicates:** two identical atomic-write helpers (`appliancecert.writeAtomic`, `identity.writeFileAtomic`) plus a third variant in `localkeys`; two AEAD seal implementations (`sealbox`, `signinattempt/seal.go`) with the same associated-data builder; a second Crockford alphabet in `hwid`; seven repeated blocks in `cmd/keybootstrap/main.go`; three copies of the begin, commit and rollback wrapper in `authctx`. | One write helper (used by H1, M15); `signinattempt` uses `sealbox`; `hwid` uses `crockford`; a table in `keybootstrap`; `pgx.BeginFunc` in `authctx`. |
| CC3 | **Mirrored code:** `assignment`, `offline`, `activation`, `totp` and `applianceauth` are copied line for line in `control-plane/internal`, with the same signed byte layout. A one-sided change silently breaks verification. | Move them into a shared module like `license/`, used by both sides, keeping the golden-bytes tests; or at least run one golden-vector test file in both modules. |
| CC4 | **Inconsistent key-id encodings:** `activation` uses base64url, the others hex. | Keep it for compatibility, but document it next to each function, and add the encoding to the golden tests. |
| CC5 | **Misleading comments:** `cmd/scd/offline_first_activation.go` says `assignment.Store.Adopt` verifies the document (it does not); `authctx` says a query "holds FOR UPDATE OF st" (it has none; the lock is taken by `lock_stay` first); `appliancecert` says "verify-before-switch" (see M9). `identity.Register` passes the key id as the token's appliance id. | Correct the comments; rename `Adopt` to `AdoptVerified`; pass "" or a named constant for the registration issuer. |
| CC6 | **Startup and logging noise:** `writerguard` runs the same catalog query for `begin_controlled_operation(text)` eleven times. `licstate.applianceBound` only logs. | Check each function once; make the binding a refusal (M3). |

---

## Fix batches

A batch is done when its tests pass and the package tests still pass. Almost every batch is CONTROLLED (named in the last column) and needs that authorization.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| **B1** Licence store integrity | H1, M1, M2, L11, L13 | `license/store.go`, `verify.go`, `sign.go` | Licensing. **Highest priority.** `license/` is shared with Central (signing); `store.go` is appliance-only. Run Central's tests too. |
| **B2** Licence acceptance rules | M3, M4, L12, L14, L24 | `license/binding.go`, `doc.go`, `data-plane/internal/licstate/licstate.go` | Licensing. M3 needs the decision on unbound licences; M4's signed revocations need a protocol decision with Central. Run after B1 (shares `license/`). |
| **B3** Appliance certificate client | M5, M7, M8, M9, M15 (certificate part), L20 (certificate part) | `data-plane/internal/appliancecert/cert.go` | Certificates and trust. M8 needs the decision on pinning the root. |
| **B4** Appliance identity | M6, L20 (identity part), L6 | `data-plane/internal/identity/identity.go`, `hwid/hwid.go` | Identity. |
| **B5** Assignment and registry | M12, M15 (assignment part), L1, L17, L18 | `data-plane/internal/assignment/*` | Assignment trust. |
| **B6** Offline and first activation | L15, L16 | `data-plane/internal/offline/offline.go`, `activation/activation.go`, `cmd/scd/offline_import.go`, `cmd/scd/offline_first_activation.go` | Licensing and assignment. Touches `cmd/scd`: coordinate with Phase 4 fixes. Run after B5. |
| **B7** Portal certificate install | M13, L19 | `data-plane/internal/portaltls/portaltls.go` | Certificates. |
| **B8** Writer boundary check | M10, CC6 (`writerguard`) | `data-plane/internal/writerguard/writerguard.go` | Guest authentication core. Needs the Phase 3 database test harness. |
| **B9** Guest code requests | M11, L22, (L23 after decision) | `data-plane/internal/otp/otp.go` | Guest authentication. |
| **B10** Local keys | L2, L3, L4, L5 | `data-plane/internal/localkeys/localkeys.go`, `sealbox.go`, `signinattempt/seal.go`, `otpkey.go`, `throttle.go` | Security. L3 touches several packages: do it as one change. |
| **B11** Small hardening | L7, L8, L9, L10, L21 | `passwordhash`, `throttle`, `authctx`, `signinattempt/protection.go`, `backupcrypt` | Guest authentication. Run after B10 (shares `throttle.go`, `signinattempt`). |
| **B12** Shared code between Central and appliance | CC3, CC4 | new shared module; `control-plane/internal/{assignment,offline,activation,totp,applianceauth}`; `data-plane/internal/` equivalents | Large refactor across both modules; do last, when no other backend batch is open, with both modules' full tests. |
| **B13** Clean code | CC1, CC2, CC5, CC6 | various | After the behaviour batches. |

**Waiting for a decision**

| Item | Decision | Recommended |
|---|---|---|
| M3 | May unbound "legacy" licences still be accepted? | No, if no deployed appliance uses one. |
| M4 | Should revocations become a vendor-signed document? | Yes; until then, accept them over mTLS only. |
| M8 | Pin Central's appliance root CA on the appliance at install? | Yes. |
| M14 | Keep the network-wide room sign-in pause? | Keep it, exempt recently successful devices, alert staff. |
| L23 | Which code limits become site settings? | Hourly caps and lifetime. |

## Checked and rejected

- **Ignored `crypto/rand` errors** (several packages). Since Go 1.24 they never happen.
- **"Certificate rotation strands the appliance on a rolled-back certificate."** The five-minute reconcile installs the newer certificate Central holds. What remains is recorded as M9.
- **The PMS auth-context consumption race.** The context is flipped with `consumed_at IS NULL AND expires_at > now()` after the Stay lock, so exactly one caller wins.

## Not verified

- **Behaviour against a real database.** The writer guard (M10), the code-request race (M11) and the sign-in protection functions need the Phase 3 database harness.
- **Race-detector runs.** These need a Linux build host (L14, H1 concurrency).
- **How `cmd/scd` resolves a device MAC** (L10) and whether its rate limits bound password-hash memory (L7) are Phase 4 questions.
