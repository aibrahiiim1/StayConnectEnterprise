package iamv2

import (
	"context"
	"encoding/json"
	"time"
)

type QuoteRequest struct {
	TenantID       string
	SiteID         string
	AuthContextID  string
	PackageID      string
	DeviceID       string
	GuestNetworkID string
	// Empty means the single applicable method (Free for a free package, Voucher for a voucher subject); a
	// priced package must name its method.
	Method string
}

type ConfirmRequest struct {
	TenantID       string
	SiteID         string
	QuoteID        string
	DeviceID       string
	GuestNetworkID string
}

// CreateQuote resolves everything server-side in one transaction without consuming the auth context, then
// writes a one-time offer_quote (5-min TTL). A voucher subject is quoted the revision its card was printed
// against, whatever the package's current revision or active flag.
func (e *CommerceEngine) CreateQuote(ctx context.Context, req QuoteRequest) (QuoteResult, error) {
	if !e.cfg.PortalOn() {
		e.obs.Event("phase2.disabled", map[string]string{"op": "quote"})
		return QuoteResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if req.TenantID == "" || req.SiteID == "" || req.AuthContextID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		return QuoteResult{}, &Error{Code: ErrInvalidInput, Msg: "quote: missing tenant/site/auth_context/device/guest_network"}
	}
	now := e.now()
	site := e.siteMethods(ctx)
	var res QuoteResult
	err := e.repo.WithTx(ctx, func(tx CommerceTx) error {
		ac, err := tx.LoadAuthContext(ctx, req.TenantID, req.SiteID, req.AuthContextID)
		if err != nil {
			return err
		}
		// The auth-context must belong to this tenant/site, be for this device and network, be non-PMS,
		// unconsumed and unexpired.
		if ac.TenantID != req.TenantID || ac.SiteID != req.SiteID || ac.DeviceID != req.DeviceID || ac.GuestNetworkID != req.GuestNetworkID {
			res = quoteDeny("auth_context_mismatch")
			return nil
		}
		if ac.StayID != "" {
			res = quoteDeny("pms_uses_room_sign_in")
			return nil
		}
		if ac.Consumed || !ac.ExpiresAt.After(now) {
			res = quoteDeny("auth_context_unavailable")
			return nil
		}

		var pkg PackageRevisionRow
		if ac.Subject.Kind == SubjectVoucher {
			pinned, perr := tx.VoucherPinnedPackageRevision(ctx, req.TenantID, req.SiteID, ac.Subject.VoucherID)
			if perr != nil {
				return perr
			}
			if pinned == "" {
				res = quoteDeny("voucher_unpinned")
				return nil
			}
			pkg, err = tx.LoadPackageRevisionByID(ctx, req.TenantID, req.SiteID, pinned)
			if err != nil {
				return err
			}
			// The guest may not choose a different package; a request naming one is refused, not corrected.
			if req.PackageID != "" && req.PackageID != pkg.PackageID {
				res = quoteDeny("voucher_package_mismatch")
				return nil
			}
		} else {
			if req.PackageID == "" {
				return &Error{Code: ErrInvalidInput, Msg: "quote: missing package"}
			}
			pkg, err = tx.ResolveActivePackageRevision(ctx, req.TenantID, req.SiteID, req.PackageID)
			if err != nil {
				return err
			}
			if !pkg.PackageActive || !pkg.IsCurrent {
				res = quoteDeny("package_unavailable")
				return nil
			}
			if pkg.VisibleFrom != nil && now.Before(*pkg.VisibleFrom) {
				res = quoteDeny("not_in_sale_window")
				return nil
			}
			if pkg.VisibleUntil != nil && !now.Before(*pkg.VisibleUntil) {
				res = quoteDeny("sale_window_closed")
				return nil
			}
		}

		applicable := applicableMethods(pkg, ac, site)
		method := req.Method
		if method == "" && len(applicable) == 1 {
			method = applicable[0]
		}
		if method == "" || !containsMethod(applicable, method) {
			res = quoteDeny("method_not_available")
			return nil
		}
		cur, cerr := ValidateCurrency(pkg.Currency, pkg.CurrencyExponent)
		if cerr != nil {
			res = quoteDeny("invalid_currency")
			return nil
		}

		plan, err := tx.LoadPlanRevision(ctx, req.TenantID, req.SiteID, pkg.PlanRevisionID)
		if err != nil {
			return err
		}

		// Also accept the client networks this one continues, so an unused printed voucher (which redeems its
		// pinned revision) survives a network replacement.
		ancestors, aerr := tx.GuestNetworkAncestors(ctx, req.TenantID, req.SiteID, ac.GuestNetworkID)
		if aerr != nil {
			return aerr
		}
		subj := EligibilitySubject{Now: now, AuthMethod: ac.Method, Kind: ac.Subject.Kind,
			GuestNetworkID: ac.GuestNetworkID, GuestNetworkLineage: ancestors, ClientGroupID: ac.ClientGroupID}

		rules, err := tx.LoadEligibilityRules(ctx, pkg.ID)
		if err != nil {
			return err
		}
		// History is keyed on the package under the revision's own policy; a quote must never pass what the
		// list withheld.
		if pol := PriorPurchasePolicyOf(rules, pkg.PriceMinor).ForSubject(ac.Subject.Kind); pol.Present {
			prior, err := tx.HasPriorPurchase(ctx, req.TenantID, req.SiteID, PriorPurchaseQuery{PackageID: pkg.PackageID, Subject: ac.Subject, Policy: pol})
			if err != nil {
				return err
			}
			subj.HasPriorPurchaseOfPackage = prior
		}
		if ok, why := EvaluatePackageEligible(rules, subj); !ok {
			res = quoteDeny("ineligible:" + why)
			return nil
		}
		// A free package staff ended is not taken again automatically (0202); a priced one may still be bought.
		if pkg.PriceMinor == 0 {
			ended, err := tx.EndedByStaff(ctx, req.TenantID, req.SiteID, pkg.ID, ac.Subject)
			if err != nil {
				return err
			}
			if ended {
				res = quoteDeny("ineligible:ended_by_staff")
				return nil
			}
		}

		tiers, err := tx.LoadGrantTiers(ctx, pkg.ID)
		if err != nil {
			return err
		}
		tier, matched := FirstMatchTier(tiers, subj)
		if !matched {
			res = quoteDeny("no_matching_grant_tier")
			return nil
		}

		// Typed, validated snapshot: no arbitrary jsonb is copied into a security-enforced snapshot.
		snapshot, err := BuildGrantSnapshot(tier, plan, pkg)
		if err != nil {
			res = quoteDeny("invalid_grant_config")
			return nil
		}
		// A time mode this runtime cannot account for is refused here, before a quote exists.
		if why := TimeModeAcquirable(snapshot.TimeAccountingMode, e.aggregateOnlineTime); why != "" {
			res = quoteDeny(why)
			return nil
		}
		endMode, window, derr := ResolveEndPolicy(pkg.DurationPolicy, now)
		if derr != nil {
			res = quoteDeny("invalid_duration_policy")
			return nil
		}
		snapshot.EndMode = endMode
		if window != nil {
			snapshot.WindowEndsAt = window.UTC().Format(time.RFC3339)
		}
		if dp, mErr := marshalPolicy(pkg.DurationPolicy); mErr == nil {
			snapshot.DurationPolicy = dp
		}
		snapshot.AcquisitionMethod = method
		id, err := tx.InsertOfferQuote(ctx, OfferQuoteSpec{
			TenantID: req.TenantID, SiteID: req.SiteID, AuthContextID: ac.ID, PackageRevisionID: pkg.ID,
			PriceMinor: pkg.PriceMinor, Currency: cur, CurrencyExponent: pkg.CurrencyExponent,
			GrantSnapshot: snapshot, ExpiresAt: now.Add(e.ttl), Now: now,
		})
		if err != nil {
			return err
		}
		res = QuoteResult{QuoteID: id, ExpiresAt: now.Add(e.ttl), Display: guestDisplay(snapshot, pkg), Reason: "ok"}
		return nil
	})
	if err != nil {
		return QuoteResult{}, &Error{Code: ErrRepo, Msg: "quote"}
	}
	return res, nil
}

