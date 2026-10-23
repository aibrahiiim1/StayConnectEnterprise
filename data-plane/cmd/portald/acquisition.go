package main

// Acquisition in the client portal. Nothing the browser says is proof: the device is identified server-side from the connection, and the card return page only asks scd, which asks the provider.

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	resumeCookie     = "og_resume"
	resumeCookieLife = 30 * 24 * time.Hour
)

func (h *handler) authOpen(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, msgServiceError)
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, msgNoDeviceAddress)
		return
	}
	// The MAC comes from the kernel neighbour table and the kernel is asked to resolve it (arp_resolve.go): a cold cache is not evidence that a device is off the guest network.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, msgDeviceNotOnNetwork)
		return
	}
	body := map[string]any{"ip": ip.String(), "mac": mac.String()}
	if code := strings.TrimSpace(r.FormValue("return_code")); code != "" {
		if len(code) > 20 {
			h.landing(w, r, msgReturnCodeUnknown)
			return
		}
		// A typed code goes alone: scd resolves the resume cookie before a recovery code, so sending both let someone else's cookie win.
		body["recovery_code"] = code
	} else if c, err := r.Cookie(resumeCookie); err == nil && c.Value != "" && len(c.Value) < 100 {
		body["resume_token"] = c.Value
	}
	raw, _ := json.Marshal(body)
	resp, err := h.scdDo(r.Context(), http.MethodPost, "/v1/sessions/authorize-open", raw)
	if err != nil {
		h.landing(w, r, msgServiceError)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		switch e.Error {
		case "METHOD_DISABLED":
			h.landing(w, r, msgMethodDisabled)
		case "RECOVERY_CODE_UNKNOWN":
			h.landing(w, r, msgReturnCodeUnknown)
		case "TOO_MANY_ATTEMPTS":
			h.landing(w, r, msgTooManyAttempts)
		case "NO_GUEST_NETWORK":
			h.landing(w, r, msgDeviceNotOnNetwork)
		default:
			if isLicenceRefusal(e.Error) {
				h.landing(w, r, guestLicenseRefusedMessage)
				return
			}
			h.landing(w, r, msgServiceError)
		}
		return
	}
	var reply struct {
		ResumeToken  string `json:"resume_token"`
		RecoveryCode string `json:"recovery_code"`
	}
	_ = json.Unmarshal(b, &reply)
	if reply.ResumeToken != "" {
		// og_resume, not og_client: the anonymous subject's token is bound server-side to this device and lives as long as the credential.
		setPortalCookie(w, r, resumeCookie, reply.ResumeToken, resumeCookieLife)
	}
	if !h.tryIAMv2Auth(w, r, b) {
		h.landing(w, r, msgServiceError)
	}
}

func (h *handler) redeemVoucher(w http.ResponseWriter, r *http.Request, sess commerceSession, token string) bool {
	raw, _ := json.Marshal(map[string]any{"auth_context_id": sess.authContextID, "device_id": sess.deviceID,
		"guest_network_id": sess.guestNetworkID})
	resp, err := h.scdDo(r.Context(), http.MethodPost, "/v1/commerce/redeem", raw)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		EntitlementID string `json:"entitlement_id"`
		Error         string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode == http.StatusTooManyRequests || out.Error == "TOO_MANY_ATTEMPTS" {
		// A throttled voucher is not an invalid one: tell the client to wait.
		h.landing(w, r, msgTooManyAttempts)
		return true
	}
	if resp.StatusCode != http.StatusOK || out.EntitlementID == "" {
		return false
	}
	sid, failure := h.activateEnforced(r, sess, out.EntitlementID)
	switch failure {
	case "":
		http.Redirect(w, r, "/success?s="+url.QueryEscape(sid), http.StatusSeeOther)
	case activateDeviceLimit:
		// Every place is taken: show the shared replacement page (device_pages.go); with no session store there is nothing to choose from.
		if h.commerceSessions != nil && token != "" {
			h.toReplace(w, r, sess, token, false)
			return true
		}
		h.landing(w, r, msgVoucherDevices)
	case activateNoDevice:
		h.landing(w, r, msgDeviceNotOnNetwork)
	case activateCapacity:
		h.landing(w, r, guestCapacityMessage)
	case activateLicense:
		h.landing(w, r, guestLicenseRefusedMessage)
	default:
		h.landing(w, r, "We could not bring your device online. Please try again in a moment.")
	}
	return true
}

