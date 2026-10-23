// Package licensing issues vendor-signed licences, each bound to one appliance.
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

// PlanCode stays in the signed document because deployed appliances verify that exact format.
const PlanCode = "direct"

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
	ErrNoSigner  = errors.New("vendor signing key not configured (CTRLAPI_VENDOR_KEY)")
	ErrNoLicense = errors.New("no current licence")
)

type IssueParams struct {
	TenantID    string
	SiteID      string
	ApplianceID string // required
	CreatedBy   string

	MaxConcurrentOnlineGuests int       // 0 = unlimited
	ValidFrom                 time.Time // zero = now
	ValidUntil                time.Time // required (or derive via ValidFor)
	ValidFor                  time.Duration
	GracePeriodDays           int // after valid_until; new sessions still allowed
	OfflineGraceDays          int // default 30

	// Modules are the sole commercial authority of the document; Features is only their projection. Empty = core only.
	Modules []string

	Status lic.DocStatus // zero value -> active
}

// PreservedTerms re-signs a licence without changing its terms (suspend, resume, rebind, same-customer move);
// only the actor differs.
func PreservedTerms(cur IssueParams, createdBy string) IssueParams {
	p := cur
	p.CreatedBy = createdBy
	p.ValidFor = 0 // valid_until is carried explicitly; never re-derived from "now"
	p.Modules = append([]string{}, cur.Modules...)
	return p
}

// ModuleTerms validates module ids against the registry and returns the signed Modules map, its Features
// projection and the sorted, de-duplicated ids. The map is never nil: a core-only licence carries an empty object.
func ModuleTerms(ids []string) (lic.Modules, lic.Features, []string, error) {
	m, err := lic.ModulesFromIDs(ids)
	if err != nil {
		return nil, lic.Features{}, nil, err
	}
	if m == nil {
		m = lic.Modules{}
	}
	return m, lic.ProjectFeatures(m), m.ModuleIDs(), nil
}

type applianceBinding struct {
	serial, wanMAC, hwFingerprint, identityFpr, tenantID, siteID string
}