func (e *CommerceEngine) ConfirmFreePurchase(ctx context.Context, req ConfirmRequest) (PurchaseResult, error) {
	return e.ConfirmPurchase(ctx, req)
}

// ConfirmPurchase consumes the quote and its pinned auth-context and writes Purchase and Settlement in one
// transaction (lock order quote -> auth-context -> subject); site gates are re-checked, not trusted from the
// quote.
//
//	Free          settlement NOT_REQUIRED; granted in this transaction
//	Voucher       purchase VOUCHER_REDEMPTION, settlement PREPAID/SETTLED; granted in this transaction, burning the voucher
//	Card payment  purchase AWAITING_SETTLEMENT, settlement ONLINE_PAYMENT/REQUIRED; nothing is granted until a verified capture
func (e *CommerceEngine) ConfirmPurchase(ctx context.Context, req ConfirmRequest) (PurchaseResult, error) {
	if !e.cfg.PortalOn() {
		e.obs.Event("phase2.disabled", map[string]string{"op": "purchase"})
		return PurchaseResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if req.TenantID == "" || req.SiteID == "" || req.QuoteID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		return PurchaseResult{}, &Error{Code: ErrInvalidInput, Msg: "confirm: missing tenant/site/quote/device/guest_network"}
	}
	now := e.now()
	site := e.siteMethods(ctx)
	var res PurchaseResult
	err := e.repo.WithTx(ctx, func(tx CommerceTx) error {
		q, err := tx.LockOfferQuoteForUpdate(ctx, req.TenantID, req.SiteID, req.QuoteID)
		if err != nil {
			return err
		}
		if q.Consumed || !q.ExpiresAt.After(now) {
			res = purchaseDeny("quote_unavailable")
			return nil
		}
		method := q.GrantSnapshot.AcquisitionMethod
		if method == "" {
			method = AcquireFree
		}
		// Re-validate for its method before consuming anything.
		if why := revalidateQuote(q, method, site); why != "" {
			res = purchaseDeny(why)
			return nil
		}
		// Time mode re-checked at confirm, before anything is consumed.
		if why := TimeModeAcquirable(q.GrantSnapshot.TimeAccountingMode, e.aggregateOnlineTime); why != "" {
			res = purchaseDeny(why)
			return nil
		}
		// A per-night allowance needs a stay, re-checked as the listing and quote do (applicableMethods).
		if pkg, perr := tx.LoadPackageRevisionByID(ctx, req.TenantID, req.SiteID, q.PackageRevisionID); perr != nil {
			return perr
		} else if pkg.DataAllocationMode == AllocPerStayNight {
			res = purchaseDeny("per_night_allowance_needs_stay")
			return nil
		}
		ac, err := tx.LockAuthContextForUpdate(ctx, req.TenantID, req.SiteID, q.AuthContextID)
		if err != nil {
			return err
		}
		if ac.TenantID != req.TenantID || ac.SiteID != req.SiteID ||
			ac.DeviceID != req.DeviceID || ac.GuestNetworkID != req.GuestNetworkID {
			res = purchaseDeny("auth_context_mismatch")
			return nil
		}
		if ac.Consumed || !ac.ExpiresAt.After(now) {
			res = purchaseDeny("auth_context_unavailable")
			return nil
		}
		if _, ok := ac.Subject.subjectID(); !ok {
			res = purchaseDeny("subject_invalid")
			return nil
		}
		// The method must still fit the subject: only a voucher subject acquires by Voucher, and by nothing
		// else.
		if (method == AcquireVoucher) != (ac.Subject.Kind == SubjectVoucher) {
			res = purchaseDeny("method_subject_mismatch")
			return nil
		}
		// Same lock order for every confirm.
		if err := tx.AcquireSubjectLock(ctx, req.TenantID, req.SiteID, ac.Subject); err != nil {
			return err
		}
		okQ, err := tx.ConsumeOfferQuote(ctx, q.ID, now)
		if err != nil {
			return err
		}
		if !okQ {
			res = purchaseDeny("quote_already_consumed")
			return nil
		}
		okA, err := tx.ConsumeAuthContextByID(ctx, ac.ID, now)
		if err != nil {
			return err
		}
		if !okA {
			res = purchaseDeny("auth_context_already_consumed")
			return nil
		}
		spec := PurchaseSpec{
			TenantID: req.TenantID, SiteID: req.SiteID, PackageRevisionID: q.PackageRevisionID,
			OfferQuoteID: q.ID, AuthContextID: ac.ID, Subject: ac.Subject,
			AmountMinor: q.PriceMinor, Currency: q.Currency, CurrencyExponent: q.CurrencyExponent,
		}
		switch method {
		case AcquireFree:
			pid, err := tx.InsertPurchase(ctx, spec)
			if err != nil {
				return err
			}
			if err := tx.InsertSettlement(ctx, req.TenantID, req.SiteID, pid); err != nil {
				return err
			}
			eid, superseded, err := tx.GrantQuotedEntitlement(ctx, req.TenantID, req.SiteID, pid)
			if err != nil {
				return err
			}
			res = PurchaseResult{PurchaseID: pid, EntitlementID: eid, Superseded: superseded, Method: method, Reason: "granted"}
		case AcquireVoucher:
			pid, err := tx.InsertPurchaseAs(ctx, spec, "VOUCHER_REDEMPTION", "PENDING")
			if err != nil {
				return err
			}
			sid, err := tx.InsertSettlementAs(ctx, req.TenantID, req.SiteID, pid, AcquireVoucher, "SETTLED")
			if err != nil {
				return err
			}
			eid, superseded, err := tx.GrantVoucherEntitlement(ctx, req.TenantID, req.SiteID, pid)
			if err != nil {
				return err
			}
			res = PurchaseResult{PurchaseID: pid, EntitlementID: eid, Superseded: superseded, SettlementID: sid,
				Method: method, Reason: "granted"}
		case AcquireCard:
			pid, err := tx.InsertPurchaseAs(ctx, spec, "GUEST_SELECTION", "AWAITING_SETTLEMENT")
			if err != nil {
				return err
			}
			sid, err := tx.InsertSettlementAs(ctx, req.TenantID, req.SiteID, pid, AcquireCard, "REQUIRED")
			if err != nil {
				return err
			}
			res = PurchaseResult{PurchaseID: pid, SettlementID: sid, Method: method, AwaitingSettlement: true,
				Reason: "awaiting_settlement"}
		default:
			res = purchaseDeny("method_not_available")
			return errDenyRollback
		}
		return nil
	})
	if err == errDenyRollback {
		return res, nil
	}
	if err != nil {
		// Fail closed: the whole tx rolled back; report a generic repo error.
		return PurchaseResult{}, &Error{Code: ErrRepo, Msg: "confirm"}
	}
	return res, nil
}

