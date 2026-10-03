package main

// ACQUISITION IN THE CLIENT PORTAL: open package selection, one-step voucher redemption, card payment and the
// payment return page.
//
// Nothing the browser says is proof of anything. The return page after a card payment only brings the client
// back to ASK; scd asks the provider. The device is always identified server-side from the connection (source
// IP -> ARP), never from the page, which is also why the return page works when a captive mini-browser has
// dropped every cookie.

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

// ---- open package selection ----------------------------------------------------------------------------------

// authOpen serves POST /auth/open: "Continue without signing in", optionally with a return code.
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
	// The hardware address comes from the kernel's neighbour table, never from the client, and the kernel is
	// ASKED to resolve it rather than only consulted (arp_resolve.go): a cold cache is not evidence that a
	// device is off the guest network. Same mechanism, same guarantee, on every sign-in path.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, msgDeviceNotOnNetwork)
		return
	}
	body := map[string]any{"ip": ip.String(), "mac": mac.String()}
	if c, err := r.Cookie(resumeCookie); err == nil && c.Value != "" && len(c.Value) < 100 {
		body["resume_token"] = c.Value
	}
	if code := strings.TrimSpace(r.FormValue("return_code")); code != "" {
		if len(code) > 20 {
			h.landing(w, r, msgReturnCodeUnknown)
			return
		}
		body["recovery_code"] = code
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
		http.SetCookie(w, &http.Cookie{Name: resumeCookie, Value: reply.ResumeToken, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(resumeCookieLife)})
	}
	if !h.tryIAMv2Auth(w, r, b) {
		h.landing(w, r, msgServiceError)
	}
}

// ---- voucher redemption ------------------------------------------------------------------------------------

// redeemVoucher redeems a voucher in one step and activates the device. It returns false when redemption did
// not produce an entitlement (the caller shows the voucher error).
func (h *handler) redeemVoucher(w http.ResponseWriter, r *http.Request, sess commerceSession) bool {
	raw, _ := json.Marshal(map[string]any{"auth_context_id": sess.authContextID, "device_id": sess.deviceID,
		"guest_network_id": sess.guestNetworkID})
	resp, err := h.scdDo(r.Context(), http.MethodPost, "/v1/commerce/redeem", raw)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		EntitlementID string `json:"entitlement_id"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out.EntitlementID == "" {
		return false
	}
	sid, failure := h.activateEnforced(r, sess, out.EntitlementID)
	switch failure {
	case "":
		http.Redirect(w, r, "/success?s="+url.QueryEscape(sid), http.StatusSeeOther)
	case activateDeviceLimit:
		h.landing(w, r, "This voucher has reached its device limit. Disconnect another device and try again.")
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

// ---- card payment return -----------------------------------------------------------------------------------

type payStatus struct {
	State         string `json:"state"`
	EntitlementID string `json:"entitlement_id"`
	DeviceID      string `json:"device_id"`
	Method        string `json:"method"`
}

// purchaseState asks scd where a purchase stands, for THIS device (server-derived identity).
func (h *handler) purchaseState(r *http.Request, purchaseID string) (payStatus, bool) {
	ip := clientIP(r)
	if ip == nil {
		return payStatus{}, false
	}
	// The hardware address comes from the kernel's neighbour table, never from the client, and the kernel is
	// ASKED to resolve it rather than only consulted (arp_resolve.go): a cold cache is not evidence that a
	// device is off the guest network. Same mechanism, same guarantee, on every sign-in path.
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

// payReturn serves GET /pay/return?p=<purchase>: the page the provider sends the client back to. It shows a
// "confirming your payment" page that polls /api/pay/status; whether the client paid is decided by scd.
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
	_ = payTmpl.Execute(w, payView{guestPage: pg, PurchaseID: p, Cancelled: r.URL.Query().Get("cancelled") == "1",
		Room: r.URL.Query().Get("m") == "room"})
}

// payStatusAPI serves GET /api/pay/status?p=<purchase>. When the purchase is granted it activates THIS device
// and answers where to go; otherwise it reports pending, failed or review.
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
		writeJSONPortal(w, http.StatusOK, map[string]any{"state": st.State})
		return
	}
	sid, failure := h.activateEnforced(r, commerceSession{deviceID: st.DeviceID}, st.EntitlementID)
	if failure != "" {
		writeJSONPortal(w, http.StatusOK, map[string]any{"state": "granted", "activation": failure})
		return
	}
	writeJSONPortal(w, http.StatusOK, map[string]any{"state": "connected", "redirect": "/success?s=" + url.QueryEscape(sid)})
}

type payView struct {
	guestPage
	PurchaseID string
	Cancelled  bool
	// Room is a room charge: the page says the room is being charged rather than naming a payment page.
	Room bool
}

var payTmpl = template.Must(template.New("pay").Parse(compactMarkup(payHTML)))
