# Phase 3: Client Portal (portald) and captive-portal probes

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE.

**Covers:**
- the portal every guest device reaches before it signs in: pages, probes, assets and the calls portald makes to scd;
- the TLS front on port 8343;
- what a device that never signs in costs the appliance;
- the items Phase 2 handed on (how a busy scd is shown to the guest, and portald's timeouts toward scd).

**How:**
- **Code:** the request paths were traced in `cmd/portald` and `deploy/nftables`.
- **Benchmarks:** portald's own handler was benchmarked on 2 CPUs (`GOMAXPROCS=2`, parallel clients), in a throwaway copy of the module:
  - the landing page, compressed and not;
  - an unknown path (the catch-all);
  - the Google and Windows probes.
  - In those runs scd was absent, so the branding fetch failed fast and the defaults were used. That is the portal's own cost without scd's share.
- **Neighbour lookup:** the `/proc/net/arp` scan was benchmarked with 500, 2,000 and 5,000 neighbours.
- **TLS:** a refused TLS handshake with the portal's ECDSA P-256 key was benchmarked.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 2 | 6 | 0 |

**Main result.** Every device on the guest network that has not signed in is redirected to portald.

The expensive part is the landing page, which portald returns for:
- Apple's probe;
- the catch-all, that is, any HTTP request a device's apps send to any other site.

**Measured** on 2 CPUs:
- **Size:** 214 KB of HTML, 54 KB compressed.
- **Cost:** 3.4 ms of CPU to build and compress, plus 1 ms to find the device's hardware address among 2,000 neighbours.
- **Ceiling:** about **200–290 landing pages a second, using both CPUs**, before anything else on the appliance gets a cycle.
- **Follow-up calls:** each page view in a browser then makes three more calls, two of which reach scd and its database (D3-M2).

**Nothing limits one device's page requests.** The backend audit already reported that (Phase 8 M1), and this phase measures its cost: one looping device can take most of the appliance's CPU.

**Fix:**
- serve the landing page from a cache;
- answer the catch-all with a small redirect;
- add the per-device limit.

Together these bring a page view to a fraction of a millisecond.

**Already right:**
- The Google and Windows probes are a 12 µs redirect.
- DHCP option 114 (RFC 8910, the captive-portal API) is advertised, so recent Apple and Android devices can skip probing (`deploy/kea/kea-dhcp4.conf:42`).
- Templates are parsed once, and signed-in devices bypass portald entirely.

## Measured costs

| Request | CPU per request (2 CPUs) | Ceiling with both CPUs | Bytes sent |
|---|---|---|---|
| Landing page, compressed | 3.4 ms | about 290 a second | 54 KB |
| Landing page, uncompressed client | 0.75 ms | about 1,300 a second | 214 KB |
| Unknown path (catch-all), compressed | 3.0 ms | about 330 a second | 54 KB |
| `/generate_204`, `/connecttest.txt` | 0.012 ms | about 80,000 a second | 31 bytes |
| Neighbour lookup, 500 / 2,000 / 5,000 neighbours | 0.37 / 1.04 / 2.41 ms | added to every landing page and sign-in | |
| Refused TLS handshake (an app's HTTPS to another site) | ≤ 0.74 ms (client and server together) | ≥ 1,300 a second | |

## Findings

### D3-M1: The landing page is rebuilt and compressed for every request, including every unknown path (Medium, Measured)

**Where:**
- **Who gets it:** `cmd/portald/main.go:155-182`, `landing`. It runs for `/`, Apple's `/hotspot-detect.html` and `/library/test/success.html`, and the router's catch-all `NotFound` (`main.go:589-598`). The catch-all answers any path an unsigned device's software requests.
- **What is redone per request:**
  - the template is executed;
  - the full English word map is copied and overlaid with the guest's language and the hotel's words (`portal_page.go:123`);
  - the language and translation tables are marshalled to JSON;
  - the hotel's style sheet is run through a regular expression (`portal_page.go:257`);
  - a new CSP nonce is made (`portal_design.go:116`);
  - the whole page is compressed at gzip level 6 (`portal_gzip.go`).
- **Headers:** the page is `no-store`.

**Measured:** see the table above. Compression is about 80% of the cost.

**Problem.**
- **Every unsigned device costs a full page per HTTP request.** That includes devices whose owners never sign in: background app updates, analytics, news widgets.
- **Each one is a 54 KB answer** to a request that cannot use it. Clients that do not ask for compression get 214 KB.
- **The cost does not depend on who asks.** The page differs between requests only by the nonce, the leave token, and the language.

**Impact** (cost Measured; load Suspected until Phase 11 counts real probe traffic).
- **CPU:** 100 such requests a second (for example 500 unsigned devices each making one HTTP request every 5 s) take about 35–45% of the reference appliance's CPU. That CPU is the same one sign-in and accounting need (Phase 1 D1-H2).
- **Bandwidth:** about 5 MB/s of the guest network's airtime.

**Recommended fix.**
1. **Answer unknown paths with a redirect, not the page.** Use a `302` to the portal's root, as the Google and Windows probes already get, or `511 Network Authentication Required` (RFC 6585). Browsers follow either to the page; app traffic stops at 31 bytes.
2. **Cache the rendered page.** Key it on language and design version. Keep the compressed bytes too, so each is built once and served many times.
3. **Take the per-request parts out of the cached body:**
   - replace the nonce with script and style hashes in the Content-Security-Policy, which is equally strict;
   - give the leave token through a cookie or the existing status call.
4. **Keep Apple's probe returning a page** that is not "Success", as Apple requires, but serve it from the cache.

**Proof.**
- On 2 CPUs: a cached landing page under 0.2 ms, the catch-all under 0.05 ms, and identical page content to today.
- The existing portal tests, including `portal_a11y_test.go`, the CSP browser test and the language tests, still pass.

**Compliance.**
- **CSP:** it stays at least as strict, with hashes in place of the nonce (OWASP ASVS V14.4).
- **No guest data in the cache:** the cache holds only what is the same for every guest of that language and design (GDPR Art. 25). Per-device values, such as the leave token or a returning-device welcome, never enter it.
- **Accessibility:** the page and its notices are unchanged.

**Batch:** B1.

### D3-M2: Each page view makes three more calls, two of which reach scd and its database (Medium, Confirmed)

**Where:** on load, the page's script calls:
- **`/api/auth-methods`** (`templates.go:1117`). portald forwards it to scd every time, with no cache (`email_handlers.go:27-55`). scd then reads the tenant's settings from the database (Phase 2 D2-L2).
- **`/api/branding`** (`templates.go:922`). It comes from portald's 3-second design cache. That cache has no single-flight, so when it expires, every request in flight asks scd (`portal_design.go:136-157`).
- **`/access/status`** (`templates.go:1182`). It is forwarded to scd and its database for every view, including devices that have never had a session.

**Problem.** A device opening the portal costs scd 2 or more database round trips before the guest does anything, on scd's 4-connection pool (Phase 1 D1-M3). This multiplies the D3-M1 load into the database.

**Impact.** At 100 page views a second, that is 200 or more scd requests and database reads a second, competing with sign-ins.

**Recommended fix.**
1. **Put the sign-in methods and the branding into the cached page** (D3-M1), keyed on their versions, so the two calls are not needed on load.
2. **Ask `/access/status` only when it can matter.** It shows the "your access has ended" notice, which needs a previous session, so call it only when the device presents the resume cookie or portald has seen it sign in before.
3. **Add single-flight to the design cache refresh.**

**Proof.** A first page view by a new device causes no scd request. The "ended" notice still shows for a device whose access ended.

**Compliance.** The status notice stays specific to the device that asks, and is never cached across devices.

**Batch:** B1.

### D3-L1: Finding a device's hardware address reads the whole neighbour table (Low, Measured)

**Where:**
- **The lookup:** `cmd/portald/arp.go:13-34` opens `/proc/net/arp` and scans every line, formatting the address again for each line. It runs on every landing page and every sign-in.
- **Sign-in POSTs** (`arp_resolve.go:36-60`) also poll it every 40 ms for up to 400 ms.

**Measured:** 0.37 ms at 500 neighbours, 1.04 ms at 2,000, 2.41 ms at 5,000, with 388 KB allocated per lookup at 2,000.

**Impact.** It adds about 30% to a landing page at the design load, and it grows with the number of devices on the segment.

**Recommended fix.**
- Ask the kernel for the one address over netlink (`RTM_GETNEIGH`).
- Or keep one table, read once a second and shared by all requests.
- Format the wanted address once, before the loop.

**Batch:** B2.

### D3-L2: Hotel images have no cache lifetime and no size limit beyond 8 MB (Low, Confirmed)

**Where:**
- `cmd/portald/main.go:570-571`: `/assets/*` is served by `http.FileServer` with `Last-Modified` only. There is no `Cache-Control` and no ETag.
- `cmd/edged/resources_portal_assets.go:31`: uploads up to 8 MB are accepted with no resizing.

**Problem.**
- **Captive-portal mini browsers often keep no cache,** so the logo and background are fetched again on every view.
- **A large photo crosses the guest Wi-Fi each time.** An 8 MB background, sent to 300 arriving guests an hour, is 2.4 GB an hour of airtime.

**Recommended fix.**
1. **Serve assets under names that change with their content,** with `Cache-Control: public, max-age=31536000, immutable`.
2. **At upload,** scale images to at most 1,920 pixels wide and re-encode them, for example WebP at about 300 KB. Show the hotel the result.

**Compliance.** Assets are the hotel's branding, not personal data, so public caching is acceptable.

**Batch:** B2, with an edged change.

### D3-L3: Every TLS handshake checks the certificate files on disk (Low, Confirmed)

**Where:** `cmd/portald/portal_https.go:356-386`. `GetCertificate` calls `os.Stat` twice under a mutex for every handshake. Port 443 from unsigned devices is redirected to portald (`deploy/nftables/stayconnect.nft:102`), so every HTTPS connection their apps make is a handshake that the app then refuses.

**Measured:** a refused handshake costs at most 0.74 ms, so 1,300 or more a second fit on 2 CPUs. The two file checks add to it, under one lock.

**Recommended fix.** Check the files on the existing 15-second TLS poll (`pollTLSState`), not per handshake, and keep the parsed pair in memory.

**Batch:** B2.

### D3-L4: The scd client keeps only two idle connections (Low, Confirmed)

**Where:** `cmd/portald/main.go:92-106`. The Unix-socket transport uses Go's defaults: 2 idle connections per host and no idle timeout.

**Impact.** In a burst, each call beyond two opens and then closes a new connection to scd. That is small per call, but it is churn at exactly the moment scd is busiest.

**Recommended fix.** Set `MaxIdleConnsPerHost` to about 32 and an idle timeout of 90 s.

**Batch:** B2.

### D3-L5: A busy scd is shown as "service unavailable" with no retry (Low, Confirmed; from Phase 2)

**Where:** `cmd/portald/main.go:235, 258`, `client_resume.go:160`. Any scd 5xx or transport error becomes "Service unavailable. Please try again." `429` and `TOO_MANY_ATTEMPTS` have their own message.

**Problem.** Phase 2's fixes (D2-H1's hash limiter, D2-L5's request cap) make scd answer `503` with `Retry-After` when it is briefly full. Shown as "unavailable", the guest thinks the Wi-Fi is broken.

**Recommended fix.**
- Treat `503` with `Retry-After` as "busy".
- Show a short "one moment" notice in the guest's language.
- Retry the request once automatically after the stated delay. Only the steps safe to repeat qualify: status and methods, and sign-in submissions that scd treats as idempotent.

**Compliance.** No retry repeats a step that would count twice against the sign-in throttle. The retry is the same attempt, and scd counts it once.

**Batch:** B3, with Phase 2 B1.

### D3-L6: Per-request work that is the same for every request (Low, Confirmed)

**Where:**
- **The word map** (`guestWords`, `portal_page.go:123`) is copied per request.
- **The translation JSON** is marshalled per request.
- **The hotel sheet's regular expression** runs per request (`hotelSheet`, `portal_page.go:257`).
- **The brand colours** are derived per request (`brandFor`, `portal_page.go:167`).

**Impact.** These make up the 0.75 ms of the uncompressed page. Measured with D3-M1, which makes them run once per language and design instead.

**Recommended fix.** Folded into D3-M1. Compute them when the design version changes.

**Batch:** B1.

## Items from earlier phases and the backend audit

| Item | Outcome |
|---|---|
| Phase 2: how a busy scd is shown | D3-L5. |
| Phase 2: portald's timeouts toward scd | 1.5 s (design), 2–3 s (resume, TLS state) and 5 s overall, under a 30 s page timeout (`timeout.go:14`). These are adequate. The concurrency churn is D3-L4. |
| Backend audit Phase 8 M1: no per-source portal limits | Still open, and now measured. One device can request the landing page about 290 times a second on 2 CPUs, and nothing in portald (`throttle.go` damps sign-in POSTs only) or in nftables stops it. **Not counted again here.** Its fix (a per-device token bucket in portald plus an nftables `meter` on new connections to 8380 and 8343) should be sized from this phase's numbers: for example 5 page requests a second, a burst of 20. |
| Backend audit pattern P1: server timeouts | portald sets only `ReadHeaderTimeout` (`main.go:622, 624`). Still open, not counted again. |

## Handed on

| To | What |
|---|---|
| Phase 4 | The DNAT of 80 and 443 for unsigned devices. Whether 443 should be redirected at all, or answered with a TCP reset, so apps fail fast and only probes reach the portal. This is a networking decision (CONTROLLED). |
| Phase 10 | portald's memory and CPU in the appliance budget at the D3-M1 load. |
| Phase 11 | Count real probe and catch-all traffic from unsigned devices on PRE-LIVE (read-only, request-log counts by route, no guest data) to replace this phase's assumed rate. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D3-M1, D3-M2, D3-L6 | The cached page, the catch-all redirect and the fewer calls on load. **Do first**, together with the backend audit's Phase 8 M1 limit. |
| **B2** | D3-L1, D3-L2, D3-L3, D3-L4 | Small, independent. D3-L2 has an edged half. |
| **B3** | D3-L5 | With Phase 2 B1. |

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D3-M1 | OWASP ASVS V14.4 (security headers); GDPR Art. 25 | The CSP stays as strict, using hashes. The cached body holds nothing specific to a guest or device. The catch-all redirect keeps HTTP-only answers for unsigned devices (no new data collected). |
| D3-M2 | GDPR Art. 5(1)(c) | Asking `/access/status` only for devices with a previous session also stops querying guest records for devices that never signed in. |
| D3-L2 | None (hotel branding is not personal data) | |
| D3-L5 | ASVS V2.2 | An automatic retry is counted once. |
| Request logging | GDPR Art. 5(1)(c); ISO 27001 A.8.15 | `reqlog` logs the route pattern and `redactLoggedQuery` removes query strings. The Phase 11 traffic count uses those route counts only, never addresses or MACs. |

## Tests run

| Test | Result |
|---|---|
| Landing page size, compressed and not | 214,512 bytes; 53,830 bytes compressed |
| Benchmarks on 2 CPUs: landing compressed, landing plain, catch-all compressed, `/generate_204` | 3.41 ms, 0.75 ms, 3.00 ms, 0.012 ms per request |
| Neighbour lookup at 500, 2,000, 5,000 neighbours | 0.37 ms, 1.04 ms, 2.41 ms |
| Refused TLS handshake, ECDSA P-256 | ≤ 0.74 ms |
| Code trace of portald's routes, middleware, scd client, caches and background loop | D3-M2, D3-L3 to D3-L6 |

**Cleanup.**
- **Benchmarks:** they ran in throwaway copies of the module and a separate TLS module in the scratchpad, both deleted. Nothing was written to the repository's code.
- **Docker:** no containers were needed for this phase. Docker is unchanged: no audit containers and 16 volumes.
