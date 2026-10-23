package iamv2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CommerceAdmin publishes immutable package revisions and moves the current-revision pointer atomically.
// When the admin surface is OFF it holds a nil repository, issues no SQL and returns a disabled result.
type CommerceAdmin struct {
	cfg  CommerceConfig
	repo CommerceAdminRepository
	obs  Observer
	now  func() time.Time
	// aggregateOnlineTime is off by default: only VALIDITY_WINDOW revisions are published.
	aggregateOnlineTime bool
}

// AllowAggregateOnlineTime lets new plan revisions be published as AGGREGATE_ONLINE_TIME; existing
// (immutable) revisions are unaffected.
func (a *CommerceAdmin) AllowAggregateOnlineTime(on bool) *CommerceAdmin {
	a.aggregateOnlineTime = on
	return a
}

// NewCommerceAdmin: repo must be nil while the master flag is OFF and non-nil when enabled (fail closed).
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

type CommerceAdminRepository interface {
	WithTx(ctx context.Context, fn func(CommerceAdminTx) error) error
	ListPackages(ctx context.Context, tenantID, siteID string) ([]PackageSummary, error)
	ListPackageRevisions(ctx context.Context, tenantID, siteID, packageID string) ([]RevisionInfo, error)
	GetPackageCurrent(ctx context.Context, tenantID, siteID, packageID string) (PackageCurrent, error)
	ListPlans(ctx context.Context, tenantID, siteID string) ([]PlanSummary, error)
	ListPlanRevisions(ctx context.Context, tenantID, siteID, planID string) ([]RevisionInfo, error)
	GetGraceConfig(ctx context.Context, tenantID, siteID string) (GraceConfig, error)
	ListQuotes(ctx context.Context, tenantID, siteID string, limit int) ([]QuoteInspect, error)
	ListPurchases(ctx context.Context, tenantID, siteID string, limit int) ([]PurchaseInspect, error)
	// Blockers are what the database says still depends on the item; the Delete calls are refused by the database otherwise.
	PackageBlockers(ctx context.Context, tenantID, siteID, packageID string) ([]CatalogueBlocker, error)
	PlanBlockers(ctx context.Context, tenantID, siteID, planID string) ([]CatalogueBlocker, error)
	DeleteUnusedPackage(ctx context.Context, tenantID, siteID, packageID, operatorID, reason string) error
	DeleteUnusedPlan(ctx context.Context, tenantID, siteID, planID, operatorID, reason string) error
}

// CommerceAdminTx: a whole publish runs on one tx.
type CommerceAdminTx interface {
	UpsertPackage(ctx context.Context, tenantID, siteID, code string) (packageID string, err error)
	// CreatePackage refuses with ErrCodeExists when the code is taken; UpsertPackage revises an existing one.
	CreatePackage(ctx context.Context, tenantID, siteID, code string) (packageID string, err error)
	NextRevisionNo(ctx context.Context, packageID string) (int, error)
	PlanRevisionBelongs(ctx context.Context, tenantID, siteID, planRevisionID string) (bool, error)
	InsertPackageRevision(ctx context.Context, spec PackagePublishSpec, packageID string, revNo int) (revisionID string, err error)
	InsertEligibilityRule(ctx context.Context, tenantID, siteID, revisionID string, rule EligibilityRule) error
	InsertGrantTier(ctx context.Context, tenantID, siteID, revisionID string, tier GrantTier) error
	InsertRoomChargeMapping(ctx context.Context, tenantID, siteID, revisionID string, m RoomChargeMapping) error
	SetCurrentRevision(ctx context.Context, packageID, revisionID string) error
	SetPackageActive(ctx context.Context, tenantID, siteID, packageID string, active bool) error

	UpsertPlan(ctx context.Context, tenantID, siteID, code string) (planID string, err error)
	CreatePlan(ctx context.Context, tenantID, siteID, code string) (planID string, err error)
	NextPlanRevisionNo(ctx context.Context, planID string) (int, error)
	InsertPlanRevision(ctx context.Context, spec PlanPublishSpec, planID string, revNo int) (revisionID string, err error)
	SetPlanCurrentRevision(ctx context.Context, planID, revisionID string) error

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
	// CurrencyExponent is absent when the revision recorded none, so a client can say it assumed one.
	CurrencyExponent *int `json:"currency_exponent,omitempty"`
}

