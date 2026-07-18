package iamv2

import (
	"context"
	"errors"
	"time"
)

// PackageListRequest is the guest's package-discovery input. Only server-resolved trusted identifiers;
// the browser never supplies any of these directly (portald derives them from the trusted session).
type PackageListRequest struct {
	TenantID       string
	SiteID         string
	AuthContextID  string
	DeviceID       string
	GuestNetworkID string
}

// PackageListItem is the guest-safe view of one eligible free package. It carries ONLY an opaque package
// id and guest-appropriate display — never pins, prices internals, revisions or ineligible-package data.
type PackageListItem struct {
	PackageID string         `json:"package_id"`
	Display   map[string]any `json:"display"`
}

// PackageListResult is the result of ListEligiblePackages.
type PackageListResult struct {
	Disabled bool
	Packages []PackageListItem
	Reason   string
}

// ListEligiblePackages returns — read-only, in one transaction, WITHOUT creating any quote/purchase/
// entitlement — the free packages the authenticated subject is eligible for right now. It enforces the
// identical gates as CreateQuote (auth-context pins, non-PMS, free-only + NOT_REQUIRED, authoritative
// currency, visibility window, typed eligibility, a resolvable first-match grant tier and pinned plan
// revision) but SILENTLY EXCLUDES any package that fails a gate so no ineligible package's existence is
// revealed. When the portal surface is OFF it returns Disabled without touching the repository.
func (e *CommerceEngine) ListEligiblePackages(ctx context.Context, req PackageListRequest) (PackageListResult, error) {
	if !e.cfg.PortalOn() {
		e.obs.Event("phase2.disabled", map[string]string{"op": "list"})
		return PackageListResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if req.TenantID == "" || req.SiteID == "" || req.AuthContextID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		return PackageListResult{}, &Error{Code: ErrInvalidInput, Msg: "list: missing tenant/site/auth_context/device/guest_network"}
	}
	now := e.now()
	var res PackageListResult
	err := e.repo.WithTx(ctx, func(tx CommerceTx) error {
		ac, err := tx.LoadAuthContext(ctx, req.TenantID, req.SiteID, req.AuthContextID)
		if err != nil {
			return err
		}
		// Auth-context pins (no consume): tenant/site + THIS device+network, non-PMS, unconsumed, unexpired.
		if ac.TenantID != req.TenantID || ac.SiteID != req.SiteID || ac.DeviceID != req.DeviceID || ac.GuestNetworkID != req.GuestNetworkID {
			res = PackageListResult{Reason: "auth_context_mismatch"}
			return nil
		}
		if ac.StayID != "" {
			res = PackageListResult{Reason: "pms_not_supported_phase2"}
			return nil
		}
		if ac.Consumed || !ac.ExpiresAt.After(now) {
			res = PackageListResult{Reason: "auth_context_unavailable"}
			return nil
		}
		pkgs, err := tx.ListActivePackageRevisions(ctx, req.TenantID, req.SiteID)
		if err != nil {
			return err
		}
		var items []PackageListItem
		for _, pkg := range pkgs {
			snap, display, eligible, herr := e.evalPackageForSubject(ctx, tx, now, req, ac, pkg)
			if herr != nil {
				return herr
			}
			if !eligible {
				continue // silently excluded — never reveals an ineligible package
			}
			_ = snap
			items = append(items, PackageListItem{PackageID: pkg.PackageID, Display: display})
		}
		res = PackageListResult{Packages: items, Reason: "ok"}
		return nil
	})
	if err != nil {
		return PackageListResult{}, &Error{Code: ErrRepo, Msg: "list"}
	}
	return res, nil
}

// evalPackageForSubject applies every guest-eligibility gate to one candidate package and, when it
// passes, returns the resolved grant snapshot + guest-safe display. A gate failure returns eligible=false
// (never an error) so the caller silently excludes the package. Only a repository/read error is a hard
// error. This mirrors the CreateQuote pipeline exactly, minus any write.
func (e *CommerceEngine) evalPackageForSubject(ctx context.Context, tx CommerceTx, now time.Time, req PackageListRequest, ac AuthContextRow, pkg PackageRevisionRow) (GrantSnapshot, map[string]any, bool, error) {
	if !pkg.PackageActive || !pkg.IsCurrent {
		return GrantSnapshot{}, nil, false, nil
	}
	if pkg.VisibleFrom != nil && now.Before(*pkg.VisibleFrom) {
		return GrantSnapshot{}, nil, false, nil
	}
	if pkg.VisibleUntil != nil && !now.Before(*pkg.VisibleUntil) {
		return GrantSnapshot{}, nil, false, nil
	}
	if ok, _ := IsFreePackage(MoneySpec{PriceMinor: pkg.PriceMinor, Currency: pkg.Currency, CurrencyExponent: pkg.CurrencyExponent, SettlementMethods: pkg.SettlementMethods}); !ok {
		return GrantSnapshot{}, nil, false, nil
	}
	if _, err := ValidateCurrency(pkg.Currency, pkg.CurrencyExponent); err != nil {
		return GrantSnapshot{}, nil, false, nil
	}
	plan, err := tx.LoadPlanRevision(ctx, req.TenantID, req.SiteID, pkg.PlanRevisionID)
	if err != nil {
		// a missing/broken plan revision is a config gap, not a guest-visible fact: exclude, don't error
		if isNotFound(err) {
			return GrantSnapshot{}, nil, false, nil
		}
		return GrantSnapshot{}, nil, false, err
	}
	subj := EligibilitySubject{Now: now, AuthMethod: ac.Method, Kind: ac.Subject.Kind, GuestNetworkID: ac.GuestNetworkID}
	// rules / tiers / prior-purpose are keyed on the RESOLVED package revision id (pkg.ID), exactly as
	// CreateQuote does — not the package id.
	prior, err := tx.HasPriorPurchase(ctx, req.TenantID, req.SiteID, pkg.ID, ac.Subject)
	if err != nil {
		return GrantSnapshot{}, nil, false, err
	}
	subj.HasPriorPurchaseOfPackage = prior
	rules, err := tx.LoadEligibilityRules(ctx, pkg.ID)
	if err != nil {
		return GrantSnapshot{}, nil, false, err
	}
	if ok, _ := EvaluatePackageEligible(rules, subj); !ok {
		return GrantSnapshot{}, nil, false, nil
	}
	tiers, err := tx.LoadGrantTiers(ctx, pkg.ID)
	if err != nil {
		return GrantSnapshot{}, nil, false, err
	}
	tier, matched := FirstMatchTier(tiers, subj)
	if !matched {
		return GrantSnapshot{}, nil, false, nil
	}
	snap, err := BuildGrantSnapshot(tier, plan, pkg)
	if err != nil {
		return GrantSnapshot{}, nil, false, nil
	}
	endMode, window, derr := ResolveEndPolicy(pkg.DurationPolicy, now)
	if derr != nil {
		return GrantSnapshot{}, nil, false, nil
	}
	snap.EndMode = endMode
	if window != nil {
		snap.WindowEndsAt = window.UTC().Format(time.RFC3339)
	}
	return snap, guestDisplay(snap, pkg), true, nil
}

// isNotFound reports whether err is a typed not-found domain error (missing plan/package config).
func isNotFound(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == ErrInvalidInput || e.Code == ErrACNotFound
	}
	return false
}
