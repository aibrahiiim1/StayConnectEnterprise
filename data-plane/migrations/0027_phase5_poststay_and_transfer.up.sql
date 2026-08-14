-- ============================================================================================================
-- PHASE 5 — MILESTONE 1: FOUNDATION + SECURITY (DARK)
--
-- Post-Stay identity and Cross-PMS transfer both already had TABLES: post_stay_profiles, stay_links and
-- entitlement_transfers were created in the Phase-1A base schema. What they did not have was anything that
-- makes them TRUE. post_stay_profiles held origin lineage and no PIN material at all; entitlement_transfers
-- carried the words "typed, cycle-safe" in a COMMENT, with a CHECK that only required two different Stays —
-- not two different PMS interfaces, not a terminated source, not an absent supersession pointer, and nothing
-- whatsoever about cycles. A comment is not an invariant.
--
-- This migration adds the guards, and it is deliberately additive: no table is dropped, no column is removed,
-- and exactly ONE existing Phase-3 object is modified (p3_stay_lifecycle_guard, which today raises
-- 'POST_STAY_ACTIVE transitions are Phase 5' — this is Phase 5, and the arm it names is added, with every
-- other arm left byte-identical). The full Phase-3 F1–F7 suite is re-run against it.
--
-- Everything here is DARK: no service holds privileges on any of it while the Phase-5 flags are off.
-- ============================================================================================================
BEGIN;

-- ------------------------------------------------------------------------------------------------------------
-- (1) POST-STAY IDENTITY — episode-bound, never room-owned.
--
-- The isolation primitive is NOT a new invention: it is the episode counter Phase 3 already made strict.
-- UNIQUE(origin_stay_id, origin_lifecycle_version) exists; a reinstatement increments lifecycle_version and
-- therefore ORPHANS the old profile by construction. That is what makes a PIN unusable by the room's next
-- occupant, and it is why nothing here keys on a room number — there is no room-keyed path to attack.
-- ------------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.post_stay_profiles
  ADD COLUMN created_at      timestamptz NOT NULL DEFAULT now(),
  -- Argon2id PHC only. The CHECK is not decoration: it makes storing a raw or weakly-hashed PIN
  -- REPRESENTATIONALLY impossible, so the "PIN is write-only" invariant cannot be lost by a future caller
  -- passing the wrong string. A guest PIN and a voucher code are different secrets with different rules; this
  -- column accepts exactly one shape.
  ADD COLUMN pin_hash        text NOT NULL DEFAULT '$argon2id$PLACEHOLDER'
    CHECK (pin_hash LIKE '$argon2id$%'),
  ADD COLUMN pin_generation  int NOT NULL DEFAULT 1 CHECK (pin_generation > 0),
  ADD COLUMN pin_set_at      timestamptz NOT NULL DEFAULT now(),
  -- ONE-TIME REVEAL. The plaintext is shown once, at issuance, and the fact that it was shown is durable.
  -- A second reveal of the SAME generation is refused; re-issuance mints a new generation, which is a
  -- different secret and an audited operator action.
  ADD COLUMN pin_revealed_at timestamptz,
  ADD COLUMN valid_until     timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
  ADD COLUMN status          text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','REVOKED')),
  ADD COLUMN revoked_at      timestamptz,
  ADD COLUMN revoked_by      uuid,
  ADD COLUMN revoke_reason   text,
  -- PROVENANCE OF ISSUANCE. There is no anonymous issuance path, and this column is how that is auditable
  -- rather than merely asserted: every profile records WHICH authenticated route created it.
  ADD COLUMN issued_via      text NOT NULL DEFAULT 'GUEST_AUTHENTICATED_SESSION'
    CHECK (issued_via IN ('GUEST_AUTHENTICATED_SESSION','OPERATOR_RESET')),
  ADD COLUMN issued_by_operator uuid,
  ADD COLUMN issued_at       timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT psp_revoked_coherent CHECK (
        (status = 'ACTIVE'  AND revoked_at IS NULL AND revoked_by IS NULL AND revoke_reason IS NULL)
     OR (status = 'REVOKED' AND revoked_at IS NOT NULL AND revoke_reason IS NOT NULL)),
  ADD CONSTRAINT psp_validity_window CHECK (valid_until > created_at),
  -- An operator-issued profile must name the operator; a guest-issued one must NOT, or "who issued this"
  -- stops being answerable from the row.
  ADD CONSTRAINT psp_issuer_coherent CHECK (
        (issued_via = 'OPERATOR_RESET'              AND issued_by_operator IS NOT NULL)
     OR (issued_via = 'GUEST_AUTHENTICATED_SESSION' AND issued_by_operator IS NULL));

