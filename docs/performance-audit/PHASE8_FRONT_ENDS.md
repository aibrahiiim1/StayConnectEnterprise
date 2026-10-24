# Phase 8: Admin front ends (hotel-admin, cloud-admin)

**Audited:** master `653363d7`, confirmed by a fetch before committing. Code and repository only: nothing was run against PRE-LIVE or Central.

**Covers:**
- the Hotel Admin console (`hotel-admin`, Next.js 15.5, served by Node on the appliance);
- the Central console (`cloud-admin`, Next.js 14.2, served by Node on Central);
- their bundles, polling, list rendering, the Node server's memory and its API proxy, and the shipped package;
- the items earlier phases handed on:
  - Phase 7: the dashboard's raw `setInterval` (D7-M3), the 10 s sessions poll, and every page that polls while hidden.

**How:**
- **Builds:** both apps were built for production with `next build`, as the deploy script does. hotel-admin was built in place; its existing `.next` was moved aside first and restored afterwards. cloud-admin was built in a scratch copy installed with `npm ci` from its lockfile, because the workstation's `node_modules` was older than the lockfile.
- **Servers:** each standalone server (`node server.js`) was run from a scratch copy against a small stub API on loopback:
  - the stub stood in for edged or ctrlapi, answered every call with minimal JSON and logged only method, path and whether the call was marked background;
  - the cloud stub served a generated fleet of 200 or 1,000 appliances with `.invalid` host names and `192.0.2.0/24` (TEST-NET-1) addresses.
- **Browser:** pages were opened in the desktop app's built-in Chromium browser. Requests were counted by the stub's log and by wrapping `fetch` in the page, together with `document.hidden` for each call.
- **Main-thread cost:** the longest gap seen by a 10 ms timer while a list refresh runs. A gap is time the page cannot respond to input.
- **Node:** CPU time and private memory of the server process, read from Windows. A load script sent 2,000 requests through the server's API proxy and 2,000 straight to the stub.
- **Code:** every `setInterval`, `usePoll` and paged list in both apps was read.
- **Caveat:** a fast Windows workstation is neither the appliance nor a front-desk PC. Phase 11 repeats the render and proxy numbers on reference hardware.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 2 | 4 | 0 |

**Main result.** The bundles are small and the shared poll hook (`usePoll`) is right: it pauses in hidden tabs and refreshes on return. The cost comes from the places that do not use it.

- **Raw timers keep polling in hidden tabs (D8-M1).** Six hotel-admin components poll with a bare `setInterval`. The expensive one is the dashboard: measured hidden, it still asks for the Overview, health and alerts every 30 s. That is Phase 7's D7-M3: 0.8–1.1 s of database time per Overview request.
- **The Central appliance list re-renders the whole fleet on every 30 s refresh (D8-M2).** At 1,000 appliances each refresh downloads 532 kB of JSON and freezes the page for 0.43–0.85 s. At the 200-appliance design fleet it is 0.12–0.35 s.

## Measured

### Bundles

| | hotel-admin | cloud-admin |
|---|---|---|
| JavaScript shared by every page | 102 kB | 87.1 kB |
| First load per page | 103–216 kB (largest: internet packages 216 kB, portal branding 200 kB, PMS interfaces 198 kB) | 87–182 kB (largest: appliance detail 182 kB) |
| Middleware | 34.2 kB | 27.3 kB |
| Static assets | 3.6 MB | 1.7 MB |
| Standalone server tree | 66 MB | 22 MB |
| Production build | compiled in 73 s, then type-checked | built |

Sizes are as `next build` reports them. Every page is a client component and nothing is split with `next/dynamic`. At these sizes that is not a problem.

### Requests from an idle console (hotel-admin)

Measured with the stub; the other rows are read from the code.

| Page | Visible | Hidden | Source |
|---|---|---|---|
| Every page (layout) | `whoami` and `/health` every 30 s: 4 a minute | none | `usePoll` |
| Dashboard, on load | 9 requests | | measured |
| Dashboard, polling | 5 every 30 s: Overview, alerts, `/health` twice, `whoami` | **3 every 30 s: Overview, alerts, `/health`** | measured |
| Diagnostics (health) | `/diagnostics/services` every 10 s | **the same: 5 calls in 43 s hidden** | measured |
| Sessions, active tab | every 10 s | none | `usePoll` |
| Guest sign-in attempts | restrictions every 15 s, network pauses every 30 s | **the same** | raw `setInterval` |
| Sign-in methods | network pauses every 30 s | **the same** | raw `setInterval` |
| PMS interfaces | list, routing and one health call per interface every 20 s (4 s during a sync) | none | `usePoll` |
| PMS interfaces, connection sheet open | the same reload again every 10 s (3 s during a sync) | **the sheet's reload continues** | raw `setInterval` |
| Appliance | every 15 s (5 s while activating) | none | `usePoll` |

