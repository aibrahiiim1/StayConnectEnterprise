#!/usr/bin/env bash
# Generate review inventories + deterministic catalog fingerprint from the current scratch iam_v2.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
R="$HERE/review"; mkdir -p "$R"
Qf(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -F $'\t' -At -c "$1"; }

require_scratch

Qf "SELECT table_name, ordinal_position, column_name, data_type, is_nullable, coalesce(column_default,'')
    FROM information_schema.columns WHERE table_schema='iam_v2' ORDER BY table_name, ordinal_position;" > "$R/OBJECT_INVENTORY.txt"

Qf "SELECT conrelid::regclass::text AS tbl, contype, conname, pg_get_constraintdef(oid)
    FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace ORDER BY 1,2,3;" > "$R/CONSTRAINT_INVENTORY.txt"

{ echo '# TRIGGERS'; Qf "SELECT tgrelid::regclass::text, tgname, pg_get_triggerdef(t.oid)
      FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
      WHERE n.nspname='iam_v2' AND NOT t.tgisinternal ORDER BY 1,2;"
  echo; echo '# FUNCTIONS'; Qf "SELECT proname, pg_get_function_arguments(p.oid), pg_get_function_result(p.oid)
      FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' ORDER BY 1,2;"
} > "$R/TRIGGER_FUNCTION_INVENTORY.txt"

{ echo '# object ownership'; Qf "SELECT n.nspname, c.relname, c.relkind, c.relowner::regrole::text
      FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
      WHERE n.nspname='iam_v2' AND c.relkind IN ('r','i','S') ORDER BY 3,2;"
  echo; echo '# schema owner'; Qf "SELECT nspname, nspowner::regrole::text FROM pg_namespace WHERE nspname='iam_v2';"
  echo; echo '# table grants (grantee/priv) — expect none for PUBLIC or service roles';
  Qf "SELECT grantee, table_name, privilege_type FROM information_schema.role_table_grants
      WHERE table_schema='iam_v2' ORDER BY 1,2,3;"
  echo; echo '# roles'; Qf "SELECT rolname, rolcanlogin, rolsuper FROM pg_roles WHERE rolname LIKE 'iam_v2%' ORDER BY 1;"
} > "$R/ROLE_GRANT_INVENTORY.txt"

# deterministic catalog fingerprint (no OIDs; stable ordering)
FP="$(docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -At -c "
SELECT md5(string_agg(line, E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s %s %s', table_name, ordinal_position, column_name, data_type, is_nullable) AS line
    FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT format('CON %s %s', conrelid::regclass::text, pg_get_constraintdef(oid))
    FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
  UNION ALL SELECT format('IDX %s', indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT format('TRG %s %s', tgrelid::regclass::text, tgname)
    FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT format('FUN %s(%s)', p.proname, pg_get_function_arguments(p.oid))
    FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'
) x;")"
tables=$(docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -At -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';")
{ echo "iam_v2 base tables: $tables"; echo "catalog_fingerprint_md5: $FP"; } > "$R/CATALOG_FINGERPRINT.txt"
echo "inventories written to review/. fingerprint=$FP tables=$tables"
