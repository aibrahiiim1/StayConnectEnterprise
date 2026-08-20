# Factory-clean install and disaster recovery — StayConnect appliance

**Scope.** How a *new* appliance is built from repository-controlled sources alone, with **no** database dump,
`/etc/stayconnect`, service state, test identity, account, voucher or other runtime state copied from any
existing machine. This is the procedure behind the **Fresh Production Appliance Deployment** strategy
(D35/T0075) and it is also the disaster-recovery path for a lost appliance.

> The DEVELOPMENT appliance `172.21.60.23` is **reference evidence only**. It is never an installation source.
> Its database contains development tenants, operators, plans, vouchers and PMS probes; copying any of it
> would carry those identities into a production system.

---

## 1. What must come from the repository, and what must not

| Layer | Repository-controlled | Arrives at runtime |
|---|---|---|
| OS + packages | base image / distro install | — |
| PostgreSQL schema | `data-plane/migrations/*.up.sql` + `data-plane/migrations/iam_base/` | — |
| Service binaries | built from `data-plane/cmd/*` | — |
| Hotel-Admin bundle | built from `hotel-admin/` (Next standalone) | — |
| systemd units | `deploy/systemd/*.service`, `deploy/kea/systemd/override.conf` | — |
| Database roles + privileges | `deploy/gatep/gatep-roles.sql`, `gatep-iam-roles.sql`, `gatep-grants.sql` (+ its includes) | passwords generated **on the appliance**, never committed |
| Network baseline | `deploy/netplan/`, `deploy/nftables/`, `deploy/sysctl/`, `deploy/tmpfiles/` | WAN/LAN addressing confirmed on site |
| Reverse proxy | `deploy/caddy/` + the managed hotel-admin vhost | certificate minted on the appliance |
| DHCP | `deploy/kea/` | leases are runtime state |
| **Appliance identity** | — | **enrollment → claim → signed assignment** |
| **Tenant / Site** | — | **signed assignment document** (never env, never a dump) |
| **Licence** | — | installed via `POST /license`, hardware/identity bound |
| **Operators** | — | created through Hotel Admin after claim |
| **Guest access config, packages, plans, PMS interfaces, networks** | — | Hotel-Admin configuration |
| **Guests, accounts, vouchers, sessions, folios** | — | real operation only |

**Verified:** a factory-clean build seeds **only** the `schema_migrations` ledger. Every identity-bearing
table is empty — `tenants=0 sites=0 appliances=0 operators=0 guest_accounts=0 vouchers=0 ticket_templates=0`.

`edged` resolves identity from the **signed assignment** first; a generic appliance with none runs in
*awaiting-assignment* mode. `EDGED_TENANT_ID` / `EDGED_SITE_ID` exist only as a documented migration-only
fallback and **must not be set on a production appliance**.

`scripts/phase1-bootstrap.sh` creates a dev tenant, site, appliance and a test voucher. **It must never be
run on a production appliance.**

---

## 2. The ordered install path

Order is load-bearing, and it is not the numeric order of the files:

```
0. timescale/timescaledb:2.16.1-pg16                    the appliance's own image (see §4E)
1. deploy/gatep/gatep-roles.sql                         the four runtime service roles, no passwords
2. data-plane/migrations/0001..0008                     public platform (0002 creates public.guest_networks)
3. deploy/gatep/gatep-iam-roles.sql                     iam_v2_owner + iam_v2_migrator, CREATE + REFERENCES
4. data-plane/migrations/iam_base/mg0_guest_networks_tsi_anchor.sql
                                                        the contract-defined tenant/site anchor
5. data-plane/migrations/iam_base/mg1..mg9              the IAM-v2 domain, applied AS iam_v2_owner
6. data-plane/migrations/0009..0048                     everything that builds on the IAM domain
7. deploy/gatep/gatep-iam-ownership.sql                 assert iam_v2 ownership (see §4F) — BEFORE the grants
8. deploy/gatep/gatep-grants.sql                        least-privilege grants + fail-closed assertions
```

Step 7 precedes step 8 so the privilege bootstrap is applied to the final ownership state rather than to an
intermediate one.

Every step, base steps included, is recorded in `schema_migrations`. Steps 4-5 sit between `0008` and `0009`
because `0009` is the first migration to address `iam_v2` objects, and `mg1` in turn needs
`public.guest_networks` from `0002`.

