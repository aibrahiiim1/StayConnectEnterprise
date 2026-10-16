package main

// ISSUING THE TRUSTED COMMERCE SESSION, AND NOT CALLING AUTHENTICATION "CONNECTED".
//
// The trust boundary this uses already existed (see commerce.go): an opaque httpOnly cookie keyed to
// server-held pins, with the browser never able to supply an Auth Context, Device or Guest Network. What did
// not exist was anything that ISSUED one, because IAM-v2 authentication was dark -- so the store was always
// empty and every bridge call fail-closed.
//
// Meanwhile the voucher and account handlers still parsed the LEGACY reply shape:
//
//     var ok2 struct { SessionID string; DurationSeconds int }
//     http.Redirect(w, r, "/success?s="+ok2.SessionID+...)
//
// Under IAM-v2 that reply carries auth_context_id / device_id / guest_network_id and NO session, so both
// fields unmarshalled empty and the guest was redirected to /success?s=&t=0 -- told they were online at the
// exact moment they had done nothing but prove who they were. Authentication is not access: the guest still
// has to be offered a package, acquire it, and have an entitlement activated into an enforced session.
//
// So on an IAM-v2 reply this issues the commerce session and sends the guest to package selection. /success
// is reached only after a session exists and is enforced.

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

// iamv2AuthReply is the IAM-v2 shape returned by scd's guest entry points.
type iamv2AuthReply struct {
	AuthContextID  string `json:"auth_context_id"`
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
	Authority      string `json:"authority"`
	Method         string `json:"method"`
	// LiveEntitlementID: the subject already holds ACTIVE access, so this device joins it rather than buying.
	LiveEntitlementID string `json:"live_entitlement_id,omitempty"`
	// RecoveryCode is present only on an open-selection reply that minted a new anonymous subject.
	RecoveryCode string `json:"recovery_code,omitempty"`
	// ResumeToken is the remembered-device credential (client_resume.go); IdentityLabel the masked factor.
	ResumeToken   string `json:"resume_token,omitempty"`
	ResumeDays    int    `json:"resume_days,omitempty"`
	IdentityLabel string `json:"identity_label,omitempty"`
}

// commerceSessionTTL bounds how long the server-held pins stay usable. It is deliberately short: the pins
// are the guest's whole claim to buy something, and an auth context is one-time and TTL'd on the scd side
// too, so a stale cookie must not outlive the thing it points at.
const commerceSessionTTL = 10 * time.Minute

// newCommerceToken mints an unguessable cookie value. The token IS the capability, so it comes from
// crypto/rand and is never derived from anything the guest supplied.
func newCommerceToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tryIAMv2Auth inspects an scd auth reply. If it is an IAM-v2 result it issues the trusted commerce
// session, sets the cookie and redirects the guest to package selection, returning true to tell the caller
// it has fully handled the response.
//
// A legacy reply returns false, so the existing session_id/duration_seconds path runs completely unchanged.
func (h *handler) tryIAMv2Auth(w http.ResponseWriter, r *http.Request, payload []byte) bool {
	return h.tryIAMv2AuthMode(w, r, payload, false)
}

