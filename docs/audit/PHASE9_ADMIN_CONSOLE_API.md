# Phase 9: Admin Console API (edged)

**Audited:** master `b00410b5`, on `delivery/backend-audit`. Code only: nothing was run against PRE-LIVE.

**Scope:**
- `data-plane/cmd/edged`, about 32,000 lines in 90 files.
- `internal/modules`, `tenantcfg`, `reqlog`, `metrics`, `livez` and `startupbackoff`.
- The edged side of the Caddy and hotel-admin proxy (`deploy/caddy/Caddyfile.edge`, `deploy/scripts/hotel-admin-cert-manager.sh`).
- scd's restore check, because only edged reaches it.

**edged was reviewed in four parts:**

| Part | Area | Files |
|---|---|---|
| 9a | Sign-in, operators, MFA, the role matrix, route mounting, health | `main.go`, `auth.go`, `operator_*.go`, `resources_operators.go`, `common.go`, `health*.go`, small helpers |
| 9b | Guest data, privacy, sign-in administration, vouchers, usage | `guest_*.go`, `client_devices_admin.go`, `resources_guest_accounts.go`, `resources_sessions.go`, `resources_signin_*.go`, `resources_vouchers.go`, `resources_code_requests.go`, `data_retention_admin.go`, `resources_usage.go`, overview and dashboard |
| 9c | Money, commerce, PMS administration, providers | `resources_commerce*.go`, `ledger_*.go`, `resources_card_reviews.go`, `resources_phase3*.go`–`resources_phase6*.go`, `resources_pms_*.go`, `resources_roster_reconciliation.go`, `resources_providers.go`, `resources_acquisition_admin.go` |
| 9d | Network, portal files and HTTPS, backups, certificates | `resources_network*.go`, `resources_sysnetwork.go`, `netd_client.go`, `resources_portal_*.go`, `resources_branding.go`, `resources_backups.go`, `backup_*.go`, `resources_cert.go`, `resources_cloud.go`, `resources_site.go` |

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 5 | 16 | 4 |

**edged's access control is well built:**
- Every resource is mounted through one helper that applies its permission key. Every route outside that helper has its own role check, and a comment warns against mounting routes directly.
- The role matrix splits powers deliberately. For example, a role can read sign-in attempts without reading what guests typed.
- Operator sign-in is throttled per account and per address in the database, and the throttle fails closed. Unknown accounts take the same time as known ones.
- Every operator, MFA, financial, transfer, export, erasure, restore, certificate and backup action needs the operator's password again.
- Sessions are revoked whenever an operator's credentials or roles change.
- No GET handler changes state, and no SQL is built from request text.

**The findings fall into three gaps:**
1. Request bodies have no size limit.
2. A few sensitive settings skip the password step-up that the rest of the console uses.
3. Some recovery and audit paths are weaker than their comments say.

## Earlier phases' questions, answered

| Item | Answer |
|---|---|
| **Operator MFA (Phase 4 L8)** | **Holds in edged.** Wrong codes are counted per operator and per address before any check (`operator_mfa.go:293`, `:401-422`). Turning off your own MFA needs your password and a current code or recovery code. An admin resetting another operator's MFA must give their own password and a 4–500 character reason, cannot reset themselves, and the reset ends the target's sessions and is audited (`:637-690`). scd's own counter (Phase 4 L8) stays optional, as a backstop. |
| **PMS TLS pin form (Phase 7 L3)** | **Holds in edged.** A pin with TLS off is refused, and the pin must be 64 hex digits (`resources_phase3_interface_authoring.go:537-545`). The pmsd check recommended in Phase 7 L3 is still worth adding as a second layer. |
| **Provider URL entry (Phases 5–7)** | **No provider takes a free URL.** The PMS endpoint is `host:port` and may be any LAN host, by design. Social callback URLs are built from the portal hostnames and must be https. The SMTP host has only a character check, so it may point anywhere. The risk is the missing step-up, in M2. |
| **Stay transfer step-up (Phase 6 L11)** | **Holds.** Password step-up, an actor taken from the session, and a 4–500 character reason that goes to `transfer.Execute` and the audit (`resources_phase5_transfer.go:248-290`). Phase 6 L11, that the transfer event itself does not keep the reason, is still open in `internal/transfer`. |
| **Network names (Phase 8 H1, M9)** | edged stores a guest network's name, domain and reservation hostnames without validation. See M5. |
| **Sessions after a restore (Phase 4)** | A restore restarts edged (`cmd/scd/restore.go:216-218`), and edged keeps sessions only in memory, so every session ends. The real restore gap is in M3. |

