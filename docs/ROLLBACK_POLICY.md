# Rollback Policy — Central Control Plane

This defines how to roll back `ctrlapi` and `cloud-admin` on Central (`sc-central.echofusion.com`, host `172.21.96.196` since 2026-09-27)
**without weakening the ownership-tree delete protection** introduced in migration
`0037_ownership_delete_protection`, and where the schema boundaries lie that a
binary rollback cannot cross — on Central (0046, 0047) and on the appliance
(0092, 0093). The deploy/rollback mechanics for Central are in
[DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md) (`central-deploy.sh rollback`, the
pre-deploy dump and the database rollback commands).

## Principle

Migration 0037 sets `ON DELETE RESTRICT` on the ownership edges (Customer → Site,
Site → Appliance, and the license edges; the subscription and enrollment-token edges
it also covered went away with those tables in migration 0046). These constraints are
the database backstop that prevents a whole customer/site subtree from being wiped
by an accidental cascade or a direct SQL/API call.

## The redesign boundary (migration 0046)

The Central redesign ([CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md)) ships
with migration `0046_central_redesign`, which drops `appliances.status` and
`appliances.environment`, reduces the lifecycle to `pending_approval | assigned |
revoked | decommissioned`, drops 24 empty legacy tables and moves the commercial
history to schema `legacy_archive`. **A ctrlapi or cloud-admin release from before
the redesign cannot run against the 0046 schema.** Rolling back *within* the
redesigned releases is the standard procedure below; rolling back *across* the
redesign requires `0046_central_redesign.down.sql` first, which restores every
column and table but is **lossy by design** (fine-grained old lifecycle values come
back as `pending_approval`/`assigned`) — that is a CONTROLLED schema action needing
Product-Owner authorization and a fresh backup, exactly like the emergency case
below.

## The cleanup boundary (migration 0047)

`0047_central_cleanup` **deletes data on purpose** (Product-Owner decision,
2026-09-27): it drops schema `legacy_archive` with everything in it (the
commercial history 0046 moved there and, on the live Central, an older
guest/session/voucher/accounting archive), drops columns no code reads
(operator SSO, `tenants.auth_methods`/`contact_email`/`metadata`,
`sites.metadata`, `appliances.metadata`/`name`/`cert_fingerprint`/`identity_verified_at`,
`operator_roles.site_id`, `licenses.commercial_plan_code`/`features`/`limits`),
renames `appliances.enrolled_at` to `registered_at`, adds
`retired_appliance_identities` and truncates the assignment fetch log.

A ctrlapi from before 0047 cannot run against the 0047 schema (it reads
`enrolled_at` and the dropped columns). `0047_central_cleanup.down.sql` restores
the **structure only**: `legacy_archive` returns with its four tables **empty**,
the dropped columns return with their defaults (not their old values), and the
retired-identity register is dropped — **and with it the refusal of retired
identity keys**. The deleted data exists only in a dump taken before 0047. Rolling
back across 0047 is a CONTROLLED schema action (approval + backup).

## Appliance schema boundaries (0092, 0093)

These are appliance (`data-plane/migrations`) migrations, applied per site; a
binary rollback on the appliance follows the same rule — schema first, and only
where the down migration allows it.

- **0092** (room sign-in answers to the licence) widens `iam_v2.sign_in_attempts`
  to record `LICENSE_REFUSED` and `LICENSE_CAPACITY_REACHED`. Its down migration
  **refuses** while any recorded attempt carries one of them (an attempt is
  evidence and is never relabelled); such rows age out under the thirty-day
  retention. An scd from before 0092 does not write these results.
- **0093** (the appliance reports to nobody) drops the cloud-mode and cloud-sync
  settings, the outbox recovery/retention/accounting functions, and the public
  tables `sync_outbox`, `sync_checkpoints`, `edge_executed_commands` and
  `edge_installed_updates` **with their rows** (authorized). Its down migration
  restores the structure **empty**; the removed code comes back only with the
  previous binaries.
  **Deployment note.** A live site applies migrations as `iam_v2_owner`, which may
  not drop the four public tables it does not own: 0093 skips them there with a
  NOTICE. Their owner, the installation superuser, removes them after 0093 has
  applied (nothing reads them; Gate P no longer grants on them):

  ```sh
  docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -v ON_ERROR_STOP=1 -c \
    "DROP TABLE IF EXISTS public.sync_outbox, public.sync_checkpoints, public.edge_executed_commands, public.edge_installed_updates"
  ```

  A factory-clean or disposable database drops them inside 0093 itself.

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
#    a) the 5 RESTRICT edges still present (0046 removed the subscription/token tables)
docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc \
 "SELECT count(*) FROM pg_constraint WHERE confdeltype='r' AND conname IN
  ('sites_tenant_id_fkey','appliances_tenant_id_fkey','appliances_site_id_fkey',
   'licenses_tenant_id_fkey','licenses_site_id_fkey');"
#    expect: 5
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
| Bad ctrlapi/cloud-admin deploy (both releases post-redesign) | Restore previous binary + flip symlink | **Keep applied** |
| Roll back across the redesign | 0046 down migration first (lossy), then pre-redesign binaries | CONTROLLED — approval + backup |
| Roll back across the cleanup | 0047 down migration first (structure only; deleted data needs a pre-0047 dump), then pre-0047 binaries | CONTROLLED — approval + backup |
| Appliance: roll back across 0092 / 0093 | the down migration (0092 refuses while licence results are recorded; 0093 restores empty tables), then the previous binaries | CONTROLLED — approval + backup |
| Defect proven to be the RESTRICT constraints | Emergency down migration | Reverted (approval + backup) |
