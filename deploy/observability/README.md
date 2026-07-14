# StayConnect — observability (Prometheus + Alertmanager + Grafana)

Phase 13 added Prometheus scraping + Grafana dashboards + alert rules.
Phase 15 completes the loop with Alertmanager: fire rules now route to
email (via SendGrid SMTP) and webhooks, grouped + de-duped + inhibit-aware.

## What's here

```
deploy/observability/
├── docker-compose.yml          # prometheus + alertmanager + grafana
├── prometheus/
│   ├── prometheus.yml          # scrape config + alerting target
│   └── alerts.yml              # alert rules
├── alertmanager/
│   ├── alertmanager.yml        # prod template (SendGrid SMTP + webhooks)
│   └── alertmanager-dev.yml    # dev/E2E variant (webhook-only)
├── grafana/
│   ├── provisioning/
│   │   ├── datasources/prometheus.yml
│   │   └── dashboards/stayconnect.yml
│   └── dashboards/
│       ├── overview.json
│       ├── payments.json
│       ├── auth.json
│       └── system.json
└── README.md
```

## Prerequisites

1. The main stack (`deploy/compose/docker-compose.yml`) is running —
   postgres, redis, nats.
2. `ctrlapi` is running on `127.0.0.1:8080` with `/metrics` exposed.
3. `scd` has `SCD_METRICS_ADDR=127.0.0.1:9101` set in `/etc/stayconnect/scd.env`
   so Prometheus can scrape it. (The existing unix-socket /metrics endpoint
   is kept for ad-hoc local curls.)

Add to `/etc/stayconnect/scd.env`:

```
SCD_METRICS_ADDR=127.0.0.1:9101
```

Then `systemctl restart stayconnect-scd` and confirm:

```
curl -s http://127.0.0.1:9101/metrics | head
```

## Running

```sh
docker compose \
    -f deploy/compose/docker-compose.yml \
    -f deploy/observability/docker-compose.yml \
    up -d
```

Access:
- Prometheus: `http://127.0.0.1:9090`
- Grafana:    `http://127.0.0.1:3001` (admin / `${GRAFANA_ADMIN_PASSWORD}`
              — default `admin`; set in `.env` or export before `up`)

Both bind to 127.0.0.1 by default; put them behind a reverse proxy with
real auth before opening remotely.

## Dashboards

| Dashboard  | What it covers                                                 |
|------------|----------------------------------------------------------------|
| Overview   | Active sessions, sessions-started-by-method, nft ops, HTTP rate|
| Payments   | Checkout success rate, webhook outcomes, 24h revenue, per-tenant|
| Auth       | OTP issue/verify, social login + latency, PMS validate + latency|
| System     | Scrape targets, uptimes, Go runtime, ctrlapi p95, reaper rate   |

Every scd-sourced panel is filterable by `$tenant_id` (multi-select).
Payments panels additionally filter by `$currency`.

## Alert rules

The rules in `prometheus/alerts.yml` are grouped:

- **system-health**: `ScrapeTargetDown`, `ApplianceOffline`, `ApplianceNoHeartbeats`
- **ctrlapi-http**: `CtrlapiHigh5xxRate`, `CtrlapiSlow`
- **pms**: `PMSProviderDown`, `PMSValidationFailuresSpike`
- **auth**: `SocialLoginFailureRate`, `OTPVerifyLockoutsSpike`
- **payments**: `StripeSignatureFailures`, `StripeVoucherIssueFailures`, `CheckoutCreateFailureRate`

Wire Alertmanager to Prometheus for routing to Slack/PagerDuty — not
included in this compose file (keep it up to the operator's existing
alerting stack).

## Retention

- Prometheus: 30d on-disk (configurable via `--storage.tsdb.retention.time`).
- Grafana: persists dashboard edits in its own volume, but with
  `disableDeletion: false` + `allowUiUpdates: false` the provisioned
  dashboards are source-of-truth. Edit the JSON files + restart
  `stayconnect-grafana` to ship changes.

