# StayConnect Enterprise

Linux-based inline gateway appliance + cloud control plane — an enterprise alternative to IACBOX.

## Layout

| Path            | Purpose                                                              |
|-----------------|----------------------------------------------------------------------|
| `control-plane` | Go API, DB migrations, admin-facing services (cloud or on-prem)      |
| `data-plane`    | Gateway daemons that run on the appliance (scd, acctd, portald, policyd) |
| `hotel-admin`   | OneGate Admin Console (formerly Hotel Admin) — the Next.js console served by each appliance   |
| `cloud-admin`   | OneGate Central — the vendor's Next.js console for licensing, activation and fleet status ([`docs/CENTRAL_CONTROL_PLANE.md`](docs/CENTRAL_CONTROL_PLANE.md)) |
| `design-system` | The OneGate design system: canonical tokens and the component and pattern guide shared by all three front-ends |
| `deploy`        | docker-compose stacks, nftables templates, appliance image pipeline  |
| `docs`          | Architecture, data model, API specs                                  |
| `scripts`       | Dev helpers                                                          |

## Front-ends

The product's user-facing name is **OneGate**. Its three front-ends — the Client Portal (formerly Guest Portal; served by
`data-plane/cmd/portald`), the Admin Console (`hotel-admin`) and Central (`cloud-admin`) — share one design
system, described in [`design-system/README.md`](design-system/README.md). Token values live only in
`design-system/tokens.css`; run `node tools/sync-design-tokens.mjs` after changing them. Operator
documentation is in [`docs/user-guide/`](docs/user-guide/README.md).

## Phase 0 quickstart

```bash
make infra-up          # Postgres+Timescale, Redis
make migrate           # apply SQL migrations
make ctrlapi-run       # start control-plane API on :8080
curl localhost:8080/healthz
```