type PlanSummary struct {
	PlanID            string `json:"plan_id"`
	Code              string `json:"code"`
	Enabled           bool   `json:"enabled"`
	CurrentRevisionID string `json:"current_revision_id"`
	RevisionCount     int    `json:"revision_count"`

	// Fields below come from the current revision (the values in force), not the newest drafted.
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

	// UsedByActivePackages counts the guest-facing offers that change meaning if packages are repinned to a new revision.
	UsedByActivePackages int `json:"used_by_active_packages"`
}

type GraceConfig struct {
	GracePackageRevisionID string         `json:"grace_package_revision_id"`
	Config                 map[string]any `json:"config"`
	// ConfigVersion is sent back as expected_version when publishing; zero means nothing is published yet.
	ConfigVersion int `json:"config_version"`
}

type GraceCandidate struct {
	Found          bool
	PackageActive  bool
	PackageType    string
	PriceMinor     int64
	Currency       string
	CurrencyExp    int
	SettlementOnly bool // settlement_methods == exactly {NOT_REQUIRED}
	// IsSystem: a grace package must be a hidden, system-provisioned fallback, not an operator catalogue entry.
	IsSystem     bool
	PlanRevValid bool
}

// QuoteInspect and PurchaseInspect carry no guest PII.
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
	// SpeedAllocation is PER_DEVICE or SHARED; empty publishes PER_DEVICE.
	SpeedAllocation string
	// CreateOnly: see PackagePublishSpec.CreateOnly.
	CreateOnly bool
}

type PackageSummary struct {
	PackageID         string `json:"package_id"`
	Code              string `json:"code"`
	Active            bool   `json:"active"`
	CurrentRevisionID string `json:"current_revision_id"`
	RevisionCount     int    `json:"revision_count"`

	// Read-only projections of the CURRENT revision and the plan revision it pins: what a guest granted now receives.
	Name       *string `json:"name,omitempty"`
	PriceMinor *int64  `json:"price_minor,omitempty"`
	Currency   *string `json:"currency,omitempty"`
	// CurrencyExponent is the minor-unit exponent of PriceMinor (2 for USD: 500 = 5.00), as published.
	CurrencyExponent *int    `json:"currency_exponent,omitempty"`
	PackageType      *string `json:"package_type,omitempty"`
	VisibleFrom      *string `json:"visible_from,omitempty"`
	VisibleUntil     *string `json:"visible_until,omitempty"`
	// Rule and tier counts are deliberately absent: svc_edged has no SELECT on those tables.

	// ServicePlanRevisionID is carried because an unchanged republish must re-pin the same revision.
	ServicePlanID         *string `json:"service_plan_id,omitempty"`
	ServicePlanRevisionID *string `json:"service_plan_revision_id,omitempty"`
	ServicePlanCode       *string `json:"service_plan_code,omitempty"`
	ServicePlanRevisionNo *int    `json:"service_plan_revision_no,omitempty"`
	DownKbps              *int    `json:"down_kbps,omitempty"`
	UpKbps                *int    `json:"up_kbps,omitempty"`
	MaxConcurrentDevices  *int    `json:"max_concurrent_devices,omitempty"`
	DeviceLimitPolicy     *string `json:"device_limit_policy,omitempty"`
	TimeQuotaSeconds      *int64  `json:"time_quota_seconds,omitempty"`
	DataQuotaBytes        *int64  `json:"data_quota_bytes,omitempty"`
	SpeedAllocation       *string `json:"speed_allocation,omitempty"`

	// PlanHasNewerRevision: publishing a plan revision does not move packages onto it.
	PlanHasNewerRevision bool `json:"plan_has_newer_revision"`
}