## Alertmanager (phase 15)

Prometheus forwards firing alerts to `127.0.0.1:9093`. Alertmanager
groups, de-duplicates, and routes them to email + webhook receivers.

### Routing tree

| Severity   | Grouping          | Destinations                         |
|------------|-------------------|--------------------------------------|
| `critical` | 10s wait / 2m gap | `email-critical` + `webhook-critical`|
| `warning`  | 30s wait / 5m gap | `email-warning`                      |
| `info`     | —                 | `blackhole` (dropped)                |

Inhibit rule: a firing `critical` for a given `(alertname, tenant_id)`
pair suppresses the matching `warning` so incidents hit the inbox once.

### Production setup (SendGrid SMTP)

1. Create a SendGrid API key with `mail.send` scope.
2. Verify the `From` address in SendGrid's Sender Authentication panel.
3. Substitute the template placeholders:

   ```sh
   export SENDGRID_API_KEY="SG.xxxxxxxx"
   export SMTP_FROM="alerts@yourdomain.com"
   export PAGING_WEBHOOK_URL="https://events.pagerduty.com/v2/enqueue?…"
   export OPS_WEBHOOK_URL="https://hooks.slack.com/services/…"
   envsubst < deploy/observability/alertmanager/alertmanager.yml \
       > deploy/observability/alertmanager/alertmanager.resolved.yml
   ```

4. Point the compose service at the resolved file:

   ```sh
   ALERTMANAGER_CONFIG=alertmanager.resolved.yml \
       docker compose -f deploy/observability/docker-compose.yml \
                      up -d stayconnect-alertmanager
   ```

5. Reload Prometheus so the new alerting target is picked up:

   ```sh
   curl -XPOST http://127.0.0.1:9090/-/reload
   ```

### Silences

Alertmanager ships a small UI at `http://127.0.0.1:9093`. To silence a
noisy alert during a planned maintenance window:

- UI: Silences → New Silence → match on `alertname=PMSProviderDown`,
  `tenant_id=<uuid>`, set duration, click Create.
- CLI: `amtool silence add alertname=PMSProviderDown tenant_id=<uuid> --duration=2h --author="<you>" --comment="planned maint"`
- API: `POST /api/v2/silences` with the silence JSON.

Silences persist across Alertmanager restarts via the
`stayconnect-alertmanager-data` volume.

### Receivers you can plug in

The template ships three receiver classes (email + two webhooks). Some
ready-to-use webhook URL shapes:

| Target          | URL shape                                                       |
|-----------------|-----------------------------------------------------------------|
| PagerDuty v2    | `https://events.pagerduty.com/v2/enqueue?integration_key=…`     |
| Slack           | `https://hooks.slack.com/services/…` (incoming webhook)         |
| OpsGenie        | `https://api.opsgenie.com/v1/json/alertmanager?apiKey=…`        |
| Custom HTTP     | anything that accepts `POST` with Alertmanager's JSON body      |

Alertmanager's native webhook payload format is documented at
https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
— Slack and PagerDuty consume it directly via their generic webhook
paths.

## Known limitations (things to add later)

- **Scaling out** — single Prometheus + single Alertmanager. For HA
  Alertmanager, add two more replicas + enable `--cluster.listen-address`
  + put Prometheus's `alertmanagers` list behind all three.
- **Long-term TSDB retention** — 30d on-disk; federate to Thanos /
  Mimir for multi-month storage.
- **Postgres / Redis / NATS exporters** — not deployed here. Add
  `postgres_exporter`, `redis_exporter`, `nats_exporter` to this
  compose file if you need DB/cache/bus-internal metrics.
- **scd /metrics TCP binding is plaintext** — fine for localhost scrapes;
  put it behind an internal TLS proxy if Prometheus runs elsewhere.
