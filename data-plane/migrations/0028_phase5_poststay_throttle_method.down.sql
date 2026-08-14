-- Reverses 0028. Any post_stay_pin bucket is deleted FIRST: re-adding the narrower CHECK would otherwise fail
-- against live rows, and a rollback that cannot run is not a rollback. The rows are throttle counters, not
-- authoritative records — losing them re-opens the current window, which is the correct fail-safe direction
-- for an operation that is removing the feature that created them.
BEGIN;
DELETE FROM public.auth_throttle_buckets WHERE method = 'post_stay_pin';
ALTER TABLE public.auth_throttle_buckets DROP CONSTRAINT IF EXISTS auth_throttle_buckets_method_chk;
ALTER TABLE public.auth_throttle_buckets ADD CONSTRAINT auth_throttle_buckets_method_chk
  CHECK (method IN ('account','otp','voucher','social','pms','*'));
DELETE FROM public.schema_migrations WHERE version = '0028_phase5_poststay_throttle_method';
COMMIT;
