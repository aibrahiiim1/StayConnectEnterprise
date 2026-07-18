package iamv2

import (
	"context"
	"time"
)

// CommerceAdmin is the DARK Phase-2 Hotel-Admin surface for revisioned commercial-package management.
// It publishes IMMUTABLE package revisions (rules + grant tiers validated at publication), moves the
// package's current-revision pointer atomically, and toggles package activation — all non-PMS and
// free-only in Phase 2. When the admin surface is OFF it holds a nil repository, issues zero SQL and
// returns a disabled result.
type CommerceAdmin struct {
	cfg  CommerceConfig
	repo CommerceAdminRepository
	obs  Observer
	now  func() time.Time
}

// NewCommerceAdmin builds the admin engine. repo MUST be nil while the master flag is OFF (dark) and
// non-nil when enabled (fail closed).
func NewCommerceAdmin(cfg CommerceConfig, repo CommerceAdminRepository, obs Observer) (*CommerceAdmin, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled() && repo == nil {
		return nil, &Error{Code: ErrConfig, Msg: "phase2 admin enabled but no commerce-admin repository provided"}
	}
	if obs == nil {
		obs = NopObserver{}
	}
	return &CommerceAdmin{cfg: cfg, repo: repo, obs: obs, now: time.Now}, nil
}

// CommerceAdminRepository is the Phase-2 admin data boundary (nil while dark).
type CommerceAdminRepository interface {
	WithTx(ctx context.Context, fn func(CommerceAdminTx) error) error
	ListPackages(ctx context.Context, tenantID, siteID string) ([]PackageSummary, error)
	ListPackageRevisions(ctx context.Context, tenantID, siteID, packageID string) ([]RevisionInfo, error)
	ListPlans(ctx context.Context, tenantID, siteID string) ([]PlanSummary, error)
	ListPlanRevisions(ctx context.Context, tenantID, siteID, planID string) ([]RevisionInfo, error)
	GetGraceConfig(ctx context.Context, tenantID, siteID string) (GraceConfig, error)
	ListQuotes(ctx context.Context, tenantID, siteID string, limit int) ([]QuoteInspect, error)
	ListPurchases(ctx context.Context, tenantID, siteID string, limit int) ([]PurchaseInspect, error)
}

// CommerceAdminTx is the transactional admin surface: a whole publish runs on one tx.
type CommerceAdminTx interface {
	UpsertPackage(ctx context.Context, tenantID, siteID, code string) (packageID string, err error)
	NextRevisionNo(ctx context.Context, packageID string) (int, error)
	PlanRevisionBelongs(ctx context.Context, tenantID, siteID, planRevisionID string) (bool, error)
	InsertPackageRevision(ctx context.Context, spec PackagePublishSpec, packageID string, revNo int) (revisionID string, err error)
	InsertEligibilityRule(ctx context.Context, tenantID, siteID, revisionID string, rule EligibilityRule) error
	InsertGrantTier(ctx context.Context, tenantID, siteID, revisionID string, tier GrantTier) error
	SetCurrentRevision(ctx context.Context, packageID, revisionID string) error
	SetPackageActive(ctx context.Context, tenantID, siteID, packageID string, active bool) error

	// service plans
	UpsertPlan(ctx context.Context, tenantID, siteID, code string) (planID string, err error)
	NextPlanRevisionNo(ctx context.Context, planID string) (int, error)
	InsertPlanRevision(ctx context.Context, spec PlanPublishSpec, planID string, revNo int) (revisionID string, err error)
	SetPlanCurrentRevision(ctx context.Context, planID, revisionID string) error

	// grace config
	GraceCandidateValid(ctx context.Context, tenantID, siteID, packageRevisionID string) (GraceCandidate, error)
	UpsertGraceConfig(ctx context.Context, tenantID, siteID, packageRevisionID string, config map[string]any) error
}

