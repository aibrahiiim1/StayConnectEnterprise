# Backend audit — Phase 6: payments, room charges and accounting

| | |
|---|---|
| Audited commit | `b00410b5` (master; merged into the audit branch before this phase). It differs from Phase 5's commit only by wording in four scd files. |
| Scope | `data-plane/cmd/acctd` and `data-plane/internal`: payment (with stripe and paymob), checkout, posting, postinghandoff, poststay, transfer, shape, shapeplan, shapeproducer. About 11,200 lines of non-test Go. Also scd's card-checkout bridge (`cmd/scd/card_checkout.go`) and posting worker (`posting_worker.go`), and the confirm path for Phase 5 M2 (`internal/iamv2/commerce_flow.go`, `cmd/scd/phase3_paid.go`). `internal/ledger` is the erasure ledger and was audited in Phase 4. |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |

## How to use this file

1. **Format.** It is the same as Phases 1–5. Each finding has a problem, its impact, the recommended fix and the test that proves it. Fixes are grouped into batches at the end.
2. **Almost all of this is CONTROLLED.** It covers payment, financial posting, settlement, reversal, entitlement and enforcement (CLAUDE.md §0). Each batch names its area. No fix here may send PMS, payment-provider or financial traffic outside the test doubles without its own authorization.
3. **Status of a finding:**
   - **Confirmed**: the code path was traced.
   - **Possible**: the code suggests the problem, but it needs a test, the provider's behaviour or a Linux kernel to prove.
4. **How it was checked.**
   - The auditor read the commerce confirm path, scd's card-checkout bridge, the posting worker and engine, acctd's plan counter, and the shaping re-rate path.
   - Two independent reviewers read the rest in two slices: payment and checkout; and posting, post-stay, transfer, shaping and acctd.
   - Every claim of theirs listed here was re-checked; the rejected ones are at the end.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 4 |
| Low | 15 |
| Clean code | 4 |

Phase 5 M2 is settled here (no money moves) and lowered to Low in the Phase 5 file.

### What was run

| Check | Result |
|---|---|
| `go vet` (all Phase 6 packages) | clean |
| `staticcheck` | 2 unused items in acctd (CC1), 1 nil-context test call |
| `gosec` | 11 leads, triaged below. G115: byte counters and argon2 parameters already bounded. G703/G304: the restore-marker path is fixed or set by the unit. G702/G204: acctd re-executes its own binary, and shaping runs `tc` and `ip` with an argument list, no shell. |
| `govulncheck` | only the Phase 0 P0-2 advisories |
| **Database integration tests** on a throwaway full-schema database. Built the same way as `scripts/phase4-pg-integration.sh`, with the real `svc_posting`, `svc_pmsd` and `svc_edged` roles and the payment runtime, outcome, operator and commerce logins. Run with `integration phase4`, then on a fresh database with `integration phase5`. Every provider and the PMS are in-process doubles; nothing left the machine. | payment 145 pass and 1 skip (Linux-only file permission test); checkout 30; posting 195; acctd 48; postinghandoff 2; shape 13. Then transfer, post-stay and checkout under `phase5`: 57. **478 pass, 0 fail.** |

`shapeplan` and `shapeproducer` have no tests of their own; the plan validator is reached through netd's and acctd's tests (CC4).

### Phase 5 M2: which confirm methods spend without a guest step

**None.** The portal's commerce cookie reaches only `ConfirmPurchase` (`internal/iamv2/commerce_flow.go:213-345`):

| Method | What confirm does | Money |
|---|---|---|
| Free (`NOT_REQUIRED`) | grants at once | none |
| Voucher (`PREPAID`) | allowed only when the sign-in itself was that voucher; burns it | the voucher the guest already typed |
| Card (`ONLINE_PAYMENT`) | opens a provider checkout; nothing is granted until the provider confirms the capture | only if someone pays on the provider's page |
| Room charge (`PMS_POSTING`) and anything else | refused (`method_not_available`), with the whole transaction rolled back | none |

Room charge is reachable only through the room sign-in grant (`cmd/scd/phase3_paid.go:100-230`). That grant consumes the sign-in against the connection's own device (`authctx.ConsumeTx` with the device portald derived from the IP and MAC), so a copied cookie or a sign-in id is not enough.