-- The defaults above exist only so the columns can be added NOT NULL to an EMPTY table. Every one of them is
-- dropped immediately: a real caller must supply the PIN hash, the validity window and the issuance route,
-- and a row that inherits a placeholder secret would be a profile nobody minted.
ALTER TABLE iam_v2.post_stay_profiles
  ALTER COLUMN pin_hash    DROP DEFAULT,
  ALTER COLUMN valid_until DROP DEFAULT,
  ALTER COLUMN issued_via  DROP DEFAULT;

CREATE INDEX post_stay_profiles_origin ON iam_v2.post_stay_profiles (tenant_id, site_id, origin_stay_id);

-- ------------------------------------------------------------------------------------------------------------
-- (2) THE PHASE-5 CONTROLLED-WRITER BOUNDARY.
--
-- Phase 3 built this boundary and it works; what it does not do is know about tables that did not exist as
-- authoritative surfaces when it was written. Rather than editing the Phase-3 trigger function — which would
-- put every accepted Phase-3 family at risk to add two Phase-5 ones — Phase 5 brings its OWN opener and its
-- OWN guard, reusing the same unforgeable scope table and the same open-check.
--
-- p3_controlled_operation_open is family-generic already, so it is reused verbatim.
-- ------------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p5_begin_controlled_operation(p_family text) RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_token uuid;
BEGIN
  -- The allowlist IS the authorization decision, and it is also what keeps the GUC name below safe: by the
  -- time the family is concatenated it is never caller-shaped text.
  IF p_family NOT IN ('post_stay_identity','entitlement_transfer') THEN
    RAISE EXCEPTION 'no approved Phase-5 capability-scoped controlled-writer family %', p_family;
  END IF;
  DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour';
  v_token := gen_random_uuid();
  INSERT INTO iam_v2.controlled_operation_scope (txid, family, token)
  VALUES (txid_current(), p_family, v_token)
  ON CONFLICT (txid, family) DO UPDATE SET token = EXCLUDED.token, opened_at = now();
  PERFORM set_config('iam_v2.op_' || p_family, v_token::text, true);
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_begin_controlled_operation(text) FROM PUBLIC;

-- Phase 5 brings its OWN open-checker rather than calling the Phase-3 one, for a reason that only shows up
-- when the guard actually fires: p3_controlled_operation_open was written to keep PUBLIC EXECUTE precisely so
-- the guard could evaluate it as whatever role is attempting the write, but by migration 0026 the
-- least-privilege work has revoked that. Calling it from here would abort a non-owner's write with
-- "permission denied for function p3_controlled_operation_open" instead of the refusal that says what to do —
-- still fail-closed, but an error nobody can act on, which is exactly the outcome Phase 3's comment warned
-- about. This copy carries the grant it needs and cannot be affected by a change to the Phase-3 one.
--
-- SECURITY DEFINER because it reads the scope table, which is closed to every role but the opener's owner;
-- PUBLIC EXECUTE because the guard trigger runs as the writing role. Neither weakens anything: what a caller
-- learns by invoking it is whether IT has an open scope in ITS OWN transaction, which it already knows.
CREATE OR REPLACE FUNCTION iam_v2.p5_controlled_operation_open(p_family text) RETURNS boolean
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_tok text;
BEGIN
  v_tok := current_setting('iam_v2.op_' || p_family, true);   -- missing_ok: NULL when never set
  IF v_tok IS NULL OR v_tok = '' THEN
    RETURN false;
  END IF;
  RETURN EXISTS (
    SELECT 1 FROM iam_v2.controlled_operation_scope s
     WHERE s.txid = txid_current() AND s.family = p_family AND s.token::text = v_tok);
