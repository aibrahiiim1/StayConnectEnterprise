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

## 2. Two install paths, and which one a Production appliance uses

A brand-new Production appliance and an existing installation reach the same schema by different routes, and
conflating them is what the Zero-Legacy criterion rejects.

### 2a. FACTORY-CLEAN BASELINE — what a NEW Production appliance uses

```
0. timescale/timescaledb:2.16.1-pg16                    the appliance's own image (§4E)
1. deploy/gatep/gatep-roles.sql                         the four runtime service roles, no passwords
2. deploy/gatep/gatep-iam-roles.sql                     iam_v2_owner + iam_v2_migrator
3. data-plane/migrations/baseline/0000_production_baseline.sql
                                                        the CURRENT schema, and only the current schema
4. deploy/gatep/gatep-iam-roles.sql                     again: its REFERENCES grant needs guest_networks
5. deploy/gatep/gatep-iam-ownership.sql                 assert iam_v2 ownership (§4F)
6. deploy/gatep/gatep-grants.sql                        least-privilege grants + fail-closed assertions
```

**The superseded guest-IAM tables are never constructed on this path — not even transiently.** That is
stronger than "the final catalog is clean", and the difference is not academic: a create-then-drop install
leaves those tables in every WAL segment, in any backup taken mid-install, and in the install log a reviewer
reads. Proved by `scripts/factory-clean-baseline-verify.sh`, which arms a **DDL event trigger on the empty
database before the first object exists**; any `CREATE TABLE` of a superseded name aborts the install. A
catalog check afterwards cannot tell "never created" from "created and dropped". This can, and it was
verified by deliberately appending `CREATE TABLE public.vouchers` to the baseline and watching it refuse.

The baseline is **generated, not hand-maintained** — `scripts/generate-production-baseline.sh` dumps the end
state of the upgrade path — and the verifier compares the two catalogs fact by fact, so the paths cannot
drift into new and existing appliances running different schemas.

### 2b. UPGRADE PATH — what an EXISTING installation uses

```
0-3. as above, then
4.  data-plane/migrations/0001..0008                    public platform (0002 creates public.guest_networks)
5.  data-plane/migrations/iam_base/mg0..mg9             the anchor, then the IAM-v2 domain
6.  data-plane/migrations/0009..0049                    0049 removes the superseded guest-IAM domain (§4H)
7.  Gate-P ownership and grants
```

This path still creates the superseded tables and then drops them, because that is what actually happened to
those installations. History is not rewritten.

### 2c. Migration 0049 is a one-way boundary

`0049` is **destructive and non-reconstructive**. Its down migration deliberately does **not** recreate
`sessions`, `guests`, `guest_accounts`, `vouchers`, `voucher_batches`, `ticket_templates` or `payments`; it
restores only the two nullable columns it dropped, so the boundary can be crossed in an upgrade rehearsal
without pretending the data came back.

**The rollback contract for 0049 is a restore, not a DDL step.** Before applying it: take a full database
backup and verify it restores. If an installation must return to the superseded domain, restore that backup.
Recreating empty tables no code can read would give back the risk and none of the function, and would leave
an operator believing they had rolled back when they had only rebuilt the shape.

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

### G. The superseded guest-auth authority was configuration-selectable  ·  **CLOSED, then superseded by §4H**

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

### H. The superseded guest-IAM domain is REMOVED  ·  **CLOSED**

The earlier readiness criterion was *zero superseded **active** dependency*: the tables existed but no
configuration could make them the guest authority. The Product Owner superseded that before any deployment.
The requirement is **physical** zero-superseded guest IAM, and an unselectable table is still a table —
anything holding the privilege can write it, it must be backed up, restored and reasoned about, and the only
thing between it and the authority is a build flag.

**The dependency map, taken from a real factory-clean build.** Nothing outside the superseded set pointed into
it except two tables:

```
public.payments            -> ticket_templates, vouchers
public.social_oauth_states -> ticket_templates
public.auth_otps           -> ticket_templates
public.guest_accounts      -> ticket_templates
public.voucher_batches     -> ticket_templates
public.vouchers            -> ticket_templates, voucher_batches
public.sessions            -> guest_accounts, guests, vouchers
```

No view, function or trigger referenced any of them. `public.accounting_records.session_id` carries no FK and
belonged to the legacy accounting pass, which is removed.

**Why those two still depended on the superseded set, and what replaced them.**

* **`public.payments`** was a read-only Stripe-session history keyed to a voucher and an access plan. It had
  **no writer anywhere in the tree** — the current financial authority is the Phase-4 IAM-v2 model
  (`payment_transactions`, `payment_transaction_events`, `payment_provider_accounts`, `settlements`,
  `purchases`, surfaced through `iam_v2.v_financial_settlements`). It was a second, thinner payment history
  beside the real one. Removed with its operator surface.
* **`public.social_oauth_states`** is the OAuth **handshake nonce** store, and **it is retained**. Its
  `template_id` pinned the access plan at handshake time — the superseded coupling, not a property of OAuth.
  In the current model the guest authenticates first and selects a package afterwards in the commerce flow,
  and the verified issuer-scoped subject is mapped to a principal by `authSocialIdentity` into
  `iam_v2.guest_principal_identities`. The column and its FK are dropped; the store stays.
* **`public.auth_otps`** is the OTP **challenge** store and is retained for the same reason, and on an
  explicit accepted basis: `internal/iamv2/adapters.go` records that per **D2** the challenge is verified
  upstream and only the verified factor crosses into `iam_v2`. Delivery scaffolding, not an identity store.

