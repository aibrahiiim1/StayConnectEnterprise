-- 0087 — THE OPERATOR SURFACE A VOUCHER NEEDS, AND THE AUDIT A RECOVERABLE SECRET NEEDS.
--
-- Nothing in the product could issue an IAM-v2 voucher. scd registers POST /v1/vouchers/issue and no caller
-- exists anywhere: no edged route, no Hotel Admin page, no CLI. The handler's own header says "edged
-- proxies to this route"; it does not. So this migration is the database half of the surface that makes the
-- VOUCHER authentication method -- enabled by flag, structurally incapable of succeeding -- actually
-- reachable by the property that owns it.
--
-- THREE THINGS, AND ONE ASYMMETRY THAT DECIDES THE THIRD.
--
-- 1. iam_v2.vouchers gains created_at and issued_by. A list an operator can use needs to say WHEN a card
--    was made and WHO made it, and the table said neither.
--
--    CORRECTION, MADE AFTER THIS MIGRATION WAS APPLIED TO PRE-LIVE. The sentence here originally read
--    "Safe as a plain ALTER: the table holds zero rows in production (iam_v2 is live-DARK), so DEFAULT now()
--    invents no history." THE ROW COUNT WAS WRONG. iam_v2.vouchers held EIGHT rows on the appliance, from
--    earlier live authentication testing, and DEFAULT now() therefore stamped all eight with the migration's
--    own timestamp. Their created_at is when 0087 ran, not when they were issued. The ALTER was still safe
--    -- NOT NULL with a DEFAULT cannot fail on existing rows -- so nothing broke; what the claim got wrong
--    was the honesty of the data it produced.
--
--    The consequence, stated so it is not rediscovered as a bug: for those eight vouchers created_at is a
--    floor, not a fact. It is not backfilled, because there is nothing truthful to backfill it FROM -- no
--    other column records their issue time. All eight were revoked during the same closure work, so no card
--    whose created_at is synthetic is redeemable.
--
--    WHY THE CLAIM WAS MADE AT ALL: "iam_v2 is live-DARK" was read as "iam_v2 is empty", and DARK means
--    unreached by guests, not unwritten. The rows had been created by admin-side testing, which is exactly
--    the traffic a dark schema does receive. A row count is one query; it was asserted instead of run.
--
-- 2. iam_v2.voucher_code_reveals — the append-only record of every time a code was recovered in the clear.
--
--    THE ASYMMETRY. Both existing one-time-secret precedents in this system are HASHED: the post-stay PIN
--    and the guest-account password. "Shown once" is free for them -- re-revealing is arithmetically
--    impossible, so the crypto enforces the rule and the audit merely describes it. A voucher code is
--    ENCRYPTED and RECOVERABLE (code_ciphertext, code_nonce, opened with the DEK). It is the first
--    recoverable guest secret in the system, so single-use is NOT enforced by the crypto and cannot be:
--    whoever holds the DEK can open the same code again tomorrow. What makes that safe is that they cannot
--    do it UNSEEN. The audit is therefore load-bearing here in a way it is not for a hash, which is why it
--    is a table with a trigger rather than a log line.
--
--    THERE IS NO CODE COLUMN, AND THERE WILL NOT BE ONE. A record that a code was read is evidence; a
--    record of the code itself is a second copy of the secret, in a table built never to be deleted from.
--
-- 3. iam_v2.voucher_revoke(...) — a SECURITY DEFINER burn, for exactly the reason 0084 exists.
--
--    svc_scd holds SELECT and INSERT on iam_v2.vouchers and deliberately NOT UPDATE: 0084 moved the
--    redemption burn into the entitlement kernel rather than grant it, and asserts at the end that svc_scd
--    still cannot write voucher state. Revocation is the same shape of problem -- one narrow state
--    transition, needed by one code path -- so it gets the same answer. A blanket UPDATE grant would let
--    any statement in the process set any voucher to any state.
--
-- WHAT THIS DOES NOT ADD, deliberately:
--   * No REDEMPTION_EXPIRED writer. The redemption window is evaluated at authentication time
--     (internal/iamv2/repo_pg.go: UNUSED and now inside [valid_from, valid_until)), so a stored
--     REDEMPTION_EXPIRED would be a denormalisation of a computed fact -- two answers to one question, and
--     the stored one always the staler.
--   * No UPDATE grant to svc_scd, for the reason given in 3 above: one narrow transition gets one kernel.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. What a voucher list has to be able to say
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.vouchers
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
  -- The operator the SERVER resolved from the session, never a name from a request body. The FK is what
  -- makes that enforceable rather than conventional. NULL is allowed because a voucher may be minted by a
  -- path with no operator (there is none today, and inventing a NOT NULL for a caller that does not exist
  -- would force a fake operator id the first time one appears).
  ADD COLUMN issued_by uuid REFERENCES public.operators (id);

