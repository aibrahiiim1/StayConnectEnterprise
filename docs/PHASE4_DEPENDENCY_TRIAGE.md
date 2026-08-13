# Phase-4 Hotel-Admin dependency advisory triage

**Status as of 2026-08-13: both dependency trees report ZERO advisories.** Nothing is accepted, nothing is
outstanding, and `governance/dependency-acceptances.json` is empty because there is nothing to accept.

Evidence, preserved verbatim, is in [`docs/evidence/phase4/`](evidence/phase4/):

| File | Tree | Result |
|---|---|---|
| `npm-audit-production.json` | `npm audit --omit=dev --json` — only what ships to the appliance | **0** advisories |
| `npm-audit-full.json` | `npm audit --json` — every dependency, including dev and build tooling | **0** advisories |

Nothing here was resolved with `npm audit fix --force`.

---

## What the previous version of this document got wrong

It stated: *"Minimum fixing version, per npm's own resolver: `next@16.3.0`, `isSemVerMajor: true`. There is
no 14.x or 15.x release that clears these; the advisory range is `9.3.4-canary.0 - 16.3.0-preview.10`."*

That was read off npm's **aggregated** `range` and its `fixAvailable`, which reports the latest release
rather than the earliest sufficient one. The **per-advisory** ranges in the same JSON say something
different. The 22 advisories against `next@14.2.35` had these upper bounds:

| Upper bound | Advisories |
|---|---|
| `<15.0.8` | 1 |
| `<15.5.10`, `<15.5.13`, `<15.5.14`, `<15.5.15` | 4 |
| `<15.5.16` | 9 |
| **`<15.5.21`** | 8 |

The highest bound is `15.5.21`. **Every listed advisory is cleared by a 15.5.x release** — no framework
major was ever required. The Next 16 attempt in the previous milestone was therefore a larger step than the
advisories asked for, and it was that larger step — not the security fix — that produced the browser
regression.

The lesson is narrow and worth keeping: `fixAvailable` answers *"what is the newest version?"*, not
*"what is the minimum patched version?"*. The second question is answered by reading the ranges.

---

## What was delivered

| Package | Before | After | Why this version |
|---|---|---|---|
| `next` | `14.2.35` | **`15.5.23`** | 15.5.21 is the minimum that clears every advisory; `.23` is the current patch on that line. Its peer range is `react: ^18.2.0 \|\| ^19.0.0`, so **React stays at 18.3.1** — no React 19 migration. |
| `postcss` (production copy) | `8.4.31`, pinned inside `next` | **`8.5.26`** via `overrides` | `next` declares `postcss: 8.4.31` as a hard dependency, so the production tree carried the vulnerable copy no matter how new the direct devDependency was. Advisories ran to `<=8.5.22`. |
| `sharp` | `0.34.5`, optional dep of `next` | **`0.35.3`** via `overrides` | The libvips CVEs (`CVE-2026-33327/33328/35590/35591`) are fixed in `>=0.35.0`. Verified to load and encode: libvips **8.18.3**. |
| `vitest` | `2.1.9` | **`3.2.7`** | Clears the `vitest` UI-server RCE (critical) and the whole `vite`/`vite-node`/`esbuild`/`@vitest/mocker` chain beneath it. Dev-only, but it costs one line and removes five advisory groups. |
| `eslint-config-next` | `14.2.5` | `15.5.23` | Kept aligned with `next`. Not exercised: this repository has no ESLint configuration file and CI does not run lint. |

`react` and `react-dom` are **unchanged at 18.3.1**. This is the point: the security remediation did not
require React 19, and the previously reported "React 19 regression" was never a React problem at all.

---

## The PMS-Interfaces regression: what it actually was

The previous milestone recorded seven failures in `e2e/phase3-pms-interfaces.spec.ts` under Next 16 and
described them as *"the PMS-interfaces page renders its layout and navigation but no page content at all …
the fetch or its effect is not completing under React 19 / Next 16."*

That diagnosis was wrong, and it was wrong in a way that mattered: it attributed a test-harness problem to
the application and used it to justify keeping a vulnerable production tree.

The same seven tests fail on **Next 15.5.23 with React 18.3.1**. Reproducing them with a page snapshot shows
the real cause immediately:

```yaml
- main:
    - heading "PMS interfaces" [level=1]
    - table:
        - row:
          - cell "Main PMS"
          - cell "protel-fias"
          ...
          - cell: [ button "Open" ]           # <- the page rendered perfectly
- generic [active]:
    - menu "Next.js Dev Tools Items"          # <- and this was on top of it
```

The page had rendered its data all along. **Next 15.5 introduces a floating Dev Tools panel in `next dev`**,
bottom-left, above the page. Playwright's click on the `Open` button landed on the overlay, so the detail
section never opened and every assertion after it timed out. The earlier "no console error, no failed
request, the component simply produces nothing" observation was consistent with this the whole time — the
component produced everything; the click never reached it.

