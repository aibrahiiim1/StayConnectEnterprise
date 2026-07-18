package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Phase-2 DARK guest-portal commerce handlers. These routes are mounted ONLY when the Phase-2 portal
// surface is ON (see route registration); in the dark deployment they are absent (404) and the engine
// holds a nil repository, so zero Phase-2 SQL is ever issued.
//
// Trust boundary (WS-D): tenant and site are appliance-fixed server-side values (s.tenID / s.siteID) and
// are NEVER read from the request. The guest browser supplies only OPAQUE ids (package_id for a quote,
// quote_id for a confirm). portald — scd's trusted server-side caller — resolves the authenticated
// subject's auth-context, iam_v2 device and guest-network from its own session and forwards them; the
// browser never supplies them. Deny reasons are logged server-side but returned to the guest only as a
// single generic "unavailable", so package/eligibility internals never leak.

type commerceQuoteReq struct {
	AuthContextID  string `json:"auth_context_id"`  // trusted (portald session), not browser-supplied
	DeviceID       string `json:"device_id"`        // trusted iam_v2 device id
	GuestNetworkID string `json:"guest_network_id"` // trusted guest-network id
	PackageID      string `json:"package_id"`       // opaque guest selection
}

type commerceConfirmReq struct {
	QuoteID        string `json:"quote_id"` // opaque quote handle from a prior quote
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
}

// commerceQuote resolves a one-time free offer quote for the guest's package selection.
func (s *server) commerceQuote(w http.ResponseWriter, r *http.Request) {
	var req commerceQuoteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	res, err := s.commerce.CreateQuote(r.Context(), iamv2.QuoteRequest{
		TenantID:       s.tenID, // appliance-fixed; never from the request
		SiteID:         s.siteID,
		AuthContextID:  req.AuthContextID,
		PackageID:      req.PackageID,
		DeviceID:       req.DeviceID,
		GuestNetworkID: req.GuestNetworkID,
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
	if res.Reason != "granted" || res.PurchaseID == "" {
		slog.Info("phase2 confirm denied", "reason", res.Reason)
		httpErr(w, http.StatusConflict, "unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purchase_id":    res.PurchaseID,
		"entitlement_id": res.EntitlementID,
	})
}