END $fn$;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.p5_controlled_writer_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE owner_role text; v_oid oid; v_cap text;
BEGIN
  v_cap := CASE
    WHEN TG_TABLE_NAME = 'post_stay_profiles'     THEN 'post_stay_identity'
    WHEN TG_TABLE_NAME IN ('entitlement_transfers','stay_links') THEN 'entitlement_transfer'
    ELSE NULL END;
  IF v_cap IS NULL THEN
    RAISE EXCEPTION 'p5_controlled_writer_only attached to an unmapped table % (fail closed)', TG_TABLE_NAME;
  END IF;
  -- Resolve the opener's owner INLINE from the catalog, exactly as the Phase-3 guard does: this trigger runs
  -- as whichever role is writing, so a cross-function EXECUTE dependency would break the dedicated-owner
  -- separation Gate P needs.
  v_oid := to_regprocedure('iam_v2.p5_begin_controlled_operation(text)');
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'Phase-5 controlled-writer opener is not resolvable (fail closed)';
  END IF;
  SELECT pg_get_userbyid(proowner) INTO owner_role FROM pg_proc WHERE oid = v_oid;
  IF owner_role IS NULL OR owner_role = '' THEN
    RAISE EXCEPTION 'Phase-5 controlled-writer owner is not resolvable (fail closed)';
  END IF;
  -- DELETE is checked too. An authoritative record that can be removed outside a declared operation is a
  -- record that can be made never to have happened.
  IF current_user <> owner_role AND NOT iam_v2.p5_controlled_operation_open(v_cap) THEN
    RAISE EXCEPTION
      '%: writes to the % family require an open controlled operation (caller %) — call iam_v2.p5_begin_controlled_operation(''%'') in the transaction that performs them',
      TG_TABLE_NAME, v_cap, current_user, v_cap;
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_controlled_writer_only() FROM PUBLIC;

CREATE TRIGGER p5_post_stay_profile_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.post_stay_profiles
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_controlled_writer_only();
CREATE TRIGGER p5_entitlement_transfer_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.entitlement_transfers
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_controlled_writer_only();
CREATE TRIGGER p5_stay_link_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.stay_links
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_controlled_writer_only();