Run `scripts/clean-install-reconstruction.sh` to build and verify this path in a disposable container. It
touches no appliance, seeds no identity, and reports what the build actually produced.

---

## 3. Corrections to the first audit

Two claims in the first version of this document were **wrong** and are withdrawn.

**The foreign key was misattributed.** Committed `mg1` does **not** foreign-key `pms_interfaces` to
`guest_networks`. The anchor belongs to **`iam_v2.guest_network_pms_map (tenant_id, site_id,
guest_network_id) → public.guest_networks (tenant_id, site_id, id)`**. I queried the wrong table and drew a
conclusion from its absence.

**The development appliance had not drifted.** MG-0 creates the anchor as a **unique INDEX**
(`CREATE UNIQUE INDEX CONCURRENTLY guest_networks_tsi_anchor`), and my check queried `pg_constraint`, which
cannot see an index. Re-queried correctly, read-only, the appliance has exactly what the Phase-1A live-dark
acceptance records:

```
guest_networks_tsi_anchor | valid=true | ready=true
CREATE UNIQUE INDEX guest_networks_tsi_anchor ON public.guest_networks USING btree (tenant_id, site_id, id)
guest_network_pms_map_tenant_id_site_id_guest_network_id_fkey ::
  FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES guest_networks(tenant_id, site_id, id) ON DELETE CASCADE
```

Nothing was lost and nothing was removed by later work. **There are not two competing baselines**, and the
earlier claim that the appliance "was not built from committed mg1" is retracted. The contract-defined
tenant/site-scoped anchor and its mapping FK are the single authoritative baseline, and the clean-install
path now asserts both and fails without them.

The earlier **"matches the appliance exactly"** claim was also overstated: it rested on a table count. What
follows is the semantic comparison that claim should have been.

## 4. Findings — what a factory-clean install requires

### A. The IAM-v2 base schema was not in the ledger  ·  **CORRECTED**

Migrations `0009`+ address `iam_v2` objects and nothing in the numbered sequence created the schema, so a
clean database stopped at `0009`. The accepted base is now promoted to
`data-plane/migrations/iam_base/` (MG-0 plus `mg1..mg9`), applied between `0008` and `0009`, and **recorded in
`schema_migrations`**. `iam_v2_scratch/` is retained as provenance and is no longer an installation source.

### B. MG-0 was not part of the install path  ·  **CORRECTED**

The anchor is a migration step, not an incidental constraint.
`data-plane/migrations/iam_base/mg0_guest_networks_tsi_anchor.sql` creates the same named unique index and
asserts `indisvalid`. It is deliberately **not** an `ALTER TABLE … ADD CONSTRAINT … UNIQUE`, which would
satisfy the FK while producing a different catalog object from the accepted baseline.

### C. Three tables were created by a running service  ·  **CORRECTED**

`edge_executed_commands`, `edge_installed_updates` and `edge_offline_packages` were created by `scd` at
runtime, and `deploy/scripts/phase7-appliance-m4.sh` says so outright. Gate-P grants on them, and Gate-P runs
before any service starts, so a clean install failed with `relation "public.edge_executed_commands" does not
exist`. Migration **`0048`** now declares them, shapes verified column by column against the appliance.

### D. The IAM ownership roles lived in the scratch fixture  ·  **CORRECTED**

`iam_v2_owner` and `iam_v2_migrator` existed only in `iam_v2_scratch/roles.sql`, headed *"Scratch-only role
model"*. Ownership is the security model here — the IAM boundary functions are `SECURITY DEFINER` and execute
as their owner — so production ownership cannot be sourced from a test fixture.
**`deploy/gatep/gatep-iam-roles.sql`** now defines them, grants `iam_v2_owner` to `iam_v2_migrator`, revokes
`CREATE` on `public` from `PUBLIC`, grants `CREATE ON DATABASE` via `current_database()`, and grants
`REFERENCES` (only) on `public.guest_networks`.

### E. The base image is part of the install path  ·  **CORRECTED**

The appliance runs **`timescale/timescaledb:2.16.1-pg16`**. Reconstructing on plain `postgres:16` silently
omitted the `ts_insert_blocker` hypertable triggers on `public.accounting_records` and `public.audit_log` —
a difference no table count can see. The reconstruction now uses the same image and the triggers match.

### F. Ownership of `iam_v2` objects created by migrations `0009`+  ·  **CLOSED**

