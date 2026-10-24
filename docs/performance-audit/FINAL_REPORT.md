# Performance audit: final report

**Audited:** master `653363d7` throughout; master was fetched at the start of every phase and had not moved. Code and local measurements only: nothing was run against PRE-LIVE or Central, and no PMS or payment traffic was made. Every measurement used generated data.

**For:** the engineer who will make the fixes. Each phase file has the full findings, with where, the measurement, the fix and its proof. This report gives the totals, what matters most, the order of work and the decisions needed.

## Totals

| Phase | Area | Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|---|---|
| 0 | [Baseline](PHASE0_BASELINE.md) | 0 | 0 | 1 | 0 | 1 |
| 1 | [Database foundations](PHASE1_DATABASE.md) | 0 | 3 | 3 | 7 | 0 |
| 2 | [Guest sign-in](PHASE2_GUEST_SIGN_IN.md) | 0 | 1 | 4 | 5 | 2 |
| 3 | [Client Portal](PHASE3_CLIENT_PORTAL.md) | 0 | 0 | 2 | 6 | 0 |
| 4 | [Enforcement](PHASE4_ENFORCEMENT.md) | 0 | 3 | 3 | 3 | 1 |
| 5 | [Background jobs and accounting](PHASE5_BACKGROUND_JOBS.md) | 0 | 2 | 2 | 2 | 0 |
| 6 | [PMS](PHASE6_PMS.md) | 0 | 1 | 2 | 1 | 0 |
| 7 | [Admin Console API](PHASE7_ADMIN_API.md) | 0 | 2 | 3 | 2 | 0 |
| 8 | [Admin front ends](PHASE8_FRONT_ENDS.md) | 0 | 0 | 2 | 4 | 0 |
| 9 | [Central](PHASE9_CENTRAL.md) | 0 | 0 | 2 | 3 | 0 |
| 10 | [Resource budget](PHASE10_RESOURCE_BUDGET.md) | 0 | 1 | 4 | 3 | 0 |
| 11 | [Load-test plan](PHASE11_LOAD_TEST_PLAN.md) | 0 | 0 | 0 | 1 | 0 |
| **Total** | | **0** | **13** | **28** | **37** | **4** |