-- ------------------------------------------------------------------------------------------------------------
-- (3) POST-STAY PROFILE INTEGRITY.
--
-- Issuance is legal only from a Stay that is currently IN_HOUSE (before checkout) or CHECKED_OUT (during
-- Checkout Grace) AT THE EPISODE THE PROFILE NAMES. A profile minted against a stale episode, a RESERVED
-- stay, or a CANCELLED/NO_SHOW stay is refused outright rather than created and later found unusable.
--
-- Origin lineage is READ-ONLY after insert (the contract's words), the PIN may only move FORWARD by
-- generation, and status may only travel ACTIVE → REVOKED. Nothing here can be walked backwards.
-- ------------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p5_post_stay_profile_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_status text; v_lifecycle int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    -- A profile is the durable answer to "who was allowed post-stay access, on whose authority". Revocation
    -- is a state, not an erasure.
    RAISE EXCEPTION 'post_stay_profiles rows are never deleted; revoke the profile instead';
  END IF;

  IF TG_OP = 'INSERT' THEN
    SELECT s.status, s.lifecycle_version INTO v_status, v_lifecycle
      FROM iam_v2.stays s
     WHERE s.tenant_id = NEW.tenant_id AND s.site_id = NEW.site_id AND s.id = NEW.origin_stay_id
     FOR UPDATE;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'post-stay profile origin Stay % is not in scope (fail closed)', NEW.origin_stay_id;
    END IF;
    IF v_lifecycle <> NEW.origin_lifecycle_version THEN
      RAISE EXCEPTION 'post-stay profile must name the CURRENT Stay episode (stay is at lifecycle_version %, profile names %)',
        v_lifecycle, NEW.origin_lifecycle_version;
    END IF;
    IF v_status NOT IN ('IN_HOUSE','CHECKED_OUT') THEN
      RAISE EXCEPTION 'post-stay profile may only be issued from an IN_HOUSE or CHECKED_OUT Stay (stay is %)', v_status;
    END IF;
    IF NEW.pin_revealed_at IS NOT NULL THEN
      RAISE EXCEPTION 'a post-stay profile is created unrevealed; the one-time reveal is a separate recorded act';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE. Identity and lineage are immutable.
  IF NEW.id                      IS DISTINCT FROM OLD.id
     OR NEW.tenant_id            IS DISTINCT FROM OLD.tenant_id
     OR NEW.site_id              IS DISTINCT FROM OLD.site_id
     OR NEW.origin_stay_id       IS DISTINCT FROM OLD.origin_stay_id
     OR NEW.origin_lifecycle_version IS DISTINCT FROM OLD.origin_lifecycle_version
     OR NEW.created_at           IS DISTINCT FROM OLD.created_at
     OR NEW.issued_at            IS DISTINCT FROM OLD.issued_at THEN
    RAISE EXCEPTION 'post-stay profile identity and origin lineage are read-only';
  END IF;
  IF OLD.status = 'REVOKED' AND NEW.status <> 'REVOKED' THEN
    RAISE EXCEPTION 'a revoked post-stay profile is never reactivated (issue a new one)';
  END IF;
  -- The PIN changes only by MINTING A NEW GENERATION, and a new generation is always unrevealed: a re-issue
  -- that inherited the previous reveal timestamp would silently consume the new secret's one-time reveal.
  IF NEW.pin_hash IS DISTINCT FROM OLD.pin_hash THEN
    IF NEW.pin_generation <> OLD.pin_generation + 1 THEN
      RAISE EXCEPTION 'a new post-stay PIN must increment pin_generation by exactly 1 (% -> %)',
        OLD.pin_generation, NEW.pin_generation;
    END IF;
    IF NEW.pin_revealed_at IS NOT NULL THEN
      RAISE EXCEPTION 'a newly minted post-stay PIN starts unrevealed';
    END IF;
  ELSIF NEW.pin_generation IS DISTINCT FROM OLD.pin_generation THEN
    RAISE EXCEPTION 'pin_generation may not change without a new PIN';
  END IF;
  -- ONE-TIME REVEAL: the timestamp may go NULL -> set, and never back, and never from one value to another.
  IF OLD.pin_revealed_at IS NOT NULL AND NEW.pin_revealed_at IS DISTINCT FROM OLD.pin_revealed_at
     AND NEW.pin_hash IS NOT DISTINCT FROM OLD.pin_hash THEN
    RAISE EXCEPTION 'a post-stay PIN is revealed exactly once per generation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_post_stay_profile_guard() FROM PUBLIC;
CREATE TRIGGER p5_post_stay_profile_guard
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.post_stay_profiles
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_post_stay_profile_guard();

-- ------------------------------------------------------------------------------------------------------------
-- (4) THE AUTHENTICABILITY PREDICATE — invariant I-2, in ONE place.
--
-- Post-Stay authentication is legal only while the origin Stay is STILL AT THE PROFILE'S EPISODE and has
-- actually checked out. Written once, as a function, so the runtime, the tests and any future caller are
-- asking the same question. Any of these makes it false, with no distinction visible to the guest:
--   * the profile is revoked or past its validity window;
--   * the Stay has moved to a NEW episode (a reinstatement) — this is what protects the next occupant;
--   * the Stay never reached checkout, or was cancelled / marked no-show after the PIN was issued.
-- ------------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p5_post_stay_authenticable(
    p_tenant uuid, p_site uuid, p_profile uuid) RETURNS boolean
  LANGUAGE sql STABLE SECURITY INVOKER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT EXISTS (
    SELECT 1
      FROM iam_v2.post_stay_profiles p
      JOIN iam_v2.stays s
        ON s.tenant_id = p.tenant_id AND s.site_id = p.site_id AND s.id = p.origin_stay_id
     WHERE p.tenant_id = p_tenant AND p.site_id = p_site AND p.id = p_profile
       AND p.status = 'ACTIVE'
       AND p.valid_until > now()
       AND s.lifecycle_version = p.origin_lifecycle_version
       AND s.status IN ('CHECKED_OUT','POST_STAY_ACTIVE'));
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_post_stay_authenticable(uuid,uuid,uuid) FROM PUBLIC;

-- ------------------------------------------------------------------------------------------------------------
-- (5) AUTH-CONTEXT COHERENCE FOR POST_STAY_PIN.
--
-- The PMS method pins the episode it authenticated against so a checkout or reinstatement inside the TTL
-- invalidates the context. Post-Stay needs the SAME protection for the same reason, so the pin is made
-- mandatory for this method rather than optional. The occupancy-evidence pin is deliberately NOT required:
-- post-stay identity is not proven from occupancy evidence, and demanding a field that carries no meaning
-- here would be a constraint that teaches callers to fill something in.
-- ------------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.auth_contexts
  ADD CONSTRAINT ac_post_stay_pins CHECK (
    method <> 'POST_STAY_PIN' OR (post_stay_profile_id IS NOT NULL AND pinned_lifecycle_version IS NOT NULL));

-- ------------------------------------------------------------------------------------------------------------
-- (6) THE STAY LIFECYCLE ARM THIS PHASE IS NAMED IN.
--
-- p3_stay_lifecycle_guard raises 'POST_STAY_ACTIVE transitions are Phase 5'. This is Phase 5. The function is
-- replaced with a copy that is IDENTICAL except for one added arm — CHECKED_OUT -> POST_STAY_ACTIVE — and the
-- comments that explain it. Every other arm, every message and every counter rule is unchanged, and the whole
-- Phase-3 F1–F7 suite is re-run against this version.
--
-- POST_STAY_ACTIVE has NO exit, and that is the FINAL contract's own state diagram (§17: RESERVED -> IN_HOUSE
-- -> CHECKED_OUT -> (POST_STAY_ACTIVE); reinstatement is CHECKED_OUT -> IN_HOUSE). No exit arm is invented
-- here. A reinstatement event arriving for a POST_STAY_ACTIVE Stay is therefore REFUSED by this guard and the
-- event lands in the operator's queue rather than silently rewriting the episode — see the limitation recorded
-- for Product-Owner review.
-- ------------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_lifecycle_guard() RETURNS trigger
  LANGUAGE plpgsql
  SET search_path = iam_v2, pg_temp
  AS $fn$
