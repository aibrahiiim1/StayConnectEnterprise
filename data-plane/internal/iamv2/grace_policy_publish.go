package iamv2

// PRODUCING THE SYSTEM GRACE PACKAGE *FROM* HOTEL-ADMIN POLICY (D32).
//
// WHY THE EARLIER SHAPE COULD NOT WORK
// ------------------------------------
// Provisioning first created a grace package at startup pinned to "the site's first enabled plan", with a
// MANUAL_END duration policy. That package can never be adopted, because the checkout conversion judges the
// configuration with iam_v2.grace_package_mismatch_reason, which demands EXACT equality between the typed
// site policy and the pinned revision:
//
//   end_mode = GRACE_AFTER_CHECKOUT, grace_duration_seconds = the published duration,
//   policy_version = CHECKOUT_GRACE_V1 (required, string), and the pinned PLAN revision carrying exactly the
//   published down/up/data-quota/device-limit/device-policy with time_accounting_mode = VALIDITY_WINDOW,
//   on an ACTIVE, is_system, CHECKOUT_GRACE package whose CURRENT revision it is, price 0,
//   settlement exactly {NOT_REQUIRED}, and neither package nor plan drawn from the reserved Emergency catalog.
//
// An arbitrary plan satisfies almost none of that. So the package is not something to create up front and
// hope matches -- it is DERIVED from the policy at publication time. The operator supplies policy; the system
// materialises the plan revision and package revision that exactly express it.
//
// WHY THE AUDITED BOUNDARY, NOT THE RAW ONE
// -----------------------------------------
// Publication goes through iam_v2.publish_checkout_grace_policy, not publish_checkout_grace_config. The
// former is the canonical boundary: it opens the grace_publication controlled operation, requires an ACTIVE
// OPERATOR as actor and a bounded machine reason code, enforces optimistic concurrency against the version
// the caller last read, validates the package graph with THE SAME matcher the conversion uses, and appends to
// iam_v2.checkout_grace_policy_publications. The raw config writer skips the actor, the reason, the version
// check and the audit row -- everything that makes the change attributable and safe under two operators.

import (
	"context"
	"encoding/json"
	"fmt"
)

// SystemGracePolicy is the typed policy an operator publishes. Every field is required: grace_all_or_none in
// the schema and the exact matcher both refuse a partial policy, so there is no meaningful half-specified form.
type SystemGracePolicy struct {
	DurationSeconds          int
	DownKbps                 int
	UpKbps                   int
	DataQuotaBytes           int64
	DeviceLimit              int
	DeviceLimitPolicy        string
	EligibilityWindowSeconds int
}

// systemGracePlanCode is the reserved code for the derived system grace SERVICE PLAN. Distinct from the
// package code, and distinct from the Emergency catalog's reserved codes, which the matcher refuses outright.
const systemGracePlanCode = "__system_checkout_grace_plan"

// GracePublishRequest carries the policy plus the governance inputs the audited boundary requires.
type GracePublishRequest struct {
	TenantID, SiteID string
	Policy           SystemGracePolicy
	// ActorOperatorID must be an active operator of this tenant: the boundary refuses a policy nobody can be
	// held to.
	ActorOperatorID string
	// ReasonCode is a bounded machine code (^[A-Z][A-Z0-9_]{0,63}$), not free text.
	ReasonCode string
	// ExpectedVersion is the config_version the caller last read; 0 means "I believe nothing is published".
	ExpectedVersion int
}

// GracePublishTx is the transactional surface policy-driven publication needs.
type GracePublishTx interface {
	EnsureSystemGracePlanRevision(ctx context.Context, tenantID, siteID string, p SystemGracePolicy) (planRevisionID string, err error)
	EnsureSystemGracePackageRevision(ctx context.Context, tenantID, siteID, planRevisionID string, p SystemGracePolicy) (packageRevisionID string, err error)
	PublishGracePolicy(ctx context.Context, req GracePublishRequest, packageRevisionID string) (newVersion int, err error)
	GraceMismatchReason(ctx context.Context, tenantID, siteID, packageRevisionID string, p SystemGracePolicy) (string, error)
}

// GracePublishRepository is the repository half.
type GracePublishRepository interface {
	WithGracePublishTx(ctx context.Context, fn func(GracePublishTx) error) error
}