// PackageCurrent is the full current configuration, so a save cannot silently drop fields: publishing replaces
// the whole spec, and a form that omitted rules or tiers would change who the package is offered to.
type PackageCurrent struct {
	PackageID             string            `json:"package_id"`
	Code                  string            `json:"code"`
	Active                bool              `json:"active"`
	RevisionID            string            `json:"revision_id"`
	RevisionNo            int               `json:"revision_no"`
	ServicePlanRevisionID string            `json:"service_plan_revision_id"`
	PackageType           string            `json:"package_type"`
	PriceMinor            int64             `json:"price_minor"`
	Currency              string            `json:"currency"`
	CurrencyExponent      *int              `json:"currency_exponent,omitempty"`
	SettlementMethods     []string          `json:"settlement_methods"`
	Display               map[string]any    `json:"display"`
	DurationPolicy        map[string]any    `json:"duration_policy"`
	EligibilityRules      []EligibilityRule `json:"eligibility_rules"`
	GrantTiers            []GrantTier       `json:"grant_tiers"`
	VisibleFrom           *string           `json:"visible_from,omitempty"`
	VisibleUntil          *string           `json:"visible_until,omitempty"`
	// DataAllocationPolicy must be handed back UNCHANGED on save, or an unrelated rename would turn a
	// PER_STAY_NIGHT package into a flat one.
	DataAllocationPolicy map[string]any      `json:"data_allocation_policy,omitempty"`
	RoomChargeMappings   []RoomChargeMapping `json:"room_charge_mappings"`

	// These three are carried so a republish (operator save or automatic network replacement) preserves them
	// instead of resetting them.
	PlanOverrides       map[string]any `json:"plan_overrides,omitempty"`
	Renewable           bool           `json:"renewable"`
	MaxPurchasesPerStay *int           `json:"max_purchases_per_stay,omitempty"`
}

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
	// DataAllocationPolicy turns a stay into a byte allowance. Nil or {"mode":"FIXED"} uses the plan revision's quota unchanged.
	DataAllocationPolicy map[string]any
	// PackageType is carried through a republish so a revision keeps its kind; empty means GENERAL.
	PackageType string
	// CreateOnly refuses with ErrCodeExists when the code is in use, instead of becoming a revision of the owner.
	CreateOnly bool
	// PriceMinor 0 with no methods publishes a Free package. A priced package names its currency and at least
	// one of Voucher, Card payment and Room charge; Room charge needs a mapping per interface.
	PriceMinor         int64
	Currency           string
	CurrencyExponent   int
	AcquisitionMethods []string
	RoomChargeMappings []RoomChargeMapping
	// PlanOverrides, Renewable and MaxPurchasesPerStay are carried so a republish preserves them.
	PlanOverrides       map[string]any
	Renewable           bool
	MaxPurchasesPerStay *int

	// DryRun runs every publication gate and writes nothing (Reason "validated"). A client-network replacement
	// uses it to learn whether every dependent package can be carried forward, via the real gates so the answer cannot drift.
	DryRun bool
	// PriorDurationPolicy is the replaced revision's policy: a republish carrying its existing, now-past
	// FIXED_AT end unchanged is not a duration decision and must not be blocked.
	PriorDurationPolicy map[string]any
}

// RoomChargeMapping is the posting code (and optional tax code and rate) a room-charge purchase books under on one PMS interface.
type RoomChargeMapping struct {
	PMSInterfaceID string `json:"pms_interface_id"`
	PostingCode    string `json:"posting_code"`
	TaxCode        string `json:"tax_code,omitempty"`
	TaxRateBP      *int   `json:"tax_rate_bp,omitempty"`
}

type AdminResult struct {
	Disabled          bool
	PackageID         string
	CurrentRevisionID string
	Reason            string
}

