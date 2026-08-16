-- PHASE-7 — SEMANTIC SCHEMA FIDELITY, NOT A NAME LIST.
--
-- The first fingerprint hashed object NAMES and a couple of flags. That is enough to notice a missing table
-- and nothing else: a column could change type, a CHECK could be rewritten to admit what it used to refuse, a
-- trigger could be repointed at a different function, a SECURITY DEFINER body could be replaced wholesale --
-- and every one of those would have produced the identical "accepted" fingerprint. A proof that cannot
-- distinguish a materially different schema is not a proof, it is a coincidence detector.
--
-- This hashes DEFINITIONS:
--
--   columns      table, name, ordinal, resolved type, nullability, default expression
--   constraints  the full pg_get_constraintdef, so a rewritten CHECK moves the hash
--   indexes      the full indexdef, including uniqueness, predicate and column order
--   triggers     the full pg_get_triggerdef, including timing, events and the function it calls
--   functions    identity signature, SECURITY DEFINER flag, volatility, and md5 of the BODY
--   grants       every svc_* table privilege, and every function ACL entry, PUBLIC included
--
-- Two databases with the same digest here have the same iam_v2 semantics, not merely the same nouns.
--
-- Usage:  psql -tAqf phase7_fidelity.sql          -> one line: the digest
--         psql -tAqvdetail=1 -f phase7_fidelity.sql -> every component row, for diffing two databases
\set QUIET on
\if :{?detail}
\else
  \set detail 0
\endif

