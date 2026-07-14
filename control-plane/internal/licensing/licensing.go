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

// IssueParams describes ONE license in the simple model: bound to one
// appliance, one concurrent-online-guest cap, one validity window with an
// explicit grace period. No plan or subscription is required.
type IssueParams struct {
	TenantID    string
	SiteID      string
	ApplianceID string // empty = legacy site-wide license
	CreatedBy   string

	MaxConcurrentOnlineGuests int       // 0 = unlimited
	ValidFrom                 time.Time // zero = now
	ValidUntil                time.Time // required (or derive via ValidFor)
	ValidFor                  time.Duration
	GracePeriodDays           int // grace AFTER valid_until (new sessions still allowed)
	OfflineGraceDays          int // cloud-staleness allowance (default 30)

	Status lic.DocStatus // zero value -> active
}

// IssueForSite builds, signs and persists a new license for a site,
// superseding any previous current license. validFor is the validity window
// from now; graceDays the grace period. createdBy may be empty (system).
func (s *Service) IssueForSite(ctx context.Context, tenantID, siteID, createdBy string, validFor time.Duration, graceDays int) (*lic.Document, *lic.Envelope, error) {
	return s.issue(ctx, IssueParams{TenantID: tenantID, SiteID: siteID, CreatedBy: createdBy,
		ValidFor: validFor, GracePeriodDays: graceDays, OfflineGraceDays: graceDays})
}

// IssueForAppliance signs a license bound to ONE specific appliance's
// cryptographic identity + hardware (appliance_id, identity-key fingerprint,
// StayConnect serial, hardware fingerprint, WAN MAC). The appliance verifies
// this binding locally and rejects a license minted for a different device.
func (s *Service) IssueForAppliance(ctx context.Context, tenantID, siteID, applianceID, createdBy string, validFor time.Duration, graceDays int) (*lic.Document, *lic.Envelope, error) {
	return s.issue(ctx, IssueParams{TenantID: tenantID, SiteID: siteID, ApplianceID: applianceID,
		CreatedBy: createdBy, ValidFor: validFor, GracePeriodDays: graceDays, OfflineGraceDays: graceDays})
}