// tryIAMv2AuthMode is tryIAMv2Auth for both kinds of caller. A browser navigation (voucher form, social
// callback, Connect) gets redirects and the landing page; a script (the one-time-code verify) gets JSON:
// {"next": url} on success, {"error": sentence, "key": translation key} on refusal. The decisions are identical.
func (h *handler) tryIAMv2AuthMode(w http.ResponseWriter, r *http.Request, payload []byte, asJSON bool) bool {
	var reply iamv2AuthReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return false
	}
	// Both an auth context AND a device are required. A partial reply is not something to paper over with a
	// half-populated session: without either pin the commerce bridge cannot identify the guest at all.
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
	// A verified factor earned this device a resume credential (client_resume.go): keep it, so the next
	// reconnection from this device greets the Client instead of asking for another code.
	//
	// NOT FOR OPEN SELECTION (audit P-4). An open-selection reply's resume_token is the ANONYMOUS subject's token,
	// a different credential that lives in og_resume (acquisition.go authOpen). Writing it here overwrote the
	// device's Welcome-back credential, so "Continue without signing in" silently made the device forget its
	// Client.
	if reply.ResumeToken != "" && reply.Method != "OPEN" {
		setClientResumeCookie(w, r, reply.ResumeToken, reply.ResumeDays)
	}
	if h.commerceSessions == nil {
		// The store is only constructed when the Phase-2 portal surface is on. Authenticating with nowhere to
		// put the pins means the guest cannot proceed, and saying "connected" would be false.
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
	// ALREADY ENTITLED: JOIN, DON'T BUY. A guest whose voucher or account already holds ACTIVE access -- a
	// second phone, or the same one reconnecting -- takes a device slot on that access. Offering a purchase
	// instead would supersede it and knock their other devices offline (contract B3 and 6.3). Success still
	// means enforced, exactly as after a purchase.
	if reply.LiveEntitlementID != "" {
		sid, failure := h.activateEnforced(r, sess, reply.LiveEntitlementID)
		switch failure {
		case "":
			next("/success?s=" + url.QueryEscape(sid))
		case activateDeviceLimit:
			// Every place on the package is taken: let the client choose which device this one replaces
			// (device_pages.go), keeping this fresh sign-in as the proof.
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
			// An email account past its verification deadline: the page that says why, and what to do.
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
	// A VOUCHER IS REDEEMED IN ONE STEP. The client entered a code; there is nothing to choose. The code grants
	// exactly the package revision it was printed for, and is spent in the same transaction as the grant.
	if reply.Method == "VOUCHER" {
		if !h.redeemVoucher(w, r, sess, token) {
			h.landing(w, r, msgVoucherInvalid)
		}
		return true
	}
	h.commerceSessions.put(token, sess)
	// HttpOnly (the page never reads it), SameSite=Lax (the captive portal is same-site; Lax still blocks the
	// cross-site POST shapes), Secure over TLS only (cookies.go): on the HTTP fallback a Secure cookie would never
	// be sent back. The token is short-lived, single-purpose, and carries no guest data.
	setPortalCookie(w, r, commerceCookie, token, commerceSessionTTL)
	// NOT /success. The guest has authenticated; they have not acquired anything yet.
	next("/packages")
	return true
}

// packagesPage renders the guest's eligible packages. It is reachable only with a valid commerce session:
// without one there is nothing to identify the guest by, and guessing would defeat the whole boundary.
func (h *handler) packagesPage(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		h.landing(w, r, "Please sign in again.")
		return
	}
	// A refusal the page's script was told about (backToPackages) is shown once, here. A client who comes to the
	// package list has chosen to choose again: a card purchase they left is no longer where "/" sends them.
	notice := sess.notice
	if notice != "" || sess.purchaseID != "" {
		if c, err := r.Cookie(commerceCookie); err == nil {
			sess.notice, sess.purchaseID = "", ""
			h.commerceSessions.put(c.Value, sess)
		}
	}
	h.packagesFor(w, r, sess, notice)
}

// backToPackages answers a refused acquisition. THE CLIENT STAYS SIGNED IN: while their commerce session is
// valid they are shown the same choices again with the reason above them, not the sign-in page. Sending them
// to the sign-in page -- which this did -- read as "you have been signed out", and on a phone it meant a new
// code, a trip to the mail app, and the captive browser gone again (found by the Product Owner, 2026-10-11).
// Only a client whose session has already expired is asked to sign in again.
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
		// The page's script submitted the choice: it is told to load the package page, which shows this reason
		// once (the session carries it, never the URL).
		if c, err := r.Cookie(commerceCookie); err == nil {
			sess.notice = msg
			h.commerceSessions.put(c.Value, sess)
		}
		h.navigate(w, r, "/packages")
		return
	}
	h.packagesFor(w, r, sess, msg)
}

// navJSON reports whether the page's own script submitted this form and asked to be TOLD where to go, rather
// than redirected (packagesHTML). A redirect to a payment provider's hosted checkout is a cross-origin hop of a
// form submission, which form-action governs; window.location is a navigation, which it does not (audit P-1).
func navJSON(r *http.Request) bool { return r.Header.Get("X-OneGate-Nav") == "1" }

