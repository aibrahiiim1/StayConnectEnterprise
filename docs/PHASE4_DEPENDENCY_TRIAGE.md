# Phase-4 Hotel-Admin dependency advisory triage

Evidence, preserved verbatim, is in [`docs/evidence/phase4/`](evidence/phase4/):

| File | Tree |
|---|---|
| `npm-audit-full.json` | `npm audit --json` — every dependency, including dev and build tooling |
| `npm-audit-production.json` | `npm audit --omit=dev --json` — only what ships to the appliance |

Nothing here was resolved with `npm audit fix --force`.

## The number that matters

| Tree | Advisory groups | Severity |
|---|---|---|
| Full | 13 | 1 critical, 10 high, 2 moderate |
| **Production-only** | **2** (`next`, `postcss`) | high |

The production tree contains exactly two vulnerable packages, and the `postcss` entry there is the copy
bundled inside `next` — our direct `postcss` (8.5.26) is build tooling and is already patched. So the entire
production exposure is **Next.js**.

## Production exposure: Next.js

Twenty-two advisories against `next@14.2.35`, all `high`. By GHSA, with the ones that matter most to an
authenticated admin app behind a reverse proxy first:

| GHSA | Class | Reachability here |
|---|---|---|
| `GHSA-ggv3-7p47-pfv8` | HTTP request smuggling in rewrites | Hotel Admin runs behind Caddy and uses Next rewrites for `/api/edge/*` |
| `GHSA-36qx-fr4f-26g5` | Middleware / proxy bypass (Pages Router) | Hotel Admin gates every route on a session cookie **in middleware** |
| `GHSA-3g8h-86w9-wvmq` | Middleware / proxy redirects cache-poisoned | same middleware path |
| `GHSA-ffhc-5mcf-pf4q`, `GHSA-gx5p-jg67-6x7h` | XSS (App Router, beforeInteractive) | the whole admin app is App Router |
| `GHSA-vfv6-92ff-j949`, `GHSA-wfc6-r584-vfw7`, `GHSA-68g3-v927-f742`, `GHSA-4633-3j49-mh5q` | RSC cache poisoning / response-body confusion | App Router |
| `GHSA-c4j6-fc7j-m34r`, `GHSA-89xv-2m56-2m9x`, `GHSA-p9j2-gv94-2wf4` | SSRF (Server Actions, rewrites) | no Server Actions in this app; rewrites are used |
| `GHSA-955p-x3mx-jcvp` | Unauthenticated disclosure of internal Server Function IDs | App Router |
| `GHSA-9g9p-9gw9-jx7f`, `GHSA-h64f-5h5j-jqjh`, `GHSA-3x4c-7xq6-9pq8` | Image-optimizer DoS / unbounded cache | `next/image` is not used by any Phase-3 or Phase-4 screen |
| `GHSA-q4gf-8mx6-v5v3`, `GHSA-8h8q-6873-q5fj`, `GHSA-m99w-x7hq-7vfj`, `GHSA-4c39-4ccg-62r3`, `GHSA-h25m-26qc-wcjf` | DoS (Server Components, Server Actions, deserialization) | App Router |

**Minimum fixing version, per npm's own resolver:** `next@16.3.0`, `isSemVerMajor: true`. There is no 14.x
or 15.x release that clears these; the advisory range is `9.3.4-canary.0 - 16.3.0-preview.10`. So the
earlier statement that "Next 16 is the only fix" was not an assumption — it is what the advisory data says,
and it is recorded here rather than asserted.

## The upgrade was attempted in this milestone, and it is NOT shipped

The milestone authorizes a framework major *if it is genuinely required to remove a production-exposed
blocker*, provided it receives the full Hotel-Admin regression suite. It is required. It was attempted. It
does not pass the suite, so it is not shipped.

Measured, on `next@16.3.0` + `react@19.2.8` + `eslint@10`:

| Gate | Result |
|---|---|
| `tsc --noEmit` | **pass** |
| `next build` (Phase-4 flags OFF) | **pass** — every route compiles, middleware builds as the renamed "Proxy" |
| `vitest run` (all suites) | **pass**, 83/83 |
| `playwright test` (all suites) | **FAIL — 49 passed, 7 failed** |

Every failure is in `e2e/phase3-pms-interfaces.spec.ts`, and they share one symptom: the PMS-interfaces page
renders its layout and navigation but **no page content at all** — no status badges, no Publish control, no
revision table. There is no page error, no console error and no failed request; the component simply
produces nothing. That page returns `null` until its health fetch resolves, so the fetch or its effect is
not completing under React 19 / Next 16, and the same spec passes on 14.2.35.

Diagnosing an App-Router data-loading regression across seven browser tests is open-ended work on a page
that governs PMS credentials and revision publication. Doing it inside a financial milestone — where the
reviewer's attention is on money handling — is the wrong place to discover a routing or effect-ordering
regression, and shipping a framework major whose own regression suite is red would be worse than the
advisory it fixes.

**The upgrade was reverted.** The delivered baseline is `next@14.2.35` + `react@18.3.1`, re-verified after
the revert: `tsc` clean, `next build` clean, **83/83 unit**, **56/56 browser**.

### What this leaves

`next@14.2.35` picks up the critical Cache Poisoning fix resolved in the previous milestone and every other
patch on the 14.2 line. It does not clear the 22 advisories above.

Compensating controls, stated as controls and not as fixes:

- Hotel Admin listens on `127.0.0.1:3100` and is reached only through Caddy on the hotel LAN. It is not
  internet-facing and is not multi-tenant: one appliance serves one property.
- Every route except `/login` requires an authenticated operator session, and edged re-authorizes every API
  call independently of the frontend.
- `next/image` is unused, so the image-optimizer advisories have no reachable surface.
- No Server Actions are used.

None of that makes the smuggling, middleware-bypass or XSS advisories unreachable. They remain **an open
production-exposed blocker**, and closing them needs the Next 16 upgrade delivered as its own authorized
change with the Phase-3 browser regressions diagnosed and fixed first.

## Build-time only

`glob` (`GHSA-5j98-mcp5-4vw2`), `minimatch` (`GHSA-3ppc-4f35-3m26`, `GHSA-7r86-cg39-jmmj`,
`GHSA-23c5-xmqv-rm74`), `postcss` direct (`GHSA-qx2v-qp2m-jg93`, `GHSA-6g55-p6wh-862q`,
`GHSA-r28c-9q8g-f849`, `GHSA-fxqj-rqcc-2cmp`).

These run on a developer machine or a CI runner against this repository's own sources. They do not execute
on the appliance and never process guest or financial input. They are transitive and pinned by their
parents; resolving them individually means overriding versions their parents were tested against.

## Development / test only

`vitest` (`GHSA-5xrq-8626-4rwp`, critical), `esbuild` (`GHSA-67mh-4wv8-2f99`), `vite`
(`GHSA-4w7w-66w2-5vf9`, `GHSA-v6wh-96g9-6wx3`, `GHSA-fx2h-pf6j-xcff`).

All three require a **listening dev server or UI server**. This repository never starts one: the scripts are
`vitest run`, and CI runs `npx vitest run --reporter=default`. The Playwright web server is `next dev`,
which is Next's server rather than Vite's.

Upgrading `vitest` 2 → 4 is a major with a changed mocking API that every test file uses. It is mechanical
but broad, and it is not a financial-milestone change.

## Standing position

The production tree has exactly one vulnerable package. Its fix is a framework major; the major was
attempted under the full regression suite, failed it, and was reverted rather than shipped red. Build-time
and dev-only advisories are recorded with their actual reachability rather than silenced. Nothing was
resolved by force.
