package main

// Issues the trusted commerce session for an IAM-v2 reply and sends the guest to package selection: authentication is not access, so /success is reached only after a session exists and is enforced.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type iamv2AuthReply struct {
	AuthContextID     string `json:"auth_context_id"`
	DeviceID          string `json:"device_id"`
	GuestNetworkID    string `json:"guest_network_id"`
	Authority         string `json:"authority"`
	Method            string `json:"method"`
	LiveEntitlementID string `json:"live_entitlement_id,omitempty"`
	RecoveryCode      string `json:"recovery_code,omitempty"`
	ResumeToken       string `json:"resume_token,omitempty"`
	ResumeDays        int    `json:"resume_days,omitempty"`
	IdentityLabel     string `json:"identity_label,omitempty"`
}

// commerceSessionTTL is short: the pins are the guest's whole claim to buy, and the scd auth context is one-time and TTL'd, so a stale cookie must not outlive it.
const commerceSessionTTL = 10 * time.Minute

// newCommerceToken uses crypto/rand: the token is the capability and is never derived from guest input.
func newCommerceToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tryIAMv2Auth issues the commerce session for an IAM-v2 reply and returns true when it handled the response; a legacy reply returns false and the old path runs unchanged.
func (h *handler) tryIAMv2Auth(w http.ResponseWriter, r *http.Request, payload []byte) bool {
	return h.tryIAMv2AuthMode(w, r, payload, false)
}

// tryIAMv2AuthMode serves both callers: browser navigations get redirects and pages, scripts get {"next": url} or {"error", "key"}; the decisions are identical.
func (h *handler) tryIAMv2AuthMode(w http.ResponseWriter, r *http.Request, payload []byte, asJSON bool) bool {
	var reply iamv2AuthReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return false
	}
	// Both an auth context and a device are required: a half-populated session leaves the commerce bridge unable to identify the guest.
	if reply.Authority != "iam_v2" || reply.AuthContextID == "" || reply.DeviceID == "" {
		return false
	}
	fail := func(msg string) {
		if asJSON {
			words := guestWords(guestLanguage(r, nil).Code, nil)
			m := localise(words, msg)
			writeJSONPortal(w, http.StatusForbidden, map[string]any{"error": msg, "key": m.Key})
			return
		}
		h.landing(w, r, msg)
	}
	next := func(to string) {
		if asJSON {
			writeJSONPortal(w, http.StatusOK, map[string]any{"next": to})
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
	// A verified factor earns a resume credential (client_resume.go), kept only when the guest ticked "Remember this device" over HTTPS; otherwise it is revoked at once.
	// Not for open selection: that resume_token is the anonymous subject's, lives in og_resume, and writing it here would overwrite the device's Welcome-back credential.
	if reply.ResumeToken != "" && reply.Method != "OPEN" {
		if guestAskedToRemember(r, reply.Method) {
			setClientResumeCookie(w, r, reply.ResumeToken, reply.ResumeDays)
		} else {
			h.revokeResume(r.Context(), reply.ResumeToken)
		}
	}
	if h.commerceSessions == nil {
		// The store exists only when the Phase-2 portal surface is on; without it the guest cannot proceed, and saying "connected" would be false.
		fail("Internet packages are not available right now. Please contact the site team for assistance.")
		return true
	}
	token, err := newCommerceToken()
	if err != nil {
		fail("Something went wrong. Please try again.")
		return true
	}
	sess := commerceSession{
		authContextID:  reply.AuthContextID,
		deviceID:       reply.DeviceID,
		guestNetworkID: reply.GuestNetworkID,
		expiry:         time.Now().Add(commerceSessionTTL),
		returnCode:     reply.RecoveryCode,
		identityLabel:  reply.IdentityLabel,
	}
	// Already entitled: join, don't buy. A purchase would supersede the existing access and knock their other devices offline (contract B3 and 6.3); success still means enforced.
	if reply.LiveEntitlementID != "" {
		sid, failure := h.activateEnforced(r, sess, reply.LiveEntitlementID)
		switch failure {
		case "":
			next("/success?s=" + url.QueryEscape(sid))
		case activateDeviceLimit:
			// Every place is taken: the client chooses which device this one replaces (device_pages.go), keeping this sign-in as proof.
			h.toReplace(w, r, sess, token, asJSON)
		case activateNoDevice:
			fail("Your device isn't connected to this Wi-Fi network.")
		case activateCapacity:
			fail(guestCapacityMessage)
		case activateLicense:
			fail(guestLicenseRefusedMessage)
		case activateNotEnforced:
			fail("We could not bring your device online. Please try again in a moment.")
		case activateVerification:
			if asJSON {
				next("/account/verification")
			} else {
				h.renderAccountPage(w, r, nil, true, accountNote{})
			}
		default:
			fail("We could not connect your device.")
		}
		return true
	}
	// A voucher is redeemed in one step: the code grants exactly the package revision it was printed for and is spent in the same transaction.
	if reply.Method == "VOUCHER" {
		if !h.redeemVoucher(w, r, sess, token) {
			h.landing(w, r, msgVoucherInvalid)
		}
		return true
	}
	h.commerceSessions.put(token, sess)
	// HttpOnly, SameSite=Lax (blocks cross-site POST shapes), Secure over TLS only (cookies.go); the token is short-lived, single-purpose and carries no guest data.
	setPortalCookie(w, r, commerceCookie, token, commerceSessionTTL)
	// Not /success: the guest has authenticated but acquired nothing yet.
	next("/packages")
	return true
}

// packagesPage requires a valid commerce session; without one there is nothing to identify the guest by.
func (h *handler) packagesPage(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		h.landing(w, r, "Please sign in again.")
		return
	}
	// A refusal the page's script was told about is shown once here; a client who returns to the package list has chosen to choose again.
	notice := sess.notice
	if notice != "" || sess.purchaseID != "" {
		if c, err := r.Cookie(commerceCookie); err == nil {
			sess.notice, sess.purchaseID = "", ""
			h.commerceSessions.put(c.Value, sess)
		}
	}
	h.packagesFor(w, r, sess, notice)
}

