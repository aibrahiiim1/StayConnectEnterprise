# Phase 7: Admin Console API (edged)

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE.

**Covers:**
- edged's read endpoints over the tables that grow (`data-plane/cmd/edged`):
  - the Overview and dashboard reports;
  - the session, PMS, device, account, usage, audit, sign-in-attempt and settlement lists;
  - the PMS interface health and revision pages.
- edged's own loops.
- How often the Hotel Admin pages that read these endpoints poll them.
- The items earlier phases handed on:
  - Phase 1: the reports that read `accounting_records`, and the session pages that filter `state = 'active'`;
  - Phase 5: edged's loops;
  - Phase 6: the PMS pages that read `stay_events`.

**How:**
- **Inventory:** a code inventory of every edged query over a growing table. It was read-only and run by a separate agent; I checked its top entries.
- **Database:** a throwaway full-schema database (`scripts/lib-fullschema-testdb.sh`), limited to 2 CPUs and 4 GB with the reference settings. It was filled with generated data only:
  - **one year of history:**
    - 732,000 sessions on 366,000 entitlements;
    - 400,000 devices;
    - 122,000 stays with their guests;
    - 370,000 PMS events;
    - 2,000,040 audit rows;
  - **the last 30 days:** 150,000 sign-in attempts;
  - **live:** 2,000 devices with 30 minutes of 1-second accounting detail. With the day's ended sessions, that is 4.2 million accounting rows.
- **Calls:** the real edged route groups were served through `httptest` as a `site_admin` session, three times each, from a temporary integration test that was deleted afterwards. That runs exactly the SQL the console triggers.
- **Slow-statement log:** statements over 500 ms were logged with `log_parameter_max_length = 0`, so no parameter values were written.
- **Not measured:** package activity, because the commercial-packages module was off in the harness.
- **Caveat:** the workstation's disk is not the appliance's. Phase 11 repeats the key numbers on a reference VM.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 2 | 4 | 2 | 0 |

**Main result.** With one year of history, two admin pages already fail and the session pages are slow. All three have the same cause as Phase 5's D5-H1: `iam_v2.sessions` cannot be searched by entitlement or by device (D7-H1).

| Page | Now | With the session indexes |
|---|---|---|
| PMS stays | times out at 10 s and returns 500 | 1.0 s |
| Client devices | spends 10 s and returns **an empty list with status 200**, because a read error is dropped (D7-M2) | 0.9–1.1 s |
| Sessions, active tab (refreshes every 10 s) | 3.7 s | 2.1 s |
| Sessions, ended | 6.3 s | 4.4 s |

**The Overview's traffic charts cannot work at the design load (D7-H2).** They sum raw per-second accounting detail. With only 30 minutes of it, a batch already took up to 3.5 s of its 4 s budget. A full day, a week or a month cannot fit.

**One audit query takes over 10 minutes against its 10 s limit (D7-M1).** The PMS revisions page joins the whole audit log to itself. Filtering each side first takes it to 0.63 s.

## Measured costs

All times with one year of generated history, served through edged's real routes. Each cell shows the first, second and third run.

| Route | Without the session indexes | With them |
|---|---|---|
| `GET /reports/overview?range=24h` | 11.1 s (traffic cache cold), 0.90 s, 0.79 s | 4.4 s, 0.79 s, 0.78 s |
| `…?range=7d` | 4.7 s, 0.85 s, 0.87 s | 3.9 s, 0.91 s, 0.87 s |
| `…?range=30d` | 10.5 s, 1.18 s, 1.11 s | 10.1 s, 1.13 s, 1.12 s |
| `GET /reports/dashboard` | 0.99–1.06 s | 1.03–1.11 s |
| `GET /reports/summary` | 0.24–0.25 s | 0.10–0.12 s |
| `GET /sessions?state=active` | 3.69–4.08 s | 2.07–2.17 s |
| `GET /sessions?state=ended` | 6.34–6.78 s | 4.40–4.50 s |
| `GET /pms-stays` | **10.0 s, status 500** (statement cancelled) | 0.97–1.05 s |
| `GET /client-devices` | **10.0 s, status 200 with no devices** | 0.88–1.11 s |
| `GET /usage/sources` | 1.30–1.39 s | 1.27–1.35 s |
| `GET /pms-events` | 0.52–0.64 s | 0.48–0.49 s |
| `GET /audit` | 0.24–0.65 s | 0.27–0.44 s |
| `GET /usage/stays` | 0.38–0.41 s | 0.40–0.41 s |
| `GET /pms-interfaces/{id}/health` | 0.10 s | 0.09–0.10 s |
| Sign-in attempts, guest accounts, settlements | 3–36 ms | 3–35 ms |