// RevisionInfo is a read-only revision-history row (plan or package).
type RevisionInfo struct {
	RevisionID  string `json:"revision_id"`
	RevisionNo  int    `json:"revision_no"`
	IsCurrent   bool   `json:"is_current"`
	Label       string `json:"label,omitempty"`        // plan name / package type
	PriceMinor  int64  `json:"price_minor,omitempty"`  // packages only
	Currency    string `json:"currency,omitempty"`     // packages only
	PackageType string `json:"package_type,omitempty"` // packages only
}

// PlanSummary is the read shape for the service-plan list.
type PlanSummary struct {
	PlanID            string `json:"plan_id"`
	Code              string `json:"code"`
	Enabled           bool   `json:"enabled"`
	CurrentRevisionID string `json:"current_revision_id"`
	RevisionCount     int    `json:"revision_count"`
}

// GraceConfig is the read/write shape for site_checkout_grace_config.
type GraceConfig struct {
	GracePackageRevisionID string         `json:"grace_package_revision_id"`
	Config                 map[string]any `json:"config"`
}

// GraceCandidate describes a candidate grace package revision for validation.
type GraceCandidate struct {
	Found          bool
	PackageActive  bool
	PackageType    string
	PriceMinor     int64
	Currency       string
	CurrencyExp    int
	SettlementOnly bool // settlement_methods == exactly {NOT_REQUIRED}
	PlanRevValid   bool
}

// QuoteInspect / PurchaseInspect are guest-PII-free inspection rows.
type QuoteInspect struct {
	ID                string  `json:"id"`
	PackageRevisionID string  `json:"package_revision_id"`
	PriceMinor        int64   `json:"price_minor"`
	Currency          string  `json:"currency"`
	ExpiresAt         string  `json:"expires_at"`
	ConsumedAt        *string `json:"consumed_at"`
}
type PurchaseInspect struct {
	ID                string `json:"id"`
	PackageRevisionID string `json:"package_revision_id"`
	State             string `json:"state"`
	AmountMinor       int64  `json:"amount_minor"`
	Currency          string `json:"currency"`
}

// PlanPublishSpec publishes a new immutable service-plan revision.
type PlanPublishSpec struct {
	TenantID, SiteID            string
	PlanCode                    string
	Name                        string
	DownKbps                    *int
	UpKbps                      *int
	MaxConcurrentDevices        int
	DeviceLimitPolicy           string
	IdleTimeoutSeconds          *int
	MaxContinuousSessionSeconds *int
	TimeQuotaSeconds            *int64
	DataQuotaBytes              *int64
	TimeAccountingMode          string
}

// PackageSummary is the read shape for the admin list.
type PackageSummary struct {
	PackageID         string `json:"package_id"`
	Code              string `json:"code"`
	Active            bool   `json:"active"`
	CurrentRevisionID string `json:"current_revision_id"`
	RevisionCount     int    `json:"revision_count"`
}

// PackagePublishSpec is a request to publish a new immutable free package revision.
type PackagePublishSpec struct {
	TenantID, SiteID      string
	PackageCode           string
	ServicePlanRevisionID string
	Display               map[string]any
	DurationPolicy        map[string]any
	EligibilityRules      []EligibilityRule
	GrantTiers            []GrantTier
	VisibleFrom           *time.Time
	VisibleUntil          *time.Time
}

// AdminResult is the guest-independent result of an admin mutation.
type AdminResult struct {
	Disabled          bool
	PackageID         string
	CurrentRevisionID string
	Reason            string
}

// ListPackages returns the site's packages (disabled result when the admin surface is OFF).
func (a *CommerceAdmin) ListPackages(ctx context.Context, tenantID, siteID string) ([]PackageSummary, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil // disabled
	}
	out, err := a.repo.ListPackages(ctx, tenantID, siteID)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "list packages"}
	}
	return out, false, nil
}