## Medium

### M1: Request bodies have no size limit (Confirmed)

**Where.**
- `cmd/edged/common.go:40-48`: `decodeJSON`, which almost every handler uses, reads `r.Body` with no limit.
- Raw decoders are also unbounded: `resources_sysnetwork.go:19`, `:27`, `:69`; `resources_cert.go:44`; `backup_keys.go`.
- Only nine handlers use `MaxBytesReader`: backup download, the ledger, guest data, the sign-in code and branding.
- `http.Server` sets only `ReadHeaderTimeout` (`main.go:468`).
- Caddy and the hotel-admin proxy set no body limit either.

**Impact.** Any signed-in operator, of any role, can send a multi-gigabyte JSON body. A single long string is held in memory whole while it is decoded, so a few such requests can exhaust edged's memory and take the Admin Console down. The same applies to the system-network proposals, which are passed on to netd.

**Recommended fix.**
- In `decodeJSON`, wrap the body with `http.MaxBytesReader(w, r.Body, 1<<20)`, and give the few larger payloads an explicit higher limit.
- Do the same for the raw decoders.
- Add `ReadTimeout` and `IdleTimeout` to the server.
- As a second layer, add `request_body { max_size 10MB }` to the hotel-admin vhost.

**Test.** A 2 MB body to a normal write route returns 413. The existing large payloads still pass.

### M2: Email, SMS and social sign-in settings change without the password step-up (Confirmed)

**Where.**
- `cmd/edged/resources_providers.go:317-480` covers notification provider create, update, delete and secret, and `/test` (`:274-290`).
- Social provider create, update, delete and secret are at `:759-772` and `:900-1016`.
- Neither calls `reauth`.
- Payment providers do require it (`resources_acquisition_admin.go:39-44`, `:74`, `:129`, `:200`).
- The write keys `notification-providers` and `social-providers` belong to `site_admin` and `hotel_it_manager` (`auth.go:208`).

**Problem.** Changing only the SMTP host keeps the sealed SMTP password (`:430-445`). The next sign-in code or password-reset email then logs in to the new host and hands it the saved password and every code.

The SMTP host check refuses only a few characters (`internal/mail/smtp.go:52`), so loopback and private hosts are allowed. `/test` therefore also works as a port probe from the appliance. Swapping a social provider's client id or secret redirects guest social sign-in to another app. The callback hosts are still checked, which limits this.

**Impact.**
- A stolen or unattended console session is enough to capture the mail credential and guests' sign-in codes.
- The same session can break or redirect social sign-in.
- Elsewhere, the console demands the password for actions of lower impact than this.

**Recommended fix.**
- Use the same step-up as payment providers for every write of these two kinds.
- When the SMTP host, port or user changes, clear the sealed password and require it again.
- Fix the secret-store ordering (L15) at the same time.

**Test.**
- A write without the password returns 403.
- After a host change, the provider reports "password required" until a new one is set.

**CONTROLLED** (guest authentication).

### M3: A restore does not check the backup's schema against the installed software (Confirmed by reading)

**Where.**
- `cmd/scd/restore.go:617-645`: `restoreIntegrity` checks that some tables exist and that `tenants` can be read. Its comment says "migration ledger", but the code never reads the ledger.
- No migration step runs after the swap.
- edged forwards any listed `db-*` backup after the password and the typed name (`cmd/edged/resources_backups.go:395-445`).

**Problem.** A backup taken before newer migrations can be restored under binaries that expect the newer schema. The services restart against tables and functions that are missing or older. The financial guards, the posting authoriser and the sign-in roster rules all live in migrations, so their versions then do not match the code.

