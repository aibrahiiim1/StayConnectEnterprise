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
	var reply iamv2AuthReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return false
	}
	// Both an auth context AND a device are required. A partial reply is not something to paper over with a
	// half-populated session: without either pin the commerce bridge cannot identify the guest at all.
	if reply.Authority != "iam_v2" || reply.AuthContextID == "" || reply.DeviceID == "" {
		return false
	}
	if h.commerceSessions == nil {
		// The store is only constructed when the Phase-2 portal surface is on. Authenticating with nowhere to
		// put the pins means the guest cannot proceed, and saying "connected" would be false.
		h.landing(w, r, "Internet packages are not available right now. Please ask reception.")
		return true
	}
	token, err := newCommerceToken()
	if err != nil {
		h.landing(w, r, "Something went wrong. Please try again.")
		return true
	}
	h.commerceSessions.put(token, commerceSession{
		authContextID:  reply.AuthContextID,
		deviceID:       reply.DeviceID,
		guestNetworkID: reply.GuestNetworkID,
		expiry:         time.Now().Add(commerceSessionTTL),
	})
	http.SetCookie(w, &http.Cookie{
		Name:  commerceCookie,
		Value: token,
		Path:  "/",
		// HttpOnly: the page never needs to read this, and script that cannot read it cannot leak it.
		HttpOnly: true,
		// SameSite=Lax: the captive portal is same-site; Lax still blocks the cross-site POST shapes.
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(commerceSessionTTL),
		// Secure is deliberately NOT set: a captive portal is reached over plain HTTP before the guest has
		// any access at all, and a Secure cookie would simply never be sent, breaking the flow it protects.
		// The token is short-lived, single-purpose, and carries no guest data.
	})
	// NOT /success. The guest has authenticated; they have not acquired anything yet.
	http.Redirect(w, r, "/packages", http.StatusSeeOther)
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
		Packages []struct {
			PackageID string         `json:"package_id"`
			Display   map[string]any `json:"display"`
		} `json:"packages"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil || len(out.Packages) == 0 {
		h.landing(w, r, "No internet packages are available for you right now.")
		return
	}
	h.renderPackages(w, r, out.Packages)
}

// acquirePackage runs quote -> confirm -> activate server-side.
//
// The browser supplies ONE opaque id, the package it chose. Every other input -- auth context, device,
// guest network, and the entitlement that comes back -- is server-held or server-derived, so a guest cannot
// acquire against someone else's context by editing a form.
func (h *handler) acquirePackage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		h.landing(w, r, "Please sign in again.")
		return
	}
	pkgID := strings.TrimSpace(r.FormValue("package_id"))
	if pkgID == "" {
		h.landing(w, r, "Please choose a package.")
		return
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
		"guest_network_id": sess.guestNetworkID, "package_id": pkgID})
	if !ok {
		h.landing(w, r, "That package is not available. Please choose another.")
		return
	}
	confirm, ok := post("/v1/commerce/confirm", map[string]any{
		"quote_id": quote["quote_id"], "device_id": sess.deviceID,
		"guest_network_id": sess.guestNetworkID})
	if !ok {
		h.landing(w, r, "We could not complete that. Please try again.")
		return
	}
	// The entitlement is durable; the guest is still offline until it is activated into a session.
	eid, _ := confirm["entitlement_id"].(string)
	sid, failure := h.activateEnforced(r, sess, eid)
	switch failure {
	case "":
	case activateNoDevice:
		h.landing(w, r, "Your device isn't on the guest network.")
		return
	case activateDeviceLimit:
		h.landing(w, r, "This account has reached its device limit. Disconnect another device and try again.")
		return
	case activateNotEnforced:
		h.landing(w, r, "We could not bring your device online. Please try again in a moment.")
		return
	default:
		h.landing(w, r, "We could not connect your device.")
		return
	}
	http.Redirect(w, r, "/success?s="+url.QueryEscape(sid), http.StatusSeeOther)
}

// renderPackages draws the selection page: branded and in the guest's language, like every guest page. The
// guest has no access yet, so nothing here references an external stylesheet, font or script.
//
// Every value that reaches the page goes through html/template, which escapes it. Package display text is
// operator-authored, but it arrives here through the database and an API, and treating it as trusted markup
// would make the package name an injection point into a page shown to every guest.
func (h *handler) renderPackages(w http.ResponseWriter, r *http.Request, pkgs []struct {
	PackageID string         `json:"package_id"`
	Display   map[string]any `json:"display"`
}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The same policy as the sign-in page; the page's only scripts carry this nonce.
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	_ = packagesTmpl.Execute(w, packagesView{guestPage: p, Packages: packageRows(p.T, pkgs)})
}

var packagesTmpl = template.Must(template.New("packages").Parse(compactMarkup(packagesHTML)))

// Why an activation did not put the device online. "" means it did, and was enforced.
const (
	activateNoDevice    = "NO_DEVICE"
	activateDeviceLimit = "MAX_DEVICES_REACHED"
	activateNotEnforced = "NOT_ENFORCED"
	activateFailed      = "ACTIVATE_FAILED"
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
	mac, ok := h.arpCache(ip)
	if !ok {
		return "", activateNoDevice
	}
	raw, _ := json.Marshal(map[string]any{
		"entitlement_id": entitlementID, "device_id": sess.deviceID,
		"ip": ip.String(), "mac": mac.String()})
	resp, err := h.scdDo(r.Context(), http.MethodPost, "/v1/sessions/activate", raw)
	if err != nil {
		return "", activateFailed
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var act map[string]any
	_ = json.Unmarshal(b, &act)
	if resp.StatusCode != http.StatusOK {
		if act != nil && act["error"] == "MAX_DEVICES_REACHED" {
			return "", activateDeviceLimit
		}
		return "", activateFailed
	}
	if enforced, _ := act["enforced"].(bool); !enforced {
		return "", activateNotEnforced
	}
	sid, _ := act["session_id"].(string)
	return sid, ""
}