**Measured switch to hidden.** With the dashboard open, the browser pane was hidden at 187 s and shown again at 234 s:
- **Hidden:** the layout's `whoami` and `/health` polls stopped, but the dashboard's timer fetched the Overview, alerts and `/health` again at 211 s.
- **Visible again:** `usePoll` refreshed at once, at 234 s.

Chrome and Edge slow a hidden tab's timers to about once a minute after five minutes. So a dashboard hidden all day costs about half its visible rate: still around 1,440 Overview requests a day, each 0.8–1.1 s of database time (D7-M3).

**cloud-admin** polls only through its own `usePoll`, which stops its timer in a hidden tab:
- every page: `whoami` every 30 s;
- Overview: every 30 s;
- appliance list: the list and the Overview counts every 30 s;
- appliance detail: every 30 s, or 5 s while activating.

### Central appliance list

| Fleet | List response | Longest main-thread gap per refresh | Rows rendered |
|---|---|---|---|
| 200 | 106 kB | 123–352 ms | 200 |
| 1,000 | 532 kB | 425–853 ms | 1,000 (18,194 DOM nodes) |
| Idle page, no refresh | | 21–57 ms | |

These are three refreshes each, triggered while the page was visible.

### Node server (hotel-admin)

| Measure | Result |
|---|---|
| Private memory, after logging in and opening pages | 122 MB |
| After the load runs and a 200 MB download | 265–290 MB, not returned to the system |
| While a 2 MB/s client downloads 200 MB through the proxy | flat at 264–277 MB: the proxy streams with backpressure and does not buffer |
| Systemd limit | `MemoryHigh=640M`, `MemoryMax=768M` (`deploy/systemd/stayconnect-hotel-admin.service:48-49`) |
| Node CPU per proxied API call | 5.2 ms |
| One client, 2,000 calls | stub direct p50 6.9 ms; through Node p50 9.9–15 ms |
| 20 clients at once, 2,000 calls | stub direct 617 calls/s, p95 60 ms; **through Node 85 calls/s, p95 622 ms** |

cloud-admin's server used 89 MB with the 1,000-appliance list open.

## Findings

### D8-M1: Raw timers keep polling in hidden tabs (Medium, Measured)

**Where.** These components poll with a bare `setInterval` instead of `usePoll` (`hotel-admin/lib/use-poll.ts`):

| Component | Interval | What it calls |
|---|---|---|
| `app/(app)/dashboard/page.tsx:79` | 30 s | Overview, `/health`, `/operational-alerts` |
| `app/(app)/health/page.tsx:133` | 10 s | `/diagnostics/services` |
| `app/(app)/pms-interfaces/synchronization-card.tsx:74` | 10 s, 3 s in a sync | the PMS page's whole reload: list, routing, health per interface |
| `components/guest-signin-restrictions.tsx:80` | 15 s | `/guest-signin-restrictions` |
| `components/guest-signin-network-pause.tsx:192` | 30 s | `/guest-signin-restrictions/network-pauses` |

The 1 s tickers that only redraw a countdown (`patterns.tsx`, `idle-warning.tsx`, `network/system/page.tsx`, `guest-signin-restrictions.tsx:28`) make no requests and are fine.

**Problem.**
- **Hidden tabs keep costing the appliance.** A hidden dashboard keeps asking for the Overview, which is the costliest read in the console (D7-H2, D7-M3).
- **The PMS sheet doubles the page's own poll.** With the connection sheet open, the card reloads the page every 10 s on top of the page's 20 s poll, and keeps doing so when hidden.
- **No overlap guard.** None of these timers waits for the previous call. When a call takes longer than the interval, the calls stack up (D8-L1).

