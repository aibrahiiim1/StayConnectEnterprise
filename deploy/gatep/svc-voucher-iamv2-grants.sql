-- IAM-v2 voucher issuance and redemption: minimum privileges.
--
-- Derived from the two code paths that touch this material:
--   * scd   (issuance)      -- cmd/scd/voucher_issue_iamv2.go
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

-- ---- the code format issuance reads (migration 0085) ------------------------------------------------
-- scd reads the site's voucher code format at issuance, through the reader function and nothing else. It
-- holds no privilege on either settings table: the format is chosen by an operator through edged, and
-- issuance only needs to be told what was chosen.
--
-- Mirrored here rather than left in 0085 alone. gatep-grants.sql revokes all privileges from the service
-- roles and runs AFTER the numbered migrations, so a grant that exists only in a migration does not survive
-- a factory-clean install -- and issuance is written to REFUSE rather than guess a format, so losing this
-- grant does not produce a default, it produces a refusal to issue.
GRANT EXECUTE ON FUNCTION iam_v2.voucher_code_settings_get(uuid,uuid) TO svc_scd;

-- ---- the operator surface (migration 0086) ----------------------------------------------------------
-- REVEAL AND EXPORT RUN IN scd, for the same reason issuance does: the DEK is there. The audit row and the
-- code recovery are written in ONE transaction, so scd needs INSERT as well as SELECT -- a reveal that
-- could succeed while its record failed is the one outcome this table exists to prevent.
GRANT SELECT, INSERT ON iam_v2.voucher_code_reveals TO svc_scd;

-- REVOCATION IS A KERNEL, NOT A GRANT. svc_scd still holds no UPDATE on iam_v2.vouchers: 0084 moved the
-- redemption burn into the entitlement kernel rather than widen that privilege, and 0086 does the same for
-- revocation. One narrow transition, needed by one code path, expressed as a SECURITY DEFINER function
-- instead of a blanket UPDATE that would let any statement in the process set any voucher to any state.
GRANT EXECUTE ON FUNCTION iam_v2.voucher_revoke(uuid, uuid, uuid, uuid, text) TO svc_scd;

-- edged READS THE AUDIT AND NOTHING ELSE. "Who has already taken a copy of these cards" is the question the
-- reveal record exists to answer, and hiding it from the only screen an operator uses would make the record
-- ceremonial. This is still the ONLY voucher-domain privilege svc_edged holds: it proxies every operation
-- that touches a code, because scd owns the key.
GRANT SELECT ON iam_v2.voucher_code_reveals TO svc_edged;

-- NOT granted, deliberately:
--   * UPDATE or DELETE on iam_v2.voucher_code_reveals to anyone. The append-only trigger refuses both, and
--     a privilege that is only ever refused is a privilege waiting for the trigger to be dropped.
--   * anything on iam_v2.vouchers to svc_edged -- including SELECT. The list it shows comes from scd.

-- ---- rotation (migration 0087) ----------------------------------------------------------------------
-- The comment at the top of this file has always said that superseding a generation "belongs to a
-- deliberate rotation path, not to routine issuance". Until 0087 there WAS no such path: superseded_at was
-- read by issuance to find the active generation and written by nothing, so a per-generation key was the
-- key forever. This is that path, and it is a kernel for the reason the withheld UPDATE explains -- a
-- blanket UPDATE would let any statement retire any generation, including two at once.
GRANT EXECUTE ON FUNCTION
  iam_v2.voucher_code_generation_supersede(uuid, uuid, uuid, uuid, text) TO svc_scd;

-- STILL NOT granted: UPDATE or DELETE on iam_v2.voucher_code_key_generations, to anyone. 0087 asserts it.
