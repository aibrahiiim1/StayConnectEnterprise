# Deployment — Cloud (OneGate Central)

> The runbook for OneGate Central, the vendor half: licensing, activation and fleet status
> ([CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md)). Central runs on its own host; appliances reach it
> by name (`sc-central.echofusion.com`, CLAUDE.md §0D) and only ever for licensing (CLAUDE.md §0E).
> Appliance counterpart: [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md).
>
> **Everything here is a script in `deploy/scripts/`.** A Central host is built from a release and nothing
> else: no hand-written compose file, no unit typed on the server, no key minted by hand. If a step below is
> not a command, it is a decision (DNS, key custody), and it says so.

| Script | Runs on | Does |
|---|---|---|
| `central-build.sh` | workstation / CI (docker) | builds a **release**: ctrlapi + console bundle + this tooling, from one clean commit |
| `central-install.sh --mode new` | a clean host | a **brand-new** Central with a brand-new identity |
| `central-install.sh --mode restore` | a clean host | **becomes** an existing Central, from its export bundle |
| `central-export.sh [--final]` | the existing Central | encrypted bundle: every key, CA and certificate + the database |
| `central-deploy.sh` | an installed Central | upgrade to a release; `rollback`, `smoke`, `status` |
| `central-cleanup-obsolete.sh` | the first Central host | removes the retired NATS/telemetry/remote-control state |
| `central-migrate.sh` | an installed Central | schema ledger: `status`, `up`, `verify`, `adopt` |
| `central-preflight.sh` | an installed Central | read-only: *can this host activate an appliance?* |
| `central-mint-tls.sh` | an installed Central | the :443 certificate from the internal TLS CA (`--init-ca` once, on a new Central) |
| `central-firewall.sh` | an installed Central | ufw: 22, 443, 9443 — nothing else |
| `vendor-signing-key.sh` | an installed Central | the vendor key's life: `init` (once, ever), `show`, `backup`, `restore`, `export-public` |

## 1. Components

```
                 Internet
                    │ :443                                   │ :9443 (client certificate required)
              ┌─────▼─────┐                                  │
              │   Caddy   │  TLS, security headers            │
              └──┬─────┬──┘                                  │
   everything else  /v1/* /cloud/* /healthz /readyz          │
              │           │                                  │
     ┌────────▼───┐   ┌───▼──────────────────────────────────▼─┐   ┌────────────────────┐
     │ cloud-admin│   │ ctrlapi  127.0.0.1:8080 /v1/auth,       │──▶│ sc-central-pg      │
     │ (Next.js,  │   │          /cloud/v1, appliance protocol  │   │ TimescaleDB 2.16.1 │
     │  :3000)    │   │          :9443 appliance mutual TLS     │   └────────────────────┘
     └────────────┘   └─────────────────────────────────────────┘──▶ sc-central-redis (sessions)
     appliances dial in over HTTPS only (outbound from the hotel) — no message bus, no telemetry
```

