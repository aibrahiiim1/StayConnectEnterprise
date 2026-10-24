# Backend audit — Phase 1: Central (ctrlapi)

| | |
|---|---|
| Audited commit | `008a4cb3`, re-checked against `9ffa981b` (PR #229, which adds only a Central test) |
| Scope | `control-plane/`: cmd/ctrlapi and internal/*, about 16,500 lines of non-test Go |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |

## How to use this file

1. **Each finding** has a problem, its impact, the recommended fix and the test that proves the fix.
2. **Fixes are grouped into small batches** (end of the file). Do one batch at a time, on its own branch, and finish its tests before starting the next.
3. **Batches that touch the same file** say so. Run them one after another, never in parallel.
4. **Findings marked DECISION** wait for a Product Owner decision. Do not fix them until it is recorded.
5. **Findings marked CONTROLLED** touch an area CLAUDE.md §0 controls (database schema, certificates and trust, deployment). They need that authorization.
6. **Status of a finding:**
   - **Confirmed**: the code path was traced.
   - **Possible**: the code suggests the problem, but it needs a test or deployment facts to prove.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 2 (+ the Go toolchain, Phase 0 P0-2) |
| Medium | 17 |
| Low | 22 |
| Clean code | 9 |

What holds up well (keep it this way when fixing):
- **Permission model.** Permissions are checked on the route, and customer scope in the handler. No customer-scoped user can read or change another customer's data, and no role can grant itself a platform role.
- **Appliance request signatures.** They bind method, path, body hash, audience and key id within 60 s, and replays are blocked.
- **Sessions.** They are re-authorised against the database on every request.
- **Two-step codes.** They are counted inside the checking transaction, and never accepted twice.
- **Database errors.** They never reach an operator-visible message (`publicError`).
- **Customer and site deletes.** Foreign keys (migration 0037) protect them from races.

## Endpoint inventory

Every route in `internal/http/router.go`, `internal/api/routes.go`, `internal/api/portal_tls.go`, `internal/api/appliance_mtls.go` and `internal/http/mtls.go` was checked.

Column legend:
- **Session**: the console cookie.
- **Step-up**: the password was re-entered in the last 5 minutes.
- **Scope**: the handler pins the request to the caller's customer.
- **Signed**: an Ed25519-signed appliance request.

**Public listener (443, behind Caddy)**

| Route | Who may call |
|---|---|
| GET /healthz | anyone |
| GET /readyz | anyone (returns version and sign-in state) |
| GET /metrics | loopback only |
| POST /v1/auth/login, POST /v1/auth/login/code | anyone, 20/min per address, account lockout |
| POST /v1/auth/logout | anyone (same-origin check) |
| POST /v1/auth/reauth | session, 20/min |
| GET /v1/auth/whoami, GET /v1/auth/mfa | session |
| POST /v1/auth/mfa/{setup,confirm,disable,recovery-codes} | session + step-up (+ a code for disable and recovery codes) |
| POST /v1/appliances/register | anyone, self-signed request, 30/min per address |
| GET /v1/appliance/hello, /license, /certificate; POST /v1/appliance/offline-reconcile, /csr | signed, 240/min |

**Operator API (`/cloud/v1`, session on every route)**

| Route | Permission |
|---|---|
| GET overview, customers, customers/{id}, customers/{id}/sites, customers/{id}/users, appliances, appliances/{id}, licenses, security-alerts, audit | scope |
| GET modules | session only (static catalogue) |
| POST customers; PATCH customers/{id}; POST customers/{id}/archive, /restore | customers.manage |
| DELETE customers/{id} | customers.manage + step-up |
| POST customers/{id}/sites; PATCH sites/{id}; POST sites/{id}/archive, /restore | sites.manage, or the customer's own sites.manage |
| DELETE sites/{id} | the same + step-up |
| POST/PATCH/DELETE customers/{id}/users[/{uid}]; POST …/{uid}/mfa-reset | users.manage, or own customer.users.manage, + step-up |
| POST appliances/{id}/{activate,move,retire,replace,rebind-wan-mac,reissue-certificate}; DELETE appliances/{id}; POST appliances/{id}/offline-activation-package | appliances.manage + step-up |
| POST offline-activation/requests | appliances.manage (no step-up, see M16) |
| POST appliances/{id}/license; POST licenses/{id}/{suspend,resume,revoke}; POST appliances/{id}/offline-license | licenses.manage + step-up |
| PATCH security-alerts/{id} | security.manage |
| GET trust, team, backup-health, settings/security, settings/retention, portal-tls/{settings,fleet,events} | fleet.view |
| POST/PATCH/DELETE team[/{id}]; POST team/{id}/password, /mfa-reset; POST users/{id}/data-export; PATCH settings/security, settings/retention | team.manage + step-up |
| PUT portal-tls/settings, /dns-token; POST portal-tls/test, /caa, /appliances/{id}/revoke | security.manage + step-up |

**Appliance listener (9443, mutual TLS, reached directly)**

| Route | Who may call |
|---|---|
| GET /healthz | anyone |
| GET /v1/appliance/assignment | client certificate only (strict: the current certificate) |
| GET hello, license, assignment-registry, certificate; POST offline-reconcile, assignment/ack, csr, portal-tls/sync, portal-tls/orders; GET portal-tls/orders/{id} | certificate + signed request, same appliance id, certificate current or superseded < 24 h |

**Licensing-only rule (CLAUDE.md §0E):** the appliance-facing routes are exactly the licensing set the rule lists.

---

## High

### H1 — A missing CA certificate file makes Central mint a new root CA

**Severity, status:** High · Confirmed · CONTROLLED (certificates and trust)

**Where:** `internal/pki/ca.go:50-87` (`LoadCAChain`), `:106-142` (`loadOrCreateKey`); `cmd/ctrlapi/main.go:236-264`.

**Problem:**
- `LoadCAChain` uses the stored CA only when both `root-ca.crt` and `intermediate-ca.crt` can be read.
- Otherwise it calls `loadOrCreateKey(rootKeyPath)`. In production the root private key is not on the host (CA ceremony: only an encrypted copy exists, offline), so a brand-new root key is generated, a new intermediate is signed, and both are written; the write errors are ignored.
- A missing `intermediate-ca.key` is silently regenerated the same way.
- `main.go` then overwrites `appliance_ca_versions` version 0 and the intermediate row with `ON CONFLICT DO UPDATE`, ignoring errors.
- The comment above it ("Absent key → PKI routes and the mTLS listener stay disabled") is the opposite of what happens.

**Impact:**
- **Fleet outage.** One missing `.crt` after a restore or host move gives Central a different root. Every appliance certificate stops verifying, so the mutual-TLS channel (assignment, licence, certificate rotation, portal TLS) refuses the whole fleet.
- **Lost record.** The database's record of the real root is replaced.

**Recommended fix:**
1. `serve` must never create CA keys or certificates. Replace `LoadCAChain` in `serve` with a `LoadCA` that needs `root-ca.crt`, `intermediate-ca.crt` and `intermediate-ca.key`, and returns an error if any is missing.
2. In `LoadCA`, check:
   - the intermediate is signed by the root (`intCert.CheckSignatureFrom(rootCert)`);
   - the intermediate's public key equals the key file's;
   - the intermediate has not expired.
3. Move first-time creation into an explicit command (for example `ctrlapi init-ca`). The command refuses if any CA file exists, or if `appliance_ca_versions` already has a row.
4. Update `deploy/scripts/central-install.sh` so a fresh install calls the new command. Restores and upgrades never do.
5. In `main.go`:
   - If the CA cannot be loaded, log an error and leave PKI disabled. `/readyz` should then report it.
   - Insert `appliance_ca_versions` rows with `ON CONFLICT DO NOTHING`.
   - If a stored row differs from the loaded certificate, refuse to start the PKI.
6. Correct the comment.

**Test:**
- Unit tests in `internal/pki`:
  - a missing root `.crt`, intermediate `.crt` or intermediate key each returns an error and creates no file;
  - a mismatched key and certificate is refused;
  - `init-ca` refuses when files exist.
- An integration test: a different root certificate already stored in `appliance_ca_versions` stops the PKI from starting and leaves the row unchanged.

### H2 — An appliance is activated on facts anyone can know

**Severity, status:** High · Confirmed path (exploitation Possible) · **DECISION**

**Where:**
- `internal/api/register.go:39-261` (registration) and `:267-349` (`replaceWaitingIdentity`).
- The procedure in `docs/APPLIANCE_ACTIVATION_AND_LICENSING.md`, step 1.

**Problem:**
- **Registration.** It is unauthenticated, trust on first use.
- **Choosing the box.** The operator picks the waiting appliance by its serial (and the WAN MAC, under Advanced). Neither is secret, and the appliance never shows its identity-key fingerprint.
- **Squatting a serial.** An attacker who knows a box's serial registers it first with their own key. The real box is then refused, and the operator sees one waiting row with the right serial.
- **Taking over a waiting row.** If the real box registered first, an attacker who also knows its hardware fingerprint can replace that row's identity key (`replaceWaitingIdentity` accepts any request whose fingerprint matches).

**Impact:** the attacker's machine receives the customer's signed assignment, licence and a client certificate. Through `portal-tls/orders` it can also obtain a publicly trusted certificate for the hotel's guest-portal hostname.

**Recommended fix (needs the decision):**
1. **The appliance shows a code.** It displays a short activation code on its own Admin Console screen, under System → Appliance & licence, while it waits. The code is derived from its identity public key, for example the first 8 base32 characters of the key's SHA-256.
2. **Central asks for it.** The Activate dialog asks for that code. `activate` recomputes it from the stored public key and refuses with 409 if it does not match.
3. **Offline activation does the same.** The request file already carries the public key, so the console asks for the code shown on the box.
4. **Stop key swaps on waiting rows.** `replaceWaitingIdentity` should no longer swap the key on a fingerprint match alone. Either refuse every key change on a waiting row, so the operator deletes the row, or require the new code.

**Scope:** this touches Central (`activate`, `register.go`), the Central console (Activate dialog) and the appliance Admin Console (a display of the code). Do it after Phase 2, which covers the appliance identity code.

**Test:**
- Unit test: the code derivation matches on both sides.
- Integration test: activating with a wrong or missing code is refused; with the right one it succeeds.
- A waiting row's key cannot be replaced by a matching fingerprint alone.

---

## Medium

### M1 — A rate-limit key can lose its expiry and block an address for good

**Status:** Confirmed

**Where:** `internal/api/ratelimit.go:35-44`

**Problem:** `INCR` and `EXPIRE` are two calls, and the `EXPIRE` error is ignored. If it fails after the first `INCR` (a Redis blip, or a restart between the calls), the key has no time-to-live.

**Impact:** that address (a whole hotel or office behind one NAT) is refused sign-in, re-authentication and registration until someone deletes the key in Redis by hand.

**Fix:** use one atomic round trip, a `MULTI`/pipeline of `INCR` + `EXPIRE key window NX` (Redis 7). Treat an `EXPIRE` error like an `INCR` error, so it fails open as the limiter already does.

**Test:** a unit test with a fake counter whose expire fails, showing the next window starts fresh. Plus an integration test against Redis checking that the key has a TTL after the first hit.

### M2 — "Reissue certificate" with a waiting CSR leaves the old certificate usable for 24 h

**Status:** Confirmed

**Where:** `internal/api/appliance_ops.go:608-654`, `certificates.go:136`, `appliance_mtls.go:102`

**Problem:**
- When a CSR is waiting, reissue signs it. The old certificate is only marked `superseded`, and `mtlsCertBinding` accepts superseded certificates for `SupersededOverlap` (24 h).
- The other branch of the same action (no CSR waiting) revokes at once.

**Impact:** an operator who reissues because a key leaked believes the old certificate is dead. It keeps working on every mutual-TLS route except the assignment fetch for a day.

**Fix:** in `reissueCertificate`, after signing the waiting CSR, revoke every certificate of the appliance other than the new one:
- insert into `appliance_certificate_revocations`;
- set its status to `revoked`;

all in the same transaction as the issuance. Pass an "operator reissue" flag into `issueCertForAppliance` (or a separate function), so automatic rotation keeps its overlap.

**Test:** integration test: after reissue with a pending CSR, a request with the old certificate gets 403 on `/v1/appliance/hello`.

### M3 — A failure to re-sign the trust registry is swallowed

**Status:** Confirmed

**Where:** `internal/api/assignment_keys.go:45-51`; the callers `KeyToVerifyOnly` and `KeyRevoke`; `cmd/ctrlapi/main.go:323-335`

**Problem:** `resignRegistry` ignores `Rebuild`'s error, and does nothing at all when the registry root key is not loaded. The key-state change is still committed, audited and reported as success.

**Impact:** after an emergency revocation of a signing key, appliances keep the old registry, which still trusts the revoked key, and nobody is told.

**Fix:**
1. `resignRegistry` returns an error. It also returns an error when the registry root key is missing.
2. `KeyToVerifyOnly` and `KeyRevoke` re-sign before reporting success. On failure, return the error. Either roll the state change back (one transaction), or keep the state and report that the registry is not yet re-signed, with the CLI exiting non-zero.
3. Add a startup check: if the newest signed registry is older than the newest key-state change, log an error and re-sign.

**Test:** unit test: with no root key, revoke returns an error. Integration test: a revoke without a re-signed registry is not reported as done.

### M4 — Customer users see Central staff emails and internal reasons

**Status:** Confirmed · **DECISION** (recommended: hide them)

**Where:** `internal/api/fleet.go:295-315` (`applianceEvents`, used by `GET /cloud/v1/appliances/{id}`)

**Problem:** the history joins `operators` and returns the operator's email and the free-text reason of every audited action, to every scope, including a customer `viewer`.

**Impact:** personal data about Central staff, and internal notes (for example retirement reasons), are shown to hotel users.

**Fix:** pass the scope into `applianceEvents`. When the caller is not platform scope:
- show `actor` as "OneGate support" when the actor is a platform operator;
- drop `reason`;
- keep the action and the time.

**Test:** API test with a customer viewer session: no platform operator email and no reason in the response. A platform session still sees both.

### M5 — The customer audit view shows platform staff ids and IP addresses

**Status:** Confirmed · **DECISION** (recommended: hide them)

**Where:** `internal/api/system.go:230-304` (`listAudit`)

**Problem:** platform actions taken on a customer are stored with that customer's id. `listAudit` returns `actor_id`, `ip` and the whole `payload` to that customer's own users.

**Fix:** for a non-platform scope, on rows whose actor is a platform operator:
- set `actor_id`, `ip` and `user_agent` to null;
- remove free-text fields (`reason`, `note`) from the payload.

Keep everything for platform scope.

**Test:** same shape as M4.

### M6 — Anyone who knows the password can lock the account's code step for up to 24 h

**Status:** Confirmed · CONTROLLED (needs a database migration)

**Where:** `internal/auth/mfa.go:197-212` (`failTx`)

**Problem:** wrong codes are counted on the account's single `operator_mfa` row, not per account and address. The password lockout was deliberately made per address (`signin_throttle.go`) to stop exactly this.

**Impact:** a third party with only the password can keep the real user's second step locked for `signin_lock_minutes` (up to 1440).

**Fix:**
1. Count wrong codes per (operator, client address) in a new table, like `operator_signin_throttle`, and lock that pair only.
2. Keep an account-wide ceiling as a backstop, for example 4× the per-address limit, so many addresses cannot brute-force the code.
3. The counting must stay inside the checking transaction (see the comment on `failTx`).

**Test:** integration test: failures from address A lock A but not B, and the account-wide ceiling still locks.

### M7 — WAN MAC rebind accepts any string and is not atomic

**Status:** Confirmed

**Where:** `internal/api/appliance_ops.go:534-598`

**Problem:**
- `new_mac` is only lower-cased, yet it is signed into the licence as the hardware binding.
- The MAC update is committed, then the licence is re-signed. On failure a compensating update runs with its error ignored.
- A concurrent registration refresh can write `wan_mac` in between.

**Impact:** a typo makes the appliance reject its own licence. A half-failed rebind leaves a MAC with no matching licence.

**Fix:**
1. Validate with `net.ParseMAC` and store `mac.String()`.
2. Do the MAC update and the re-issue in one transaction, locking the appliance row `FOR UPDATE`. This needs `Lic.ReissueTx(ctx, tx, …)`.
3. Re-check `lifecycle_state` and retirement under the lock, as `move` does.

**Test:** unit test for MAC validation; integration test that a failed re-issue leaves `wan_mac` unchanged.

### M8 — Deleting an appliance runs irreversible steps outside one transaction

**Status:** Confirmed (narrow window)

**Where:** `internal/api/appliance_ops.go:682-771`

**Problem:**
- Certificate revocation, licence revocation and the portal hostname and certificate release (external DNS and ACME calls) each commit separately.
- They all run under a 10 s deadline, and only the final DELETE re-checks the state.

**Impact:** if an activation lands between the unlocked check and the DELETE, a just-activated appliance loses its certificate, licence and hostname but is not deleted.

**Fix:**
1. Start with a transaction that locks the appliance row `FOR UPDATE` and re-checks that it is waiting or retired.
2. Inside the same transaction, mark it `deleting`, using a status flag or `appliance_terminal_delivery` (if a new column is needed, it is a migration, CONTROLLED). Activation and move must refuse a `deleting` appliance.
3. Then run the external release with its own longer deadline.
4. Then the final delete transaction.
5. If the release fails, clear the flag.
6. Stop returning `err.Error()` from `PortalTLS` to the client (`:711`, `:728`). Log it and return a fixed message.

**Test:** integration test: activation of an appliance being deleted is refused, and a release failure leaves the appliance intact and not flagged.

### M9 — No request-body size limits

**Status:** Confirmed

**Where:** `internal/http/auth_handlers.go:113` (login) and `:177` (reauth); `internal/api/common.go:131-139` (`DecodeJSON`)

**Problem:** sign-in and re-authentication decode an unbounded body. `DecodeJSON`, used by every operator write, has no `MaxBytesReader`. Names, reasons, passwords and emails have no maximum length, and site and customer names are signed into assignments.

**Fix:**
1. **Change `DecodeJSON`'s signature** to `DecodeJSON(w, r, dst)` and wrap the body in `http.MaxBytesReader(w, r.Body, 64<<10)`. Use the same in login and reauth (4 KB is plenty).
2. **Field limits**, checked in the handler with a clear 400:

   | Field | Maximum |
   |---|---|
   | Customer and site name | 120 |
   | Site code | 48, slug characters only (same rule as the customer slug, see L14) |
   | Reason | 500 |
   | Display name | 120 |
   | Password | 1024 |
   | Email | 254 (already checked) |
3. **Check that nothing larger than 64 KB is expected:** the offline request import and the portal-TLS settings take bodies; their size must be checked before applying the limit.

**Test:** unit tests for each limit (413 or 400), and for a valid body at the limit.

### M10 — The servers have no read, write or idle timeouts

**Status:** Confirmed

**Where:** `internal/http/mtls.go:41-51` (9443, internet-facing); `cmd/ctrlapi/main.go:294-298` (443, behind Caddy)

**Problem:** only `ReadHeaderTimeout` is set, and the chi timeout middleware is not on the 9443 router. Slow bodies, slow TLS handshakes and idle connections are unbounded.

**Fix:**
- Set on both servers: `ReadTimeout: 30s`, `WriteTimeout: 60s`, `IdleTimeout: 120s`.
- On the 9443 server add `MaxHeaderBytes: 64<<10`, plus a `middleware.Timeout(15s)` and `middleware.Recoverer` on the mutual-TLS router.

**Test:** unit test that the configured `http.Server` values are set; an existing integration test of the mTLS listener still passes.

### M11 — Unauthenticated registration has no cap and no field validation

**Status:** Confirmed

**Where:** `internal/api/register.go:39-261`; `offline_activation_api.go:331` (`writeFile`)

**Problem:**
- Anyone can create unlimited waiting appliances (30 per minute per address, unlimited across addresses).
- Serial, hostname, model, MAC and fingerprint values are stored unvalidated (up to 64 KB).
- The serial is put into a `Content-Disposition` filename unescaped.

**Fix:**
1. Validate on registration (and on offline import):
   - **serial:** 1–64 characters, `[A-Za-z0-9._-]`;
   - **hostname:** at most 253, DNS characters;
   - **model:** at most 120, printable;
   - **MACs:** `net.ParseMAC`;
   - **fingerprint:** hex, at most 128.

   Refuse anything else with 400.
2. Cap the waiting backlog. When more than N waiting appliances exist (N a setting, default 200), refuse new registrations with 503 and raise one security alert. Also prune waiting rows never activated after M days (a setting, default 30). Both are new settings: follow the settings rule (CLAUDE.md §0C, `docs/OPERATIONAL_SETTINGS_POLICY.md`).
3. In `writeFile`, build the header with `mime.FormatMediaType("attachment", map[string]string{"filename": name})`.

**Test:** unit tests for each field rule and the filename escaping; an integration test for the cap.

### M12 — Portal certificate orders can retry for ever after a crash, and shutdown can lose a result

**Status:** Possible

**Where:** `internal/portaltls/worker.go:185-207`; `cmd/ctrlapi/main.go:232`

**Problem:**
- Attempts are counted only when an order fails cleanly, so a crash or restart loop re-runs a `PROCESSING` order (once its 20-minute lease expires) without ever reaching `maxAttempts`.
- `failOrder` and `completeOrder` use the root context, which is cancelled on shutdown. A certificate issued just before shutdown is never recorded, and is ordered again.

**Impact:** duplicate certificates count against Let's Encrypt's duplicate-certificate limit, and the first certificate is never revoked.

**Fix:**
1. Increment `attempts` when the order is claimed (the `PROCESSING` update), not only on failure.
2. Run `failOrder` and `completeOrder` with `context.WithTimeout(context.Background(), 30*time.Second)`.
3. On reclaiming an expired lease, check the ACME order URL first and finalise from it if it was already valid.

**Test:** the Pebble integration test: kill mid-order, restart, and see one certificate, with attempts incremented.

### M13 — One mutex serialises every ACME and DNS action for minutes

**Status:** Possible

**Where:** `internal/portaltls/worker.go:231,511`; `admin.go:141,181`

**Problem:** `acmeMu` is held across a whole order. Deleting an appliance, "Test access" and "Apply CAA" block behind it without honouring the request deadline.

**Fix:** replace `sync.Mutex` with a one-slot channel semaphore. Request paths `select` on `ctx.Done()` and return "busy, try again" instead of hanging.

**Test:** unit test: with the semaphore held, `TestAccess` returns within its deadline.

### M14 — Audit rows can be lost after a committed change

**Status:** Confirmed

**Where:** `internal/audit/audit.go:32-82` (`Emit`, `Op`) and every caller

**Problem:**
- `audit.Op` writes with the request context. A client disconnect, or the 15 s deadline, cancels the insert for a change that already committed, with only a warning.
- `Op` also deletes `_tenant_id` from the caller's map.

**Fix:**
1. In `Emit`, write with `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)`.
2. In `Op`, copy the payload before removing `_tenant_id`.

**Test:** unit test: an audit call with an already-cancelled context still writes (against the test database). The caller's map is unchanged.

### M15 — Administrators can take over peer administrators

**Status:** Confirmed · **DECISION** (recommended: only owners reset administrators)

**Where:** `internal/api/users.go:111-123` (`mayGrant`) and its callers `updateUser`, `teamPassword`, `resetUserMFA`

**Problem:** `mayGrant` protects only owners. A `tenant_admin` can set another `tenant_admin`'s password, reset their two-step sign-in and sign in as them. A `platform_admin` can do the same on the Team.

**Fix:** add `mayManage(actor, target)`:
- a password reset, an email change or a two-step reset of a user holding `tenant_admin` or `platform_admin` needs the matching owner role (`tenant_owner` or `platform_owner`);
- administrators keep managing auditors and viewers.

**Test:** API tests: an admin resetting a peer admin gets 403; an owner succeeds.

### M16 — Offline request import: no step-up, silent conflicts, double packages

**Status:** Confirmed

**Where:** `internal/api/routes.go:68`; `offline_activation_api.go:67-255`

**Problem:**
- Importing creates appliance rows with the permission only, no step-up.
- `ON CONFLICT (request_id) DO NOTHING` reports success even when the request id belongs to another appliance.
- `activationPackage` reads and consumes the request without a lock, so two concurrent calls mint two packages for one request.

**Fix:**
1. Add step-up to the import route, matching the other appliance actions.
2. Detect the conflict: if the existing `request_id` names another appliance, return 409.
3. In `activationPackage`, `SELECT … FOR UPDATE` the request in a transaction, mint and consume in that transaction, and refuse a consumed one.

**Test:** API tests for the 409 and for a second package from the same request (refused).

### M17 — Signing-key files are trusted without checks

**Status:** Possible

**Where:** `cmd/ctrlapi/main.go:144-158` (assignment key), `:323-335` (registry root); `cmd/ctrlapi/assignkey.go`; `license.LoadSigner`

**Problem:**
- Key files are accepted whatever their mode or owner.
- Raw 64-byte Ed25519 keys are checked only for length.
- Nothing stops the registry-root key and the assignment key being the same file.
- `--force` overwrites a key in place with no backup.

**Fix:**
1. **One shared loader** `loadEd25519File(path)` that:
   - refuses a mode wider than 0600 (on Linux);
   - checks `ed25519.NewKeyFromSeed(raw[:32])` equals `raw`.
2. **At startup**, refuse to start the signing roles when any two of the vendor, assignment and registry-root keys are equal.
3. **Key generation** writes with `O_CREATE|O_EXCL`. `--force` renames the old file to `.bak-<timestamp>` first.

**Test:** unit tests for mode, consistency, equality and the backup on `--force`.

---

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | The last-platform-administrator guard runs outside the transaction and treats a database error as "not last" (fails open). Two concurrent demotions both pass. | `api/users.go:378-387` | Run the count inside the update transaction with `FOR UPDATE` on the platform-admin rows. Return an error (and refuse) on a query failure. |
| L2 | The step-up mark ignores a Redis error but answers `{ok:true}`. | `api/reauth.go:18-24`, `http/auth_handlers.go` (reauth) | `MarkReauth` returns an error; reauth answers 503 when it fails. |
| L3 | `IssueTx` ignores the previous-version read error; any error but "no rows" issues version 1 over a higher version. | `api/assignment_api.go:108` | Treat only `pgx.ErrNoRows` as version 0; return any other error. |
| L4 | `licenses/{id}/revoke` takes no per-appliance lock (issue does) and does not release the portal hostname, unlike retirement. Suspend and resume check, then act, without a lock. | `licensing/licensing.go:300-312`, `api/licenses.go:153-217` | Make `Revoke` take the same advisory lock as `IssueTx`, and release the portal hostname in the same transaction (`portaltls.ReleaseAppliance`), as `revokeApplianceBoundLicensesTx` does. Run suspend and resume check-and-act in one locked transaction. |
| L5 | `revocation_delivered_at` is set once the response is encoded, which does not prove the appliance stored it; it gates site deletion. | `api/licenses.go:258-276` | Set it only when the appliance confirms (the next licence fetch reporting the revoked ids it holds), or document it as "sent" and rename the column, a migration (CONTROLLED). |
| L6 | `ReconcileTerminalTimeouts` changes the state, then inserts the alert, outside a transaction; a failed insert means the alert is never raised. | `api/terminal_delivery.go:239-275` | Do both in one transaction per appliance. |
| L7 | The trust, fleet and appliance-detail pages hide database errors and return empty sections with 200. `strictMTLSSelf` turns a database outage into "certificate not active" (403). | `api/system.go:103-212`, `fleet.go`, `terminal_delivery.go:352-378` | Return 503 when a query fails. In `strictMTLSSelf`, distinguish a query error (503) from no match (403). |
| L8 | Each CSR submission stores a new pending row with no cap. Two concurrent issuances can leave two active certificates. A retiring appliance still gets automatic rotation. | `api/certificates.go:25-80,114-170` | Keep one pending CSR per appliance (an upsert, which needs a unique index, a migration, CONTROLLED, or delete the older pending rows in the same transaction). Lock the appliance row in `issueCertForAppliance`. Refuse rotation while `appliance_terminal_delivery` exists. |
| L9 | `replace` has no row lock or state re-check. | `api/appliance_ops.go:490-528` | Lock and re-check in a transaction, as `move` does. |
| L10 | `valid_days` overflows above about 106,000 days; licence terms have no upper bounds. | `api/licenses.go:69` | Bound `valid_days` to 1–3650 and `valid_until` to 10 years from now; bound the guest cap and the grace days. |
| L11 | The appliance client-certificate CSR accepts any key type and size. | `pki/ca.go:234-246` | Accept Ed25519, or ECDSA P-256 or P-384, only (check which type the appliance generates in Phase 2 first). |
| L12 | Failed-authentication audit rows use a hand-written JSON escaper that misses control characters, so the row is dropped. A non-uuid `iss` becomes an unaudited 500. | `auth/appliance.go:137-175`, `:56-79` | Build the payload with `json.Marshal`. Validate `iss` as a uuid before the lookup and answer 401 "not enrolled". |
| L13 | Every insert failure is reported as "already exists" (customer, site, user), hiding outages. Because emails are globally unique, a customer administrator can test whether an address exists anywhere. | `api/customers.go:96-99,145-149`, `users.go:187-193` | Map only unique violations (`isUniqueViolation`) to 409; others 500. For a customer-scoped caller, word the 409 neutrally ("this email cannot be used"). |
| L14 | The site `code` is not validated (the customer slug is). A tenant administrator can restore a site of an archived customer. | `api/customers.go:107-150,647-668` | Apply the slug rule to `code`; refuse site restore while the customer is archived. |
| L15 | The public `/readyz` returns the version; backup health returns host file paths and raw error text to `platform_support`. | `http/router.go:174-193`, `api/backup_health.go` | Return the version only on the loopback `/readyz` (the deploy smoke test uses loopback). Return file names, not paths, and a short error class. |
| L16 | The intermediate certificate is not checked against its key and the root. `relocateRootOffline` moves the root key to another directory on the same host, and fails across file systems. | `pki/ca.go:61-68`, `cmd/ctrlapi/main.go:345-361` | The check is part of H1. Remove `relocateRootOffline` once H1 removes runtime root generation; on a fresh install the ceremony takes the key. |
| L17 | The ACME renewal window from the CA is used without clamping. | `portaltls/ari.go:111-115` | Clamp the start to no later than `NotAfter − 7 days`, and ignore a window ending after `NotAfter`. |
| L18 | The daily order cap is count-then-insert. Several worker loops ignore row-scan errors and `rows.Err()`. | `portaltls/orders.go:80-92`, `worker.go` | Count inside the inserting transaction with an advisory lock per appliance; check scan errors and `rows.Err()` everywhere. |
| L19 | Long-lived goroutines (housekeeping, portal-TLS worker, mTLS listener) have no panic recovery. | `cmd/ctrlapi/main.go:177,232,256` | Wrap each loop iteration in a `recover` that logs and continues. |
| L20 | The in-memory replay cache grows without bound once full, scans the whole map on each insert, and empties on restart. | `applianceauth/jwt.go:177-195` | Keep a time-ordered queue so expiry is O(1). When it is full of unexpired entries, refuse with 503 rather than grow. Optionally back it with Redis `SET NX EX` so a restart keeps it. |
| L21 | Operational values are hard-coded: rate limits (20/30/240 per minute), step-up lifetime (5 min), replacement window (72 h), retirement acknowledgement timeout (10 min), superseded overlap (24 h), client-certificate lifetime (90 days). | various | **DECISION.** Recommended: make the replacement window and the acknowledgement timeout settings (rule §0C), and keep the others fixed as security guarantees. |
| L22 | `clientip` trusts forwarding headers only from loopback. If Caddy reaches ctrlapi over a Docker bridge, every client shares one rate-limit and lockout bucket. | `clientip/clientip.go` | Verify on Central that Caddy connects from 127.0.0.1. If not, add a configured trusted-proxy address list. No code change if it is loopback. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | Dead code: `api.(*Base).issueAssignment`, `pki.LoadOrCreate` and `selfSign`, `cmd/pkitest rawGet`, two unused values in `cmd/acceptance`. | Delete them. |
| CC2 | Appliance-side verifier code compiled into ctrlapi (`assignment.Store`, `RegistryStore`, `offline.AcceptFor`, `activation.AcceptForFirstActivation`). The test DNS server (`Challtestsrv`) is selectable in production by an environment variable. | Leave the shared verifier code (it mirrors the appliance), but refuse `Challtestsrv` unless `CTRLAPI_ENV` is not production. |
| CC3 | Duplicates: three UUID generators (`assignUUID`, `newUUIDv4`, `licensing.newUUID`), three atomic-write helpers, three raw-key loaders, three one-method DB interfaces, `clientIP` and `clientIPFromReq`, `emailOf` and `actorOf`. | Keep one of each. Merging the key loaders is part of M17. |
| CC4 | `serve()` in `main.go` is about 240 lines with a `goto`; `activate` is about 160 lines. | Extract `loadSigningKeys`, `startPKI` and `startHousekeeping`. Split `activate` into validate, create-or-find, bind, sign. |
| CC5 | `id::text = $1` defeats the primary-key index (licensing, portal TLS, users). | Compare `id = $1::uuid`. |
| CC6 | `customerRows` loads the whole fleet to count per customer; list limits of 500 and 2000 truncate silently. | Count in SQL with `GROUP BY`; return `has_more` when a limit is hit. |
| CC7 | `WriteErr` is a legacy duplicate of `Fail`. | Replace the callers and delete it. |
| CC8 | `metrics.statusRecorder` hides `Flusher` and `Hijacker`. | Use `middleware.NewWrapResponseWriter`, as `requestLog` does. |
| CC9 | `StrictApplianceAssignmentHandler` answers every refusal with 204. A variable `idFpr` holds a public key. The CA comment contradicts the code (see H1). | Log the refusal reason (already in the fetch log) and keep 204 only for "no change"; rename the variable; fix the comment. |

---

## Fix batches

Do them in this order. A batch is done when its tests pass and the Central unit tests still pass.

| Batch | Findings | Files | Notes |
|---|---|---|---|
| **B1** CA startup fails closed | H1, L16 | `internal/pki/ca.go`, `cmd/ctrlapi/main.go`, `deploy/scripts/central-install.sh` | CONTROLLED (trust). Highest priority. Needs a fresh-install test on a disposable Central. |
| **B2** Limits and timeouts | M1, M9, M10 | `internal/api/ratelimit.go`, `internal/api/common.go` and every `DecodeJSON` caller, `internal/http/auth_handlers.go`, `internal/http/mtls.go`, `cmd/ctrlapi/main.go` | Touches many handler files (a signature change). Do it before B5–B12 to avoid conflicts. |
| **B3** Certificate lifecycle | M2, L8, L11 | `internal/api/appliance_ops.go` (reissue), `internal/api/certificates.go`, `internal/pki/ca.go` | L8's unique index is a migration (CONTROLLED); the delete-older-rows alternative is not. Run after B1 (shares `ca.go`). |
| **B4** Signing keys and registry | M3, M17, L3 | `internal/api/assignment_keys.go`, `assignment_registry.go`, `assignment_api.go`, `cmd/ctrlapi/main.go`, `cmd/ctrlapi/assignkey.go` | Run after B1 (shares `main.go`). |
| **B5** Appliance actions are atomic | M7, M8, L9 | `internal/api/appliance_ops.go`, `internal/licensing/licensing.go` | Run after B3 (shares `appliance_ops.go`). M8 may need a migration. |
| **B6** Audit reliability | M14, L6, L12 | `internal/audit/audit.go`, `internal/api/terminal_delivery.go`, `internal/auth/appliance.go` | |
| **B7** Registration hardening | M11 | `internal/api/register.go`, `offline_activation_api.go` | The cap and pruning add settings (rule §0C). |
| **B8** Offline activation | M16 | `internal/api/routes.go`, `offline_activation_api.go` | Run after B7 (shares a file). |
| **B9** Licence actions | L4, L5, L10 | `internal/licensing/licensing.go`, `internal/api/licenses.go` | Run after B5 (shares `licensing.go`). L5 rename is a migration. |
| **B10** User and admin guards | L1, L2, L13, L14 | `internal/api/users.go`, `reauth.go`, `customers.go`, `internal/http/auth_handlers.go` | Run after B2 (shares `auth_handlers.go`). |
| **B11** Error visibility | L7, L15 | `internal/api/system.go`, `fleet.go`, `terminal_delivery.go`, `backup_health.go`, `internal/http/router.go` | Run after B6 (shares `terminal_delivery.go`). |
| **B12** Portal TLS worker | M12, M13, L17, L18 | `internal/portaltls/*` | Needs the Pebble integration test. |
| **B13** Resilience | L19, L20 | `cmd/ctrlapi/main.go`, `internal/applianceauth/jwt.go` | `jwt.go` is mirrored in `data-plane/internal/applianceauth`; change both together (Phase 2). |
| **B14** Clean code | CC1–CC9 | various | Last, after the behaviour batches. |

**Waiting for a decision**

| Item | Decision | Then |
|---|---|---|
| H2 | Activation needs a code shown on the box | Its own batch, with Phase 2's appliance identity work. |
| M4, M5 | Hide staff emails, reasons and IP addresses from customer users | One batch: `fleet.go`, `system.go`. |
| M15 | Only owners reset administrators | One batch: `users.go`. Run after B10. |
| M6 | Per-address two-step lock | A migration (CONTROLLED). |
| L21 | Which values become settings | Settings rule §0C. |
| L22 | Confirm Caddy connects from loopback | A check on Central, read-only. |
| P0-2 | Supported Go release and library updates | Phase 0, batch B0. |

## Checked and rejected

- **"`newUUID` returns a predictable UUID if randomness fails."** Since Go 1.24, `crypto/rand.Read` never returns an error.
- **"Deleting a customer races with a new site and cascades it away."** Migration 0037 made those foreign keys `RESTRICT`.
- **"`peekIss` reads the first `iss`, JSON the last."** The identity is taken from the key that verified the signature, so a second `iss` gains nothing.

## Not verified

- **Behaviour against a real database.** The 33 database-backed Central tests did not run (Phase 3).
- **The deployed Caddy-to-ctrlapi path** (L22) and the file modes of keys on Central.
- **ACME and Cloudflare behaviour under failure** (M12, M13), which needs the Pebble integration test or a staging zone.
