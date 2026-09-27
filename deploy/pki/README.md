# `deploy/pki/` — the vendor trust slot

Put the vendor's **public** verification key here as `vendor-license.pub` (32 raw bytes) and every appliance
provisioned from this deploy tree is pinned to it automatically. `provision-fresh-appliance.sh` installs it
in step 5; nobody has to remember a manual step, and no appliance is left trusting nothing by accident.

Get it from Central, which holds the private half:

```
deploy/scripts/vendor-signing-key.sh export-public deploy/pki/     # run on the Central host
```

Then confirm the fingerprint it prints matches what the appliance reports after provisioning
(`install-vendor-trust-key.sh --show`). That comparison is the whole security of offline activation.

## Nothing in here is committed

The `.gitignore` beside this file excludes all key material. That is deliberate in both directions:

- The **private** signing key must never enter Git, an image or a package. Anyone holding it can mint an
  activation package for any appliance in the fleet.
- The **public** key is not secret, but it is *vendor* material, not source. Committing one would make a
  repository checkout look like an authorization to trust a particular vendor, and a fork or a stale branch
  would then pin appliances to a key nobody chose.

What lives in source is the **path** and the tooling. The key itself is supplied per deployment.

## Where Central publishes its public trust material

`central-install.sh` writes everything an appliance needs to trust a Central — and nothing secret — to
`/opt/stayconnect/central/appliance-trust/` on the Central host:

| File | Appliance side |
|---|---|
| `vendor-license.pub` | copy here (`deploy/pki/vendor-license.pub`); provisioning pins it |
| `assignment-registry-root.pub` | the signed assignment registry's anchor (`install-assignment-root-anchor.sh`) |
| `central-tls-ca.crt` | Central's internal TLS CA for :443 (`install-central-trust.sh`) |
| `appliance-root-ca.crt` | the appliance CA root |
| `FINGERPRINTS.txt` | the key ids and fingerprints to confirm out of band |

A **moved** Central (`central-install.sh --mode restore`) publishes byte-identical files, because it carries the
same keys; if its `FINGERPRINTS.txt` differs from the old host's, the move is wrong — stop before switching DNS.
The private halves never leave Central except inside an encrypted `central-export.sh` bundle
([docs/DEPLOYMENT_CLOUD.md](../../docs/DEPLOYMENT_CLOUD.md) §6, §9).
