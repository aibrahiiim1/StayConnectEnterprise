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
