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
| PostgreSQL schema | `data-plane/migrations/*.up.sql` + the IAM-v2 base schema (§3) | — |
| Service binaries | built from `data-plane/cmd/*` | — |
| Hotel-Admin bundle | built from `hotel-admin/` (Next standalone) | — |
| systemd units | `deploy/systemd/*.service`, `deploy/kea/systemd/override.conf` | — |
| Database roles + privileges | `deploy/gatep/gatep-grants.sql` (+ the per-service files it includes) | passwords generated **on the appliance**, never committed |
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
1. data-plane/migrations/0001..0008        public platform (0002 creates public.guest_networks)
2. the guest_networks composite anchor      UNIQUE (tenant_id, site_id, id)      <-- see §3, correction A
3. iam_v2_scratch/migrations/mg1..mg9       the IAM-v2 domain (creates schema iam_v2, 49 tables)
4. data-plane/migrations/0009..0047         everything that addresses iam_v2 objects
5. deploy/gatep/gatep-grants.sql            roles, least-privilege grants, fail-closed assertions
```

Run `scripts/clean-install-reconstruction.sh` to build and verify this path in a disposable container. It
touches no appliance, seeds no identity, and reports what the build actually produced.

---

## 3. Findings — what currently blocks a clean install

### A. The IAM-v2 base schema is not in the numbered migration ledger  ·  **BLOCKING**

`data-plane/migrations/0009` onwards address `iam_v2` objects, and **nothing in that sequence creates the
schema**. A clean database stops at `0009` with `schema "iam_v2" does not exist`. The base schema lives in
`iam_v2_scratch/migrations/mg1..mg9`, a directory whose own README states it is *"SCRATCH/TEST … not
production code, not a live migration"*, and `mg1..mg9` are **not recorded in `schema_migrations`** on the
development appliance (47 recorded = the numbered set only).

*Recommended correction:* promote the IAM-v2 base schema into the repository's authoritative, ordered,
ledger-recorded install path — either as numbered migrations ahead of `0009`, or as an explicit
base-schema step the installer records. Do not delete the scratch directory; it is the provenance.

### B. `public.guest_networks` lacks the composite unique key `mg1` requires  ·  **BLOCKING**

`mg1` anchors `iam_v2.pms_interfaces` to `public.guest_networks (tenant_id, site_id, id)`, and `0002` gives
that table only `PRIMARY KEY (id)`. PostgreSQL refuses a composite foreign key without a matching unique
constraint, so the base schema cannot build. The development appliance does **not** have this constraint
either, and its `iam_v2.pms_interfaces` has **no** foreign key to `guest_networks` — so that appliance was
built from something other than the committed `mg1`, and is not reproducible from it as-is.

*Recommended correction:* add the constraint additively (`UNIQUE (tenant_id, site_id, id)`) as part of the
authoritative install path, and decide deliberately whether the production IAM-v2 schema keeps `mg1`'s
cross-schema anchor or matches the development appliance, which omits it. **These two are not the same
schema, and only one can be the production baseline.**

### C. A clean install still creates the superseded guest-auth surface  ·  **NON-BLOCKING, by design decision**

`0001` and `0006` create `guest_accounts`, `ticket_templates`, `vouchers`, `voucher_batches`, `sessions`,
`auth_otps` and `guests` in `public`. On a fresh appliance where IAM-v2 is the authority from first
operation, none of these hold guest state — but they are created, and they remain **selectable**.

### D. The legacy guest-auth path is selectable by configuration  ·  **NON-BLOCKING, but it defeats "zero legacy"**

The authority switch is **call-level** (`if s.iamv2Cfg.Enabled(...)` inside handlers across six files), not
build-level. With IAM-v2 enabled those branches are not taken, but the code is compiled in and the legacy
path can be re-selected by changing configuration.

*Recommended correction, required before a production appliance can honestly claim zero legacy:* make the
authority non-selectable on a production build — remove the legacy branches, or gate them behind a build tag
so no environment variable or configuration file can select them, and fail closed if legacy is requested.
This is a code change of real size and is proposed for a separate Product-Owner work item, not improvised
here.

---

## 4. Verification evidence

`scripts/clean-install-reconstruction.sh`, run on a workstation against a disposable
`postgres:16-alpine` container that has never seen any appliance:

```
CLEAN_INSTALL_RECONSTRUCTION = PASS
schemas          : iam_v2 public
public tables    : 41
iam_v2 tables    : 74        <-- identical to the development appliance's 74 base tables
rows in iam_v2   : 0
identity tables  : tenants=0 sites=0 appliances=0 operators=0 guest_accounts=0 vouchers=0 ticket_templates=0
```

The IAM-v2 domain built from repository sources matches the accepted appliance domain **exactly** on base
table count, with zero rows and zero identities — once corrections A and B are applied as the ordered path
above does.

---

## 5. Disaster-recovery / fresh-install procedure

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