// Issue is the simple-model entry point with fully explicit commercial
// parameters (appliance binding, concurrent-guest cap, validity, grace).
func (s *Service) Issue(ctx context.Context, p IssueParams) (*lic.Document, *lic.Envelope, error) {
	return s.issue(ctx, p)
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

// issue builds, signs and persists one license under the SIMPLE model.
//
// No plan or subscription is required: when the tenant still has one the plan
// code is recorded for reporting continuity, otherwise the license is issued
// as plan "direct". The commercial entitlement comes from the params
// (concurrent cap, validity, grace) — never from plan limits.
//
// Anti-rollback: license_version is a per-appliance/site MONOTONIC sequence
// assigned inside the same transaction that supersedes the previous current
// license, under a pg advisory lock, so concurrent issuance can never mint
// two current documents or reuse a version.
func (s *Service) issue(ctx context.Context, p IssueParams) (*lic.Document, *lic.Envelope, error) {
	if s.Signer == nil {
		return nil, nil, ErrNoSigner
	}
	if p.Status == "" {
		p.Status = lic.DocActive
	}
	if p.OfflineGraceDays <= 0 {
		p.OfflineGraceDays = 30
	}
	now := time.Now().UTC().Truncate(time.Second)
	validFrom := p.ValidFrom.UTC().Truncate(time.Second)
	if p.ValidFrom.IsZero() {
		validFrom = now
	}
	validUntil := p.ValidUntil.UTC().Truncate(time.Second)
	if p.ValidUntil.IsZero() {
		if p.ValidFor <= 0 {
			return nil, nil, fmt.Errorf("valid_until (or valid_for) is required")
		}
		validUntil = now.Add(p.ValidFor)
	}
	if !validUntil.After(validFrom) || !validUntil.After(now) {
		return nil, nil, fmt.Errorf("valid_until must be in the future and after valid_from")
	}

	// Optional plan code (reporting only). No subscription -> "direct".
	planCode := "direct"
	_ = s.DB.QueryRow(ctx, `
        SELECT p.code
          FROM tenant_subscriptions ts
          JOIN plans p ON p.id = ts.plan_id
         WHERE ts.tenant_id = $1 AND ts.status IN ('trialing','active','past_due')
         ORDER BY ts.created_at DESC LIMIT 1
    `, p.TenantID).Scan(&planCode)

	// Site must belong to the tenant (cross-tenant safety).
	var siteOK bool
	if err := s.DB.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sites WHERE id = $1 AND tenant_id = $2)`,
		p.SiteID, p.TenantID).Scan(&siteOK); err != nil {
		return nil, nil, err
	}
	if !siteOK {
		return nil, nil, fmt.Errorf("site %s does not belong to tenant %s", p.SiteID, p.TenantID)
	}

	// Binding target list (legacy site-wide form when no appliance named).
	applianceIDs := []string{}
	var bind applianceBinding
	if p.ApplianceID != "" {
		applianceIDs = []string{p.ApplianceID}
		b, err := s.applianceBinding(ctx, p.ApplianceID)
		if err != nil {
			return nil, nil, fmt.Errorf("appliance binding: %w", err)
		}
		bind = b
	} else {
		rows, err := s.DB.Query(ctx,
			`SELECT id FROM appliances WHERE site_id = $1 AND status <> 'retired' ORDER BY created_at`, p.SiteID)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, nil, err
			}
			applianceIDs = append(applianceIDs, id)
		}
	}

	// Simple model: all product features are available; the ONLY commercial
	// controls are the binding, the concurrent cap, and the validity window.
	// The legacy Limits mirror carries the cap so pre-v3 appliances enforce it.
	feats := lic.Features{PMS: true, PaidWiFi: true, SMSOTP: true, EmailOTP: true,
		SocialLogin: true, HA: true, WhiteLabel: true}
	lims := lic.Limits{MaxConcurrentGuestSessions: p.MaxConcurrentOnlineGuests}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	// Serialize version assignment per site (one appliance per site in the
	// product; the appliance key is covered by the site scope).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 42))`, p.SiteID); err != nil {
		return nil, nil, err
	}
	var prevID *string
	var maxVer int64
	if err := tx.QueryRow(ctx, `
        SELECT COALESCE(MAX(license_version), 0)
          FROM licenses
         WHERE site_id = $1 OR ($2 <> '' AND $2::uuid = ANY(appliance_ids))
    `, p.SiteID, p.ApplianceID).Scan(&maxVer); err != nil {
		return nil, nil, err
	}
	_ = tx.QueryRow(ctx, `
        SELECT id::text FROM licenses
         WHERE site_id = $1 AND status IN ('active','suspended')
         ORDER BY issued_at DESC LIMIT 1`, p.SiteID).Scan(&prevID)

	doc := &lic.Document{
		LicenseID:          newUUID(),
		TenantID:           p.TenantID,
		SiteID:             p.SiteID,
		ApplianceIDs:       applianceIDs,
		CommercialPlanCode: planCode,
		Status:             p.Status,
		IssuedAt:           now,
		ValidUntil:         validUntil,
		OfflineGraceDays:   p.OfflineGraceDays,
		Features:           feats,
		Limits:             lims,

		ApplianceID:            p.ApplianceID,
		ApplianceSerial:        bind.serial,
		HardwareFingerprint:    bind.hwFingerprint,
		IdentityKeyFingerprint: bind.identityFpr,
		WANMAC:                 bind.wanMAC,
		ValidFrom:              validFrom,
		SignerKeyID:            s.Signer.KeyID(),

		MaxConcurrentOnlineGuests: p.MaxConcurrentOnlineGuests,
		GracePeriodDays:           p.GracePeriodDays,
		LicenseVersion:            maxVer + 1,
		SchemaVersion:             lic.CurrentSchemaVersion,
	}
	if prevID != nil {
		doc.SupersedesLicenseID = *prevID
	}
	env, err := s.Signer.Sign(doc)
	if err != nil {
		return nil, nil, err
	}
	envRaw, err := env.Encode()
	if err != nil {
		return nil, nil, err
	}

	// Atomically supersede the previous current license and insert the new one.
	if _, err := tx.Exec(ctx,
		`UPDATE licenses SET status = 'superseded' WHERE site_id = $1 AND status IN ('active','suspended')`,
		p.SiteID); err != nil {
		return nil, nil, err
	}
	var createdByArg any
	if p.CreatedBy != "" {
		createdByArg = p.CreatedBy
	}
	rowStatus := "active"
	if p.Status == lic.DocSuspended {
		rowStatus = "suspended"
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO licenses (id, tenant_id, site_id, commercial_plan_code, status,
                              issued_at, valid_until, valid_from, offline_grace_days, appliance_ids,
                              features, limits, signed_envelope, key_id, created_by,
                              license_version, max_concurrent_online_guests, grace_period_days,
                              supersedes_license_id)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
                jsonb_build_object('pms',true,'paid_wifi',true,'sms_otp',true,
                                   'email_otp',true,'social_login',true,'ha',true,
                                   'white_label',true),
                jsonb_build_object('max_appliances_for_site',0,
                                   'max_concurrent_guest_sessions',$11::int,
                                   'max_local_operators',0,
                                   'max_guest_access_plans',0,
                                   'accounting_retention_days',0,
                                   'audit_retention_days',0),
                $12,$13,$14,$15,$16,$17,NULLIF($18,'')::uuid)
    `, doc.LicenseID, p.TenantID, p.SiteID, planCode, rowStatus,
		doc.IssuedAt, doc.ValidUntil, doc.ValidFrom, p.OfflineGraceDays, applianceIDs,
		p.MaxConcurrentOnlineGuests,
		string(envRaw), env.KeyID, createdByArg,
		doc.LicenseVersion, p.MaxConcurrentOnlineGuests, p.GracePeriodDays,
		doc.SupersedesLicenseID); err != nil {
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
          SELECT DISTINCT ON (site_id) site_id, valid_until,
                 CASE WHEN COALESCE(grace_period_days,0) > 0
                      THEN grace_period_days ELSE offline_grace_days END AS eff_grace
            FROM licenses WHERE status IN ('active','suspended')
            ORDER BY site_id, issued_at DESC
        ), want AS (
          SELECT a.id,
                 CASE
                   WHEN now() <= c.valid_until THEN 'licensed'
                   WHEN now() <= c.valid_until + make_interval(days => c.eff_grace) THEN 'grace'
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
// signed status, preserving the original commercial terms.
func (s *Service) licenseParams(ctx context.Context, licenseID string) (p IssueParams, err error) {
	var validFrom *time.Time
	err = s.DB.QueryRow(ctx, `
        SELECT tenant_id::text, site_id::text, valid_until, valid_from, offline_grace_days,
               COALESCE(max_concurrent_online_guests, 0), COALESCE(grace_period_days, 0),
               COALESCE(appliance_ids[1]::text, '')
          FROM licenses WHERE id = $1 AND status IN ('active','suspended')
    `, licenseID).Scan(&p.TenantID, &p.SiteID, &p.ValidUntil, &validFrom, &p.OfflineGraceDays,
		&p.MaxConcurrentOnlineGuests, &p.GracePeriodDays, &p.ApplianceID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNoLicense
	}
	if validFrom != nil {
		p.ValidFrom = *validFrom
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
	p, err := s.licenseParams(ctx, licenseID)
	if err != nil {
		return "", err
	}
	if time.Until(p.ValidUntil) <= 0 {
		return "", fmt.Errorf("license already expired; renew instead of suspend/resume")
	}
	// Preserve every commercial term (binding, cap, validity, grace); the new
	// document carries the next license_version so the appliance's anti-rollback
	// state advances and the previous document can never be replayed.
	p.CreatedBy = createdBy
	p.Status = status
	if _, _, err := s.issue(ctx, p); err != nil {
		return "", err
	}
	return p.SiteID, nil
}
