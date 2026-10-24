# Backend audit — Phase 7: PMS (pmsd)

| | |
|---|---|
| Audited commit | `b00410b5` (master; unchanged since Phase 6) |
| Scope | `data-plane/cmd/pmsd` and `data-plane/internal`: pms, pmsd, pmsguard, pmsloader, pmsprovider, pmsresolve, pmsrest. About 10,100 lines of non-test Go. Also pmsd's side of the room-charge hand-off (`internal/pmsd/financial_relay.go`, the database authoriser `iam_v2.p4_posting_command_authorised`), and the scd code that still loads the legacy providers (`cmd/scd/main.go`, `pms_admin.go`). |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |

## How to use this file

1. **Format.** It is the same as Phases 1–6. Each finding has a problem, its impact, the recommended fix and the test that proves it. Fixes are grouped into batches at the end.
2. **All of this is CONTROLLED.** It covers PMS protocol, configuration and traffic, guest authentication (the mirror decides room sign-in), and financial posting (CLAUDE.md §0). Every test runs against a scripted PMS. No fix may contact the live Protel link without its own authorization.
3. **Status of a finding:**
   - **Confirmed**: the code path was traced.
   - **Possible**: the code suggests the problem, but it needs a test or the PMS's real behaviour to prove.
4. **How it was checked.**
   - The auditor read the room-charge relay and its database authoriser, the FIAS TLS code, pmsd's start-up and key loading, the resync barrier against the room sign-in rule (migration 0150), the duplicate-record insert, and what still calls the legacy providers.
   - Two independent reviewers read the rest in two slices: the FIAS adapter and the pmsd worker; and the REST clients, provider configuration, resolver and legacy providers.
   - Every claim of theirs listed here was re-checked; the rejected and re-graded ones are at the end.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 2 |
| Low | 14 |
| Clean code | 4 |

### What was run

| Check | Result |
|---|---|
| `go vet` (all Phase 7 packages) | clean |
| `staticcheck` | 3 unused items (CC1); 2 tests that compare a value with itself (CC2) |
| `gosec` | 7 leads, triaged below. G402 (`InsecureSkipVerify` in `fias_tls.go`) is set only together with a certificate pin checked on every handshake; see "Checked and rejected". G115: an advisory-lock key and AAD lengths, already bounded. G118 and G104: in L11 and CC4. |
| `govulncheck` | only the Phase 0 P0-2 advisories |
| **Database integration tests** on a throwaway full-schema database (`-tags integration`). Then the room-charge hand-off suites (`integration phase4`, `TestHandoff_`, `TestA1_`, `TestD48_`, `TestRoomCharge`), with the posting worker as `svc_posting` and pmsd as `svc_pmsd`. A scripted FIAS PMS in-process; nothing left the machine. | pmsd 202, pmsresolve 21, pmsrest 19, pms 13, pmsloader 10, pmsprovider 5, hand-off 54. **324 pass, 0 fail, 0 skipped.** `pmsguard` and `cmd/pmsd` have no tests. |

### pmsd's side of the room-charge hand-off (asked for in Phase 6)

**It holds up.** The relay decides nothing about the charge; it only carries it, and refuses at every doubt:

| Check | Where |
|---|---|
| Refuses unless all three PHASE4 flags are true on pmsd itself | `financial_relay.go:36-45` |
| The socket is `0600` in a `0750` runtime directory owned by pmsd's own user; scd connects as root | `financial_relay.go:337-350`; `deploy/systemd/stayconnect-pmsd.service:35-52` |
| The database authorises exactly these bytes before anything is written. One attempt with this P#, still SENDING, under 2 minutes old, with matching SHA-256. The outbox is IN_FLIGHT and the interface financially ready and fresh. The reservation and the room are what the database holds now. | `iam_v2.p4_posting_command_authorised` (migration 0100) |
| Sent only on a steady link, one command at a time, and never the same P# twice in this process | `financial_relay.go:247-305` |
| Re-checks the room immediately before the first byte, against what the link itself has just seen (a room move or departure not yet in the database) | `financial_relay.go:144-166` |
| "Not sent" is answered only when no byte reached the writer; every later doubt is UNKNOWN, so the posting engine never retries it into a second charge | `financial_relay.go:283-321` |
| A command body (room, reservation) is never logged | `financial_relay.go:389-392` |

