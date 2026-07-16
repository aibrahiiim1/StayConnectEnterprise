# Phase 1B Planning Evidence — PostgreSQL Access Map (per service)

Read-only code inventory (source baseline `afade95`). Input to the least-privilege / superuser-elimination prerequisite (Phase-1B plan §2).

**Key finding:** every DB-connecting service today connects as the **superuser role `stayconnect`** (`rolsuper=true`). The default DSN in every daemon is `postgres://stayconnect:stayconnect@127.0.0.1:5432/<db>`. The `iam_v2_svc_*` least-privilege roles exist only as **NOLOGIN**, unbound placeholders in `iam_v2_scratch/roles.sql`.

## Databases
- **Site/edge DB `stayconnect_site`** (data-plane; one per appliance, loopback-only) — the Phase-1B target DB.
- **Central DB `stayconnect`** (control-plane) — out of Phase-1B scope.
- **`stayconnect_site_b`** — isolated second-site DB for isolation tests (not a replication standby).
- Postgres runs in Docker container `stayconnect-pg`.

## Per-service (site DB unless noted)
| Service | Source | DB user today | DSN source | In iam_v2 scope | Reads / Writes |
|---|---|---|---|---|---|
| `scd` | `data-plane/cmd/scd` | `stayconnect` (superuser) | env `SCD_DB_URL` (`scd/main.go:112`) | **YES** (auth/session/credential) | W: sessions, guests, guest_accounts, vouchers, auth_otps, social_oauth_states, pms_providers/attempts, sync_outbox, sync_checkpoints, tenant_effective_limits, guest_networks, audit_log, appliances, sites, tenants, edge_*; R: + ticket_templates, walled_garden_rules, notification_providers |
| `acctd` | `data-plane/cmd/acctd` | `stayconnect` (superuser) | env `ACCTD_DB_URL` (`acctd/main.go:47`) | **YES** (usage) | R: sessions; W: accounting_records, sessions |
| `edged` | `data-plane/cmd/edged` | `stayconnect` (superuser) | env `EDGED_DB_URL` (`edged/main.go:57`) | **YES** (admin CRUD of credentials/accounts/vouchers) | broad admin surface: guest_accounts, guest_networks, vouchers, voucher_batches, ticket_templates, pms_providers, social/stripe/notification providers, operators, operator_roles, walled_garden_rules, dhcp_*, network_interfaces, tenants, appliance_*, audit_log; R: + sessions, payments, backup_records, tenant_effective_limits, network_* |
| `netd` | `data-plane/cmd/netd` | `stayconnect` (superuser, root OS) | env `NETD_DB_URL` (`netd/main.go:53`) | no (networking public tables) | W: network_interfaces, network_config_revisions, network_apply_events, network_health_checks, system_network_audit; R: guest_networks, dhcp_* |
| `portald` | `data-plane/cmd/portald` | **none** | none (dials scd Unix socket `/run/stayconnect/scd.sock`) | no (**no DB connection**) | — |
| `hotel-admin` (Next.js) | `hotel-admin/` | **none** | none (HTTP → edged) | no (**no DB connection**) | — |
| `ctrlapi` | `control-plane/cmd/ctrlapi` | `stayconnect` (superuser) | env `CTRLAPI_DB_URL` (`config.go:23`); optional `CTRLAPI_GUEST_COMPAT_DB_URL` | no (central DB) | central fleet/licensing/billing surface |
| `nats-authz` | `control-plane/cmd/nats-authz` | `stayconnect` | env `AUTHZ_DB_URL` (`nats-authz/main.go:122`) | no (central DB) | R: appliance_certificates, revocations; W: audit_log |
| `cloud-admin`/`web-admin` (Next.js) | `cloud-admin/`,`web-admin/` | **none** | none (HTTP → ctrlapi) | no | — |
| `sitemigrate` | `data-plane/cmd/sitemigrate` | operator-supplied | CLI `--source`/`--dest` (`main.go:102-103`) | maintenance (one-shot) | central→site guest-domain copy |
| migrations | `Makefile` | `stayconnect` (superuser) | `psql -U stayconnect` (`Makefile:25-34,126-147`) | migrator | DDL + `schema_migrations` |

## Superuser evidence
`deploy/compose/infra.yml:17-19` (`POSTGRES_USER: stayconnect`); default DSNs hard-code user `stayconnect` (`scd/main.go:112`, `acctd/main.go:47`, `edged/main.go:57`, `netd/main.go:53`, `control-plane/internal/config/config.go:23`, `.env.example`, `Makefile:3,119`); all migration/psql use `-U stayconnect`.

## Least-privilege target set (Phase 1B, site DB)
LOGIN roles needed: `svc_scd`, `svc_edged`, `svc_acctd`, `svc_netd`; plus NOLOGIN `iam_v2_owner` (owns objects), `iam_v2_migrator` (member of owner, applies migrations). `portald` + the three Next.js UIs need **no** DB role. The `iam_v2_svc_portald`/`iam_v2_svc_hoteladm` skeleton roles are unused misnomers (recommend drop/rename — plan D6). `ctrlapi`/`nats-authz` are a separate future central-DB hardening.
