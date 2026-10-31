package iamv2

import (
	"context"
	"errors"
	"time"
)

// Only server-resolved trusted identifiers; the browser never supplies them.
type PackageListRequest struct {
	TenantID       string
	SiteID         string
	AuthContextID  string
	DeviceID       string
	GuestNetworkID string
}

// Guest-safe view: an opaque package id and display only, never pins, price internals, revisions or
// ineligible-package data.
type PackageListItem struct {
	PackageID string         `json:"package_id"`
	Display   map[string]any `json:"display"`
	// Methods this client may use right now (never one that would predictably fail after selection); price
	// fields are display only, the quote pins them.
	Methods          []string `json:"methods"`
	PriceMinor       int64    `json:"price_minor"`
	Currency         string   `json:"currency"`
	CurrencyExponent int      `json:"currency_exponent"`
}

// Non-voucher subjects have no printed card or pin, so this returns true. A voucher whose row cannot be
// resolved offers nothing.
func (e *CommerceEngine) voucherMayHavePackage(ctx context.Context, tx CommerceTx, req PackageListRequest, ac AuthContextRow, pkg PackageRevisionRow) (bool, error) {
	if ac.Subject.Kind != SubjectVoucher || ac.Subject.VoucherID == "" {
		return true, nil
	}
	pinned, err := tx.VoucherPinnedPackageRevision(ctx, req.TenantID, req.SiteID, ac.Subject.VoucherID)
	if err != nil {
		return false, err
	}
	return pinned != "" && pinned == pkg.ID, nil
}

type PackageListResult struct {
	Disabled bool
	Packages []PackageListItem
	Reason   string
	// The one withheld reason safe to show: a free package withheld only because the Client's own history says
	// they already had it. Every other exclusion stays silent.
	FreeAllowanceUsed bool
	// For display only; never decides anything here.
	ClientGroupID string
}

// Read-only, creates no quote, purchase or entitlement. Applies the same gates as CreateQuote but silently
// excludes failures so no ineligible package's existence is revealed.
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
		if ac.TenantID != req.TenantID || ac.SiteID != req.SiteID || ac.DeviceID != req.DeviceID || ac.GuestNetworkID != req.GuestNetworkID {
			res = PackageListResult{Reason: "auth_context_mismatch"}
			return nil
		}
		if ac.StayID != "" {
			res = PackageListResult{Reason: "pms_uses_room_sign_in"}
			return nil
		}
		if ac.Consumed || !ac.ExpiresAt.After(now) {
			res = PackageListResult{Reason: "auth_context_unavailable"}
			return nil
		}
		var pkgs []PackageRevisionRow
		if ac.Subject.Kind == SubjectVoucher {
			// A voucher is offered only the revision it was printed against, even after the package was
			// republished or deactivated.
			pinned, perr := tx.VoucherPinnedPackageRevision(ctx, req.TenantID, req.SiteID, ac.Subject.VoucherID)
			if perr != nil {
				return perr
			}
			if pinned != "" {
				pkg, lerr := tx.LoadPackageRevisionByID(ctx, req.TenantID, req.SiteID, pinned)
				if lerr != nil && !isNotFound(lerr) {
					return lerr
				}
				if lerr == nil {
					pkgs = append(pkgs, pkg)
				}
			}
		} else {
			pkgs, err = tx.ListActivePackageRevisions(ctx, req.TenantID, req.SiteID)
			if err != nil {
				return err
			}
		}
		site := e.siteMethods(ctx)
		var items []PackageListItem
		freeUsed := false
		for _, pkg := range pkgs {
			snap, display, eligible, why, herr := e.evalPackageForSubject(ctx, tx, now, req, ac, pkg)
			if herr != nil {
				return herr
			}
			if !eligible {
				if why == "forbids_prior_purchase" && pkg.PriceMinor == 0 {
					freeUsed = true
				}
				continue
			}
			_ = snap
			methods := applicableMethods(pkg, ac, site)
			if len(methods) == 0 {
				continue
			}
			items = append(items, PackageListItem{PackageID: pkg.PackageID, Display: display, Methods: methods,
				PriceMinor: pkg.PriceMinor, Currency: pkg.Currency, CurrencyExponent: pkg.CurrencyExponent})
		}
		res = PackageListResult{Packages: items, Reason: "ok", FreeAllowanceUsed: freeUsed, ClientGroupID: ac.ClientGroupID}
		return nil
	})
	if err != nil {
		return PackageListResult{}, &Error{Code: ErrRepo, Msg: "list"}
	}
	return res, nil
}

