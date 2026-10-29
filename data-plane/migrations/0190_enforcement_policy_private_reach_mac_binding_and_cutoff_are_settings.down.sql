-- Removing the enforcement policy. WHAT THIS COSTS: netd keeps the policy it last read until it restarts and then
-- renders the previous ruleset (no private-destination deny, IP-only authorization); acctd falls back to its built-in
-- cut-off period. Any hotel allow-list and the change history are dropped with the tables -- copy them first if wanted.
BEGIN;
DROP FUNCTION IF EXISTS public.enforcement_policy_set(text,text,text,text);
DROP TRIGGER IF EXISTS enforcement_policy_changes_append_only ON public.enforcement_policy_changes;
DROP TABLE IF EXISTS public.enforcement_policy_changes;
DROP FUNCTION IF EXISTS public.enforcement_policy_changes_append_only();
DROP TABLE IF EXISTS public.enforcement_policy;
COMMIT;