Restoring old operators and old data is what a restore is (Phase 4). The missing check is that the schema matches.

**Impact.** After restoring an older backup, the appliance can come up half working. Or worse, it can run newer code over older database rules. The restore reports success.

**Recommended fix.**
- Record the highest applied migration in each backup's metadata, and show it before the restore.
- After the swap, compare the restored ledger with the binaries' expected level.
- Older: run the forward migrations as part of the restore.
- Newer: refuse.
- Unreadable: refuse and swap back.

**Test.** An integration test restores a dump made at an older level. The restore either migrates it forward or refuses, and never reports a bare success.

**CONTROLLED** (database, destructive). The fix lives in scd; it is recorded here because the restore surface is edged's.

### M4: The guest-data read audit misses routes, and nothing catches a new one (Confirmed)

**Where.** `cmd/edged/guest_data_reads.go:30-52` (the map) and `:192-196`.

**Problem.** The audit records a read only when the route pattern is in a hand-kept map.
- **Routes that show guest data but are not in the map:**
  - `/guest-accounts/portal`;
  - `/vouchers/{id}/history`, which has operator reasons;
  - the `/guest-signin-restrictions` list, which has device addresses and typed room numbers;
  - `/network-pauses/{id}/devices`;
  - `/guest-data-rights/erasures`.
- **No test fails when a new guest-data route is added without an entry.**
- **Plain list browsing is deliberately not recorded** (the file header). The lists of guest accounts, devices, sessions and sign-in attempts show names, notes, IP and MAC addresses.
- **Reads are recorded only on status 200**, at most once per 10 minutes per record, through an in-memory queue. A crash loses up to 3 s of entries.

**Impact.** The read log cannot show everyone who saw a guest's data. This is the evidence a data-subject access request or a privacy complaint relies on.

**Recommended fix.**
- Add the missing routes.
- Add a test that walks the chi router and fails for any route under a guest-data permission key that has no map entry and no explicit "not guest data" mark.
- Whether list browsing is recorded is a product decision. Recording one entry per page view, with the filter and no search text, is cheap.

**Test.** The router-walk test above.

**DECISION** (list browsing).

### M5: edged stores network names, domains and hostnames without checking them (Confirmed; the edged side of Phase 8 H1 and M9)

**Where.**
- `cmd/edged/resources_network_replace.go:76-92` (`normalizeGuestNetworkInput`) only fills defaults.
- `resources_network.go:233-259` stores the network name as given.
- `:751` and `:778` store reservation hostnames as given.
- `internal/netcfg/validate.go` checks none of them.

**Problem.**
- The name ends up in the firewall script: Phase 8 H1, rule injection.
- The domain name is sent to every guest as DHCP option 15.
- The hostnames go into Kea reservations.
- A bad value fails only at apply, as a Kea or nft error that names no field.
- Reservation IP addresses and MACs are checked only by PostgreSQL casts until apply.

**Recommended fix.**
- Validate when saving, in edged, and again in `netcfg.ValidateOne`, so netd refuses a bad value from any writer:
  - **Name:** a length cap and printable characters only; no quotes, backslashes or control characters.
  - **Domain and hostname:** RFC 1123 labels.
  - **Reserved IP:** must be inside the subnet.
- Return a field-level error.

**Test.** Hostile values for each field get a 400 that names the field.