COMMENT ON COLUMN iam_v2.vouchers.created_at IS
  'When this voucher was minted. Not the redemption window: see redemption_valid_from/_until, which are the '
  'credential-validity bounds the authenticator enforces.';
COMMENT ON COLUMN iam_v2.vouchers.issued_by IS
  'The authenticated operator who issued it, as the server resolved them from the session.';

CREATE INDEX vouchers_created_lookup ON iam_v2.vouchers (tenant_id, site_id, created_at DESC);
-- batch_id has existed since mg3 and has never been written. Issuance now mints one per call, so a
-- printed batch is addressable as a batch -- which is what an export selection and a bulk revoke need.
CREATE INDEX vouchers_batch_lookup ON iam_v2.vouchers (tenant_id, site_id, batch_id)
  WHERE batch_id IS NOT NULL;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The append-only reveal/export audit
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE iam_v2.voucher_code_reveals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  -- NULL for an EXPORT: an export names a SELECTION, not one voucher, and recording the first of two
  -- hundred rows would be a worse record than recording none.
  voucher_id uuid,
  action text NOT NULL CHECK (action IN ('REVEAL','EXPORT')),
  -- 1 for a reveal; the size of the selection for an export.
  voucher_count integer NOT NULL CHECK (voucher_count >= 1),
  -- What was asked for, as asked for (batch, state filter). Evidence for "who took a copy of which cards".
  selection jsonb,
  revealed_at timestamptz NOT NULL DEFAULT now(),
  operator_id uuid NOT NULL REFERENCES public.operators (id),
  -- A human-readable copy, derived from the resolved operator, so the evidence survives a rename.
  operator_label text NOT NULL CHECK (length(btrim(operator_label)) > 0),
  -- MANDATORY and BOUNDED, the same rule the post-stay actions carry: an unexplained reveal is
  -- indistinguishable afterwards from an unauthorised one, and a cap keeps a free-text field from becoming
  -- somewhere to store something else.
  reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 4 AND 500),
  CONSTRAINT vcr_reveal_names_one_voucher
    CHECK ((action = 'REVEAL' AND voucher_id IS NOT NULL AND voucher_count = 1)
        OR (action = 'EXPORT')),
  FOREIGN KEY (tenant_id, site_id, voucher_id) REFERENCES iam_v2.vouchers (tenant_id, site_id, id)
);

COMMENT ON TABLE iam_v2.voucher_code_reveals IS
  'Append-only record of every recovery of a voucher code in the clear. A voucher code is encrypted and '
  'RECOVERABLE, unlike the hashed post-stay PIN and guest-account password, so "shown once" cannot be '
  'enforced by the crypto and is enforced by visibility instead. There is deliberately no column that '
  'could hold a code.';

CREATE INDEX vcr_lookup ON iam_v2.voucher_code_reveals (tenant_id, site_id, revealed_at DESC);
CREATE INDEX vcr_by_voucher ON iam_v2.voucher_code_reveals (tenant_id, site_id, voucher_id)
  WHERE voucher_id IS NOT NULL;

