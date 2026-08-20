SELECT 'COLUMN|'||table_schema||'.'||table_name||'|'||column_name||'|'||data_type||'|'||is_nullable||'|'||COALESCE(column_default,'-')
  FROM information_schema.columns WHERE table_schema IN ('public','iam_v2')
UNION ALL SELECT 'CONSTRAINT|'||n.nspname||'.'||rel.relname||'|'||con.conname||'|'||con.contype::text||'|'||pg_get_constraintdef(con.oid)
  FROM pg_constraint con JOIN pg_class rel ON rel.oid=con.conrelid
  JOIN pg_namespace n ON n.oid=rel.relnamespace WHERE n.nspname IN ('public','iam_v2')
UNION ALL SELECT 'INDEX|'||schemaname||'.'||tablename||'|'||indexname||'|'||indexdef
  FROM pg_indexes WHERE schemaname IN ('public','iam_v2')
UNION ALL SELECT 'TRIGGER|'||n.nspname||'.'||c.relname||'|'||t.tgname||'|'||pg_get_triggerdef(t.oid)
  FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE NOT t.tgisinternal AND n.nspname IN ('public','iam_v2')
UNION ALL SELECT 'FUNCTION|'||n.nspname||'.'||p.proname||'|'||pg_get_function_identity_arguments(p.oid)||'|secdef='||p.prosecdef::text||'|owner='||pg_get_userbyid(p.proowner)
  FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname IN ('public','iam_v2')
UNION ALL SELECT 'OWNER|'||n.nspname||'.'||c.relname||'|'||pg_get_userbyid(c.relowner)
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname IN ('public','iam_v2') AND c.relkind IN ('r','v','m')
UNION ALL SELECT 'SCHEMAOWNER|'||nspname||'|'||pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname IN ('public','iam_v2')
UNION ALL SELECT 'TABLEGRANT|'||table_schema||'.'||table_name||'|'||grantee||'|'||privilege_type
  FROM information_schema.role_table_grants WHERE table_schema IN ('public','iam_v2')
UNION ALL SELECT 'FUNCGRANT|'||n.nspname||'.'||p.proname||'|'||r.rolname
  FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
  CROSS JOIN pg_roles r WHERE n.nspname IN ('public','iam_v2') AND NOT r.rolsuper
    AND has_function_privilege(r.rolname, p.oid, 'EXECUTE')
UNION ALL SELECT 'MEMBERSHIP|'||g.rolname||'|'||m.rolname
  FROM pg_auth_members am JOIN pg_roles g ON g.oid=am.roleid JOIN pg_roles m ON m.oid=am.member;
