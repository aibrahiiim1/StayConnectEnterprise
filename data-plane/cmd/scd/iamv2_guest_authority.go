package main

// IAM-v2 GUEST AUTHORITY AT THE REAL ENTRY POINTS.
//
// THE DEFECT THIS FIXES
// ---------------------
// STAYCONNECT_IAMV2_MASTER=true with STAYCONNECT_IAMV2_VOUCHER=true and
// STAYCONNECT_IAMV2_ACCOUNT=true did NOT make IAM-v2 authoritative for guest
// authentication. The flags were read at startup, an *iamv2.Authenticator was
// constructed, and then the two real guest entry points -- /v1/sessions/authorize
// and /v1/sessions/authorize-credentials -- went on calling the legacy voucher
// and guest-account pipelines and writing public.sessions. Measured on the
// DEVELOPMENT appliance: redeeming a voucher moved public.sessions 24 -> 25 and
// left iam_v2.auth_contexts, iam_v2.devices, iam_v2.entitlements and
// iam_v2.sessions ALL unchanged, with iam_v2.auth_contexts at 0 rows.
//
// So "the flag is ON" and "the Authenticator was constructed" were both true
// while the authority was entirely legacy. Neither is evidence of authority.
//
// WHAT THIS FILE DOES
// -------------------
// It puts the decision in ONE place that both entry points consult, so the two
// cannot drift apart again, and it makes the enabled path fail closed rather
// than fall back. Three states, and only three:
//
//   method disabled  -> legacy behaviour, byte for byte unchanged.
//   method enabled   -> IAM-v2 is the authority. It establishes the auth_context
//                       and device in iam_v2 and the guest continues through the
//                       accepted commerce flow (eligible packages -> quote ->
//                       confirm -> purchase -> entitlement -> iam_v2.sessions).
//   method enabled
//   but IAM-v2 cannot
//   serve the request -> REFUSE. Never fall back to legacy.
//
// That last state is the whole point. A fallback is what produced the defect:
// it looks like success, it gives the guest Internet access, and it silently
// relocates the authority. Refusing is visibly wrong, which is what a
// misconfiguration should be.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO
// ---------------------------------------
// It does not bridge an IAM-v2 authentication result back into public.sessions
// to "make Internet access work". That would restore the symptom (a working
// guest) while preserving the defect (legacy session authority), and it would
// be indistinguishable from the bug in every measurement that matters.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// iamv2GuestResult is what a successful IAM-v2 guest authentication hands back
// to the portal. It is deliberately NOT a session: authentication establishes
// identity and device, and the commerce flow is what grants access. These are
// exactly the three trusted identifiers /v1/commerce/* requires.
type iamv2GuestResult struct {
	AuthContextID  string `json:"auth_context_id"`
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
	Method         string `json:"method"`
	// Authority names the domain that owns this result. It exists so a caller,
	// a test or an operator reading a response can tell which authority served
	// the request without having to infer it from a flag.
	Authority string `json:"authority"`
}

// iamv2MethodEnabled reports whether IAM-v2 is the configured authority for a
// method. It reads the SAME config the Authenticator was built from, so the
// switch here can never disagree with the Authenticator's own gate.
func (s *server) iamv2MethodEnabled(m iamv2.Method) bool {
	return s.iamv2Cfg.Enabled(m)
}

// authorizeViaIAMv2 runs the accepted IAM-v2 authentication domain and writes the
// JSON response. It returns true when it has fully handled the request (success
// or refusal); the caller must then return without touching the legacy path.
//
// Every refusal path here answers the guest with a deterministic, non-sensitive
// reason and NEVER continues to legacy.
func (s *server) authorizeViaIAMv2(w http.ResponseWriter, r *http.Request,
	method iamv2.Method, req iamv2.Request, ip net.IP) bool {

	// The guest network is server-derived from the source IP, never supplied by
	// the caller -- the same rule the commerce routes enforce on their trusted
	// identifiers.
	nc := s.resolveNetwork(r.Context(), ip)
	if nc.NetworkID == "" {
		// A guest whose IP maps to no enabled guest_network cannot be placed in
		// the IAM-v2 device/network model. Legacy tolerated this via a bridge
		// fallback; IAM-v2 must not invent a network, so this refuses.
		slog.Warn("iamv2 guest auth: no guest network for source ip",
			"method", string(method), "ip", ip.String())
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "NO_GUEST_NETWORK", "authority": "iam_v2",
		})
		return true
	}
	req.Method = method
	req.TenantID = s.tenID
	req.SiteID = s.siteID
	req.Device.GuestNetworkID = nc.NetworkID
	req.Device.IP = ip.String()
	req.Device.ApplianceID = s.applianceID

	res, err := s.iamv2Auth.Authenticate(r.Context(), req)
	if err != nil {
		// A configuration or repository error while the method is ENABLED is
		// exactly the case that must not degrade to legacy: the operator asked
		// for IAM-v2 authority and it could not be provided.
		slog.Error("iamv2 guest auth failed", "method", string(method), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2",
		})
		return true
	}
	switch res.Decision {
	case iamv2.DecisionAllow:
		if res.AuthContextID == "" || res.DeviceID == "" {
			// Allow without the state the commerce flow needs is not a usable
			// success; treating it as one would hand the guest a dead end.
			slog.Error("iamv2 allow without auth context/device",
				"method", string(method), "auth_context", res.AuthContextID != "", "device", res.DeviceID != "")
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "IAMV2_INCOMPLETE", "authority": "iam_v2",
			})
			return true
		}
		s.met.SessionsStarted.WithLabelValues(string(method) + "_iamv2").Inc()
		writeJSON(w, http.StatusOK, iamv2GuestResult{
			AuthContextID:  res.AuthContextID,
			DeviceID:       res.DeviceID,
			GuestNetworkID: nc.NetworkID,
			Method:         string(method),
			Authority:      "iam_v2",
		})
		return true

	case iamv2.DecisionDisabled:
		// Unreachable: the caller only routes here when the method is enabled.
		// If it ever happens the config and the Authenticator have disagreed,
		// which is a fail-closed condition, not a reason to serve from legacy.
		slog.Error("iamv2 reported disabled for an enabled method", "method", string(method))
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2",
		})
		return true

	default: // DecisionDeny
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "AUTH_DENIED", "reason": res.Reason, "authority": "iam_v2",
		})
		return true
	}
}

// decodeGuestAuthBody is shared by the two entry points so their parsing (and
// therefore their error behaviour) cannot drift.
func decodeGuestAuthBody(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

var _ = context.Background // keep context imported for future use by callers
