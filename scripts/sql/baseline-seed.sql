-- ROWS A NEW APPLIANCE NEEDS FROM ITS FIRST MINUTE, WHICH THE MIGRATIONS INSERT AND A SCHEMA-ONLY BASELINE LOSES.
-- (Appended to data-plane/migrations/baseline/0000_production_baseline.sql by scripts/generate-production-baseline.sh; the semantic
-- catalog compares the row count of each of these tables between the upgrade path and the baseline, so a seed a later migration adds
-- and this file lacks is a failed gate, not a missing row on an appliance.)
--
-- Found when the Phase-5 gate, which builds from the baseline alone, met an erasure ledger with no identity: migration 0148 inserts the
-- ledger's identity and its first entry, and a baseline built before those rows existed never had them.
--
-- These are the migration statements, unchanged, so each appliance mints its OWN ledger identity and first entry (nothing here is a
-- value copied from another database). Idempotent.

-- 0148: the erasure ledger's identity, and its first entry.
INSERT INTO iam_v2.ledger_meta DEFAULT VALUES ON CONFLICT DO NOTHING;
INSERT INTO iam_v2.erasure_ledger (seq, ledger_id, tenant_id, site_id, kind, at_us, body, prev_hash, hash, confirmed)
SELECT 1, m.ledger_id, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000', 'genesis',
       t.us, '{"note": "ledger created"}', repeat('0', 64),
       iam_v2.ledger_hash(m.ledger_id, 1, repeat('0', 64), 'genesis', '00000000-0000-0000-0000-000000000000',
                          '00000000-0000-0000-0000-000000000000', t.us, '{"note": "ledger created"}'),
       true
  FROM iam_v2.ledger_meta m, (SELECT (extract(epoch from clock_timestamp()) * 1000000)::bigint AS us) t
 WHERE NOT EXISTS (SELECT 1 FROM iam_v2.erasure_ledger);

-- 0144: the floor for rows whose age is unknown. A new appliance has no row of unknown age, so the floor is the moment it is built.
INSERT INTO iam_v2.retention_floor (id, since) VALUES (true, now()) ON CONFLICT (id) DO NOTHING;
