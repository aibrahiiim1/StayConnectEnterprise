package iamv2

import (
	"context"
	"encoding/json"
	"time"
)

// QuoteRequest is the guest's package-selection input. The browser submits only identifiers; the
// server resolves every priced/pinned dimension.
type QuoteRequest struct {
	TenantID       string
	SiteID         string
	AuthContextID  string
	PackageID      string
	DeviceID       string
	GuestNetworkID string
	// Method is how the client chose to acquire the package. "" means the single applicable method (Free for
	// a free package, Voucher for a voucher subject); a priced package must name its method.
	Method string
}

// ConfirmRequest confirms a previously-created quote. The client submits only the opaque quote id and
// the device/network it is operating from.
type ConfirmRequest struct {
	TenantID       string
	SiteID         string
	QuoteID        string
	DeviceID       string
	GuestNetworkID string
}

// CreateQuote resolves -- server-side, in one transaction, WITHOUT consuming the auth context -- the
// package/plan revisions, the acquisition method, eligibility, the first-match grant tier and the price, then
// writes a one-time offer_quote (5-min TTL). Returns only guest-safe display + the opaque quote id. When the
// portal surface is OFF it returns Disabled without touching the repository (zero SQL).
//
// A voucher subject is quoted the revision its card was printed against, by Voucher, whatever the package
// current revision or active flag: an issued voucher is honoured until its own state, window or an explicit
// revocation ends it.
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
		// Pin validation (no consume): the auth-context must belong to this tenant/site, be for THIS
		// device+network, be non-PMS, unconsumed and unexpired.
		if ac.TenantID != req.TenantID || ac.SiteID != req.SiteID || ac.DeviceID != req.DeviceID || ac.GuestNetworkID != req.GuestNetworkID {
			res = quoteDeny("auth_context_mismatch")
			return nil
		}
		if ac.StayID != "" {
			res = quoteDeny("pms_uses_room_sign_in") // a verified stay acquires through the Room sign-in path
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
				res = quoteDeny("sale_window_closed") // upper bound exclusive
				return nil
			}
		}

		// THE METHOD: effective at this site, listed by the package, applicable to this client -- now.
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

		// ...AND THE CLIENT NETWORKS THIS ONE CONTINUES. A rule naming a network that was safely replaced for
		// VLAN/port/addressing reasons is satisfied by a device on its successor; without this an UNUSED PRINTED
		// VOUCHER, which redeems its pinned immutable revision directly, died the moment a cable moved.
		ancestors, aerr := tx.GuestNetworkAncestors(ctx, req.TenantID, req.SiteID, ac.GuestNetworkID)
		if aerr != nil {
			return aerr
		}
		subj := EligibilitySubject{Now: now, AuthMethod: ac.Method, Kind: ac.Subject.Kind,
			GuestNetworkID: ac.GuestNetworkID, GuestNetworkLineage: ancestors}
		prior, err := tx.HasPriorPurchase(ctx, req.TenantID, req.SiteID, pkg.ID, ac.Subject)
		if err != nil {
			return err
		}
		subj.HasPriorPurchaseOfPackage = prior

		rules, err := tx.LoadEligibilityRules(ctx, pkg.ID)
		if err != nil {
			return err
		}
		if ok, why := EvaluatePackageEligible(rules, subj); !ok {
			res = quoteDeny("ineligible:" + why)
			return nil
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

		// typed, validated grant snapshot (no arbitrary jsonb copied into a security-enforced snapshot)
		snapshot, err := BuildGrantSnapshot(tier, plan, pkg)
		if err != nil {
			res = quoteDeny("invalid_grant_config")
			return nil
		}
		// PHASE 6: a mode this runtime cannot account for is refused HERE, before a quote exists.
		if why := TimeModeAcquirable(snapshot.TimeAccountingMode, e.aggregateOnlineTime); why != "" {
			res = quoteDeny(why)
			return nil
		}
		// resolve the immutable duration/end policy once, freezing it into the snapshot
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

// ConfirmFreePurchase is kept as the historical name of the confirm step; it confirms whichever method the
// quote pinned. See ConfirmPurchase.
func (e *CommerceEngine) ConfirmFreePurchase(ctx context.Context, req ConfirmRequest) (PurchaseResult, error) {
	return e.ConfirmPurchase(ctx, req)
}

// ConfirmPurchase consumes the quote + its pinned auth-context and writes the Purchase + Settlement in ONE
// transaction with a deterministic lock order (quote -> auth-context -> subject). What happens next depends
// on the method the quote pinned, and the site gates are re-checked here rather than trusted from the quote:
//
//	Free          settlement NOT_REQUIRED; granted in this transaction (p4_grant_quoted_entitlement)
//	Voucher       purchase VOUCHER_REDEMPTION, settlement PREPAID/SETTLED; granted in this transaction by
//	              p4_grant_voucher_entitlement, which burns the voucher in the same statement chain
//	Card payment  purchase AWAITING_SETTLEMENT, settlement ONLINE_PAYMENT/REQUIRED; NOTHING is granted. The
//	              caller starts the provider checkout; access follows only a verified capture.
//
// Any failure rolls the whole thing back (single tx): zero partial rows.
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
		// 1. lock the quote (tenant/site scoped); reject consumed/expired.
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
			method = AcquireFree // a quote that predates acquisition methods was a free quote
		}
		// 1a. Re-validate the quote for its method BEFORE consuming anything.
		if why := revalidateQuote(q, method, site); why != "" {
			res = purchaseDeny(why)
			return nil
		}
		// 1b. PHASE 6, RE-CHECKED AT CONFIRM AND BEFORE ANYTHING IS CONSUMED.
		if why := TimeModeAcquirable(q.GrantSnapshot.TimeAccountingMode, e.aggregateOnlineTime); why != "" {
			res = purchaseDeny(why)
			return nil
		}
		// 2. lock the EXACT pinned auth-context; verify all pins.
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
		// The method must still fit the subject: only a voucher subject acquires by Voucher, and a voucher
		// subject acquires by nothing else.
		if (method == AcquireVoucher) != (ac.Subject.Kind == SubjectVoucher) {
			res = purchaseDeny("method_subject_mismatch")
			return nil
		}
		// 3. deterministic subject/entitlement lock (advisory) - same order for every confirm.
		if err := tx.AcquireSubjectLock(ctx, req.TenantID, req.SiteID, ac.Subject); err != nil {
			return err
		}
		// 4. atomic compare-and-set consume of quote then auth-context.
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
			AmountMinor: q.PriceMinor, Currency: q.Currency, CurrencyExponent: q.CurrencyExponent, // pinned from the quote, never defaulted
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
			// THE SHARED GRANT KERNEL (migrations 0024, 0095): the FREE authorization.
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
		// FAIL CLOSED: the whole tx rolled back; no partial rows. Report a generic repo error.
		return PurchaseResult{}, &Error{Code: ErrRepo, Msg: "confirm"}
	}
	return res, nil
}

