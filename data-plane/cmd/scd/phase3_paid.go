package main

// PAID ACQUISITION FOR A VERIFIED STAY: PMS Room Charge and Card payment.
//
// A client who proved their stay by Room sign-in is offered priced packages alongside the included ones, each
// with only the methods that can succeed for THIS stay right now:
//
//	Room charge   room_charge effective; the package maps a posting code on the stay's interface; that interface
//	              is financially onboarded; package currency == interface currency (no FX); the stay is
//	              IN_HOUSE and posting_allowed (it has a reservation number and no posting block); it has no
//	              unresolved room charge; every freshness axis is green. The charge targets the reservation
//	              (RN + G#), never a folio (Phase-0 Amendment A1).
//	Card payment  card_payment effective (provider ready).
//
// Choosing one consumes the verified auth context and writes quote, purchase (AWAITING_SETTLEMENT) and a
// REQUIRED settlement in ONE transaction; for a room charge the posting is created in that same transaction by
// iam_v2.p4_create_room_charge_posting (evidence re-derived from the purchase; the insert gates decide).
// NOTHING IS GRANTED HERE: access follows a PA=OK or a provider-verified capture.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
	"github.com/stayconnect/enterprise/data-plane/internal/staygrant"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// stayMethods returns the acquisition methods this verified stay may use for one offered package now.
func (p *phase3Auth) stayMethods(ctx context.Context, stayID, ifaceID string, d offerDecision) []string {
	if d.PriceMinor == 0 {
		if containsString(d.SettlementMethods, iamv2.AcquireFree) {
			return []string{iamv2.AcquireFree}
		}
		return nil
	}
	site := p.srv.siteAcquisitionMethods(ctx)
	if !site.PaidAccess {
		return nil
	}
	var out []string
	if site.RoomCharge && containsString(d.SettlementMethods, iamv2.AcquireRoomCharge) &&
		p.roomChargeApplicable(ctx, stayID, ifaceID, d) {
		out = append(out, iamv2.AcquireRoomCharge)
	}
	if site.Card && containsString(d.SettlementMethods, iamv2.AcquireCard) {
		out = append(out, iamv2.AcquireCard)
	}
	return out
}

