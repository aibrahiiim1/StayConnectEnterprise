# StayConnect IAM Phase 2 — Live-Dark Deployment Evidence

**Immutable record. DARK Phase 2 (all `STAYCONNECT_PHASE2_*` + `NEXT_PUBLIC_PHASE2_ADMIN` OFF). No cutover, no paid access, no PMS settlement. Appliance `radius` / `172.21.60.23`.**

Deployed source: branch `phase/2-commercial-packages`, PR #4. Pinned artifacts built `-trimpath`, `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` from the committed Phase-2 Go source (functionally HEAD `b89a744`).

## Pinned artifacts (SHA-256)

| Component | SHA-256 | Deployed path |
|---|---|---|
| scd | `1e25f9ef44b544d08d112d44f0847a674d51602362b01b9d2cb266ec740e1e86` | `/opt/stayconnect/bin/scd` |
| edged | `30ed45f15059390e559db4298295bbf5f6b48fb17595ee705eb1be8b44be9c34` | `/opt/stayconnect/bin/edged` |
| portald | `bf40065409504065bd24d75aaa8c9fa3b58ab47b47b0569755a1bb4ebe2ebd14` | `/opt/stayconnect/bin/portald` |
| hotel-admin bundle | `e25126737341d8f248ae3a4589ba3a72778705a00f25b8caf6312c64a723999d` (tarball) | release `20260718-083255` (symlinked) |

Rollback: prior binaries kept as `/opt/stayconnect/bin/{scd,edged,portald}.bak-phase2pre`; prior UI release retained via `hotel-admin.previous`.

## Pre-deployment
- Host identity: `radius` / `172.21.60.23` reverified.
- Fresh backup: `/opt/stayconnect/backups/pre-phase2-20260718-082640.dump` (`sha256 3af4237b573d59da908f333fe8a41cde2cafc5e6fadf9ed8990b1c67441a6843`).
- IAM-v2 baseline before migration: **49 tables / 0 rows**; migration `0009` not present; commerce tables present.
- `public` columns SHA-256 (pre): `833c3d6740af4ac79731bb038616559b999109df2f0bef37e95173ffe3a26bfb`.

## Migration 0009 (additive; applied via the migration executor)
Applied under `SET LOCAL ROLE iam_v2_owner` (DDL owned by `iam_v2_owner`, consistent with the rest of the schema); migration recorded as the superuser; single transaction, `ON_ERROR_STOP`. Created: 2 trigger functions + 2 triggers (`purchase_quote_pin_equal`, `offer_quote_immutable`) + 6 lookup indexes.
- `schema_migrations` records `0009_phase2_commerce`.
- Trigger functions owned by `iam_v2_owner`.
- **`public` columns SHA-256 (post) = `833c3d67…` — identical to pre (public schema unchanged).**
- iam_v2 rows after migration: **0**.

## Privileges (step 6)
Every Phase-2 flag is OFF and no Phase-2 repository is constructed, so **zero** new runtime Phase-2 iam_v2 privileges were granted and no runtime service was routed to iam_v2. Verified: `svc_scd`/`svc_edged`/`svc_acctd`/`svc_netd` hold **zero** iam_v2 table grants and **zero** iam_v2 function EXECUTE grants (Gate-P least-privilege intact).

## Darkness verification — before AND after one reboot
Boot before: `2026-07-17 21:17:32`. **Final reboot** → boot after: `2026-07-18 08:35:06`. Every check below passed identically pre- and post-reboot:

| Check | Result |
|---|---|
| Services active (scd/edged/portald/hotel-admin/netd/acctd) | all `active` |
| Deployed binary SHA-256 | match pinned (above) |
| Dark construction log | scd + edged: `phase2 master=false portal=false admin=false` |
| Phase-2 flags in units/env | none set (all default OFF); hotel-admin env has no `NEXT_PUBLIC_PHASE2_ADMIN` |
| scd `/v1/commerce/{packages,quote,confirm}` | **404 (routes absent)** |
| scd `/v1/health` (legacy) | 200 |
| `schema_migrations` 0009 | present |
| iam_v2 tables / rows | **49 / 0** |
| iam_v2 commerce data (quotes/purchases/entitlements/packages/plans) | all **0** |
| `public` columns SHA-256 | `833c3d67…` (unchanged) |
| svc roles iam_v2 grants | **ALL_ZERO** |
| Legacy smoke (portald `/`, portald `/generate_204`, hotel-admin `/login`, edged `/edge/v1/health`, scd auth-methods) | 200 / 302 / 200 / 200 / 200 |

## Final acceptance gate — UI bundle redeploy + second reboot (2026-07-18)

The final acceptance gate refactored the Hotel-Admin commercial-packages UI (typed publish form) and added the automated UI test suites. This changed a deployed runtime artifact (the hotel-admin bundle only — the Go `scd`/`edged`/`portald` binaries were NOT changed and keep their hashes above). The updated bundle was rebuilt (authoritative production build) and redeployed; a second reboot was performed and darkness re-verified.

- New hotel-admin bundle tarball SHA-256: `678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95` → release `20260718-115608` (stamp `20260718T115435Z-phase2-final`); prior release retained via `hotel-admin.previous`.
- Go binaries unchanged (verified after reboot): scd `1e25f9ef…`, edged `30ed45f1…`, portald `bf400654…`.
- Second reboot → boot `2026-07-18 11:56:34`. Re-verified: all six services active; hotel-admin `/login` 200; Phase-2 flags none set (hotel-admin env has no `NEXT_PUBLIC_PHASE2_ADMIN`); scd `/v1/commerce/{packages,quote,confirm}` **404**; dark construction `phase2 master=false portal=false admin=false`; iam_v2 **49/0**; `schema_migrations` 0009 present; iam_v2 commerce data **0**; `svc_scd`/`svc_edged`/`svc_acctd`/`svc_netd` **zero** iam_v2 grants; legacy smoke (portald `/`, scd health, edged health) all 200.

## Conclusion
Phase 2 is deployed **live-dark** and **reboot-verified** (twice): the commercial-packages code, migration 0009, pinned binaries and the final UI bundle are on the appliance; every surface is inert behind OFF flags; legacy public-schema authentication remains the sole production authority; iam_v2 remains empty except authorized schema metadata. No Phase-2 functionality is enabled and no IAM-v2 cutover was performed.