func (a *CommerceAdmin) ListPackages(ctx context.Context, tenantID, siteID string) ([]PackageSummary, bool, error) {
	if !a.cfg.AdminOn() {
		return nil, true, nil
	}
	out, err := a.repo.ListPackages(ctx, tenantID, siteID)
	if err != nil {
		return nil, false, &Error{Code: ErrRepo, Msg: "list packages"}
	}
	return out, false, nil
}

func (a *CommerceAdmin) GetPackageCurrent(ctx context.Context, tenantID, siteID, packageID string) (PackageCurrent, bool, error) {
	if !a.cfg.AdminOn() {
		return PackageCurrent{}, true, nil
	}
	out, err := a.repo.GetPackageCurrent(ctx, tenantID, siteID, packageID)
	if err != nil {
		// Passed through: it means "do not offer to edit this", not "something broke".
		if errors.Is(err, ErrPackageConditionsUnreadable) {
			return PackageCurrent{}, false, err
		}
		return PackageCurrent{}, false, &Error{Code: ErrRepo, Msg: "read package"}
	}
	return out, false, nil
}

// PublishRevision publishes an immutable revision and moves the current pointer atomically. It fails closed:
// a malformed rule or tier, a bad duration policy, or a plan revision from another tenant/site is rejected before any write.
func (a *CommerceAdmin) PublishRevision(ctx context.Context, spec PackagePublishSpec) (AdminResult, error) {
	if !a.cfg.AdminOn() {
		a.obs.Event("phase2.disabled", map[string]string{"op": "publish"})
		return AdminResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if spec.TenantID == "" || spec.SiteID == "" || spec.PackageCode == "" || spec.ServicePlanRevisionID == "" {
		return AdminResult{}, &Error{Code: ErrInvalidInput, Msg: "publish: missing tenant/site/code/plan_revision"}
	}
	// Validate every rule and tier before any write; the refusal carries the validator's reason (trusted operator surface).
	for _, rule := range spec.EligibilityRules {
		if err := ValidateEligibilityRule(rule); err != nil {
			return AdminResult{Reason: reasonWithDetail("invalid_eligibility_rule", rule.Type, err)}, nil
		}
	}
	if why := contradictoryPriorPurchase(spec.EligibilityRules); why != "" {
		return AdminResult{Reason: "invalid_eligibility_rule: " + why}, nil
	}
	if len(spec.GrantTiers) == 0 {
		return AdminResult{Reason: "no_grant_tiers"}, nil
	}
	seenOrder := map[int]bool{}
	for _, tier := range spec.GrantTiers {
		if err := ValidateGrantTier(tier); err != nil {
			return AdminResult{Reason: reasonWithDetail("invalid_grant_tier", "", err)}, nil
		}
		// Two steps with the same order collide on the tier key; refuse it here in words, not as a 500.
		if seenOrder[tier.Order] {
			return AdminResult{Reason: fmt.Sprintf("invalid_grant_tier: two speed steps share the order %d; give each step its own order", tier.Order)}, nil
		}
		seenOrder[tier.Order] = true
	}
	// The immutable duration policy must resolve (PMS/checkout/local-time modes are capability-disabled).
	if _, _, err := ResolveEndPolicy(spec.DurationPolicy, a.now()); err != nil && !unchangedPastFixedEnd(spec, err) {
		return AdminResult{Reason: reasonWithDetail("invalid_duration_policy", "", err)}, nil
	}
	if spec.VisibleFrom != nil && spec.VisibleUntil != nil && !spec.VisibleFrom.Before(*spec.VisibleUntil) {
		return AdminResult{Reason: "invalid_sale_window"}, nil
	}
	// Validated at publication: the revision is immutable, so a policy failing at grant time would fail in
	// front of a guest. The database CHECK holds the same shape; this gives a readable reason.
	if _, err := ParseDataAllocationPolicy(spec.DataAllocationPolicy); err != nil {
		return AdminResult{Reason: "invalid_data_allocation_policy: " + err.Error()}, nil
	}
	if reason := normalizeAcquisition(&spec); reason != "" {
		return AdminResult{Reason: reason}, nil
	}
	if spec.DryRun {
		return AdminResult{Reason: "validated"}, nil
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
		var pkgID string
		if spec.CreateOnly {
			pkgID, err = tx.CreatePackage(ctx, spec.TenantID, spec.SiteID, spec.PackageCode)
		} else {
			pkgID, err = tx.UpsertPackage(ctx, spec.TenantID, spec.SiteID, spec.PackageCode)
		}
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
		for _, m := range spec.RoomChargeMappings {
			if err := tx.InsertRoomChargeMapping(ctx, spec.TenantID, spec.SiteID, revID, m); err != nil {
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
		// A refusal from inside the transaction must stay a refusal, not collapse to an opaque ErrRepo (a 500).
		var de *Error
		if errors.As(err, &de) && de.Code == ErrInvalidInput {
			return AdminResult{}, de
		}
		if errors.Is(err, ErrCodeExists) {
			return AdminResult{}, ErrCodeExists
		}
		return AdminResult{}, &Error{Code: ErrRepo, Msg: "publish"}
	}
	return res, nil
}

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

// validatePlanSpec fails closed on a malformed plan spec.
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
	// ADMIN_APPROVAL stays in the enum (published history names it) but cannot be published: there is no
	// approval workflow, and the database refuses it.
	if spec.DeviceLimitPolicy == "ADMIN_APPROVAL" {
		return &Error{Code: ErrInvalidInput, Msg: "device_limit_policy ADMIN_APPROVAL is not available: there is " +
			"no approval workflow, so a device over the limit could never be admitted. Choose REJECT_NEW_DEVICE " +
			"(refuse the new device) or DISCONNECT_OLDEST (disconnect the oldest device to admit the new one)"}
	}
	if spec.TimeAccountingMode == "" {
		spec.TimeAccountingMode = "VALIDITY_WINDOW"
	}
	// An unknown value is refused, not defaulted: defaulting would give a per-device rate where a shared ceiling was meant.
	if spec.SpeedAllocation == "" {
		spec.SpeedAllocation = "PER_DEVICE"
	}
	if spec.SpeedAllocation != "PER_DEVICE" && spec.SpeedAllocation != "SHARED" {
		return &Error{Code: ErrInvalidInput, Msg: "unknown speed_allocation"}
	}
	// Without a ceiling the applier would build the shared group at the unlimited fallback.
	if spec.SpeedAllocation == "SHARED" &&
		((spec.DownKbps == nil || *spec.DownKbps <= 0) || (spec.UpKbps == nil || *spec.UpKbps <= 0)) {
		return &Error{Code: ErrInvalidInput,
			Msg: "SHARED speed_allocation requires positive down_kbps and up_kbps to share"}
	}
	switch spec.TimeAccountingMode {
	case "VALIDITY_WINDOW":
	case "AGGREGATE_ONLINE_TIME":
		if !allowAggregate {
			return &Error{Code: ErrInvalidInput, Msg: "unsupported time_accounting_mode"}
		}
		// Without a budget the entitlements could never exhaust.
		if spec.TimeQuotaSeconds == nil || *spec.TimeQuotaSeconds <= 0 {
			return &Error{Code: ErrInvalidInput,
				Msg: "AGGREGATE_ONLINE_TIME requires a positive time_quota_seconds"}
		}
	default:
		return &Error{Code: ErrInvalidInput, Msg: "unsupported time_accounting_mode"}
	}
	// Each bound names its field, its limit and the value sent.
	nn := func(field string, p *int, max int) error {
		if p != nil && (*p < 0 || *p > max) {
			return &Error{Code: ErrInvalidInput,
				Msg: fmt.Sprintf("%s must be between 0 and %d, but %d was sent", field, max, *p)}
		}
		return nil
	}
	nn64 := func(field string, p *int64, max int64) error {
		if p != nil && (*p < 0 || *p > max) {
			return &Error{Code: ErrInvalidInput,
				Msg: fmt.Sprintf("%s must be between 0 and %d, but %d was sent", field, max, *p)}
		}
		return nil
	}
	for _, e := range []error{
		nn("down_kbps", spec.DownKbps, maxKbps), nn("up_kbps", spec.UpKbps, maxKbps),
		nn("idle_timeout_seconds", spec.IdleTimeoutSeconds, maxIdleSeconds),
		nn("max_continuous_session_seconds", spec.MaxContinuousSessionSeconds, maxSessionSeconds),
		nn64("time_quota_seconds", spec.TimeQuotaSeconds, maxTimeQuotaSecond),
		nn64("data_quota_bytes", spec.DataQuotaBytes, maxDataQuotaBytes),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

// contradictoryPriorPurchase refuses purchase-history rules that cannot be satisfied together: forbids_prior
// and requires_prior both test this package's own history, so together nobody passes.
func contradictoryPriorPurchase(rules []EligibilityRule) string {
	var forbids, requires, n int
	for _, r := range rules {
		if strings.ToUpper(strings.TrimSpace(r.Type)) != RulePriorPurchase {
			continue
		}
		n++
		if b, _ := r.Value["forbids_prior"].(bool); b {
			forbids++
		}
		if b, _ := r.Value["requires_prior"].(bool); b {
			requires++
		}
	}
	if forbids > 0 && requires > 0 {
		return "PRIOR_PURCHASE: the free-access limit (once per client) and \"requires an earlier purchase\" " +
			"both test this package's own history, so together they offer the package to nobody; keep one of them"
	}
	if n > 1 {
		return "PRIOR_PURCHASE: a package carries at most one purchase-history rule"
	}
	return ""
}

// unchangedPastFixedEnd: the refusal is only "FIXED_AT ends_at must be in the future" for an end the package already had.
func unchangedPastFixedEnd(spec PackagePublishSpec, err error) bool {
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Msg, "must be in the future") || spec.PriorDurationPolicy == nil {
		return false
	}
	mode := func(m map[string]any) string {
		s, _ := m["end_mode"].(string)
		return strings.ToUpper(strings.TrimSpace(s))
	}
	end := func(m map[string]any) string { s, _ := m["ends_at"].(string); return strings.TrimSpace(s) }
	if mode(spec.DurationPolicy) != "FIXED_AT" || mode(spec.PriorDurationPolicy) != "FIXED_AT" {
		return false
	}
	a, aerr := time.Parse(time.RFC3339, end(spec.DurationPolicy))
	b, berr := time.Parse(time.RFC3339, end(spec.PriorDurationPolicy))
	return aerr == nil && berr == nil && a.Equal(b)
}

// reasonWithDetail appends the validator's sentence to the machine label; the label stays first because callers match on it.
func reasonWithDetail(label, ruleType string, err error) string {
	var e *Error
	if !errors.As(err, &e) || e.Msg == "" {
		if ruleType != "" {
			return label + ": " + ruleType
		}
		return label
	}
	if ruleType != "" {
		return label + ": " + ruleType + ": " + e.Msg
	}
	return label + ": " + e.Msg
}

func (a *CommerceAdmin) PublishPlanRevision(ctx context.Context, spec PlanPublishSpec) (AdminResult, error) {
	if !a.cfg.AdminOn() {
		a.obs.Event("phase2.disabled", map[string]string{"op": "publish_plan"})
		return AdminResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if spec.TenantID == "" || spec.SiteID == "" || spec.PlanCode == "" {
		return AdminResult{}, &Error{Code: ErrInvalidInput, Msg: "publish plan: missing tenant/site/code"}
	}
	if err := validatePlanSpec(&spec, a.aggregateOnlineTime); err != nil {
		// The operator is trusted, so the specific reason is returned verbatim; guest-facing surfaces keep a uniform refusal.
		var e *Error
		if errors.As(err, &e) && e.Msg != "" {
			return AdminResult{Reason: e.Msg}, nil
		}
		return AdminResult{Reason: "invalid_plan_spec"}, nil
	}
	var res AdminResult
	err := a.repo.WithTx(ctx, func(tx CommerceAdminTx) error {
		var planID string
		var err error
		if spec.CreateOnly {
			planID, err = tx.CreatePlan(ctx, spec.TenantID, spec.SiteID, spec.PlanCode)
		} else {
			planID, err = tx.UpsertPlan(ctx, spec.TenantID, spec.SiteID, spec.PlanCode)
		}
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
		var de *Error
		if errors.As(err, &de) && de.Code == ErrInvalidInput {
			return AdminResult{}, de
		}
		if errors.Is(err, ErrCodeExists) {
			return AdminResult{}, ErrCodeExists
		}
		return AdminResult{}, &Error{Code: ErrRepo, Msg: "publish plan"}
	}
	return res, nil
}

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

// AggregateOnlineTimeAllowed lets composition tests assert what the real startup wiring produced.
func (a *CommerceAdmin) AggregateOnlineTimeAllowed() bool { return a != nil && a.aggregateOnlineTime }

// normalizeAcquisition fills Free defaults and returns an operator-readable refusal reason or "". The database CHECKs hold the same rules.
func normalizeAcquisition(spec *PackagePublishSpec) string {
	if len(spec.AcquisitionMethods) == 0 && spec.PriceMinor == 0 {
		spec.AcquisitionMethods = []string{AcquireFree}
	}
	if err := ValidateAcquisitionMethods(spec.PriceMinor, spec.AcquisitionMethods); err != nil {
		return "invalid_acquisition_methods: " + err.Error()
	}
	if spec.PriceMinor == 0 && strings.TrimSpace(spec.Currency) == "" {
		spec.Currency, spec.CurrencyExponent = "USD", 2
	} else {
		c, err := ValidateCurrency(spec.Currency, spec.CurrencyExponent)
		if err != nil {
			return "invalid_currency: " + err.Error()
		}
		spec.Currency = c
	}
	// Stripe accepts three-decimal currencies (BHD, JOD, KWD, OMR, TND) only with a last minor-unit digit of 0
	// and would refuse the checkout after the client chose the package.
	if hasMethod(spec.AcquisitionMethods, AcquireCard) && spec.CurrencyExponent == 3 && spec.PriceMinor%10 != 0 {
		return "invalid_currency: card payment providers accept " + spec.Currency +
			" prices only in steps of 0.010 (the last of the three decimals must be 0)"
	}
	room := hasMethod(spec.AcquisitionMethods, AcquireRoomCharge)
	if !room && len(spec.RoomChargeMappings) > 0 {
		return "invalid_room_charge_mapping: mappings are only for packages acquired by room charge"
	}
	if room && len(spec.RoomChargeMappings) == 0 {
		return "invalid_room_charge_mapping: room charge needs a posting code for at least one PMS interface"
	}
	seen := map[string]bool{}
	for i := range spec.RoomChargeMappings {
		m := &spec.RoomChargeMappings[i]
		m.PMSInterfaceID = strings.TrimSpace(m.PMSInterfaceID)
		m.PostingCode = strings.TrimSpace(m.PostingCode)
		m.TaxCode = strings.TrimSpace(m.TaxCode)
		if m.PMSInterfaceID == "" || seen[m.PMSInterfaceID] {
			return "invalid_room_charge_mapping: one mapping per PMS interface"
		}
		seen[m.PMSInterfaceID] = true
		if l := len(m.PostingCode); l < 1 || l > 20 || strings.Contains(m.PostingCode, "|") || hasControl(m.PostingCode) {
			return "invalid_room_charge_mapping: the posting code must be 1 to 20 printable characters without |"
		}
		if len(m.TaxCode) > 20 || strings.Contains(m.TaxCode, "|") || hasControl(m.TaxCode) {
			return "invalid_room_charge_mapping: the tax code must be at most 20 printable characters without |"
		}
		if m.TaxRateBP != nil && (*m.TaxRateBP < 0 || *m.TaxRateBP > 10000) {
			return "invalid_room_charge_mapping: the tax rate must be between 0 and 100%"
		}
	}
	return ""
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