func containsString(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// roomChargeApplicableSQL is every room-charge precondition in one statement, run as svc_scd. Everything it
// reads is either a table svc_scd may read or a narrow definer (financial readiness, the open-charge check and
// the interface freshness reader): svc_scd must not read pms_interface_runtime itself.
const roomChargeApplicableSQL = `
		SELECT COALESCE((SELECT r.ready AND r.currency = $5 AND r.currency_exponent = $6
		                   FROM iam_v2.pms_interface_financially_ready($1,$2,$3) r), false)
		   AND EXISTS (SELECT 1 FROM iam_v2.package_settlement_mappings m
		                WHERE m.package_revision_id=$7 AND m.pms_interface_id=$3 AND m.retired_at IS NULL)
		   AND EXISTS (SELECT 1 FROM iam_v2.stays st
		                WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.id=$4 AND st.pms_interface_id=$3
		                  AND st.status='IN_HOUSE' AND st.posting_allowed)
		   AND NOT iam_v2.p4_stay_room_charge_open($4::uuid)
		   AND iam_v2.p4_room_charge_interface_fresh($1,$2,$3)`

// roomChargeApplicable asks the database every room-charge precondition in one statement. Any error is "no".
func (p *phase3Auth) roomChargeApplicable(ctx context.Context, stayID, ifaceID string, d offerDecision) bool {
	var ok bool
	err := p.srv.db.QueryRow(ctx, roomChargeApplicableSQL,
		p.srv.tenID, p.srv.siteID, ifaceID, stayID, d.Currency, d.CurrencyExponent, d.PackageRevisionID).Scan(&ok)
	if err != nil {
		slog.Info("room charge: applicability not established", "err", err)
		return false
	}
	return ok
}

// paidPurchase handles a grant request whose chosen method is a paid one. The offer has already been locked
// and its evidence re-checked by grantHandler.
func (p *phase3Auth) paidPurchase(w http.ResponseWriter, r *http.Request, tx pgx.Tx, req phase3GrantReq, dev deviceIdentity,
	offeredTier *int) (signinattempt.Result, bool) {
	ctx := r.Context()
	acID := strings.TrimSpace(req.AuthContextID)
	pkgRev := strings.TrimSpace(req.PackageRevID)

	// The package, as the offer saw it -- re-read under the transaction.
	var d offerDecision
	var svcRev, pkgType string
	var duration, alloc []byte
	if err := tx.QueryRow(ctx, `
		SELECT ipr.id::text, ip.id::text, ipr.service_plan_revision_id::text, ipr.package_type, ipr.duration_policy,
		       ipr.data_allocation_policy, ipr.price_minor, COALESCE(ipr.currency,''), COALESCE(ipr.currency_exponent,2),
		       ipr.settlement_methods
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3 AND ip.current_revision_id=ipr.id AND ip.active`,
		p.srv.tenID, p.srv.siteID, pkgRev).Scan(&d.PackageRevisionID, &d.PackageID, &svcRev, &pkgType, &duration,
		&alloc, &d.PriceMinor, &d.Currency, &d.CurrencyExponent, &d.SettlementMethods); err != nil {
		notVerified(w, signinattempt.VerifiedNoEligiblePackage, "paid: package not grantable")
		return signinattempt.VerifiedNoEligiblePackage, false
	}
	d.Currency = strings.TrimSpace(d.Currency)

	// THE METHOD, re-validated now: the page's earlier state is never trusted.
	var stayID, ifaceID string
	if err := tx.QueryRow(ctx, `SELECT stay_id::text, pms_interface_id::text FROM iam_v2.auth_contexts
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`, p.srv.tenID, p.srv.siteID, acID).Scan(&stayID, &ifaceID); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: context unreadable")
		return signinattempt.ServiceUnavailable, false
	}
	if !containsString(p.stayMethods(ctx, stayID, ifaceID, d), req.Method) {
		notVerified(w, signinattempt.VerifiedNoEligiblePackage, "paid: method not available now")
		return signinattempt.VerifiedNoEligiblePackage, false
	}

	if err := writerguard.Open(ctx, tx, writerguard.CapCommerceIntent); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: writer scope")
		return signinattempt.ServiceUnavailable, false
	}
	// CONSUME the verified context exactly once, against its full pin set.
	consumed, err := authctx.NewStore(p.srv.db).ConsumeTx(ctx, tx, acID, authctx.Presenter{
		Tenant: p.srv.tenID, Site: p.srv.siteID, Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork})
	if err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: consume: "+err.Error())
		return signinattempt.ServiceUnavailable, false
	}

	// THE SNAPSHOT: the same typed snapshot the commerce engine builds, with the stay grant's own end shape
	// and frozen per-night allowance.
	plan, err := p.planRevision(ctx, tx, svcRev)
	if err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: plan")
		return signinattempt.ServiceUnavailable, false
	}
	tier := iamv2.GrantTier{Value: map[string]any{}}
	if offeredTier != nil {
		if tiers, terr := p.grantTiers(ctx, pkgRev); terr == nil {
			for _, t := range tiers {
				if t.Order == *offeredTier {
					tier = t
				}
			}
		}
	}
	snap, err := iamv2.BuildGrantSnapshot(tier, plan, iamv2.PackageRevisionRow{ID: pkgRev, PackageID: d.PackageID,
		PlanRevisionID: svcRev, PackageType: pkgType, PriceMinor: d.PriceMinor, Currency: d.Currency,
		CurrencyExponent: d.CurrencyExponent, SettlementMethods: d.SettlementMethods})
	if err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: snapshot")
		return signinattempt.ServiceUnavailable, false
	}
	endMode, windowSecs := staygrant.GrantShape(duration)
	snap.EndMode = endMode
	if windowSecs > 0 {
		snap.WindowEndsAt = time.Now().UTC().Add(time.Duration(windowSecs) * time.Second).Format(time.RFC3339)
	}
	if q, qerr := p.grants.EffectiveDataQuota(ctx, tx, p.srv.tenID, p.srv.siteID, stayID, alloc); qerr != nil {
		notVerified(w, signinattempt.StayNotEligible, "paid: stay length")
		return signinattempt.StayNotEligible, false
	} else if q != nil {
		snap.FrozenDataQuotaBytes = *q
	}
	snap.AcquisitionMethod = req.Method

	var mapping *string
	if req.Method == iamv2.AcquireRoomCharge {
		var m string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.package_settlement_mappings
			WHERE package_revision_id=$1 AND pms_interface_id=$2 AND retired_at IS NULL`, pkgRev, consumed.Interface).Scan(&m); err != nil {
			notVerified(w, signinattempt.VerifiedNoEligiblePackage, "paid: no mapping")
			return signinattempt.VerifiedNoEligiblePackage, false
		}
		mapping = &m
	}
	var quoteID, purchaseID, settlementID string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.offer_quotes
		(tenant_id, site_id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id,
		 price_minor, currency, currency_exponent, grant_snapshot, expires_at, consumed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb, now()+interval '5 minutes', now()) RETURNING id::text`,
		p.srv.tenID, p.srv.siteID, acID, pkgRev, consumed.Interface, mapping, d.PriceMinor, d.Currency,
		d.CurrencyExponent, string(snap.Canonical())).Scan(&quoteID); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: quote: "+err.Error())
		return signinattempt.ServiceUnavailable, false
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, offer_quote_id, auth_context_id, pms_interface_id, stay_id,
		 settlement_mapping_id, authentication_interface_revision_id, trigger, amount_minor, currency,
		 currency_exponent, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'GUEST_SELECTION',$10,$11,$12,'AWAITING_SETTLEMENT') RETURNING id::text`,
		p.srv.tenID, p.srv.siteID, pkgRev, quoteID, acID, consumed.Interface, consumed.Stay, mapping, consumed.Revision,
		d.PriceMinor, d.Currency, d.CurrencyExponent).Scan(&purchaseID); err != nil {
		if strings.Contains(err.Error(), "purchase_once_per_stay") {
			notVerified(w, signinattempt.StayNotEligible, "paid: already purchased for this stay")
			return signinattempt.StayNotEligible, false
		}
		notVerified(w, signinattempt.ServiceUnavailable, "paid: purchase: "+err.Error())
		return signinattempt.ServiceUnavailable, false
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.settlements (tenant_id, site_id, purchase_id, method, status)
		VALUES ($1,$2,$3,$4,'REQUIRED') RETURNING id::text`, p.srv.tenID, p.srv.siteID, purchaseID, req.Method).Scan(&settlementID); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: settlement: "+err.Error())
		return signinattempt.ServiceUnavailable, false
	}
	if req.Method == iamv2.AcquireRoomCharge {
		var posting string
		if err := tx.QueryRow(ctx, `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)::text`,
			p.srv.tenID, p.srv.siteID, settlementID).Scan(&posting); err != nil {
			// The insert gates refused (folio, currency, stay, freshness): nothing is committed.
			notVerified(w, signinattempt.VerifiedNoEligiblePackage, "paid: posting refused: "+err.Error())
			return signinattempt.VerifiedNoEligiblePackage, false
		}
	}
	if err := tx.Commit(ctx); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "paid: commit")
		return signinattempt.ServiceUnavailable, false
	}
	resp := phase3PaidResp{Outcome: "PENDING", PurchaseID: purchaseID, Method: req.Method}
	if req.Method == iamv2.AcquireCard {
		base := portalBase(req.ReturnBase)
		if p.srv.card == nil || p.srv.card.engine == nil || base == "" {
			resp.State = "failed"
		} else {
			ret := base + "/pay/return?p=" + purchaseID
			st, cerr := p.srv.card.engine.StartCheckout(ctx, p.srv.tenID, p.srv.siteID, settlementID, "Internet access", ret, ret+"&cancelled=1")
			switch {
			case cerr == nil:
				resp.RedirectURL = st.RedirectURL
			case errors.Is(cerr, payment.ErrCheckoutAmbiguous):
			default:
				resp.State = "failed"
			}
		}
	}
	writeJSONScd(w, http.StatusOK, resp)
	return signinattempt.Verified, true
}

type phase3PaidResp struct {
	Outcome     string `json:"outcome"` // PENDING
	PurchaseID  string `json:"purchase_id"`
	Method      string `json:"method"`
	RedirectURL string `json:"redirect_url,omitempty"`
	State       string `json:"state,omitempty"`
}

// planRevision reads the plan revision the snapshot is built from.
func (p *phase3Auth) planRevision(ctx context.Context, tx pgx.Tx, id string) (iamv2.PlanRevisionRow, error) {
	var r iamv2.PlanRevisionRow
	var tq, dq *int64
	err := tx.QueryRow(ctx, `SELECT id::text, COALESCE(down_kbps,0), COALESCE(up_kbps,0), COALESCE(max_concurrent_devices,1),
		time_quota_seconds, data_quota_bytes, COALESCE(time_accounting_mode,'VALIDITY_WINDOW')
		FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		p.srv.tenID, p.srv.siteID, id).Scan(&r.ID, &r.DownKbps, &r.UpKbps, &r.MaxConcurrentDevices, &tq, &dq, &r.TimeAccountingMode)
	if tq != nil {
		r.TimeQuotaSeconds = *tq
	}
	if dq != nil {
		r.DataQuotaBytes = *dq
	}
	return r, err
}

var _ = json.Marshal
