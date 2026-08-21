-- OFFLINE FIRST ACTIVATION — the request half.
--
-- 0027 added offline_activation_packages: what Central ISSUES. This adds what Central RECEIVES.
--
-- An activation request is an appliance's own evidence about itself: its serial, its hardware, and the
-- public half of a keypair it generated locally, self-signed with the private half. Central stores it so the
-- package it later mints can be bound to that exact request -- which is what stops a package issued for one
-- physical box being applied to another that happens to share a serial, and what makes a stale request
-- unusable once its package has been generated.
--
-- The table was previously created at first use by the API. That is invisible to anyone reading the schema,
-- unversioned, and silently absent on a database restored from a dump taken before the first import.
CREATE TABLE IF NOT EXISTS offline_activation_requests (
  request_id   TEXT PRIMARY KEY,
  appliance_id UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  serial       TEXT NOT NULL,
  -- The identity public key the request proved possession of. Kept so the package's binding fingerprint is
  -- derived from what was actually verified at import, never from a value supplied later.
  public_key   TEXT NOT NULL,
  nonce        TEXT NOT NULL,
  imported_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  imported_by  UUID,
  -- Set when a package has been generated for this request. A consumed request cannot be answered twice.
  consumed_at  TIMESTAMPTZ);

CREATE INDEX IF NOT EXISTS offline_req_appl_idx
  ON offline_activation_requests(appliance_id, imported_at DESC);
-- Finding the outstanding request for an appliance is the hot path at package generation.
CREATE INDEX IF NOT EXISTS offline_req_open_idx
  ON offline_activation_requests(appliance_id) WHERE consumed_at IS NULL;