// PublishSystemGracePolicy derives the system plan and package revisions from the policy and publishes it.
//
// One transaction end to end: if publication is refused, no half-built plan or package revision is left
// behind claiming to be a grace catalogue nobody validated.
// Returns the new config version AND the package revision that was published. Returning the revision is not
// a convenience: the site grace config is shared state that any concurrent publication can move, so a caller
// that re-read it to learn "what did I just publish" could be told about somebody else's policy.
func PublishSystemGracePolicy(ctx context.Context, repo GracePublishRepository, req GracePublishRequest) (int, string, error) {
	if repo == nil {
		return 0, "", &Error{Code: ErrConfig, Msg: "grace publication: no repository"}
	}
	if err := validateSystemGracePolicy(req.Policy); err != nil {
		return 0, "", err
	}
	var version int
	var publishedRev string
	err := repo.WithGracePublishTx(ctx, func(tx GracePublishTx) error {
		planRev, err := tx.EnsureSystemGracePlanRevision(ctx, req.TenantID, req.SiteID, req.Policy)
		if err != nil {
			return err
		}
		pkgRev, err := tx.EnsureSystemGracePackageRevision(ctx, req.TenantID, req.SiteID, planRev, req.Policy)
		if err != nil {
			return err
		}
		// Ask the SAME matcher the conversion uses, before publishing. The boundary checks it too, but asking
		// here turns a generic publication failure into the specific reason the derivation was wrong -- which
		// is the difference between a fixable bug report and "grace stopped working".
		if reason, rerr := tx.GraceMismatchReason(ctx, req.TenantID, req.SiteID, pkgRev, req.Policy); rerr != nil {
			return rerr
		} else if reason != "" {
			return &Error{Code: ErrInvalidInput,
				Msg: "derived grace package does not satisfy the checkout validator: " + reason}
		}
		version, err = tx.PublishGracePolicy(ctx, req, pkgRev)
		publishedRev = pkgRev
		return err
	})
	if err != nil {
		return 0, "", err
	}
	return version, publishedRev, nil
}

func validateSystemGracePolicy(p SystemGracePolicy) error {
	// Mirrors the database's own bounds so an obviously impossible policy is refused with a reason naming the
	// field, rather than as an opaque CHECK violation from inside a function three layers down.
	switch {
	case p.DurationSeconds <= 0 || p.DurationSeconds > 604800:
		return &Error{Code: ErrInvalidInput, Msg: "grace duration must be within 1..604800 seconds"}
	case p.DownKbps <= 0 || p.DownKbps > 10000000:
		return &Error{Code: ErrInvalidInput, Msg: "grace down_kbps out of range"}
	case p.UpKbps <= 0 || p.UpKbps > 10000000:
		return &Error{Code: ErrInvalidInput, Msg: "grace up_kbps out of range"}
	case p.DataQuotaBytes <= 0 || p.DataQuotaBytes > 1099511627776:
		return &Error{Code: ErrInvalidInput, Msg: "grace data_quota_bytes out of range"}
	case p.DeviceLimit <= 0 || p.DeviceLimit > 1000:
		return &Error{Code: ErrInvalidInput, Msg: "grace device_limit out of range"}
	case p.DeviceLimitPolicy != "REJECT_NEW_DEVICE":
		// The only policy the enforcement path implements. Refused rather than accepted-and-approximated.
		return &Error{Code: ErrInvalidInput, Msg: "grace device_limit_policy must be REJECT_NEW_DEVICE"}
	case p.EligibilityWindowSeconds <= 0 || p.EligibilityWindowSeconds > 604800:
		return &Error{Code: ErrInvalidInput, Msg: "eligibility_window_seconds must be within 1..604800"}
	}
	return nil
}

// graceDurationPolicyJSON builds the duration policy the exact matcher requires. The policy version is not
// optional-if-present: a package omitting it is refused, because "no declared version" is not the approved one.
func graceDurationPolicyJSON(p SystemGracePolicy) string {
	b, _ := json.Marshal(map[string]any{
		"end_mode":               "GRACE_AFTER_CHECKOUT",
		"grace_duration_seconds": p.DurationSeconds,
		"policy_version":         "CHECKOUT_GRACE_V1",
	})
	return string(b)
}

var _ = fmt.Sprintf
