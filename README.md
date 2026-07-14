# StayConnect Enterprise

Linux-based inline gateway appliance + cloud control plane — an enterprise alternative to IACBOX.

## Layout

| Path            | Purpose                                                              |
|-----------------|----------------------------------------------------------------------|
| `control-plane` | Go API, DB migrations, admin-facing services (cloud or on-prem)      |
| `data-plane`    | Gateway daemons that run on the appliance (scd, acctd, portald, policyd) |
| `web-admin`     | Next.js admin UI                                                     |
| `deploy`        | docker-compose stacks, nftables templates, appliance image pipeline  |
| `docs`          | Architecture, data model, API specs                                  |
| `scripts`       | Dev helpers                                                          |

## Phase 0 quickstart

```bash
make infra-up          # Postgres+Timescale, Redis, NATS
make migrate           # apply SQL migrations
make ctrlapi-run       # start control-plane API on :8080
curl localhost:8080/healthz
```
