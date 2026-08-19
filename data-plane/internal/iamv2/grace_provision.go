package iamv2

// SYSTEM PROVISIONING OF THE NORMAL CHECKOUT-GRACE PACKAGE (D32/T0069).
//
// THE DECISION THIS IMPLEMENTS
// ----------------------------
// The normal grace package is provisioned BY THE SYSTEM, one per Site, and is hidden from the operator
// catalogue. Hotel Admin controls POLICY ONLY -- which revision is current for the site and the typed grace
// fields -- and never the existence, type, price, settlement or provenance of the package itself.
//
// That is why this lives here rather than being reached by opening package_type on the operator publisher.
// A package an operator can create is not a system package: the contract requires is_system = true and
// re-validates it at save AND at every checkout, so provenance has to be a property of HOW the row is made,
// not a flag an operator can pass. The publisher stays closed.
//
// WHAT "HIDDEN" MEANS CONCRETELY
// ------------------------------
// The package is excluded from the operator-facing catalogue listing and cannot be published, edited or
// deactivated through the operator surface. It is not hidden from the GUEST by these means -- guest
// eligibility is decided by the commerce engine at checkout, not by catalogue visibility -- and it is not
// secret from the operator, who still sets its policy. Hidden here means "not an operator catalogue entry".
//
// EMERGENCY GRACE IS NOT THIS
// ---------------------------
// Emergency Grace is the separate fail-safe for the case where this package is missing or corrupt AT
// checkout. Provisioning normal grace does not remove, replace or weaken it; the two exist precisely so that
// a failure of the normal path still degrades to something rather than to nothing.
//
// IDEMPOTENCE
// -----------
// Provisioning runs at startup and must converge, not accumulate. A site that already has a system grace
// package keeps it -- including its revision history, which is immutable and which live entitlements pin.

import (
	"context"
	"encoding/json"
	"fmt"
)

// GraceProvisionResult reports what provisioning observed or did. No secret material, no guest data.
type GraceProvisionResult struct {
	PackageID   string
	RevisionID  string
	Created     bool   // the package row was created by this call
	RevisionNew bool   // a first revision was published by this call
	Skipped     string // non-empty when provisioning deliberately did nothing
}

// GraceProvisioner creates the hidden per-site system grace package if it is absent.
type GraceProvisioner struct {
	repo CommerceAdminRepository
}

// NewGraceProvisioner builds the provisioner. repo may be nil while Phase-2 commerce is dark, in which case
// EnsureSiteGracePackage does nothing at all -- the same fail-closed shape the rest of the domain uses.
func NewGraceProvisioner(repo CommerceAdminRepository) *GraceProvisioner {
	return &GraceProvisioner{repo: repo}
}

// GraceProvisionTx is the transactional surface provisioning needs. It is deliberately NARROWER than
// CommerceAdminTx: provisioning may create a system package and its first revision, and may do nothing else.
// In particular it cannot touch the grace CONFIG, because policy is the operator's and provisioning must not
// silently choose one.
type GraceProvisionTx interface {
	SystemGracePackage(ctx context.Context, tenantID, siteID string) (packageID, revisionID string, err error)
	CreateSystemGracePackage(ctx context.Context, tenantID, siteID, code string) (packageID string, err error)
	InsertSystemGraceRevision(ctx context.Context, tenantID, siteID, packageID, planRevisionID string, revNo int, display map[string]any) (revisionID string, err error)
	SetCurrentRevision(ctx context.Context, packageID, revisionID string) error
	DefaultPlanRevisionForGrace(ctx context.Context, tenantID, siteID string) (planRevisionID string, err error)
}

// GraceProvisionRepository is the repository half.
type GraceProvisionRepository interface {
	WithGraceProvisionTx(ctx context.Context, fn func(GraceProvisionTx) error) error
}

// systemGraceCode is the reserved code for the per-site system grace package. Reserved, not configurable:
// an operator-chosen code would make the package findable and therefore editable by code, which is exactly
// the operator ownership D32 excludes.
const systemGraceCode = "__system_checkout_grace"

// EnsureSiteGracePackage converges the site on exactly one hidden system grace package.
//
// It does NOT set grace policy. A site with a provisioned package and no policy is the correct intermediate
// state: the package exists so the operator has something to point policy at, and until they set policy
// there is deliberately no grace configured. Choosing a default policy here would be the system deciding a
// commercial question that D32 assigns to Hotel Admin.
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
			// Already converged. The existing revision is immutable and may be pinned by live entitlements,
			// so it is kept rather than superseded on every boot.
			res = GraceProvisionResult{PackageID: pkgID, RevisionID: revID}
			return nil
		}
		if pkgID == "" {
			if pkgID, err = tx.CreateSystemGracePackage(ctx, tenantID, siteID, systemGraceCode); err != nil {
				return err
			}
			res.Created = true
		}
		// A grace revision must pin an ENABLED plan revision -- that is part of what the contract re-validates
		// at every checkout. Without one there is nothing coherent to publish, so provisioning stops and says
		// so rather than publishing a revision that would fail validation the moment it was used.
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
			"note":   "System-provisioned per-site checkout grace (D32). Policy is set in Hotel Admin.",
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

// graceDisplayJSON is shared by the repository implementation.
func graceDisplayJSON(display map[string]any) []byte {
	b, _ := json.Marshal(orEmptyObj(display))
	return b
}
