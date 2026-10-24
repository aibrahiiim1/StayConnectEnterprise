# Phase 2: Guest sign-in (scd)

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE.

**Covers:**
- The sign-in paths a guest device reaches through the Client Portal:
  - **room:** `/v1/phase3/auth/pms/resolve` and `/grant`;
  - **voucher:** `/v1/sessions/authorize`;
  - **one-time code:** `/v1/auth/otp/issue` and `/v1/sessions/authorize-otp`;
  - **guest account:** `/v1/sessions/authorize-credentials` and `/v1/auth/account/register`;
  - **social:** `/v1/sessions/authorize-social`;
  - **open access:** `/v1/sessions/authorize-open`;
  - **returning device:** `/v1/sessions/authorize-resume`.
- The session activation they share (`reserveLicensedSlot`).
- The items earlier phases handed on: Phase 0 H1 and H2; Phase 1 D1-H1, D1-M1 and D1-M3.

**How:**
- **Code:** every database round trip, lock and external call on each path was traced in the code (`cmd/scd`, `internal/iamv2`, `throttle`, `otp`, `pmsresolve`, `staygrant`, `authctx`).
- **Password hash:** Argon2id was benchmarked with the appliance's exact parameters, limited to 2 CPUs (`GOMAXPROCS=2`), at 1 to 30 sign-ins at once.
- **Database:**
  - a throwaway full-schema database was built, limited to 2 CPUs and 4 GB with the reference settings;
  - it was given one year of generated sessions (732,000) and 400,000 devices;
  - the licence-capacity count was timed on it;
  - the durable throttle's transaction was load-tested with `pgbench` at 1, 4 and 12 clients.
