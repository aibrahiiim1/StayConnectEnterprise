package main

// IAM-v2 is the guest authority at the real entry points. When a method is enabled but IAM-v2 cannot serve the request, REFUSE:
// a fallback looks like success, gives the guest Internet and silently relocates the authority. Never bridge an IAM-v2 result into
// public.sessions to make access work; that restores the symptom while preserving the defect.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// liveEntitlementForContext returns the ACTIVE, in-window entitlement held by the subject of this auth context
// (voucher, account or guest principal), or "". A read failure answers "" -- the guest is offered packages as
// before, and a purchase for a subject that does hold a live grant still supersedes it correctly.
func (s *server) liveEntitlementForContext(ctx context.Context, authContextID string) string {
	if s.db == nil || authContextID == "" {
		return ""
	}
	var id string
	err := s.db.QueryRow(ctx, `
	    SELECT e.id::text
	      FROM iam_v2.auth_contexts ac
	      JOIN iam_v2.entitlements e
	        ON e.tenant_id = ac.tenant_id AND e.site_id = ac.site_id
	       AND (e.voucher_id = ac.voucher_id OR e.guest_account_id = ac.guest_account_id
	            OR e.guest_principal_id = ac.guest_principal_id
	            OR e.anonymous_subject_id = ac.anonymous_subject_id)
	     WHERE ac.id = $1 AND ac.tenant_id = $2 AND ac.site_id = $3
	       AND e.status = 'ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now())
	     LIMIT 1`, authContextID, s.tenID, s.siteID).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("iamv2: live entitlement lookup failed", "err", err)
	}
	return id
}

type iamv2GuestResult struct {
	AuthContextID  string `json:"auth_context_id"`
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
	Method         string `json:"method"`
	// Authority names the domain that served the request.
	Authority string `json:"authority"`
	// LiveEntitlementID is set when the subject already holds an ACTIVE entitlement; the portal joins this device to it (within its
	// device limit) instead of offering a purchase, so a new grant does not supersede it and knock other devices offline.
	LiveEntitlementID string `json:"live_entitlement_id,omitempty"`
	// ResumeToken is the remembered-device credential (client_resume.go) the portal stores in an HttpOnly cookie.
	ResumeToken string `json:"resume_token,omitempty"`
	// ResumeDays is the Remember-devices setting the credential was issued under, so the portal cookie lives as long as the credential.
	ResumeDays int `json:"resume_days,omitempty"`
	// IdentityLabel is the masked factor the Client proved (a•••@example.com), for guest pages only.
	IdentityLabel   string `json:"identity_label,omitempty"`
	ClientGroupName string `json:"client_group_name,omitempty"`
	// AccountToken and Account (self-registered Email account only): proof of a recent password sign-in on this device and the
	// verification state. A suspended account still authenticates; activation refuses it.
	AccountToken string              `json:"account_token,omitempty"`
	Account      *selfServiceAccount `json:"account,omitempty"`
}

// iamv2MethodEnabled reads the same config the Authenticator was built from, so the switch cannot disagree with its gate.
func (s *server) iamv2MethodEnabled(m iamv2.Method) bool {
	return s.iamv2Cfg.Enabled(m)
}