// Aborts a confirm after consumption when the method is unknown, so nothing consumed survives a refusal.
var errDenyRollback = &Error{Code: ErrInvalidInput, Msg: "confirm refused"}

// Re-asserts at confirm time that a locked quote is coherent for its method and that the method is still
// effective at this site; a tampered quote row is rejected before any consume.
func revalidateQuote(q OfferQuoteRow, method string, site SiteMethods) string {
	switch method {
	case AcquireFree:
		return revalidateFreeQuote(q)
	case AcquireVoucher:
		if q.PMSInterfaceID != nil || q.SettlementMappingID != nil {
			return "quote_has_pms_settlement"
		}
	case AcquireCard:
		if !site.PaidAccess || !site.Card {
			return "method_not_available"
		}
		if q.PriceMinor <= 0 {
			return "quote_not_priced"
		}
		if q.PMSInterfaceID != nil || q.SettlementMappingID != nil {
			return "quote_has_pms_settlement"
		}
		// Currency is re-checked now: the default account may have changed since the quote, and confirming
		// would strand an AWAITING_SETTLEMENT purchase (PAYMENT_ACCOUNT_CURRENCY).
		if !site.CardApplicable(q.Currency) {
			return "card_currency_not_accepted"
		}
	default:
		return "method_not_available"
	}
	if _, err := ValidateCurrency(q.Currency, q.CurrencyExponent); err != nil {
		return "quote_bad_currency"
	}
	if q.GrantSnapshot.Version != GrantSnapshotVersion || q.GrantSnapshot.ServicePlanRevisionID == "" {
		return "quote_bad_snapshot"
	}
	return ""
}

