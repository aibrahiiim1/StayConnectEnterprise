# OneGate by Semantics

A Linux gateway appliance that gives a property's clients managed Internet access (captive portal, vouchers, accounts,
room sign-in through the PMS, paid packages), and **OneGate Central**, the vendor's server for appliance identity,
activation and licensing. Each appliance runs its site on its own; Central never holds guest data
([`docs/CENTRAL_CONTROL_PLANE.md`](docs/CENTRAL_CONTROL_PLANE.md)).

## Layout

| Path            | Purpose |
|-----------------|---------|
| `data-plane`    | The appliance's Go services: `scd` (sessions, identity, licence, Central link), `edged` (Admin Console API), `portald` (Client Portal), `netd` (client networks, firewall, DHCP), `acctd` (accounting and shaping), `pmsd` (PMS connector), `keybootstrap`, `svc-run` |
| `control-plane` | OneGate Central: `ctrlapi` and its database migrations |
| `hotel-admin`   | The Admin Console (Next.js), served by each appliance |
| `cloud-admin`   | The Central console (Next.js) |
| `license`       | The signed licence and module model shared by both sides |
| `design-system` | Tokens and the component guide shared by the three front-ends |
| `deploy`        | Release builders, installers, systemd units, compose files, Gate-P database grants, env templates |
| `governance`    | The machine-readable project state, decision register and transition receipts |
| `tools`         | Governance validators, the change-manifest generator, pack builder |
| `docs`          | Manuals, architecture, runbooks — start at [`docs/README.md`](docs/README.md) |

## Build and install

An appliance and a Central are each installed from **one release** built from one commit:

```bash
bash deploy/scripts/appliance-build.sh     # → dist/appliance/onegate-appliance-<sha12>.tar.gz
bash deploy/scripts/central-build.sh       # → dist/central/onegate-central-<sha12>.tar.gz
```

then `deploy/scripts/appliance-install.sh` (fresh install, re-run, `--upgrade`) and `deploy/scripts/central-install.sh` /
`central-deploy.sh`. The whole procedure, with verification, recovery and upgrades, is the
[installation manual](docs/runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md); the system as built is the
[system handbook](docs/architecture/ONEGATE_SYSTEM_HANDBOOK.md).

## Front-ends

The Client Portal (served by `data-plane/cmd/portald`), the Admin Console (`hotel-admin`) and Central (`cloud-admin`) share one
design system ([`design-system/README.md`](design-system/README.md)). Token values live only in `design-system/tokens.css`; run
`node tools/sync-design-tokens.mjs` after changing them. Operator documentation is in [`docs/user-guide/`](docs/user-guide/README.md).

## Working on it

How work is delivered (branches, tests, PRE-LIVE, the Product Owner's merge approval) is in [`CLAUDE.md`](CLAUDE.md) and
[`docs/PO_LED_DELIVERY_MODEL.md`](docs/PO_LED_DELIVERY_MODEL.md). For local development, `make infra-up` and `make migrate` start a
database and apply Central's migrations; `make ctrlapi-run` serves Central's API on `:8080`.