// backToPackages keeps the client signed in while their commerce session is valid, showing the choices again with the reason above; sending them to sign-in read as "signed out" and cost a new code on a phone. Only an expired session asks to sign in again.
func (h *handler) backToPackages(w http.ResponseWriter, r *http.Request, msg string) {
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		if navJSON(r) {
			h.navigate(w, r, "/packages")
			return
		}
		h.landing(w, r, msg)
		return
	}
	if navJSON(r) {
		// The page's script is told to load the package page, which shows this reason once (the session carries it, never the URL).
		if c, err := r.Cookie(commerceCookie); err == nil {
			sess.notice = msg
			h.commerceSessions.put(c.Value, sess)
		}
		h.navigate(w, r, "/packages")
		return
	}
	h.packagesFor(w, r, sess, msg)
}

// navJSON: when the page's script submitted the form it is told where to go instead of redirected, because a redirect to a provider's checkout is a cross-origin form hop that form-action governs, whereas window.location is not.
func navJSON(r *http.Request) bool { return r.Header.Get("X-OneGate-Nav") == "1" }

func (h *handler) navigate(w http.ResponseWriter, r *http.Request, to string) {
	if navJSON(r) {
		writeJSONPortal(w, http.StatusOK, map[string]any{"next": to})
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (h *handler) packagesFor(w http.ResponseWriter, r *http.Request, sess commerceSession, errMsg string) {
	q := url.Values{}
	q.Set("auth_context_id", sess.authContextID)
	q.Set("device_id", sess.deviceID)
	q.Set("guest_network_id", sess.guestNetworkID)
	resp, err := h.scdDo(r.Context(), http.MethodGet, "/v1/commerce/packages?"+q.Encode(), nil)
	if err != nil {
		h.landing(w, r, "Internet packages are unavailable right now.")
		return
	}
	defer resp.Body.Close()
	var out struct {
		Packages          []guestPackage `json:"packages"`
		FreeAllowanceUsed bool           `json:"free_allowance_used"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
		h.landing(w, r, "No internet packages are available for you right now.")
		return
	}
	if len(out.Packages) == 0 {
		// The upgrade journey ends honestly: a client with nothing priced to choose is told so on the package page, still signed in, with what they can do (voucher code or sign out) rather than dropped on sign-in under an error.
		msg := "No internet packages are available for you right now."
		if out.FreeAllowanceUsed {
			msg = msgFreeUsedNothingElse
		}
		h.renderPackages(w, r, nil, "", sess.identityLabel, false, msg)
		return
	}
	// The return code stays with the server-held session (ten minutes) and is shown on this page, the pending page and the success page; never in a URL.
	h.renderPackages(w, r, out.Packages, sess.returnCode, sess.identityLabel, out.FreeAllowanceUsed, errMsg)
}

// acquirePackage runs quote, confirm and activate server-side; the browser supplies one opaque package id and everything else is server-held, so a form edit cannot acquire against another context.
func (h *handler) acquirePackage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.backToPackages(w, r, "Bad request.")
		return
	}
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		if navJSON(r) {
			h.navigate(w, r, "/packages")
			return
		}
		h.landing(w, r, "Please sign in again.")
		return
	}
	pkgID := strings.TrimSpace(r.FormValue("package_id"))
	if pkgID == "" {
		h.backToPackages(w, r, "Please choose a package.")
		return
	}
	method := strings.TrimSpace(r.FormValue("method"))
	if len(method) > 32 {
		method = ""
	}
	post := func(path string, body map[string]any) (map[string]any, bool) {
		raw, _ := json.Marshal(body)
		resp, err := h.scdDo(r.Context(), http.MethodPost, path, raw)
		if err != nil {
			return nil, false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var out map[string]any
		if json.Unmarshal(b, &out) != nil || resp.StatusCode != http.StatusOK {
			return out, false
		}
		return out, true
	}
	quote, ok := post("/v1/commerce/quote", map[string]any{
		"auth_context_id": sess.authContextID, "device_id": sess.deviceID,
		"guest_network_id": sess.guestNetworkID, "package_id": pkgID, "method": method})
	if !ok {
		if method == "ONLINE_PAYMENT" {
			h.backToPackages(w, r, msgCardUnavailable)
			return
		}
		h.backToPackages(w, r, "That package is not available. Please choose another.")
		return
	}
	confirm, ok := post("/v1/commerce/confirm", map[string]any{
		"quote_id": quote["quote_id"], "device_id": sess.deviceID,
		"guest_network_id": sess.guestNetworkID, "return_base": h.returnBase(r)})
	if pending, _ := confirm["awaiting_settlement"].(bool); pending {
		// Card payment: nothing is granted yet; the client pays on the provider's page and returns to a page that asks where the purchase stands.
		pid, _ := confirm["purchase_id"].(string)
		if u, _ := confirm["redirect_url"].(string); strings.HasPrefix(u, "https://") {
			h.rememberPurchase(r, sess, pid)
			h.navigate(w, r, u)
			return
		}
		if validPurchaseID(pid) {
			h.rememberPurchase(r, sess, pid)
			h.navigate(w, r, "/pay/return?p="+pid)
			return
		}
	}
	if !ok {
		if method == "ONLINE_PAYMENT" {
			h.backToPackages(w, r, msgCardUnavailable)
			return
		}
		h.backToPackages(w, r, "We could not complete that. Please try again.")
		return
	}
	eid, _ := confirm["entitlement_id"].(string)
	sid, failure := h.activateEnforced(r, sess, eid)
	switch failure {
	case "":
	case activateNoDevice:
		h.backToPackages(w, r, "Your device isn't connected to this Wi-Fi network.")
		return
	case activateDeviceLimit:
		h.toReplace(w, r, sess, "", navJSON(r))
		return
	case activateCapacity:
		h.backToPackages(w, r, guestCapacityMessage)
		return
	case activateLicense:
		h.backToPackages(w, r, guestLicenseRefusedMessage)
		return
	case activateNotEnforced:
		h.backToPackages(w, r, "We could not bring your device online. Please try again in a moment.")
		return
	case activateVerification:
		if navJSON(r) {
			h.navigate(w, r, "/account/verification")
			return
		}
		h.renderAccountPage(w, r, nil, true, accountNote{})
		return
	default:
		h.backToPackages(w, r, "We could not connect your device.")
		return
	}
	if c, err := r.Cookie(commerceCookie); err == nil {
		sess.acquired = true
		h.commerceSessions.put(c.Value, sess)
	}
	h.navigate(w, r, "/success?s="+url.QueryEscape(sid))
}

// rememberPurchase stores the started card purchase in the commerce session so a client returning to "/" is sent to the page that asks where it stands; the page re-identifies the device server-side.
func (h *handler) rememberPurchase(r *http.Request, sess commerceSession, pid string) {
	if !validPurchaseID(pid) || h.commerceSessions == nil {
		return
	}
	if c, err := r.Cookie(commerceCookie); err == nil {
		sess.purchaseID = pid
		h.commerceSessions.put(c.Value, sess)
	}
}

// renderPackages references no external stylesheet, font or script (the guest has no access yet), and every value goes through html/template: package text arrives via the database and an API and must not be trusted markup.
func (h *handler) renderPackages(w http.ResponseWriter, r *http.Request, pkgs []guestPackage, returnCode, identity string, freeUsed bool, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	v := packagesView{guestPage: p, Packages: packageRows(p.T, pkgs), ReturnCode: returnCode,
		Identity: identity, FreeUsed: freeUsed, Error: localise(p.T, errMsg).Text}
	// A voucher is entered as a code, never chosen from the list; with nothing to choose, the code field is the way forward, otherwise the page stays a choice.
	v.VoucherCTA = len(pkgs) == 0 && h.voucherOffered(r.Context(), clientIPString(r))
	_ = packagesTmpl.Execute(w, v)
}

var packagesTmpl = template.Must(template.New("packages").Parse(compactMarkup(packagesHTML)))

// activateClientTimeout covers scd's enforcement wait (8 s) with margin for the grant transaction.
const activateClientTimeout = 12 * time.Second

const (
	activateNoDevice    = "NO_DEVICE"
	activateDeviceLimit = "MAX_DEVICES_REACHED"
	// activateCapacity and activateLicense are the licence refusing the session; they use the same sentences as the sign-in forms and room sign-in.
	activateCapacity    = "LICENSE_CAPACITY_REACHED"
	activateLicense     = "LICENSE_REFUSED"
	activateNotEnforced = "NOT_ENFORCED"
	activateFailed      = "ACTIVATE_FAILED"
	// activateVerification: an email account suspended for Internet until verified; the entitlement is untouched, only this activation is refused.
	activateVerification = "VERIFICATION_REQUIRED"
)

// activateEnforced reports success only when netd has enforced the session: PENDING_ENFORCEMENT means packets are still dropped. The /packages form and the success-page confirm both use it, so neither can claim a guest is online when they are not.
func (h *handler) activateEnforced(r *http.Request, sess commerceSession, entitlementID string) (string, string) {
	if entitlementID == "" {
		return "", activateFailed
	}
	ip := clientIP(r)
	if ip == nil {
		return "", activateNoDevice
	}
	// The MAC comes from the kernel neighbour table and the kernel is asked to resolve it (arp_resolve.go).
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		return "", activateNoDevice
	}
	raw, _ := json.Marshal(map[string]any{
		"entitlement_id": entitlementID, "device_id": sess.deviceID,
		"ip": ip.String(), "mac": mac.String(),
		"auth_context_id": sess.authContextID})
	// The wait must fit inside the call: scd holds the request until netd enforces (up to iamv2EnforcementWaitMax, 8 s) but portald's shared client times out at 5 s, which surfaced as a failure after the grant had committed. Activation uses a client whose timeout covers the whole wait.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://unix/v1/sessions/activate", bytes.NewReader(raw))
	if err != nil {
		return "", activateFailed
	}
	req.Header.Set("Content-Type", "application/json")
	client := *h.scd
	client.Timeout = activateClientTimeout
	resp, err := client.Do(req)
	if err != nil {
		return "", activateFailed
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var act map[string]any
	_ = json.Unmarshal(b, &act)
	if resp.StatusCode != http.StatusOK {
		code, _ := act["error"].(string)
		switch {
		case code == "MAX_DEVICES_REACHED":
			return "", activateDeviceLimit
		case code == "LICENSE_CAPACITY_REACHED":
			return "", activateCapacity
		case code == "VERIFICATION_REQUIRED":
			return "", activateVerification
		case isLicenceRefusal(code):
			return "", activateLicense
		}
		return "", activateFailed
	}
	if enforced, _ := act["enforced"].(bool); !enforced {
		return "", activateNotEnforced
	}
	sid, _ := act["session_id"].(string)
	return sid, ""
}

// returnBase is the portal's own origin, never the client's Host header: the canonical name over HTTPS, the portal address (else the connection's address) on the HTTP fallback, and the request origin only as a last resort.
func (h *handler) returnBase(r *http.Request) string {
	st := h.tlsState()
	if st.HTTPSActive && st.Canonical != "" {
		return "https://" + st.Canonical
	}
	if o := h.fallbackOrigin(r); o != "" {
		return o
	}
	return portalOrigin(r)
}

// portalOrigin is the last resort for returnBase and the email link; anything odd yields "" and card payment is refused.
func portalOrigin(r *http.Request) string {
	host := r.Host
	if host == "" || strings.ContainsAny(host, "/\\ @?#") || len(host) > 255 {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}

// voucherOffered answers false on any failure: the field is a convenience and sign-in still offers vouchers.
func (h *handler) voucherOffered(ctx context.Context, ip string) bool {
	if h.scd == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/tenant/auth-methods?ip="+url.QueryEscape(ip), nil)
	if err != nil {
		return false
	}
	resp, err := h.scd.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var m struct {
		Voucher struct {
			Enabled bool `json:"enabled"`
		} `json:"voucher"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m) == nil && m.Voucher.Enabled
}
