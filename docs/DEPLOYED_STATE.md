# Deployed state — activation, licensing, Central FQDN, Kea health

Current as of **2026-08-21**. Status: **PRE-LIVE**. No Go-Live, no guest traffic, no LAN/guest VLAN/DHCP
configuration, no PMS or payment traffic.

## What is deployed

| | Host | Commit |
|---|---|---|
| Central source + tooling | `sc-central` (150.0.0.252) | `4be7adfec80752d938afca6974c13aa4a872692b` |
| ctrlapi binary | same | `5477a2b` (control-plane source unchanged since) |
| cloud-admin bundle | same | `5477a2b` (cloud-admin source unchanged since) |
| Appliance source + tooling | `sce` (172.21.60.25) | `4be7adfec80752d938afca6974c13aa4a872692b` |
| scd / edged / netd / hotel-admin | same | scd `469697a`, edged+netd `97a9b9d`, hotel-admin `5477a2b` |

Branch `post-roadmap/activation-fqdn-kea-deploy`, PR #23, **unmerged** by instruction.

## Central endpoint

`https://sc-central.echofusion.com`, defined once in
[`deploy/config/central-endpoint.env`](../deploy/config/central-endpoint.env) and installed to
`/etc/stayconnect/central-endpoint.env` by `install-central-endpoint.sh`. The ctrlapi unit reads it *after*
`ctrlapi.env`, so the fleet-wide value wins over anything hand-edited on a host.

No runtime component contains a hard-coded Central address any more. The mTLS certificate's names come from
that configuration (`sc-central.echofusion.com`, `150.0.0.252`, `127.0.0.1`), and ctrlapi re-issues a stored
certificate that does not cover them all. Central's public TLS certificate is issued by
`central-mint-tls.sh` from the same source and now carries the FQDN alongside the names already in use.

**The appliance still dials `https://150.0.0.252`.** That is deliberate: see the DNS blocker below.
The mTLS base is `:9443` — the product's real port. It read `:8443` in configuration and provisioning, which
nothing has ever listened on.

## Trust material

Three separate roots, none interchangeable:

| Root | Where | Fingerprint |
|---|---|---|
| Vendor signing (licences, offline packages) | private on Central only; public pinned on the appliance | `L738veIJ67U` / licence key_id `2fbdfcbde209ebb5` |
| Assignment registry root (which keys may sign an assignment) | public pinned on the appliance | `hGVXZ_mDT6I` |
| Central CA (TLS) | appliance system trust store | `O=StayConnect, CN=StayConnect Internal CA` |

The vendor identity **already existed** and was preserved, not rotated — minting a new one would have
invalidated everything already signed by it. Its encrypted escrow lives at
`/opt/stayconnect/central/secrets/vendor-escrow/` and has been proven restorable (a restore into a throwaway
location reproduced the same fingerprint). The passphrase is at `/root/.vendor-escrow-passphrase`, root-only.

> **Escrow is not finished.** Both the encrypted file and its passphrase are on the host they protect. Move
> the file off-host, and keep the passphrase somewhere separate from it.

## Database

Ledgered at `schema_migrations`, 44 rows: 43 adopted (schema verified object-by-object through 0043 before
adoption) and `0044_offline_activation_requests` applied by `central-migrate.sh`. A verified `pg_dump -Fc`
was taken first at `/opt/stayconnect/backups/pre-0044-20260821T135204Z/` and test-restored.

## Kea before LAN configuration

`waiting`, with the reason, and boot convergence treats it as satisfied. Before this change the same
appliance reported `kea = failed` with `converged=false, alert_open=true, pending={kea}` — permanently.
The Kea unit remains `inactive` and `disabled`; nothing is started to make health green. Once a guest
network is applied and confirmed, Kea is checked normally and a real failure is reported as a failure.

## Next step

Manual Product-Owner **Online Activation**: Pending appliance → Customer / Site / licence → Activate. The
appliance is registered, its assignment agent is running, and it is waiting for exactly that.

## Genuine remaining blocker

**`sc-central.echofusion.com` does not exist in DNS.** `echofusion.com` is hosted at Spaceship;
`sc-central` returns NXDOMAIN from public resolvers and from the internal resolver at 150.0.0.11. The name
resolves on the appliance *only* through an `/etc/hosts` line, which is a stopgap on one machine and is not
the product's dependency.

Until the record exists, the appliance stays pointed at `https://150.0.0.252`. Everything else — Central
configuration, both certificates, all three trust roots, the schema — is already on the FQDN and needs no
further change when DNS appears; only the appliance's `SCD_CTRLAPI_BASE` and `SCD_MTLS_BASE` are switched.
