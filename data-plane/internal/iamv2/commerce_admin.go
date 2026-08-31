package iamv2

import (
	"context"
	"errors"
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
	// aggregateOnlineTime is the Phase-6 capability. OFF by default, which is what keeps every existing
	// deployment publishing VALIDITY_WINDOW revisions and nothing else.
	aggregateOnlineTime bool
}

// AllowAggregateOnlineTime turns on publication of AGGREGATE_ONLINE_TIME plan revisions.
//
// It is a separate call rather than a constructor argument because it answers a different question from the
// Phase-2 commerce flags: whether this build may OFFER the mode at all. Existing revisions are untouched
// either way -- a plan revision is immutable, so a revision published as VALIDITY_WINDOW stays
// VALIDITY_WINDOW forever, and turning the capability on can only affect revisions published afterwards.
func (a *CommerceAdmin) AllowAggregateOnlineTime(on bool) *CommerceAdmin {
	a.aggregateOnlineTime = on
	return a
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

	// WHAT THE PLAN ACTUALLY IS, from its current revision.
	//
	// The summary used to carry a code, a revision count and a revision UUID and nothing else, so the operator
	// screen listing service plans could not say what any of them DID. Choosing which plan to attach to a
	// guest-facing package meant opening each one's revision history in turn and reading it — for the single
	// most consequential field on the package form. These come from the current revision, so they are the
	// values in force right now rather than the newest ones drafted.
	Name                 *string `json:"name,omitempty"`
	DownKbps             *int    `json:"down_kbps,omitempty"`
	UpKbps               *int    `json:"up_kbps,omitempty"`
	MaxConcurrentDevices *int    `json:"max_concurrent_devices,omitempty"`
	DeviceLimitPolicy    *string `json:"device_limit_policy,omitempty"`
	IdleTimeoutSeconds   *int    `json:"idle_timeout_seconds,omitempty"`
	MaxSessionSeconds    *int    `json:"max_continuous_session_seconds,omitempty"`
	TimeQuotaSeconds     *int64  `json:"time_quota_seconds,omitempty"`
	DataQuotaBytes       *int64  `json:"data_quota_bytes,omitempty"`
	TimeAccountingMode   *string `json:"time_accounting_mode,omitempty"`
	SpeedAllocation      *string `json:"speed_allocation,omitempty"`
}

