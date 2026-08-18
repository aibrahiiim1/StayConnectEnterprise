-- IAM-v2 voucher issuance and redemption: minimum privileges.
--
-- Derived from the two code paths that touch this material:
--   * edged (issuance)      -- cmd/edged/voucher_issue_iamv2.go
--   * scd   (redemption)    -- cmd/scd/voucher_keys.go + internal/iamv2/repo_pg.go
--
-- The key generation row holds hmac_key_ciphertext, which is the sealed blind-index key. Both services must
-- READ it (edged to index a new code, scd to index a submitted one); only edged CREATES a generation. No
-- service may UPDATE or DELETE a generation: superseding is a lifecycle action that sets superseded_at and
-- belongs to a deliberate rotation path, not to routine issuance, and deleting one would orphan every
-- voucher that pins it.
-- ISSUANCE RUNS IN scd, NOT edged, because scd owns the DEK: scd is root and unix-socket only, edged is the
-- unprivileged HTTP service and merely proxies the admin route. So the write privileges belong to svc_scd,
-- and svc_edged needs nothing here at all -- it never touches this material.
--
-- (An earlier draft of this file granted issuance to svc_edged, because the route terminates there. The
-- route terminating somewhere is not a reason to put the key there; the grants follow the code, and the code
-- follows the key.)
GRANT SELECT, INSERT ON iam_v2.voucher_code_key_generations TO svc_scd;
GRANT SELECT, INSERT ON iam_v2.vouchers                     TO svc_scd;

-- NOT granted, deliberately:
--   * anything on either table to svc_edged -- it proxies and never reads voucher material;
--   * UPDATE/DELETE on voucher_code_key_generations to anyone -- superseding a generation is its own
--     deliberate, audited rotation action, and deleting one would orphan every voucher that pins it;
--   * DELETE on vouchers -- an issued voucher is revoked by state, never erased;
--   * any read of the DEK itself, which lives in the appliance secret store and never in the database. The
--     database holds only material sealed UNDER that key, so a database compromise alone yields no code.