// PublishRevision validates and publishes a new immutable revision, then moves the current-revision
// pointer — atomically. Publication is fail-closed: a malformed rule/tier, an unknown/PMS rule type, a
// bad duration policy, or a plan revision from another tenant/site is rejected before any write. Phase 2
// is FREE-ONLY: the published revision is price 0 / settlement NOT_REQUIRED (enforced by the writer).
func (a *CommerceAdmin) PublishRevision(ctx context.Context, spec PackagePublishSpec) (AdminResult, error) {
	if !a.cfg.AdminOn() {
		a.obs.Event("phase2.disabled", map[string]string{"op": "publish"})
		return AdminResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if spec.TenantID == "" || spec.SiteID == "" || spec.PackageCode == "" || spec.ServicePlanRevisionID == "" {
		return AdminResult{}, &Error{Code: ErrInvalidInput, Msg: "publish: missing tenant/site/code/plan_revision"}
	}
	// publication-strict validation of every rule and tier (no writes yet)
	for _, rule := range spec.EligibilityRules {
		if err := ValidateEligibilityRule(rule); err != nil {
			return AdminResult{Reason: "invalid_eligibility_rule"}, nil
		}
	}
	if len(spec.GrantTiers) == 0 {
		return AdminResult{Reason: "no_grant_tiers"}, nil
	}
	for _, tier := range spec.GrantTiers {
		if err := ValidateGrantTier(tier); err != nil {
			return AdminResult{Reason: "invalid_grant_tier"}, nil
		}
	}
	// the immutable duration policy must resolve (PMS/checkout/local-time modes are capability-disabled)
	if _, _, err := ResolveEndPolicy(spec.DurationPolicy, a.now()); err != nil {
		return AdminResult{Reason: "invalid_duration_policy"}, nil
	}
	if spec.VisibleFrom != nil && spec.VisibleUntil != nil && !spec.VisibleFrom.Before(*spec.VisibleUntil) {
		return AdminResult{Reason: "invalid_sale_window"}, nil
	}

	var res AdminResult
	err := a.repo.WithTx(ctx, func(tx CommerceAdminTx) error {
		ok, err := tx.PlanRevisionBelongs(ctx, spec.TenantID, spec.SiteID, spec.ServicePlanRevisionID)
		if err != nil {
			return err
		}
		if !ok {
			res = AdminResult{Reason: "plan_revision_not_found"}
			return nil
		}
		pkgID, err := tx.UpsertPackage(ctx, spec.TenantID, spec.SiteID, spec.PackageCode)
		if err != nil {
			return err
		}
		revNo, err := tx.NextRevisionNo(ctx, pkgID)
		if err != nil {
			return err
		}
		revID, err := tx.InsertPackageRevision(ctx, spec, pkgID, revNo)
		if err != nil {
			return err
		}
		for _, rule := range spec.EligibilityRules {
			if err := tx.InsertEligibilityRule(ctx, spec.TenantID, spec.SiteID, revID, rule); err != nil {
				return err
			}
		}
		for _, tier := range spec.GrantTiers {
			if err := tx.InsertGrantTier(ctx, spec.TenantID, spec.SiteID, revID, tier); err != nil {
				return err
			}
		}
		if err := tx.SetCurrentRevision(ctx, pkgID, revID); err != nil {
			return err
		}
		res = AdminResult{PackageID: pkgID, CurrentRevisionID: revID, Reason: "published"}
		return nil
	})
	if err != nil {
		return AdminResult{}, &Error{Code: ErrRepo, Msg: "publish"}
	}
	return res, nil
}

// ListPlans returns the site's service plans (disabled result when the admin surface is OFF).
func (a *CommerceAdmin) ListPlans(ctx context.Context, tenantID, siteID string) ([]PlanSummary, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil
	}
	out, err := a.repo.ListPlans(ctx, tenantID, siteID)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "list plans"}
	}
	return out, false, nil
}

// PlanRevisions returns a plan's immutable revision history.
func (a *CommerceAdmin) PlanRevisions(ctx context.Context, tenantID, siteID, planID string) ([]RevisionInfo, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil
	}
	out, err := a.repo.ListPlanRevisions(ctx, tenantID, siteID, planID)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "plan revisions"}
	}
	return out, false, nil
}

// PackageRevisions returns a package's immutable revision history.
func (a *CommerceAdmin) PackageRevisions(ctx context.Context, tenantID, siteID, packageID string) ([]RevisionInfo, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil
	}
	out, err := a.repo.ListPackageRevisions(ctx, tenantID, siteID, packageID)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "package revisions"}
	}
	return out, false, nil
}

