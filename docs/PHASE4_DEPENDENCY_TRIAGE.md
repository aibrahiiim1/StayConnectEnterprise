# Phase-4 Hotel-Admin dependency advisory triage

`npm audit` reported 17 advisories against `hotel-admin/` during the Phase-4 CI run. This is the triage,
separated by what is actually exposed. Nothing here was resolved with `npm audit fix --force`: that command
resolves advisories by taking whatever major version satisfies them, which on this dependency tree would
have moved Next across two majors as a side effect of a lint-plugin advisory.

Re-audit after the changes below: **17 → 16**, and the one **critical** production advisory is resolved.

## What was upgraded

| Package | From | To | Why |
|---|---|---|---|
| `next` | 14.2.5 | 14.2.35 | Latest patch on the current line. Resolves the **critical** Next.js Cache Poisoning advisory. |
| `postcss` | 8.4.41 | 8.5.26 | Build-time CSS pipeline. Resolves the direct XSS-via-unescaped-`</style>` and `sourceMappingURL` file-read advisories. |

`npx tsc --noEmit`, the full vitest suite and `next build` all pass afterwards, so these are upgrades rather
than assertions that they are safe.

## What is left, and why

### Production / runtime exposure

**`next` (high, remaining).** The advisory range is `0.9.9 - 16.3.0-preview.10`: the fix is in Next **16**,
two majors ahead of this application. The remaining item is a denial-of-service condition in the image
optimizer.

Assessment of the actual exposure on this appliance:

- Hotel Admin is served on `127.0.0.1:3100` behind Caddy and requires an authenticated operator session for
  every route except `/login`. It is not internet-facing.
- The image optimizer is not used by any Phase-4 screen. The financial surfaces render text, tables and
  badges; there is no `next/image` usage in them.

It is nonetheless a real advisory and it is NOT fixed. Upgrading Next 14 → 16 changes the App Router, the
build pipeline and the middleware contract across the whole admin application and the guest portal. That is
a project of its own with its own regression surface, and doing it inside a financial milestone -- where the
reviewer's attention is on money handling -- would be the wrong place to discover a routing regression.

**Recorded as an explicit remaining Phase-4 blocker.** It needs its own authorized change with its own
browser-level regression pass.

### Build-time only

`glob`, `minimatch`, `brace-expansion`, `js-yaml`, `undici`, `nanoid` reach the tree through the Next build
and the ESLint toolchain. They run on a developer machine or a CI runner against this repository's own
sources; they do not execute on the appliance and they do not process guest or financial input. They are
transitive and pinned by their parents, so resolving them individually means overriding versions their
parents were tested against.

### Development / test only

`vitest` (critical), `@vitest/mocker`, `vite`, `vite-node`, `esbuild`, `eslint-config-next`,
`@next/eslint-plugin-next`, `@typescript-eslint/*`.

The `vitest` advisory is "when the Vitest UI server is listening, an arbitrary file can be read and
executed". This repository never runs the Vitest UI: the scripts are `vitest run`, and CI runs
`npx vitest run --reporter=default`. The `esbuild`/`vite` dev-server advisories are the same shape -- they
require a dev server to be listening, which no CI step and no appliance ever starts.

Upgrading `vitest` 2 → 4 is a major with a changed mocking API, which every existing test file uses. That is
a mechanical but broad change and it is not a financial-milestone change.

## The standing position

Production-exposed advisories are fixed in this milestone. Build-time and dev-only advisories are recorded
here with their actual reachability rather than being silenced, and the two that need a major upgrade --
Next 16 and Vitest 4 -- are named as separate authorized work rather than being done as a side effect.
