# Phase 10: Cross-cutting checks

**Audited:** master `0879e839` (PR #231, merged into `delivery/backend-audit` first; its backend changes are wording only and touch no finding). Code only: nothing was run against PRE-LIVE.

**This phase covers:**
- the items earlier phases handed on;
- the race detector, which had never been run;
- the patterns that recur across phases, and so are best fixed once.

The overall picture, with priorities and every open decision, is in [FINAL_REPORT.md](FINAL_REPORT.md).

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 0 | 2 | 0 |

The two Lows are X1 (a correction that lowers Phase 0 P0-3) and X3 (Phase 6 L14 extended to Central). X2 stays counted in Phase 0.

**What changed since earlier phases:**
- One Phase 0 finding is corrected: the appliance build is pinned after all (X1).
- Two findings are confirmed again on the latest master: the toolchain and library vulnerabilities, and the default database addresses.
- Ten cross-phase patterns are listed so each can be fixed with one shared helper or rule, instead of once per file.

## Items handed to this phase

### X1: Phase 0 P0-3 corrected: the appliance build is pinned (Low, Confirmed)

**Phase 0 said** the appliance build script was missing and the toolchain unpinned. **It exists:** `scripts/build-appliance-binaries.sh`, added on 2026-09-15. Phase 0 searched only `deploy/scripts`. The script:
- builds all six services in `golang:1.25.12` with `-trimpath`, `CGO_ENABLED=0`, linux/amd64 and `-tags stayconnect_production`;
- refuses a dirty tree;
- reads back each binary's embedded commit.

**What is still open:**
- The image is pinned by tag, not by digest.
- Neither `go.mod` has a `toolchain` line, so a local `go build` uses whatever Go is installed.
- The `Makefile` targets still build without the production tag.

**Recommended fix.**
- Pin `GO_IMAGE` by digest in both build scripts.
- Add `toolchain go1.x.y` to both `go.mod` files with P0-2.
- Mark the `Makefile` targets development-only, or make them call the script.

**Severity.** P0-3 is lowered from Medium to Low. Phase 0 now points here.

### X2: The Go toolchain and libraries still carry reachable vulnerabilities (Phase 0 P0-2, unchanged, High)

Re-run on master `0879e839` with Go 1.25.12, `GOOS=linux`, both whole modules:

| Module | Reachable advisories | From |
|---|---|---|
| Appliance (`data-plane`) | 37 | standard library 21 (net/http 10, html/template 3, crypto/tls 2, and others); golang.org/x/net 11; x/crypto 7; x/text 2; chi 2; pgx 1 |
| Central (`control-plane`) | 20 | standard library 16; x/net 6; x/text 2; pgx 1; go-redis 1 |

The module versions are unchanged since Phase 0:
- appliance: x/net v0.51.0, x/crypto v0.49.0, x/text v0.35.0, chi v5.1.0, pgx v5.7.1;
- Central: x/net v0.54.0, x/text v0.37.0, go-redis v9.7.0.

Several standard-library advisories are fixed only in Go 1.26.9 or 1.26.10, so a 1.25 patch release is not enough.

**Recommended fix.** As Phase 0 P0-2: Go 1.26 (latest patch) and the module versions listed there, run on its own branch before any other backend batch. Re-run `govulncheck`, and it must report nothing reachable.

**DECISION** (toolchain change), still open. It is counted once, in Phase 0.

### X3: Five services fall back to a database address with a published password and TLS off (Phase 6 L14, extended; Low)

| Service | Where | Default database |
|---|---|---|
| ctrlapi (Central) | `control-plane/internal/config/config.go:29` | `stayconnect` |
| scd | `data-plane/cmd/scd/main.go:109` | `stayconnect` |
| acctd | `data-plane/cmd/acctd/main.go:39` | `stayconnect` |
| edged | `data-plane/cmd/edged/main.go:46` | `stayconnect_site` |
| netd | `data-plane/cmd/netd/main.go:51` | `stayconnect_site` |

All five use `postgres://stayconnect:stayconnect@127.0.0.1:5432/…?sslmode=disable`.

**What already exists:**
- pmsd shows the right pattern: it requires `PMSD_DB_URL` and has no default (`cmd/pmsd/main.go:41`).
- The install scripts write the variables, and edged warns on the Overview while the published password still works (`db_default_password.go`).

**Problem.** One missing line in an env file silently connects a service as the shared default account. On Central, that service is the internet-facing one.

**Recommended fix.** Remove the defaults and refuse to start without the variable, as pmsd does. Before deploying, check that every PRE-LIVE and Central env file sets it. This is Phase 6 batch B8, extended to ctrlapi.

### X4: Possible hard-coded credentials (gosec G101): none real

All 21 non-test hits were checked by hand: 20 on the appliance and 1 on Central. They are:
- provider sign-in web addresses (`internal/social/*.go`);
- error and result code names (`signinattempt`, `pmsprovider`, `pmsd`, `iamv2`, `scd/central.go`);
- translation tables (`portald/languages*.go`, `portal_page.go`);
- the PMS provider catalogue (`edged/resources_pms_providers.go`);
- a key file name (`portaltls/settings.go:126`).

The tracked files were also searched for private keys and live-looking tokens (Stripe, AWS, GitHub, Slack, Google). The only matches are:
- test fixtures with made-up values;
- the review-evidence filter's own detection patterns;
- Stripe's key-prefix checks.

Two known intentional literals were already reported elsewhere:
- edged's published default password, which it uses only to warn (X3);
- the payment test double's fixed signing secret (Phase 6 L2).

No new finding.

### X5: Race detector (first run)

**Result: no data races.**
- **Coverage:** `go test -race -count=1 ./...` over the unit tests of every package.
  - Appliance: 61 packages with tests.
  - Central: 17.
  - The shared `license` module: 1.
  - All passed, with 0 `DATA RACE` reports.
- **Environment:** a throwaway Linux container with CGO, which the Windows workstation lacks (Phase 0).
- **Not covered:** database integration tests and the kernel suite were not run under `-race`. They need their throwaway database or namespaces, and Phases 3–9 ran them without it.
- **First-run failures:** nine packages failed to build and two edged test groups failed. All were caused by the copy of the source, not by races, and all passed once the copy was complete:
  - the copy lacked the `license` module, which both `go.mod` files reference through `../license`;
  - it lacked the hotel-admin fixtures and documentation that some tests read.
- **Noted for P8:** several packages' tests read files outside their own module (`../license`, `hotel-admin/e2e/fixtures`, `docs/ROLE_AND_SCOPE_MATRIX.md`). A harness must give them the whole repository.

**Recommended.** Add `-race` to P0-1's on-demand check gate, run on Linux.

## Cross-phase patterns

Each row is a defect class found in three or more places. Fixing the class once, with one helper and one test, prevents the next instance. The per-phase findings keep their own batches; the "Fix once" column is the shared piece those batches should use.

| # | Pattern | Found in | Fix once |
|---|---|---|---|
| P1 | **No request-body limits or server timeouts** | Central M9, M10; portald M1 (Phase 5); edged M1 (Phase 9); FIAS frames M1 (Phase 7); portal connections at the firewall M1 (Phase 8) | One `decodeJSON` with `MaxBytesReader` per module, and one `newServer` helper that sets read, write and idle timeouts. A test per module that a 2 MB body gets 413. |
| P2 | **State files written in place, without fsync, or with errors ignored** | licence and assignment files (Phase 2 H1, M15); acctd plan counter (Phase 6 M2); netd plan store and config files (Phase 8 M8, L12); portal assets (Phase 9 L9); the identity and TLS keys being regenerated after a read error (Phase 2 M5, M6) | One `atomicfile.Write` (temp file, fsync, rename, fsync of the directory) in a shared package; `healthdir` and `phase3_journal` already do it right. "Unreadable" is never treated as "absent" for security state. |
| P3 | **Recovery paths that report success after a failure** | registry re-sign (Phase 1 M3); audit after commit (Phase 1 M14); provider reversals (Phase 6 M3); netd rollbacks (Phase 8 M7); restore check (Phase 9 M3); revocations save (Phase 2 M4) | A rule for reviews: every rollback, revert or recovery step checks its error, records a `*.failed` event, and keeps its retry marker. Each fix adds a test with a failing double. |
| P4 | **Values written into generated files or scripts without validation** | nft rule injection from a network name (Phase 8 H1); netplan, Kea and interface names (Phase 8 M9, Phase 9 M5); WAN MAC rebind (Phase 1 M7) | Validate at the boundary, with one shared validator per value kind (interface name, bridge name, hostname, free-text label). Renderers never write free text into executable configuration. |
| P5 | **Sensitive changes with no password step-up, where similar ones have it** | offline request import (Phase 1 M16); email, SMS and social providers (Phase 9 M2); system-network confirm and the device self-service switch (Phase 9 L6, L16) | One step-up helper per service (Phase 9 CC2), and a test that walks the router and lists every write route without step-up, for review. |
| P6 | **Operational limits hard-coded, against rule §0C** | Central rate limits and windows (Phase 1 L21); guest code limits (Phase 2 L23); voucher and throttle defaults (Phase 4 M2); portal connection limits and guest-reachable ranges (Phase 8 M1, M2) | Each becomes a persisted setting through the existing settings system when its batch is done, with a default, unit, validation and audit. |
| P7 | **Raw database or transport error text returned to callers** | edged (Phase 9 L1); transfer (Phase 6 L11); pmsd applier logs (Phase 7 L12) | One error helper that logs the detail with the request id and returns a fixed message. Central already does this (`publicError`), which is the model. |
| P8 | **Tests that exist but are not run, or cannot be re-run** | OTP and key-bootstrap tests stale (Phase 3 M5); kernel gate uses a hand-written ruleset (Phase 8 CC2); edged suites leave throttle and seed rows (Phase 9 CC4); post-stay and transfer need the `phase5` tag (Phase 6 CC4) | A single documented test harness: one throwaway full-schema database per suite, the tags each suite needs, and the kernel suite loading the real rendered ruleset. P0-1's check gate runs it on demand. |
| P9 | **Default credentials and TLS off by default** | X3; Phase 6 L14 | Required variables, no defaults. |
| P10 | **Dead code** | staticcheck U1000 in every phase (Central 6, appliance 48) | Delete it in each phase's clean-code batch. P0-1's gate keeps it at zero. |

## Tests run

- **Race detector** (`go test -race ./...`, unit tests only, both modules, Go 1.25.12 with CGO, in a throwaway Linux container): see X5.
- **govulncheck** on both whole modules: see X2.
- **gosec G101** on both whole modules, and a search of the tracked files for private keys and tokens: see X4.
- No database or kernel tests were needed for this phase. Phases 3–9 ran those.