func quoteDeny(reason string) QuoteResult       { return QuoteResult{Reason: reason} }
func purchaseDeny(reason string) PurchaseResult { return PurchaseResult{Reason: reason} }

func (s CommerceSubject) subjectID() (string, bool) {
	switch s.Kind {
	case SubjectVoucher:
		return s.VoucherID, s.VoucherID != "" && s.AccountID == "" && s.PrincipalID == ""
	case SubjectAccount:
		return s.AccountID, s.AccountID != "" && s.VoucherID == "" && s.PrincipalID == ""
	case SubjectPrincipal:
		return s.PrincipalID, s.PrincipalID != "" && s.VoucherID == "" && s.AccountID == ""
	case SubjectAnonymous:
		return s.AnonymousID, s.AnonymousID != "" && s.VoucherID == "" && s.AccountID == "" && s.PrincipalID == ""
	}
	return "", false
}

// Zero price, valid currency and exponent, and no PMS interface, settlement mapping or tax; returns "" when
// valid, else a reason.
func revalidateFreeQuote(q OfferQuoteRow) string {
	if q.PriceMinor != 0 {
		return "quote_not_free"
	}
	if _, err := ValidateCurrency(q.Currency, q.CurrencyExponent); err != nil {
		return "quote_bad_currency"
	}
	if q.PMSInterfaceID != nil || q.SettlementMappingID != nil {
		return "quote_has_pms_settlement"
	}
	if q.TaxCode != nil || (q.TaxRateBP != nil && *q.TaxRateBP != 0) || (q.TaxAmountMinor != nil && *q.TaxAmountMinor != 0) {
		return "quote_has_tax"
	}
	if q.GrantSnapshot.Version != GrantSnapshotVersion || q.GrantSnapshot.ServicePlanRevisionID == "" {
		return "quote_bad_snapshot"
	}
	return ""
}

func marshalPolicy(dp map[string]any) (json.RawMessage, error) {
	if len(dp) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(dp)
	return json.RawMessage(b), err
}

func guestDisplay(snap GrantSnapshot, pkg PackageRevisionRow) map[string]any {
	d := map[string]any{
		"down_kbps":              snap.DownKbps,
		"up_kbps":                snap.UpKbps,
		"max_concurrent_devices": snap.MaxConcurrentDevices,
		"data_quota_bytes":       snap.DataQuotaBytes,
		"time_quota_seconds":     snap.TimeQuotaSeconds,
		"end_mode":               snap.EndMode,
		"price_minor":            pkg.PriceMinor,
		"currency":               pkg.Currency,
		"currency_exponent":      pkg.CurrencyExponent,
		"free":                   pkg.PriceMinor == 0,
	}
	if snap.WindowEndsAt != "" {
		d["window_ends_at"] = snap.WindowEndsAt
	}
	if pkg.Display != nil {
		if name, ok := pkg.Display["name"]; ok {
			d["name"] = name
		}
	}
	return d
}
