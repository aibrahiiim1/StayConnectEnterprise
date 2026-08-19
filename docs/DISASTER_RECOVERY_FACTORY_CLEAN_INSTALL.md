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
7. deploy/gatep/gatep-grants.sql                        least-privilege grants + fail-closed assertions
```

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

### F. **OUTSTANDING** — ownership of `iam_v2` objects created by migrations `0009`+

26 objects differ. On the appliance they are owned by `iam_v2_owner`; a clean build leaves them owned by the
platform role, because migrations `0009`+ must run as the platform role — they maintain
`public.schema_migrations` and public objects, and running them as `iam_v2_owner` fails immediately with
`permission denied for table schema_migrations`.

**This is a real blocker for a production install**, because it decides what the `SECURITY DEFINER` boundary
functions execute as. The correction is an explicit ownership-reassertion step in the installer
(`ALTER … OWNER TO iam_v2_owner` across `iam_v2`, asserted afterwards), authored deliberately rather than
improvised inside the verifier — the verifier's job is to expose this, not to hide it.

### G. **BLOCKER** — the legacy guest-auth authority is configuration-selectable

Per the Production objective of zero superseded **active** runtime dependency, this is a **Production-readiness
blocker**, not an observation. The authority switch is call-level (`if s.iamv2Cfg.Enabled(...)`) across six
files, so legacy remains compiled in and selectable by configuration.

Per-object assessment of the superseded `public` guest-auth surface:

| Object | Verdict |
|---|---|
| `guest_accounts`, `ticket_templates`, `vouchers`, `voucher_batches` | **superseded** — IAM-v2 owns credentials, plans and vouchers |
| `sessions` | **superseded** for guest sessions (`iam_v2.sessions` is authoritative) |
| `auth_otps` | **superseded** by the IAM-v2 OTP path |
| `guests` | **superseded** as a guest-identity store |
| `operators`, `operator_roles` | **RETAIN** — operator authentication is a live platform foundation, and `iam_v2.publish_checkout_grace_policy` validates its actor against `public.operators` |
| `tenants`, `sites`, `appliances`, `guest_networks` | **RETAIN** — platform identity and networking foundations |
| `audit_log`, `accounting_records` | **RETAIN** — TimescaleDB hypertables, still current |

Removing the superseded objects and the configurable fallback is a code and schema change of real size. It is
recorded here as the remaining Production-readiness blocker and proposed as its own Product-Owner work item;
historical migrations and evidence are preserved either way.

## 5. Semantic reconstruction evidence

`scripts/clean-install-reconstruction.sh`, blank `timescale/timescaledb:2.16.1-pg16` container on a
workstation, never connected to any appliance. 58 install steps applied, all recorded in the ledger.

```
CLEAN_INSTALL_RECONSTRUCTION = PASS
public tables 44 · iam_v2 tables 74 · identity tables all 0
guest_networks_tsi_anchor present and valid, definition matches the contract
guest_network_pms_map composite FK to guest_networks present
```

Semantic comparison against the development appliance's catalog (read-only reference, 9,639 facts):

| Fact type | only in appliance | only in clean build | reading |
|---|---:|---:|---|
| COLUMN | 0 | 0 | identical |
| CONSTRAINT (PK/UNIQUE/FK/CHECK) | 0 | 0 | identical |
| INDEX | 0 | 0 | identical |
| TRIGGER | 0 | 0 | identical, hypertable blockers included |
| SCHEMAOWNER | 0 | 0 | identical |
| OWNER | 26 | 26 | **outstanding — §4F** |
| FUNCTION | 182 | 182 | same functions, owner differs (same cause as OWNER) |
| TABLEGRANT | 184 | 182 | follows ownership |
| FUNCGRANT | 1464 | 0 | appliance carries extra trial/probe roles; a clean install correctly has none |
| MEMBERSHIP | 2 | 0 | `p3_guard_probe` / `p3_noexec_probe` — trial artifacts, correctly absent |

**The structural surface is identical.** The remaining differences are ownership (§4F, outstanding) and the
absence of trial-only roles, which is the desired direction for a clean install.

## 6. Disaster-recovery / fresh-install procedure

1. **Provision the server.** Fresh OS. Exactly **two NICs**: WAN (also management) and LAN. No third NIC.
2. **Install packages** and the PostgreSQL container per `deploy/compose/infra.yml`.
3. **Create the database and roles.** Apply `deploy/gatep/gatep-roles.sql` with passwords generated *on the
   appliance*; never reuse a committed or copied secret.
4. **Build the schema** in the order of §2. Record every step in `schema_migrations`.
5. **Apply `deploy/gatep/gatep-grants.sql`.** It re-asserts least privilege and ends in fail-closed
   assertions; a failure here is a stop, not a warning.
6. **Install binaries and units** from `deploy/systemd/` and the built `data-plane/cmd/*`; install the
   Hotel-Admin bundle with `deploy/scripts/deploy-hotel-admin.sh install`.
7. **Network baseline** from `deploy/netplan/`, `deploy/nftables/`, `deploy/kea/`, `deploy/caddy/`.
8. **Enrol and claim** the appliance against the Central Control Plane; wait for the **signed assignment**
   to resolve tenant and site. Do **not** set `EDGED_TENANT_ID` / `EDGED_SITE_ID`.
9. **Install the licence** through Hotel Admin.
10. **Configure the hotel** through Hotel Admin: networks, packages, access policy, PMS interfaces.
11. **Acceptance test**, then a Product-Owner **Go-Live** decision. IAM-v2 is the intended authority from
    first operation; legacy guest-auth is never configured on.

**Never** restore a database dump, `/etc/stayconnect`, an identity or assignment document, or a licence from
another appliance in order to reproduce it. Restoring a backup is only valid for recovering *that same*
appliance's own state.
