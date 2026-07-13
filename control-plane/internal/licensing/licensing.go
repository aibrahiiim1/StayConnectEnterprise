// Package licensing issues vendor-signed entitlement documents for sites.
//
// The Service reads the tenant's active commercial subscription (plans /
// tenant_effective_limits) and the site's appliances, projects them into a
// license.Document, signs it with the vendor key, and persists both the
// queryable projection and the exact signed envelope in the licenses table.
// Appliances fetch the envelope over their authenticated channel and verify
// it offline; nothing in the guest path ever calls back here.
package licensing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	lic "github.com/stayconnect/enterprise/license"
)

// newUUID returns a random RFC 4122 v4 UUID string (avoids an extra dep).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type Service struct {
	DB     *pgxpool.Pool
	Signer *lic.Signer
}

var (
	ErrNoSubscription = errors.New("tenant has no active subscription")
	ErrNoSigner       = errors.New("vendor signing key not configured (CTRLAPI_VENDOR_KEY)")
	ErrNoLicense      = errors.New("no current license for site")
)

// effective limit lookup helpers (bool + int) against the merged view.

func (s *Service) intLimit(ctx context.Context, tenantID, key string) (int64, bool, error) {
	var v *int64
	err := s.DB.QueryRow(ctx, `
        SELECT int_value FROM tenant_effective_limits
         WHERE tenant_id = $1 AND key = $2 AND value_type = 'int' LIMIT 1
    `, tenantID, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

func (s *Service) boolLimit(ctx context.Context, tenantID, key string) (bool, error) {
	var v *bool
	err := s.DB.QueryRow(ctx, `
        SELECT bool_value FROM tenant_effective_limits
         WHERE tenant_id = $1 AND key = $2 AND value_type = 'bool' LIMIT 1
    `, tenantID, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != nil && *v, nil
}

// docLimit converts effective-limit semantics (-1 unlimited, 0/missing
// unmodelled) into license-document semantics (0 = unlimited).
func docLimit(v int64, ok bool) int {
	if !ok || v <= 0 {
		return 0
	}
	return int(v)
}

// IssueForSite builds, signs and persists a new license for a site,
// superseding any previous current license. validFor is the validity window
// from now; graceDays the offline grace. createdBy may be empty (system).
func (s *Service) IssueForSite(ctx context.Context, tenantID, siteID, createdBy string, validFor time.Duration, graceDays int) (*lic.Document, *lic.Envelope, error) {
	return s.issue(ctx, tenantID, siteID, createdBy, validFor, graceDays, lic.DocActive, "")
}

// IssueForAppliance signs a license bound to ONE specific appliance's
// cryptographic identity + hardware (appliance_id, identity-key fingerprint,
// StayConnect serial, hardware fingerprint, WAN MAC). The appliance verifies
// this binding locally and rejects a license minted for a different device.
func (s *Service) IssueForAppliance(ctx context.Context, tenantID, siteID, applianceID, createdBy string, validFor time.Duration, graceDays int) (*lic.Document, *lic.Envelope, error) {
	return s.issue(ctx, tenantID, siteID, createdBy, validFor, graceDays, lic.DocActive, applianceID)
}

// applianceBinding reads the per-appliance binding facts from the appliances row.
type applianceBinding struct {
	serial, wanMAC, hwFingerprint, identityFpr string
}

func (s *Service) applianceBinding(ctx context.Context, applianceID string) (applianceBinding, error) {
	var b applianceBinding
	var serial, wanMAC, hwFpr, pubB64 *string
	err := s.DB.QueryRow(ctx, `
        SELECT serial, wan_mac, hardware_fingerprint, public_key
          FROM appliances WHERE id = $1`, applianceID).Scan(&serial, &wanMAC, &hwFpr, &pubB64)
	if err != nil {
		return b, err
	}
	if serial != nil {
		b.serial = *serial
	}
	if wanMAC != nil {
		b.wanMAC = *wanMAC
	}
	if hwFpr != nil {
		b.hwFingerprint = *hwFpr
	}
	if pubB64 != nil {
		b.identityFpr = identityFprFromB64(*pubB64)
	}
	return b, nil
}

// identityFprFromB64 computes the identity-key fingerprint the appliance uses
// (hex of sha256(pubkey)[:8]) from the stored base64-raw Ed25519 public key.
func identityFprFromB64(pubB64 string) string {
	raw, err := base64.RawStdEncoding.DecodeString(pubB64)
	if err != nil || len(raw) != 32 {
		raw, err = base64.StdEncoding.DecodeString(pubB64)
		if err != nil || len(raw) != 32 {
			return ""
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// issue is the general form: it stamps the given signed status into the
// document (DocActive or DocSuspended). Suspension is delivered by re-issuing
// a signed doc with Status=suspended so the appliance evaluates it offline —
// a mutable DB flag alone would never reach an offline appliance.
func (s *Service) issue(ctx context.Context, tenantID, siteID, createdBy string, validFor time.Duration, graceDays int, status lic.DocStatus, applianceID string) (*lic.Document, *lic.Envelope, error) {
	if s.Signer == nil {
		return nil, nil, ErrNoSigner
	}

	// Active subscription → plan code.
	var planCode string
	err := s.DB.QueryRow(ctx, `
        SELECT p.code
          FROM tenant_subscriptions ts
          JOIN plans p ON p.id = ts.plan_id
         WHERE ts.tenant_id = $1 AND ts.status IN ('trialing','active','past_due')
         ORDER BY ts.created_at DESC LIMIT 1
    `, tenantID).Scan(&planCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNoSubscription
	}
	if err != nil {
		return nil, nil, err
	}

	// Site must belong to the tenant (cross-tenant safety).
	var siteOK bool
	if err := s.DB.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sites WHERE id = $1 AND tenant_id = $2)`,
		siteID, tenantID).Scan(&siteOK); err != nil {
		return nil, nil, err
	}
	if !siteOK {
		return nil, nil, fmt.Errorf("site %s does not belong to tenant %s", siteID, tenantID)
	}

	// Appliances bound to the site (non-retired).
	rows, err := s.DB.Query(ctx,
		`SELECT id FROM appliances WHERE site_id = $1 AND status <> 'retired' ORDER BY created_at`, siteID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var applianceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		applianceIDs = append(applianceIDs, id)
	}

	feats := lic.Features{}
	for key, dst := range map[string]*bool{
		"feature.pms_integration": &feats.PMS,
		"feature.paid_wifi":       &feats.PaidWiFi,
		"feature.auth.sms_otp":    &feats.SMSOTP,
		"feature.auth.email_otp":  &feats.EmailOTP,
		"feature.auth.social":     &feats.SocialLogin,
		"feature.ha_pair":         &feats.HA,
		"feature.white_label":     &feats.WhiteLabel,
	} {
		v, err := s.boolLimit(ctx, tenantID, key)
		if err != nil {
			return nil, nil, err
		}
		*dst = v
	}

	lims := lic.Limits{}
	for key, dst := range map[string]*int{
		"max_appliances":            &lims.MaxAppliancesForSite,
		"max_concurrent_devices":    &lims.MaxConcurrentGuestSessions,
		"max_operators":             &lims.MaxLocalOperators,
		"max_guest_access_plans":    &lims.MaxGuestAccessPlans,
		"retention_days_accounting": &lims.AccountingRetentionDays,
		"retention_days_audit":      &lims.AuditRetentionDays,
	} {
		v, ok, err := s.intLimit(ctx, tenantID, key)
		if err != nil {
			return nil, nil, err
		}
		*dst = docLimit(v, ok)
	}

	// Per-appliance hardware/identity binding (schema v2). When an appliance is
	// named, the license binds to exactly that box; otherwise it stays the
	// legacy site-wide form (all appliances, no hardware fields).
	var bind applianceBinding
	if applianceID != "" {
		applianceIDs = []string{applianceID}
		b, err := s.applianceBinding(ctx, applianceID)
		if err != nil {
			return nil, nil, fmt.Errorf("appliance binding: %w", err)
		}
		bind = b
	}

	now := time.Now().UTC().Truncate(time.Second)
	doc := &lic.Document{
		LicenseID:          newUUID(),
		TenantID:           tenantID,
		SiteID:             siteID,
		ApplianceIDs:       applianceIDs,
		CommercialPlanCode: planCode,
		Status:             status,
		IssuedAt:           now,
		ValidUntil:         now.Add(validFor),
		OfflineGraceDays:   graceDays,
		Features:           feats,
		Limits:             lims,

		ApplianceID:            applianceID,
		ApplianceSerial:        bind.serial,
		HardwareFingerprint:    bind.hwFingerprint,
		IdentityKeyFingerprint: bind.identityFpr,
		WANMAC:                 bind.wanMAC,
		ValidFrom:              now,
		SignerKeyID:            s.Signer.KeyID(),
		SchemaVersion:          lic.CurrentSchemaVersion,
	}
	env, err := s.Signer.Sign(doc)
	if err != nil {
		return nil, nil, err
	}
	envRaw, err := env.Encode()
	if err != nil {
		return nil, nil, err
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE licenses SET status = 'superseded' WHERE site_id = $1 AND status IN ('active','suspended')`,
		siteID); err != nil {
		return nil, nil, err
	}
	var createdByArg any
	if createdBy != "" {
		createdByArg = createdBy
	}
	rowStatus := "active"
	if status == lic.DocSuspended {
		rowStatus = "suspended"
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO licenses (id, tenant_id, site_id, commercial_plan_code, status,
                              issued_at, valid_until, offline_grace_days, appliance_ids,
                              features, limits, signed_envelope, key_id, created_by)
        VALUES ($1,$2,$3,$4,$25,$5,$6,$7,$8,
                jsonb_build_object('pms',$9::bool,'paid_wifi',$10::bool,'sms_otp',$11::bool,
                                   'email_otp',$12::bool,'social_login',$13::bool,'ha',$14::bool,
                                   'white_label',$15::bool),
                jsonb_build_object('max_appliances_for_site',$16::int,
                                   'max_concurrent_guest_sessions',$17::int,
                                   'max_local_operators',$18::int,
                                   'max_guest_access_plans',$19::int,
                                   'accounting_retention_days',$20::int,
                                   'audit_retention_days',$21::int),
                $22,$23,$24)
    `, doc.LicenseID, tenantID, siteID, planCode, doc.IssuedAt, doc.ValidUntil, graceDays,
		applianceIDs,
		feats.PMS, feats.PaidWiFi, feats.SMSOTP, feats.EmailOTP, feats.SocialLogin, feats.HA, feats.WhiteLabel,
		lims.MaxAppliancesForSite, lims.MaxConcurrentGuestSessions, lims.MaxLocalOperators,
		lims.MaxGuestAccessPlans, lims.AccountingRetentionDays, lims.AuditRetentionDays,
		string(envRaw), env.KeyID, createdByArg, rowStatus); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return doc, env, nil
}

// ReconcileStates moves each appliance in a license-driven commercial state
// between licensed → grace → license_expired based ONLY on time vs the site's
// current signed license (valid_until + offline_grace_days). It never touches
// suspended, revoked, decommissioned, or pre-license states — those are set
// explicitly and must not be overridden by the time reconcile. Run on a ticker.
func (s *Service) ReconcileStates(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `
        WITH cur AS (
          SELECT DISTINCT ON (site_id) site_id, valid_until, offline_grace_days
            FROM licenses WHERE status IN ('active','suspended')
            ORDER BY site_id, issued_at DESC
        ), want AS (
          SELECT a.id,
                 CASE
                   WHEN now() <= c.valid_until THEN 'licensed'
                   WHEN now() <= c.valid_until + make_interval(days => c.offline_grace_days) THEN 'grace'
                   ELSE 'license_expired'
                 END AS target
            FROM appliances a JOIN cur c ON c.site_id = a.site_id
           WHERE a.lifecycle_state IN ('licensed','grace','license_expired')
        )
        UPDATE appliances a SET lifecycle_state = w.target, updated_at = now()
          FROM want w
         WHERE a.id = w.id AND a.lifecycle_state <> w.target`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CurrentEnvelopeForAppliance returns the signed envelope of the current
// license covering the given appliance (via its site).
func (s *Service) CurrentEnvelopeForAppliance(ctx context.Context, applianceID string) (string, string, error) {
	var envelope, licenseID string
	err := s.DB.QueryRow(ctx, `
        SELECT l.signed_envelope, l.id
          FROM licenses l
          JOIN appliances a ON a.site_id = l.site_id
         WHERE a.id = $1 AND l.status IN ('active','suspended')
         ORDER BY l.issued_at DESC LIMIT 1
    `, applianceID).Scan(&envelope, &licenseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNoLicense
	}
	return envelope, licenseID, err
}

// Revoke marks a license revoked. Delivery of the revocation notice to the
// appliance happens via sync (license fetch returns the revoked state).
func (s *Service) Revoke(ctx context.Context, licenseID string) error {
	tag, err := s.DB.Exec(ctx, `
        UPDATE licenses SET status = 'revoked', revoked_at = now()
         WHERE id = $1 AND status IN ('active','suspended')
    `, licenseID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoLicense
	}
	return nil
}

// licenseParams reads the fields needed to re-issue a license under a new
// signed status, preserving the original tenant/site/validity/grace.
func (s *Service) licenseParams(ctx context.Context, licenseID string) (tenantID, siteID string, validUntil time.Time, grace int, err error) {
	err = s.DB.QueryRow(ctx, `
        SELECT tenant_id::text, site_id::text, valid_until, offline_grace_days
          FROM licenses WHERE id = $1 AND status IN ('active','suspended')
    `, licenseID).Scan(&tenantID, &siteID, &validUntil, &grace)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNoLicense
	}
	return
}

// Suspend re-issues the site's current license as a signed 'suspended'
// document (billing hold). The appliance evaluates StateSuspended offline.
// Returns the site id so the caller can drive appliance lifecycle_state.
func (s *Service) Suspend(ctx context.Context, licenseID, createdBy string) (string, error) {
	return s.reissueStatus(ctx, licenseID, createdBy, lic.DocSuspended)
}

// Resume re-issues the site's current license as a signed 'active' document.
func (s *Service) Resume(ctx context.Context, licenseID, createdBy string) (string, error) {
	return s.reissueStatus(ctx, licenseID, createdBy, lic.DocActive)
}

func (s *Service) reissueStatus(ctx context.Context, licenseID, createdBy string, status lic.DocStatus) (string, error) {
	tenantID, siteID, validUntil, grace, err := s.licenseParams(ctx, licenseID)
	if err != nil {
		return "", err
	}
	validFor := time.Until(validUntil)
	if validFor <= 0 {
		return "", fmt.Errorf("license already expired; renew instead of suspend/resume")
	}
	// Preserve the existing per-appliance binding when re-issuing suspend/resume.
	var boundAppliance string
	_ = s.DB.QueryRow(ctx, `SELECT COALESCE(appliance_ids[1]::text,'') FROM licenses WHERE id = $1`, licenseID).Scan(&boundAppliance)
	if _, _, err := s.issue(ctx, tenantID, siteID, createdBy, validFor, grace, status, boundAppliance); err != nil {
		return "", err
	}
	return siteID, nil
}
