// Package licensing issues vendor-signed licences, one appliance at a time.
//
// A licence is bound to ONE appliance: its id, serial, hardware fingerprint, identity key and WAN MAC. Its
// only commercial terms are a concurrent-online-guest cap, a validity window and a grace period. The Service
// signs the document with the vendor key and persists both the queryable projection and the exact signed
// envelope in the licenses table. Appliances fetch the envelope over their authenticated channel and verify
// it offline; nothing in the guest path ever calls back here.
//
// Licence state is never written into the appliance lifecycle. ctrlapi derives it on read.
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

// PlanCode is the commercial_plan_code field of every signed licence document. There are no plans; the field
// stays in the document because deployed appliances verify that exact signed format (license/doc.go).
const PlanCode = "direct"

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
	ErrNoSigner  = errors.New("vendor signing key not configured (CTRLAPI_VENDOR_KEY)")
	ErrNoLicense = errors.New("no current licence")
)

// IssueParams describes ONE licence: bound to one appliance, one concurrent-online-guest cap, one validity
// window with an explicit grace period.
type IssueParams struct {
	TenantID    string
	SiteID      string
	ApplianceID string // required
	CreatedBy   string

	MaxConcurrentOnlineGuests int       // 0 = unlimited
	ValidFrom                 time.Time // zero = now
	ValidUntil                time.Time // required (or derive via ValidFor)
	ValidFor                  time.Duration
	GracePeriodDays           int // grace AFTER valid_until (new sessions still allowed)
	OfflineGraceDays          int // cloud-staleness allowance (default 30)

	// Modules are the registered module ids the licence authorises (license/modules.go). Empty = core only.
	// They are the SOLE commercial authority of the v4 document; the legacy Features block is their
	// projection. A preserved-terms re-issue carries exactly what is stored, never an invented module.
	Modules []string

	Status lic.DocStatus // zero value -> active
}

// PreservedTerms is the re-issue rule used whenever a licence is re-signed WITHOUT the operator changing its
// terms (suspend, resume, WAN-MAC rebind, same-customer move): every term and the status carry over unchanged; only
// the actor differs, and issue() assigns the next licence_version.
func PreservedTerms(cur IssueParams, createdBy string) IssueParams {
	p := cur
	p.CreatedBy = createdBy
	p.ValidFor = 0 // valid_until is carried explicitly; never re-derived from "now"
	p.Modules = append([]string{}, cur.Modules...)
	return p
}

// ModuleTerms validates module ids against the registry (unknown ids and unmet dependencies are refused) and
// returns the signed Modules map, its Features projection and the sorted, de-duplicated ids to persist. It
// never returns a nil map: a core-only v4 licence carries an explicit empty modules object.
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

// applianceBinding reads the per-appliance binding facts from the appliances row.
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

// Issue signs and persists a licence in its own transaction.
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

// IssueTx builds, signs and persists one licence inside the caller's transaction, superseding the
// appliance's current licence. Activation uses it so the assignment and the licence commit together.
//
// Anti-rollback: license_version is a per-appliance MONOTONIC sequence assigned under a transaction-scoped
// advisory lock on the appliance, so concurrent issuance can never mint two current documents or reuse a
// version.
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
	// The licence is scoped to where the appliance IS, never to a caller-supplied site: the appliance row,
	// read inside this transaction, is the authority.
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

	// The commercial controls are the binding, the concurrent cap, the validity window and the modules.
	// Features is only the modules' compatibility projection. The legacy Limits mirror carries the cap so
	// pre-v3 appliances enforce it.
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
	// The queryable projection, including the module ids a preserved-terms re-issue reads back. Features,
	// limits and the plan code live only in the signed envelope.
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

// CurrentEnvelopeForAppliance returns the signed envelope of the current licence bound to THIS appliance.
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

// Revoke marks a licence revoked. The appliance learns of it from the revocation list on its next fetch.
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

// CurrentTerms reads the terms of a CURRENT (active or suspended) licence, including its status, so it can
// be re-signed unchanged. ErrNoLicense means verified: there is no current licence with that id.
func (s *Service) CurrentTerms(ctx context.Context, licenseID string) (IssueParams, error) {
	return scanTerms(s.DB.QueryRow(ctx, `SELECT `+currentTermsColumns+`
          FROM licenses WHERE id = $1 AND status IN ('active','suspended')`, licenseID))
}

// CurrentTermsForApplianceTx reads the appliance's current licence terms inside the caller's transaction,
// holding the same per-appliance lock IssueTx takes, so what is read cannot change before it is re-signed.
// ErrNoLicense means verified: the appliance has no current licence. Any other error means unknown.
func (s *Service) CurrentTermsForApplianceTx(ctx context.Context, tx pgx.Tx, applianceID string) (IssueParams, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 42))`, applianceID); err != nil {
		return IssueParams{}, err
	}
	return scanTerms(tx.QueryRow(ctx, `SELECT `+currentTermsColumns+`
          FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')
         ORDER BY issued_at DESC LIMIT 1`, applianceID))
}

// CurrentLicenseID returns the id of the appliance's current licence, or ErrNoLicense.
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

// Reissue re-signs the current licence with every term preserved (PreservedTerms) and the next version —
// used after a WAN-MAC rebind so the new document carries the new hardware binding.
func (s *Service) Reissue(ctx context.Context, licenseID, createdBy string) (*lic.Document, error) {
	cur, err := s.CurrentTerms(ctx, licenseID)
	if err != nil {
		return nil, err
	}
	doc, _, err := s.Issue(ctx, PreservedTerms(cur, createdBy))
	return doc, err
}

// Suspend re-issues the current licence as a signed 'suspended' document. The appliance evaluates
// StateSuspended offline.
func (s *Service) Suspend(ctx context.Context, licenseID, createdBy string) (*lic.Document, error) {
	return s.reissueStatus(ctx, licenseID, createdBy, lic.DocSuspended)
}

// Resume re-issues the current licence as a signed 'active' document.
func (s *Service) Resume(ctx context.Context, licenseID, createdBy string) (*lic.Document, error) {
	return s.reissueStatus(ctx, licenseID, createdBy, lic.DocActive)
}

func (s *Service) reissueStatus(ctx context.Context, licenseID, createdBy string, status lic.DocStatus) (*lic.Document, error) {
	cur, err := s.CurrentTerms(ctx, licenseID)
	if err != nil {
		return nil, err
	}
	if time.Until(cur.ValidUntil) <= 0 {
		return nil, fmt.Errorf("licence already past valid_until; set new terms instead of suspend/resume")
	}
	p := PreservedTerms(cur, createdBy)
	p.Status = status
	doc, _, err := s.Issue(ctx, p)
	return doc, err
}