### What holds up well (keep it this way when fixing)

- **A charge is never sent twice by accident.**
  - The claim, the P# allocation and the attempt record are committed before the bytes go out (`internal/posting/engine.go:176-197`).
  - A crash mid-send leaves a SENDING attempt, which recovery turns into UNKNOWN.
  - An ambiguous answer (timeout, torn connection, wrong P#, an answer code the vendor never confirmed) is UNKNOWN. Nothing resends it until an operator authorises a retry, and the database enforces that.
- **The right room and the right amount.**
  - The reservation (G#) is pinned at purchase. The room is re-read under the stay lock at send time, and pmsd checks again.
  - Checkout, a stale revision or a blocked stay stops the charge before sending.
  - The amount is a positive integer in minor units, and the currency and exponent must match the interface, purchase and package.
  - Wire fields refuse `|` and control characters.
- **Card outcomes come only from the provider.**
  - Neither the guest nor the return page can mark a purchase paid. The status page asks the provider with the stored credentials (`cmd/scd/card_checkout.go:195-250`).
  - Outcomes are written through a separate database login (`sc_payment_outcome`).
  - A capture whose amount, currency or provider reference differs from the pinned purchase becomes UNKNOWN, never a grant (`internal/payment/checkout.go:369-374`).
  - Grants happen only for a settled settlement, re-checked under row locks.
- **Money arithmetic.** `int64` minor units throughout, with no floats. Exchange rates are kept as text.
- **Reversals are bounded.** OneGate never starts a refund. Provider refunds and chargebacks are bounded by a database trigger: they can never exceed the charge.
- **Provider credentials.**
  - Sealed with AES-256-GCM, bound to tenant, site, account and generation.
  - Provider error text is redacted.
  - LIVE mode is refused unless it is allowed, and key prefixes must match the account mode.
  - Calls have 20 s timeouts and a 4 MB response cap.
- **No transaction spans a provider call** (`checkout.go:175-208`).
- **Post-stay PIN.**
  - 8 random characters from 28 symbols (about 38 bits).
  - Hashed with argon2id (64 MiB, t=1, p=4) and a 16-byte salt, compared in constant time.
  - The stored hash parameters are bounds-checked.
  - The throttle is charged before argon2.
- **Transfer** locks both stays in id order, re-checks them under the lock, and moves device authorisations at one instant in one transaction.

## Medium

### M1 — The room-charge outbox retries with no backoff, and one refused charge blocks the PMS lane (Confirmed)

**Where.**
- `cmd/scd/posting_worker.go:68-110`: a 2 s ticker, with one `RunOnce` per lane on each tick.
- `internal/posting/repo.go:61-82`: `ClaimNext` takes the oldest `QUEUED` row, `ORDER BY o.id`.
- `internal/posting/engine.go:327-361` (`settle`): NOT_SENT returns the row to `QUEUED`.
- `engine.go:309-318` (`decline`): a refused row is released back to `QUEUED`.

**Problem.**
1. **NOT_SENT.** When pmsd or the PMS link is down, every tick allocates a new P#, writes a new attempt row and returns the row to the queue. That is about 43,000 P#s and attempt rows per lane per day, and a log line each time.
2. **Declined.** A row refused before sending (a currency or lifecycle gate) also goes back to `QUEUED`. Being the oldest, it is claimed again every tick.

**Impact.**
- A PMS outage of a day burns tens of thousands of P# values and fills `pms_posting_attempts` and the journal.
- A single row the gate refuses blocks every later room charge on that PMS interface. Every guest who chooses room charge waits on "pending" until an operator finds and clears the first row. Nothing about the blocked lane tells the operator why.

**Recommended fix.**
1. Add `not_before` and `fail_count` to `posting_outbox` (a CONTROLLED migration). On NOT_SENT and on a decline, set `not_before` with exponential backoff (2 s doubling to 5 min), and order the claim by `not_before, id`.
2. After N declines of the same row, park it in `HELD_RECOVERY` with a reason and an operator alert, so the lane moves on.
3. Show the oldest blocked row and its reason in the posting health the Admin Console already reads.

**Test.**
- With a transport double that always returns NOT_SENT, ten `RunOnce` calls inside a minute make at most two attempts.
- With a refused row at the head of a lane, the next row is posted.
- After N declines the head row is held, with the reason recorded.

### M2 — acctd's plan counter is not durable, and a lost counter freezes enforcement (Confirmed)

**Where.**
- `cmd/acctd/phase3_envelope.go:38-78`.
- netd keeps the last accepted generation and refuses a lower one: `internal/shapeplan/plan.go:157-164`, `cmd/netd/phase3_control.go:57-75`.

**Problem.**
- `next()` writes the new generation to a temporary file and renames it, with no `fsync` of the file or the directory. Every error is discarded, so a full disk means the generation is used but never saved.
- `start()` treats a missing or unreadable file as "never produced" and starts again from 0.
- netd stored the last generation it accepted, and refuses anything lower as `stale_generation`.

**Impact.** After a power cut that loses the last renames, or a disk-full period, a restarted acctd issues generations netd has already seen. Every plan is refused. acctd's own comment says what follows: a new guest is admitted on a short provisional lease that only the next plan renews. So new guests drop offline, and rate changes and expiries stop being applied, while both services keep running. The file is also rewritten every second, so the window is always open.

**Recommended fix.**
1. Write, `fsync` the file, rename, then `fsync` the directory. Return the error, and refuse to submit a plan whose generation was not saved.
2. On start, seed the generation as `max(saved, current Unix time in milliseconds)`. It then cannot go backwards, even with no file.
3. Optionally save once every N generations with a gap (save `gen + 1000`, use up to it). This removes the per-second write without weakening the guarantee.

**Test.**
- With the counter file missing, corrupt or older than netd's last generation, the first plan after start is accepted by netd's validator.
- A write failure stops plan submission and reports it in acctd's health.

### M3 — Provider reversal errors are swallowed, so a refused chargeback is never recorded or raised (Confirmed; over-recording Possible)

**Where.** `internal/payment/checkout.go:473-517` (`recordReversals`).
- The read of the amount already recorded (`:493`) and the `p4_record_provider_reversal` call (`:501`) both discard their errors.
- The amount already recorded is read outside the parent-row lock the database function takes.

**Problem.**
- **Refused chargebacks.** The database trigger refuses a reversal that would exceed the charge (refunds and chargebacks counted together). That refusal is dropped silently. Example: a partial refund, then the provider reports a lost dispute for the full amount. The chargeback is never recorded, and no review case is opened, because only DISPUTE events open one. The 6-hourly sweep repeats the same refusal forever.
- **Paymob refunds.** Paymob reports refunds as a running total, and the code turns it into a new amount by subtracting what was already recorded. If that first read fails, the subtraction starts from 0 and the whole total is recorded again under a new event id. Two passes running at once can both subtract from the same stale total.

**Impact.** The finance record can miss a chargeback, with no alert. Or it can over-record refunds, and then the refund policy (`refunded >= captured`) ends a guest's paid access although they were only partly refunded. Both are silent.

**Recommended fix.**
1. Move the running-total subtraction into a database function that does it under the existing parent-row lock (`p4_record_provider_reversal` given the provider's running total). A CONTROLLED migration.
2. Check and log every error. When the database refuses a reversal, open a `card_reversal_reviews` case, or report it in `FinancialHealth`.
3. Check `rows.Err()` in `SweepReversals` (`:520-562`).

**Test.**
- Two concurrent passes reporting running totals of 100 and 150 end with 150 recorded.
- A failed read records nothing.
- A 40 refund followed by a 100 lost dispute on a 100 charge opens a review case.

### M4 — Shared-allowance sessions are re-rated as single-device ones on every plan (Possible; confirmed on a real kernel in Phase 8 M4)

**Where.**
- `internal/shape/shape.go:306-326` (`ReRateSession`) and `:445-451` (`changeClass` with parent `RootParent`, and `rate` equal to `ceil`).
- Called for every active session on every plan, as the lease-renewal path: `cmd/netd/phase3_provision.go:148-160`.
- A SHARED session's class is created under its group class with a separate floor (`shape.go:212-243`).

**Problem.** The re-rate always names the root as parent and sets the guaranteed rate equal to the ceiling. For a SHARED session that is wrong on both counts. The kernel either refuses the change, or the session gets the whole ceiling as a guarantee and its share of the group is lost.

**Impact.** If the kernel refuses, every plan logs a re-rate failure for each shared session, and netd returns before `proveActive`. This is the lease-renewal path, so those guests may lose access each cycle. If the kernel accepts, a shared allowance stops being shared. No test re-rates a SHARED session: `internal/kerneltest` covers single-device only.

**Recommended fix.** Give `ReRateSession` the session's parent and floor (from the group, as `PrepareSessionIn` does), and add a kernel test that re-rates a SHARED session. This code is netd's: do it with the Phase 8 batch, on a Linux host.

**Test.** `kerneltest`: prepare two SHARED sessions under one group, re-rate one, and assert its parent and floor are unchanged and the group's total ceiling still holds.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | **A capture after a checkout was marked failed is never noticed.** Reconcile checks only pending charges and the reversal sweep only captured ones. Paymob reports a checkout with no transaction as expired 5 minutes after the intention's expiry, and that becomes FAILED at once. A card payment completing later is money taken with no access and no alert. UNKNOWN charges go to manual review, but they are not looked at again either. | `internal/payment/checkout.go:313-400`; `paymob/paymob.go:538-547` | A slow, read-only re-check of charges FAILED or UNKNOWN in the last N days. If the provider reports a capture, open a review case. Test with a double that reports a capture after the expiry. |
| L2 | **The dormant execute/notification path would grant without an amount check.** `applyTrusted` applies a signed CAPTURED with no amount or currency. It is unreachable today: `productionProvider` refuses to start with provider execution on, and nothing calls `HandleProviderNotification`, `CreateChargeIntent`, `CreateRefundIntent` or `Engine.Execute`. The test double, with its fixed signing secret, is compiled into the production build. | `internal/payment/engine.go:300-354`; `provider.go:15-19`; `testdouble.go:74` | Delete the dormant path and move the double to a `_test.go` file or a build tag; or, if it is kept, add amount and currency to the notification and check them. |
| L3 | **Stripe can fail to resolve an ambiguous create on a busy account.** Resolution lists at most 50 pages of 100 sessions over 30 days, so an account with more than 5,000 sessions in that time ends every ambiguous create as UNKNOWN. | `internal/payment/stripe/stripe.go:41-44`, `resolveByClientRef` | List from the transaction's `created_at` (`created[gte]`). Test with a double holding 6,000 sessions. |
| L4 | **Paymob ownership is not checked when the inquiry omits the order fields.** A transaction whose `merchant_order_id` and order id are absent is accepted because the inquiry returned it. | `internal/payment/paymob/paymob.go:564-569` | Require at least one of them to be present and to match; otherwise fail closed. |
| L5 | **Checkout start edge cases.** If saving the redirect fails after a successful create, the guest gets "pending" with no link until the reconciler resolves it. A concurrent second start hits the idempotency-key unique index, which `classify` does not recognise, so it returns a 500. | `internal/payment/checkout.go:216-221`; `engine.go` (`classify`) | Retry saving the redirect once; recognise the idempotency index as "already executing". |
| L6 | **Check-out boundary inputs.** On a stay that has already checked out, re-entry does not validate the stay event it is given, and records it as the boundary. A PMS check-out time far in the past is trusted, and can end paid access from before it began. | `internal/checkout/checkout.go:148-157`, `:342-349` | Validate the event in both branches. Clamp the boundary to no earlier than the stay's arrival or last applied event, and mark it suspect otherwise. CONTROLLED (entitlement). |
| L7 | **One slow PMS stalls the other lanes and recovery.** Lanes run one after another, despite the comment saying a slow PMS never holds another. | `cmd/scd/posting_worker.go:94-107` | One goroutine per lane, with a guard so a lane never runs twice at once. Most sites have one interface, so this is Low. |
| L8 | **A charge accepted during shutdown becomes UNKNOWN.** `settle` reuses the worker's context. A stop request during the send cancels it, so a charge the PMS acknowledged is recorded as UNKNOWN and goes to manual review. Safe, but it creates review work after a restart. | `internal/posting/engine.go:197-204`, `:327` | Settle with `context.WithoutCancel(ctx)` and its own 10 s timeout. |
| L9 | **Attempt and requeue updates lack state guards.** `SettleAttempt` does not require `outcome='SENDING'`, so a late settle after orphan recovery could overwrite UNKNOWN. `Requeue` checks that a retry was authorised, not that the authorisation is unused. The database may already refuse both (0011's attempt-sequence rules); this was not proven. | `internal/posting/repo.go:612-616`; `engine.go:417-424` | Add the guards and check rows affected. Integration test for each. |
| L10 | **Post-stay PIN edge cases.** A malformed profile id returns a raw database error at once: a different answer, and no argon2 work. Anyone who knows a profile id can lock that guest out for 15 minutes with 5 wrong PINs. `Reset` does not refuse an expired profile. | `internal/poststay/poststay.go:342-354`, `:315-325`, `:248-254` | Parse the id first and take the dummy-hash path. Key the lockout by profile and device. Refuse reset on an expired profile. |
| L11 | **Transfer bookkeeping.** The operator's reason is validated but not stored. The landing package is chosen with no tie-break or interface filter. Raw database error text is returned to the operator. | `internal/transfer/transfer.go:142`, `:157`, `:233-244` | Store the reason in the transfer event; add `ORDER BY ip.code, ipr.id` and the interface filter; return a fixed message and log the detail. Phase 9 confirms the step-up in edged. |
| L12 | **Shaping input is trusted.** A zero or negative rate silently becomes 1 Gbit. `shapeplan.Validate` checks the envelope, not the sessions: IPs, duplicates, bridge names. The IFB name is truncated to 15 characters, so two long bridge names can share one. Group minors are a 12-bit hash, so a site with many shared allowances meets collisions, which are refused. | `internal/shape/shape.go:105-110`, `:181-189`, `:311-318`, `:78-94`; `internal/shapeplan/plan.go:135-166` | Make "unlimited" an explicit flag and refuse 0 or negative. Validate sessions in `Validate`. Refuse an IFB name collision. Phase 8 owns the netd side. |
| L13 | **acctd details.** The assignment watcher reads its baseline after the scope was resolved, so a change in between becomes the baseline and acctd keeps the old customer. A class missing from the counter map is read as 0 bytes and ingested as a real reading. | `cmd/acctd/main.go:111-123`; `phase3_accounting.go:128-140` | Pass the resolved scope version to the watcher; skip a session whose class is absent. |
| L14 | **Default database URLs carry a password and turn TLS off.** acctd, scd, edged and netd fall back to `postgres://stayconnect:stayconnect@127.0.0.1…?sslmode=disable` when their env file has no URL. acctd and scd default to database `stayconnect`, edged and netd to `stayconnect_site`. | `cmd/acctd/main.go:39`, `cmd/scd/main.go:109`, `cmd/edged/main.go:46`, `cmd/netd/main.go:51` | Require the variable and refuse to start without it. Cross-cutting: track in Phase 10. |
| L15 | **The card status limiter is wiped when full.** After 5,000 entries the map is reset, which lets every status page call the provider again at once. | `cmd/scd/card_checkout.go:270-282` | Prune entries older than the window instead of resetting. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code.** acctd: `activeSession`, `revoke` (and with it the scd client and `ScdSocket`), `snapshot`, `snapEntry`, `prev`, and a no-op `loop`. Payment: the dormant execute path (L2), `VerifyRedirectHMAC` and the Paymob `hmac_secret`, `GrantSettledSettlement`. Posting: `FormatCT` (truncates silently, against the `BuildPS` rule). Shape: `cidClass`. Several unused values (`engine.go:325`, `checkout.go:193`, `:440`). | Delete them. |
| CC2 | **Duplication.** Stripe and Paymob carry copies of the same transport helpers. `psHash` and `postinghandoff.BodyHash`, and `pnOfBody` and `postinghandoff.PNumberOf`, are duplicates kept in step by comments. | Share one helper package for providers; import the postinghandoff versions. |
| CC3 | **Ignored errors and loose scoping.** `_ =` on database calls in reversal recording (M3) and elsewhere; `rows.Err()` unchecked in `SweepReversals`. `internal/checkout` updates `iam_v2.stays` by id alone (`:139`, `:144`, `:151`); the ids come from scoped reads, so this is defence in depth. `AssignmentGe` in `phase3_envelope.go` is a typo. | Check errors; add tenant and site to those updates; rename. |
| CC4 | **Test gaps and comment volume.** `shapeplan` and `shapeproducer` have no tests of their own. The post-stay and transfer integration tests need the `phase5` tag, so the Phase 4 harness never runs them. Long narrative comments, as in earlier phases. | Unit tests for `shapeplan.Validate`; note the tags in each harness; trim comments when a batch touches a file. |

## Fix batches

A batch is done when its tests pass and the package tests, including the `integration phase4` and `integration phase5` suites on a throwaway full-schema database, still pass.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| **B1** Posting outbox backoff | M1, L7, L8, L9 | `internal/posting/{engine,repo}.go`, `cmd/scd/posting_worker.go`, a new appliance migration | CONTROLLED (financial posting, schema). Test only against the scripted PMS double. **First.** |
| **B2** Plan counter durability | M2, L13 | `cmd/acctd/phase3_envelope.go`, `main.go`, `phase3_accounting.go` | Enforcement core. Small; do early. |
| **B3** Provider reversals | M3, L1 | `internal/payment/checkout.go`, a new migration for the locked running-total function | CONTROLLED (financial, schema). Provider doubles only. |
| **B4** Shared re-rate | M4, L12 | `internal/shape`, `internal/shapeplan`, `cmd/netd/phase3_provision.go` | Networking: fold into the Phase 8 netd batch; needs a Linux host for `kerneltest`. |
| **B5** Payment hygiene | L2, L3, L4, L5, L15 | `internal/payment/{engine,provider,testdouble,checkout}.go`, `stripe`, `paymob`, `cmd/scd/card_checkout.go` | CONTROLLED (payment). L2's deletion is the bulk of it. |
| **B6** Check-out boundary | L6 | `internal/checkout/checkout.go` | CONTROLLED (entitlement). |
| **B7** Post-stay and transfer | L10, L11 | `internal/poststay`, `internal/transfer` | Guest authentication and entitlement. |
| **B8** Default database URLs | L14 | four `main.go` files | Cross-cutting; do with Phase 10 or alone. Check that every PRE-LIVE env file sets the URL before deploying. |
| **B9** Clean code | CC1–CC4 | various | After the behaviour batches, never alongside them (same files). |

No item in this phase needs a Product-Owner decision. The migrations in B1 and B3 need the authorization CLAUDE.md §0 requires for schema changes.

## Checked and rejected

- **"Two guests on a wide subnet share a shaping class."** The class id uses 12 bits of the address, but client networks are refused wider than /20 (`internal/netcfg/validate.go:10-11`, `:105`), which is exactly 12 bits.
- **"A declined Paymob attempt fails the purchase while the guest can still retry."** A decline before the intention's expiry is reported as still open (`paymob.go:605-609`). Only a decline after it is final.
- **"There is an unauthenticated payment webhook."** There is none. Both adapters are outbound only: create a checkout, then ask for its status.
- **"The status page lets a browser mark a purchase paid."** It is bound to the device's MAC, and asks the provider (`card_checkout.go:195-250`).
- **"Room charge can be confirmed with the portal's commerce cookie."** Refused by `ConfirmPurchase` (`method_not_available`); see the table above.
- **"A duplicate idempotency key on posting double-posts."** The unique index refuses it; what remains is a generic error instead of the existing id. Folded into B1 as a small improvement, not a separate finding.
- **gosec G115, G703/G304, G702/G204**: see "What was run".

## Not verified

- **The real providers and the real PMS.** Every test used doubles. Stripe's and Paymob's actual behaviour for late captures (L1) and dispute amounts (M3) was not checked against their APIs.
- **Kernel behaviour of the SHARED re-rate** (M4). It needs `kerneltest` on a Linux host.
- **pmsd's side of the hand-off.** Phase 7.
- **edged's step-up on transfer and recovery routes.** Phase 9.
- **Race-detector runs.** These still need a Linux build host.
