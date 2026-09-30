-- SITE TYPE AND LICENCE MODULES.
--
-- sites.site_type is descriptive metadata Central owns (HOTEL, CAFE, OFFICE, ...). It is signed into the
-- appliance's assignment so the appliance can present itself sensibly, and it authorises NOTHING: licence
-- modules do. The CHECK is a shape, not a list, so a later type needs no migration; the API decides which
-- values it accepts for writes. Existing sites become 'UNSPECIFIED' through the column default.
--
-- licenses.modules is the queryable projection of the signed v4 licence's module ids. It is read back when a
-- licence is re-signed with its terms preserved (suspend, resume, WAN-MAC rebind, move), so a re-issue never
-- loses a module. Existing rows get '{}' (core only): no module is invented for a licence issued before
-- modules existed.

BEGIN;

ALTER TABLE sites ADD COLUMN IF NOT EXISTS site_type text NOT NULL DEFAULT 'UNSPECIFIED';
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_site_type_shape;
ALTER TABLE sites ADD CONSTRAINT sites_site_type_shape CHECK (site_type ~ '^[A-Z][A-Z0-9_]{1,31}$');

COMMENT ON COLUMN sites.site_type IS
  'Descriptive site type (HOTEL, CAFE, ...); UNSPECIFIED when not set. Signed into the assignment. Authorises nothing.';

ALTER TABLE licenses ADD COLUMN IF NOT EXISTS modules text[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN licenses.modules IS
  'Module ids the signed v4 licence authorises (projection of the envelope). Empty = core only. Carried on every preserved-terms re-issue.';

COMMIT;
