# IAM-v2 base install steps — authoritative, ordered, ledger-recorded

These are the **Production installation source** for the IAM-v2 domain. They run **after**
`data-plane/migrations/0008` and **before** `0009`, because `0009` onwards address `iam_v2` objects and
nothing in the numbered sequence creates that schema.

`iam_v2_scratch/` remains in the repository as **provenance** for how this schema was authored and accepted.
It is not an installation source: its own README states it is scratch/test and not production code, and a
production installer must not depend on a directory documented that way. The files here are the same accepted
content, promoted into a path the installer records in `schema_migrations`.

| Step | File | Purpose |
|---|---|---|
| `mg0` | `mg0_guest_networks_tsi_anchor.sql` | the contract-defined tenant/site-scoped guest-network anchor |
| `mg1..mg9` | copied from the accepted Phase-1A set at install time | the IAM-v2 domain |

## MG-0 and `CONCURRENTLY`

The accepted Phase-1A MG-0 builds `guest_networks_tsi_anchor` with `CREATE UNIQUE INDEX CONCURRENTLY`,
because on a live appliance the anchor is added to a table already in service and must not take a long lock.
`CONCURRENTLY` cannot run inside a transaction block, so MG-0 is applied outside one and its validity is
verified afterwards — an invalid index left by an interrupted build is worse than no index, because a bare
`IF NOT EXISTS` would then silently skip the rebuild.

On a **factory-clean** install the table is empty and unattached, so the same index is built without
`CONCURRENTLY` in a normal transaction. The resulting object is identical: same name, same uniqueness, same
column order. The installer asserts that, rather than assuming it.

**It is an INDEX, not a table constraint.** A composite foreign key may reference either, and
`iam_v2.guest_network_pms_map (tenant_id, site_id, guest_network_id)` references
`public.guest_networks (tenant_id, site_id, id)` through this index. Adding an equivalent
`ALTER TABLE ... ADD CONSTRAINT ... UNIQUE` instead would satisfy the foreign key but produce a *different*
catalog object from the accepted baseline, which is why the installer creates the index by name.