L14 records two defence-in-depth gaps in the relay's in-memory guards. The database check is the real one and does not depend on them.

### What holds up well (keep it this way when fixing)

- **TLS on the FIAS link.** No plaintext fallback after a failed handshake, and TLS 1.2 at least. Without a pin, the system chain and the PMS host name are checked. With a pin, the leaf's SHA-256 is compared in constant time on every handshake.
- **Strict parsing.**
  - Control bytes in values and non-printable field codes are refused, and nothing is truncated.
  - A duplicated RN or G# is a fault, not "first one wins".
  - A malformed guest record becomes a recorded gap plus a resync request, and an unknown control record ends the cycle.
- **The last good roster is protected.**
  - A resync is staged and published in one compare-and-set, so a link drop mid-roster leaves the published roster untouched.
  - The database refuses an incomplete or implausibly small roster (migration 0123).
  - Room sign-in keeps answering from the last published roster while a resync runs, and refuses outright when continuity is broken (migration 0150).
- **One owner per interface.**
  - An advisory lock is taken before the secret is decrypted or the PMS dialled.
  - Every write is a compare-and-set on the runtime generation.
  - Losing the lock cancels the connection.
- **Secrets.**
  - PMS credentials are AES-256-GCM sealed and bound to tenant, site, interface and generation, and zeroed after the dial.
  - pmsd refuses to dial without its identity and evidence keys.
  - The REST client's errors carry no body, URL or credential.
- **REST limits.** 30 s timeout, 32 MB response cap, 3 attempts with backoff, and `Retry-After` capped at 60 s. HTTPS is required, with no userinfo, query or fragment.
- **No guest data in pmsd's logs.** Records are logged as type and a bounded reason, and raw frames are never logged.
- **Resolver.** It fails closed: no candidate, too many, or an unclear verdict all give INDETERMINATE. Every query is scoped by tenant and site, and requires an ACTIVE interface.
- **Service hardening.** pmsd runs as its own user with no capabilities, `ProtectSystem=strict`, and no writable path except its runtime directory.

## Medium

### M1 — A FIAS frame has no length limit (Confirmed)

**Where.** `internal/pms/protel_fias.go:373-391` (`readFramedRecord`), used by the live adapter at `internal/pmsd/adapter_fias.go:203`.

**Problem.** After the start byte (STX), the reader calls `ReadString(ETX)` and keeps every byte until an end byte arrives. Nothing limits the frame's size. The only bound is the read deadline, which defaults to the 60 s heartbeat timeout and is reset per frame.

**Impact.** A faulty PMS interface, or anything on the path of a plain-TCP FIAS link (the hotel network), can send STX followed by a continuous stream with no ETX. pmsd's memory then grows at line rate for up to a minute. That can kill pmsd or starve the appliance. The roster stops updating, and once the cached roster ages out, room sign-in stops too.

**Recommended fix.** Read the frame into a buffer capped at a few KB (the longest real FIAS record is far shorter). Past the cap, return a typed error that ends the cycle as `CodeProtocolFraming`. Count and report the bytes discarded outside frames, which are dropped silently today.

**Test.** A scripted PMS sends STX and 10 MB with no ETX: the cycle ends with the framing code, and allocation stays under the cap.

### M2 — A repeated record inside one resync can make every resync fail (Possible)

**Where.** `internal/pmsd/pg.go:285-300` (`insertInboxRow`): only LIVE rows have `ON CONFLICT … DO NOTHING`. RESYNC rows are inserted against the unique index `se_resync_identity (tenant, site, interface, resync_generation, external_event_identity)` with no conflict clause.

**Problem.** The event identity is a keyed hash of the whole record. If the PMS sends the same record twice inside one DS..DE window, the second insert violates the index. The worker reports a staging failure and ends the cycle. The next cycle asks for the full roster again, and the same duplicate arrives.