None of this invented business semantics: every replacement already existed and was already accepted.

**What was removed, and what it moved to:**

| Removed | Current authority |
|---|---|
| `public.sessions` | `iam_v2.sessions` (+ `session_entitlement_bindings`, watermarks) |
| `public.guests` | `iam_v2.guest_principals` (+ `guest_principal_identities`) |
| `public.guest_accounts` | `iam_v2.guest_access_accounts` |
| `public.vouchers` / `voucher_batches` | `iam_v2.vouchers` / `iam_v2.voucher_batches` |
| `public.ticket_templates` | `iam_v2.internet_packages`, `service_plans`, `package_eligibility_rules` |
| `public.payments` | `iam_v2.payment_transactions`, `settlements` |

**Retained and still current:** `auth_otps`, `social_oauth_states`, `social_oauth_providers`,
`otp_hmac_key_generations`, `auth_throttle_buckets`, `accounting_records` (a TimescaleDB hypertable and a
historical series — destroying an accounting series is not a schema cleanup), and every platform foundation:
`tenants`, `sites`, `appliances`, `operators`, `operator_roles`, `guest_networks`, networking, audit,
licensing and enrolment.

**One thing the removal nearly took with it.** The site-wide **licensed concurrent-guest cap** lived only in
the superseded session manager, which owned session creation. Deleting that manager would have deleted the
licence limit itself — an unlicensed appliance that still works. It now runs inside the IAM-v2 activation
transaction (`cmd/scd/iamv2_session_activate.go`), counted in the same transaction that inserts, against the
same local signed licence, and refuses with `LICENSE_CAPACITY_REACHED`.

**The build-tag guard is retained as a defensive assertion**, which is a demotion worth stating: it no longer
guards a selectable implementation, because there is none. It catches an operator who still believes there is
one — a configuration saying `STAYCONNECT_IAMV2_VOUCHER=false` during an upgrade from a pre-removal release
would otherwise start an appliance that refuses every guest with no explanation.

**History is preserved.** Migrations `0001`–`0048` still create these tables; `0049` removes them at the end
of the chain, so an existing installation upgrades through the same ordered path and a factory-clean install
ends with none of them. `0049`'s down migration deliberately does **not** recreate them: empty tables that no
code can read are the risk without the function, and a real rollback is a restore from a pre-upgrade backup.

## 5. Factory-clean verification evidence

### 5a. The factory-clean BASELINE path (what Production uses)

`scripts/factory-clean-baseline-verify.sh`, blank `timescale/timescaledb:2.16.1-pg16`, never connected to
any appliance:

```
FACTORY_CLEAN_BASELINE = PASS (current-only: the superseded guest-IAM tables were never constructed)
tripwire armed before a single object exists
baseline applied with the tripwire armed throughout
none exist  ·  no foreign key references any of them
every iam_v2 object is owned by iam_v2_owner
every SECURITY DEFINER function executes as iam_v2_owner
guest_networks_tsi_anchor present and valid  ·  guest_network_pms_map composite FK present
every identity-bearing table is empty
catalog facts: 8104  ·  IDENTICAL to the upgrade path's end state
```

The tripwire was checked by appending `CREATE TABLE public.vouchers` to the baseline: the install aborted.
A verifier that cannot fail is not evidence.

### 5b. The UPGRADE path

`scripts/clean-install-reconstruction.sh`, 59 ledger-recorded steps, `CLEAN_INSTALL_RECONSTRUCTION = PASS`,
ending with none of the superseded tables, no foreign key referencing them, and the IAM-v2 guest domain
present and empty. `public` 44 → 37 tables.

### 5c. The licensed concurrent-guest gate

Scope resolved from the accepted implementation, not assumed: **per APPLIANCE**. One licence is issued per
appliance and bound to its hardware serial and WAN MAC, and the superseded session manager took a
transaction advisory lock on the appliance id and counted that appliance's sessions. The scope moved with the
authority; it did not change. `iam_v2.sessions` carries no `appliance_id` — the DEVICE does — so the count
joins through it.

Four integration tests in `cmd/scd/license_capacity_integration_test.go`, against a real PostgreSQL:

| Test | Proves |
|---|---|
| `ForcedInterleavingCannotOverfill` | two transactions held open across the check-then-insert window; the second **blocks** and is refused |
| `LastSlotIsRaceSafe` | 16 simultaneous contenders for one slot → exactly 1 admitted, 15 refused, never over the licence |
| `AppliancesAreIndependent` | appliance A full does not block B; B's guests do not count against A; B enforces its own limit |
| `AppliancesRaceIndependently` | both appliances race for their own last slot at once; each ends at exactly its own limit |
| `UnlimitedDoesNotSerialize` | a non-positive limit takes no lock and refuses nothing |

**Each was checked by breaking the thing it guards.** Removing the advisory lock fails
`ForcedInterleavingCannotOverfill`; widening the scope from appliance to site fails both independence tests.
The 16-way stress test alone did **not** catch a missing lock — it passed three runs without one — which is
why the forced-interleaving test exists and why the stress test is not the evidence.

Central Control Plane availability plays no part: the limit is read from the signed licence on disk, so guest
admission remains local-first.

**Build and test.** `go build`, `go vet` and `go test ./...` pass on both the development build and the
production build (`-tags stayconnect_production`); the Hotel-Admin Next build and 83 Playwright tests pass.

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