| Component | What runs it |
|---|---|
| **ctrlapi** | `stayconnect-ctrlapi.service` → `/opt/stayconnect/bin/ctrlapi serve`, root (it writes the mTLS certificate and, on a new Central, the CA under `/etc/stayconnect`), sandboxed; env `/etc/stayconnect/ctrlapi.env` (from [`deploy/env/ctrlapi.env.example`](../deploy/env/ctrlapi.env.example)) then `/etc/stayconnect/central-endpoint.env`. Stateless; one replica. |
| **cloud-admin** | `stayconnect-cloud-admin.service` → `node server.js` in `/opt/stayconnect/cloud-admin-current`, user `stayconnect`, `127.0.0.1:3000`, sandboxed, 512 MB cap. Node ≥ 18.17 at `/usr/bin/node` (built with 20). |
| **Postgres + Redis** | [`deploy/compose/central-infra.yml`](../deploy/compose/central-infra.yml), project `stayconnect-central`: `sc-central-pg` (`timescale/timescaledb:2.16.1-pg16`) and `sc-central-redis` (`redis:7-alpine`, password, AOF), both on 127.0.0.1 only, passwords from `/opt/stayconnect/central/secrets/{db_password,redis_password}`. **No NATS.** |
| **Caddy** | `stayconnect-caddy.service` (from `deploy/caddy/stayconnect-caddy.central.service`), user `caddy`, config rendered from the [`Caddyfile.central`](../deploy/caddy/Caddyfile.central) template. The apt package's own `caddy.service` is masked. |
| **Database backup** | `stayconnect-central-backup.timer` (daily, 02:15) runs `central-backup.sh`: `pg_dump -Fc` to `/opt/stayconnect/backups/db/central-<stamp>-scheduled.dump` (0600), proven readable before it counts. The installer runs it once immediately. **Local only** — see §9. |
| **Backup retention** | `stayconnect-backup-cleanup.timer` (daily) and every backup run apply `/etc/stayconnect/backup-retention.conf` (`KEEP_DB` dumps, newest never deleted, `backup-retention.pins` never deleted; also rollback binaries and console releases) and write `/opt/stayconnect/backup-retention-status.json` — what *System → Backup health* (`GET /cloud/v1/backup-health`) shows. |
| **Observability** | optional, separate: [`deploy/observability`](../deploy/observability) (Prometheus scrapes ctrlapi's loopback-only `/metrics`). Not installed by `central-install.sh`. |

## 2. Network exposure

| Port | Exposure |
|---|---|
| 443 (Caddy) | public — console, `/v1/auth`, `/cloud/v1`, the appliance HTTPS protocol |
| **9443 (ctrlapi)** | **public, client-certificate-gated** — the appliance mutual-TLS listener. `RequireAndVerifyClientCert` against the appliance CA rejects anything without a certificate that CA signed, during the handshake. It must be reachable from wherever hotels are. |
| 22 | management |
| 5432 · 6379 · 8080 · 3000 | **never public** — bound to 127.0.0.1 (Docker publishes the two containers on 127.0.0.1 only, so ufw is not what protects them) |

`central-firewall.sh --enable` applies exactly this (and also allows whatever port `sshd` really listens on, so
enabling ufw cannot lock the operator out). It reads the mTLS port from `deploy/config/central-endpoint.env`, so
it cannot drift from what appliances are told to dial. Central initiates **no** connection toward hotels.

## 3. Names, TLS and the host layout

- **`sc-central.echofusion.com`** is the only Central address an appliance ever learns. It is defined once, in
  [`deploy/config/central-endpoint.env`](../deploy/config/central-endpoint.env) (`CENTRAL_BASE`,
  `CENTRAL_MTLS_BASE`, `CTRLAPI_APPLIANCE_BASE`); the installer, ctrlapi and appliance provisioning all read it.
  **Moving Central is a DNS change** — no appliance knows an IP.
- The console may have its own name(s) (`--admin-name`), so it can later sit behind MFA/VPN without that
  becoming a dependency of a hotel's connectivity. Those names become `CTRLAPI_ALLOW_ORIGINS`.
- **:443 certificate**, two modes (`--tls`):
  - `internal` (default, what the first Central uses): Central's own TLS CA in `/opt/stayconnect/central/tls`
    issues the certificate (`central-mint-tls.sh`); appliances trust that CA (`install-central-trust.sh`).
  - `acme`: Caddy obtains a public certificate. Needs public DNS pointing at the host and :80/:443 reachable.
- **:9443 certificate**: issued by ctrlapi itself from the appliance intermediate CA, for the appliance-facing
  name + `CTRLAPI_MTLS_SANS` + 127.0.0.1, rotated before expiry.

```
/opt/stayconnect/bin/ctrlapi                  (+ ctrlapi.bak-<stamp> rollback copies)
/opt/stayconnect/cloud-admin-current          -> releases/cloud-admin/<stamp>      (+ .previous)
/opt/stayconnect/releases/central/<release>/  tooling of each installed release (deploy/, migrations, RELEASE.json)
/opt/stayconnect/central/tooling              -> the current one; run operational scripts from here
/opt/stayconnect/central/compose/central-infra.yml
/opt/stayconnect/central/secrets/             db_password, redis_password               (0700 / 0600)
/opt/stayconnect/central/tls/                 internal TLS CA + :443 certificate          (0700)
/opt/stayconnect/central/appliance-trust/     PUBLIC material for appliance provisioning + FINGERPRINTS.txt
/opt/stayconnect/central/DEPLOYED.json        what is running, read back from the installed files
/etc/stayconnect/ctrlapi.env                  (0600)   central-endpoint.env   central-install.env
/etc/stayconnect/{vendor-license,assignment-signing,assignment-registry-root}.{key,pub}
/etc/stayconnect/pki/                         root-ca.crt, intermediate-ca.{crt,key}, server-mtls.{crt,key}, ca-bundle.crt
/etc/stayconnect/pki-offline/root-ca.key      only until it is taken to cold storage
/etc/caddy/Caddyfile   /etc/caddy/tls/server.{crt,key}
/opt/stayconnect/backups/db/central-<stamp>-{scheduled|pre-<sha12>}.dump   daily + pre-deploy dumps (0700 dir)
/opt/stayconnect/backup-retention-status.json  written by backup cleanup; read by the Backup health page
```

## 4. Build a release

On a workstation or CI with docker, from a **committed** tree:

```sh
bash deploy/scripts/central-build.sh            # -> dist/central/onegate-central-<sha12>.tar.gz (+ .sha256)
```

It refuses a dirty tree and builds from a fresh clone of that one commit: ctrlapi in `golang:1.25.12`
(`-trimpath`, `CGO_ENABLED=0`, no ldflags) with its embedded `vcs.revision` read back and required to equal the
commit; the console with `npm ci` + `next build` in `node:20.18.0-bookworm-slim`, assembled as a Next standalone
bundle (`.next/standalone` + `.next/static`) with `cloud-admin-release.json`. `RELEASE.json` records commit,
toolchains, the ctrlapi sha256 and the console `BUILD_ID`; `SHA256SUMS` covers every file. Install and deploy
refuse a release that does not match its checksums or whose binary does not embed its commit.

## 5. A brand-new Central (`--mode new`)

Use this **only** for a Central no appliance trusts yet. It creates a new vendor key, assignment key, registry
root key, appliance Root/Intermediate CA and internal TLS CA — appliances pinned to another Central will not
accept anything it signs.

Host: Ubuntu 22.04/24.04 or Debian 12, systemd, root. Prerequisites: docker + compose v2, caddy, Node ≥ 18.17
at `/usr/bin/node`, openssl, python3, curl, iproute2, ufw — or pass `--install-prereqs` (apt: docker.io,
docker-compose-v2, Caddy's apt repo, NodeSource 20).

```sh
tar -xzf onegate-central-<sha>.tar.gz
export CENTRAL_ADMIN_PASSWORD='…'                 # or be prompted; never on the command line
sudo -E bash onegate-central-<sha>/deploy/scripts/central-install.sh --mode new \
     --admin-email ops@example.com [--admin-name admin.example.com] [--extra-names 203.0.113.10] \
     [--tls internal|acme --acme-email ops@example.com] [--install-prereqs] [--dry-run]
```

What it does, in order (each step converges; re-running is safe):

1. verifies the release (checksums, embedded commit, console manifest);
2. checks prerequisites; refuses a host that already runs a Central database it did not create;
3. creates user `stayconnect`, the directories above, and the DB/Redis passwords (only if absent);
4. stores the release tooling, installs ctrlapi;
5. installs the compose file, starts Postgres + Redis, waits until they answer queries;
6. **identity**: `vendor-signing-key.sh init`, `ctrlapi gen-assignment-key`, `ctrlapi gen-registry-key`,
   `central-mint-tls.sh --init-ca` — each only if absent; refuses outright if the database already holds
   appliances or licences but a key is missing (that is a move, not a new Central);
7. `central-migrate.sh up` + `verify`;
8. renders `ctrlapi.env` (passwords, origins, every path explicit), installs `central-endpoint.env`, records the
   install settings in `/etc/stayconnect/central-install.env`;
9. installs the units, masks the stock `caddy.service`, enables the backup-retention and daily database-backup timers;
10. installs the console release;
11. starts ctrlapi — its first start creates the appliance CA and moves the Root CA key to
    `/etc/stayconnect/pki-offline` — then writes `pki/ca-bundle.crt` (intermediate + root) and restarts it so
    offline activation packages carry it; checks the assignment key is registered;
12. seeds the first platform admin (skipped when one exists);
13. starts the console, issues the :443 certificate, renders + validates the Caddyfile, starts Caddy, applies
    the firewall; takes the first database backup through the backup unit (so *Backup health* reports from
    minute one);
14. publishes the **public** trust material to `/opt/stayconnect/central/appliance-trust` (vendor and registry
    public keys, appliance Root CA, Central TLS CA, `FINGERPRINTS.txt`), writes `DEPLOYED.json`, then runs
    `central-deploy.sh smoke` and `central-preflight.sh` — the install fails unless both pass.

Then, before anything else — **key custody** (§9):

1. `central-export.sh --out /secure/media` (encrypted escrow of every key, CA and the database);
2. move `/etc/stayconnect/pki-offline/root-ca.key` to cold storage and delete it from the host;
3. publish `FINGERPRINTS.txt` out of band; put `vendor-license.pub` and `assignment-registry-root.pub` into
   `deploy/pki/` for appliance provisioning ([deploy/pki/README.md](../deploy/pki/README.md) — never committed);
   appliances install `central-tls-ca.crt` with `install-central-trust.sh`.

## 6. Move an existing Central to a new host (`--mode restore`)

Everything appliances trust is **carried**, never regenerated: the vendor licence key, the assignment signing
key, the registry root key, the appliance Root CA certificate and intermediate CA (and the Root CA key if it is
still on the host), the :9443 certificate, the internal TLS CA and :443 certificate, and the database with its
licence and assignment version history. Regenerating any of them breaks every deployed appliance; changing the
Root CA is a Product-Owner decision, not a deployment step.

1. **Rehearse** (optional, recommended): on the old host `central-export.sh --out /root/central-export` takes a
   consistent snapshot (ctrlapi stopped for the seconds of the dump, then restarted) — restore it on the new host
   exactly as below, check, then rebuild the new host clean.
2. **Build the new host** to the point of prerequisites (or let `--install-prereqs` do it). Do not run
   `--mode new` on it.
3. **Final export on the old host** — from here on the old host must not issue anything:
   ```sh
   export CENTRAL_BUNDLE_PASSPHRASE='…'           # or be prompted
   sudo -E bash deploy/scripts/central-export.sh --final --out /root/central-export
   ```
   It first proves the old host's trust material belongs together, stops **and disables** ctrlapi and the
   console, dumps the database (`pg_dump -Fc`) with per-table row counts from the same snapshot, and writes
   `central-export-<host>-<stamp>.tar.gz.enc` (AES-256, PBKDF2 600k; `--no-encrypt` only onto encrypted media),
   verified to decrypt. **The bundle holds every private key of the vendor.** Move it over an encrypted channel,
   keep the passphrase separate, destroy all copies once the move is verified.
   Appliances keep serving guests throughout; they retry Central until DNS points at the new host.
4. **Restore on the new host**:
   ```sh
   export CENTRAL_BUNDLE_PASSPHRASE='…'
   sudo -E bash onegate-central-<sha>/deploy/scripts/central-install.sh --mode restore \
        --bundle /root/central-export-<host>-<stamp>.tar.gz.enc [--extra-names <new-ip>] [--yes]
   ```
   Beyond the new-install steps it: checks every carried key/certificate belongs with the others (each `.pub` is
   its key's half; vendor ≠ assignment ≠ registry key; intermediate chains to the Root and its key matches; :443
   and :9443 certificates chain to their CAs); installs them without ever overwriting different content;
   restores the dump **into an empty database only** (`timescaledb_pre_restore` → `pg_restore` →
   `timescaledb_post_restore`, same TimescaleDB version required) and requires identical row counts in every
   table and the same hypertables; cross-checks the database against the keys (the carried assignment key is in
   `assignment_signing_keys`, `appliance_ca_versions` holds the carried Root and intermediate); **refuses to
   start ctrlapi unless the CA files are present** (a ctrlapi started without them silently mints a new CA), and
   after start proves the CA files are unchanged. Operational settings (`CTRLAPI_MTLS_SANS`, log level, TTLs) are
   carried from the old `ctrlapi.env`; passwords and paths are the new host's own. Operators come with the
   database — no admin is seeded.
5. **Compare identities**: `FINGERPRINTS.txt` on the new host must equal the old host's (vendor, assignment,
   registry key ids; Root CA and TLS CA fingerprints). Sign in with an existing operator account.
6. **Switch DNS** for `sc-central.echofusion.com` to the new host. No appliance changes. Watch *Overview* for
   appliances turning `connected`.
7. **Retire the old host**: leave it stopped (it is disabled already). Once the new host has issued anything,
   the old one must never run again: two Centrals with one identity produce licence versions that appliances
   refuse. **Abandoning a move** before DNS switches: `systemctl enable --now stayconnect-ctrlapi
   stayconnect-cloud-admin` on the old host, and wipe the new one — nothing was issued there.

## 7. Deploy a release, roll back

```sh
tar -xzf onegate-central-<sha>.tar.gz
sudo bash onegate-central-<sha>/deploy/scripts/central-deploy.sh            # deploy
sudo bash /opt/stayconnect/central/tooling/deploy/scripts/central-deploy.sh status
sudo bash /opt/stayconnect/central/tooling/deploy/scripts/central-deploy.sh smoke
sudo bash /opt/stayconnect/central/tooling/deploy/scripts/central-deploy.sh rollback
```

`deploy`: verify the release → `central-backup.sh --reason pre-<sha12>` (`/opt/stayconnect/backups/db/central-<stamp>-pre-<sha12>.dump`,
same place, pattern and retention as the daily backup, checked readable) → back up the binary and `ctrlapi.env` → stop ctrlapi → `central-migrate.sh up` + `verify` with the new
release's migrations → install binary + changed units → start (`--no-block`), poll `/readyz`, require the running
binary to embed the release commit → re-render the Caddyfile from the template and restart Caddy only if it
changed (validated first) → new console release dir, atomic `current`/`previous` switch, wait until it serves the
new `BUILD_ID` → **smoke** → `DEPLOYED.json` → retention. A failure after the migration rolls ctrlapi and the
console back to what was running. A changed `central-infra.yml` is reported, never applied automatically (it
would recreate the database container). Appliances are unaffected by any of this.

**Smoke** (also run by the installer) checks through Caddy **by the configured name** with
`--resolve <name>:443:127.0.0.1` — Caddy answers a Host it has no site for with an empty 200, so a probe of
`https://127.0.0.1/` proves nothing: loopback and proxied `/readyz`, console `/login`, the deployed console
`BUILD_ID`'s assets, `/metrics` **not** reachable through Caddy (it is loopback-only), `:9443` listening and its
certificate verifying for the appliance-facing name against `ca-bundle.crt`.

**Rolling back the database** is deliberate, never automatic (migrations are forward-only; each migration is its
own transaction, so a failed one leaves nothing behind, but earlier ones in the run stay applied):

```sh
systemctl stop stayconnect-ctrlapi stayconnect-cloud-admin
docker exec sc-central-pg psql -U stayconnect -d postgres -c "DROP DATABASE stayconnect WITH (FORCE)"
docker exec sc-central-pg psql -U stayconnect -d postgres -c "CREATE DATABASE stayconnect"
docker exec sc-central-pg psql -U stayconnect -d stayconnect -c "CREATE EXTENSION IF NOT EXISTS timescaledb"
docker exec sc-central-pg psql -U stayconnect -d stayconnect -c "SELECT timescaledb_pre_restore()"
docker exec -i sc-central-pg pg_restore -U stayconnect -d stayconnect < /opt/stayconnect/backups/db/central-<stamp>-pre-<sha12>.dump
docker exec sc-central-pg psql -U stayconnect -d stayconnect -c "SELECT timescaledb_post_restore()"
docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc "SELECT count(*) FROM timescaledb_information.hypertables"
bash /opt/stayconnect/central/tooling/deploy/scripts/central-deploy.sh rollback     # the binary that matches
```

Never `pg_restore --clean` a TimescaleDB database: it drops the extension and every hypertable with it.

## 8. The move from the first Central host (150.0.0.252) — done 2026-09-27

The first Central predated this tooling. It was moved, as in §6, onto `172.21.96.196`, which the installer built:

1. `central-cleanup-obsolete.sh --apply --remove-legacy-bundle` on the old host (NATS state, command/update keys,
   the legacy trust file; `pki/ca-bundle.crt` in place of `nats-ca-bundle.crt`).
2. `central-export.sh --dry-run` refused the first time: `assignment-signing.pub` was the public half of the key
   revoked on 2026-07-12, left behind by that rotation. It was replaced by the active key's public half (the stale
   file is kept as `assignment-signing.pub.revoked-c63f848bf5ded3f6`) and the export then passed every
   consistency check.
