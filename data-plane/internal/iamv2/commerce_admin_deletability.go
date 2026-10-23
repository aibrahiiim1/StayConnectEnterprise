package iamv2

import (
	"context"
	"errors"
	"fmt"
)

// ErrCodeExists is the refusal an "Add" publication gets when the code already belongs to a package or plan
// at this site; Add must never silently revise the owner.
var ErrCodeExists = errors.New("iamv2: code already exists at this site")

// ErrCommerceNotFound also covers system objects (the hidden grace catalogue), which are not the operator's to inspect or remove.
var ErrCommerceNotFound = errors.New("iamv2: not found")

var ErrCatalogueInUse = errors.New("iamv2: still in use")

var ErrCatalogueDeleteNeedsReason = errors.New("iamv2: deletion needs a reason")

// ErrCatalogueDeleteUnavailable: the database predates the deletion functions (migration 0091); deletion is refused, never approximated.
var ErrCatalogueDeleteUnavailable = errors.New("iamv2: deletion is not available on this database")

// DeletabilityReason is one blocker in operator words. Count is nil when the reason is not a count; counts
// come from the database's own deletion_blockers functions, never a guess.
type DeletabilityReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   *int64 `json:"count,omitempty"`
}

// Deletability: an item may be permanently deleted only when nothing has ever used it. The database counts
// every reference, including ones this service cannot read, and refuses deletion on the same list.
type Deletability struct {
	Deletable bool                 `json:"deletable"`
	Reasons   []DeletabilityReason `json:"reasons"`
}

type CatalogueBlocker struct {
	Code string
	N    int64
}

// ReasonDeleteUnavailable is the only reason when the database cannot answer.
const ReasonDeleteUnavailable = "DELETE_UNAVAILABLE"

const deleteUnavailableMsg = "This appliance's database does not support permanent deletion yet. " +
	"Disable it instead — it stops being offered and keeps its history."

func (a *CommerceAdmin) PackageDeletability(ctx context.Context, tenantID, siteID, packageID string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	d, err := a.deletability(ctx, tenantID, siteID, packageID, true)
	return d, false, err
}

func (a *CommerceAdmin) PlanDeletability(ctx context.Context, tenantID, siteID, planID string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	d, err := a.deletability(ctx, tenantID, siteID, planID, false)
	return d, false, err
}

func (a *CommerceAdmin) deletability(ctx context.Context, tenantID, siteID, id string, pkg bool) (Deletability, error) {
	var (
		rows []CatalogueBlocker
		err  error
	)
	if pkg {
		rows, err = a.repo.PackageBlockers(ctx, tenantID, siteID, id)
	} else {
		rows, err = a.repo.PlanBlockers(ctx, tenantID, siteID, id)
	}
	if errors.Is(err, ErrCatalogueDeleteUnavailable) {
		return Deletability{Deletable: false, Reasons: []DeletabilityReason{
			{Code: ReasonDeleteUnavailable, Message: deleteUnavailableMsg}}}, nil
	}
	if err != nil {
		return Deletability{}, &Error{Code: ErrRepo, Msg: "deletion blockers"}
	}
	return blockersToDeletability(rows, pkg)
}

// DeletePackage: the database re-checks every blocker under a row lock; on refusal the Deletability is re-read.
func (a *CommerceAdmin) DeletePackage(ctx context.Context, tenantID, siteID, packageID, operatorID, reason string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	return a.deleteUnused(ctx, tenantID, siteID, packageID, operatorID, reason, true)
}

func (a *CommerceAdmin) DeletePlan(ctx context.Context, tenantID, siteID, planID, operatorID, reason string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	return a.deleteUnused(ctx, tenantID, siteID, planID, operatorID, reason, false)
}

