-- ============================================================================================================
-- PHASE 5 — the post-stay PIN gets its OWN throttle method.
--
-- auth_throttle_buckets keys every counter on (scope_kind, scope_key, METHOD, window). The method column is
-- what stops one authentication method's failures from consuming another's budget for the same guest, IP or
-- device — and it is CHECK-constrained, so a new method is a schema change rather than a string a caller can
-- invent.
--
-- Without this, a post-stay attempt could only be counted under an existing method's name or under the shared
-- '*'. Both are wrong: the first makes a post-stay brute force lock a guest out of the ordinary portal (or,
-- worse, be masked by it), and the second shares one budget across every method at once.
-- ============================================================================================================
BEGIN;

ALTER TABLE public.auth_throttle_buckets DROP CONSTRAINT IF EXISTS auth_throttle_buckets_method_chk;
ALTER TABLE public.auth_throttle_buckets ADD CONSTRAINT auth_throttle_buckets_method_chk
  CHECK (method IN ('account','otp','voucher','social','pms','post_stay_pin','*'));

INSERT INTO public.schema_migrations (version)
  VALUES ('0028_phase5_poststay_throttle_method') ON CONFLICT DO NOTHING;
COMMIT;
