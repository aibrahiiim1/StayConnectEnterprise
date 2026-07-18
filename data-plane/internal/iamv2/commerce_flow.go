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

// CreateQuote resolves — server-side, in one transaction, WITHOUT consuming the auth context — the
// package/plan revisions, eligibility, first-match grant tier and free price, then writes a one-time
// offer_quote (5-min TTL). Returns only guest-safe display + the opaque quote id. When the portal
// surface is OFF it returns Disabled without touching the repository (zero SQL).
func (e *CommerceEngine) CreateQuote(ctx context.Context, req QuoteRequest) (QuoteResult, error) {
	if !e.cfg.PortalOn() {
		e.obs.Event("phase2.disabled", map[string]string{"op": "quote"})
		return QuoteResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if req.TenantID == "" || req.SiteID == "" || req.AuthContextID == "" || req.PackageID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		return QuoteResult{}, &Error{Code: ErrInvalidInput, Msg: "quote: missing tenant/site/auth_context/package/device/guest_network"}
	}
	now := e.now()
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
			res = quoteDeny("pms_not_supported_phase2") // Phase 2 is non-PMS only
			return nil
		}
		if ac.Consumed || !ac.ExpiresAt.After(now) {
			res = quoteDeny("auth_context_unavailable")
			return nil
		}

		pkg, err := tx.ResolveActivePackageRevision(ctx, req.TenantID, req.SiteID, req.PackageID)
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
		// Free-only gate (Phase 2). A priced / non-NOT_REQUIRED package is unavailable and fails closed.
		if ok, why := IsFreePackage(MoneySpec{PriceMinor: pkg.PriceMinor, Currency: pkg.Currency, CurrencyExponent: pkg.CurrencyExponent, SettlementMethods: pkg.SettlementMethods}); !ok {
			res = quoteDeny(why)
			return nil
		}

		plan, err := tx.LoadPlanRevision(ctx, req.TenantID, req.SiteID, pkg.PlanRevisionID)
		if err != nil {
			return err
		}

		subj := EligibilitySubject{Now: now, AuthMethod: ac.Method, Kind: ac.Subject.Kind, GuestNetworkID: ac.GuestNetworkID}
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
		// canonical currency for the (zero-price) free quote
		cur, cerr := ValidateCurrency(pkg.Currency, pkg.CurrencyExponent)
		if cerr != nil {
			res = quoteDeny("invalid_currency")
			return nil
		}
		id, err := tx.InsertOfferQuote(ctx, OfferQuoteSpec{
			TenantID: req.TenantID, SiteID: req.SiteID, AuthContextID: ac.ID, PackageRevisionID: pkg.ID,
			PriceMinor: 0, Currency: cur, CurrencyExponent: pkg.CurrencyExponent,
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

// ConfirmFreePurchase consumes the quote + its pinned auth-context and creates the Purchase +
// Settlement + Entitlement in ONE transaction with a deterministic lock order (quote → auth-context →
// subject). Any failure rolls the whole thing back (single tx): zero partial rows. Concurrent
// confirmations of the same quote produce exactly one Purchase (quote consume CAS + offer_quote_id
// UNIQUE); losers get a deterministic generic conflict.
func (e *CommerceEngine) ConfirmFreePurchase(ctx context.Context, req ConfirmRequest) (PurchaseResult, error) {
	if !e.cfg.PortalOn() {
		e.obs.Event("phase2.disabled", map[string]string{"op": "purchase"})
		return PurchaseResult{Disabled: true, Reason: "phase2_disabled"}, nil
	}
	if req.TenantID == "" || req.SiteID == "" || req.QuoteID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		return PurchaseResult{}, &Error{Code: ErrInvalidInput, Msg: "confirm: missing tenant/site/quote/device/guest_network"}
	}
	now := e.now()
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
		// 1a. Re-validate the quote as a Phase-2 FREE quote BEFORE consuming anything (a tampered quote
		//     row — non-zero price, a settlement mapping, a PMS interface, any tax — must not be granted).
		if why := revalidateFreeQuote(q); why != "" {
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
		// 3. deterministic subject/entitlement lock (advisory) — same order for every confirm.
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
		// 5. insert Purchase (PENDING) + Settlement (NOT_REQUIRED). state becomes GRANTED only after the
		//    entitlement exists.
		pid, err := tx.InsertPurchase(ctx, PurchaseSpec{
			TenantID: req.TenantID, SiteID: req.SiteID, PackageRevisionID: q.PackageRevisionID,
			OfferQuoteID: q.ID, AuthContextID: ac.ID, Subject: ac.Subject,
			AmountMinor: q.PriceMinor, Currency: q.Currency, CurrencyExponent: q.CurrencyExponent, // pinned from the quote, never defaulted
		})
		if err != nil {
			return err
		}
		if err := tx.InsertSettlement(ctx, req.TenantID, req.SiteID, pid); err != nil {
			return err
		}
		// 6. create/supersede the subject's single live entitlement from the quote's immutable snapshot.
		superseded, err := tx.TerminateLiveEntitlementForSubject(ctx, req.TenantID, req.SiteID, ac.Subject)
		if err != nil {
			return err
		}
		var window *time.Time
		if q.GrantSnapshot.WindowEndsAt != "" {
			if w, perr := time.Parse(time.RFC3339, q.GrantSnapshot.WindowEndsAt); perr == nil {
				window = &w
			}
		}
		eid, err := tx.InsertEntitlement(ctx, EntitlementSpec{
			TenantID: req.TenantID, SiteID: req.SiteID, PurchaseID: pid, Subject: ac.Subject,
			ServicePlanRevID: q.GrantSnapshot.ServicePlanRevisionID, PackageRevID: q.PackageRevisionID,
			PolicySnapshot:     q.GrantSnapshot,
			TimeAccountingMode: q.GrantSnapshot.TimeAccountingMode,
			EndMode:            q.GrantSnapshot.EndMode,
			WindowEndsAt:       window,
			SupersedesID:       superseded,
		})
		if err != nil {
			return err
		}
		// 7. mark the Purchase GRANTED only now that the entitlement exists.
		if err := tx.MarkPurchaseGranted(ctx, pid); err != nil {
			return err
		}
		res = PurchaseResult{PurchaseID: pid, EntitlementID: eid, Superseded: superseded, Reason: "granted"}
		return nil
	})
	if err != nil {
		// FAIL CLOSED: the whole tx rolled back; no partial rows. Report a generic repo error.
		return PurchaseResult{}, &Error{Code: ErrRepo, Msg: "confirm"}
	}
	return res, nil
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
		"price_minor":            0,
		"currency":               pkg.Currency,
		"free":                   true,
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