**CONTROLLED** (networking). Do it in one batch with Phase 8 B1.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | **Raw database and transport errors are returned to operators.** Driver messages, function names, the scd socket URL and file paths reach the response. Operators are trusted, but the console's own rule is a fixed message. | `resources_signin_protection.go` (10 places), `resources_signin_network_pause.go` (9), `resources_vouchers.go` (7), `resources_sessions.go:360`, `resources_phase5_poststay.go:74/85/217`, `resources_card_reviews.go:55/64/115`, `resources_phase6_setting.go:39/92/97`, `resources_phase4_review.go:444`, `netd_client.go:62`, `resources_cloud.go:63/85`, `resources_network.go:235/1073`, `resources_portal_assets.go:~118`, `guest_data_rights.go:~277` | One helper that logs the detail with the request id and returns a fixed message. Map known database refusals to their own codes. |
| L2 | **Secrets in responses lack `Cache-Control: no-store`.** A staff reset of a guest account's password returns the generated password with no reason and no cache header. Voucher codes from issue, reveal and export, and new guest accounts, also lack the header. Credential reveals set it. | `guest_accounts_iamv2_lifecycle.go:482-570` (`:566`); `resources_vouchers.go:469-525`; `resources_guest_accounts.go:105` | Set `no-store` on every response that carries a secret, through one helper. Whether a desk password reset needs a reason or step-up is a product choice; today it is a desk power by design. |
| L3 | **`verifyPassword` trusts the hash's own argon2 settings.** m, t and p are read from the stored string with no bounds, so a hash written outside edged, for example through a restore or direct database access, can make one sign-in allocate any amount of memory. It duplicates `internal/passwordhash`, which has bounds. | `auth.go:39-60` | Use `internal/passwordhash.Verify`. |
| L4 | **Anyone who can reach the console can lock a named operator out** by failing their password, until the lockout ends (default 900 s, a setting). This is the usual trade-off; the per-address limit does not protect the target. | `operator_signin.go:235-270`; `operator_signin_settings.go:40-49` | Accept it and document it, or make the account lock apply only to addresses that have not signed in successfully before. |
| L5 | **`middleware.RealIP` lets a client set `r.RemoteAddr`.** Caddy overwrites `X-Real-IP` but passes `True-Client-IP` through, and chi checks that first. Nothing reads `RemoteAddr` except two fallbacks, and edged listens on loopback, so there is no effect today. `clientIP` also falls back to the first `X-Forwarded-For` entry. | `main.go:294`; `common.go:80-95` | Remove `RealIP`. Use `X-Real-IP` only when the peer is loopback, otherwise `RemoteAddr`. |
| L6 | **System-network confirm has no step-up and starts a detached task every time.** Apply and rollback need the password, confirm does not. Each call starts a goroutine of up to 150 s to renew the console certificate, before netd has confirmed anything, with no limit. | `resources_sysnetwork.go:55-66` | Require the password; start the renewal once, after netd confirms; refuse a second while one runs. |
| L7 | **Backup download serves any file in the backup folder,** including deploy and gatep files and old unencrypted `.sql.gz` dumps, with no warning that a file is unencrypted. `os.Stat` follows symlinks. | `resources_backups.go:~268`, `:332`; `backup_download.go` | Serve only `db-*` dumps; mark or refuse unencrypted ones; refuse symlinks (`Lstat`). |
| L8 | **Listing backups re-hashes every dump on each call.** A read-only role can cause heavy disk reads by refreshing. | `resources_backups.go:~190` (`readVerification`) | Cache the hash by size and modification time. |
| L9 | **Portal asset upload details.** The file is written in place, so portald can serve a half-written file to guests. One `Read` call may return less than the file. Each request allocates 8 MB. A JPEG is recognised from 3 bytes. No stored-XSS path was found. | `resources_portal_assets.go:~110-121` | `io.ReadFull` into a limited buffer; temp file and rename; check more of the JPEG header. |
| L10 | **The one-time-code reveal is the only reveal that skips `validReason`.** It counts bytes and accepts any non-empty text, and its audit uses `s.audit` (errors ignored) after the reveal, where the others write the audit first and stop on failure. | `resources_code_requests.go:102-133` | Use `validReason` and `auditErr` before the reveal, like voucher reveal. |
| L11 | **The old `?q=` search fallback.** It puts guest search text in URLs and so in proxy logs, against the design of header-only search, and skips the length cap (`listUsageSources` has no bound). | `resources_usage.go:98`, `:622`; `guest_data_rights.go:~58` | Remove the fallback once the console no longer sends it. |
| L12 | **Paging and id parsing.** A huge `page` overflows the offset and gives a 500. Date ranges are unbounded, and a bad `from` or `to` is ignored silently. Some ids reach `::uuid` casts unchecked. `?fresh=1` on a read-only ledger route triggers an scd probe. | `resources_helpers.go:35`; `guest_accounts_iamv2_lifecycle.go:217`; `client_devices_admin.go:191`; `resources_usage.go:62-77`; `resources_signin_attempts.go:~170`; `ledger_admin.go:27` | Cap `page`; refuse bad dates and over-long ranges; validate ids with `uuidRe`; rate-limit the fresh probe. |
| L13 | **Free-text bounds in the data-rights register and client groups.** Requester, verification method, extend reason and note have no length checks in Go, and the register has no name-free check, unlike the assisted flow. | `guest_data_register.go:49-55`; `resources_client_groups.go:249`, `:322`, `:388` | Apply `validReason`-style bounds. |
| L14 | **Shortening a retention period shows no impact first.** With a password and a reason, `audit_log_days` can drop to 90 and `sign_in_attempt_days` to 7, and the next purge deletes or anonymises. The read of the old values ignores errors, so the audit can record `old: null`. | `data_retention_admin.go:131-200` (`:168`) | Show how many rows the next run would remove and ask for confirmation; check the read. |
| L15 | **A failed secret store after an update leaves the provider half changed.** The row is committed, then the secret store fails with 502. The update is not audited, and the success audit has no detail. | `resources_providers.go:456-466` | Store the secret first, or roll the row back as create does; audit both outcomes. Fix with M2. |
| L16 | **The guest device self-service switch changes without the password step-up.** It has a reason (optional) and an audit row. | `resources_phase6_setting.go:97` | Require the password, as the other guest-facing settings do. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code (staticcheck U1000):** `sessionStore.get`, `errNotFound`, `iamv2MethodAccount`, `iamv2AccountAuthority`, `shellQuote`, `nullIfBlankText`, `roomAuthResyncInFlight`, `extraKeysFor`, `bodyReason`, `backupRow` and `listBackupRecords`. Two tests compare an expression with itself (SA4000): `db_default_password_test.go:13` and `operator_mfa_test.go:69`. | Delete the dead code; fix the two tests so they check something. |
| CC2 | **Patterns repeated per file.** Four step-up helpers do the same thing: `paymentStepUp`, `stepUpActor`, `portalStepUp` and `voucherCodeStepUp`. Error responses are written differently in each file (L1). Password hashing duplicates `internal/passwordhash` (L3). | One step-up helper, one error helper, one hashing package. |
| CC3 | **Large files.** `resources_overview.go` (1,430 lines), `resources_phase3_interfaces.go` (1,338), `resources_phase3.go` (1,194), `resources_network.go` (1,098) and `resources_providers.go` (1,063) each mix several resources. | Split by resource when a batch touches them. |
| CC4 | **Comment volume, and suites that cannot be re-run.** Comments are long, as in earlier phases. The integration suites leave password-confirmation throttle rows and seed rows behind, so running a second suite on the same database fails. | Trim comments when a batch touches a file. Clear the throttle for the test operators in test setup, and seed with unique keys or delete in cleanup. |

