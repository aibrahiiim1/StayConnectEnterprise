-- 0007 rollback — drop the durable throttle table (additive; safe to remove).
DROP TABLE IF EXISTS public.auth_throttle_buckets;
DELETE FROM public.schema_migrations WHERE version = '0007_auth_throttle_buckets';
