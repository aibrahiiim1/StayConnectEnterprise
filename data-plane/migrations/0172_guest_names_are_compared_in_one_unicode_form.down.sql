-- Down for 0172: the SQL normaliser returns to its 0066 definition. The re-normalised *_norm values are canonically
-- equivalent to what they replaced, so nothing is restored: the compared form simply stays NFC, which both the old
-- and the new normaliser accept as input.
BEGIN;
CREATE OR REPLACE FUNCTION iam_v2.norm_identity_text(s text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
  SELECT CASE WHEN s IS NULL THEN NULL
              ELSE upper(regexp_replace(regexp_replace(s, '^\s+', ''), '\s+$', ''))
         END
$$;
COMMIT;