## Not findings

- **gosec G705 and G203 (unescaped HTML output, 11).** Every hit writes JSON through `writeJSON` or relays scd's JSON, with `Content-Type: application/json`. Caddy adds `nosniff`.
- **gosec G703 (path traversal, 3).** Asset names must match a content-hash pattern, and backup names match `safeArtifact` with a prefix check. The symlink point is L7.
- **gosec G704 (SSRF, 2).** The scd client calls a unix socket through a fixed `http://unix` base.
- **gosec G402.** `health_checks.go:166` skips certificate checks on a loopback liveness probe of Caddy, on purpose.
- **gosec G124 (cookie attributes).** The session cookie is HttpOnly, SameSite=Lax, and Secure unless `EDGED_COOKIE_SECURE=false`. The MFA cookie is Strict. Both are fine.
- **gosec G120.** Multipart parsing on portal assets is bounded by the 8 MB asset limit (L9).
- **gosec G404.** `math/rand` is used for restart jitter in `startupbackoff`, which needs no secrecy.
- **Cross-site requests.** `sameOrigin` checks Origin, or Referer, on every state-changing request. The cookie is Lax, and edged answers no CORS preflight. `X-Forwarded-Host` is accepted in the comparison, but a browser cannot send it cross-site without a preflight.
- **Operators escalating their own rights.** Only `site_admin` writes `operators`. Every operator change needs the admin's password, an admin cannot remove their own admin role or disable themselves, and sessions end on every role change.
- **govulncheck: 27 reachable vulnerabilities.** These are the Go 1.25.12 standard-library, `x/text` and `pgx` advisories found in earlier phases, plus those of edged's wider imports. They are tracked once, in Phase 10.
- **The default `EDGED_DB_URL` with a password.** Already Phase 6 L14, tracked in Phase 10.