DECLARE allowed boolean; is_reinstate boolean; evidence_changed boolean;
BEGIN
  IF NEW.last_applied_event_version < OLD.last_applied_event_version THEN
    RAISE EXCEPTION 'stays.last_applied_event_version cannot decrease (% -> %)',
      OLD.last_applied_event_version, NEW.last_applied_event_version;
  END IF;

  IF NEW.occupancy_evidence_version < OLD.occupancy_evidence_version THEN
    RAISE EXCEPTION 'stays.occupancy_evidence_version cannot decrease (% -> %)',
      OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
  END IF;
  evidence_changed := (
       NEW.occupancy_evidence_at          IS DISTINCT FROM OLD.occupancy_evidence_at
    OR NEW.occupancy_revision_id          IS DISTINCT FROM OLD.occupancy_revision_id
    OR NEW.occupancy_normalization_version IS DISTINCT FROM OLD.occupancy_normalization_version
    OR NEW.occupancy_clock_suspect        IS DISTINCT FROM OLD.occupancy_clock_suspect);
  IF evidence_changed THEN
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version + 1 THEN
      RAISE EXCEPTION 'a material occupancy-evidence change must increment occupancy_evidence_version by exactly 1 (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  ELSE
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version THEN
      RAISE EXCEPTION 'stays.occupancy_evidence_version may not change without a material occupancy-evidence change (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  END IF;

  is_reinstate := (OLD.status = 'CHECKED_OUT' AND NEW.status = 'IN_HOUSE');

  IF NEW.effective_checkout_at IS DISTINCT FROM OLD.effective_checkout_at THEN
    IF is_reinstate THEN
      IF NEW.effective_checkout_at IS NOT NULL THEN
        RAISE EXCEPTION 'reinstatement must CLEAR effective_checkout_at (starts a new episode)';
      END IF;
    ELSIF OLD.status = 'IN_HOUSE' AND NEW.status = 'CHECKED_OUT' THEN
      IF NEW.effective_checkout_at IS NULL THEN
        RAISE EXCEPTION 'checkout must SET effective_checkout_at';
      END IF;
    ELSE
      RAISE EXCEPTION 'effective_checkout_at is immutable within an episode (% -> %, status % -> %)',
        OLD.effective_checkout_at, NEW.effective_checkout_at, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.lifecycle_version <> OLD.lifecycle_version THEN
    IF NOT (NEW.lifecycle_version = OLD.lifecycle_version + 1 AND is_reinstate) THEN
      RAISE EXCEPTION 'stays.lifecycle_version may increment by exactly 1 ONLY during a CHECKED_OUT->IN_HOUSE reinstatement (% -> %, % -> %)',
        OLD.lifecycle_version, NEW.lifecycle_version, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.status <> OLD.status THEN
    allowed := CASE
      WHEN OLD.status='RESERVED'    AND NEW.status IN ('IN_HOUSE','CANCELLED','NO_SHOW') THEN true
      WHEN OLD.status='IN_HOUSE'    AND NEW.status IN ('CHECKED_OUT')                    THEN true
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('IN_HOUSE')                       THEN true  -- reinstatement
      -- PHASE 5: the post-stay conversion, and the only arm this migration adds. It stays within the episode
      -- (lifecycle_version unchanged, enforced above) and keeps the immutable checkout boundary (unchanged,
      -- enforced above), so a post-stay Stay is still the same episode that checked out.
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('POST_STAY_ACTIVE')               THEN true
      ELSE false END;
    IF NOT allowed THEN
      RAISE EXCEPTION 'illegal stays.status transition % -> %', OLD.status, NEW.status;
    END IF;
    IF is_reinstate AND NEW.lifecycle_version <> OLD.lifecycle_version + 1 THEN
      RAISE EXCEPTION 'reinstatement (CHECKED_OUT->IN_HOUSE) must increment lifecycle_version exactly once';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_stay_lifecycle_guard() FROM PUBLIC;

