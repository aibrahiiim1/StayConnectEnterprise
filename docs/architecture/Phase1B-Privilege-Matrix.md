# Phase 1B — Exact Least-Privilege Grant Matrix (machine-reviewable)

**Planning-only.** Authoritative grant specification for Gate P, derived from the completed DB-access inventory (`exports/chatgpt/phase1b-planning/inventory/DB_ACCESS_MAP.md`) and exact query/code inspection. **No grant is applied by this planning document.**

<!-- MACHINE ASSERTION — validated by tools/project-state.py -->
`PRODUCTION_IAM_V2_DML: NONE`  (no production runtime service role holds any `iam_v2` INSERT/UPDATE/DELETE/SELECT/EXECUTE grant)

**Binding rules (all rows conform):**
- **Production** service roles receive **only** `public`-schema privileges for their current legacy behavior — **zero `iam_v2` DML**, **zero `iam_v2` EXECUTE**.
- PUBLIC receives **zero** `iam_v2` privileges; `ALTER DEFAULT PRIVILEGES` denies future owner objects to service roles.
- **No** service role receives `ALL TABLES`, owner membership, `CREATE`/`ALTER`/`DROP`, `BYPASSRLS`, or superuser.
- `iam_v2` privileges appear **only** in the scratch/test section (§2); granting any to a runtime role in production requires a later separate runtime-routing (cutover) authorization.
- All service roles: `LOGIN`, `NOSUPERUSER`, `NOCREATEDB`, `NOCREATEROLE`, `NOBYPASSRLS`, `CONNECTION LIMIT` set, `statement_timeout`/`lock_timeout`/`idle_in_transaction_session_timeout` via `ALTER ROLE … SET`.
- Owner/migrator are **NOLOGIN**; migrations run via the non-runtime `migrate_exec` model (plan §2.10).

Legend: S=SELECT I=INSERT U=UPDATE D=DELETE · seq=sequence USAGE/SELECT · fn=function EXECUTE · ✓ granted · – denied. Columns are table-level unless a "cols" note appears. Rollback for every row = `REVOKE` the grant / point the service DSN back to the break-glass superuser (time-bounded, audited, removed after Gate P — plan §2.11).

---

## 1. PRODUCTION grants — `public` schema only (zero `iam_v2`)

### 1.1 `svc_scd` (site DB `stayconnect_site`) — session/auth/credential
| Table | cols | S | I | U | D | seq | reason / source-code path | negative test |
|---|---|---|---|---|---|---|---|---|
| sessions | all | ✓ | ✓ | ✓ | – | ✓ | `session.Manager.Start*`, reaper (`session.go`, `reaper.go`) | svc_acctd/svc_netd cannot INSERT sessions |
| guests | all | ✓ | ✓ | ✓ | – | ✓ | upsert by `(tenant,mac)` (`session.go`) | svc_netd denied |
| guest_accounts | all | ✓ | – | ✓ | – | – | validate + lockout/login counters (`credentials_handlers.go`) — INSERT is edged only | svc_scd cannot INSERT guest_accounts |
| vouchers | all | ✓ | – | ✓ | – | – | redeem state flip (`voucher/voucher.go`) | svc_acctd cannot read vouchers |
| auth_otps | all | ✓ | ✓ | ✓ | – | ✓ | OTP issue/verify/attempts (`otp/otp.go`) | svc_netd denied |
| social_oauth_states | all | ✓ | ✓ | ✓ | – | – | CSRF state single-use (`social_handlers.go`) | svc_acctd denied |
| social_oauth_providers | all | ✓ | – | – | – | – | provider config read (`socialloader`) | no write |
| pms_providers | all | ✓ | – | – | – | – | provider config read (`pmsloader`) | no write |
| pms_attempts | all | ✓ | ✓ | – | – | ✓ | per-room/IP lockout (`pmsguard/guard.go`) | no UPDATE/DELETE |
| tenants | auth_methods (read) | ✓ | – | – | – | – | `tenantcfg.Load` reads `auth_methods` | no write to tenants |
| tenant_effective_limits | all | ✓ | – | ✓ | – | – | concurrency/limits (`edge.go`) | — |
| guest_networks | all | ✓ | – | – | – | – | IP→network mapping (`netcontext.go`) | no write |
| notification_providers | all | ✓ | – | – | – | – | mail/sms provider (`notifyloader`) | — |
| walled_garden_rules | all | ✓ | – | – | – | – | portal allowlist | — |
| ticket_templates | all | ✓ | – | – | – | – | plan params | — |
| sync_outbox | all | ✓ | ✓ | ✓ | – | ✓ | durable telemetry outbox | — |
| sync_checkpoints | all | ✓ | ✓ | ✓ | – | ✓ | sync watermarks | — |
| audit_log | all | – | ✓ | – | – | ✓ | audit inserts | no SELECT/UPDATE/DELETE |
| auth_throttle_buckets *(new, §4b)* | all | ✓ | ✓ | ✓ | ✓ | – | durable throttling atomic upsert + cleanup | scope_key stores hash only |
| **iam_v2.* (any)** | — | – | – | – | – | – | **ZERO — production svc_scd has no iam_v2 grant** | svc_scd cannot SELECT/INSERT any iam_v2 table |

### 1.2 `svc_acctd` — accounting
| Table | S | I | U | D | seq | reason | negative test |
|---|---|---|---|---|---|---|---|
| sessions | ✓ | – | ✓ | – | – | usage/quota update (`acctd/main.go`) | cannot INSERT sessions; cannot read credentials |
| accounting_records | – | ✓ | – | – | ✓ | append usage samples | no UPDATE/DELETE (append-only) |
| **iam_v2.* (any)** | – | – | – | – | – | **ZERO** | svc_acctd cannot touch iam_v2 |