// authorizeViaIAMv2 writes the JSON response and returns true when it fully handled the request; every refusal is
// deterministic and non-sensitive, and never continues to legacy.
func (s *server) authorizeViaIAMv2(w http.ResponseWriter, r *http.Request,
	method iamv2.Method, req iamv2.Request, ip net.IP) bool {

	// The guest network is server-derived from the source IP, never from the caller.
	nc := s.resolveNetwork(r.Context(), ip)
	if nc.NetworkID == "" {
		// A source IP mapping to no enabled guest_network cannot be placed in the IAM-v2 model; refuse rather than invent a network.
		slog.Warn("iamv2 guest auth: no guest network for source ip",
			"method", string(method), "ip", ip.String())
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "NO_GUEST_NETWORK", "authority": "iam_v2",
		})
		return true
	}
	keys := methodKeys(method)
	if k, ok := r.Context().Value(methodKeyCtxKey{}).(string); ok {
		keys = []string{k}
	}
	if !s.networkOffers(r.Context(), nc.NetworkID, keys...) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_NOT_OFFERED_ON_NETWORK", "authority": "iam_v2"})
		return true
	}
	r = r.WithContext(withNetwork(r.Context(), nc.NetworkID))
	req.Method = method
	req.TenantID = s.tenID
	req.SiteID = s.siteID
	req.Device.GuestNetworkID = nc.NetworkID
	req.Device.IP = ip.String()
	req.Device.ApplianceID = s.applianceID

	res, err := s.iamv2Auth.Authenticate(r.Context(), req)
	if err != nil && iamv2.CodeOf(err) == iamv2.ErrBusy {
		// AD-35: not a wrong credential and not an outage. The same answer scd gives when it is at capacity.
		w.Header().Set("Retry-After", strconv.Itoa(guestBusyRetryAfter))
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "BUSY", "retry_after": guestBusyRetryAfter})
		return true
	}
	if err != nil {
		// A config or repository error on an ENABLED method must not degrade to legacy.
		slog.Error("iamv2 guest auth failed", "method", string(method), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2",
		})
		return true
	}
	switch res.Decision {
	case iamv2.DecisionAllow:
		if res.AuthContextID == "" || res.DeviceID == "" {
			// Allow without the state the commerce flow needs is a dead end, not a success.
			slog.Error("iamv2 allow without auth context/device",
				"method", string(method), "auth_context", res.AuthContextID != "", "device", res.DeviceID != "")
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "IAMV2_INCOMPLETE", "authority": "iam_v2",
			})
			return true
		}
		// Not SessionsStarted: no session exists yet, and counting it unbalanced started/closed.
		if s.met != nil && s.met.AuthContextsCreated != nil {
			s.met.AuthContextsCreated.WithLabelValues(string(method)).Inc()
		}
		out := iamv2GuestResult{
			AuthContextID:     res.AuthContextID,
			DeviceID:          res.DeviceID,
			GuestNetworkID:    nc.NetworkID,
			Method:            string(method),
			Authority:         "iam_v2",
			LiveEntitlementID: s.liveEntitlementForContext(r.Context(), res.AuthContextID),
			IdentityLabel:     res.IdentityLabel,
			ClientGroupName:   res.ClientGroupName,
		}
		// A verified factor earns a resume credential (contract 2.3); a resumed sign-in already holds one.
		// A code staff showed the client earns none (contract 6.7).
		if req.ResumedPrincipalID == "" && res.Subject.PrincipalID != "" && !iamv2.IsAssistedCredential(req.Credential) {
			out.ResumeToken = s.issueResumeCredential(r.Context(), res.Subject.PrincipalID, res.DeviceID, method, req.Credential)
			if out.ResumeToken != "" {
				out.ResumeDays = s.rememberDeviceDays(r.Context())
			}
		}
		// A self-registered Email account carries its verification state and recent-sign-in proof; a staff-created one has no address to verify.
		if method == iamv2.MethodAccount && res.Subject.GuestAccountID != "" {
			s.decorateSelfServiceResult(r.Context(), &out, res.Subject.GuestAccountID, req.Device.MAC)
		}
		if len(res.IdentityConflicts) > 0 {
			slog.Info("iamv2 identity link conflict", "method", string(method), "factors", res.IdentityConflicts)
		}
		writeJSON(w, http.StatusOK, out)
		return true

	case iamv2.DecisionDisabled:
		// Unreachable: the config and the Authenticator disagreeing is a fail-closed condition, not a reason to serve from legacy.
		slog.Error("iamv2 reported disabled for an enabled method", "method", string(method))
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2",
		})
		return true

	default: // DecisionDeny
		out := map[string]any{"error": "AUTH_DENIED", "reason": res.Reason, "authority": "iam_v2"}
		if res.Reason == "assisted_existing_client" {
			out["key"] = "err.otp.assisted" // the portal says it in the client's language
		}
		writeJSON(w, http.StatusForbidden, out)
		return true
	}
}

var _ = context.Background // keep context imported for future use by callers