-- ------------------------------------------------------------------------------------------------------------
-- (7) CROSS-PMS TRANSFER — the invariants the comment claimed.
--
-- What existed: two UNIQUE columns and two CHECKs, one of which required only that the two Stays differ.
-- Two Stays on the SAME interface differ — that is an ordinary room move, and it would have been recordable
-- as a transfer. What follows is the difference between a typed relationship and a pair of columns.
-- ------------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p5_entitlement_transfer_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_from_iface uuid; v_to_iface uuid;
  v_from_stay_of_ent uuid; v_to_stay_of_ent uuid;
  v_from_status text; v_from_reason text; v_to_supersedes uuid;
  v_cursor uuid; v_hops int := 0;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    -- Lineage is a historical fact. A transfer that can be edited or removed is a lineage that cannot be
    -- trusted to answer "where did this access come from".
    RAISE EXCEPTION 'entitlement_transfers is append-only (attempted %)', TG_OP;
  END IF;

  IF NEW.reason <> 'CROSS_PMS_TRANSFER' THEN
    RAISE EXCEPTION 'entitlement_transfers records CROSS_PMS_TRANSFER only (got %)', NEW.reason;
  END IF;

  -- (a) TWO DIFFERENT PMS INTERFACES. This is the line between a transfer and a room move, and until now
  --     nothing enforced it.
  SELECT pms_interface_id INTO v_from_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.from_stay_id;
  SELECT pms_interface_id INTO v_to_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.to_stay_id;
  IF v_from_iface IS NULL OR v_to_iface IS NULL THEN
    RAISE EXCEPTION 'both transfer Stays must exist in scope (fail closed)';
  END IF;
  IF v_from_iface = v_to_iface THEN
    RAISE EXCEPTION 'a cross-PMS transfer requires two DIFFERENT PMS interfaces; same-interface movement is a room move, not a transfer';
  END IF;

  -- (b) The entitlements must actually belong to the Stays named. Without this the lineage could point at a
  --     real pair of entitlements and a fictional pair of Stays.
  SELECT stay_id, status, terminal_reason INTO v_from_stay_of_ent, v_from_status, v_from_reason
    FROM iam_v2.entitlements
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.from_entitlement_id;
  SELECT stay_id, supersedes_entitlement_id INTO v_to_stay_of_ent, v_to_supersedes
    FROM iam_v2.entitlements
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.to_entitlement_id;
  IF v_from_stay_of_ent IS DISTINCT FROM NEW.from_stay_id THEN
    RAISE EXCEPTION 'the source entitlement does not belong to the source Stay';
  END IF;
  IF v_to_stay_of_ent IS DISTINCT FROM NEW.to_stay_id THEN
    RAISE EXCEPTION 'the destination entitlement does not belong to the destination Stay';
  END IF;

  -- (c) STATE COUPLING. A transfer row whose source is still live would claim the access moved while it is
  --     still being served in the old place.
  IF v_from_status <> 'TERMINATED' OR v_from_reason IS DISTINCT FROM 'TRANSFERRED' THEN
    RAISE EXCEPTION 'the source entitlement must be TERMINATED with reason TRANSFERRED (is % / %)',
      v_from_status, v_from_reason;
  END IF;

  -- (d) A TRANSFER IS NOT A SUPERSESSION. Supersession is same-subject; a transfer crosses Stays, which is
  --     exactly why it needs its own typed relationship. Both must never be asserted for one entitlement.
  IF v_to_supersedes IS NOT NULL THEN
    RAISE EXCEPTION 'a transfer-created entitlement carries no supersedes_entitlement_id (supersession is same-subject)';
  END IF;

  -- (e) CYCLE-FREE. from/to are UNIQUE, so the lineage is a set of chains; a cycle exists exactly when the
  --     source is reachable by walking FORWARD from the destination. The hop bound is a fail-closed backstop:
  --     a chain longer than this is not a legitimate guest movement and must not spin a trigger.
  v_cursor := NEW.to_entitlement_id;
  LOOP
    v_hops := v_hops + 1;
    IF v_hops > 64 THEN
      RAISE EXCEPTION 'transfer lineage exceeds the supported depth (fail closed)';
    END IF;
    SELECT to_entitlement_id INTO v_cursor FROM iam_v2.entitlement_transfers
     WHERE from_entitlement_id = v_cursor;
    EXIT WHEN v_cursor IS NULL;
    IF v_cursor = NEW.from_entitlement_id THEN
      RAISE EXCEPTION 'transfer would create a cycle in the entitlement lineage';
    END IF;
  END LOOP;

  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_entitlement_transfer_guard() FROM PUBLIC;
