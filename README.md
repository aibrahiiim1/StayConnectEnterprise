# StayConnect Enterprise

Linux-based inline gateway appliance + cloud control plane — an enterprise alternative to IACBOX.

## Layout

| Path            | Purpose                                                              |
|-----------------|----------------------------------------------------------------------|
| `control-plane` | Go API, DB migrations, admin-facing services (cloud or on-prem)      |
| `data-plane`    | Gateway daemons that run on the appliance (scd, acctd, portald, policyd) |
| `hotel-admin`   | OneGate Hotel Admin — the Next.js console served by each appliance   |
| `cloud-admin`   | OneGate Central — the vendor's Next.js licensing console              |
| `design-system` | The OneGate design system: canonical tokens and the component and pattern guide shared by all three front-ends |
| `web-admin`     | The pre-split admin UI (legacy, out of scope for the OneGate front-ends) |
| `deploy`        | docker-compose stacks, nftables templates, appliance image pipeline  |
| `docs`          | Architecture, data model, API specs                                  |
| `scripts`       | Dev helpers                                                          |

## Front-ends

The product's user-facing name is **OneGate**. Its three front-ends — the guest portal (served by
`data-plane/cmd/portald`), Hotel Admin (`hotel-admin`) and Central (`cloud-admin`) — share one design
system, described in [`design-system/README.md`](design-system/README.md). Token values live only in
`design-system/tokens.css`; run `node tools/sync-design-tokens.mjs` after changing them. Operator
documentation is in [`docs/user-guide/`](docs/user-guide/README.md).

## Phase 0 quickstart

```bash
make infra-up          # Postgres+Timescale, Redis, NATS
make migrate           # apply SQL migrations
make ctrlapi-run       # start control-plane API on :8080
curl localhost:8080/healthz
```
