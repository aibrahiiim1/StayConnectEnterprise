package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
)

// AssignmentBase issues vendor-signed appliance-assignment documents and serves
// the current one to the authenticated appliance. Signing reuses the vendor key
// the appliance already trusts for licenses/offline packages.
type AssignmentBase struct {
	*Base
	SignKey ed25519.PrivateKey
}

// assignTTLSeconds is the fleet-wide assignment refresh horizon (0 = none).
// Reaching it marks an appliance's assignment STALE; it never unassigns it.
func assignTTLSeconds() int64 {
	v, err := strconv.ParseInt(os.Getenv("CTRLAPI_ASSIGN_TTL_SECONDS"), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func assignUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// identityFprFromPubB64 computes the appliance identity-key fingerprint the same
// way the edge does: hex(sha256(pub)[:8]) over the raw Ed25519 public key.
func identityFprFromPubB64(pubB64 string) string {
	raw, err := base64.RawStdEncoding.DecodeString(pubB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		// tolerate std-padding just in case
		if r2, e2 := base64.StdEncoding.DecodeString(pubB64); e2 == nil && len(r2) == ed25519.PublicKeySize {
			raw = r2
		} else {
			return ""
		}
	}
	return assignment.KeyID(ed25519.PublicKey(raw))
}

// Issue builds, signs and persists a NEW current assignment for the appliance in its own transaction.
func (b *AssignmentBase) Issue(ctx context.Context, applianceID, state string) (*assignment.Document, error) {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	doc, err := b.IssueTx(ctx, tx, applianceID, state)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return doc, nil
}

// IssueTx builds, signs and persists a NEW current assignment for the appliance inside the caller's
// transaction, bumping its version. It reads the appliance's tenant/site/serial/pubkey from the appliances
// row AS SEEN BY THAT TRANSACTION, so a caller that has just set the tenant and site signs exactly that.
// Activation uses it so the assignment and the licence commit or roll back together.
func (b *AssignmentBase) IssueTx(ctx context.Context, tx pgx.Tx, applianceID, state string) (*assignment.Document, error) {
	if b.SignKey == nil {
		return nil, errors.New("assignment signing key not configured")
	}
	// SIGNING GUARD: only an ACTIVE key may mint new assignments. A verify_only key
	// (rotated out) or a revoked key must never produce a new document, even if
	// ctrlapi is still configured with it.
	signerKeyID := assignment.KeyID(b.SignKey.Public().(ed25519.PublicKey))
	var st string
	if err := tx.QueryRow(ctx, `SELECT state FROM assignment_signing_keys WHERE key_id=$1`, signerKeyID).Scan(&st); err == nil && !assignment.CanSign(st) {
		return nil, fmt.Errorf("refusing to sign: assignment key %s is %s and may not sign new assignments", signerKeyID, st)
	}
	var tenantID, siteID, serial, pubB64, tenantName, siteName, siteType string
	err := tx.QueryRow(ctx, `
        SELECT COALESCE(a.tenant_id::text,''), COALESCE(a.site_id::text,''),
               COALESCE(a.serial,''), COALESCE(a.public_key,''),
               COALESCE(t.name,''), COALESCE(s.name,''), COALESCE(s.site_type,'UNSPECIFIED')
          FROM appliances a
          LEFT JOIN tenants t ON t.id = a.tenant_id
          LEFT JOIN sites   s ON s.id = a.site_id
         WHERE a.id = $1`, applianceID).Scan(&tenantID, &siteID, &serial, &pubB64, &tenantName, &siteName, &siteType)
	if err != nil {
		return nil, fmt.Errorf("appliance lookup: %w", err)
	}
	if assignment.Grants(state) && (tenantID == "" || siteID == "") {
		return nil, errors.New("refusing to sign an assignment with no customer or site")
	}

	var prevVersion int64
	_ = tx.QueryRow(ctx, `SELECT version FROM appliance_signed_assignments WHERE appliance_id=$1 FOR UPDATE`, applianceID).Scan(&prevVersion)
	now := time.Now().UTC().Truncate(time.Second)

	// expires_at is a REFRESH horizon, not a kill switch: past it the appliance marks
	// the assignment stale but keeps operating on it (a hotel must survive a Central
	// outage). 0 = no expiry. CTRLAPI_ASSIGN_TTL_SECONDS sets a fleet-wide horizon.
	var expiresAt int64
	if ttl := assignTTLSeconds(); ttl > 0 {
		expiresAt = now.Add(time.Duration(ttl) * time.Second).Unix()
	}
	doc := &assignment.Document{
		AssignmentID:   assignUUID(),
		ApplianceID:    applianceID,
		IdentityKeyFpr: identityFprFromPubB64(pubB64),
		Serial:         serial,
		Version:        prevVersion + 1,
		State:          state,
		IssuedAt:       now.Unix(),
		ExpiresAt:      expiresAt,
	}
	if assignment.Grants(state) {
		doc.TenantID, doc.SiteID, doc.TenantName, doc.SiteName = tenantID, siteID, tenantName, siteName
		// Descriptive only, and signed exactly like the names. UNSPECIFIED is sent as empty so an untyped
		// site's document stays byte-identical to the pre-site-type layout.
		doc.SiteType = assignmentSiteType(siteType)
	}
	assignment.Sign(b.SignKey, doc)

	raw, _ := json.Marshal(doc)
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_signed_assignments
            (appliance_id, assignment_id, version, tenant_id, site_id, state, identity_key_fpr, signed_doc, issued_at, expires_at, updated_at)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,NULLIF($7,''),$8,now(),NULL,now())
        ON CONFLICT (appliance_id) DO UPDATE SET
            assignment_id=EXCLUDED.assignment_id, version=EXCLUDED.version,
            tenant_id=EXCLUDED.tenant_id, site_id=EXCLUDED.site_id, state=EXCLUDED.state,
            identity_key_fpr=EXCLUDED.identity_key_fpr, signed_doc=EXCLUDED.signed_doc,
            issued_at=now(), updated_at=now()`,
		applianceID, doc.AssignmentID, doc.Version, doc.TenantID, doc.SiteID, doc.State, doc.IdentityKeyFpr, raw); err != nil {
		return nil, fmt.Errorf("persist assignment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO appliance_assignment_history (appliance_id, assignment_id, version, tenant_id, site_id, state, signed_doc)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7)`,
		applianceID, doc.AssignmentID, doc.Version, doc.TenantID, doc.SiteID, doc.State, raw); err != nil {
		return nil, fmt.Errorf("assignment history: %w", err)
	}
	return doc, nil
}

// signerKeyID reports which assignment key signed an appliance's current
// assignment — used to verify a rotation left no appliance on the old signer.
func signerKeyID(b *Base, ctx context.Context, applianceID string) string {
	var k string
	_ = b.DB.QueryRow(ctx,
		`SELECT COALESCE(signed_doc->>'signer_key_id','') FROM appliance_signed_assignments WHERE appliance_id=$1`,
		applianceID).Scan(&k)
	return k
}