| Other measurement | Result |
|---|---|
| Overview traffic, one batch of 1,000 live sessions (about 1.8 million samples) | 0.53–3.47 s per statement, against `overviewStatementTimeout` of 4 s |
| PMS revision provenance (`pmsRevisionProvenanceSQL`), 2 million audit rows | **cancelled at 600 s**; plan cost 1.85 × 10⁹ (hash full join of the whole table with itself) |
| The same query with each side filtered by action first | 0.63 s, sequential scan, no new index |

The indexes used for the "with" column were the ones Phase 1 and Phase 5 already recommend, plus one for ordering:

```sql
CREATE INDEX ON iam_v2.sessions (entitlement_id);                                            -- Phase 5 D5-H1
CREATE INDEX ON iam_v2.sessions (device_id) WHERE ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT'); -- Phase 1 D1-M1
CREATE INDEX ON iam_v2.sessions (tenant_id, site_id, started DESC);                          -- this phase
```

## Findings

### D7-H1: The session, PMS stays and device pages look sessions up with no index, per row (High, Measured)

**Where:**
- **PMS stays** (`resources_phase3.go:191-263`): the summary and every row count active devices with `SELECT count(DISTINCT mac) FROM iam_v2.sessions WHERE entitlement_id = …`, once per stay. The summary does this across every matching stay.
- **Client devices** (`client_devices_admin.go:178-191`): a `LATERAL` per device reads `sessions WHERE device_id = d.id AND state = 'active' … ORDER BY started DESC LIMIT 1`, then sorts all devices by that result.
- **Sessions list** (`resources_sessions.go`):
  - each row counts active devices by `entitlement_id`, and counts the subject's earlier acquisitions over `entitlements` (`sessionCols`, around line 91);
  - the summary joins eight tables over every session matching the state;
  - the page orders by `started` with no index on it.
- **The same pattern, from the inventory but not timed:**
  - the guest accounts list (`guest_accounts_iamv2_lifecycle.go:129-217`, a `LATERAL` count over sessions per account, summed for the whole list);
  - commerce guest activity (`resources_commerce.go:814`).

**Measured:**
- **Without the indexes:** PMS stays fails at edged's 10 s `dbCtx` with status 500; client devices runs for 10 s; sessions takes 3.7–6.8 s.
- **With the three indexes above:** 1.0 s, 0.9–1.1 s and 2.1–4.5 s.

**Impact.**
- **Two pages stop working after about a year of normal use.** PMS stays and client devices are the front desk's daily tools.
- **The sessions page loads the database the whole time it is open.** Its active tab refreshes every 10 s (`hotel-admin/app/(app)/sessions/page.tsx:111`) and each refresh costs 2–4 s of database time. One open tab keeps a fifth to a third of one of the appliance's two CPUs busy.
- **It ties up connections.** edged has 4 database connections (Phase 1 D1-M3), and each refresh holds one for the whole query.
- **It gets worse each year,** because the scans grow with every session ever recorded.

**Recommended fix.**
1. **Ship the indexes.** Phase 5's `sessions (entitlement_id)` and Phase 1's live-device partial index, in their B1 migration, plus `sessions (tenant_id, site_id, started DESC)` for the ordered lists.
2. **Then make the active tab cheap:**
   - read the summary only for live sessions (the partial index) instead of joining every session;
   - compute "package acquisitions" only on the detail view, not for each row of a list that refreshes every 10 s.
3. **Then bound the ended and all tabs** by a time window, for example 30 days by default, with older history on request.