// errDenyRollback aborts a confirm transaction after consumption when the method turns out to be unknown,
// so nothing consumed survives a refusal.
var errDenyRollback = &Error{Code: ErrInvalidInput, Msg: "confirm refused"}

// revalidateQuote re-asserts, at confirm time, that a locked quote is coherent for its method AND that the
// method is still effective at this site. A tampered quote row is rejected before any consume.
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

// ---- helpers ----

func quoteDeny(reason string) QuoteResult       { return QuoteResult{Reason: reason} }
func purchaseDeny(reason string) PurchaseResult { return PurchaseResult{Reason: reason} }

// subjectID returns the single non-PMS subject id and whether exactly one is set.
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

// revalidateFreeQuote re-asserts, at confirm time, that a locked quote is a valid Phase-2 FREE quote:
// zero price, valid currency+exponent, and NO PMS interface / settlement mapping / tax. A tampered
// quote row is rejected before any consume. Returns "" when valid, else a deterministic reason.
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

// marshalPolicy returns a compact JSON of the duration policy for the snapshot's audit copy.
func marshalPolicy(dp map[string]any) (json.RawMessage, error) {
	if len(dp) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(dp)
	return json.RawMessage(b), err
}

// guestDisplay returns only guest-appropriate display fields from the typed snapshot.
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