### 1.3 `svc_edged` — admin CRUD
| Table group | S | I | U | D | reason | negative test |
|---|---|---|---|---|---|---|
| guest_accounts, guest_networks, vouchers, voucher_batches, ticket_templates | ✓ | ✓ | ✓ | ✓ | admin CRUD (`edged`) | — |
| pms_providers, social_oauth_providers, notification_providers | ✓ | ✓ | ✓ | ✓ | provider config; secret columns write-only where applicable | cannot read another service's runtime rows it doesn't own |
| stripe_accounts | ✓ | ✓ | ✓ | ✓ | operator config | — |
| operators, operator_roles | ✓ | ✓ | ✓ | ✓ | RBAC admin | — |
| walled_garden_rules, dhcp_pools, dhcp_reservations, network_interfaces | ✓ | ✓ | ✓ | ✓ | network/portal config | — |
| tenants | ✓ | – | ✓ | – | auth_methods/config update | no INSERT/DELETE tenants |
| appliance_boot_convergence, appliance_recovery_events, appliance_service_health | ✓ | ✓ | ✓ | – | edge health | — |
| sessions, payments, backup_records, tenant_effective_limits, network_config_revisions, network_apply_events, network_health_checks | ✓ | – | – | – | admin read-only views | no write |
| audit_log | – | ✓ | – | – | audit | no SELECT/UPDATE/DELETE |
| **iam_v2.* (any)** | – | – | – | – | **ZERO — production** | svc_edged cannot touch iam_v2 in production |

### 1.4 `svc_netd` — networking only (no credentials, no iam_v2)
| Table | S | I | U | D | reason | negative test |
|---|---|---|---|---|---|---|
| network_interfaces, network_config_revisions, network_apply_events, network_health_checks, system_network_audit | ✓ | ✓ | ✓ | – | network apply (`netd/store.go`) | cannot touch credentials/sessions |
| guest_networks, dhcp_pools, dhcp_reservations | ✓ | – | – | – | read for apply | no write |
| **iam_v2.* (any)** | – | – | – | – | **ZERO** | svc_netd cannot touch iam_v2 or any credential table |

### 1.5 No-DB principals
`portald`, `hotel-admin` (Next.js), `cloud-admin`/`web-admin` (Next.js): **no DB role** (no direct connection). The unused `iam_v2_svc_portald` / `iam_v2_svc_hoteladm` skeleton roles are **removed/retired** (plan §2, D6).

### 1.6 Central DB (`stayconnect`) — OUT OF PHASE 1B
`ctrlapi`, `nats-authz` continue as-is in Phase 1B; their de-superuser is a **separate future central-DB security-hardening item** (recorded, not done here).

---

## 2. SCRATCH/TEST grants — `iam_v2` (acceptance testing ONLY; never production in 1B)

In the disposable scratch/test DB, test roles mirroring `svc_scd`/`svc_edged`/`svc_acctd` additionally receive the `iam_v2` privileges required to run the [IMPL] acceptance matrix. These grants **must not** exist on any production runtime role in Phase 1B.

| Test role | iam_v2 objects | S | I | U | D | fn EXECUTE | reason |
|---|---|---|---|---|---|---|---|
| `t_scd` | guest_principals, guest_principal_identities, guest_access_accounts, vouchers, voucher_batches, voucher_code_key_generations, auth_contexts, devices, device_network_appearances | ✓ | ✓ | ✓ | – | `reserve_device_slot`, `close_session` | credential validation + principal/device/auth_context resolution (W1–W3) |
| `t_scd` | entitlements, entitlement_devices, sessions, accounting_records, session_counter_watermarks | ✓ | ✓ | ✓ | – | `ingest_sample`, `apply_adjustment` | session-after-grant **adapter** scratch tests (W4) — entitlement/session portions are DEFERRED for production |
| `t_edged` | guest_access_accounts, vouchers, voucher_batches, entitlements (admin), guest_principals | ✓ | ✓ | ✓ | ✓ | – | admin CRUD acceptance |
| `t_acctd` | sessions (S), accounting_records (I) | ✓ | ✓ | – | – | `ingest_sample` | accounting idempotency tests |
| owner `iam_v2_owner` | all iam_v2 | (owner) | | | | | schema/object ownership; NOLOGIN |
| `iam_v2_migrator` | via SET ROLE owner | | | | | | migrations only; NOLOGIN |

Negative tests (both DBs): `t_*`/`svc_*` roles are `NOSUPERUSER NOBYPASSRLS`; cannot `DROP`/`ALTER` iam_v2 objects; PUBLIC has zero iam_v2 privileges; a `SELECT`/`INSERT` by a **production** `svc_*` role against any `iam_v2` table is **rejected**; `pg_stat_activity` shows `svc_*`, never `stayconnect`.

---

## 3. Migration executor (non-runtime) & break-glass (summary — full text in plan §2.10/§2.11)
- `iam_v2_owner` NOLOGIN (owns iam_v2), `site_migrator` NOLOGIN (owns public migrations), `iam_v2_migrator` NOLOGIN (SET ROLE owner).
- `migrate_exec` LOGIN — drives migrations via SET ROLE; enabled only for the migration window; audited; rotated + disabled after use; holds no runtime service privileges.
- `stayconnect` superuser DSN = **break-glass Gate-P rollback only** (time-bounded, audited, runbook-approved, credential removed after Gate-P acceptance; `RETAINED for rollback` with removal gate). Not a steady-state option.