**Impact.**
- **Database time:** about 1,440 Overview requests a day from one dashboard left in a background tab.
- **Measured hidden behaviour:** both raw timers measured (dashboard and diagnostics) kept polling while hidden.
- **The rest:** the diagnostics call itself is cheap, because it reads health that edged's 10 s loop has already stored. The sign-in restriction calls were not timed.

**Recommended fix.**
1. Replace each raw timer with `usePoll`. It already accepts an interval that changes, which covers the card's 10 s and 3 s modes.
2. The synchronization card should not reload the whole PMS page. It should ask only for its own interface's health, and leave the page's list to the page's poll.
3. Add a lint rule (`no-restricted-syntax` on `setInterval` under `app/` and `components/`) that allows only the display tickers. That keeps the next page from repeating this.

**Proof.** With the dashboard, diagnostics, sign-in attempts and PMS sheet open and their tab hidden for 10 minutes, the stub logs no requests. When the tab becomes visible again, each page refreshes once.

**Compliance.** Polls must keep the background mark (`inBackground`, `X-OneGate-Background`). It stops polling from extending a staff session past its idle limit (GDPR Art. 32; `data-plane/cmd/edged/auth.go:406`).

**Batch:** B1. FAST.

### D8-M2: The Central appliance list re-renders the whole fleet on every refresh (Medium, Measured)

**Where.**
- **The page:** `cloud-admin/app/(app)/appliances/page.tsx:62-78`:
  - fetches every appliance in scope, with no paging, every 30 s;
  - fetches the Overview again for the counts;
  - replaces the row array with newly parsed objects.
- **The table:** `components/appliance-table.tsx:51-92` renders every row again, each with a `Link`, three badges and their tooltips.
- **Customer detail:** `customers/[id]/page.tsx:530` uses the same table.

**Problem.** Every refresh rebuilds every row whether or not anything changed. The payload and the render grow with the fleet.

**Impact.** At the 1,000-appliance stress fleet:
- each refresh moves 532 kB of uncompressed JSON;
- each refresh freezes the page for 0.43–0.85 s, measured on a fast workstation;
- an operator scrolling or typing into the filter feels the freeze every 30 s.

At the 200-appliance design fleet the freeze is 0.12–0.35 s, and it grows linearly.

**Recommended fix.**
1. **Keep unchanged rows.** Keep each unchanged row's object identity and memoise the row component, as hotel-admin's sessions list does (`keepUnchanged`, `sessions/page.tsx:101-108`). Only changed rows then render.
2. **Page the list in ctrlapi** (50–100 rows), with the filters applied on the server. This is handed to Phase 9, which owns the endpoint.
3. **Optional:** take the counts from the list response instead of a second Overview call.

**Proof.** At 1,000 appliances, a refresh with no change keeps the longest main-thread gap under 50 ms, and the list response is under 50 kB.

**Compliance.** Paging must keep the server's role scope: a customer-scoped operator still sees only that customer's appliances (GDPR Art. 25, 32).

**Batch:** B2. FAST. The ctrlapi part is in Phase 9.

### D8-L1: Polls can overlap, and replaced requests are never cancelled (Low, Confirmed)

**Where.**
- **`usePoll`:** both apps' `usePoll` start the next call on schedule even if the last one has not answered.
- **`useServerPage`** (`hotel-admin/lib/use-server-page.ts:46-62`) drops a superseded answer but does not abort its request.
- **The API client:** `lib/api.ts:47` passes no `AbortSignal`.

**Problem.**
- **Overlap:** the active sessions tab polls every 10 s, and its call takes 3.7 s with a year of history (D7-H1). Up to 10 s is possible, at edged's request limit. A slow appliance then has two of the same query running.
- **Stale work:** a filter change starts a new query while the old one keeps running to the end.

All searches are already debounced by 300 ms, so typing alone does not cause a burst.

**Impact.** Low today. The cost rises with D7-H1 until its indexes land, and it competes for edged's 4 connections (D1-M3).

**Recommended fix.**
1. **Skip a busy tick:** `usePoll` skips a tick while the previous call is still pending.
2. **Abort superseded calls:** `useServerPage` aborts a superseded call with an `AbortController`.
3. **Pass the signal:** `request()` passes the signal through to `fetch`.

edged derives each query's context from the request (`dbCtx`, `cmd/edged/common.go:50-51`), so the database work stops when the request is dropped. Whether Next's rewrite proxy forwards the browser's abort to edged is **Suspected**: the fix measures it.