Migrations `0009`+ must run as the platform role — they maintain `public.schema_migrations` and public objects,
and running them as `iam_v2_owner` fails immediately with `permission denied for table schema_migrations`. The
consequence was that every `iam_v2` object those migrations created was left owned by the platform role.

That is not cosmetic. The IAM boundary functions are `SECURITY DEFINER` and **execute as their owner**, so
ownership decides what the whole boundary can reach; a schema that agrees on every column, constraint and index
and disagrees on ownership is a different security model wearing the same shape.

Closed by **`deploy/gatep/gatep-iam-ownership.sql`**, an explicit install step (§2 step 7). It reassigns every
table, view, materialised view, sequence, function and procedure in `iam_v2` to `iam_v2_owner`, takes the schema
itself, and then **fails closed** twice: once naming any object left behind, once requiring every `SECURITY
DEFINER` function in `iam_v2` to execute as `iam_v2_owner`. The verifier applies it **twice** — the DR
procedure re-runs this step on a rebuild, and a step documented as idempotent that has only ever been run once
is a claim rather than a property.

The verifier does not take the step's word for it. `scripts/clean-install-reconstruction.sh` asks the catalog
who owns each object afterwards, names any exception rather than counting them, and reports ownership as an
ordinary catalog fact in the semantic comparison — so a build that fails to converge shows up as a difference,
not as silence.

```
every iam_v2 table, view, sequence, function and procedure is owned by iam_v2_owner
all 67 SECURITY DEFINER functions in iam_v2 execute as iam_v2_owner
schema iam_v2 is owned by iam_v2_owner
```

**One consequence, recorded rather than acted on:** the development appliance carries this same drift — 107
functions and 6 tables in `iam_v2` owned by `stayconnect` — because the same migrations ran the same way there.
A factory-clean build is now *more* correct than the appliance on this point. 172.21.60.23 was **not** modified;
it is reference evidence, not an installation source.

### G. The superseded guest-auth authority was configuration-selectable  ·  **CLOSED for the production runtime**

Per the Production objective of zero superseded **active** runtime dependency, a fallback that merely *isn't
taken* is not removed: it is one environment variable, one bad rollback or one half-restored env file away from
being the authority again — and it would fail **towards** the superseded system rather than refusing to run.

Closed by **`data-plane/internal/iamv2/guest_authority_lock.go`**, applied inside `LoadConfigFromEnv` and keyed
on a **build tag**, `stayconnect_production` (`build_profile_production.go` / `build_profile_development.go`).

Keyed on the build, not on the `productionProfile` argument: that argument is already passed as `true` by both
live services, **including on the DEVELOPMENT appliance**, so keying the lock on it would have changed the
behaviour the development trial was accepted with. And a switch that decides which IAM authority is in force
must not be reachable by the configuration it governs — the Production binary carries the answer inside it.
`Config.SafeFlagSummary()` reports `build=production` or `build=development` at startup, so which binary is
running is visible in a log rather than inferred.

On a production build:

* `STAYCONNECT_IAMV2_VOUCHER` or `..._ACCOUNT` set to `false`/`0` is a **startup refusal** naming the exact
  variable and why, not a silent downgrade;
* the master switch cannot be off either, since that would leave every method disabled — precisely how the
  runtime behaved before IAM-v2 existed;
* with nothing configured at all, both locked methods come up as IAM-v2.

`MethodOTP` and `MethodSocial` are deliberately **not** locked on: enabling them reaches an external SMS, email
or OAuth provider, and that is gated by its own decision. They can still only ever be configured *on* — never
redirected to a superseded implementation. Operator authentication is untouched: `public.operators` is a live
platform foundation, not superseded guest IAM (§4G table below).

Builds **without** the tag — the DEVELOPMENT appliance and every ordinary `go build` — keep the configurable
behaviour, because the DEVELOPMENT appliance deliberately exercises both authorities and its accepted evidence
depends on being able to.

Proven on **both** builds. `guest_authority_lock_test.go` drives the lock with the build constant supplied
explicitly, so a single run proves both sides; `guest_authority_e2e_production_test.go` and
`guest_authority_e2e_development_test.go` then prove the real loader end to end, one under each tag. CI builds,
vets and tests the production tag as well as the development one, because a production-only build tag that CI
never compiles is a build tag nobody has proven. The clean-install verifier re-runs both, so the property is
evidenced by the same artifact as the schema:

```
PRODUCTION build: refuses every configuration that would select the superseded guest authority
  (explicit disable, master off, and unset all proven)
DEVELOPMENT build: keeps the configurable behaviour the accepted trial evidence depends on
```

The one assertion this deliberately contradicts is the dark-era "everything defaults OFF" default in
`concurrency_test.go`. It is scoped to the development build rather than weakened for both: on a Production
binary an all-off configuration is a startup refusal, not a valid dark default.

### H. Bounded dependency review of the superseded guest-auth objects  ·  **physical removal NOT yet provable**

The authority is closed; the **tables** are a separate question, and the honest answer is that a physical drop
is not provable in this pass. Reviewed read-only on the development appliance:

| Object | Verdict |
|---|---|
| `guest_accounts`, `ticket_templates`, `vouchers`, `voucher_batches` | **superseded** — IAM-v2 owns credentials, plans and vouchers |
| `sessions` | **superseded** for guest sessions (`iam_v2.sessions` is authoritative) |
| `auth_otps` | **superseded** by the IAM-v2 OTP path |
| `guests` | **superseded** as a guest-identity store |
| `operators`, `operator_roles` | **RETAIN** — operator authentication is a live platform foundation, and `iam_v2.publish_checkout_grace_policy` validates its actor against `public.operators` |
| `tenants`, `sites`, `appliances`, `guest_networks` | **RETAIN** — platform identity and networking foundations |
| `audit_log`, `accounting_records` | **RETAIN** — TimescaleDB hypertables, still current |

Inbound foreign keys into the superseded set, and what still holds them:

```
public.payments             -> ticket_templates, vouchers      still referenced by edged (resources_site.go)
public.social_oauth_states  -> ticket_templates                still referenced (3 call sites)
public.voucher_batches      -> ticket_templates
public.vouchers             -> ticket_templates, voucher_batches
public.auth_otps            -> ticket_templates
public.guest_accounts       -> ticket_templates
public.sessions             -> guest_accounts, guests, vouchers
```

No view or function references them. But `payments` and `social_oauth_states` are **current** and still
foreign-key into `ticket_templates` and `vouchers`, so dropping those parents today would break code that is
not superseded. Per the instruction to remove superseded tables only after proving no current required
dependency, **that proof does not hold and the tables stay.** Migrating `payments` and `social_oauth_states`
onto IAM-v2 parents is the prerequisite, and it is its own bounded work item — deliberately not expanded into
here. Historical migrations and evidence are preserved either way.

A factory-clean install creates these tables **empty** and no configuration can make them the guest authority,
so a fresh Production appliance has zero superseded *active* runtime dependency for guest IAM. That is the claim
being made; "the tables do not exist" is not.

## 5. Semantic reconstruction evidence

`scripts/clean-install-reconstruction.sh`, blank `timescale/timescaledb:2.16.1-pg16` container on a
workstation, never connected to any appliance. 58 install steps applied, all recorded in the ledger.

```
CLEAN_INSTALL_RECONSTRUCTION = PASS
public tables 44 · iam_v2 tables 74 · identity tables all 0
guest_networks_tsi_anchor present and valid, definition matches the contract
guest_network_pms_map composite FK to guest_networks present
every iam_v2 table, view, sequence, function and procedure is owned by iam_v2_owner
all 67 SECURITY DEFINER functions in iam_v2 execute as iam_v2_owner
schema iam_v2 is owned by iam_v2_owner
production profile refuses every configuration that would select the superseded guest authority
```

Semantic comparison against the development appliance's catalog (read-only reference; 8,371 facts in the clean
build). Every remaining difference is named and directional — none is unexplained:

| Fact type | only in appliance | only in clean build | reading |
|---|---:|---:|---|
| COLUMN | 0 | 0 | identical |
| CONSTRAINT (PK/UNIQUE/FK/CHECK) | 0 | 0 | identical |
| INDEX | 0 | 0 | identical |
| TRIGGER | 0 | 0 | identical, hypertable blockers included |
| SCHEMAOWNER | 0 | 0 | identical |
| OWNER | 6 | 6 | same 6 tables; appliance `stayconnect`, clean build `iam_v2_owner` — **§4F, clean build correct** |
| FUNCTION | 107 | 107 | same 107 functions; owner differs, same cause |
| TABLEGRANT | 44 | 42 | 42 follow the ownership move; the extra 2 are §4F-note below |
| FUNCGRANT | 1312 | 48 | 8 roles absent from a clean install (below); 48 are the owner's own, from owning the 107 |
| MEMBERSHIP | 2 | 0 | `p3_guard_probe` / `p3_noexec_probe` → `phase2_runner` — trial artifacts, correctly absent |

