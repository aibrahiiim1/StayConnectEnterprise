package main

// The guest's identity is derived here from the connection and the neighbour table, never from the body.
// Every non-success is the same bytes (see pms_phase3.go), so the portal is not an occupancy oracle.
// Flow: resolve → grant, with the guest choosing among server-offered packages; a single option is chosen
// for them to save a round trip.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// The scd* types mirror scd's contract and are separate from the guest-facing ones: scd tells portald more
// than the guest may see, and a shared struct is how the extra fields leak.
type scdOffer struct {
	PackageRevisionID string   `json:"package_revision_id"`
	Code              string   `json:"code"`
	DownKbps          int      `json:"down_kbps"`
	UpKbps            int      `json:"up_kbps"`
	PriceMinor        int64    `json:"price_minor"`
	Currency          string   `json:"currency"`
	CurrencyExponent  int      `json:"currency_exponent"`
	Methods           []string `json:"methods"`
}

type scdResolveResp struct {
	Outcome string `json:"outcome"`
	// FailureClass feeds the guest's message only; the exact reason stays in scd.
	FailureClass string `json:"failure_class"`
	// RetryAfterSeconds is passed through untouched, never computed, extended or shortened.
	RetryAfterSeconds int        `json:"retry_after_seconds"`
	AuthContextID     string     `json:"auth_context_id"`
	ExpiresIn         int        `json:"expires_in_seconds"`
	Offers            []scdOffer `json:"offers"`
}

type scdGrantResp struct {
	Outcome       string `json:"outcome"`
	FailureClass  string `json:"failure_class"`
	SessionID     string `json:"session_id"`
	EntitlementID string `json:"entitlement_id"`
	// A paid choice answers PENDING until the PMS or card provider confirms.
	PurchaseID  string `json:"purchase_id"`
	Method      string `json:"method"`
	RedirectURL string `json:"redirect_url"`
	State       string `json:"state"`
	// DeviceID and GuestNetworkID come only with DEVICE_LIMIT; held server-side, never forwarded to the guest.
	DeviceID       string `json:"device_id"`
	GuestNetworkID string `json:"guest_network_id"`
}

// The two classes portald may decide on its own; everything else comes from scd, since portald never knows
// what the guest got wrong.
const (
	classCredential = "CREDENTIAL"
	classTechnical  = "TECHNICAL"
	// classPostStay selects the post-stay wording, for guests with no room or surname to re-check.
	classPostStay = "POST_STAY"
)

// phase3In is the guest's submission; no ip, mac, stay, interface or price. Every verification field the
// portal can send must be named here: an unnamed field is dropped silently on the hop and reaches the guest
// as the uniform failure (`verification` and `first_name` once made two sign-in modes dead this way).
type phase3In struct {
	Room string `json:"room"`
	// Verification is the single room_any value, forwarded verbatim; scd compares it against first name, last
	// name and reservation number. Never guess its type from its shape.
	Verification      string `json:"verification,omitempty"`
	FirstName         string `json:"first_name,omitempty"`
	LastName          string `json:"last_name,omitempty"`
	ReservationNumber string `json:"reservation_number,omitempty"`
	// RequestID makes a retry idempotent.
	RequestID string `json:"request_id"`
	// AuthContextID and PackageRevisionID are present only on the second call, after the guest picked an offer.
	AuthContextID     string `json:"auth_context_id,omitempty"`
	PackageRevisionID string `json:"package_revision_id,omitempty"`
	// Method is the acquisition method chosen for a priced offer (PMS_POSTING, ONLINE_PAYMENT).
	Method string `json:"method,omitempty"`
}

// phase3Out: on failure only the uniform message; `choices` only when the guest must pick.
type phase3Out struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	// RetryAfterSeconds is the server's remaining time on a restricted refusal; the page never decides the wait is over.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`

	SessionID  string `json:"session_id,omitempty"`
	RedirectTo string `json:"redirect_to,omitempty"`

	// NeedsChoice and Choices carry no stay, interface or property detail.
	NeedsChoice   bool           `json:"needs_choice,omitempty"`
	AuthContextID string         `json:"auth_context_id,omitempty"`
	Choices       []phase3Choice `json:"choices,omitempty"`
}

