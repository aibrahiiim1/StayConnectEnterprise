# Backend audit — Phase 0: baseline

Audited commit: `008a4cb3` (master, 2026-10-24). Read-only: no source code was changed.

PR #229 (`9ffa981b`) merged after this scan. It changes appliance code, so each later phase re-runs `govulncheck`, `staticcheck` and `gosec` on its own packages, at the latest master, before reviewing them.

Each finding has a recommended fix. The fixes are grouped into batch B0 at the end of this file. No fix is applied by the audit.

## Severity scale

| Severity | Meaning |
|---|---|
| Critical | Exploitable now by an outside party, or loses or corrupts data with no operator mistake needed. |
| High | Breaks a trust boundary, or causes a fleet-wide outage, under a realistic condition (a misconfiguration, a lost file, an operator error the system invites). |
| Medium | A real defect with a bounded impact: one appliance, one account, a narrow race, or a gap that needs another fault to matter. |
| Low | Robustness, error handling or observability problems with little direct impact. |
| Clean code | Readability, duplication, dead code, standards. |

Every finding has a location, a failure scenario and a status:
- **Confirmed**: the code path was traced by the auditor.
- **Possible**: the code suggests the problem, but it needs a test or deployment facts to prove.

Findings from the independent second reviewer are only listed as Confirmed after the auditor re-checked them.

## Checklist applied in every phase

1. **Bugs:** logic, error handling, nil and panic paths, races, transaction boundaries, retries and idempotency, resource leaks.
2. **Security:**
   - Authentication and permission on every endpoint, plus customer and site isolation.
   - Input validation and size limits; injection (SQL, shell, outbound requests).
   - CSRF and same-origin checks; secrets and guest data in logs, errors and responses.
   - Rate limits; certificate and key handling; failing closed.
3. **Data integrity:** constraints, atomic multi-step changes, audit trail.
4. **Operations:** timeouts, startup validation, health checks, shutdown.
5. **Project rules:**
   - Operational values are settings (CLAUDE.md §0C).
   - Central only serves licensing (§0E).
   - No guest data in evidence.
6. **Clean code:** names, function size, duplication, dead code, idiomatic Go, tests on the real boundary.

## Tooling

No lint, static-analysis or vulnerability scanner is configured in the repository: no `.golangci` file, no `staticcheck`, `govulncheck` or `gosec` in CI or tools.

**Finding P0-1 (Medium, Confirmed):** there is no automated security or static analysis gate. Every finding below was reachable by a free, standard tool.

Tools used for this baseline, installed on the audit workstation only:
- `go vet`
- `staticcheck` (latest)
- `govulncheck` (latest), run with the production toolchain Go 1.25.12
- `gosec` v2 (latest)

## Results

### go vet

Clean on both modules.

### govulncheck

Central is built with `golang:1.25.12` (`deploy/scripts/central-build.sh:34`). Both `go.mod` files declare `go 1.25.0`. The scan was run with Go 1.25.12, so it reflects what ships.

**Finding P0-2 (High, Confirmed): the Go toolchain is out of support, and the shipped binaries carry known, reachable vulnerabilities.**
- **Unpatched standard library in the Go 1.25 line.** Several standard-library advisories (net/http, net/http/internal/http2, crypto/tls, crypto/x509, html/template, net/textproto) have fixes only in Go 1.26.9 or later. No 1.25.x release fixes them. Examples: GO-2026-6603, -6605, -6607, -6610 to -6613, -6617, -6809.
- **Fixed in a later 1.25 release.** Others are fixed in 1.25.13: GO-2026-5026, -5972, -6088, -6089, -6090, -6091, -6218.
- **Who is exposed.** ctrlapi's internet-facing listeners (443 behind Caddy, and 9443 directly) and every appliance HTTP service are built on these packages.

| Module | Central | Appliance | Fixed in |
|---|---|---|---|
| Go standard library | 13 advisories | 16 advisories | 1.26.9 / 1.26.10 (some 1.25.13) |
| golang.org/x/net | v0.54.0, 6 advisories | v0.51.0, 11 advisories | v0.60.0 |
| golang.org/x/text | v0.37.0, 2 | v0.35.0, 2 | v0.41.0 |
| golang.org/x/crypto | not affected | v0.49.0, 7 | v0.56.0 |
| github.com/jackc/pgx/v5 | v5.7.1, GO-2026-5004 (SQL injection via placeholder confusion with dollar-quoted strings) | same | v5.9.2 |
| github.com/go-chi/chi/v5 | not affected | v5.1.0, GO-2026-5775 / -5777 | v5.3.0 |
| github.com/redis/go-redis/v9 | v9.7.0, GO-2025-3540 | not affected | v9.7.3 |

govulncheck only lists advisories whose vulnerable function is reachable from this code. The full reports are kept on the audit workstation, not in the repository.

**Gap:** the build script for the appliance binaries (edged, scd, portald, netd, acctd, pmsd) was not found in `deploy/scripts`, so the toolchain that builds them is not pinned in the repository. **Finding P0-3 (Medium, Confirmed).**

### staticcheck

- **Central:** 6 items. Dead code: `api.(*Base).issueAssignment` and `cmd/pkitest rawGet`; two unused values in `cmd/acceptance`; one error-string style issue.
- **Appliance:** 48 items:
  - 44 unused functions or fields, mostly `cmd/netd/phase3_shaping.go`, `cmd/netd/sysnet.go`, `cmd/edged`, `cmd/scd`, `internal/iamv2`.
  - One use of a deprecated `applianceauth.Sign` in a test helper.
  - Three style items.

These are assigned to the phase that owns each package.

### gosec

