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

// AssignmentBase issues signed appliance-assignment documents and serves the current one to the appliance.
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

// identityFprFromPubB64 must match the edge's fingerprint: hex(sha256(pub)[:8]) of the raw Ed25519 key.
func identityFprFromPubB64(pubB64 string) string {
	raw, err := base64.RawStdEncoding.DecodeString(pubB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		if r2, e2 := base64.StdEncoding.DecodeString(pubB64); e2 == nil && len(r2) == ed25519.PublicKeySize {
			raw = r2
		} else {
			return ""
		}
	}
	return assignment.KeyID(ed25519.PublicKey(raw))
}

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

// IssueTx signs and persists a new current assignment inside the caller's transaction, bumping its version.
// It reads the appliances row as seen by that transaction, so a caller that just set tenant and site signs exactly that.
func (b *AssignmentBase) IssueTx(ctx context.Context, tx pgx.Tx, applianceID, state string) (*assignment.Document, error) {
	if b.SignKey == nil {
		return nil, errors.New("assignment signing key not configured")
	}
	// Only an active key may mint assignments; a verify_only or revoked key must not, even if ctrlapi still holds it.
	signerKeyID := assignment.KeyID(b.SignKey.Public().(ed25519.PublicKey))
	// Fail closed: a key with no readable record does not sign; unknown is not "active".
	var st string
	if err := tx.QueryRow(ctx, `SELECT state FROM assignment_signing_keys WHERE key_id=$1`, signerKeyID).Scan(&st); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("refusing to sign: assignment key %s is not registered in Central (restart ctrlapi to register it)", signerKeyID)
		}
		return nil, fmt.Errorf("refusing to sign: the state of assignment key %s could not be read: %w", signerKeyID, err)
	}
	if !assignment.CanSign(st) {
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

	// Only "no assignment yet" is version 0; any other error stops issuance rather than restart at 1 (audit Phase 1 L3).
	var prevVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM appliance_signed_assignments WHERE appliance_id=$1 FOR UPDATE`, applianceID).
		Scan(&prevVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("previous assignment version: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	// expires_at is a refresh horizon, not a kill switch: past it the appliance marks the assignment stale but keeps
	// operating (a hotel must survive a Central outage). 0 = no expiry.
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
		// Descriptive only. UNSPECIFIED is sent empty so an untyped site's document stays byte-identical.
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

// signerKeyID reports which key signed the appliance's current assignment, to verify a rotation left none on the old signer.
func signerKeyID(b *Base, ctx context.Context, applianceID string) string {
	var k string
	_ = b.DB.QueryRow(ctx,
		`SELECT COALESCE(signed_doc->>'signer_key_id','') FROM appliance_signed_assignments WHERE appliance_id=$1`,
		applianceID).Scan(&k)
	return k
}