type phase3Choice struct {
	PackageRevisionID string `json:"package_revision_id"`
	Code              string `json:"code"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
	// Method and Price: one choice per (package, method) this stay may use; Price is display text.
	Method string `json:"method"`
	Price  string `json:"price"`
}

func (h *handler) authPMSPhase3(w http.ResponseWriter, r *http.Request) {
	// The budget starts before the body is read, or the cheap local checks would be distinguishable by timing.
	b := h.newPhase3Budget(r)
	defer b.cancel()

	var in phase3In
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		h.phase3Fail(w, r, b, "malformed_request", classCredential)
		return
	}
	// Identity is derived here only. An unplaceable device is TECHNICAL (nothing the guest types helps), and
	// it is forwarded to scd with whatever identity could be established so scd refuses and records it in the
	// sign-in attempt log; scd re-derives the network from the source address itself.
	ip := clientIP(r)
	device := map[string]string{"ip": "", "mac": ""}
	if ip != nil {
		device["ip"] = ipString(ip)
		// The MAC comes from the kernel neighbour table, asked to resolve (arp_resolve.go); a cold cache is not
		// evidence the device is off the network.
		if mac, ok := h.deviceMAC(b.ctx, ip); ok {
			device["mac"] = mac.String()
		}
	}

	// Second call: the guest already proved who they are and chose a package.
	if strings.TrimSpace(in.AuthContextID) != "" {
		h.phase3Grant(w, r, b, in.AuthContextID, in.PackageRevisionID, in.Method, device)
		return
	}

	res, ok := h.phase3Resolve(w, r, b, in, device)
	if !ok {
		return // the uniform failure has already been written
	}
	// One choice per (offer, method): a single included offer is granted at once; anything priced, or more than
	// one way in, is the guest's choice. Prices are formatted without the page dictionary (no extra scd hop
	// inside the budget); a free choice carries no price text.
	noWords := map[string]string{"acq.free": ""}
	var choices []phase3Choice
	for _, o := range res.Offers {
		ms := o.Methods
		if len(ms) == 0 {
			ms = []string{"NOT_REQUIRED"}
		}
		for _, m := range ms {
			choices = append(choices, phase3Choice{PackageRevisionID: o.PackageRevisionID, Code: o.Code,
				DownKbps: o.DownKbps, UpKbps: o.UpKbps, Method: m,
				Price: formatPrice(noWords, o.PriceMinor, o.Currency, o.CurrencyExponent)})
		}
	}
	if len(choices) == 1 && choices[0].Method == "NOT_REQUIRED" {
		h.phase3Grant(w, r, b, res.AuthContextID, choices[0].PackageRevisionID, "", device)
		return
	}
	if len(choices) > 1 || (len(choices) == 1 && choices[0].Method != "NOT_REQUIRED") {
		writeJSONPortal(w, http.StatusOK, phase3Out{
			OK: true, NeedsChoice: true, AuthContextID: res.AuthContextID, Choices: choices})
		return
	}
	// A configuration problem, TECHNICAL: their details were right.
	h.phase3Fail(w, r, b, "verified_without_offers", classTechnical)
}

func (h *handler) phase3Resolve(w http.ResponseWriter, r *http.Request, b *phase3Budget, in phase3In, device map[string]string) (scdResolveResp, bool) {
	// Filled verification fields are forwarded as typed; `device` is built by the appliance, and no phase3In
	// field can reach ip, mac, stay or interface. Which fields the site accepts is scd's decision
	// (cmd/scd/phase3_auth_mode.go); this hop only omits empty keys so a blank is never mistaken for evidence.
	body := map[string]any{"room": in.Room, "request_id": in.RequestID, "device": device}
	for k, v := range map[string]string{
		"verification": in.Verification, "first_name": in.FirstName,
		"last_name": in.LastName, "reservation_number": in.ReservationNumber,
	} {
		if strings.TrimSpace(v) != "" {
			body[k] = v
		}
	}
	raw, _ := json.Marshal(body)
	var out scdResolveResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase3/auth/pms/resolve", raw, &out) {
		// Includes an abandoned hop (budget expired); nothing about the input is known, so TECHNICAL.
		h.phase3Fail(w, r, b, "scd_unavailable", classTechnical)
		return out, false
	}
	if out.Outcome != "VERIFIED" || out.AuthContextID == "" {
		// scd's class crosses verbatim; portald re-derives nothing, including the remaining wait.
		h.phase3FailRetryAfter(w, r, b, "not_verified", out.FailureClass, out.RetryAfterSeconds)
		return out, false
	}
	return out, true
}

func (h *handler) phase3Grant(w http.ResponseWriter, r *http.Request, b *phase3Budget, authContextID, packageRevID, method string, device map[string]string) {
	req := map[string]any{
		"auth_context_id":     authContextID,
		"package_revision_id": packageRevID,
		"device":              device,
	}
	if method != "" && method != "NOT_REQUIRED" {
		if len(method) > 32 {
			h.phase3Fail(w, r, b, "malformed_request", classCredential)
			return
		}
		req["method"] = method
		req["return_base"] = h.returnBase(r)
	}
	body, _ := json.Marshal(req)
	var out scdGrantResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase3/auth/pms/grant", body, &out) {
		h.phase3Fail(w, r, b, "scd_unavailable", classTechnical)
		return
	}
	if out.Outcome == "PENDING" && validPurchaseID(out.PurchaseID) && out.State != "failed" {
		// The purchase awaits the PMS (room charge) or the card provider.
		to := "/pay/return?p=" + out.PurchaseID
		if out.Method == "PMS_POSTING" {
			to += "&m=room"
		}
		if strings.HasPrefix(out.RedirectURL, "https://") {
			to = out.RedirectURL
		}
		writeJSONPortal(w, http.StatusOK, phase3Out{OK: true, RedirectTo: to})
		return
	}
	if out.FailureClass == "DEVICE_LIMIT" && h.phase3ToReplace(w, r, authContextID, out) {
		return
	}
	if out.Outcome != "VERIFIED" || out.SessionID == "" {
		// No session means no access; never report success. The class comes from scd.
		h.phase3Fail(w, r, b, "grant_refused", out.FailureClass)
		return
	}
	writeJSONPortal(w, http.StatusOK, phase3Out{OK: true, SessionID: out.SessionID, RedirectTo: "/success"})
}

// phase3ToReplace sends a room guest with every device place taken to the replacement page (contract
// ONEGATE_DEVICE_MANAGEMENT §3). Identity is already proved (scd refused the grant, not the sign-in), so
// nothing is disclosed. The auth context and placed device are held server-side in a commerce session. It
// returns false, and the device-limit sentence is answered, when there is nowhere to hold the proof or scd
// did not name the device.
func (h *handler) phase3ToReplace(w http.ResponseWriter, r *http.Request, authContextID string, out scdGrantResp) bool {
	authContextID = strings.TrimSpace(authContextID)
	if h.commerceSessions == nil || authContextID == "" || out.DeviceID == "" || out.GuestNetworkID == "" {
		return false
	}
	token, err := newCommerceToken()
	if err != nil {
		return false
	}
	sess := commerceSession{authContextID: authContextID, deviceID: out.DeviceID, guestNetworkID: out.GuestNetworkID,
		expiry: time.Now().Add(commerceSessionTTL)}
	h.commerceSessions.put(token, sess)
	setPortalCookie(w, r, commerceCookie, token, commerceSessionTTL)
	slog.Info("phase3 guest at the stay's device limit: offered the replacement page")
	writeJSONPortal(w, http.StatusOK, phase3Out{OK: true, RedirectTo: "/devices/replace"})
	return true
}

// scdPhase3Call runs under the budget's context, not the request's, so a slow hop is cut off at the budget
// instead of the 5s client timeout. Transport failure, abandonment and refusal all become the same uniform
// answer; only the log differs.
func (h *handler) scdPhase3Call(b *phase3Budget, url string, body []byte, out any) bool {
	req, err := http.NewRequestWithContext(b.ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("phase3 scd hop failed", "url", url, "err", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		slog.Warn("phase3 scd hop refused", "url", url, "status", resp.StatusCode)
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// phase3Fail writes the uniform non-success through the one shared builder, so there is a single place to drift.
func (h *handler) phase3Fail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason, class string) {
	h.phase3FailRetryAfter(w, r, b, reason, class, 0)
}

// phase3FailRetryAfter alone carries a retry hint, so no other failure can acquire one.
func (h *handler) phase3FailRetryAfter(w http.ResponseWriter, r *http.Request, b *phase3Budget,
	reason, class string, retryAfter int) {
	status, body, audit := buildGuestPMSResponse(outcomeNoMatch, reason, class, "", "", retryAfter)
	slog.Info("phase3 guest auth not verified", "reason", audit.ReasonCode, "class", class)
	// Wait before the write: after it, the bytes are already on the wire.
	b.wait(r)
	writeGuestPMSResponse(w, status, body)
}

func writeJSONPortal(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