**Impact.** The mirror can never publish a new roster. Room sign-in keeps answering from the last published one (migration 0150) until it ages out (72 hours), while the link reconnects in a loop and live events are disrupted. No test sends a duplicate inside one window. Whether Protel ever repeats a record within a resync is not known, so this is Possible.

**Recommended fix.** Add `ON CONFLICT … DO NOTHING` for RESYNC rows too. Count a duplicate as skipped, and report the count with the coverage numbers.

**Test.** A scripted resync containing two identical GI frames publishes, with one stay and a skipped count of 1.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | **The live-event barrier does not close on a gap.** After a malformed record, the database is marked `GAP_DETECTED` and a resync is requested. But the worker sink keeps admitting LIVE events until the PMS starts the resync (DS), although the adapter's comment says admission stops. A valid event after a dropped one is applied out of context. Room sign-in is refused while continuity is broken, and the resync reconciles afterwards, so the guest-facing effect is small. | `internal/pmsd/worker.go:356-385`, `:408-414`; `adapter_fias.go:228-247` | Set `s.synced = false` in `markGapResync`, so the documented barrier holds. Test: after a fault and before DS, `OnDomainEvent` admits nothing. |
| L2 | **The FIAS TCP dial has no timeout.** `DialTimeout` applies only to the TLS handshake; the dial uses the worker's context and a zero `net.Dialer`. A black-holed PMS host blocks each attempt for the operating system's connect timeout (about 2 minutes), and shutdown and revision changes wait for it. | `internal/pmsd/adapter_fias.go:106`; `cmd/pmsd/main.go:90-92` | Dial under `context.WithTimeout(ctx, DialTimeout)`. Test with a listener whose backlog is full. |
| L3 | **A certificate pin without TLS is silently ignored.** A revision with `tls.cert_sha256` set and `tls.enabled` false or missing connects in plaintext with no warning. The pin's format is checked only at dial time. | `internal/pmsd/pg.go:117-118`; `pmsd.go` (`Revision.Validate`) | Refuse that combination in `Validate` as `CodeConfigInvalid`, and check the pin format there. Phase 9 checks edged's authoring form. |
| L4 | **The REST clients follow redirects, with credentials.** No `CheckRedirect` is set. Mews puts its client and access tokens in the POST body, which a 307/308 replays to the new host. The base URLs (operator-set) must be https, but may be an IP literal, loopback or a private or link-local address. | `internal/pmsrest/client.go` (no `CheckRedirect`); `mews.go` (tokens in the body); `internal/pmsprovider/config.go:120-134` | Return `http.ErrUseLastResponse` (provider APIs do not redirect). Refuse loopback, link-local and private addresses at the dialer (`Control` hook, which also covers DNS rebinding) unless the operator marks the provider as on-site. |
| L5 | **REST paging limits.** A Mews cursor that never advances makes up to 10,000 requests of 1,000 rows before stopping. Hitting a page cap returns the partial list with no error. The damage is bounded: a reservation missing from the list is looked up before it can become a departure (`adapter_rest.go:303-325`), and the database refuses an incomplete full roster. | `internal/pmsrest/mews.go:102-135` and the other loops in `mews.go`, `apaleo.go`, `operacloud.go` | Stop when a page adds no new id; return `KindInvalidResponse` at the cap. Test: a stuck cursor fails within two requests. |
| L6 | **REST records with blank fields pass through.** A Mews reservation whose room cannot be resolved has an empty room, an unknown customer gives an empty name, and an unparsable date becomes an empty string. Whether every consumer refuses those was not traced. | `internal/pmsrest/mews.go:124-138`; `client.go` (`parseInstant`) | A `Reservation.Valid()` check: an in-house reservation needs an id, a room and a departure. Test each blank. |
| L7 | **The legacy provider layer is still loaded, but nothing signs in through it.** scd loads `pms_providers` rows through `pmsloader` and starts the old Apaleo and Mews pollers (`cmd/scd/main.go:612-624`). It serves `/v1/admin/pms/{name}/test`, `/cache` and `/health` (`main.go:834-836`); `/cache` returns the cached guest rows. Nothing calls `ValidateGuest`, and `pmsguard` has no caller at all: room sign-in goes through `pmsresolve` and pmsd's mirror (Phase 4). If the old validators were reused, they would be wrong. Apaleo ignores the reservation status (cancelled or checked-out bookings match), Mews asks for `Processed` (checked-out) stays, and `ModeEither` matches on a bare first name. The legacy layer also reads plaintext credentials and puts provider response bodies into errors and `Health.LastError`. | `internal/pms/{apaleo,mews,pms}.go`; `internal/pmsguard/guard.go`; `internal/pmsloader/loader.go`; `cmd/scd/pms_admin.go` | Retire it: stop loading and starting legacy providers, remove the three admin routes and `pmsguard`, and drop the plaintext columns in a later CONTROLLED migration. PRE-LIVE uses Protel FIAS through pmsd, so nothing live depends on it; check `pms_providers` is empty there first. |
| L8 | **Key settings fail late.** Invalid hex in `PMSD_SECRET_KEY_HEX`, `PMSD_EVIDENCE_KEY_HEX` or `PMSD_EVENT_IDENTITY_KEY_HEX` is discarded. The failure appears only at dial time as `CONFIG_INVALID`, without naming the variable. There is no minimum key length. | `cmd/pmsd/main.go:80-89` | Validate at start-up: decode, require at least 32 bytes, and log the variable name (never the value) on failure. |
| L9 | **A FIAS link drop mid-roster is not recorded as interrupted.** `OnDisconnected` is called only by the REST adapter, so after a FIAS drop the stage still reads RECEIVING until the next cycle. Whether the abandoned generation's staged rows are purged was not traced. | `internal/pmsd/worker.go:337`; `adapter_fias.go` (`Serve`) | Call `sink.OnDisconnected` when `Serve` returns. Test: after a drop mid-window the stage reads INTERRUPTED. |
| L10 | **Reconnect jitter can be near zero.** The wait is `rnd % cur`, so a retry can follow immediately, and several interfaces can retry in step. | `internal/pmsd/supervisor.go:134-150` | Equal jitter (`cur/2 + rand(cur/2)`). Test: `next() >= min/2`. |
| L11 | **Database lock housekeeping has no timeout.** The lock watcher's `Ping` and the unlock in `Close` use `context.Background()`, so a half-open connection can hang them. Each worker also holds one pooled connection for its whole cycle, from a pool of default size shared with the applier and the relay. | `internal/pmsd/pg.go:388-415` | 2 s timeouts on both; size the pool explicitly from the number of interfaces. |
| L12 | **Applier errors are logged in full.** The comment says no guest value passes, but `err.Error()` from pgx can carry a constraint's key values. | `internal/pmsd/applier.go:63` | Log the bounded code and the SQLSTATE only. |
| L13 | **A cached resolution is replayed by request id alone.** A prior VERIFIED answer is found by tenant, site and request id, not the guest network. Request ids are random, so this is defence in depth. | `internal/pmsresolve/fanout.go:187-203` | Add the guest network to the lookup. |
| L14 | **The relay's in-memory guards are bounded loosely.** The set of P#s already carried evicts an arbitrary entry at 4,096 and is empty after a restart. The per-link room view resets on overflow. The database authoriser is the real guard (one attempt per P#, SENDING, under 2 minutes), and the posting engine never reuses a P#, so neither is reachable today. | `internal/pmsd/financial_relay.go:196-217`, `:116-141` | Evict the oldest entry, not an arbitrary one. Keep both as backstops; no further change. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | **Dead code.** `(*workerSink).queue` and `BoundedQueue`, which are "integration tests only" (`worker.go:417`); `apaleo.go`'s `unitNames`; the Opera Cloud `Changes` stub; `StartAll`'s FIAS refusal for a kind `Load` can no longer return; the whole legacy layer in L7. | Delete it with L7. |
| CC2 | **Tests that cannot fail.** `internal/pmsd/secret_test.go:49` and `internal/pmsresolve/resolve_test.go:59` compare a value with itself. `pmsguard` and `cmd/pmsd` have no tests. | Compare against the intended value. |
| CC3 | **Stale or misleading comments.** `financial_relay.go`'s `ServeSocket` says "root-only unix socket", but the socket belongs to pmsd's own user (scd reaches it as root). The adapter's comment says admission stops on a gap, which L1 shows it does not. | Correct each to one line. |
| CC4 | **Size and duplication.** `adapter_fias.go`'s `Serve` is one long function holding the DS/DE state machine. `internal/pms`'s REST providers duplicate `pmsrest`, as a weaker copy. Unhandled errors at `fias_tls.go:51`, `:62` are on close paths. | Split the resync state machine into its own type; remove the duplicates with L7. |