**Proof.**
- On this phase's data:
  - PMS stays and client devices answer in under 1 s;
  - the active tab in under 300 ms;
  - ended sessions in under 1 s for a 30-day window.
- `EXPLAIN` of each per-row subquery shows an index scan.

**Batch:** B1 (the indexes, with Phase 1 and 5) and B2 (the queries). B1 is CONTROLLED (schema); B2 is FAST.

### D7-H2: The Overview's traffic charts sum raw per-second detail, which cannot fit their time budget (High, Measured parts, extrapolated)

**Where:** `overviewTraffic` (`resources_overview.go:690-770`).
- **The read:** candidate sessions come from `sessions`. Their samples are then summed from `iam_v2.accounting_records`, 1,000 sessions per statement, under `SET LOCAL statement_timeout = 4s`.
- **On timeout:** the block is reported unavailable (`range_too_large_for_time_budget`).
- **The cache:** the result is cached per range for 1, 5 or 10 minutes.

**Measured.** With 30 minutes of 1-second detail for 2,000 devices, one batch took 0.53–3.47 s. A request that missed the cache took 4.4–11.1 s in total.

**Impact.**
- **A full day does not fit:**
  - at Phase 1's current write rate (about 5.7 million rows a day), one 24-hour batch reads about 2.9 million rows, which is over the budget;
  - at the design rate (up to 52 million rows a day), it reads about 26 million rows, roughly ten times the budget.
- **7 and 30 days can never be computed this way.** Staff see "range too large" where the traffic charts should be.
- **Cache misses are heavy:** each one does up to 4 s of reading per 1,000 sessions before giving up. The Overview is polled every 30 s (D7-M3), and the 24-hour cache expires every minute.

**Recommended fix.**
1. **Read traffic from a rollup, not from the detail.** Per hour, per session, keep bytes up and down, with the session's interface and package already attached.
   - **How it is written:** in the same transaction as accounting ingest, or as a TimescaleDB continuous aggregate if the detail table becomes a hypertable (Phase 1 D1-H3).
   - **What the Overview reads:** at most 24, 56 or 30 buckets per session from it, never the samples.
2. **This is the same decision as Phase 1's usage-detail grain** (per-minute for 30 days, hourly for 12 months): the hourly table is this rollup.

**Proof.**
- `overview?range=30d` answers in under 1 s with a cold cache, on 30 days of generated detail at the design rate.
- Its totals match a direct sum over the detail for the same sessions.

**Batch:** B3, after Phase 1's decision. CONTROLLED (schema; the accounting write path).

### D7-M1: The PMS revisions page joins the whole audit log to itself (Medium, Measured)

**Where:**
- `pmsRevisionProvenanceSQL` (`resources_phase3_interfaces.go:332-349`) is a `FULL JOIN public.audit_log a … public.audit_log p ON p.payload->>'revision_id' = a.payload->>'revision_id'`.
- The action filters sit in `ON` and `WHERE`, so on a full join Postgres can push neither down.
- It is not limited to this interface or tenant.
- `listPMSInterfaceRevisions` runs it whenever an interface has a revision.

**Measured** with 2,000,040 audit rows:
- **As written:** cancelled at 600 s. The plan is a hash full join of the whole hypertable with itself, at a cost of 1.85 × 10⁹.
- **Rewritten** as two filtered CTEs (`WHERE action = 'pms_interface_revision.authored'`, `WHERE action = 'pms_interface.revision_published'`), then full-joined: **0.63 s**.

**Impact.**
- **The page loses its provenance.** On a real appliance with a year of audit rows, the query always reaches edged's 10 s limit. The page then shows its configuration with provenance `UNAVAILABLE`, so who authored and who published each revision is never shown.
- **It wastes database time.** Each opening also spends the full 10 s scanning on one of edged's four connections.

**Recommended fix.**
1. Filter each side by its action before the join.
2. Scope both sides to this interface (`target_id`) and tenant.
3. Index `audit_log (action, target_id)`, or read provenance from the revision table if the authored and published facts are recorded there.

**Proof.** The revisions page answers in under 200 ms with 2 million audit rows, with the same provenance as today on a small database.

