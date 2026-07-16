-- Phase 1A LIVE-DARK production RE-VERIFICATION (READ-ONLY). No DDL/DML. Run with: psql -f prod_verify.sql
\pset tuples_only on
\pset format unaligned
\set ON_ERROR_STOP on
SELECT 'current_database='||current_database();
SELECT 'server_addr='||coalesce(host(inet_server_addr()),'local-unix-socket');
SELECT 'in_recovery='||pg_is_in_recovery();
SELECT 'pg_version='||substring(version() from 1 for 24);
SELECT 'iam_v2_schema_owner='||nspowner::regrole::text FROM pg_namespace WHERE nspname='iam_v2';
SELECT 'iam_v2_base_tables='||count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';
SELECT 'iam_v2_total_rows='||coalesce(sum(n_live_tup),0) FROM pg_stat_user_tables WHERE schemaname='iam_v2';
SELECT 'iam_v2_constraints='||count(*) FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace;
SELECT 'iam_v2_triggers='||count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal;
SELECT 'iam_v2_functions='||count(*) FROM pg_proc pr JOIN pg_namespace n ON n.oid=pr.pronamespace WHERE n.nspname='iam_v2';
SELECT 'iam_v2_fingerprint='||md5(string_agg(line,E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s %s %s',table_name,ordinal_position,column_name,data_type,is_nullable) line FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT format('CON %s %s',conrelid::regclass::text,pg_get_constraintdef(oid)) FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
  UNION ALL SELECT format('IDX %s',indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT format('TRG %s %s',tgrelid::regclass::text,tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT format('FUN %s(%s)',pr.proname,pg_get_function_arguments(pr.oid)) FROM pg_proc pr JOIN pg_namespace n ON n.oid=pr.pronamespace WHERE n.nspname='iam_v2'
) x;
SELECT 'mg0_anchor_def='||indexdef FROM pg_indexes WHERE schemaname='public' AND indexname='guest_networks_tsi_anchor';
SELECT 'mg0_anchor_indisvalid='||indisvalid FROM pg_index WHERE indexrelid='public.guest_networks_tsi_anchor'::regclass;
SELECT 'public_base_tables='||count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE';
SELECT 'public_total_rows='||coalesce(sum(n_live_tup),0) FROM pg_stat_user_tables WHERE schemaname='public';
SELECT 'public_fingerprint='||md5(string_agg(line,E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s',table_name,ordinal_position,column_name) line FROM information_schema.columns WHERE table_schema='public'
  UNION ALL SELECT format('CON %s %s',conrelid::regclass::text,pg_get_constraintdef(oid)) FROM pg_constraint WHERE connamespace='public'::regnamespace
  UNION ALL SELECT format('IDX %s',indexdef) FROM pg_indexes WHERE schemaname='public' AND indexname<>'guest_networks_tsi_anchor'
) x;
SELECT 'iam_v2_roles='||coalesce(string_agg(rolname,',' ORDER BY rolname),'(none)') FROM pg_roles WHERE rolname LIKE 'iam_v2%';
SELECT 'public_grants_on_iam_v2='||count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee='PUBLIC';
SELECT 'stayconnect_rolsuper='||rolsuper||' rolcanlogin='||rolcanlogin FROM pg_roles WHERE rolname='stayconnect';
SELECT 'db_default_search_path='||setting FROM pg_settings WHERE name='search_path';
SELECT 'stayconnect_role_search_path='||coalesce((SELECT array_to_string(setconfig,',') FROM pg_db_role_setting s JOIN pg_roles r ON r.oid=s.setrole WHERE r.rolname='stayconnect' AND array_to_string(setconfig,',') ILIKE '%search_path%'),'(none-set)');
SELECT 'iam_v2_leak_into_public='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname LIKE 'iam_v2%';