## Fix batches

A batch is done when its tests pass, the pmsd package's `integration` suite passes on a throwaway full-schema database, and the hand-off suites in `internal/posting` still pass.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| **B1** FIAS input limits | M1, L2, L10 | `internal/pms/protel_fias.go`, `internal/pmsd/adapter_fias.go`, `supervisor.go`, `cmd/pmsd/main.go` | CONTROLLED (PMS protocol). Scripted PMS only. **First.** |
| **B2** Resync robustness | M2, L1, L9 | `internal/pmsd/{pg,worker,adapter_fias}.go` | CONTROLLED (PMS, guest authentication). Must keep migration 0150's behaviour; re-run `resync_wedge_integration_test.go`. |
| **B3** Configuration checks | L3, L8 | `internal/pmsd/pmsd.go`, `cmd/pmsd/main.go` | CONTROLLED (PMS configuration). Check PRE-LIVE's revision and env file pass the new checks before deploying. |
| **B4** REST client hardening | L4, L5, L6 | `internal/pmsrest`, `internal/pmsprovider/config.go` | CONTROLLED (PMS traffic). No REST PMS is live, so it can go any time. |
| **B5** Database and logging housekeeping | L11, L12, L13, L14 | `internal/pmsd/{pg,applier,financial_relay}.go`, `internal/pmsresolve/fanout.go` | Small. |
| **B6** Retire the legacy provider layer | L7, CC1 | `internal/{pms,pmsguard,pmsloader}`, `cmd/scd/{main,pms_admin,pms_seed}.go`; later, a migration for the plaintext columns | Large deletion. Check that `pms_providers` is empty on PRE-LIVE first. The migration is CONTROLLED and separate. |
| **B7** Clean code | CC2–CC4 | various | After the behaviour batches, never alongside them. |