**Fix:** `devIndicators: false` in `next.config.mjs`. It applies to `next dev` only, so the deployed
appliance bundle is byte-identical either way, and the browser suite goes back to measuring the application
rather than the development tooling.

---

## The full regression surface, re-run on the delivered tree

`next@15.5.23` · `react@18.3.1` · `postcss@8.5.26` · `sharp@0.35.3` · `vitest@3.2.7`

| Surface | Result |
|---|---|
| `tsc --noEmit` | **pass** |
| `next build` (Phase-4 flags OFF) | **pass** — every route compiles, middleware builds at 34 kB |
| standalone bundle (`node .next/standalone/server.js`) | **pass** — boots, serves `/login` 200, middleware issues `307 → /login?next=%2Fdashboard` |
| `vitest run`, all suites | **pass — 92/92** |
| `playwright test`, all suites | **pass — 64/64** |
| ├ `phase3-pms-interfaces.spec.ts` | **pass — 10/10** (was 3/10) |
| ├ `phase3-guest-portal`, `phase3-guest-portal-resilience`, `phase3-stays-grace` | pass |
| ├ `phase4-financial-operator.spec.ts` | pass, including the 3 new zero-attempt tests |
| ├ `hotel-admin.spec.ts`, `guest-portal.spec.ts` | pass |
| └ `auth-middleware.spec.ts` *(new)* | **pass — 5/5** |
| `npm audit --omit=dev` | **0 advisories** |
| `npm audit` | **0 advisories** |

### New: browser coverage for the authentication gate

`e2e/auth-middleware.spec.ts` did not exist before. The middleware is the single function that gates every
operator page on this appliance, it is the part of Next most likely to change across a major, and
`middleware.ts` itself records a previous 500 caused by exactly that. It had no browser test. It now
proves, in a real browser:

- an operator with no session is redirected to `/login?next=<path>` from every app route, and the login page
  actually renders rather than the redirect landing on an error;
- nothing from a gated page reaches the browser for an unauthenticated request;
- an operator who already has a session is redirected off `/login` to `/dashboard`;
- `/icon.svg` is exempt, so the login page can render itself;
- the redirect carries an **absolute** `Location`, which is what Next's middleware normalisation requires.

### One operational note: where the lockfile is generated

`package-lock.json` is produced by a real `npm install` on **Linux** (`node:20-bookworm`), not on the
Windows development host. `sharp` and the SWC binaries are optional platform packages, and npm's ideal-tree
differs per platform: a Windows-generated lock omits the hoisted `@emnapi/core` and `@emnapi/runtime` that a
Linux `npm ci` computes, and CI fails `EUSAGE — package.json and package-lock.json are not in sync`. The
Linux-generated lock is a superset — it carries every platform's binaries — and `npm ci` reproduces it
byte-for-byte on both Linux and Windows, with `sharp` loading on each. Measured both ways.

---

## The gate that reports on all this

`scripts/ci/phase4-dependency-gate.sh` was rewritten in this milestone. The previous version carried
`ACCEPTED="next postcss"` **inside the script itself**, written by the same agent that was delivering the
code — so it reported PASS on a production tree with high-severity advisories in it. A gate that can grant
itself an exception measures nothing.

The rewritten gate distinguishes two things the old one conflated:

| | Meaning | Gate verdict |
|---|---|---|
| **TRIAGED** | investigated and written up in this document | **FAIL** — understanding a risk is not accepting it |
| **PO-ACCEPTED** | recorded in `governance/dependency-acceptances.json` with the exact GHSA ids, `decided_by`, `decision_ref`, `decided_on` and `expires_on` | PASS, until it expires |

Acceptances name **advisories**, not packages, so an exception cannot silently absorb the next unrelated
advisory published against the same package. An expired acceptance is treated as no acceptance. An
acceptance for a risk that is no longer reported fails as stale, because a dead entry is how a live one gets
lost in the noise.

`scripts/ci/phase4-dependency-gate-selftest.sh` runs the **same** judgement file CI runs against synthetic
audits and proves it refuses what it claims to refuse: a triaged-but-unaccepted advisory, an acceptance with
no named decider, an expired acceptance, an acceptance of a *different* advisory in the same package, and a
stale acceptance — while still passing a complete, current Product-Owner acceptance and a clean tree. It
runs **before** the gate in CI, because a gate observed only passing on a clean tree has not been shown to
do anything.

---

## Standing position

There is no outstanding production dependency risk and nothing has been accepted by anybody. If a new
advisory appears, the gate fails and the decision goes to the Product Owner — it cannot be closed by this
document, by the gate, or by whoever is delivering the change.
