package iamv2

import (
	"context"
	"errors"
	"fmt"
)

// ErrCodeExists is the refusal an "Add" publication gets when the code it asked for already belongs to a
// package (or plan) at this site.
//
// It exists because Add used to be an upsert: typing an existing code into "Add package" quietly published a
// new revision of the package that owned it -- replacing its name, conditions and plan for every future guest --
// and reported success. Revising an existing item is what Edit is for; Add now says the code is taken.
var ErrCodeExists = errors.New("iamv2: code already exists at this site")

// ErrCommerceNotFound means no operator-visible package or plan has that id at this site. System objects (the hidden
// grace catalogue) answer the same way, because they are not the operator's to inspect or remove.
var ErrCommerceNotFound = errors.New("iamv2: not found")

// ErrCatalogueInUse is the database refusing a deletion because something still depends on the item. The
// Deletability returned beside it says what.
var ErrCatalogueInUse = errors.New("iamv2: still in use")

// ErrCatalogueDeleteNeedsReason is the database refusing a deletion with no bounded reason.
var ErrCatalogueDeleteNeedsReason = errors.New("iamv2: deletion needs a reason")

// ErrCatalogueDeleteUnavailable means this appliance's database predates migration 0091, so the deletion
// functions do not exist yet. Deletion is refused, never approximated.
var ErrCatalogueDeleteUnavailable = errors.New("iamv2: deletion is not available on this database")

// DeletabilityReason is one thing that stands between an item and its removal, in the words an operator reads.
//
// Count is nil when the reason is not a count. It is never a guess: every count comes from the database's own
// iam_v2.*_deletion_blockers function, the same one the deletion itself refuses on.
type DeletabilityReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   *int64 `json:"count,omitempty"`
}

// Deletability is the answer to "can I delete this?".
//
// A package or plan may be permanently deleted only when nothing has ever used it (migration 0091). The
// answer comes from the database, which counts every reference -- including ones this service cannot read
// itself -- and refuses the deletion on exactly the same list. When the answer is no, the reasons say what is
// attached and Disable remains the way to stop offering it.
type Deletability struct {
	Deletable bool                 `json:"deletable"`
	Reasons   []DeletabilityReason `json:"reasons"`
}

// CatalogueBlocker is one row of iam_v2.internet_package_deletion_blockers / service_plan_deletion_blockers.
type CatalogueBlocker struct {
	Code string
	N    int64
}

// ReasonDeleteUnavailable is the only reason when the database cannot answer (pre-0091).
const ReasonDeleteUnavailable = "DELETE_UNAVAILABLE"

const deleteUnavailableMsg = "This appliance's database does not support permanent deletion yet. " +
	"Disable it instead — it stops being offered and keeps its history."

// PackageDeletability reports whether a package may be permanently deleted, and if not, why.
func (a *CommerceAdmin) PackageDeletability(ctx context.Context, tenantID, siteID, packageID string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	d, err := a.deletability(ctx, tenantID, siteID, packageID, true)
	return d, false, err
}

// PlanDeletability reports whether a service plan may be permanently deleted, and if not, why.
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

// DeletePackage permanently deletes a package that has never been used. The database re-checks every
// blocker under a row lock and refuses on any of them; on that refusal the returned Deletability is re-read so
// the operator sees what appeared.
func (a *CommerceAdmin) DeletePackage(ctx context.Context, tenantID, siteID, packageID, operatorID, reason string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	return a.deleteUnused(ctx, tenantID, siteID, packageID, operatorID, reason, true)
}

// DeletePlan permanently deletes a service plan that no package revision and no grant has ever used.
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
			// The lock saw a reference the explanation no longer sees (it was removed in between). Still a
			// refusal: say so rather than show an empty list.
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

// blockersToDeletability turns the database's rows into the operator's answer. Pure, so the wording is tested.
// NOT_FOUND and SYSTEM_* answer not-found: a system object is not the operator's to inspect or remove.
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

// blockerMessage is the sentence for one blocker code. An unknown code (a newer database) still refuses, in
// general words, because the database refused.
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
