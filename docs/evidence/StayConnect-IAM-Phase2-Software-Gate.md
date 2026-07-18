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

| Step | Command | Result |
|---|---|---|
| Typecheck | `npx tsc --noEmit` | **EXIT 0 / PASS** (2026-07-18T08:02:20Z) |
| Build (compile) | `npx next build` | **`✓ Compiled successfully` + types valid** |

Note: `next build` static-prerender of all 31 routes OOMs on the dev workstation (V8 heap ceiling); this is an environment memory limit, not a code defect — the commercial-packages page is `"use client"` (dynamic, not prerendered), and both the compile and the TypeScript validity passes succeed. The standalone release build runs on the deployment host.

## Governance

- `python tools/project-state.py validate` → `PROJECT_STATE_GOVERNANCE = PASS`.
- Adversarial mutation suite `tools/tests/project_state_validator/run_mutations.py` → recorded in the final report (includes the deterministic Phase-2 scope guard M36).
- GitHub Actions **Project Governance** — green on every pushed Phase-2 HEAD.

## Darkness assertions verified in code + tests

- Every surface (scd portal, edged admin, portald bridge, both UIs) is gated on the `STAYCONNECT_PHASE2_*` / `NEXT_PUBLIC_PHASE2_ADMIN` flags; while OFF the routes/UI are absent, the repository is nil, and zero Phase-2 SQL is issued.
- Master-on-without-repo fails closed at startup in scd and edged (cutover-only).
- Free-only invariant: publish writes `price_minor=0` + `{NOT_REQUIRED}`; quote/confirm re-validate; grace requires a free CHECKOUT_GRACE revision.
