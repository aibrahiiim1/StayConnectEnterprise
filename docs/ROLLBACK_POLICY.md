# Rollback Policy — Central Control Plane

This defines how to roll back `ctrlapi` and `cloud-admin` on Central (150.0.0.252)
**without weakening the ownership-tree delete protection** introduced in migration
`0037_ownership_delete_protection`.

## Principle

Migration 0037 sets `ON DELETE RESTRICT` on the ownership edges (Customer → Site,
Site → Appliance, and the license/subscription/token edges). These constraints are
the database backstop that prevents a whole customer/site subtree from being wiped
by an accidental cascade or a direct SQL/API call.

**A normal application rollback MUST keep migration 0037 applied.** The application
code (all hardened releases from commit `ce783df` onward) is fully compatible with
the 0037 schema — the RESTRICT constraints and the added `sites.status` column do
not break any older hardened binary. Verified live: rolling `ctrlapi` + `cloud-admin`
back to the previous release and forward again keeps all 8 RESTRICT edges intact and
delete-safety enforced (customer-with-site delete → 409) at every stage.

## Compatible rollback (STANDARD — no approval needed)

Roll the binaries/UI back to the last-good release; **do not touch the schema.**

```sh
# 1. ctrlapi — restore the previous binary (backups live in /opt/stayconnect/bin/)
ls -1t /opt/stayconnect/bin/ctrlapi.bak-*          # pick the last-good build
install -m0755 /opt/stayconnect/bin/ctrlapi.bak-<tag> /opt/stayconnect/bin/ctrlapi
systemctl restart stayconnect-ctrlapi.service

# 2. cloud-admin — flip the release symlink back to .previous
ln -sfn "$(readlink -f /opt/stayconnect/cloud-admin-current.previous)" \
        /opt/stayconnect/cloud-admin-current
systemctl restart stayconnect-cloud-admin.service

# 3. VERIFY the protection survived (all three must hold):
#    a) 8 RESTRICT edges still present
docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc \
 "SELECT count(*) FROM pg_constraint WHERE confdeltype='r' AND conname IN
  ('sites_tenant_id_fkey','appliances_tenant_id_fkey','appliances_site_id_fkey',
   'licenses_tenant_id_fkey','licenses_site_id_fkey','tenant_subscriptions_tenant_id_fkey',
   'appliance_bootstrap_tokens_tenant_id_fkey','appliance_bootstrap_tokens_site_id_fkey');"
#    expect: 8
#    b) ctrlapi healthy:      curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/healthz  # 200
#    c) delete-safety active: deleting a customer that still has a site returns 409, not 204.
```

`cloud-admin-current.previous` and the timestamped `ctrlapi.bak-*` binaries are kept
automatically on every deploy, so the previous release is always available.

## Emergency-only: cascade-restoring schema rollback

`migrations/0037_ownership_delete_protection.down.sql` reverts the RESTRICT edges
back to `ON DELETE CASCADE` and drops `sites.status`. This **re-enables the dangerous
behaviour** where deleting a Customer or Site silently wipes its entire subtree.

**Do NOT run the 0037 down migration as part of a normal rollback.** It is required
only if a specific defect is proven to be caused by the RESTRICT constraints
themselves (extremely unlikely — no application path depends on cascade).

Running it requires ALL of:
- Explicit written approval from the platform owner.
- A full database backup taken immediately beforehand (see `BACKUP_AND_RESTORE.md`).
- A recorded reason and a follow-up ticket to re-apply 0037.

```sh
# EMERGENCY ONLY — restores ownership cascades. Approval + backup required.
docker exec -i sc-central-pg psql -U stayconnect -d stayconnect -v ON_ERROR_STOP=1 \
  < control-plane/migrations/0037_ownership_delete_protection.down.sql
```

## Summary

| Scenario | Action | Schema (0037) |
|---|---|---|
| Bad ctrlapi/cloud-admin deploy | Restore previous binary + flip symlink | **Keep applied** |
| Defect proven to be the RESTRICT constraints | Emergency down migration | Reverted (approval + backup) |