CREATE OR REPLACE FUNCTION iam_v2.voucher_code_reveals_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.voucher_code_reveals is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.voucher_code_reveals_append_only() FROM PUBLIC;

CREATE TRIGGER voucher_code_reveals_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.voucher_code_reveals
  FOR EACH ROW EXECUTE FUNCTION iam_v2.voucher_code_reveals_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Revocation, as a kernel rather than as a grant
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.voucher_revoke(
    p_tenant uuid, p_site uuid, p_voucher uuid, p_operator uuid, p_reason text)
  RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE v_n integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;
  -- UNUSED only. A REDEEMED voucher has already granted an entitlement, and revoking the card would not
  -- take that entitlement back -- so answering "revoked" would be a false statement about access. The
  -- entitlement is ended through the session/entitlement surface, which is a different action.
  UPDATE iam_v2.vouchers
     SET state = 'REVOKED'
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher AND state = 'UNUSED';
  GET DIAGNOSTICS v_n = ROW_COUNT;
  -- Not a silent no-op: the same reasoning as 0084's burn. "Nothing happened" and "it was already spent"
  -- are different answers and the caller must be able to tell them apart.
  IF v_n <> 1 THEN
    RAISE EXCEPTION 'VOUCHER_NOT_REVOCABLE: no UNUSED voucher % for tenant %/site %', p_voucher, p_tenant, p_site
      USING ERRCODE = 'check_violation';
  END IF;
END $$;

-- GUARDED, and the first version of this migration was not.
--
-- A bare `ALTER FUNCTION ... OWNER TO iam_v2_owner` aborts the WHOLE migration on any database that has no
-- such role -- every disposable fixture, every scratch reconstruction, every clean-install verification --
-- and because the migration is one transaction, nothing applies at all. That is exactly what happened when
-- this was first run against a disposable PostgreSQL: `ERROR: role "iam_v2_owner" does not exist`, and the
-- reveal table, the columns and the function all silently rolled back. 0063 and 0067 already wrap the same
-- statement for the same reason; this one had simply not copied them.
--
-- The ownership is NOT optional where it matters. SECURITY DEFINER means the function runs as its owner, so
-- on the appliance it must be iam_v2_owner or the kernel has no more privilege than its caller and the
-- revoke cannot write a table svc_scd may not write. Where the role exists, this runs.
DO $ownfn$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_revoke(uuid, uuid, uuid, uuid, text) OWNER TO iam_v2_owner';
  END IF;
END $ownfn$;