**The structural surface is identical**, and the ownership, function and grant differences all resolve to three
causes, each in the correct direction:

1. **Ownership convergence (§4F).** The clean build owns the IAM domain as `iam_v2_owner`; the appliance leaves
   107 functions and 6 tables owned by `stayconnect`. The 42 `TABLEGRANT` rows are the owner's implicit
   privileges on those 6 tables moving with them, and the 48 `FUNCGRANT` rows are `iam_v2_owner` and
   `iam_v2_migrator` gaining `EXECUTE` by owning what they should own.
2. **Roles a clean install correctly does not create.** Five scratch-fixture roles — `iam_v2_svc_scd`,
   `iam_v2_svc_edged`, `iam_v2_svc_acctd`, `iam_v2_svc_portald`, `iam_v2_svc_hoteladm` — from
   `iam_v2_scratch/roles.sql`, whose own header reads *"Scratch-only role model"*. Nothing connects as them: the
   runtime services connect as `svc_scd`, `svc_edged`, `svc_acctd` and `svc_netd`. Their 164 `FUNCGRANT` rows
   each are simply the `PUBLIC` default surface every role gets, not a bespoke privilege.
3. **Test identities.** `phase2_runner`, `p3_guard_probe`, `p3_noexec_probe` — Phase-2/3 privilege probes. A
   factory-clean install has none, which is the requirement.

**Privilege parity for the roles that actually run is exact.** Every runtime service role has an identical
`EXECUTE` surface in both: `svc_scd` 171, `svc_edged` 167, `svc_acctd` 173, `svc_netd` 173, `sc_commerce_runtime`
165, `sc_payment_runtime` 171, `sc_payment_outcome` 165, `sc_financial_operator` 171, `sc_financial_readonly`
164. Not one appears in the diff.

The two remaining `TABLEGRANT` rows are `iam_v2_owner` holding `SELECT` and `INSERT` on
`public.schema_migrations` **on the appliance only** — an ad-hoc grant from development. The clean build
deliberately lacks it: the IAM owner is not a platform ledger writer, which is precisely why migrations `0009`+
run as the platform role in the first place.

## 6. Disaster-recovery / fresh-install procedure

1. **Provision the server.** Fresh OS. Exactly **two NICs**: WAN (also management) and LAN. No third NIC.
2. **Install packages** and the PostgreSQL container per `deploy/compose/infra.yml`.
3. **Create the database and roles.** Apply `deploy/gatep/gatep-roles.sql` with passwords generated *on the
   appliance*; never reuse a committed or copied secret.
4. **Build the schema** in the order of §2. Record every step in `schema_migrations`.
5. **Apply `deploy/gatep/gatep-grants.sql`.** It re-asserts least privilege and ends in fail-closed
   assertions; a failure here is a stop, not a warning.
6. **Build the binaries with the production tag** — `go build -tags stayconnect_production ./...` in
   `data-plane/`. This is not optional and it is not cosmetic: without the tag the guest IAM authority stays
   configurable and the superseded path is one environment variable away (§4G). Confirm it after start-up —
   the `edged` and `scd` logs must report `build=production` in the IAM-v2 flag summary. Install binaries and
   units from `deploy/systemd/`; install the Hotel-Admin bundle with `deploy/scripts/deploy-hotel-admin.sh
   install`.
7. **Network baseline** from `deploy/netplan/`, `deploy/nftables/`, `deploy/kea/`, `deploy/caddy/`.
8. **Enrol and claim** the appliance against the Central Control Plane; wait for the **signed assignment**
   to resolve tenant and site. Do **not** set `EDGED_TENANT_ID` / `EDGED_SITE_ID`.
9. **Install the licence** through Hotel Admin.
10. **Configure the hotel** through Hotel Admin: networks, packages, access policy, PMS interfaces.
11. **Acceptance test**, then a Product-Owner **Go-Live** decision. IAM-v2 is the only guest IAM authority: on
    a production build it cannot be configured off, and an attempt to do so is a startup refusal (§4G).
    first operation; legacy guest-auth is never configured on.

**Never** restore a database dump, `/etc/stayconnect`, an identity or assignment document, or a licence from
another appliance in order to reproduce it. Restoring a backup is only valid for recovering *that same*
appliance's own state.