// navigate sends the client to `to`: a 303 for a plain form, {"next": to} for the page's script.
func (h *handler) navigate(w http.ResponseWriter, r *http.Request, to string) {
	if navJSON(r) {
		writeJSONPortal(w, http.StatusOK, map[string]any{"next": to})
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// packagesFor lists the session's eligible packages, with an optional refusal above them.
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
		// THE UPGRADE JOURNEY ENDS HONESTLY, AND NOT AT THE SIGN-IN PAGE. A Client whose free allowance is spent and
		// who has nothing priced to choose from is told so in words -- on the package page, still signed in, with
		// what they CAN do: enter a voucher code (when the site sells by voucher) or sign out. It used to drop them
		// on the sign-in page under an error, with the first sign-in form selected, which read as "you were signed
		// out" and offered nothing to act on (found on PRE-LIVE 2026-10-15: a card-only package whose currency the
		// card account does not settle left an email-account client with nothing).
		msg := "No internet packages are available for you right now."
		if out.FreeAllowanceUsed {
			msg = msgFreeUsedNothingElse
		}
		h.renderPackages(w, r, nil, "", sess.identityLabel, false, msg)
		return
	}
	// The return code stays with the server-held session for its life (ten minutes) and is shown on this page, the
	// payment pending page and the success page (contract MODULES §6.5, audit P-11). It is never put in a URL.
	h.renderPackages(w, r, out.Packages, sess.returnCode, sess.identityLabel, out.FreeAllowanceUsed, errMsg)
}

// acquirePackage runs quote -> confirm -> activate server-side.
//
// The browser supplies ONE opaque id, the package it chose. Every other input -- auth context, device,
// guest network, and the entitlement that comes back -- is server-held or server-derived, so a guest cannot
// acquire against someone else's context by editing a form.
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
	// The method the client chose. scd re-validates it against every gate; an unknown value is refused there.
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
		// CARD PAYMENT: nothing is granted yet. The client pays on the provider's page, and comes back to a page
		// that asks where the purchase stands.
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
	// The entitlement is durable; the guest is still offline until it is activated into a session.
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
	// The journey is finished: the landing page no longer continues it at package selection.
	if c, err := r.Cookie(commerceCookie); err == nil {
		sess.acquired = true
		h.commerceSessions.put(c.Value, sess)
	}
	h.navigate(w, r, "/success?s="+url.QueryEscape(sid))
}

// rememberPurchase records, in the client's commerce session, the card purchase it just started. The landing page
// then sends a client who comes back to "/" to the page that asks where that purchase stands instead of the
// package list (P-25); the purchase id is the device's own, and the page re-identifies the device server-side.
func (h *handler) rememberPurchase(r *http.Request, sess commerceSession, pid string) {
	if !validPurchaseID(pid) || h.commerceSessions == nil {
		return
	}
	if c, err := r.Cookie(commerceCookie); err == nil {
		sess.purchaseID = pid
		h.commerceSessions.put(c.Value, sess)
	}
}

// renderPackages draws the selection page: branded and in the guest's language, like every guest page. The
// guest has no access yet, so nothing here references an external stylesheet, font or script.
//
// Every value that reaches the page goes through html/template, which escapes it. Package display text is
// operator-authored, but it arrives here through the database and an API, and treating it as trusted markup
// would make the package name an injection point into a page shown to every guest.
func (h *handler) renderPackages(w http.ResponseWriter, r *http.Request, pkgs []guestPackage, returnCode, identity string, freeUsed bool, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The same policy as the sign-in page; the page's only scripts carry this nonce.
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	// The refusal is one of the server's sentences, shown in the guest's language like the sign-in page does.
	v := packagesView{guestPage: p, Packages: packageRows(p.T, pkgs), ReturnCode: returnCode,
		Identity: identity, FreeUsed: freeUsed, Error: localise(p.T, errMsg).Text}
	// A voucher is entered as a code, never chosen from this list. When there is NOTHING to choose, the code field
	// is the client's way forward (if the site takes vouchers), so it is offered here rather than by sending them
	// back to the sign-in page. With choices on screen the page stays a choice, not a second sign-in form.
	v.VoucherCTA = len(pkgs) == 0 && h.voucherOffered(r.Context())
	_ = packagesTmpl.Execute(w, v)
}