CREATE TRIGGER p5_entitlement_transfer_guard
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.entitlement_transfers
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_entitlement_transfer_guard();

-- ------------------------------------------------------------------------------------------------------------
-- (8) STAY LINKS — grounded ends only.
--
-- stay_links declares BOTH ends NOT NULL against stays. A Cross-PMS transfer has two real Stays and is what
-- §7.4 of the FINAL contract names this table for. Post-Stay has exactly ONE real Stay: the origin. Writing a
-- POST_STAY link would require inventing a destination Stay — synthetic PMS state — and the contract grounds
-- post-stay lineage elsewhere (§7.3: "profiles are unique per episode with read-only origin lineage"), which
-- post_stay_profiles already provides.
--
-- So the enum value stays reserved and this guard refuses it, rather than leaving a shape that a later caller
-- could fill in with something that is not true.
-- ------------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p5_stay_link_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_from_iface uuid; v_to_iface uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    RAISE EXCEPTION 'stay_links is append-only (attempted %)', TG_OP;
  END IF;
  IF NEW.reason = 'POST_STAY' THEN
    RAISE EXCEPTION 'stay_links(POST_STAY) is not written: post-stay has one real Stay and its lineage is post_stay_profiles.origin_stay_id (contract 7.3)';
  END IF;
  IF NEW.from_stay = NEW.to_stay THEN
    RAISE EXCEPTION 'a stay link joins two different Stays';
  END IF;
  SELECT pms_interface_id INTO v_from_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.from_stay;
  SELECT pms_interface_id INTO v_to_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.to_stay;
  IF v_from_iface IS NULL OR v_to_iface IS NULL THEN
    RAISE EXCEPTION 'both linked Stays must exist in scope (fail closed)';
  END IF;
  IF v_from_iface = v_to_iface THEN
    RAISE EXCEPTION 'a CROSS_PMS_TRANSFER link requires two DIFFERENT PMS interfaces';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_stay_link_guard() FROM PUBLIC;
CREATE TRIGGER p5_stay_link_guard
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.stay_links
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_stay_link_guard();

-- ------------------------------------------------------------------------------------------------------------
-- (9) DARK. No runtime service role receives any privilege on any Phase-5 object. Least privilege for Phase 5
--     is DERIVED later from an actual privilege audit against the delivered surface, not guessed here.
-- ------------------------------------------------------------------------------------------------------------

INSERT INTO public.schema_migrations (version)
  VALUES ('0027_phase5_poststay_and_transfer') ON CONFLICT DO NOTHING;

COMMIT;
