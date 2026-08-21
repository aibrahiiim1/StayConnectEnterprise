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
