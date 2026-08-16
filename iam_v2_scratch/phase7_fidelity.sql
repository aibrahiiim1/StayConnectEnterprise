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
-- WHAT AN EQUAL DIGEST DOES AND DOES NOT MEAN.
--
-- It means the two databases agree on every surface listed above: column types/nullability/defaults, constraint
-- definitions INCLUDING grouping, index definitions, trigger definitions, function signatures, bodies, owners
-- and configuration, object ownership, role attributes and memberships, and the complete effective privilege
-- surface for iam_v2 -- every grantee, not a known-prefix allowlist.
--
-- It does NOT mean "exactly semantically equal" in any wider sense, and that phrase is used nowhere. Row data
-- is deliberately excluded (it is test fixture, proven separately). Carriage returns inside function bodies
-- are normalised, because the same Git blob applied from a CRLF checkout and an LF checkout produces
-- byte-different, meaning-identical text. Anything outside schema iam_v2 -- including the public schema's own
-- objects -- is outside this claim and is proven separately where it matters.
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
  -- PARENTHESES ARE KEPT. Stripping them was wrong: `(A OR B) AND C` and `A OR (B AND C)` differ only in
  -- grouping and admit different rows, so a digest that erases grouping cannot see a precedence change --
  -- exactly the kind of edit that widens a CHECK while looking untouched. Only whitespace is normalised, and
  -- the two databases are compared as built from the same sources rather than through a dump round trip, so
  -- the deparse spacing that motivated the earlier stripping does not arise.
  SELECT 'CON:'||c.relname||':'||con.conname||':'
         ||regexp_replace(pg_get_constraintdef(con.oid), '[[:space:]]+', ' ', 'g')
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
  -- LANGUAGE, STRICTNESS AND CONFIGURATION, not just the body. A SECURITY DEFINER function whose
  -- `SET search_path` is removed resolves unqualified names through the caller's path -- the classic definer
  -- hijack -- while its body, name, signature and definer flag all stay identical. proconfig is where that
  -- lives, and a digest without it cannot see the change.
  SELECT 'FN:'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||')'
         ||':secdef='||p.prosecdef::text
         ||':lang='||l.lanname
         ||':strict='||p.proisstrict::text
         ||':leakproof='||p.proleakproof::text
         ||':config='||COALESCE(array_to_string(p.proconfig, ','), 'none')
         ||':vol='||p.provolatile::text
         -- CARRIAGE RETURNS STRIPPED, and this is the difference between a real divergence and a transport
         -- artifact. The appliance's migrations were applied from CRLF files; the same blobs applied from an
         -- LF checkout produce bodies that differ by exactly one character per line. All 46 "differing
         -- function bodies" were this and nothing else -- zero remain once CR is removed. A line ending is
         -- not semantics, and treating it as such sent a whole session hunting a divergence that was never
         -- there.
         ||':body='||md5(replace(COALESCE(p.prosrc, ''), chr(13), ''))
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    JOIN pg_language l ON l.oid = p.prolang
   WHERE n.nspname = 'iam_v2'
  UNION ALL
  -- OWNERSHIP. A SECURITY DEFINER function with an identical body, signature and proconfig but a DIFFERENT
  -- owner runs with different authority -- it is not the same function in any sense that matters. Table
  -- ownership matters too: an owner holds every privilege implicitly, so moving ownership silently moves the
  -- effective grant surface without touching one ACL entry.
  SELECT 'OWNTBL:'||c.relname||':'||pg_get_userbyid(c.relowner)
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2' AND c.relkind IN ('r','p','v','m','S')
  UNION ALL
  SELECT 'OWNFN:'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||'):'
         ||pg_get_userbyid(p.proowner)||':secdef='||p.prosecdef::text
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = 'iam_v2'
  UNION ALL
  SELECT 'OWNNSP:iam_v2:'||pg_get_userbyid(n.nspowner) FROM pg_namespace n WHERE n.nspname = 'iam_v2'
  UNION ALL
  -- ROLE ATTRIBUTES, for the roles this schema names. Membership alone is not fidelity: on the appliance
  -- `stayconnect` is a SUPERUSER and owns the Phase-6 definer functions, so it bypasses the schema ACL. A
  -- rebuild that created the same role name without SUPERUSER produced a database where every definer
  -- function failed with "permission denied for schema iam_v2" -- and the digest, which only knew names,
  -- called that database identical to the appliance.
  -- EVERY load-bearing attribute, not a convenient subset. BYPASSRLS defeats row-level security outright;
  -- CREATEROLE is a privilege-escalation path to any non-superuser role; CREATEDB and REPLICATION are their
  -- own escalations. A role gaining any of them is a security change that no ACL comparison would reveal.
  SELECT 'ROLE:'||r.rolname||':super='||r.rolsuper::text||':inherit='||r.rolinherit::text
         ||':login='||r.rolcanlogin::text||':bypassrls='||r.rolbypassrls::text
         ||':createrole='||r.rolcreaterole::text||':createdb='||r.rolcreatedb::text
         ||':replication='||r.rolreplication::text
    FROM pg_roles r
   -- Every non-system role in the cluster. Naming a fixed list would hide the arrival of a role nobody
   -- expected, which is the thing most worth noticing.
   WHERE r.rolname NOT LIKE 'pg\_%' AND r.rolname <> 'postgres'
  UNION ALL
  -- ROLE MEMBERSHIPS. Granting svc_scd membership of iam_v2_owner hands it every owner privilege without
  -- changing one ACL entry or role attribute, so neither of the checks above would see it.
  SELECT 'MEMBER:'||m.rolname||' IN '||g.rolname
    FROM pg_auth_members am
    JOIN pg_roles m ON m.oid = am.member
    JOIN pg_roles g ON g.oid = am.roleid
   WHERE m.rolname NOT LIKE 'pg\_%' AND g.rolname NOT LIKE 'pg\_%'
     AND m.rolname <> 'postgres' AND g.rolname <> 'postgres'
  UNION ALL
  -- schema-level privileges. GRANT USAGE ON SCHEMA is a namespace ACL, not a table grant, so a digest built
  -- only from table grants cannot see a role losing access to the schema itself.
  SELECT 'NSP:'||n.nspname||':'||COALESCE(array_to_string(n.nspacl, ','), 'DEFAULT')
    FROM pg_namespace n WHERE n.nspname = 'iam_v2'
  UNION ALL
  -- table privileges held by the real service roles
  SELECT 'GRT:'||grantee||':'||table_name||':'||privilege_type
    FROM information_schema.role_table_grants
   -- NO ALLOWLIST. Every grantee on every iam_v2 table, whoever it is. An allowlist of svc_/sc_/iam_v2_
   -- prefixes makes exactly the dangerous case invisible: a grant to PUBLIC, or to some role nobody expected,
   -- is precisely what a privilege regression looks like, and it would have matched no prefix.
   WHERE table_schema = 'iam_v2' AND grantee <> 'postgres'
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
             -- Every non-system role, not a prefix list, for the same reason as the table grants above.
             -- `postgres` is excluded as the CLUSTER BOOTSTRAP identity: a scratch container has one and the
             -- appliance does not, so including it compares environments rather than systems. Nothing else is
             -- excluded, and every other role's SUPERUSER status is still hashed in the ROLE parts, so a role
             -- that GAINS superuser is still caught.
             SELECT string_agg(r.rolname, ',' ORDER BY r.rolname)
               FROM pg_roles r
              WHERE r.rolname NOT LIKE 'pg\_%' AND r.rolname <> 'postgres'
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