func applianceBindingTx(ctx context.Context, q pgx.Tx, applianceID string) (applianceBinding, error) {
	var b applianceBinding
	var serial, wanMAC, hwFpr, pubB64 *string
	err := q.QueryRow(ctx, `
        SELECT serial, wan_mac, hardware_fingerprint, public_key,
               COALESCE(tenant_id::text,''), COALESCE(site_id::text,'')
          FROM appliances WHERE id = $1`, applianceID).
		Scan(&serial, &wanMAC, &hwFpr, &pubB64, &b.tenantID, &b.siteID)
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

// identityFprFromB64 returns the appliance's identity fingerprint: hex of sha256(pubkey)[:8].
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

func (s *Service) Issue(ctx context.Context, p IssueParams) (*lic.Document, *lic.Envelope, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	doc, env, err := s.IssueTx(ctx, tx, p)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return doc, env, nil
}

// IssueTx signs and persists one licence inside the caller's transaction, superseding the current one.
// Anti-rollback: license_version is monotonic per appliance, assigned under an advisory lock so concurrent
// issuance cannot mint two current documents or reuse a version.
func (s *Service) IssueTx(ctx context.Context, tx pgx.Tx, p IssueParams) (*lic.Document, *lic.Envelope, error) {
	if s.Signer == nil {
		return nil, nil, ErrNoSigner
	}
	if p.ApplianceID == "" {
		return nil, nil, errors.New("a licence is always bound to one appliance: appliance_id is required")
	}
	if p.Status == "" {
		p.Status = lic.DocActive
	}
	if p.OfflineGraceDays <= 0 {
		p.OfflineGraceDays = 30
	}
	if p.MaxConcurrentOnlineGuests < 0 {
		return nil, nil, errors.New("max_concurrent_online_guests cannot be negative")
	}
	if p.GracePeriodDays < 0 || p.GracePeriodDays > 365 {
		return nil, nil, errors.New("grace_period_days must be between 0 and 365")
	}
	modules, feats, moduleIDs, err := ModuleTerms(p.Modules)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	validFrom := p.ValidFrom.UTC().Truncate(time.Second)
	if p.ValidFrom.IsZero() {
		validFrom = now
	}
	validUntil := p.ValidUntil.UTC().Truncate(time.Second)
	if p.ValidUntil.IsZero() {
		if p.ValidFor <= 0 {
			return nil, nil, fmt.Errorf("valid_until (or valid_days) is required")
		}
		validUntil = now.Add(p.ValidFor)
	}
	if !validUntil.After(validFrom) || !validUntil.After(now) {
		return nil, nil, fmt.Errorf("valid_until must be in the future and after valid_from")
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 42))`, p.ApplianceID); err != nil {
		return nil, nil, err
	}
	bind, err := applianceBindingTx(ctx, tx, p.ApplianceID)
	if err != nil {
		return nil, nil, fmt.Errorf("appliance binding: %w", err)
	}
	// The appliance row read in this transaction, not the caller, decides the customer and site.
	if bind.tenantID == "" || bind.siteID == "" {
		return nil, nil, errors.New("appliance is not activated at a customer site")
	}
	if p.TenantID != "" && p.TenantID != bind.tenantID || p.SiteID != "" && p.SiteID != bind.siteID {
		return nil, nil, errors.New("licence customer/site does not match the appliance's assignment")
	}
	p.TenantID, p.SiteID = bind.tenantID, bind.siteID

	var maxVer int64
	if err := tx.QueryRow(ctx, `
        SELECT COALESCE(MAX(license_version), 0) FROM licenses WHERE $1::uuid = ANY(appliance_ids)`,
		p.ApplianceID).Scan(&maxVer); err != nil {
		return nil, nil, err
	}
	var prevID *string
	_ = tx.QueryRow(ctx, `
        SELECT id::text FROM licenses
         WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')
         ORDER BY issued_at DESC LIMIT 1`, p.ApplianceID).Scan(&prevID)

	// The legacy Limits mirror carries the cap so pre-v3 appliances enforce it.
	lims := lic.Limits{MaxConcurrentGuestSessions: p.MaxConcurrentOnlineGuests}
	applianceIDs := []string{p.ApplianceID}

	doc := &lic.Document{
		LicenseID:          newUUID(),
		TenantID:           p.TenantID,
		SiteID:             p.SiteID,
		ApplianceIDs:       applianceIDs,
		CommercialPlanCode: PlanCode,
		Status:             p.Status,
		IssuedAt:           now,
		ValidUntil:         validUntil,
		OfflineGraceDays:   p.OfflineGraceDays,
		Features:           feats,
		Limits:             lims,
		Modules:            modules,

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

	if _, err := tx.Exec(ctx,
		`UPDATE licenses SET status = 'superseded' WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')`,
		p.ApplianceID); err != nil {
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
	// Features, limits and the plan code live only in the signed envelope.
	if _, err := tx.Exec(ctx, `
        INSERT INTO licenses (id, tenant_id, site_id, status,
                              issued_at, valid_until, valid_from, offline_grace_days, appliance_ids,
                              signed_envelope, key_id, created_by,
                              license_version, max_concurrent_online_guests, grace_period_days,
                              supersedes_license_id, modules)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,'')::uuid,$17)
    `, doc.LicenseID, p.TenantID, p.SiteID, rowStatus,
		doc.IssuedAt, doc.ValidUntil, doc.ValidFrom, p.OfflineGraceDays, applianceIDs,
		string(envRaw), env.KeyID, createdByArg,
		doc.LicenseVersion, p.MaxConcurrentOnlineGuests, p.GracePeriodDays,
		doc.SupersedesLicenseID, moduleIDs); err != nil {
		return nil, nil, err
	}
	return doc, env, nil
}

func (s *Service) CurrentEnvelopeForAppliance(ctx context.Context, applianceID string) (string, string, error) {
	var envelope, licenseID string
	err := s.DB.QueryRow(ctx, `
        SELECT l.signed_envelope, l.id
          FROM licenses l
         WHERE $1::uuid = ANY(l.appliance_ids) AND l.status IN ('active','suspended')
         ORDER BY l.issued_at DESC LIMIT 1
    `, applianceID).Scan(&envelope, &licenseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNoLicense
	}
	return envelope, licenseID, err
}

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

const currentTermsColumns = `tenant_id::text, site_id::text, valid_until, valid_from, offline_grace_days,
               COALESCE(max_concurrent_online_guests, 0), COALESCE(grace_period_days, 0),
               COALESCE(appliance_ids[1]::text, ''), status, COALESCE(modules, '{}')`