var packagesTmpl = template.Must(template.New("packages").Parse(compactMarkup(packagesHTML)))

// activateClientTimeout covers scd's enforcement wait (8 s) with margin for the grant transaction around it.
const activateClientTimeout = 12 * time.Second

// Why an activation did not put the device online. "" means it did, and was enforced.
const (
	activateNoDevice    = "NO_DEVICE"
	activateDeviceLimit = "MAX_DEVICES_REACHED"
	// activateCapacity and activateLicense are the appliance's licence refusing the session: at its licensed
	// number of guests online, or not admitting new guests at all. They read as the same sentences the
	// sign-in forms and the room sign-in use for the same conditions.
	activateCapacity    = "LICENSE_CAPACITY_REACHED"
	activateLicense     = "LICENSE_REFUSED"
	activateNotEnforced = "NOT_ENFORCED"
	activateFailed      = "ACTIVATE_FAILED"
	// activateVerification is an email account suspended for Internet until its address is verified (contract
	// section 3): the entitlement is untouched, only this activation is refused.
	activateVerification = "VERIFICATION_REQUIRED"
)

// activateEnforced turns a granted entitlement into a session for THIS device and reports success only when
// netd has enforced it.
//
// SUCCESS MEANS ENFORCED, NOT MERELY GRANTED. scd waits for netd to promote the session and reports the state
// it actually observed. A session still PENDING_ENFORCEMENT is a guest whose packets are still being dropped,
// so it is a failure here. Both ways a guest acquires a package -- the /packages form and the success-page
// panel's /api/commerce/confirm -- go through this, so neither can tell a guest they are online when they
// are not.
func (h *handler) activateEnforced(r *http.Request, sess commerceSession, entitlementID string) (string, string) {
	if entitlementID == "" {
		return "", activateFailed
	}
	ip := clientIP(r)
	if ip == nil {
		return "", activateNoDevice
	}
	// The hardware address comes from the kernel's neighbour table, never from the client, and the kernel is
	// ASKED to resolve it rather than only consulted (arp_resolve.go): a cold cache is not evidence that a
	// device is off the guest network. Same mechanism, same guarantee, on every sign-in path.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		return "", activateNoDevice
	}
	raw, _ := json.Marshal(map[string]any{
		"entitlement_id": entitlementID, "device_id": sess.deviceID,
		"ip": ip.String(), "mac": mac.String(),
		// This device's own sign-in: what admits a device other than the purchaser to the entitlement.
		"auth_context_id": sess.authContextID})
	// THE WAIT MUST FIT INSIDE THE CALL. scd holds this request open until netd has enforced the session, for
	// up to iamv2EnforcementWaitMax (8 s). portald's shared scd client times out at 5 s, so a slow enforcement
	// used to surface here as a failure AFTER the grant had committed -- the guest told "could not connect"
	// while their access existed. Activation alone gets a client whose timeout covers scd's whole wait.
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

// returnBase is the portal origin a payment provider sends the client back to (audit P-15). It is the PORTAL'S
// OWN origin, never the Host header the client sent: over HTTPS the canonical name scd reports; on the HTTP
// fallback the portal address when it is answering, else the address the client's connection reached
// (fallbackOrigin). Only when none of those is known does it fall back to the request's own origin.
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

// portalOrigin is the origin the client reached the portal on (scheme://host), the last resort for returnBase
// and the email link. Anything odd yields "" and card payment is refused.
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

// voucherOffered reports whether the site takes vouchers (scd's auth-methods), for the package page's code field.
// Any failure answers false: the field is a convenience, and the sign-in page still offers vouchers.
func (h *handler) voucherOffered(ctx context.Context) bool {
	if h.scd == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/tenant/auth-methods", nil)
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
