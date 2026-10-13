package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Phase-2 DARK guest-portal commerce handlers.
//
// INTERNAL UNIX-SOCKET API. scd listens ONLY on its Unix socket (main.go: net.Listen("unix", ...),
// chmod 0660, owned root:stayconnect). These commerce handlers are therefore never exposed on any
// guest-accessible TCP listener — the only TCP listener scd opens is the metrics endpoint, which serves
// exclusively /metrics + /healthz. The sole intended caller of these routes is portald (a member of the
// stayconnect group), which owns the browser-facing trust boundary; the guest browser can never reach
// this socket directly.
//
// These routes are mounted ONLY when the Phase-2 portal surface is ON (see route registration); in the
// dark deployment they are absent and the engine holds a nil repository, so zero Phase-2 SQL is issued.
//
// Trust boundary (WS-D/item 7): tenant and site are appliance-fixed server-side values (s.tenID /
// s.siteID) and are NEVER read from the request. The guest browser supplies only OPAQUE ids (package_id
// for a quote, quote_id for a confirm). portald resolves the authenticated subject's auth-context,
// iam_v2 device and guest-network from its own trusted session and forwards them; the browser never
// supplies them. A call missing any server-derived pin is rejected here before the engine runs. Deny
// reasons are logged server-side but returned only as a single generic "unavailable", so package /
// eligibility internals never leak.

type commerceQuoteReq struct {
	AuthContextID  string `json:"auth_context_id"`  // trusted (portald session), not browser-supplied
	DeviceID       string `json:"device_id"`        // trusted iam_v2 device id
	GuestNetworkID string `json:"guest_network_id"` // trusted guest-network id
	PackageID      string `json:"package_id"`       // opaque guest selection
	// Method is the acquisition method the client chose (NOT_REQUIRED, ONLINE_PAYMENT). Optional when the
	// package has exactly one applicable method. It is re-validated against every gate by the engine.
	Method string `json:"method,omitempty"`
}

type commerceConfirmReq struct {
	QuoteID        string `json:"quote_id"` // opaque quote handle from a prior quote
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
	// ReturnBase is the portal origin (scheme://host) the provider sends the client back to after a card
	// payment. The return page proves nothing; it only brings the client back to ask.
	ReturnBase string `json:"return_base,omitempty"`
}