REVOKE EXECUTE ON FUNCTION iam_v2.voucher_revoke(uuid, uuid, uuid, uuid, text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 4. ROTATION, which the design expressed and nothing could perform
-- ---------------------------------------------------------------------------------------------------------
-- iam_v2.voucher_code_key_generations carries superseded_at, issuance reads it to find the ACTIVE
-- generation, and no code path had ever written it. So rotation was expressible in the schema and
-- impossible in the product: a per-generation blind-index key, once created, was the key forever.
--
-- WHY THE SCHEMA BOTHERS. A voucher pins the generation that indexed it, which is what makes rotation
-- meaningful at all: superseding a generation leaves every unredeemed voucher still redeemable under the
-- key it was sealed with, while every NEW code is drawn under a fresh one. A single raw key could not
-- express that -- rotating it would invalidate every printed card in the building.
--
-- A KERNEL, NOT A GRANT, and deploy/gatep/svc-voucher-iamv2-grants.sql already said why before this
-- existed: "UPDATE/DELETE on voucher_code_key_generations to anyone" is withheld because "superseding is a
-- lifecycle action that sets superseded_at and belongs to a deliberate rotation path, not to routine
-- issuance, and deleting one would orphan every voucher that pins it". This is that deliberate path.
ALTER TABLE iam_v2.voucher_code_key_generations
  ADD COLUMN superseded_by uuid REFERENCES public.operators (id),
  ADD COLUMN supersede_reason text;

COMMENT ON COLUMN iam_v2.voucher_code_key_generations.superseded_by IS
  'The authenticated operator who retired this generation, as the server resolved them from the session.';

CREATE OR REPLACE FUNCTION iam_v2.voucher_code_generation_supersede(
    p_tenant uuid, p_site uuid, p_generation uuid, p_operator uuid, p_reason text)
  RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE v_n integer; v_no integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'VOUCHER_ROTATION_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'VOUCHER_ROTATION_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;
  -- Serialise against a concurrent rotation of the same site, so two operators cannot retire two
  -- generations and leave issuance choosing between them by ordering luck.
  PERFORM pg_advisory_xact_lock(hashtext('voucher_code_generations'), hashtext(p_site::text));
  UPDATE iam_v2.voucher_code_key_generations
     SET superseded_at = now(), superseded_by = p_operator,
         supersede_reason = btrim(p_reason)
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_generation
     AND superseded_at IS NULL
  RETURNING generation_no INTO v_no;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  -- Not a silent no-op: "there is no such active generation" and "it was already retired" are different
  -- answers, and an operator who cannot tell them apart will rotate twice.
  IF v_n <> 1 THEN
    RAISE EXCEPTION 'VOUCHER_GENERATION_NOT_ACTIVE: no active generation % for tenant %/site %',
      p_generation, p_tenant, p_site USING ERRCODE = 'check_violation';
  END IF;
  RETURN v_no;
END $$;

DO $rotfn$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_code_generation_supersede(uuid, uuid, uuid, uuid, text) '
            'OWNER TO iam_v2_owner';
  END IF;
END $rotfn$;

REVOKE EXECUTE ON FUNCTION
  iam_v2.voucher_code_generation_supersede(uuid, uuid, uuid, uuid, text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Grants. MIRRORED INTO deploy/gatep/svc-voucher-iamv2-grants.sql in the same commit.
--
-- gatep-grants.sql revokes all privileges from the service roles and runs AFTER the numbered migrations, so
-- a grant that exists only here does not survive a factory-clean install. Forty grants were lost that way
-- and nine of them were broken on the live appliance; tools/validate-migration-grant-durability.py refuses
-- this file if the mirror is missing.
-- ---------------------------------------------------------------------------------------------------------
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT ON iam_v2.voucher_code_reveals TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.voucher_revoke(uuid, uuid, uuid, uuid, text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION
      iam_v2.voucher_code_generation_supersede(uuid, uuid, uuid, uuid, text) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- READ ONLY, and nothing else. edged must be able to SHOW the reveal history on the screen that
    -- performs a reveal -- "who has already taken a copy of this batch" is the question the audit exists to
    -- answer, and hiding it from the only surface an operator uses would make the record ceremonial. It
    -- gains no privilege on vouchers themselves: it still proxies every code-touching operation to scd,
    -- because scd owns the DEK.
    GRANT SELECT ON iam_v2.voucher_code_reveals TO svc_edged;
  END IF;
END $$;

-- The boundary the grant file withheld, re-asserted: rotation goes through the kernel, so no role may
-- UPDATE the generations table directly. A grant here would let any statement in the process retire any
-- generation, including a race that retires two.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd')
     AND has_table_privilege('svc_scd', 'iam_v2.voucher_code_key_generations', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd must not hold UPDATE on iam_v2.voucher_code_key_generations: rotation is '
                    'iam_v2.voucher_code_generation_supersede';
  END IF;
END $$;

-- The boundary 0084 asserted, re-asserted: this migration must not have widened it.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd')
     AND has_table_privilege('svc_scd', 'iam_v2.vouchers', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd must not hold UPDATE on iam_v2.vouchers: the burn is in the entitlement '
                    'kernel (0084) and revocation is in iam_v2.voucher_revoke (this migration)';
  END IF;
END $$;

COMMIT;