WITH parts(part) AS (
  -- columns, with their resolved type rather than the catalogue's internal one
  -- NO attnum. Ordinal position is PHYSICAL, not semantic: a column dropped years ago still occupies its
  -- slot on the appliance while a schema restored from a dump compresses the gap, so including it makes every
  -- faithful reconstruction look different for a reason that changes no behaviour. Name, type, nullability
  -- and default are the contract.
  SELECT 'COL:'||c.relname||'.'||a.attname||':'||format_type(a.atttypid, a.atttypmod)
         ||':'||(NOT a.attnotnull)::text
         ||':'||COALESCE(pg_get_expr(d.adbin, d.adrelid), '-') AS part
    FROM pg_attribute a
    JOIN pg_class c ON c.oid = a.attrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
   WHERE n.nspname = 'iam_v2' AND c.relkind IN ('r','p') AND a.attnum > 0 AND NOT a.attisdropped
  UNION ALL
  -- constraints, by DEFINITION: a CHECK rewritten to admit what it used to refuse moves this
  -- NORMALISED FOR PARENTHESES AND SPACING. A dump/restore round trip re-deparses `(A AND B) AND C` as
  -- `A AND B AND C` -- the same predicate, written differently by the same server. Grouping is stripped so
  -- that formatting cannot fail a faithful restore, and NOTHING else is: a different operator, literal,
  -- column or referenced table still changes the token stream, which is the property worth keeping.
  SELECT 'CON:'||c.relname||':'||con.conname||':'
         ||translate(pg_get_constraintdef(con.oid), '() ', '')
    FROM pg_constraint con
    JOIN pg_class c ON c.oid = con.conrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2'
  UNION ALL
  -- indexes, by definition: uniqueness, column order and partial predicates all matter
  SELECT 'IDX:'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname = 'iam_v2'
  UNION ALL
  -- triggers, by definition: timing, events, and WHICH function they call
  SELECT 'TRG:'||c.relname||':'||t.tgname||':'||pg_get_triggerdef(t.oid)
    FROM pg_trigger t
    JOIN pg_class c ON c.oid = t.tgrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2' AND NOT t.tgisinternal
  UNION ALL
  -- functions: signature, definer flag, volatility, and the BODY. A replaced definer body is the exact case
  -- a name-only fingerprint cannot see, and the one with the largest blast radius.
  SELECT 'FN:'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||')'
         ||':secdef='||p.prosecdef::text
         ||':vol='||p.provolatile::text
         ||':body='||md5(COALESCE(p.prosrc, ''))
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = 'iam_v2'
  UNION ALL
  -- ROLE ATTRIBUTES, for the roles this schema names. Membership alone is not fidelity: on the appliance
  -- `stayconnect` is a SUPERUSER and owns the Phase-6 definer functions, so it bypasses the schema ACL. A
  -- rebuild that created the same role name without SUPERUSER produced a database where every definer
  -- function failed with "permission denied for schema iam_v2" -- and the digest, which only knew names,
  -- called that database identical to the appliance.
  SELECT 'ROLE:'||r.rolname||':super='||r.rolsuper::text||':inherit='||r.rolinherit::text
         ||':login='||r.rolcanlogin::text
    FROM pg_roles r
   WHERE r.rolname IN ('stayconnect','iam_v2_owner','iam_v2_migrator','svc_scd','svc_edged','svc_acctd',
                       'svc_netd','sc_payment_runtime','sc_payment_outcome','sc_commerce_runtime',
                       'sc_financial_operator','sc_financial_readonly')
  UNION ALL
  -- schema-level privileges. GRANT USAGE ON SCHEMA is a namespace ACL, not a table grant, so a digest built
  -- only from table grants cannot see a role losing access to the schema itself.
  SELECT 'NSP:'||n.nspname||':'||COALESCE(array_to_string(n.nspacl, ','), 'DEFAULT')
    FROM pg_namespace n WHERE n.nspname = 'iam_v2'
  UNION ALL
  -- table privileges held by the real service roles
  SELECT 'GRT:'||grantee||':'||table_name||':'||privilege_type
    FROM information_schema.role_table_grants
   WHERE table_schema = 'iam_v2' AND grantee LIKE 'svc\_%'
  UNION ALL
  -- function privileges, PUBLIC included. An empty grantee IS public, and that is the entry that matters.
  -- EFFECTIVE PRIVILEGE, NOT ACL TEXT. Two databases can express the same function privileges differently:
  -- an explicit `GRANT EXECUTE TO PUBLIC` and an untouched default are both "PUBLIC may execute", and
  -- pg_dump omits an ACL that equals the default, so a faithful restore legitimately shows DEFAULT where the
  -- source showed `=X/owner`. Comparing the text would fail that restore for a difference in notation.
  --
  -- What is compared instead is what the catalogue will actually ANSWER: can PUBLIC execute this, and which
  -- named runtime roles can. That is the security contract, and it is representation-independent -- while a
  -- genuine change (PUBLIC gaining execute, or a service role losing it) still moves the digest.
  SELECT 'FNEXEC:'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||'):'
         ||'public='||has_function_privilege('public', p.oid, 'EXECUTE')::text
         ||':roles='||COALESCE((
             SELECT string_agg(r.rolname, ',' ORDER BY r.rolname)
               FROM pg_roles r
              WHERE (r.rolname LIKE 'svc\_%' OR r.rolname LIKE 'sc\_%' OR r.rolname LIKE 'iam\_v2\_svc%')
                AND has_function_privilege(r.rolname, p.oid, 'EXECUTE')), 'none')
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = 'iam_v2'
)
-- One statement, no session objects and no psql branch around the query itself. The earlier version created a
-- temp view and then selected from it inside an \if, which psql resolved in an order that left the view
-- invisible -- and a fidelity proof that cannot run over a pipe is a fidelity proof nobody will run.
, digest AS (
  -- Ordered inside the aggregate, which a WINDOW cannot do: `string_agg(... ORDER BY ...) OVER ()` is not
  -- implemented, and an UNORDERED aggregate would make the digest depend on scan order -- the same schema
  -- would hash differently on two runs, which is worse than no digest at all.
  SELECT md5(string_agg(part, E'\n' ORDER BY part)) AS d, count(*) AS n FROM parts
)
SELECT CASE WHEN :detail = 1 THEN p.part ELSE g.d || ' parts=' || g.n END AS fidelity
  FROM digest g
  LEFT JOIN parts p ON :detail = 1
 ORDER BY 1;