## Fix batches

A batch is done when its tests pass, and the edged package tests and the `integration`, `integration phase4` and `integration phase5` suites on a throwaway full-schema database still pass.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| B1 | M1 | `cmd/edged/common.go`, `main.go`, `resources_sysnetwork.go`, `resources_cert.go`, `backup_keys.go`; `deploy/scripts/hotel-admin-cert-manager.sh` | Body limits. The Caddy part changes the deployed vhost: deployment is CONTROLLED. Do first. |
| B2 | M2, L15, L16 | `cmd/edged/resources_providers.go`, `resources_phase6_setting.go`, one shared step-up helper (CC2) | Step-up on provider and guest-facing settings. CONTROLLED (guest authentication). The console needs a password field on these forms: a hotel-admin change in the same batch. |
| B3 | M5 | `cmd/edged/resources_network*.go`, `internal/netcfg/validate.go` | With Phase 8 B1, in one branch. CONTROLLED. |
| B4 | M4, L11 | `cmd/edged/guest_data_reads.go`, a router-walk test, `resources_usage.go`, `guest_data_rights.go` | Read audit. List browsing waits for the decision; adding the missing routes and the test does not. |
| B5 | M3, L7, L8 | `cmd/scd/restore.go`, `backups.go`, `cmd/edged/resources_backups.go`, `backup_download.go` | Restore and backup files. CONTROLLED (database, destructive). Needs a restore test on a throwaway database. |
| B6 | L1, L2, L10, L12, L13, L14 | Many handlers; a shared error and no-store helper | Response hygiene and input bounds. Touches many files lightly: do it after B2–B5 to avoid conflicts. |
| B7 | L3, L4, L5, L6, L9, CC1–CC4 | `auth.go`, `operator_signin.go`, `main.go`, `common.go`, `resources_sysnetwork.go`, `resources_portal_assets.go` | Clean-up. L4 is a choice: accept or change. |

## Decisions for the Product Owner

1. **M4.** Whether browsing a guest list, with no search, is recorded in the read audit. Recommended: yes, one entry per page view, with the filter and no search text.
2. **L2.** Whether a desk reset of a guest account's password needs a reason or a password step-up. Recommended: a reason, with no step-up, because it is a routine desk action.
3. **L4.** Whether account lockout stays as it is, the usual trade-off, or is changed so it cannot lock out a named operator.

## Tests run

All runs used throwaway infrastructure and were removed afterwards.

- **Unit tests** (edged and the six library packages): 380 passed, 0 failed.
- **Integration tests, throwaway full-schema PostgreSQL** (all migrations through 0150; superuser plus the real `svc_edged` and `svc_scd` logins):
  - `integration`: 703 passed, 0 failed.
  - `integration phase4`: 736 passed, 0 failed.
  - `integration phase5`: 693 passed, 0 failed.
- **The suites cannot be re-run on the same database.**
  - Running the phase4 and phase5 suites straight after the plain one failed about 130 tests with 429 "too many failed password confirmations". The earlier runs' deliberate wrong passwords were still counted.
  - After clearing `operator_signin_throttle`, one test failed with a duplicate seed row; it passed on a freshly built database.
  - The counts above come from those clean runs. See CC4.
- **Scanners:**
  - go vet: clean.
  - staticcheck: U1000, SA4000 and one S1011 (CC1).
  - gosec: covered under Not findings.
  - govulncheck: tracked in Phase 10.