// GraceConfig is the read/write shape for site_checkout_grace_config.
type GraceConfig struct {
	GracePackageRevisionID string         `json:"grace_package_revision_id"`
	Config                 map[string]any `json:"config"`
	// ConfigVersion is what the operator must send back as expected_version when publishing. Without it on the
	// read, the mandatory optimistic-concurrency check on the write would be unsatisfiable through the product:
	// the caller would have to guess the number that decides whether their change is accepted. Zero means
	// nothing is published yet, which is also the correct expected_version for a first publication.
	ConfigVersion int `json:"config_version"`
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
	// IsSystem is the contract's is_system = true requirement for a grace package. The grace package is a
	// HIDDEN, system-provisioned fallback, not an operator catalogue entry, and validating it was missing.
	IsSystem     bool
	PlanRevValid bool
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
	// SpeedAllocation is PER_DEVICE or SHARED. Empty publishes PER_DEVICE, so an operator (or an older client)
	// that says nothing about it gets exactly what every revision published so far promised.
	SpeedAllocation string
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
		// A REFUSAL from inside the transaction must survive as a refusal. Collapsing everything to ErrRepo
		// turned "that reserved code is not yours" into an opaque repository failure, which the HTTP layer
		// then reported as 500 internal -- telling an operator the server broke when it had actually made a
		// deliberate policy decision they could act on.
		var de *Error
		if errors.As(err, &de) && de.Code == ErrInvalidInput {
			return AdminResult{}, de
		}
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
func validatePlanSpec(spec *PlanPublishSpec, allowAggregate bool) error {
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
	// THE DEFAULT IS THE OLD BEHAVIOUR, and it is a default rather than a branch: a spec that omits the mode
	// publishes VALIDITY_WINDOW, exactly as every revision published so far did.
	if spec.TimeAccountingMode == "" {
		spec.TimeAccountingMode = "VALIDITY_WINDOW"
	}
	// SPEED ALLOCATION, on the same principle. PER_DEVICE is what an unstated plan has always meant, so it is
	// the default; SHARED has to be chosen. An unknown value is refused rather than defaulted, because
	// defaulting it would hand a property that asked for one shared ceiling a per-device rate for every guest
	// and overspend its capacity with nothing failing.
	if spec.SpeedAllocation == "" {
		spec.SpeedAllocation = "PER_DEVICE"
	}
	if spec.SpeedAllocation != "PER_DEVICE" && spec.SpeedAllocation != "SHARED" {
		return &Error{Code: ErrInvalidInput, Msg: "unknown speed_allocation"}
	}
	// A SHARED plan without a rate is not a shared plan: with no ceiling to divide there is nothing to share,
	// and the applier would build a group at the unlimited fallback. An operator choosing SHARED means to cap
	// the account, so the cap has to be there.
	if spec.SpeedAllocation == "SHARED" &&
		((spec.DownKbps == nil || *spec.DownKbps <= 0) || (spec.UpKbps == nil || *spec.UpKbps <= 0)) {
		return &Error{Code: ErrInvalidInput,
			Msg: "SHARED speed_allocation requires positive down_kbps and up_kbps to share"}
	}
	switch spec.TimeAccountingMode {
	case "VALIDITY_WINDOW":
	case "AGGREGATE_ONLINE_TIME":
		if !allowAggregate {
			// Capability-disabled: the mode exists in the schema but this build may not offer it.
			return &Error{Code: ErrInvalidInput, Msg: "unsupported time_accounting_mode"}
		}
		// A budget is the whole point of the mode. Publishing it without one would create a revision whose
		// entitlements can never exhaust -- an aggregate package that behaves like an unlimited one, which is
		// the opposite of what an operator selecting it meant.
		if spec.TimeQuotaSeconds == nil || *spec.TimeQuotaSeconds <= 0 {
			return &Error{Code: ErrInvalidInput,
				Msg: "AGGREGATE_ONLINE_TIME requires a positive time_quota_seconds"}
		}
	default:
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
	if err := validatePlanSpec(&spec, a.aggregateOnlineTime); err != nil {
		// The specific reason, not a generic label. validatePlanSpec already
		// distinguishes "unknown device_limit_policy" from "out of range" from
		// "AGGREGATE_ONLINE_TIME requires a positive time_quota_seconds", and
		// this surface is the trusted admin one -- the file header says
		// validation reasons are returned verbatim here precisely because the
		// operator is not a guest. Collapsing all of them to invalid_plan_spec
		// threw that away and left the operator guessing which of ten fields
		// was wrong; it cost a real debugging cycle on a one-word enum typo.
		// Guest-facing surfaces still get their uniform refusal elsewhere.
		var e *Error
		if errors.As(err, &e) && e.Msg != "" {
			return AdminResult{Reason: e.Msg}, nil
		}
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
		// Same reason as the package path: a reserved-code refusal is a decision, not a repository fault.
		var de *Error
		if errors.As(err, &de) && de.Code == ErrInvalidInput {
			return AdminResult{}, de
		}
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

// SetGrace is RETIRED (D32) and deliberately no longer exists.
//
// It took a packageRevisionID CHOSEN BY THE OPERATOR and validated it against a list of properties. That was
// the right design while an operator picked the grace package, and it cannot be made right now: the checkout
// conversion judges the pinned revision with iam_v2.grace_package_mismatch_reason, which demands EXACT
// equality between the published policy and the revision's plan scalars, duration policy and declared policy
// version. A package the operator selected can satisfy that only by coincidence, and the property list here
// checked none of those equalities -- so a save could succeed and the grace still never convert.
//
// The replacement is PublishSystemGracePolicy: the operator supplies POLICY, the system derives the plan and
// package revisions that express it exactly, the same matcher the conversion uses is consulted before the
// write, and publication goes through the audited, versioned boundary with an actor and a reason code.
//
// The name is left documented rather than silently deleted because "where did SetGrace go" is the first
// question anyone reading the old call sites will have.

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

// AggregateOnlineTimeAllowed reports whether this admin may publish AGGREGATE_ONLINE_TIME plan revisions.
// It exists so composition tests can assert what the REAL startup wiring produced, rather than asserting
// against a validator called directly with a flag the test chose itself.
func (a *CommerceAdmin) AggregateOnlineTimeAllowed() bool { return a != nil && a.aggregateOnlineTime }