- **Compliance:** checked against the [README](README.md#compliance) rules: GDPR Art. 32, OWASP ASVS V2, NIST SP 800-63B, and the privacy programme in `docs/privacy/`.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 1 | 4 | 5 | 2 |

**Main result.** Sign-in is fast enough at the design load, about 5 sign-ins a second, on every path measured.

It is fragile under bursts, for two reasons:
1. **Password hashing has no limit on how many run at once.** Each hash takes about 125 MB. A burst the sign-in throttle allows can use up the appliance's memory (D2-H1). Anyone on the guest Wi-Fi can cause that burst, without an account.
2. **scd has only 4 database connections** (Phase 1 D1-M3), and slow work holds them:
   - a password hash runs inside a database transaction;
   - a social provider's network call runs inside a transaction holding a row lock;
   - every session activation counts every session ever recorded under an appliance-wide lock (D2-M1, D2-M2).

**The fixes keep every security control.** The hash strength, the throttles and the equal time for unknown accounts all stay; they change only where the work runs and how much of it runs at once.

## What a sign-in costs today

Database round trips per successful sign-in, from the code trace. Each iam_v2 transaction also runs `begin_controlled_operation`, whose janitor (Phase 1 D1-H1) costs 0.35 ms with an idle table and about 48 ms per transaction once accounting fills it.

| Method | Round trips | Transactions | Other cost |
|---|---|---|---|
| Room | about 20–40 | 4–5 | The offer list is N+1 per package (D2-L3). There is a per-interface row lock (D2-M4). Session activation waits for netd (D2-L5). |
| Voucher | about 10 | 2 | |
| One-time code | about 25 across issue and verify | 4 | A synchronous SMS or WhatsApp send of up to 10 s; no connection is held during it. |
| Guest account | about 12 | 2 | 1 Argon2 hash inside a transaction. |
| Register | about 18 | 3 | 2 Argon2 hashes: one outside, one inside a transaction. |
| Social | about 15 | 3 | The provider exchange, up to 10 s, inside a transaction. |
| Open access | about 8 | 1 | |
| Returning device | about 8 | 2 | |

## Findings

### D2-H1: Password hashing has no limit on how many run at once (High, Measured)

**Where:**
- **Parameters:** `internal/passwordhash/passwordhash.go:17-22` uses Argon2id with 64 MiB, 1 pass and 4 threads.
- **Callers in scd:**
  - account sign-in: `internal/iamv2/adapters.go:25` (an unknown account), `:37`;
  - registration: `cmd/scd/self_service_handlers.go:157` (Hash), then a second Verify through the account sign-in;
  - password reset: `:680`;
  - accounts that are off: `BurnVerify` (`:489`, `credentials_handlers.go`).
- **The same parameters,** separately defined, are in `internal/poststay/poststay.go:41-43` (post-stay PIN) and `cmd/edged/auth.go:20-22` (staff).
- **No limit:** there is no semaphore or limiter anywhere around them.

**Measured** on 2 CPUs with the appliance's parameters:

| Hashes at once | p50 | p95 | Hashes a second | Peak memory |
|---|---|---|---|---|
| 1 | 28 ms | 28 ms | 38 | 138 MB |
| 2 | 48 ms | 55 ms | 40 | 266 MB |
| 5 | 109 ms | 281 ms | 39 | 647 MB |
| 10 | 221 ms | 760 ms | 37 | 1.29 GB |
| 30 | 908 ms | 1.59 s | 36 | **3.72 GB** |

Throughput is fixed at about 38 hashes a second whatever the concurrency. Memory grows by about 125 MB for each hash running: 64 MiB of Argon2 memory, plus Go's heap headroom before the garbage collector returns it.

**Problem.**
- **The durable throttle allows a burst.** It allows 600 attempts per minute appliance-wide, and 30 per minute per address or device (`cmd/scd/authsec.go:25-27`). All 600 can arrive in the first second of a window.
- **Spreading the attempts is easy.** A client on the guest network can change its device address, so 20 addresses give the full 600.
- **Every attempt costs a hash, so no account is needed.** Unknown accounts and accounts that are off still burn a full hash. This is deliberate, so timing does not reveal which accounts exist.
- **Registration costs two hashes.**
- **What protects the server today:** only the 4-connection pool, which by accident caps the hashes running inside a transaction. Registration's first hash and `BurnVerify` run outside any transaction, so nothing caps them.

**Impact.**
- **About 30 hashes at once take the whole 4 GB appliance.** The kernel then kills processes, and nothing in the deploy files says which. Guest sign-in, enforcement and accounting are all on that box.
- **Anyone on the guest Wi-Fi can trigger it** with a burst of registration or sign-in requests, without an account.
- **Even a busy legitimate morning slows everything.** At 10 hashes at once, other sign-ins wait behind 760 ms hashes.

**Recommended fix.**
1. **Limit how many hashes run at once.** One process-wide limiter in `internal/passwordhash` caps concurrent hashes at 2 per CPU (4 on the reference appliance) and covers Hash, Verify, BurnVerify and Dummy.
2. **Refuse rather than queue for long.** A request that cannot get a slot within about 2 s is refused with "busy, try again" (HTTP 503 with `Retry-After`).
3. **Use the same limiter** for post-stay PINs and in edged.
4. **Set a soft memory limit.** Set `GOMEMLIMIT` for scd, so the garbage collector returns hash memory promptly instead of letting the heap double.
5. **Keep the parameters.** 64 MiB, 1 pass, 4 threads is above OWASP's Argon2id minimum. A lighter setting could be an option later (see Decisions), but no fix weakens the hash to gain speed.

**Proof.** 200 concurrent registration requests from 20 generated device addresses, on the reference size:
- scd's resident memory stays under 700 MB;
- no process is killed;
- refused requests get 503;
- a voucher sign-in run alongside completes in under 1 s.

**Compliance.**
- **GDPR Art. 32 and OWASP ASVS V2.4:** the hash strength is unchanged.
- **Unknown accounts:** the limiter applies to them in the same way, so their timing still matches a wrong password (ASVS V2.2, user enumeration).
- **The refusal counts:** a "busy" refusal is a throttled attempt and is still recorded where attempts are recorded today.

**Batch:** B1. CONTROLLED (guest authentication).

### D2-M1: Slow work runs inside database transactions and holds scd's connections (Medium, Confirmed)

**Where:**
- **Account sign-in:** the password Verify runs inside `repo.WithTx` (`internal/iamv2/adapters.go:18-46`). So does registration's second Verify.
- **Social sign-in:** `cmd/scd/social_handlers.go:139` opens a transaction and locks the sign-in state row `FOR UPDATE`. It then calls `prov.Exchange`, the provider's token exchange and ID-token check, and only then commits. The provider HTTP clients allow up to 10 s (`internal/social/google.go:56`, `apple.go:87`, `facebook.go:56`, `microsoft.go:82`).

**Problem.** scd has one pool of 4 connections (Phase 1 D1-M3). That pool carries:
- every sign-in method;
- session activation;
- the throttle;
- every background loop.

Each of the cases above holds a connection for the duration of the slow work: 28 ms to 1.6 s for a hash (D2-H1), up to 10 s for a provider.

**Impact.**
- **Four slow social sign-ins at once leave scd without a database connection** until they finish, for example when a provider has a slow minute. Four bursty account sign-ins do the same.
- **Every other guest waits meanwhile,** room sign-ins included, and so does the reaper. Requests then hit the 10 s handler timeout.

**Recommended fix.**
1. **Account sign-in:**
   - read the account in a short read;
   - verify the hash outside any transaction (through D2-H1's limiter);
   - open the transaction only to create the device and auth context;
   - re-check in it that the account's state, lockout and password version have not changed.
2. **Social sign-in:**
   - consume the state row in a short transaction first, marking it used so it cannot be replayed;
   - call the provider with no transaction open;
   - then continue.

   Only the replay protection needs the row lock, and marking the row used gives the same protection.
3. **Pool size:** size scd's pool as Phase 1 D1-M3 recommends.

**Proof.**
- With a provider double that takes 8 s, 10 concurrent social sign-ins do not delay a voucher sign-in by more than 100 ms.
- Pool acquire wait p95 stays under 5 ms during the D2-H1 burst.

**Compliance.** The account re-check keeps the lockout and validity rules (ASVS V2.2). Social sign-in keeps its single use of the state value (OAuth 2.0 state, RFC 6749 §10.12).

**Batch:** B2. CONTROLLED (guest authentication).

### D2-M2: Every session activation counts every session ever recorded, under an appliance-wide lock (Medium, Measured)

**Where:** `cmd/scd/iamv2_session_ops.go:111-128`, `reserveLicensedSlot`. It is called by every session activation:
- `iamv2_session_activate.go:193` (vouchers, codes, accounts, social, open, returning devices);
- `phase3_auth.go:880` (room);
- `phase5_poststay.go:233` (post-stay).

It takes `pg_advisory_xact_lock` on the appliance id, then counts `iam_v2.sessions JOIN iam_v2.devices` where the session is open. It does this whenever the licence caps concurrent guests, as PRE-LIVE's does (2,000).

**Measured,** with one year of sessions (732,000) and 400,000 devices:

| | Time |
|---|---|
| Today | **50–82 ms**: a scan of every session |
| With a partial index on open sessions | 11 ms |

The partial index was: `CREATE INDEX ON iam_v2.sessions (device_id) WHERE ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT')`.

**Problem.**
- **The lock serialises activations.** It is held from the count until the activating transaction commits, so all activations on the appliance run one at a time. That covers every method.
- **There is a ceiling.** It is about 12–20 activations a second today, and falls as history grows: about 150–250 ms per count after three years, which is 4–6 a second.

**Impact.**
- At the design load (5 a second) activations queue only at peaks.
- At the stress load, or after a few years of history, they queue continuously. Guests wait seconds after a correct sign-in, and requests reach the 10 s timeout.

**Recommended fix.**
1. **Now:** add the partial index above, in the same migration as Phase 1 B1.
2. **Then:** keep the open-session count in one counter row, updated in the same transaction as each session opens and closes, and locked instead of the advisory lock. The check becomes a single-row read, and the licence cap stays exact.

**Proof.** With three years of generated sessions, 50 parallel activations finish within 2 s, and the cap is never exceeded in a concurrency test at the limit.

**Compliance.** None beyond availability. The licence cap is a contract control, and it stays exact.

**Batch:** B3. CONTROLLED (database schema; licensing enforcement).

### D2-M3: The appliance-wide sign-in ceiling is fixed in code, and one-time codes use it twice (Medium, Confirmed)

**Where:** `cmd/scd/authsec.go:21-28`. The ceiling is 600 attempts per minute, for all methods and clients together (`throttleEndpointPerWindow`). It is charged by:
- voucher, account and social sign-in;
- registration;
- both the issue and the verify of a one-time code.

**Problem.**
- **One-time-code sign-ins count twice.** So at the design load of 300 sign-ins a minute, a hotel whose guests use one-time codes reaches the ceiling exactly, and every guest is then refused until the minute ends. At the stress load, most are refused.
- **The number is an operational limit fixed in code,** against CLAUDE.md §0C.

**Impact.** At a busy check-in time, guests are refused with "try again later" while the appliance is idle.

**Recommended fix.**
1. **Make the ceiling a setting** (§0C: default, unit, explanation, validation, audit). Derive its default from the licence's concurrent-guest cap, for example 1 attempt per licensed guest per 2 minutes, with a floor of 600 a minute.
2. **Charge a one-time-code sign-in once** against the appliance-wide damper, at issue. Each client's own limits still apply to both steps.

**Proof.** A generated storm of 300 one-time-code sign-ins in a minute completes with no refusals, and 2,000 attempts from one address are still blocked by that address's own limit.

**Compliance.**
- **The ceiling is an anti-automation control** (OWASP ASVS V2.2.1; NIST SP 800-63B §5.2.2, which requires limiting failed attempts).
- **The fix keeps it.** Every per-client and per-identity limit stays as it is. The setting's lowest allowed value keeps the appliance protected.

**Batch:** B4. **DECISION** (the default ceiling). CONTROLLED (guest authentication).

### D2-M4: Room sign-ins at one PMS run one at a time (Medium, Suspected)

**Where:**
- **Resolve:** `iam_v2.issue_or_return_pms_context` locks `FOR UPDATE` the auth context, the stay, and **the PMS interface's runtime row**.
- **Grant:** the transaction (`cmd/scd/phase3_auth.go:737` onward, `internal/authctx/authctx.go` `ConsumeTx`) takes the interface runtime row again through `iam_v2.lock_pms_interface_runtime` (migration 0058), for the whole transaction.
- **Inside that transaction:** the package lookups, the purchase and entitlement inserts, the licence check (D2-M2) and the session insert.
- **pmsd** takes the same row lock in its admission path.

**Problem.** A hotel with one PMS interface has one such row, so every room sign-in's resolve and grant run one after another.

**The lock is deliberate.** It makes the feed-health check and the grant one decision ("linearization"). What it costs depends on how long those transactions take, which needs real room-sign-in data to measure.

**Impact** (Suspected).
- **Throughput is capped** at room sign-ins a second = 1 ÷ grant transaction time. With D2-M2 and the Phase 1 D1-H1 janitor included, that may be 100–200 ms per grant, so 5–10 a second, around the design load.
- **A PMS resync holds the same lock.** If pmsd holds it during a resync, room sign-ins wait for the resync (Phase 6).

**Recommended fix.**
1. **Measure first.** The Phase 11 harness drives 300 room sign-ins a minute against generated stays and records lock waits (`pg_locks`, `log_lock_waits`).
2. **If the lock limits throughput:**
   - shorten what runs under it: the licence counter from D2-M2, the janitor fix from D1-H1, the package lookups moved before the lock;
   - or take it in share mode for grants. A grant only needs the feed health not to change underneath it, while pmsd's health update keeps the exclusive lock.

**Proof.** In the room-sign-in storm, lock wait p95 is under 20 ms, and the feed-health refusal tests still pass.

**Compliance.** None beyond availability. The linearization is a correctness control and stays.

**Batch:** B5, after the Phase 11 measurement. CONTROLLED (guest authentication; PMS).

### D2-L1: The durable throttle costs 6–9 statements per attempt and shares one row (Low, Measured)

**Where:** `internal/throttle/throttle.go:172-245`. It is one transaction with, per rule (endpoint, address, device and identity), a `SELECT max(blocked_until)` and an upsert. The global endpoint row is upserted by every attempt on the appliance.

**Measured** with `pgbench`, reproducing the voucher path's three rules:

| Clients | Throughput | Time per attempt |
|---|---|---|
| 1 | 164 a second | 6.1 ms |
| 4 | 323 a second | 12.4 ms |
| 12 | 243 a second | **49.3 ms**: throughput falls as attempts wait for the shared row |

**Impact.**
- **Ample for the design load:** 5 sign-ins a second is far below these rates.
- **Under the D2-H1 burst,** the shared row adds tens of milliseconds to every attempt.

**Recommended fix.** Move the rules into one database function, a single round trip, that updates the shared row last.

**Compliance.** The throttle stays durable across restarts, as the backend audit's N-27 requires.

**Batch:** B4.

### D2-L2: Settings are read from the database on every sign-in (Low, Confirmed)

**Where.** Read on every request, with no cache:
- `guestMethodSwitches` (`guest_method_switch.go:48`, `tenantcfg.go:73`);
- `resolveNetwork` (`netcontext.go:20`), once or twice per request;
- `readProviderReadiness` (`method_readiness.go:18`, two queries per one-time-code issue);
- `rememberDeviceDays` (`client_resume.go:62`), twice per one-time-code or social sign-in.

**Impact.**
- **5–8 extra round trips per sign-in** on a 4-connection pool.
- **About 1–3 ms of the sign-in time.** That is small alone, but it adds to the pool pressure in D2-M1.

**Recommended fix.**
- Cache the four reads for a few seconds, the way the self-service settings already are (`self_service.go`, a cache with a TTL).
- Clear the cache when the setting's version changes.

**Compliance.** The caches hold settings, not guest data (GDPR Art. 25).

**Batch:** B6.

### D2-L3: The room offer list runs a few queries per package (Low, Confirmed)

**Where.** `cmd/scd/phase3_offers.go:102`, `offersFor`:
- for each candidate package, it runs separately the eligibility rules, the prior-purchase check, the tiers, the acquisition methods and the room-charge test (`phase3_paid.go:35`);
- it then records each offer with its own `record_auth_context_offer` call (`:333`).

**Impact.**
- **About 3 + 3–5 queries per package**, run serially.
- **A hotel with 8 packages pays about 35 round trips per room sign-in,** several inside the per-interface lock of D2-M4.

**Recommended fix.** One set-based query returns the offers with their rules and tiers. A single call records all offers.

**Proof.** The room-sign-in query count is independent of the number of packages.

**Batch:** B5.

### D2-L4: The last step of sign-in waits for netd by polling (Low, Confirmed)

**Where.** `cmd/scd/phase3_auth.go:972-1010`, `awaitEnforced`:
- after the grant commits, scd polls the session's state every 100 ms, for up to 8 s, until netd's producer has enforced it;
- the producer runs about once a second.

**Impact.**
- **Each sign-in waits 0.1–1 s or more** after the grant.
- **It adds up to 10 queries per sign-in:** 50 a second at the design load.

**Recommended fix.**
- Have netd's producer `NOTIFY` when it activates a session, and wait on that notification, with the poll as the fallback.
- Phase 4 measures the producer's cadence.

**Batch:** B6, with Phase 4.

### D2-L5: scd accepts any number of requests at once (Low, Confirmed)

**Where:** `cmd/scd/main.go:975`. The server sets only `ReadHeaderTimeout` (5 s), with a 10 s handler timeout (`:779-787`). It listens on a Unix socket reached through portald.

**Impact.** Nothing stops scd from taking on more work than its 4 connections and 2 CPUs can do. Excess requests wait inside scd until the 10 s timeout, rather than being refused early.

**Recommended fix.** Cap requests in flight on the guest routes, for example at 64, refusing beyond that with 503. portald shows it as "busy, try again" (Phase 3).

**Note.** The backend audit's pattern P1 (server timeouts) covers the timeouts; this finding is only the concurrency cap.

**Batch:** B6.

### D2-CC1: The Argon2 parameters are defined three times (Clean code, Confirmed)

**Where:** `internal/passwordhash`, `internal/poststay/poststay.go:41-43` and `cmd/edged/auth.go:20-22` each define 64 MiB, 1 pass and 4 threads.

**Recommended fix.**
- poststay and edged use `internal/passwordhash`, so the parameters, the D2-H1 limiter and any future change live in one place.
- Existing hashes stay readable, because the parameters are encoded in each hash.

**Batch:** B1.

### D2-CC2: Each sign-in method repeats the same lookups (Clean code, Confirmed)

**Where:** each handler separately resolves the network, the method switches, the licence gate and the settings (see the table above).

**Recommended fix.** Load one per-request context once, at the start of a sign-in, and pass it down. It makes D2-L2's cache one change, and the round-trip count visible in one place.

**Batch:** B6.

## Items from earlier phases

| Item | Outcome |
|---|---|
| Phase 0 H1 and Phase 1 D1-M3, pool size | Confirmed. One pool of 4 serves all of scd. Slow work holding it is D2-M1. |
| Phase 0 H2, Argon2 memory | Measured: about 125 MB per hash, unbounded (D2-H1). |
| Phase 1 D1-H1, the janitor on every controlled operation | Confirmed on every sign-in path: each iam_v2 transaction calls `begin_controlled_operation`, and room sign-in runs 3–4 of them. Fixed by Phase 1 B1; not counted again. |
| Phase 1 D1-M1, live-session lookups | Sign-in's own session queries are by id or by the licence count (D2-M2), which needs its own partial index. |

## Handed on

| To | What |
|---|---|
| Phase 3 | How portald shows "busy, try again" (D2-H1, D2-L5); its own timeouts toward scd. |
| Phase 4 | The producer cadence that sets D2-L4's wait. |
| Phase 6 | Whether pmsd holds the interface runtime lock during a resync (D2-M4). |
| Phase 10 | scd's memory limit and `GOMEMLIMIT` in the appliance budget (D2-H1). |
| Phase 11 | A room-sign-in storm against generated stays, to measure D2-M4. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D2-H1, D2-CC1 | **Do first.** Small, and it removes the memory exhaustion. CONTROLLED. |
| **B2** | D2-M1 | Hash and provider call outside transactions. CONTROLLED. |
| **B3** | D2-M2 | Its index ships with Phase 1 B1. The counter row follows. CONTROLLED. |
| **B4** | D2-M3, D2-L1 | After the ceiling decision. CONTROLLED. |
| **B5** | D2-M4, D2-L3 | After the Phase 11 measurement. CONTROLLED. |
| **B6** | D2-L2, D2-L4, D2-L5, D2-CC2 | The per-request context, NOTIFY and the request cap. |

## Decisions for the Product Owner

| # | Decision | Recommended |
|---|---|---|
| D2-M3 | The default appliance-wide sign-in ceiling. | A setting whose default follows the licence's concurrent-guest cap, never below 600 a minute. |
| D2-H1 (later) | Keep Argon2id at 64 MiB, 1 pass, 4 threads, or move to OWASP's lighter recommended setting (19 MiB, 2 passes, 1 thread)? The lighter one measured 82 hashes a second and 1.2 GB at 30 at once, against 36 and 3.7 GB. | Keep the current setting, add the limiter. Revisit only if Phase 10 shows memory is still short; both settings meet OWASP. |

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D2-H1 | GDPR Art. 32; OWASP ASVS V2.4, V2.2; OWASP Password Storage Cheat Sheet | The hash strength is unchanged, and unknown accounts keep equal timing. A "busy" refusal is recorded like a throttled attempt (`HOTEL_RECORD_OF_PROCESSING_TEMPLATE.md` row 5, sign-in attempts). |
| D2-M1 | ASVS V2.2; RFC 6749 §10.12 | The lockout and validity are re-checked inside the transaction, and the social state value is still single-use. |
| D2-M3, D2-L1 | ASVS V2.2.1; NIST SP 800-63B §5.2.2 | Per-client and per-identity limits stay as they are. The ceiling becomes configurable, with a protective floor, and stays durable. |
| D2-L2 | GDPR Art. 25 | Caches hold settings only, never guest data. |
| D2-M2, D2-M4, D2-L3, D2-L4, D2-L5 | Availability (Art. 32(1)(b)) | Licence and feed-health correctness stay exact. |
| All | GDPR Art. 5(1)(c) | No fix adds new logging of what guests type. Load tests use generated identities only. |

## Tests run

| Test | Result |
|---|---|
| Argon2id, the appliance's parameters, 2 CPUs, 1/2/5/10/30 at once; OWASP's two lighter settings at 30 | D2-H1 |
| Full-schema throwaway database (migrations to 0150), 2 CPUs, 4 GB, reference settings; 732,000 sessions, 400,000 devices | Built |
| Licence-capacity count, three runs, then with a partial index | D2-M2 |
| Durable throttle transaction, `pgbench`, 1, 4 and 12 clients, 15 s each | D2-L1 |
| Code trace of the 11 sign-in routes (database round trips, locks, external calls) | D2-M1, D2-M4, D2-L2 to D2-L5 |

**Cleanup.**
- **Docker:** the test database container was removed with its volume (`docker rm -f -v`). Docker is back to its state before the phase: no audit containers and 16 volumes.
- **Scratch files:** the benchmark program and its scratch folder were deleted.
- **Image:** the TimescaleDB image was already present and was kept.