func scanTerms(row pgx.Row) (p IssueParams, err error) {
	var validFrom *time.Time
	var status string
	err = row.Scan(&p.TenantID, &p.SiteID, &p.ValidUntil, &validFrom, &p.OfflineGraceDays,
		&p.MaxConcurrentOnlineGuests, &p.GracePeriodDays, &p.ApplianceID, &status, &p.Modules)
	if errors.Is(err, pgx.ErrNoRows) {
		return IssueParams{}, ErrNoLicense
	}
	if err != nil {
		return IssueParams{}, err
	}
	if validFrom != nil {
		p.ValidFrom = *validFrom
	}
	p.Status = lic.DocActive
	if status == "suspended" {
		p.Status = lic.DocSuspended
	}
	return p, nil
}

// CurrentTerms reads a current (active or suspended) licence's terms so it can be re-signed unchanged.
// ErrNoLicense means verified absent.
func (s *Service) CurrentTerms(ctx context.Context, licenseID string) (IssueParams, error) {
	return scanTerms(s.DB.QueryRow(ctx, `SELECT `+currentTermsColumns+`
          FROM licenses WHERE id = $1 AND status IN ('active','suspended')`, licenseID))
}

// CurrentTermsForApplianceTx reads the terms under the same per-appliance lock IssueTx takes, so they cannot
// change before re-signing. ErrNoLicense means verified absent; any other error means unknown.
func (s *Service) CurrentTermsForApplianceTx(ctx context.Context, tx pgx.Tx, applianceID string) (IssueParams, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 42))`, applianceID); err != nil {
		return IssueParams{}, err
	}
	return scanTerms(tx.QueryRow(ctx, `SELECT `+currentTermsColumns+`
          FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')
         ORDER BY issued_at DESC LIMIT 1`, applianceID))
}

func (s *Service) CurrentLicenseID(ctx context.Context, applianceID string) (string, error) {
	var id string
	err := s.DB.QueryRow(ctx, `
        SELECT id::text FROM licenses
         WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')
         ORDER BY issued_at DESC LIMIT 1`, applianceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoLicense
	}
	return id, err
}

// Reissue re-signs the current licence with terms preserved, e.g. after a WAN-MAC rebind.
func (s *Service) Reissue(ctx context.Context, licenseID, createdBy string) (*lic.Document, error) {
	return s.reissueLocked(ctx, licenseID, createdBy, nil)
}

// reissueLocked reads and re-signs the terms in one transaction under the per-appliance lock; reading them
// outside it lets a racing suspend re-sign old terms over a new licence.
func (s *Service) reissueLocked(ctx context.Context, licenseID, createdBy string, mutate func(*IssueParams) error) (*lic.Document, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var applianceID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(appliance_ids[1]::text,'') FROM licenses WHERE id::text = $1`, licenseID).Scan(&applianceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && applianceID == "") {
		return nil, ErrNoLicense
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 42))`, applianceID); err != nil {
		return nil, err
	}
	cur, err := scanTerms(tx.QueryRow(ctx, `SELECT `+currentTermsColumns+`
          FROM licenses WHERE id::text = $1 AND status IN ('active','suspended')`, licenseID))
	if err != nil {
		return nil, err
	}
	p := PreservedTerms(cur, createdBy)
	if mutate != nil {
		if err := mutate(&p); err != nil {
			return nil, err
		}
	}
	doc, _, err := s.IssueTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Service) Suspend(ctx context.Context, licenseID, createdBy string) (*lic.Document, error) {
	return s.reissueStatus(ctx, licenseID, createdBy, lic.DocSuspended)
}

func (s *Service) Resume(ctx context.Context, licenseID, createdBy string) (*lic.Document, error) {
	return s.reissueStatus(ctx, licenseID, createdBy, lic.DocActive)
}

func (s *Service) reissueStatus(ctx context.Context, licenseID, createdBy string, status lic.DocStatus) (*lic.Document, error) {
	return s.reissueLocked(ctx, licenseID, createdBy, func(p *IssueParams) error {
		if time.Until(p.ValidUntil) <= 0 {
			return fmt.Errorf("licence already past valid_until; set new terms instead of suspend/resume")
		}
		p.Status = status
		return nil
	})
}