**Proof.** With the stub delaying answers by 15 s, a 10 s poll keeps only one call in flight. A filter change leaves one active request at edged.

**Batch:** B1. FAST.

### D8-L2: The same data is fetched twice (Low, Measured)

**Where.**
- **Health:** on the dashboard, `/health` is fetched by the top-bar badge (`components/appliance-status.tsx:31`) and again by the page's own load (`dashboard/page.tsx:53`). That makes 2 calls every 30 s, measured.
- **Who am I:** 22 hotel-admin files call `/auth/whoami` themselves on mount, although the layout already holds it in `WhoamiContext` and refreshes it every 30 s. Examples: PMS interfaces, client groups, notifications and diagnostics.

**Impact.** One to two extra cheap calls per page open, and one per 30 s on the dashboard.

**Recommended fix.**
1. Read roles from `useOperatorRoles()` (`lib/whoami-context.tsx:61`) instead of fetching.
2. Let the dashboard read the badge's health, or drop its own `/health` call.

**Batch:** B1. FAST.

### D8-L3: Every console API call passes through Node (Low, Measured)

**Where.**
- **Proxy:** Next's rewrite proxy (`hotel-admin/next.config.mjs:39-43`) forwards every `/api/*` call from the browser to edged. Caddy sends everything for the management vhost to Node (`deploy/scripts/hotel-admin-cert-manager.sh:212`).
- **Central:** cloud-admin does the same for ctrlapi.

**Impact.**
- **Per call:** 5.2 ms of Node CPU, and 3–8 ms more latency at one call at a time.
- **Under load:** with 20 calls at once, Node passed 85 calls/s at p95 622 ms. The stub alone answered 617 calls/s at p95 60 ms.
- **At the design load:** idle consoles make a few calls a minute, so the hop is not visible. It only matters for many open tabs or bursts, which Phase 11 tests.

**Recommended fix.** None now. Fix D8-M1 first, which removes most of the background calls. If Phase 11 shows the hop matters, routing `/api/edge/*` straight from Caddy to edged is a DECISION and is CONTROLLED: it changes the management vhost, its logging rules and its headers.

**Batch:** none. Recorded for Phase 10 and Phase 11.

### D8-L4: The hotel-admin package ships 27 MB it never uses (Low, Measured)

**Where.** The tarball in this checkout (`hotel-admin-deploy.tgz`, packaged on this Windows workstation by `deploy/scripts/deploy-hotel-admin.sh` package) is 17.8 MB compressed and 64.3 MB unpacked. Its largest parts:
- `next` 25.2 MB;
- `@img/sharp-win32-x64` 18.9 MB;
- `typescript` 8.5 MB.

hotel-admin does not use `next/image`, so the sharp image binaries are never loaded. A tarball packaged on Linux carries the Linux sharp build instead. TypeScript is only needed to read a `next.config.ts`, and this app uses `next.config.mjs`. cloud-admin's tree is 22 MB.

**Impact.** Disk space and copy time on every deployment and every kept release, with no runtime cost.

**Recommended fix.** Exclude `@img/**`, `sharp/**` and `typescript/**` with `outputFileTracingExcludes` in `next.config.mjs`. Check that `server.js` starts and serves every page.

**Proof.** The unpacked package shrinks by about 27 MB, and the deploy script's own tarball checks pass.

**Batch:** B3. FAST. Verify at the next PRE-LIVE deployment.

## Measured clean

| Area | Result |
|---|---|
| Bundle sizes | 87–216 kB first load; no page needs splitting at this size. |
| `usePoll` in both apps | Pauses in hidden tabs, refreshes when the tab returns, and marks its calls as background. Measured on the dashboard. |
| Search boxes | Every server-side search is debounced by 300 ms (`SearchInput delay={300}`). |
| Growing hotel-admin lists | Sessions, stays, accounts, devices, audit, sign-in attempts, DHCP, PMS events and resolutions are paged by the server (`useServerPage`, at most 200 a page). The PMS reconciliation lists are capped at 500 rows by edged. |
| Sessions list | Keeps unchanged rows between polls, so a refresh re-renders only what changed. |
| Large downloads through the proxy | Streamed with backpressure; Node's memory stays flat. |
| Node memory | 89–290 MB, well inside hotel-admin's 640 MB high mark. |

