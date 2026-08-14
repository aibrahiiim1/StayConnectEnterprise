-- STRUCTURAL fingerprint of the iam_v2 schema, for comparing a schema across a ROLLBACK cycle.
--
-- WHY THIS EXISTS, measured during WS-L on the development appliance (2026-08-13).
--
-- The catalog fingerprint used everywhere else in this project includes `ordinal_position`, and that is
-- correct for what it is normally asked: "is this schema the one the migration chain produces?" It is NOT
-- correct across a DOWN -> UP cycle. Dropping a column does not free its attribute slot in PostgreSQL, so
-- every column added after it moves up by one when the chain is re-applied. After rolling 0026 -> 0011 and
-- back up on the appliance, the catalog fingerprint changed:
--
--     first apply       5ebf0dfcd3eeedaf94c15fe0fa2fdb65
--     after down -> up  5c59f5f9902dcdc6a8164176f67cf64c
--
-- ...while the schema was IDENTICAL: 838 columns, 411 constraints, 195 indexes, 84 triggers and 111
-- functions on both sides, with the same names, types, nullability and definitions. The whole difference was
-- 37 dropped-column slots left behind by the DOWN.
--
-- Reporting that as "the rollback changed the schema" would have been wrong, and reporting it as "the
-- fingerprints match" would have been false. This is the fingerprint that answers the question actually
-- being asked after a rollback: is the STRUCTURE the same? It deliberately omits ordinal_position and
-- nothing else.
--
-- Use the catalog fingerprint to prove a migration chain produced the expected schema.
-- Use THIS one to prove a rollback-and-reapply cycle returned to the same schema.
SELECT md5(string_agg(line, E'\n' ORDER BY line)) AS iam_v2_structure_fingerprint
  FROM (
    SELECT format('COL %s %s %s %s', table_name, column_name, data_type, is_nullable) AS line
      FROM information_schema.columns WHERE table_schema = 'iam_v2'
    UNION ALL
    SELECT format('CON %s %s', conrelid::regclass::text, pg_get_constraintdef(oid))
      FROM pg_constraint WHERE connamespace = 'iam_v2'::regnamespace
    UNION ALL
    SELECT format('IDX %s', indexdef) FROM pg_indexes WHERE schemaname = 'iam_v2'
    UNION ALL
    SELECT format('TRG %s %s', tgrelid::regclass::text, tgname)
      FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'iam_v2' AND NOT t.tgisinternal
    UNION ALL
    SELECT format('FUN %s(%s)', pr.proname, pg_get_function_arguments(pr.oid))
      FROM pg_proc pr JOIN pg_namespace n ON n.oid = pr.pronamespace WHERE n.nspname = 'iam_v2'
  ) x;