func (a *CommerceAdmin) deleteUnused(ctx context.Context, tenantID, siteID, id, operatorID, reason string, pkg bool) (Deletability, bool, error) {
	var err error
	if pkg {
		err = a.repo.DeleteUnusedPackage(ctx, tenantID, siteID, id, operatorID, reason)
	} else {
		err = a.repo.DeleteUnusedPlan(ctx, tenantID, siteID, id, operatorID, reason)
	}
	switch {
	case err == nil:
		return Deletability{Deletable: true, Reasons: []DeletabilityReason{}}, false, nil
	case errors.Is(err, ErrCatalogueInUse):
		d, derr := a.deletability(ctx, tenantID, siteID, id, pkg)
		if derr != nil {
			return Deletability{}, false, ErrCatalogueInUse
		}
		if d.Deletable {
			// The lock saw a reference the explanation no longer sees; still a refusal, not an empty list.
			d = Deletability{Deletable: false, Reasons: []DeletabilityReason{{Code: "CHANGED",
				Message: "Something referenced this item while it was being deleted. Try again."}}}
		}
		return d, false, ErrCatalogueInUse
	case errors.Is(err, ErrCommerceNotFound), errors.Is(err, ErrCatalogueDeleteNeedsReason),
		errors.Is(err, ErrCatalogueDeleteUnavailable):
		return Deletability{}, false, err
	default:
		return Deletability{}, false, &Error{Code: ErrRepo, Msg: "delete"}
	}
}

// blockersToDeletability: NOT_FOUND and SYSTEM_* answer not-found, since a system object is not the operator's to inspect.
func blockersToDeletability(rows []CatalogueBlocker, pkg bool) (Deletability, error) {
	out := Deletability{Deletable: true, Reasons: []DeletabilityReason{}}
	for _, b := range rows {
		switch b.Code {
		case "NOT_FOUND", "SYSTEM_PACKAGE", "SYSTEM_PLAN":
			return Deletability{}, ErrCommerceNotFound
		}
		if b.N <= 0 {
			continue
		}
		out.Reasons = append(out.Reasons, countReason(b.Code, b.N, blockerMessage(b.Code, b.N, pkg)))
	}
	out.Deletable = len(out.Reasons) == 0
	return out, nil
}

func countReason(code string, n int64, msg string) DeletabilityReason {
	c := n
	return DeletabilityReason{Code: code, Message: msg, Count: &c}
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// blockerMessage: an unknown code (a newer database) still refuses, in general words.
func blockerMessage(code string, n int64, pkg bool) string {
	item := "plan"
	if pkg {
		item = "package"
	}
	switch code {
	case "ENTITLEMENTS":
		return fmt.Sprintf("%d internet %s given to guests %s this %s.",
			n, plural(n, "grant", "grants"), plural(n, "records", "record"), item)
	case "PURCHASES":
		return fmt.Sprintf("%d %s on record for this package.", n, plural(n, "purchase is", "purchases are"))
	case "OFFER_QUOTES":
		return fmt.Sprintf("%d %s shown to guests on the portal %s this package.",
			n, plural(n, "offer", "offers"), plural(n, "names", "name"))
	case "PORTAL_OFFERS":
		return fmt.Sprintf("%d portal sign-in %s offered this package.", n, plural(n, "has", "have"))
	case "VOUCHERS":
		return fmt.Sprintf("%d %s issued for this package.", n, plural(n, "voucher was", "vouchers were"))
	case "VOUCHER_BATCHES":
		return fmt.Sprintf("%d voucher %s made for this package.", n, plural(n, "batch was", "batches were"))
	case "SETTLEMENT_MAPPINGS":
		return fmt.Sprintf("%d billing %s this package.", n, plural(n, "mapping names", "mappings name"))
	case "CHECKOUT_GRACE_CONFIG":
		return "The after-check-out grace setting points at this package."
	case "CHECKOUT_GRACE_HISTORY":
		return fmt.Sprintf("%d published after-check-out grace %s this package.",
			n, plural(n, "policy names", "policies name"))
	case "GUEST_ACCOUNTS":
		return fmt.Sprintf("%d guest %s assigned this package.", n, plural(n, "account is", "accounts are"))
	case "PACKAGE_REVISIONS":
		return fmt.Sprintf("%d package %s built on this plan, including earlier saved versions.",
			n, plural(n, "version is", "versions are"))
	}
	return fmt.Sprintf("%d %s still depend on this %s (%s).", n, plural(n, "record", "records"), item, code)
}
