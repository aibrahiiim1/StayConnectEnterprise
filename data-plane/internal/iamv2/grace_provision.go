package iamv2

// The normal checkout-grace package is system-provisioned, one hidden package per Site; Hotel Admin controls
// policy only, never the package's existence, type, price, settlement or provenance.
// Provenance (is_system = true) is a property of how the row is made, not a flag an operator can pass, so the
// operator publisher stays closed.
// Hidden means not an operator catalogue entry; guest eligibility is still decided by the commerce engine at
// checkout.
// Emergency Grace is the separate fail-safe for a missing or corrupt package; provisioning never weakens it.
// Provisioning runs at startup and must converge: an existing system package keeps its immutable revision
// history.

import (
	"context"
	"encoding/json"
	"fmt"
)

type GraceProvisionResult struct {
	PackageID   string
	RevisionID  string
	Created     bool   // the package row was created by this call
	RevisionNew bool   // a first revision was published by this call
	Skipped     string // non-empty when provisioning deliberately did nothing
}

type GraceProvisioner struct {
	repo CommerceAdminRepository
}

// repo may be nil while commerce is dark, in which case EnsureSiteGracePackage does nothing.
func NewGraceProvisioner(repo CommerceAdminRepository) *GraceProvisioner {
	return &GraceProvisioner{repo: repo}
}

// Deliberately narrower than CommerceAdminTx: it may create a system package and its first revision, and
// cannot touch the grace config, because policy is the operator's.
type GraceProvisionTx interface {
	SystemGracePackage(ctx context.Context, tenantID, siteID string) (packageID, revisionID string, err error)
	CreateSystemGracePackage(ctx context.Context, tenantID, siteID, code string) (packageID string, err error)
	InsertSystemGraceRevision(ctx context.Context, tenantID, siteID, packageID, planRevisionID string, revNo int, display map[string]any) (revisionID string, err error)
	SetCurrentRevision(ctx context.Context, packageID, revisionID string) error
	DefaultPlanRevisionForGrace(ctx context.Context, tenantID, siteID string) (planRevisionID string, err error)
}

type GraceProvisionRepository interface {
	WithGraceProvisionTx(ctx context.Context, fn func(GraceProvisionTx) error) error
}

// Reserved, not configurable: an operator-chosen code would make the package findable and editable by code.
const systemGraceCode = "__system_checkout_grace"

// Does not set grace policy: a provisioned package with no policy is the correct intermediate state, and a
// default would be the system deciding a commercial question.
func (g *GraceProvisioner) EnsureSiteGracePackage(ctx context.Context, repo GraceProvisionRepository, tenantID, siteID string) (GraceProvisionResult, error) {
	if repo == nil {
		return GraceProvisionResult{Skipped: "commerce repository unavailable (dark)"}, nil
	}
	var res GraceProvisionResult
	err := repo.WithGraceProvisionTx(ctx, func(tx GraceProvisionTx) error {
		pkgID, revID, err := tx.SystemGracePackage(ctx, tenantID, siteID)
		if err != nil {
			return err
		}
		if pkgID != "" && revID != "" {
			// Already converged; the revision is immutable and may be pinned by live entitlements, so it is
			// kept.
			res = GraceProvisionResult{PackageID: pkgID, RevisionID: revID}
			return nil
		}
		if pkgID == "" {
			if pkgID, err = tx.CreateSystemGracePackage(ctx, tenantID, siteID, systemGraceCode); err != nil {
				return err
			}
			res.Created = true
		}
		// A grace revision must pin an enabled plan revision (re-validated at every checkout); without one,
		// provisioning stops rather than publishing one that fails on first use.
		planRev, err := tx.DefaultPlanRevisionForGrace(ctx, tenantID, siteID)
		if err != nil {
			return err
		}
		if planRev == "" {
			res.PackageID = pkgID
			res.Skipped = "no enabled service-plan revision to pin; publish a service plan first"
			return nil
		}
		display := map[string]any{
			"name":   "Checkout grace",
			"system": true,
			"note":   "System-provisioned per-site checkout grace (D32). Policy is set in the Admin Console.",
		}
		revID, err = tx.InsertSystemGraceRevision(ctx, tenantID, siteID, pkgID, planRev, 1, display)
		if err != nil {
			return err
		}
		if err := tx.SetCurrentRevision(ctx, pkgID, revID); err != nil {
			return err
		}
		res.PackageID, res.RevisionID, res.RevisionNew = pkgID, revID, true
		return nil
	})
	if err != nil {
		return GraceProvisionResult{}, fmt.Errorf("grace provisioning: %w", err)
	}
	return res, nil
}

func graceDisplayJSON(display map[string]any) []byte {
	b, _ := json.Marshal(orEmptyObj(display))
	return b
}