**Corrections made in this phase** (all in this branch's audit files; no code):
- **Phase 7:** its summary table said 4 Medium. It lists 3, and the table now says 3.
- **Phase 10, D10-M4:** the disk budget had read Phase 1's accounting growth (per day) as per year, and said the 40 GB disk fits today. It does not: at design load the accounting fills it in two to three weeks, which is Phase 1's D1-H3. D10-M4 now sizes the disk for each accounting grain, and Decision D10-D1 changed with it. Phase 10's backup and restore rates per GB are unchanged.
- **Phase 5, D5-M2:** a note says its generated database held about a day and a half of design-load accounting, so real backups are larger.

## The answer in one paragraph

On the reference appliance (2 vCPU, 4 GB, 40 GB), the solution does not yet carry its design load: 2,000 devices, 300 sign-ins a minute and a year of history. Nothing is broken at PRE-LIVE's current light use. But at design load:
- sign-ins can exhaust memory;
- one guest network cannot hold more than 2,047 devices;
- the per-second accounting falls behind and fills the disk;
- a PMS resync closes room sign-in across the hotel;
- several Admin Console pages time out;
- the appliance leaks about 1.5 GB of memory a day.

Most of these have small, measured fixes, and a first wave of about ten batches removes the worst of them. With those fixes, the design load fits 4 GB with about a quarter to spare. The disk depends on how much accounting detail is kept: hourly totals fit 40 GB, and a week of per-minute detail needs 80 GB.

## What matters most

The 13 High findings, in the order they bite at design load:

| # | Finding | What happens at design load | Batch |
|---|---|---|---|
| 1 | **D2-H1** Password hashing has no limit | About 30 sign-ins at once take the whole 4 GB. Anyone on the guest Wi-Fi can cause it | P2 B1 |
| 2 | **D10-H1** edged's health loop leaks connections | About 1.5 GB a day across edged, netd and scd, until a restart | P10 B1 |
| 3 | **D4-H1** One guest network holds at most 2,047 devices | The 2,048th device on a network cannot be enforced | P4 B1 |
| 4 | **D4-H3** Every packet is matched against the device filters one by one | Packet rate falls as devices are added | P4 B1 |
| 5 | **D1-H2** acctd writes every online device every second, in its own transaction | Accounting falls behind the clock at 2,000 devices | P1 B2 with P5 B2 |
| 6 | **D5-H2** acctd runs every step in one serial loop | The slowest step sets every deadline, including cut-off on a used-up allowance | P5 B2 |
| 7 | **D5-H1** Sessions cannot be found by entitlement | The expiry sweep takes about 17 s while holding locks | P5 B1 (index) |
| 8 | **D4-H2** The enforcement pass does per-device process and database work every second | CPU is spent on devices that did not change | P4 B2 |
| 9 | **D1-H1** Every controlled operation scans an hour of bookkeeping rows | Gets slower as it is used; feeds itself | P1 B1 (index) |
| 10 | **D1-H3** Accounting detail grows faster than anything removes it | The 40 GB disk fills in weeks, and Postgres then stops | P1 B3, after a decision |
| 11 | **D6-H1** Room sign-in is closed hotel-wide while any PMS event waits | Every resync closes room sign-in for the whole hotel until the queue empties | P6 B2 |
| 12 | **D7-H1** Session, PMS stays and device pages look sessions up with no index | Two pages fail at 10 s with a year of history | P7 B1 (index) |
| 13 | **D7-H2** The Overview's traffic charts sum raw per-second detail | The charts cannot fit their time budget past a few minutes of data | P7 B3, after a decision |

**Cross-cutting causes,** each behind several findings:
- **Missing session indexes:** D1-H1, D1-M1, D5-H1, D7-H1, D2-M2. One migration fixes all five.
- **Per-second work on unchanged data:** D1-H2, D4-H2, D4-M2, D5-M1. This is the acctd and netd redesign.
- **No limits:** hashes (D2-H1), memory (D10-M3), requests to scd (D2-L5), portal requests per device (backend audit Phase 8 M1).
- **Nothing to measure with** (B0-1): no benchmarks, no per-route timings, no profiler. Every fix needs this first.

## Order of work

README rule 1 holds throughout: one batch per branch, with a number before and after, measured with the harness and the Phase 11 scenario the batch names.

### Wave 0: measurement

| Batch | Findings | Mode |
|---|---|---|
| P0 B0 | B0-1, B0-2: per-route histograms, profiler switch, data generator, shared server helper | FAST |

### Wave 1: small fixes that remove the worst risks

| Batch | Findings | Mode | Notes |
|---|---|---|---|
| P10 B1 | D10-H1, D10-L1 | FAST | The leak and the constant hashes |
| P2 B1 | D2-H1, D2-CC1 | CONTROLLED | The hash limiter. With P3 B3 (D3-L5) for portald's "busy" page |
| **Index migration** | P1 B1 (D1-H1, D1-M1), P5 B1 (D5-H1), P7 B1 (D7-H1, D7-M1 indexes), P2 B3's index (D2-M2) | CONTROLLED | One appliance migration |
| P6 B1 | D6-M1 | CONTROLLED | Two conditions in two functions |
| P3 B1 | D3-M1, D3-M2, D3-L6 | FAST, except D3-M1 step 3's CSP change (security: CONTROLLED) | With the backend audit's Phase 8 M1 portal limit |
| P4 B1 | D4-H1, D4-H3 | CONTROLLED | Hashed classification. Real-kernel test first |
| P7 B2 | D7-H1 queries, D7-M1 rewrite, D7-M2, D7-M3, D7-L1, D7-L2 | FAST | After the index migration |
| P8 B1 | D8-M1, D8-L1, D8-L2 | FAST | Polling in hidden tabs |
| P9 B2 | D9-M2, D9-L3, D9-M1 step 3 | FAST | With P8 B2 (D8-M2) |

### Wave 2: the redesigns and the deployment configuration

| Batch | Findings | Mode | Notes |
|---|---|---|---|
| **acctd and netd per-second path** | P5 B2 (D5-H2), P1 B2 (D1-H2), P4 B2 (D4-H2, D4-M2, D4-L1, D4-L2, D4-CC1) | CONTROLLED | Phase 5's batched accounting design. P1 B2 and P1 B3 ship together |
| P5 B3 | D5-M1 | CONTROLLED | Write only on change |
| P6 B2, P6 B3 | D6-H1 steps 1–3; D6-M2 | CONTROLLED | Generated rosters only |
| P2 B2 | D2-M1 | CONTROLLED | Hash and provider calls outside transactions |
| **Deployment configuration** | P1 B4 (D1-M2, D1-L2), P10 B4 (D10-M3, D10-L2), P2 B1's `GOMEMLIMIT` | CONTROLLED | Memory limits, kill order, Postgres settings, Redis removed |
| **Backups** | P10 B2 (D10-M1, D10-L3), P5 B4 (D5-M2), P10 B5 (D10-M4) | B2 CONTROLLED; B4, B5 FAST | The check moves off the live server; size-aware retention |
| P10 B3 | D10-M2 | FAST, except Kea's configuration (CONTROLLED, networking) | Logs last the hotel's period |
| P4 B3, P4 B4 | D4-M1; D4-M3, D4-L3 | CONTROLLED | |
| P1 B5, P1 B6 | D1-M3, D1-L1, D1-L6; D1-L3 | B5 FAST; B6 CONTROLLED | Pool sizes become configuration |
| P2 B6 | D2-L2, D2-L4, D2-L5, D2-CC2 | CONTROLLED (guest authentication) | |
| P3 B2 | D3-L1 to D3-L4 | FAST | D3-L2 has an edged half |
| P5 B5 | D5-L1, D5-L2 | FAST | |
| P8 B2, P8 B3 | D8-M2; D8-L4 | FAST | |
| P9 B1, P9 B3 | D9-M1 index, D9-L1; D9-L2 | B1 CONTROLLED (Central migration); B3 FAST | |

### Wave 3: after a decision

| Batch | Findings | Waits for |
|---|---|---|
| P1 B3, P7 B3 | D1-H3, D1-L4, D1-L7; D7-H2 | The usage-detail grain (D1-H3) and the tick (D1-H2) |
| P1 B7, with D11-L1 | D1-L5, D11-L1 | What anonymised rows are kept for |
| P2 B4 | D2-M3, D2-L1 | The sign-in ceiling (D2-M3) |
| P2 B5 | D2-M4, D2-L3 | Phase 11's S3 measurement |
| P6 B2 step 4, P6 B4 | D6-H1 step 4; D6-L1 | The scope of the room sign-in rule; the RESYNC payloads |
| P9 B1 step 1 | D9-M1 step 1 | What the fetch log records |
| D8-L3 | Caddy routing `/api/edge/*` straight to edged | Phase 11's S8 measurement, then a decision |

## Decisions for the Product Owner

None blocks Wave 0 or Wave 1.

| # | Decision | Recommended | Blocks |
|---|---|---|---|
| Phase 0 D1 | Are the design targets right (2,000 devices, 300 sign-ins a minute on 2 vCPU / 4 GB)? | Yes for now. Add a second reference size if larger hotels are sold | Severities only |
| D1-H2 | How soon must a used-up allowance or time limit cut a guest off? | Within 5 s by default, as a setting | P1 B2, P5 B2 |
| D1-H3 | What usage detail is kept, at what grain, for how long? | Hourly totals for the 12-month retention, with per-minute detail for **7 days** rather than Phase 1's 30. Phase 10 sized the disk: 30 days of per-minute detail is 13–25 GB of accounting and needs over 120 GB of disk with backups on the appliance; 7 days fits 80 GB. Decide with D10-D1 | P1 B3, P7 B3, P10 B5 |
| D1-L5 | What anonymised per-event rows are kept for, and for how long | See Phase 1 | P1 B7, D11-L1 |
| D2-M3 | The default appliance-wide sign-in ceiling | A setting that follows the licence's concurrent-guest cap, never below 600 a minute | P2 B4 |
| D2-H1 (later) | Keep Argon2id at 64 MiB, or move to OWASP's lighter setting? | Keep it and add the limiter. Phase 10 shows memory fits with the limiter | Nothing now |
| D6-H1 step 4 | Refuse a room sign-in only while an event for that stay or room waits, not any event | See Phase 6. It changes guest-authentication semantics | P6 B2 step 4 |
| D6-L1 | Reduce superseded RESYNC payloads to their evidence hash | See Phase 6. They are evidence | P6 B4 |
| D8-L3 | Route the console's API straight from Caddy to edged | Decide after S8 | |
| D9-M1 step 1 | Log only served and denied assignment fetches, and count the rest | Yes. It is the data-minimal choice | P9 B1 step 1 |
| D10-D1 | The reference disk, given the D1-H3 grain | 80 GB with 7 days of per-minute detail. 40 GB only with hourly totals alone | P10 B5's sizing |
| D10-D2 (Phase 0 D2) | Is 4 GB still the minimum? | Yes for the design load after Wave 1 and the deployment configuration. 8 GB for the stress load. Confirm with S12 | Sales sizing |

Phase 11's two read-only PRE-LIVE counts (P1 probe rate, P2 resync pace) each need their own authorization too. They are in the [load-test plan](PHASE11_LOAD_TEST_PLAN.md#read-only-counts-on-pre-live).

## Compliance

Every finding was checked against the [README's compliance rules](README.md#compliance). No recommended fix weakens a control (README rule 8). The ones that need the most care when they are made:

| Finding | Rules | The care needed |
|---|---|---|
| D2-H1 | GDPR Art. 32; OWASP ASVS; NIST SP 800-63B | The limiter changes how many hashes run at once, never the hash's strength, the throttles or the equal time for unknown accounts |
| D1-H3, D7-H2 | GDPR Art. 5(1)(c), (e); ePrivacy Art. 6; financial retention | Coarser and shorter usage detail is preferred. Billing and posting records keep their statutory period |
| D10-M2 | National connection-log laws; DPA Annex II §9; ISO/IEC 27001 A.8.15 | Today the appliance keeps less than the hotel's log period promises. The fix makes logs last the period, no shorter and no longer |
| D10-M1, D5-M2 | GDPR Art. 32(1)(c), (d) | Every backup stays proven restorable and checked. The proof moves; it is not dropped or sampled |
| D6-H1 | GDPR Art. 32; the local-first and feed-trust contract | A partial roster stays unclaimable; a waiting check-out still blocks that stay |
| D11-L1 | GDPR Art. 15 | The bounded export returns exactly what today's does |
| Every fix | README rule 7 | No guest data in commits, PRs, load tests or reports |

## Outside this audit

Seen in passing, and left to the backend or security backlog. None is counted above.

- **Server timeouts.** ctrlapi's console listener, portald, edged, and scd's and netd's sockets set `ReadHeaderTimeout` only. D10-H1's fix adds the idle timeout on the sockets. The rest belongs to backend audit pattern P1.
- **cloud-admin runs Next 14.2.5,** which has a published security advisory. It also uses `unsafe-eval` in its CSP (Phase 8).
- **Portal request limits per device:** backend audit Phase 8 M1, still open, now sized by Phase 3.

## How it was measured, and its limits

- **Database work:** throwaway full-schema databases (`scripts/lib-fullschema-testdb.sh`) with the appliance's and Central's images, limited to the reference CPU and memory.
- **Kernel work:** a privileged network-namespace container for nftables, tc and conntrack.
- **Services:** run idle against those databases, with Go benchmarks and reproductions in throwaway workspaces.
- **The workstation is not the appliance.** It runs WSL2 with a fast disk, so times compare and show growth; they are not promises. The load-test plan's scenarios repeat every key number on a reference-size lab appliance before and after each fix.
- **Cleanup.** Each phase removed its containers with their volumes and deleted its scratch files. Nothing was pruned globally. Containers and volumes of other projects on the workstation came and went during the audit; none were touched.
