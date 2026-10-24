# Backend audit — Phase 1: Central (ctrlapi)

Audited commit: `008a4cb3`. Scope: `control-plane/` (cmd/ctrlapi, internal/*), about 16,500 lines of non-test Go. Read-only: no source code was changed.

Method:
1. A full read of the HTTP layer, sign-in, two-step sign-in, sessions, permissions and every API handler.
2. Two independent second reviews: one on the API and authentication, one on the CA, licensing, assignment signing and portal TLS.
3. A re-check by the auditor of every second-review finding listed as Confirmed below.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 2 (+ toolchain P0-2, see Phase 0) |
| Medium | 17 |
| Low | 22 |
| Clean code | 9 |

What holds up well:
- **Permission model.** Permissions are checked on the route, and customer scope in the handler. No customer-scoped user can read or change another customer's data, and no role can grant itself a platform role.
- **Appliance request signatures.** They bind method, path, body hash, audience and key id within a 60-second window, and replays are blocked.
- **Sessions.** They are re-authorised against the database on every request.
- **Two-step codes.** They are counted inside the checking transaction, and never accepted twice.
- **Database errors.** They are kept out of operator-visible messages.
- **Customer and site deletes.** Foreign keys (migration 0037) protect them from races.

## Endpoint inventory

Every route registered in `internal/http/router.go`, `internal/api/routes.go`, `internal/api/portal_tls.go`, `internal/api/appliance_mtls.go` and `internal/http/mtls.go`. All were checked.

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
| POST offline-activation/requests | appliances.manage (**no step-up**, M16) |
| POST appliances/{id}/license; POST licenses/{id}/{suspend,resume,revoke}; POST appliances/{id}/offline-license | licenses.manage + step-up |
| PATCH security-alerts/{id} | security.manage |
| GET trust, team, backup-health, settings/security, settings/retention, portal-tls/{settings,fleet,events} | fleet.view |
| POST/PATCH/DELETE team[/{id}]; POST team/{id}/password, /mfa-reset; POST users/{id}/data-export; PATCH settings/security, settings/retention | team.manage + step-up |
| PUT portal-tls/settings, /dns-token; POST portal-tls/test, /caa, /appliances/{id}/revoke | security.manage + step-up |

**Appliance listener (9443, mutual TLS, direct)**

| Route | Who may call |
|---|---|
| GET /healthz | anyone |
| GET /v1/appliance/assignment | client certificate only (strict: current certificate) |
| GET hello, license, assignment-registry, certificate; POST offline-reconcile, assignment/ack, csr, portal-tls/sync, portal-tls/orders; GET portal-tls/orders/{id} | certificate + signed request, same appliance id, certificate current or superseded < 24 h |

**Licensing-only rule (CLAUDE.md §0E):** the appliance-facing routes are exactly the licensing set the rule lists. No other appliance-to-Central message exists.

## High

### H1. A missing CA certificate file makes Central silently mint a new root CA

**Location:** `internal/pki/ca.go:51-87`, `cmd/ctrlapi/main.go:241-252`. **Confirmed.**

`LoadCAChain` uses the stored certificates only when both `root-ca.crt` and `intermediate-ca.crt` are readable.

If either is missing:
- **A new root key.** It calls `loadOrCreateKey(rootKeyPath)`. After first boot that key has been moved away by `relocateRootOffline`, so a brand-new root key is generated.
- **A new intermediate.** A new intermediate is signed and the new files are written; the write errors are ignored.
- **The trust anchor is overwritten.** `appliance_ca_versions` version 0 is overwritten with `ON CONFLICT DO UPDATE`, with the error ignored.
- **The same applies to the intermediate key.** A missing intermediate key file is also silently regenerated.

The startup comment ("Absent key → PKI routes and the mTLS listener stay disabled") describes the opposite of what happens.

**Failure scenario:** a restore or migration copies the keys but not one `.crt` file. Central starts "successfully" with a new root:
- every appliance certificate stops verifying;
- the mutual-TLS listener rejects the whole fleet, so assignment, licence, certificate rotation and portal TLS stop for every hotel;
- the database's record of the real root is replaced.

**Fix:**
- Never create CA keys or certificates during `serve`. Creation belongs only to the CA ceremony.
- Refuse to start the PKI when a file is missing.
- Never overwrite an existing `appliance_ca_versions` row.
- At load, check that the intermediate is signed by the root and matches its key.

### H2. An appliance is activated on facts anyone can know: serial, WAN MAC, hardware fingerprint

**Location:** `internal/api/register.go:39-261` (registration), `:267-349` (`replaceWaitingIdentity`); procedure in `docs/APPLIANCE_ACTIVATION_AND_LICENSING.md` step 1. **Confirmed path. Exploitation is Possible.**

Registration is unauthenticated, trust on first use. The operator decides which waiting row to activate by comparing the **serial** (and the WAN MAC under Advanced). Neither is secret, and the appliance does not display its identity-key fingerprint for comparison.

Two ways to exploit it:
- **Registering first.** An attacker who knows a box's serial and MAC registers them with their own key before the real box does. The real box is then refused (`identity_replacement_refused`). The operator sees one waiting row with the right serial and activates it.
- **Taking over a waiting row.** If the real box registered first, an attacker who also knows its hardware fingerprint can replace the waiting row's identity key (`replaceWaitingIdentity` accepts on a fingerprint match).

In both cases the attacker's machine receives the customer's signed assignment, licence and a client certificate. Through `portal-tls/orders` it can also obtain a publicly trusted certificate for the hotel's guest-portal hostname.

**Decision needed (Product Owner, security):** bind activation to a secret the real box shows locally. For example:
- the identity-key fingerprint shown on the appliance's console and checked in the Activate dialog; or
- a short pairing code.

This changes the activation flow, so it is not a routine fix.

## Medium

| # | Finding | Location | Status |
|---|---|---|---|
| M1 | **A rate-limit key can lose its expiry.** `INCR` and `EXPIRE` are separate calls and the `EXPIRE` error is ignored. One failure leaves `rl:login:<address>` without a time-to-live, so that address (a whole hotel or office behind NAT) is refused sign-in and registration until someone deletes the key by hand. Fix: an atomic `INCR` + `EXPIRE NX` pipeline or a script. | `internal/api/ratelimit.go:35-44` | Confirmed |
| M2 | **"Reissue certificate" with a waiting CSR leaves the old certificate usable for 24 h.** The old certificate is only marked `superseded`. `mtlsCertBinding` accepts superseded certificates for `SupersededOverlap` (24 h). An operator reissuing because a key leaked expects it revoked now; the other branch of the same action does revoke. | `internal/api/appliance_ops.go:608-654`, `certificates.go:136`, `appliance_mtls.go:102` | Confirmed |
| M3 | **A failure to re-sign the trust registry is swallowed.** `resignRegistry` ignores `Rebuild`'s error, and does nothing when the registry root key is not loaded. Moving a signing key to verify-only, or revoking it (including emergency), reports success while appliances keep the old registry that still trusts the key. | `internal/api/assignment_keys.go:45-51`, `cmd/ctrlapi/main.go:323-335` | Confirmed |
| M4 | **Customer users see Central staff emails and internal reasons.** The appliance history on `GET /cloud/v1/appliances/{id}` joins `operators` and returns the platform operator's email and the free-text reason of every audited action, to every customer role including `viewer`. Product decision: is this intended? | `internal/api/fleet.go:295-315` | Confirmed |
| M5 | **The customer audit view shows platform staff ids and IP addresses.** Platform actions on a customer are stored with that customer's id, and `listAudit` returns `actor_id`, `ip` and `payload` to the customer's own users. | `internal/api/system.go:230-304` | Confirmed |
| M6 | **Anyone who knows an account's password can lock its two-step step for up to 24 h.** Wrong codes are counted per account, not per account and address. The password lockout was deliberately made per address to prevent exactly this. | `internal/auth/mfa.go:197-212` | Confirmed |
| M7 | **WAN MAC rebind accepts any string and is not atomic.** `new_mac` is only lower-cased, then signed into the licence as the hardware binding. A typo makes the box reject its own licence. The MAC is committed before the licence is re-signed, with an unchecked compensating write. Fix: `net.ParseMAC` and one transaction. | `internal/api/appliance_ops.go:534-598` | Confirmed |
| M8 | **Deleting an appliance runs irreversible steps outside one transaction.** Certificate revocation, licence revocation and DNS/certificate release each commit separately under a 10 s deadline; only the last DELETE re-checks state. If an activation lands between the unlocked check and the DELETE, a just-activated appliance loses its certificate, licence and hostname but is not deleted. | `internal/api/appliance_ops.go:682-771` | Confirmed (narrow window) |
| M9 | **No request-body limits.** Sign-in and re-authentication decode an unbounded body. `DecodeJSON` (every operator write) has no `MaxBytesReader`. Names, reasons, passwords and emails have no maximum length, and names are signed into assignments. | `internal/http/auth_handlers.go:113,177`, `internal/api/common.go:131-139` | Confirmed |
| M10 | **The servers have no read, write or idle timeouts.** The internet-facing mutual-TLS listener on 9443 sets only `ReadHeaderTimeout`, and the chi timeout middleware is not on that router. Slow bodies and half-open TLS handshakes are unbounded. The 443 server has the same gap but sits behind Caddy. | `internal/http/mtls.go:41-51`, `cmd/ctrlapi/main.go:294-298` | Confirmed |
| M11 | **Unauthenticated registration has no cap or field validation.** Anyone can create unlimited waiting appliances (30 per minute per address). Serial, hostname, model and MAC values are stored unvalidated (up to 64 KB). The serial is put into a `Content-Disposition` filename unescaped, so a `"` breaks the header. | `internal/api/register.go`, `offline_activation_api.go:331` | Confirmed |
| M12 | **Portal certificate orders can retry for ever after a crash, and shutdown can lose a result.** Attempts are counted only on a clean failure, so a restart loop re-runs an order without reaching `maxAttempts`. Completion uses the root context, which is cancelled on shutdown. A certificate issued just before shutdown is not recorded and is ordered again, which counts against Let's Encrypt limits. | `internal/portaltls/worker.go:185-207`, `cmd/ctrlapi/main.go:232` | Possible |
| M13 | **One mutex serialises every ACME and DNS action for up to minutes.** Deleting an appliance, "Test access" and "Apply CAA" wait behind a running order without honouring the request deadline. | `internal/portaltls/worker.go:231,511`, `admin.go:141,181` | Possible |
| M14 | **Audit rows can be lost after a committed change.** `audit.Op` writes with the request context, so a client disconnect or the 15 s deadline cancels the audit insert for an activation, retirement or user change that already committed. `audit.Op` also deletes `_tenant_id` from the caller's map. | `internal/audit/audit.go:32-82` | Confirmed |
| M15 | **Administrators can take over peer administrators.** A `tenant_admin` can reset another `tenant_admin`'s password and two-step sign-in and sign in as them; the same holds for `platform_admin` on the Team. It is step-up gated and audited. Product decision: should only owners reset peers? | `internal/api/users.go:111-123` | Confirmed |
| M16 | **Importing an offline activation request has no step-up and silently accepts a request id belonging to another appliance.** Package minting does not lock the request, so two concurrent calls mint two packages for one request. | `internal/api/routes.go:68`, `offline_activation_api.go:67-255` | Confirmed |
| M17 | **Signing-key files are trusted without checks.** No check that key files are mode 0600. Raw 64-byte Ed25519 keys are not checked for seed/public consistency. Nothing stops the registry-root key and the assignment key being the same file. `assignment-key --force` overwrites a key with no backup. | `cmd/ctrlapi/main.go:144-158,323-335`, `assignkey.go` | Possible |

## Low

| # | Finding | Location |
|---|---|---|
| L1 | The last-platform-administrator guard runs outside the transaction and treats a database error as "not last" (fails open). Two concurrent demotions both pass. | `internal/api/users.go:378-387` |
| L2 | The re-authentication mark ignores a Redis error but answers `{ok:true}`; the next step-up action then fails. | `internal/api/reauth.go:18-24` |
| L3 | `IssueTx` ignores the previous-version read error; any error but "no rows" issues version 1 over a higher current version. | `internal/api/assignment_api.go:108` |
| L4 | `licenses/{id}/revoke` takes no per-appliance lock (issue does) and does not release the portal hostname, unlike retirement. Suspend and resume check, then act, without a lock. | `internal/licensing/licensing.go:300-312`, `api/licenses.go:153-217` |
| L5 | `revocation_delivered_at` is set once the response is encoded, not when the appliance confirms; it gates site deletion. | `internal/api/licenses.go:258-276` |
| L6 | `ReconcileTerminalTimeouts` flips the state, then inserts the alert, outside a transaction. A failed insert means the alert is never raised. | `internal/api/terminal_delivery.go:239-275` |
| L7 | The trust, fleet and appliance-detail pages hide database errors and return empty sections with HTTP 200. `strictMTLSSelf` turns a database outage into "certificate not active". | `internal/api/system.go:103-212`, `fleet.go`, `terminal_delivery.go:352-378` |
| L8 | Each CSR submission stores a new pending row with no cap (1 MB, 240 per minute). Two concurrent issuances can leave two active certificates. A retiring appliance still gets automatic rotation. | `internal/api/certificates.go:25-80,114-170` |
| L9 | `replace` has no row lock or state re-check, so it can mark a retiring appliance. | `internal/api/appliance_ops.go:490-528` |
| L10 | `valid_days` overflows `time.Duration` above about 106,000 days; licence terms have no upper bounds. | `internal/api/licenses.go:69` |
| L11 | The appliance client-certificate CSR accepts any key type and size (portal CSRs are restricted). | `internal/pki/ca.go:234-246` |
| L12 | Failed-authentication audit rows are built with a hand-written JSON escaper that misses control characters, so the row is silently dropped. A non-uuid `iss` becomes an unaudited 500. | `internal/auth/appliance.go:137-175` |
| L13 | Every insert failure is reported as "already exists" (customer, site, user), which hides outages. Because emails are globally unique, a customer administrator can test whether an address exists anywhere on Central. | `internal/api/customers.go:96-99,145-149`, `users.go:187-193` |
| L14 | The site `code` is not validated (the customer slug is). A tenant administrator can restore a site of an archived customer. | `internal/api/customers.go:107-150,647-668` |
| L15 | The public `/readyz` returns the version; `/cloud/v1/backup-health` returns host file paths and raw error text to `platform_support`. | `internal/http/router.go:174-193`, `api/backup_health.go` |
| L16 | The intermediate certificate is not checked against its key and the root at startup. `relocateRootOffline` moves the root key to another directory on the same host, and fails across file systems. | `internal/pki/ca.go:61-68`, `cmd/ctrlapi/main.go:345-361` |
| L17 | The ACME renewal window from the CA is used without clamping it before `NotAfter`. | `internal/portaltls/ari.go:111-115` |
| L18 | The daily portal-order cap is count-then-insert (a race). Several portal-TLS loops ignore row-scan errors and `rows.Err()`. | `internal/portaltls/orders.go:80-92`, `worker.go` |
| L19 | Long-lived goroutines (housekeeping, portal TLS worker) have no panic recovery, so one panic stops Central. | `cmd/ctrlapi/main.go:177,232,256` |
| L20 | The in-memory replay cache is unbounded once full, scans the whole map on each insert, and empties on restart (a 65 s replay window after a restart). | `internal/applianceauth/jwt.go:177-195` |
| L21 | Operational values are hard-coded: rate limits (20/30/240 per minute), step-up lifetime (5 min), replacement window (72 h), retirement acknowledgement timeout (10 min), superseded-certificate overlap (24 h), client-certificate lifetime (90 days). Rule §0C asks for settings where an administrator may need to change them; Product Owner to decide which. | various |
| L22 | `clientip` trusts forwarding headers only from loopback. If Caddy ever reaches ctrlapi over a Docker bridge, every client shares one rate-limit and lockout bucket. Confirm the deployed path. | `internal/clientip/clientip.go` |

## Clean code

1. **Dead code:**
   - `api.(*Base).issueAssignment`, `pki.LoadOrCreate` and `selfSign`.
   - Appliance-side verifier code compiled into ctrlapi (`assignment.Store`, `offline.AcceptFor`, `activation.AcceptForFirstActivation`).
   - `portaltls` test DNS (`Challtestsrv`) selectable in production by an environment variable.
2. **Duplicates:**
   - Two UUID generators (`assignUUID`, `newUUIDv4`) plus `licensing.newUUID`.
   - Three atomic-write helpers.
   - Three raw-key loaders.
   - Three one-method DB interfaces (`querier`, `Querier`, `alertDB`).
   - `clientIP` and `clientIPFromReq`; `emailOf` and `actorOf`.
3. **Overlong functions:** `serve()` in `main.go` is about 240 lines with a `goto`; `activate` is about 160 lines.
4. **`id::text = $1`** comparisons in licensing and portal TLS defeat the primary-key index; cast the parameter instead.
5. **`customerRows`** loads the whole fleet to count per customer. Listing limits of 500 and 2000 truncate silently.
6. **`WriteErr`** is a legacy duplicate of `Fail`.
7. **`metrics.statusRecorder`** hides `Flusher` and `Hijacker` from wrapped handlers.
8. **`StrictApplianceAssignmentHandler`** answers every refusal with 204, so a fault and a refusal look the same to the appliance. A variable named `idFpr` holds a public key.
9. **Misleading comment:** the startup comment on the CA (see H1).

## Checked and rejected

- **"`newUUID` returns a predictable UUID if randomness fails."** Rejected: since Go 1.24, `crypto/rand.Read` never returns an error.
- **"Deleting a customer races with a new site and cascades it away."** Rejected: migration 0037 made those foreign keys `RESTRICT`.
- **"`peekIss` reads the first `iss`, JSON the last."** Rejected: the identity is taken from the key that verified the signature, so a second `iss` gains nothing.

## Not verified

- **Behaviour against a real database.** The 33 database-backed tests did not run (Phase 3).
- **The deployed Caddy-to-ctrlapi path** (L22) and the file modes of keys on Central.
- **ACME and Cloudflare behaviour under failure** (M12, M13). Proving these needs the Pebble integration test or a staging zone.

## Decisions for the Product Owner

1. **H2:** add a local secret (fingerprint check or pairing code) to activation.
2. **M4, M5:** should customer users see Central staff emails, reasons and IP addresses?
3. **M15:** may administrators reset peer administrators, or only owners?
4. **L21:** which hard-coded operational values become settings?
5. **P0-2:** move to a supported Go release (1.26.x or 1.27.x) and update the listed modules. This is a dependency and toolchain change across both services.
