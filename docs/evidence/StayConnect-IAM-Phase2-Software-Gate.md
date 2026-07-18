# StayConnect IAM Phase 2 — Software-Gate Evidence

**Immutable evidence record. DARK Phase 2 (all `STAYCONNECT_PHASE2_*` flags OFF). Disposable infra only.**

- Branch: `phase/2-commercial-packages` (PR #4)
- Source HEAD at gate run: `a0578e482015bd916d8f4c24f4deffd5fbb4daa2`
- Test database: disposable `iamv2_p2` (loopback `127.0.0.1:15432` via SSH tunnel to the disposable `p2_test_pg` container on the appliance host); **no committed credentials, no production/guest data**.
- All timestamps UTC.

## Go workspace (`data-plane/`)

| Step | Command | Result |
|---|---|---|
| Build | `go build ./...` | **EXIT 0** (2026-07-18T08:01:37Z) |
| Vet | `go vet ./...` | **EXIT 0** (2026-07-18T08:01:41Z) |
| Format | `gofmt -l` (all Phase-2 files) | **EXIT 0 / no diffs** (2026-07-18T08:01:43Z) |
| Tests | `PHASE2_TEST_DSN=… go test ./...` | **EXIT 0** — all packages `ok`/cached (2026-07-18T08:01:50Z) |

Phase-2-relevant test packages (all green):
- `internal/iamv2` — config fail-closed; typed eligibility + publication validation; grant-snapshot (floats/negative/unknown/AGGREGATE-disabled); ISO-4217 currency (exp 0/2/3, ZZZ, USD/0); duration policy; **C2** quote/free-purchase + expiry + 24-way single-winner + rollback-at-every-mutation-boundary + tampered-quote; **C3** subject uniqueness/supersession/cross-subject/concurrency (account/voucher/principal); **C4/C5** immutability + pin-trigger + revision lifecycle; **C6** per-pin substitution rejected by PostgreSQL; **C7** offer-quote immutability; **C8** duration-window stamping; **guest listing** filters + read-only + auth-pin; **admin** plans/grace-validation/inspection/publish-rejection; dark no-repo.
- `cmd/scd` — builds; commerce routes gated + missing-pin guards.
- `cmd/portald` — commerce bridge: dark routes unmounted + zero scd contact; no-session → unavailable; **browser cannot substitute** auth-context/device/network/tenant/site (quote + confirm); packages GET uses session pins; success-page commerce panel gated by flag.

## Hotel Admin UI (`hotel-admin/`)

### Automated UI tests (added in the final acceptance gate)

| Suite | Tool | Command | Result |
|---|---|---|---|
| Component/unit | Vitest + React Testing Library (jsdom) | `npm test` | **36 tests / 4 files PASS** |
| Hotel Admin E2E | Playwright (system Chrome via `channel`) | `npm run e2e -- e2e/hotel-admin.spec.ts` | **3 tests PASS** |
| Guest Portal E2E | Playwright (renders the real portald success template) | `npm run e2e -- e2e/guest-portal.spec.ts` | **6 tests PASS** |

Component/unit coverage: nav gating by `NEXT_PUBLIC_PHASE2_ADMIN` + role; 503 → approved disabled state; plan-revision selector (no raw UUID); revision-history current/immutable; typed eligibility editor lists only the five supported non-PMS rule types (PMS types absent); ordered grant-tier serialization; duration validation (manual/window/fixed; PMS/checkout modes not representable); sale-window validation; free-only payload (no price/settlement/pms/tax/currency keys, no such inputs); grace validation error; deactivation reason+password step-up (aborts on cancel); PII-free inspection; failed publish shows an error without a false success.

E2E coverage: real Chrome drives the real Next app (Hotel Admin, `next dev` flag-ON, edged mocked at the network layer) and the real portald success-page template (Guest Portal). Guest flow proves the browser submits ONLY opaque `package_id`/`quote_id` (no tenant/site/auth-context/device/guest-network), double-submit yields exactly one confirm, and expired/failed quotes show generic unavailable with no active state.

### Type-check + production build

| Step | Command | Host | Result |
|---|---|---|---|
| Typecheck | `npx tsc --noEmit` | dev workstation | **EXIT 0 / PASS** |
| **Authoritative production build** | `NODE_OPTIONS=--max-old-space-size=12288 npm run build` | CHV-MISMGR (Windows, 31.7 GB) | **EXIT 0** — `✓ Compiled successfully`, **`✓ Generating static pages (31/31)`**, standalone bundle produced. START `2026-07-18T11:52:54Z` → END `2026-07-18T11:53:39Z`. |

Standalone bundle: `.next/standalone/server.js` present; deploy tarball SHA-256 `678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95`. `/commercial-packages` builds as a static (`○`) route (8.35 kB); with `NEXT_PUBLIC_PHASE2_ADMIN` unset the nav item is hidden (dark).

**Environment observation (not a code defect), kept for the record:** the production build OOMs at the whole-app static-prerender step when the workstation has < ~7 GB free (V8 heap ceiling under memory pressure). It is NOT a build failure of the code — with adequate free memory + a 12 GB Node heap the build completes all 31 routes (the authoritative result above). This is distinct from, and supersedes, the earlier partial run that stopped at prerender under memory pressure. The Final Report reflects the successful authoritative build.

## Governance

- `python tools/project-state.py validate` → `PROJECT_STATE_GOVERNANCE = PASS`.
- Adversarial mutation suite `tools/tests/project_state_validator/run_mutations.py` → recorded in the final report (includes the deterministic Phase-2 scope guard M36).
- GitHub Actions **Project Governance** — green on every pushed Phase-2 HEAD.

## Darkness assertions verified in code + tests

- Every surface (scd portal, edged admin, portald bridge, both UIs) is gated on the `STAYCONNECT_PHASE2_*` / `NEXT_PUBLIC_PHASE2_ADMIN` flags; while OFF the routes/UI are absent, the repository is nil, and zero Phase-2 SQL is issued.
- Master-on-without-repo fails closed at startup in scd and edged (cutover-only).
- Free-only invariant: publish writes `price_minor=0` + `{NOT_REQUIRED}`; quote/confirm re-validate; grace requires a free CHECKOUT_GRACE revision.