| | Central | Appliance |
|---|---|---|
| Non-test issues | 53 | 344 |

These are leads, not findings: gosec is noisy (for example, every `exec.Command` and every `os.ReadFile` of a configured path is flagged). Each one is triaged inside the phase that owns the file.

The appliance categories worth particular attention in later phases:

| Phase | gosec rules to triage |
|---|---|
| Phase 8 (netd) | G204, G702: subprocesses with variable arguments (48) |
| Phases 5 and 9 (portald, edged) | G703: path traversal by taint (19); G705/G203: unescaped HTML output (17); G120: unbounded form parsing (1) |
| Phases 5, 6, 7 | G704: SSRF by taint (6) |
| Owning phase | G402: TLS `InsecureSkipVerify` (2) |
| Phase 10 | G101: possible hard-coded credentials (19, mostly test fixtures and constant names) |

### Tests

- **Central (`go test ./...`):** all packages pass.
- **Not run: 33 tests that need a migrated Postgres.** `CTRLAPI_TEST_DB_URL` was not set. They are scheduled for Phase 3 (database), which needs a disposable database container.
- **Not run: race detector.** The workstation has no C compiler, which `-race` needs on Windows. It needs a Linux build host or a C toolchain.

## Recommended fixes

### P0-1 — Add an automated security and static-analysis gate

1. Add `tools/go-checks.sh`. For each module it runs:
   - `go vet ./...`
   - `staticcheck ./...`
   - `govulncheck ./...`

   The tool versions are pinned with `go run <tool>@<version>`, so nothing is installed globally.
2. Run it in `tools/preflight.sh`, so it is part of FULL CHECK, and as a manual GitHub workflow next to the existing gates. Do not make it a required merge check; under D42 the only required check is `po-merge-authorization`.
3. Keep `gosec` out of the gate until each phase's triage is done. It is too noisy to block on today. Add it later with a reviewed exclusion list.

**Test:** the script exits non-zero on a planted `go vet` error and on a known-vulnerable module version.

### P0-2 — Move to a supported Go release and update the vulnerable modules

**DECISION** (dependency and toolchain change across both services).

1. Set `go 1.26` and a `toolchain go1.26.x` line (the latest patch) in both `go.mod` files. Change `GO_IMAGE` in `deploy/scripts/central-build.sh` to the same version.
2. Update the modules to at least:

   | Module | Version |
   |---|---|
   | golang.org/x/net | v0.60.0 |
   | golang.org/x/text | v0.41.0 |
   | golang.org/x/crypto | v0.56.0 |
   | github.com/jackc/pgx/v5 | v5.9.2 |
   | github.com/go-chi/chi/v5 | v5.3.0 |
   | github.com/redis/go-redis/v9 | v9.7.3 |

   Run `go mod tidy`.
3. Read the release notes of pgx (v5.7 → v5.9) and chi (v5.1 → v5.3) for behaviour changes.
4. Re-run `govulncheck` with the new toolchain; it must report nothing reachable.
5. Run both modules' full unit and database test suites, then deploy to PRE-LIVE for Product Owner testing before merge.

**Test:** `govulncheck` clean; both test suites pass; PRE-LIVE smoke check (sign-in, guest sign-in, licence fetch, mTLS assignment fetch).

### P0-3 — Pin the toolchain that builds the appliance binaries

1. Find where edged, scd, portald, netd, acctd and pmsd are built for PRE-LIVE.
2. Make that build use a pinned Go image, as `central-build.sh` does: `-trimpath`, `CGO_ENABLED=0`, a pinned `GOOS` and `GOARCH`, and the same version as P0-2.
3. Record the toolchain version in the build provenance.

**Test:** the build prints the pinned version, and `go version -m <binary>` on the output shows it.

### Batch B0

| Batch | Findings | Files | Notes |
|---|---|---|---|
| **B0a** | P0-1 | `tools/go-checks.sh`, `tools/preflight.sh`, `.github/workflows/` (manual only) | No product code. Safe at any time. |
| **B0b** | P0-2 | `control-plane/go.mod`, `go.sum`, `data-plane/go.mod`, `go.sum`, `deploy/scripts/central-build.sh` | DECISION. Touches every package's build: do it on its own branch, when no other backend branch is open, to avoid `go.sum` conflicts. |
| **B0c** | P0-3 | appliance build script | Do together with B0b. |

## Phase map

| Phase | Packages |
|---|---|
| 1 | control-plane (all) |
| 2 | license/ (shared module), cmd/keybootstrap, data-plane/internal: identity, applianceauth, appliancecert, localkeys, keybootstrap, sealbox, backupcrypt, otp, otpkey, totp, passwordhash, throttle, authctx, signinattempt, writerguard, licstate, activation, assignment, offline, portaltls, crockford, hwid |
| 3 | control-plane/migrations, data-plane/migrations, deployment, buildprofile |
| 4 | cmd/scd, iamv2, stayengine, staygrant, grace, social, socialloader, deviceselfservice, enforce, namenorm, phone |
| 5 | cmd/portald, portaldesign, mail, sms, whatsapp, notifyloader, codegen |
| 6 | payment, checkout, ledger, posting, postinghandoff, poststay, transfer, cmd/acctd, shape, shapeplan, shapeproducer |
| 7 | cmd/pmsd, pms, pmsd, pmsguard, pmsloader, pmsprovider, pmsresolve, pmsrest |
| 8 | cmd/netd, netcfg, nft, nftconverge, nftfoundation, conntrack, sysnet, kerneltest, healthdir |
| 9 | cmd/edged (9a–9d), modules, tenantcfg, reqlog, metrics, livez, startupbackoff |
| 10 | cross-cutting, final report |