// A gate failure returns eligible=false, never an error, so the caller silently excludes the package; only a
// read error is a hard error. Mirrors CreateQuote minus any write.
func (e *CommerceEngine) evalPackageForSubject(ctx context.Context, tx CommerceTx, now time.Time, req PackageListRequest, ac AuthContextRow, pkg PackageRevisionRow) (GrantSnapshot, map[string]any, bool, string, error) {
	isVoucher := ac.Subject.Kind == SubjectVoucher
	if !isVoucher && (!pkg.PackageActive || !pkg.IsCurrent) {
		return GrantSnapshot{}, nil, false, "", nil
	}
	// A voucher is offered what it was printed for and nothing else. The gate is first because it is the
	// narrowest; the grant kernel refuses a mismatch regardless, this keeps a guest from being shown a choice
	// that would fail.
	if ok, err := e.voucherMayHavePackage(ctx, tx, req, ac, pkg); err != nil || !ok {
		return GrantSnapshot{}, nil, false, "", err
	}
	// The sale window governs offers; an issued voucher is not re-windowed.
	if !isVoucher && pkg.VisibleFrom != nil && now.Before(*pkg.VisibleFrom) {
		return GrantSnapshot{}, nil, false, "", nil
	}
	if !isVoucher && pkg.VisibleUntil != nil && !now.Before(*pkg.VisibleUntil) {
		return GrantSnapshot{}, nil, false, "", nil
	}
	if _, err := ValidateCurrency(pkg.Currency, pkg.CurrencyExponent); err != nil {
		return GrantSnapshot{}, nil, false, "", nil
	}
	plan, err := tx.LoadPlanRevision(ctx, req.TenantID, req.SiteID, pkg.PlanRevisionID)
	if err != nil {
		// A missing or broken plan revision is a config gap, not a guest-visible fact: exclude, don't error.
		if isNotFound(err) {
			return GrantSnapshot{}, nil, false, "", nil
		}
		return GrantSnapshot{}, nil, false, "", err
	}
	ancestors, aerr := tx.GuestNetworkAncestors(ctx, req.TenantID, req.SiteID, ac.GuestNetworkID)
	if aerr != nil {
		return GrantSnapshot{}, nil, false, "", aerr
	}
	subj := EligibilitySubject{Now: now, AuthMethod: ac.Method, Kind: ac.Subject.Kind,
		GuestNetworkID: ac.GuestNetworkID, GuestNetworkLineage: ancestors, ClientGroupID: ac.ClientGroupID}
	// Rules and tiers are keyed on the resolved revision id; prior-purchase history is keyed on the package.
	rules, err := tx.LoadEligibilityRules(ctx, pkg.ID)
	if err != nil {
		return GrantSnapshot{}, nil, false, "", err
	}
	if pol := PriorPurchasePolicyOf(rules, pkg.PriceMinor).ForSubject(ac.Subject.Kind); pol.Present {
		prior, err := tx.HasPriorPurchase(ctx, req.TenantID, req.SiteID, PriorPurchaseQuery{PackageID: pkg.PackageID, Subject: ac.Subject, Policy: pol})
		if err != nil {
			return GrantSnapshot{}, nil, false, "", err
		}
		subj.HasPriorPurchaseOfPackage = prior
	}
	if ok, why := EvaluatePackageEligible(rules, subj); !ok {
		return GrantSnapshot{}, nil, false, why, nil
	}
	if pkg.PriceMinor == 0 {
		ended, err := tx.EndedByStaff(ctx, req.TenantID, req.SiteID, pkg.ID, ac.Subject)
		if err != nil {
			return GrantSnapshot{}, nil, false, "", err
		}
		if ended {
			return GrantSnapshot{}, nil, false, "ended_by_staff", nil
		}
	}
	tiers, err := tx.LoadGrantTiers(ctx, pkg.ID)
	if err != nil {
		return GrantSnapshot{}, nil, false, "", err
	}
	tier, matched := FirstMatchTier(tiers, subj)
	if !matched {
		return GrantSnapshot{}, nil, false, "", nil
	}
	snap, err := BuildGrantSnapshot(tier, plan, pkg)
	if err != nil {
		return GrantSnapshot{}, nil, false, "", nil
	}
	endMode, window, derr := ResolveEndPolicy(pkg.DurationPolicy, now)
	if derr != nil {
		return GrantSnapshot{}, nil, false, "", nil
	}
	snap.EndMode = endMode
	if window != nil {
		snap.WindowEndsAt = window.UTC().Format(time.RFC3339)
	}
	return snap, guestDisplay(snap, pkg), true, "", nil
}

func isNotFound(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == ErrInvalidInput || e.Code == ErrACNotFound
	}
	return false
}