## Items from earlier phases

| Item | Outcome |
|---|---|
| Phase 7: the dashboard's raw `setInterval` (D7-M3) | D8-M1, measured: it keeps polling while hidden. |
| Phase 7: the 10 s sessions poll | Already uses `usePoll`, only on the active tab, and pauses when hidden. Its cost is D7-H1. Its overlap risk is D8-L1. |
| Phase 7: every page that polls while hidden | D8-M1 lists all five. The 1 s tickers make no requests. |

## Handed on

| To | What |
|---|---|
| Phase 9 | `GET /cloud/v1/appliances` is unpaged: 532 kB at 1,000 appliances. Each open list tab polls it and the Overview every 30 s (D8-M2). |
| Phase 10 | hotel-admin Node: 122 MB after use, 265–290 MB after load, 5.2 ms CPU per proxied call. cloud-admin Node: 89 MB. The 64 MB unpacked package (D8-L4). |
| Phase 11 | Repeat the proxy throughput (D8-L3) and the Central list render (D8-M2) on reference hardware and a typical operator PC. Check whether Next's proxy forwards a browser abort to edged (D8-L1). Time the sign-in restriction calls. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D8-M1, D8-L1, D8-L2 | hotel-admin polling and the API client. **Do first.** FAST. |
| **B2** | D8-M2 | Row reuse and memoised rows in cloud-admin first. Server paging with Phase 9. FAST. |
| **B3** | D8-L4 | Trim the standalone package. FAST; verify at the next PRE-LIVE deployment. |

D8-L3 has no batch; it waits for Phase 11.

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D8-M1, D8-L1 | GDPR Art. 32; README (OWASP ASVS) | Polls stay marked as background, so they never extend a staff session's idle limit. Aborting a call changes no data. |
| D8-M2 | GDPR Art. 25, 32 | Paging and row reuse keep the server's role and customer scope. Nothing is cached across operators. |
| D8-L2 | GDPR Art. 32 | Pages read roles from the layout's revalidated `whoami`; a role change still reaches them within 30 s. |
| D8-L3 | ISO/IEC 27001 A.8.15 | A future Caddy route to edged must keep the access-log rules that drop queries and headers (`hotel-admin-cert-manager.sh:220-227`). |
| Test data | README rule 7 | The stubs served generated data only (`.invalid` names, TEST-NET-1 addresses). Their logs held method and path only, and were deleted. |

## Outside this audit

These were seen in passing and are not performance findings.
- **cloud-admin's Next version:** cloud-admin is on Next.js 14.2.5, for which npm reports a security advisory. Its middleware only checks that a session cookie exists, and ctrlapi authorises every call, so a middleware bypass would expose only the page shell. The upgrade still belongs in the security backlog.
- **CSP:** the hotel-admin vhost's CSP allows `'unsafe-eval'` (`hotel-admin-cert-manager.sh:210`). This is for the security backlog as well.

## Tests run

| Test | Result |
|---|---|
| `next build` of hotel-admin (in place) and cloud-admin (scratch copy, `npm ci` from the lockfile) | Bundles table |
| hotel-admin standalone server against the stub edged; dashboard and diagnostics pages, visible and hidden, with in-page `fetch` logging | Requests table, D8-M1, D8-L2 |
| cloud-admin standalone server against the stub ctrlapi with 200 and 1,000 generated appliances; three refreshes each with a 10 ms timer probe | D8-M2 |
| Load script: 2,000 calls at 1 and 20 at once, direct and through Node; Node CPU time; memory during a fast and a 2 MB/s 200 MB download | Node table, D8-L3 |
| Listing of the checkout's `hotel-admin-deploy.tgz` | D8-L4 |
| Read of every `setInterval`, `usePoll` and paged list in both apps | Requests table, D8-M1, D8-L1 |

**Cleanup.**
- **Processes:** the two Node servers and two stubs were stopped.
- **Builds:** both apps' original `.next` folders were moved aside before building and restored afterwards. cloud-admin's in-place build had failed on the stale `node_modules`; it was then built in the scratch copy.
- **Scratch:** the scratch copies (standalone servers, the cloud-admin copy and its `node_modules`), the stubs, the request logs and the build logs were deleted.
- **Repository:** `git status` is clean.
- **Docker:** no Docker was used in this phase.
