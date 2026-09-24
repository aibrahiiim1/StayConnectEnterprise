package iamv2

import (
	"context"
	"errors"
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

// DeletabilityReason is one thing that stands between an item and its removal, in the words an operator reads.
//
// Count is nil when the reason is not a count (the schema-change reason). It is never a guess: a reference this
// service cannot read is left out rather than reported as zero.
type DeletabilityReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   *int64 `json:"count,omitempty"`
}

// Deletability is the answer to "can I delete this?".
//
// Until the Product Owner decides how a package's records may be removed, the answer is always no: package and
// plan revisions are protected by immutable triggers, the runtime role holds no DELETE on them, and eight
// foreign keys reference a package revision with no ON DELETE. The reasons say what is attached so the operator
// understands why, and the final reason says what to do instead.
type Deletability struct {
	Deletable bool                 `json:"deletable"`
	Reasons   []DeletabilityReason `json:"reasons"`
}

// Reason code that is always last, and always present, until a removal path is approved.
const ReasonDeleteRequiresSchemaChange = "DELETE_REQUIRES_SCHEMA_CHANGE"

const packageDeleteRequiresSchemaChangeMsg = "Removing a package's records needs a database change that has not " +
	"been approved yet. Disable it instead — a disabled package is no longer offered to guests and keeps its history."

const planDeleteRequiresSchemaChangeMsg = "Removing a service plan's records needs a database change that has not " +
	"been approved yet. A plan stops reaching guests when no active package uses it — disable or edit those " +
	"packages instead. The plan keeps its history."

// PackageDeletability reports what still references a package and why it cannot be deleted.
func (a *CommerceAdmin) PackageDeletability(ctx context.Context, tenantID, siteID, packageID string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	reasons, found, err := a.repo.PackageReferences(ctx, tenantID, siteID, packageID)
	if err != nil {
		return Deletability{}, false, &Error{Code: ErrRepo, Msg: "package references"}
	}
	if !found {
		return Deletability{}, false, ErrCommerceNotFound
	}
	return finishDeletability(reasons, packageDeleteRequiresSchemaChangeMsg), false, nil
}

// PlanDeletability reports what still references a service plan and why it cannot be deleted.
func (a *CommerceAdmin) PlanDeletability(ctx context.Context, tenantID, siteID, planID string) (Deletability, bool, error) {
	if !a.cfg.AdminOn() {
		return Deletability{}, true, nil
	}
	reasons, found, err := a.repo.PlanReferences(ctx, tenantID, siteID, planID)
	if err != nil {
		return Deletability{}, false, &Error{Code: ErrRepo, Msg: "plan references"}
	}
	if !found {
		return Deletability{}, false, ErrCommerceNotFound
	}
	return finishDeletability(reasons, planDeleteRequiresSchemaChangeMsg), false, nil
}

// finishDeletability drops references that count nothing and appends the schema-change reason, so deletable is
// false by construction rather than by every caller remembering to set it.
func finishDeletability(in []DeletabilityReason, schemaMsg string) Deletability {
	out := Deletability{Deletable: false, Reasons: []DeletabilityReason{}}
	for _, r := range in {
		if r.Count != nil && *r.Count == 0 {
			continue
		}
		out.Reasons = append(out.Reasons, r)
	}
	out.Reasons = append(out.Reasons, DeletabilityReason{Code: ReasonDeleteRequiresSchemaChange, Message: schemaMsg})
	return out
}