**Batch:** B2 (the rewrite; FAST). The index joins B1 (CONTROLLED).

### D7-M2: The client device list reports a failed read as "no devices" (Medium, Measured)

**Where.** `listClientDevices` (`client_devices_admin.go:192-206`) loops over `rows.Next()` and never checks `rows.Err()`. A statement cancelled mid-read (the 10 s `dbCtx`) ends the loop without an error.

**Measured.** Without the indexes, the page returned **status 200 with `"data": []`** after 10 s.

**Impact.**
- **Staff are misled:** the device list says the hotel has no devices, instead of reporting a failure.
- **It hides the slowness:** monitoring that counts 5xx responses sees nothing, so D7-H1 stays hidden.

**Recommended fix.**
- Check `rows.Err()` after the loop, and answer 503 with a clear message when the read was cut off.
- Apply the same check to every list handler that the inventory shows iterating rows. Most already do it; this one does not.

**Proof.** A unit test with a cancelled context returns an error status, not an empty list.

**Batch:** B2. FAST.

### D7-M3: Polled pages pay whole-table aggregates on every refresh (Medium, Measured)

**Where:**
- **The dashboard** polls `GET /reports/overview` every 30 s with a raw `setInterval` (`hotel-admin/app/(app)/dashboard/page.tsx:79`). Unlike `usePoll`, that keeps polling while the tab is hidden.
  - Each request runs four scans of `sessions`: candidates, online counts, sign-ins by method and the heatmap (`resources_overview.go:535-688`).
  - That costs 0.8–1.1 s with a warm traffic cache.
- **The active sessions tab** polls every 10 s (D7-H1).
- **The lists** recompute summaries over the whole table on every request (from the inventory, timed where marked):
  - sessions;
  - PMS events (0.5 s);
  - audit (`count(*)` and `GROUP BY action` over the period, with no default period; 0.27 s);
  - sign-in attempts;
  - usage sources (1.3 s: it groups every entitlement before applying `LIMIT`);
  - settlements;
  - the dashboard report (1.0 s: `count(DISTINCT mac)` over every session of the tenant, `resources_dashboard.go:187-199`).

**Impact.**
- **Constant load:** a front-desk PC with the dashboard left open all day makes 2,880 Overview requests: 38–53 minutes of database time a day.
- **It multiplies:** each extra open tab, whether hidden or not, adds the same again.
- **It grows:** every aggregate gets slower with history, and edged's 4 connections are shared with staff actions (Phase 1 D1-M3).

**Recommended fix.**
1. **Use `usePoll` on the dashboard** so a hidden tab stops polling. This is a front-end change, handed to Phase 8.
2. **Cache the Overview's non-traffic blocks** for the length of one poll interval (30 s), shared by all tabs, as the traffic block already is.
3. **Bound every list summary by the list's own time window,** and give audit and usage a default window (30 days).
4. **Usage sources:** limit the stays, accounts and vouchers that are candidates before aggregating their sessions.

**Proof.** With five dashboard tabs open, three of them hidden, Overview database time is under 5% of one CPU, and every list answers under 500 ms on this phase's data.

**Batch:** B2. FAST.

### D7-L1: Three handlers have no request timeout of their own (Low, Confirmed)

**Where:**
- the usage list (`resources_usage.go:101`);
- session samples (`:448`);
- commerce purchases (`resources_commerce.go:355`).

These use `r.Context()` instead of `dbCtx`, so only svc_edged's 60 s `statement_timeout` bounds them.

**Impact.** A slow call holds one of edged's 4 connections for up to a minute.

**Recommended fix.** Use `dbCtx`, the same 10 s as every other list.

**Batch:** B2. FAST.

### D7-L2: The health loop trims its event table with a full anti-join every 10 s (Low, Confirmed)

**Where.** `health.go:144`. Every 10 s it runs `DELETE FROM appliance_recovery_events WHERE id NOT IN (SELECT id … ORDER BY id DESC LIMIT 2000)`, besides `systemctl show` for each service and a probe.

**Impact.** Small: the table is held at 2,000 rows. It is still a write transaction every 10 s on an idle appliance.

**Recommended fix.** Trim only when the table holds more than 2,000 rows, or once an hour.