type payStatus struct {
	State                 string `json:"state"`
	EntitlementID         string `json:"entitlement_id"`
	DeviceID              string `json:"device_id"`
	Method                string `json:"method"`
	CheckoutExpirySeconds int    `json:"checkout_expiry_seconds"`
}

func (h *handler) purchaseState(r *http.Request, purchaseID string) (payStatus, bool) {
	ip := clientIP(r)
	if ip == nil {
		return payStatus{}, false
	}
	// The MAC comes from the kernel neighbour table and the kernel is asked to resolve it (arp_resolve.go).
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		return payStatus{}, false
	}
	raw, _ := json.Marshal(map[string]any{"purchase_id": purchaseID, "ip": ip.String(), "mac": mac.String()})
	resp, err := h.scdDo(r.Context(), http.MethodPost, "/v1/commerce/purchase-status", raw)
	if err != nil {
		return payStatus{}, false
	}
	defer resp.Body.Close()
	var st payStatus
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &st) != nil {
		return payStatus{}, false
	}
	return st, true
}

func validPurchaseID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'):
		default:
			return false
		}
	}
	return true
}

// payReturn: the provider's return page polls /api/pay/status; scd decides whether the client paid.
func (h *handler) payReturn(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	if !validPurchaseID(p) {
		h.landing(w, r, msgServiceError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	pg := h.newGuestPage(r, nonce, "pay.", "room.", "err.connect")
	v := payView{guestPage: pg, PurchaseID: p, Cancelled: r.URL.Query().Get("cancelled") == "1",
		Room: r.URL.Query().Get("m") == "room"}
	// The return code of a client who chose without signing in is shown here from the server-held session, never the URL.
	if sess, ok := h.resolveCommerceSession(r); ok {
		v.ReturnCode = sess.returnCode
	}
	_ = payTmpl.Execute(w, v)
}

// payStatusAPI activates this device when granted, else reports pending, failed or review.
func (h *handler) payStatusAPI(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	if !validPurchaseID(p) {
		writeJSONPortal(w, http.StatusBadRequest, map[string]any{"state": "unknown"})
		return
	}
	st, ok := h.purchaseState(r, p)
	if !ok {
		writeJSONPortal(w, http.StatusOK, map[string]any{"state": "unknown"})
		return
	}
	if st.State != "granted" || st.EntitlementID == "" {
		if st.State == "failed" {
			h.updateCommerceSession(r, func(s *commerceSession) {
				if s.purchaseID == p {
					s.purchaseID = ""
				}
			})
		}
		out := map[string]any{"state": st.State}
		if st.CheckoutExpirySeconds > 0 {
			out["checkout_expiry_seconds"] = st.CheckoutExpirySeconds
		}
		writeJSONPortal(w, http.StatusOK, out)
		return
	}
	sid, failure := h.activateEnforced(r, commerceSession{deviceID: st.DeviceID}, st.EntitlementID)
	if failure != "" {
		writeJSONPortal(w, http.StatusOK, map[string]any{"state": "granted", "activation": failure})
		return
	}
	// The journey is finished: "/" no longer sends this client to the package list or this page.
	h.updateCommerceSession(r, func(s *commerceSession) { s.acquired, s.purchaseID = true, "" })
	writeJSONPortal(w, http.StatusOK, map[string]any{"state": "connected", "redirect": "/success?s=" + url.QueryEscape(sid)})
}

type payView struct {
	guestPage
	PurchaseID string
	Cancelled  bool
	Room       bool
	ReturnCode string
}

var payTmpl = template.Must(template.New("pay").Parse(compactMarkup(payHTML)))