3. `central-export.sh --final`, then `central-install.sh --mode restore --bundle … --extra-names 172.21.96.196
   --install-prereqs`: smoke PASS, preflight READY, vendor key `2fbdfcbde209ebb5`, assignment key
   `027a2c97f6c8fcdb`, registry root `84655767f9834fa2` unchanged.
4. The old host's Caddy was stopped and disabled as well (`--final` now does this itself): while it answered 502,
   PRE-LIVE kept using its keep-alive connection to it.
5. PRE-LIVE resolves `sc-central.echofusion.com` through the gateway DNS (FortiGate `172.21.60.1`). Until that record
   is changed to `172.21.96.196` an interim `/etc/hosts` line on the appliance points the name at the new host.

**Still to do outside this repository:** change the DNS record on the FortiGate and then delete the appliance's
interim `/etc/hosts` line; move `/opt/stayconnect/ca-ceremony-backup/root-ca.key.enc` from the old host into
offline custody ([CA_CEREMONY_RUNBOOK.md](CA_CEREMONY_RUNBOOK.md)); only then decommission `150.0.0.252`.
Rolling back to the old host is possible only until the new one has issued anything (it had issued licence
version 3 at verification time) — after that, move again with §6 instead.

## 9. Backups and key custody

| What | Where it lives | Backup |
|---|---|---|
| Vendor licence key | `/etc/stayconnect/vendor-license.key` | in every export bundle; also `vendor-signing-key.sh backup <file>` (encrypted). Appliances pin its public half — **never regenerate** (`rotate --force` is a fleet-wide trust change). |
| Assignment signing key, registry root key | `/etc/stayconnect/assignment-*.key` | export bundle. Key state changes: `ctrlapi assignment-key verify-only|revoke --key-id … --reason …`. |
| Appliance Root CA key | cold storage (after first install) | offline; the export carries it only if it is still on the host |
| Intermediate CA, :9443 cert, TLS CA, :443 cert | `/etc/stayconnect/pki`, `/opt/stayconnect/central/tls` | export bundle |
| Database | `sc-central-pg` volume | **daily local dump** (`stayconnect-central-backup.timer` → `/opt/stayconnect/backups/db`, retained per `KEEP_DB`), a dump before every deploy, `central-export.sh` for escrow. **Off-host copies are the operator's responsibility** — the destination is a Product-Owner decision and nothing here ships data off the host. Copy `/opt/stayconnect/backups/db/` (it holds the whole licence database) to that destination, or schedule `central-export.sh --out <off-host mount>` (encrypted; also carries the keys) with `CENTRAL_BUNDLE_PASSPHRASE` from a root-only file. A backup on the same disk does not survive the host. |

