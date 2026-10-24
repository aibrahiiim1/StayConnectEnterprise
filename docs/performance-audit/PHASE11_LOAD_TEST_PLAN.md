# Phase 11: Load-test plan

**Audited:** master `653363d7`, fetched at the start of the phase. Code and local measurements only: nothing was run against PRE-LIVE or Central.

This phase writes the plan that turns the audit's numbers into numbers from the real environment. It covers:
- the harness to build and keep;
- the reference environments;
- the end-to-end scenarios, with what each proves;
- the order to run them in;
- the rules that keep the runs lawful and safe.

It also closes the items earlier phases handed on, by measuring the ones that could be measured locally and placing the rest in a scenario. The totals, the order of work and the decisions are in the [final report](FINAL_REPORT.md).

**How:**
- **Handoffs:** every "Handed on" table in Phases 0–10 was read. Each item is in [Items from earlier phases](#items-from-earlier-phases) with its scenario.
- **Local checks:** two handed-on items could be settled without a reference environment:
  - whether Next's proxy cancels edged's work when the browser gives up (Phase 8 D8-L1), read in Next 15.5.27's own proxy code in `hotel-admin/node_modules`;
  - the guest data export on a year of audit history (Phase 7). This was measured on a throwaway full-schema database (migrations to 0150), limited to 2 CPUs and 4 GB with Phase 1's settings, filled with 1 million generated audit rows over one year (413 MB). The rows hold placeholder `@example.test` addresses only.
- **Compliance:** checked against the [README](README.md#compliance) rules. The plan's own rules are in [Rules for every run](#rules-for-every-run).

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 0 | 1 | 0 |

- **One new finding.** The guest data export reads the whole audit log each time (D11-L1).
- **The plan has 12 scenarios.** Each runs twice: once before its fix batch, for the "before" number, and once after it, for the proof. All of them run in an isolated lab. Only two read-only counts, taken under a separate Product-Owner authorization, touch PRE-LIVE.

## Finding

### D11-L1: The guest data export reads the whole audit log every time (Low, Measured)

**Where.** The latest `public.guest_data_export` (`data-plane/migrations/0147_…up.sql:2134`) builds the person's activity log at `:2178-2184`. It matches every audit row of the tenant against the person's identifiers, with `lower(l.payload::text) LIKE ANY (…)`. The erasure, `guest_data_erase_core` (`:950-963`, `:996`), does the same match but reads only from the person's first record onward (`v_since`). The export has no such bound.

**Measured.** Over one year of audit rows (1 million, 413 MB), the export's audit-log match took 2.5–2.6 s with one parallel worker and 4.8–5.1 s without, two runs each. It reads every chunk.

**Problem.**
- **The cost grows with the whole log, not with the guest.** The appliance's audit log is never trimmed (Phase 1 D1-L5), so the export gets slower every year. At three years it is about 8–15 s of one CPU.
- **It competes with guests.** The export runs on the appliance's database, the one guest sign-in uses.

**Impact.** Small at today's request rate: guest data requests are rare and staff wait for them. It becomes noticeable as the log grows, and it is the same read the erasure would make if its bound were removed.

**Recommended fix.** Give the export the bound the erasure already has. Read the activity log from the person's first record (less a day), using the same `v_since` expression. Nothing about the person can be older than their first record, which is the same reasoning the erasure already relies on.

**Proof.** On three years of generated audit rows, the export of a guest first seen a month ago reads only the chunks since then and takes under 0.5 s. Its result is identical to today's for the same guest.

**Batch:** with Phase 1 B7 (audit-log trimming). CONTROLLED (database function, by migration).

**Compliance.** GDPR Art. 15: the export must stay complete. The bound removes no row that can be about the person. The proof compares the full result before and after.

## The harness to keep

This is Phase 0's B0-1 and B0-2, made concrete. It lives in the repository, under a `perf` build tag and in `scripts/perf/`, so every fix batch can measure with it. It holds generated data only.

| Part | What it does | Built on |
|---|---|---|
| **Request histogram** | p50, p95 and p99 per route pattern, in every HTTP service | The scd metrics registry; the route pattern from `internal/reqlog`, never the raw path |
| **Profiler switch** | `pprof` on the loopback or admin socket, off by default, switched on by a setting | The shared server helper (B0-2) |
| **Data generator** | Fills a full-schema database to the reference sizes, at 1 and 3 years | `scripts/lib-fullschema-testdb.sh`, and the generators sketched in Phases 1, 5, 7 and 10 |
| **Device simulator** | 2,000 or 5,000 guest devices in network namespaces: DHCP, captive-portal probes, catch-all HTTP, background traffic | The privileged namespace container from Phase 4 |
| **Sign-in driver** | Voucher, credential, room and social sign-ins at a set rate per minute | The portal's own routes, as a device would call them |
| **Console driver** | 10 or 25 Admin Console sessions: dashboard, session lists, reports, exports, at the pages' real polling intervals | A headless browser, so tab visibility and polling are real |
| **PMS simulator** | A FIAS-speaking loopback peer that sends a generated roster and live events at a set pace | pmsd's own adapter, pointed at loopback. Never a real PMS |
| **Fleet simulator** | 200 or 1,000 appliance identities calling ctrlapi on their real schedule | ctrlapi's test identities; `.invalid` host names; TEST-NET addresses |
| **Collector** | Each 10 s: per-process resident memory, threads and open files (`/proc`); Postgres statements (`pg_stat_statements`, with no parameter values); WAL position; disk use per directory; journal size; CPU per process | Phase 10's sampling scripts |
| **Report** | A table per scenario: before and after, against the pass line | |

## Reference environments

| Environment | Size | Used for |
|---|---|---|
| **Lab appliance** | 2 vCPU, 4 GB, 40 GB disk; the appliance's operating system, kernel and disk type; the release under test, installed by the normal install scripts | Every appliance scenario |
| **Lab appliance, large** | 4 vCPU, 8 GB, 80 GB | S12 (stress load), and Decisions D10-D1 and D10-D2 |
| **Lab Central** | The size `central-infra.yml` assumes (Postgres limited to 2 CPUs and 2 GB) | S11 |
| **Operator PC** | A typical hotel office PC and browser | S8's front-end parts |
| **Lab network** | Isolated: no route to PRE-LIVE, Central, any PMS or any payment provider | All runs |

**Never PRE-LIVE for load.** PRE-LIVE carries a real PMS guest list (CLAUDE.md §0B), and a load run there would mix generated and real records.

## Scenarios

The pass lines are Phase 0 §2's goals unless a scenario says otherwise.

| # | Scenario | Load | Measures | Pass line | Proves or settles |
|---|---|---|---|---|---|
| **S1** | **A design day** | 2,000 devices, 500 rooms, 600 stays; probes from unsigned devices; 10 consoles; 24 h | Memory per process, total and swap; open files; journal growth; DHCP log growth; WAL per day; database and disk growth; CPU | Memory under 3.5 GB, no swap, no out-of-memory kill; open files flat; journal under 50 MB a day after D10-M2; disk growth inside the D10-M4 budget | D10-H1, D10-M2, D10-M3, D10-M4, D5-M1, D1-H3; the D10-D2 memory answer |
| **S2** | **Sign-in storm** | 300 and 1,000 sign-ins a minute, for 10 minutes, with S1's background | p95 submit-to-internet; scd memory; pool wait; hashes at once | p95 under 2 s at 300 a minute; scd under 700 MB | D2-H1, D2-M1, D2-M2, D1-H1, D1-M3 |
| **S3** | **Room sign-ins during a resync** | 300 room sign-ins a minute while the PMS simulator sends a 600- and a 1,500-stay roster | Refusals seen by the guest during the resync; time from resync start to sign-in open; applier rate | No hotel-wide refusal window longer than the decision in D6-H1 allows | D2-M4, D6-H1, D6-M1, D6-M2 |
| **S4** | **Portal traffic** | 2,000 and 5,000 unsigned devices at the probe rate counted on PRE-LIVE (P1 below) | p95 per portal route; portald CPU; bytes on the guest network | Page p95 under 300 ms; probe answer under 50 ms | D3-M1, D3-M2, D3-L1 |
| **S5** | **Enforcement** | 2,000 and 4,096 devices on one guest network; add, remove and change a device; packet rate to the last device | Time to admit and revoke; ruleset apply time; packet rate; conntrack flush | 4,096 devices install on one bridge; packet rate within 10% of one device's; change applied under 10 s | D4-H1, D4-H2, D4-H3, D4-M1 |
| **S6** | **acctd at design load** | 2,000 online devices, 10% and 30% moving, for an hour | Period of each loop; rows and WAL per tick; lock waits | Every loop keeps its period; tick WAL under 200 kB | D5-H1, D5-H2, D5-M1, D1-H2, D4-M2 |
| **S7** | **PMS throughput** | Generated rosters of 600 and 1,500 stays; live events at 1 and 10 a second | Resync time split into stage, publish, reconcile, apply; frames per second | Resync of 600 stays under 60 s on the appliance's side | D6-H1, D6-M1, D6-M2, D6-L1 |
| **S8** | **Admin Console** | 10 and 25 consoles; a month's export during S2; packages and commerce module on; guest data export and erasure at 1 and 3 years | p95 per route; proxied calls per second through Node; the Central list render on the operator PC; sign-in restriction call times | List and report p95 under 1 s; export under 30 s without moving S2's p95 | D7-H1, D7-H2, D7-M1, D7-M3, D8-M1, D8-M2, D8-L3, D11-L1 |
| **S9** | **Backup and restore** | A verified backup and a restore at 1 and 3 years of data, with S1's background and a sign-in rate of 300 a minute | Backup time; WAL written; sign-in p95 during it; restore outage; free disk at each step | Sign-in p95 under 2 s during a backup; manual backups finish inside their deadline; a restore refuses early if space is short | D10-M1, D10-L3, D10-M4, D5-M2 |
| **S10** | **Boot and recovery** | Cold boot; Postgres killed during S1; each service killed in turn; a forced out-of-memory | Time to serving; start peak per service; which process the kernel kills | Serving within 2 minutes of power-on; hotel-admin or edged killed first | D10-L1, D10-M3, Phase 10's recovery numbers |
| **S11** | **Central fleet** | 200 and 1,000 simulated appliances: steady schedule, then all restarting at once; 3 operators on the fleet list | p95 per appliance call; the burst's backlog; fleet list time; prune time | Appliance call p95 under 100 ms; burst cleared in under 1 minute; fleet list under 0.5 s | D9-M1, D9-M2, D9-L2, D9-L3, D8-M2 |
| **S12** | **Stress** | 5,000 devices, 1,000 sign-ins a minute, 25 consoles, 3 years of data, on the 4 GB and the 8 GB lab appliance | Everything S1 and S2 measure | Records where 4 GB stops; no pass line | D10-D2, Phase 0 D1 |

### Read-only counts on PRE-LIVE

Two numbers can only come from PRE-LIVE. They replace assumptions, so they come first. Each needs **its own Product-Owner authorization**. This plan does not authorize them, and this audit did not take them.

| # | What | How, with no guest data | Replaces |
|---|---|---|---|
| **P1** | Probe and catch-all requests per minute from unsigned devices, by route | Count journal lines by `route` for portald over a day. Read counts only, never a line's other fields | Phase 3's assumed rate (D3-M1) |
| **P2** | How long a resync takes, split into Protel's pace and the appliance's | Read pmsd's own progress lines: record counts and timestamps only | Phase 6's estimate (D6-H1) |

Both are reads. They send nothing to the PMS and change nothing on the appliance.

## Order of runs

1. **Build the harness** (B0-1, B0-2). Nothing else can be measured without it.
2. **Take P1 and P2,** if the Product Owner authorizes them, so S3, S4 and S7 use real rates.
3. **Run S1–S11 on the release as it is.** These are the "before" numbers. Keep them with the report.
4. **After each fix batch,** run the scenarios that batch names, and record before and after (README rule 1).
5. **Run S12 last,** after the first wave of fixes (see the final report), to answer D10-D2.

## Rules for every run

| Rule | Why |
|---|---|
| **Generated data only.** Locally administered MAC addresses (`02:…`), private and TEST-NET addresses, `.invalid` and `example.test` names, placeholder payloads | GDPR Art. 5(1)(c), 25; README rule 7. No guest data enters a lab, a report or a commit |
| **An isolated lab network.** No route to PRE-LIVE, Central, a PMS or a payment provider | CLAUDE.md §0, §0E. No PMS traffic, no financial traffic, no new message to Central |
| **No real PMS, ever.** The PMS simulator speaks FIAS on loopback | CLAUDE.md §0 (PMS is CONTROLLED) |
| **Payments off.** Commerce runs only with its test mode and no provider reachable | PCI DSS; CLAUDE.md §0 |
| **Security settings as shipped.** Argon2 parameters, throttles, lockouts and TLS stay as in the release | GDPR Art. 32; OWASP ASVS; README rule 8 |
| **Metrics carry no user values.** Route patterns, never raw paths; counts, never identifiers | GDPR Art. 25; `internal/metrics/metrics.go:2` |
| **Slow-query logging without parameters** | ISO/IEC 27001 A.8.15 |
| **Clean up after each run.** Remove lab containers with their volumes (`docker rm -f -v`); never prune globally; wipe the lab appliance's database between runs | Hygiene; nothing generated outlives its run |
| **Results are evidence.** Each report states the release commit read back from the installed artifact, the environment, and the scenario's settings | Repeatability |

## Items from earlier phases

| From | Item | Where it is settled |
|---|---|---|
| Phase 0 | What to repeat on a reference VM | The lab appliance runs every scenario; S12 for the targets (D1) |
| Phase 1 | D1-H1, D1-H2, D1-M1 on a reference VM | S2 (D1-H1, D1-M1), S6 (D1-H2) |
| Phase 2 | A room-sign-in storm against generated stays (D2-M4) | S3 |
| Phase 3 | Count real probe and catch-all traffic on PRE-LIVE | P1 (needs its own authorization), then S4 |
| Phase 4 | D4-H1, D4-H2's parts, D4-H3 and D4-M1 on the appliance's kernel | S5 |
| Phase 5 | D5-H1 and D5-M1 on a reference VM; the full acctd loop at 2,000 devices after B1 and B2 | S6 |
| Phase 6 | PRE-LIVE resync duration from pmsd's progress log; a room-sign-in storm during a resync | P2 (needs its own authorization), S3, S7 |
| Phase 7 | Package activity and commerce purchases with the module on | S8, with payments in test mode and no provider reachable |
| Phase 7 | `public.guest_data_export` on a year of data | **Measured here:** D11-L1. S8 repeats it at 3 years with the erasure |
| Phase 7 | D7-H2 at the design write rate | S1 writes the accounting at the design rate; S8 reads the Overview on it |
| Phase 8 | Proxy throughput (D8-L3) and the Central list render (D8-M2) on reference hardware and an operator PC | S8, S11 |
| Phase 8 | Whether Next's proxy forwards a browser abort to edged (D8-L1) | **Settled here, by code:** yes. Next 15.5.27's `proxyRequest` destroys the upstream request when the browser's connection closes (`next/dist/server/lib/router-utils/proxy-request.js`), so edged's request context is cancelled. hotel-admin raises the proxy's timeout to 190 s (`next.config.mjs:37`), above any request's own limit. D8-L1's client-side fix still applies: the browser should abort replaced requests |
| Phase 8 | Sign-in restriction call timings | S8 |
| Phase 9 | A 1,000-appliance boot burst against a real ctrlapi; the fleet list with several operators; all key numbers on Central's reference hardware | S11 |
| Phase 10 | The memory budget on the reference VM with scd assigned and netd running | S1, S10 |
| Phase 10 | The backup stall on the reference VM's disk | S9 |
| Phase 10 | A day of design-load traffic for the journal's growth; the DHCP log's volume at 2,000 devices | S1 |

## Compliance

| Item | Rules | What the run or fix must respect |
|---|---|---|
| D11-L1 | GDPR Art. 15 | The bounded export returns exactly what today's returns for the same person. |
| P1, P2 | GDPR Art. 5(1)(c); CLAUDE.md §0B | Counts and timestamps only. No log line's other fields are copied out. No guest data goes into a report. Each needs its own authorization. |
| All scenarios | README rules 7 and 8; [Rules for every run](#rules-for-every-run) | Generated data, an isolated lab, security settings as shipped, cleanup after each run. |

## Tests run

| Test | Result |
|---|---|
| Full-schema database (migrations to 0150), 2 CPUs and 4 GB, Phase 1's settings; 1 million generated audit rows over one year | Built (413 MB) |
| The export's audit-log match, as in `0147`, with three placeholder identifiers: two runs with one parallel worker, two without | 2.5–2.6 s; 4.8–5.1 s |
| Next 15.5.27's rewrite proxy, read in `hotel-admin/node_modules`; `hotel-admin/next.config.mjs` | Abort forwarded; proxy timeout 190 s |
| Every "Handed on" table in Phases 0–10 | Items from earlier phases |

**Cleanup.**
- **Docker:** the database container was removed with its volume (`docker rm -f -v`). No audit containers remain. The volume count changed during the phase from other projects' activity, not this audit's.
- **Scratch:** the build log was deleted.
- **Repository:** only the audit files changed. hotel-admin's `node_modules` was read, not modified.