No item in this phase needs a Product-Owner decision.

## Checked and rejected (or re-graded)

- **gosec G402, `InsecureSkipVerify` on the FIAS link.** Set only with a configured pin, and `VerifyConnection` checks the leaf's SHA-256 on every handshake (`fias_tls.go:33-47`). Without a pin, the chain and host name are verified.
- **"Room sign-in can match a cancelled, checked-out or first-name-only booking"** (reviewer, rated Medium). True of the legacy `ValidateGuest` code, but nothing calls it; recorded as L7.
- **"Room-number variants bypass the lockout"** (reviewer, on `pmsguard`). `pmsguard` has no caller; the live throttle is scd's (Phase 4). Part of L7.
- **"A truncated REST snapshot marks in-house guests departed"** (reviewer, rated Medium). A missing reservation is looked up and becomes a departure only if the PMS says it checked out (`adapter_rest.go:303-325`); a full roster is checked for coverage in the database. Re-graded to L5.
- **"Live events after a gap let the wrong person sign in"** (reviewer, rated Medium). Room sign-in requires `continuity_status = 'CONTINUOUS'` (migration 0150), so it is refused during the gap. Re-graded to L1.
- **"The relay socket is open to other users."** It is `0600` inside a `0750` runtime directory owned by `stayconnect-pmsd`.

## Not verified

- **The real Protel FIAS link.** Whether it ever repeats a record within one resync (M2), and how long its longest frame is (M1). Only the scripted PMS was used.
- **REST providers against their real APIs.** Mews, Apaleo and Opera Cloud were tested with doubles only; none is live.
- **Every consumer of blank REST fields** (L6).
- **edged's PMS interface authoring** (TLS pin form, provider URL entry). Phase 9.
- **Race-detector runs.** These still need a Linux build host.