Restoring a whole Central from an export bundle is §6 on a clean host. Restoring only the database is §7.

## 10. Operational duties

- **Activation and licences**: a platform admin in the console; renew before `valid_until` (appliances
  re-fetch on their own; the grace period covers late renewals —
  [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md)).
- **Fleet watch**: the console's *Overview* (waiting, licences expiring/in grace/expired/suspended, appliances
  offline, open security alerts, unconfirmed retirements); *System → Backup health* for this host's retention.
- **After any change on the host**: `central-preflight.sh` (read-only) and `central-deploy.sh smoke`.

## 11. Failure modes and their blast radius

| Failure | Effect on hotels | Effect on cloud users | Recovery |
|---|---|---|---|
| ctrlapi down | none (licence fetch retries with backoff) | console unusable | `central-deploy.sh rollback`, or redeploy — stateless |
| Postgres down | none | everything Central down | restart the container; restore §7 |
| Redis down | none | operators signed out | restart — sessions are re-creatable |
| Host lost | none until renewals are due | cannot issue licences | §6 from the latest export bundle |
| Vendor key lost | none until renewals are due | cannot issue licences | restore from an export bundle / escrow — there is no other way that keeps the fleet |

The recurring answer in column two — "none" — is the acceptance test: no Central failure may reach a guest.

## 12. Pilot topology (HISTORICAL)

The pilot ran Central **and** one appliance on a single VM. Superseded: Central has its own host (CLAUDE.md §0D).