**Batch:** B2. FAST.

## Items from earlier phases

| Item | Outcome |
|---|---|
| Phase 1: reports reading `accounting_records` | Only the Overview's traffic block and session samples read it. The Overview is D7-H2. Session samples are per session, `LIMIT 2001`, and fine apart from D7-L1. |
| Phase 1: edged session pages filtering `state = 'active'` | D7-H1. The live-device partial index from D1-M1 is one of the three indexes measured. |
| Phase 5: edged's loops | The guest-data reads loop (1 s) only drains an in-memory queue into `audit_log`, deduplicated over 10 minutes, so it does no large reads. The health loop is D7-L2. |
| Phase 6: PMS pages reading `stay_events` | PMS events took 0.48–0.64 s. Its whole-table summary is part of D7-M3. Interface health took 0.09–0.10 s. PMS stays is D7-H1. |

## Handed on

| To | What |
|---|---|
| Phase 8 | The dashboard's raw `setInterval` (D7-M3), the 10 s sessions poll, and every page that polls while hidden. |
| Phase 10 | edged's CPU and connection share under polling, with the pool size from D1-M3. |
| Phase 11 | Package activity and commerce purchases with the module on. The guest data export function (`public.guest_data_export`) on a year of data. D7-H2 on a reference VM at the design write rate. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D7-H1 indexes, D7-M1 index | Added to the Phase 1 and Phase 5 index migration. **Do first.** CONTROLLED. |
| **B2** | D7-H1 queries, D7-M1 rewrite, D7-M2, D7-M3, D7-L1, D7-L2 | edged query and handler changes. FAST. |
| **B3** | D7-H2 | The traffic rollup, after Phase 1's usage-detail decision. CONTROLLED. |

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D7-H1, D7-M3 | GDPR Art. 25, Art. 32 | Faster lists show the same fields to the same roles (`permFor`, `rolePerms`). Caches are per site and per role-visible block, never shared across roles. |
| D7-H2 | GDPR Art. 5(1)(c), (e); ePrivacy Art. 6; Art. 15, 17 | An hourly rollup is coarser traffic data, which minimisation prefers. It is kept no longer than the hotel's retention setting, and the guest export and erasure must include it (README rule 4). |
| D7-M1 | ISO/IEC 27001 A.8.15 | Provenance becomes readable again. The rewrite reads the same audit rows and changes none. |
| D7-M2 | GDPR Art. 32(1)(b) | Reporting a failure instead of an empty list is part of the system's integrity. |
| Logging in this phase | README (ISO 27001 A.8.15) | The slow-statement log ran with `log_parameter_max_length = 0`, in the throwaway database only. |
| All | README rule 7 | Generated data only; no guest data was read or reproduced. |

## Tests run

| Test | Result |
|---|---|
| Full-schema database, 2 CPUs and 4 GB, reference settings, one year of generated history | Built |
| Temporary integration test in `cmd/edged` serving 20 routes through their real route groups as `site_admin`, three runs each, before and after the three indexes | Measured costs table |
| Slow-statement log (over 500 ms, no parameter values) for one pass | The statements behind D7-H1 and D7-H2 |
| `EXPLAIN (ANALYZE)` of the provenance query as written (cancelled at 600 s) and rewritten | D7-M1 |
| Read-only code inventory of every edged query over a growing table | D7-M3, D7-L1, D7-L2 |

**Test-only changes, made inside the throwaway database and never in the repository:**
- One PMS interface row was added so the PMS pages had an interface to read.
- Parallel query was turned off for the provenance run, because the container's 64 MB `/dev/shm` is smaller than the appliance's.

**Cleanup.**
- **Test file:** the temporary test (`zz_perfaudit_integration_test.go`) was deleted, and `git status` is clean.
- **Docker:** the container was removed with its volume (`docker rm -f -v`).
- **Scratch:** the generator scripts were deleted.
- **Not mine:** two containers and two volumes appeared during the phase, from another project's testcontainers (SeaweedFS, Postgres 17). This repository does not use them, and they were left alone.
- **Image:** the TimescaleDB image was already present and was kept.