// commercePackages lists the free packages the authenticated subject is eligible for right now. The
// trusted identifiers (auth-context / device / guest-network) arrive from portald as query parameters on
// the internal Unix-socket API; the guest browser never reaches this route. It creates no quote/purchase
// and never discloses an ineligible package.
func (s *server) commercePackages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// reject a call missing any server-derived pin (defense in depth; portald always supplies them)
	if q.Get("auth_context_id") == "" || q.Get("device_id") == "" || q.Get("guest_network_id") == "" {
		httpErr(w, http.StatusBadRequest, "unavailable")
		return
	}
	res, err := s.commerce.ListEligiblePackages(r.Context(), iamv2.PackageListRequest{
		TenantID:       s.tenID, // appliance-fixed; never from the request
		SiteID:         s.siteID,
		AuthContextID:  q.Get("auth_context_id"),
		DeviceID:       q.Get("device_id"),
		GuestNetworkID: q.Get("guest_network_id"),
	})
	if err != nil {
		slog.Error("phase2 list", "err", err)
		httpErr(w, http.StatusInternalServerError, "unavailable")
		return
	}
	if res.Disabled {
		httpErr(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if res.Reason != "ok" {
		slog.Info("phase2 list denied", "reason", res.Reason)
		httpErr(w, http.StatusConflict, "unavailable")
		return
	}
	items := res.Packages
	if items == nil {
		items = []iamv2.PackageListItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": items, "free_allowance_used": res.FreeAllowanceUsed})
}

// commerceQuote resolves a one-time free offer quote for the guest's package selection.
func (s *server) commerceQuote(w http.ResponseWriter, r *http.Request) {
	var req commerceQuoteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.AuthContextID == "" || req.DeviceID == "" || req.GuestNetworkID == "" || req.PackageID == "" {
		httpErr(w, http.StatusBadRequest, "unavailable")
		return
	}
	res, err := s.commerce.CreateQuote(r.Context(), iamv2.QuoteRequest{
		TenantID:       s.tenID, // appliance-fixed; never from the request
		SiteID:         s.siteID,
		AuthContextID:  req.AuthContextID,
		PackageID:      req.PackageID,
		DeviceID:       req.DeviceID,
		GuestNetworkID: req.GuestNetworkID,
		Method:         req.Method,
	})
	if err != nil {
		slog.Error("phase2 quote", "err", err)
		httpErr(w, http.StatusInternalServerError, "unavailable")
		return
	}
	if res.Disabled {
		httpErr(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if res.QuoteID == "" || res.Reason != "ok" {
		// generic guest error; the specific reason is server-side only.
		slog.Info("phase2 quote denied", "reason", res.Reason)
		httpErr(w, http.StatusConflict, "unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"quote_id":   res.QuoteID,
		"expires_at": res.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"display":    res.Display,
	})
}

// commerceConfirm consumes a quote and grants the free entitlement.
func (s *server) commerceConfirm(w http.ResponseWriter, r *http.Request) {
	var req commerceConfirmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.QuoteID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		httpErr(w, http.StatusBadRequest, "unavailable")
		return
	}
	res, err := s.commerce.ConfirmFreePurchase(r.Context(), iamv2.ConfirmRequest{
		TenantID:       s.tenID,
		SiteID:         s.siteID,
		QuoteID:        req.QuoteID,
		DeviceID:       req.DeviceID,
		GuestNetworkID: req.GuestNetworkID,
	})
	if err != nil {
		slog.Error("phase2 confirm", "err", err)
		httpErr(w, http.StatusInternalServerError, "unavailable")
		return
	}
	if res.Disabled {
		httpErr(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if res.AwaitingSettlement && res.PurchaseID != "" && res.Method == iamv2.AcquireCard {
		// CARD PAYMENT: nothing is granted. The provider checkout is started now; access follows only a
		// provider-verified capture (card_checkout.go).
		s.startCardCheckout(w, r, res.PurchaseID, res.SettlementID, req.ReturnBase, "")
		return
	}
	if res.Reason != "granted" || res.PurchaseID == "" {
		slog.Info("phase2 confirm denied", "reason", res.Reason)
		httpErr(w, http.StatusConflict, "unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purchase_id":    res.PurchaseID,
		"entitlement_id": res.EntitlementID,
		"method":         res.Method,
	})
}

type commerceRedeemReq struct {
	AuthContextID  string `json:"auth_context_id"`
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
}

// commerceRedeemVoucher redeems a voucher in one step: the client entered a code, so there is nothing to
// choose. The pinned revision is quoted and confirmed at once; the grant kernel burns the voucher in the
// same transaction that creates the entitlement.
func (s *server) commerceRedeemVoucher(w http.ResponseWriter, r *http.Request) {
	var req commerceRedeemReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.AuthContextID == "" || req.DeviceID == "" || req.GuestNetworkID == "" {
		httpErr(w, http.StatusBadRequest, "unavailable")
		return
	}
	q, err := s.commerce.CreateQuote(r.Context(), iamv2.QuoteRequest{
		TenantID: s.tenID, SiteID: s.siteID, AuthContextID: req.AuthContextID,
		DeviceID: req.DeviceID, GuestNetworkID: req.GuestNetworkID, Method: iamv2.AcquireVoucher,
	})
	if err != nil || q.Disabled || q.QuoteID == "" {
		slog.Info("voucher redeem: quote denied", "reason", q.Reason, "err", err)
		httpErr(w, http.StatusConflict, "unavailable")
		return
	}
	res, err := s.commerce.ConfirmPurchase(r.Context(), iamv2.ConfirmRequest{
		TenantID: s.tenID, SiteID: s.siteID, QuoteID: q.QuoteID, DeviceID: req.DeviceID, GuestNetworkID: req.GuestNetworkID,
	})
	if err != nil || res.Reason != "granted" || res.EntitlementID == "" {
		slog.Info("voucher redeem: confirm denied", "reason", res.Reason, "err", err)
		httpErr(w, http.StatusConflict, "unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purchase_id": res.PurchaseID, "entitlement_id": res.EntitlementID, "method": res.Method,
	})
}
