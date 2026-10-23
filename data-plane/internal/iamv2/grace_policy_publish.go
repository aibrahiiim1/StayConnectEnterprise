package iamv2

// The system grace package is derived from Hotel-Admin policy at publication, not created up front: the
// conversion judges it with iam_v2.grace_package_mismatch_reason, which demands exact equality between the
// typed site policy and the pinned revision (end_mode GRACE_AFTER_CHECKOUT, duration, policy_version
// CHECKOUT_GRACE_V1, plan scalars, VALIDITY_WINDOW, active is_system package, price 0, settlement
// {NOT_REQUIRED}, no reserved Emergency codes).
// Publication uses iam_v2.publish_checkout_grace_policy, not the raw config writer: it requires an active
// operator actor and a reason code, enforces optimistic versioning, validates with the same matcher and
// appends the audit row.

import (
	"context"
	"encoding/json"
	"fmt"
)

// Every field is required: grace_all_or_none and the exact matcher both refuse a partial policy.
type SystemGracePolicy struct {
	DurationSeconds          int
	DownKbps                 int
	UpKbps                   int
	DataQuotaBytes           int64
	DeviceLimit              int
	DeviceLimitPolicy        string
	EligibilityWindowSeconds int
}

// Reserved code for the derived system grace plan; distinct from the package code and from the Emergency
// catalog codes, which the matcher refuses.
const systemGracePlanCode = "__system_checkout_grace_plan"

type GracePublishRequest struct {
	TenantID, SiteID string
	Policy           SystemGracePolicy
	// Must be an active operator of this tenant: the boundary refuses a policy nobody can be held to.
	ActorOperatorID string
	// A bounded machine code (^[A-Z][A-Z0-9_]{0,63}$), not free text.
	ReasonCode string
	// The config_version the caller last read; 0 means nothing is published.
	ExpectedVersion int
}

type GracePublishTx interface {
	EnsureSystemGracePlanRevision(ctx context.Context, tenantID, siteID string, p SystemGracePolicy) (planRevisionID string, err error)
	EnsureSystemGracePackageRevision(ctx context.Context, tenantID, siteID, planRevisionID string, p SystemGracePolicy) (packageRevisionID string, err error)
	PublishGracePolicy(ctx context.Context, req GracePublishRequest, packageRevisionID string) (newVersion int, err error)
	GraceMismatchReason(ctx context.Context, tenantID, siteID, packageRevisionID string, p SystemGracePolicy) (string, error)
}

type GracePublishRepository interface {
	WithGracePublishTx(ctx context.Context, fn func(GracePublishTx) error) error
}

// Runs in one transaction so a refused publication leaves no half-built plan or package revision. Returns the
// published package revision as well as the version: the site config is shared state that a concurrent
// publication can move, so re-reading it could report somebody else's policy.
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
		// Ask the same matcher the conversion uses before publishing, so a wrong derivation yields a specific
		// reason instead of a generic publication failure.
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
	// Mirrors the database bounds so an impossible policy is refused with a reason naming the field, not an
	// opaque CHECK violation.
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
		// The only policy the enforcement path implements; refused rather than approximated.
		return &Error{Code: ErrInvalidInput, Msg: "grace device_limit_policy must be REJECT_NEW_DEVICE"}
	case p.EligibilityWindowSeconds <= 0 || p.EligibilityWindowSeconds > 604800:
		return &Error{Code: ErrInvalidInput, Msg: "eligibility_window_seconds must be within 1..604800"}
	}
	return nil
}

// A package omitting policy_version is refused: no declared version is not the approved one.
func graceDurationPolicyJSON(p SystemGracePolicy) string {
	b, _ := json.Marshal(map[string]any{
		"end_mode":               "GRACE_AFTER_CHECKOUT",
		"grace_duration_seconds": p.DurationSeconds,
		"policy_version":         "CHECKOUT_GRACE_V1",
	})
	return string(b)
}

var _ = fmt.Sprintf