// validatePlanSpec fail-closes on a malformed plan spec (AGGREGATE accounting capability-disabled;
// device policy enum; non-negative bounded ints; concurrency >= 1).
func validatePlanSpec(spec *PlanPublishSpec) error {
	if spec.MaxConcurrentDevices == 0 {
		spec.MaxConcurrentDevices = 1
	}
	if spec.MaxConcurrentDevices < 1 || spec.MaxConcurrentDevices > maxDevices {
		return &Error{Code: ErrInvalidInput, Msg: "max_concurrent_devices out of range"}
	}
	if spec.DeviceLimitPolicy == "" {
		spec.DeviceLimitPolicy = "REJECT_NEW_DEVICE"
	}
	if !deviceLimitPolicies[spec.DeviceLimitPolicy] {
		return &Error{Code: ErrInvalidInput, Msg: "unknown device_limit_policy"}
	}
	if spec.TimeAccountingMode == "" {
		spec.TimeAccountingMode = "VALIDITY_WINDOW"
	}
	if spec.TimeAccountingMode != "VALIDITY_WINDOW" { // AGGREGATE_ONLINE_TIME capability-disabled in Phase 2
		return &Error{Code: ErrInvalidInput, Msg: "unsupported time_accounting_mode"}
	}
	nn := func(p *int, max int) error {
		if p != nil && (*p < 0 || *p > max) {
			return &Error{Code: ErrInvalidInput, Msg: "plan integer out of range"}
		}
		return nil
	}
	nn64 := func(p *int64, max int64) error {
		if p != nil && (*p < 0 || *p > max) {
			return &Error{Code: ErrInvalidInput, Msg: "plan integer out of range"}
		}
		return nil
	}
	for _, e := range []error{
		nn(spec.DownKbps, maxKbps), nn(spec.UpKbps, maxKbps),
		nn(spec.IdleTimeoutSeconds, maxIdleSeconds), nn(spec.MaxContinuousSessionSeconds, maxSessionSeconds),
		nn64(spec.TimeQuotaSeconds, maxTimeQuotaSecond), nn64(spec.DataQuotaBytes, maxDataQuotaBytes),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

// PublishPlanRevision publishes a new immutable service-plan revision and moves the plan's current
// pointer atomically. Fail-closed validation before any write.
func (a *CommerceAdmin) PublishPlanRevision(ctx context.Context, spec PlanPublishSpec) (AdminResult, error) {
	if !a.cfg.AdminOn() {
		a.obs.Event("phase2.disabled", map[string]string{"op": "publish_plan"})
		return AdminResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if spec.TenantID == "" || spec.SiteID == "" || spec.PlanCode == "" {
		return AdminResult{}, &Error{Code: ErrInvalidInput, Msg: "publish plan: missing tenant/site/code"}
	}
	if err := validatePlanSpec(&spec); err != nil {
		return AdminResult{Reason: "invalid_plan_spec"}, nil
	}
	var res AdminResult
	err := a.repo.WithTx(ctx, func(tx CommerceAdminTx) error {
		planID, err := tx.UpsertPlan(ctx, spec.TenantID, spec.SiteID, spec.PlanCode)
		if err != nil {
			return err
		}
		revNo, err := tx.NextPlanRevisionNo(ctx, planID)
		if err != nil {
			return err
		}
		revID, err := tx.InsertPlanRevision(ctx, spec, planID, revNo)
		if err != nil {
			return err
		}
		if err := tx.SetPlanCurrentRevision(ctx, planID, revID); err != nil {
			return err
		}
		res = AdminResult{PackageID: planID, CurrentRevisionID: revID, Reason: "published"}
		return nil
	})
	if err != nil {
		return AdminResult{}, &Error{Code: ErrRepo, Msg: "publish plan"}
	}
	return res, nil
}

// GetGrace returns the site checkout-grace configuration.
func (a *CommerceAdmin) GetGrace(ctx context.Context, tenantID, siteID string) (GraceConfig, bool, error) {
	if !a.cfg.AdminOn() {
		return GraceConfig{}, true, nil
	}
	gc, err := a.repo.GetGraceConfig(ctx, tenantID, siteID)
	if err != nil {
		return GraceConfig{}, false, &Error{Code: ErrRepo, Msg: "grace config"}
	}
	return gc, false, nil
}

// SetGrace validates and stores the site checkout-grace package. It creates NO grace entitlement or
// checkout behavior (that is Phase 3); it only records which package revision would be used, and only if
// that revision is active, CHECKOUT_GRACE, free, exactly NOT_REQUIRED and pinned to a valid plan revision.
func (a *CommerceAdmin) SetGrace(ctx context.Context, tenantID, siteID, packageRevisionID string, config map[string]any) (AdminResult, error) {
	if !a.cfg.AdminOn() {
		a.obs.Event("phase2.disabled", map[string]string{"op": "set_grace"})
		return AdminResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if tenantID == "" || siteID == "" || packageRevisionID == "" {
		return AdminResult{}, &Error{Code: ErrInvalidInput, Msg: "set_grace: missing tenant/site/package_revision"}
	}
	var res AdminResult
	err := a.repo.WithTx(ctx, func(tx CommerceAdminTx) error {
		c, err := tx.GraceCandidateValid(ctx, tenantID, siteID, packageRevisionID)
		if err != nil {
			return err
		}
		if !c.Found {
			res = AdminResult{Reason: "grace_package_not_found"}
			return nil
		}
		if !c.PackageActive {
			res = AdminResult{Reason: "grace_package_inactive"}
			return nil
		}
		if c.PackageType != "CHECKOUT_GRACE" {
			res = AdminResult{Reason: "grace_package_wrong_type"}
			return nil
		}
		if c.PriceMinor != 0 || !c.SettlementOnly {
			res = AdminResult{Reason: "grace_package_not_free"}
			return nil
		}
		if _, cerr := ValidateCurrency(c.Currency, c.CurrencyExp); cerr != nil {
			res = AdminResult{Reason: "grace_package_bad_currency"}
			return nil
		}
		if !c.PlanRevValid {
			res = AdminResult{Reason: "grace_package_bad_plan"}
			return nil
		}
		if err := tx.UpsertGraceConfig(ctx, tenantID, siteID, packageRevisionID, config); err != nil {
			return err
		}
		res = AdminResult{Reason: "ok"}
		return nil
	})
	if err != nil {
		return AdminResult{}, &Error{Code: ErrRepo, Msg: "set_grace"}
	}
	return res, nil
}

// Quotes / Purchases return guest-PII-free inspection rows (read-only).
func (a *CommerceAdmin) Quotes(ctx context.Context, tenantID, siteID string, limit int) ([]QuoteInspect, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out, err := a.repo.ListQuotes(ctx, tenantID, siteID, limit)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "quotes"}
	}
	return out, false, nil
}
func (a *CommerceAdmin) Purchases(ctx context.Context, tenantID, siteID string, limit int) ([]PurchaseInspect, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out, err := a.repo.ListPurchases(ctx, tenantID, siteID, limit)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "purchases"}
	}
	return out, false, nil
}

// SetActive activates or deactivates a package (a package-row toggle; never a revision-history rewrite).
func (a *CommerceAdmin) SetActive(ctx context.Context, tenantID, siteID, packageID string, active bool) (AdminResult, error) {
	if !a.cfg.AdminOn() {
		a.obs.Event("phase2.disabled", map[string]string{"op": "set_active"})
		return AdminResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if tenantID == "" || siteID == "" || packageID == "" {
		return AdminResult{}, &Error{Code: ErrInvalidInput, Msg: "set_active: missing tenant/site/package"}
	}
	err := a.repo.WithTx(ctx, func(tx CommerceAdminTx) error {
		return tx.SetPackageActive(ctx, tenantID, siteID, packageID, active)
	})
	if err != nil {
		return AdminResult{}, &Error{Code: ErrRepo, Msg: "set_active"}
	}
	return AdminResult{PackageID: packageID, Reason: "ok"}, nil
}
