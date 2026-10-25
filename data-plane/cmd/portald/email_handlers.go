package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// branding hands the guest only what portaldesign.ForGuests allows (known fields re-checked, custom CSS rebuilt from the save-time allowlist), because a design stored any other way must not reach guests as stored.
// A failure is an empty object, not an error page: a guest must never be unable to sign in because a logo could not be fetched.
func (h *handler) branding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	d, ok := h.guestDesign(r)
	if !ok {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"design": d})
}

func (h *handler) authMethods(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://unix/v1/tenant/auth-methods?ip="+url.QueryEscape(clientIPString(r)), nil)
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd auth-methods", "err", err)
		http.Error(w, "auth methods unavailable", 502)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if h.pendingOTP != nil && resp.StatusCode == http.StatusOK {
		if p, ok := h.pendingOTP.get(h.pendingKey(r)); ok {
			var m map[string]any
			if json.Unmarshal(payload, &m) == nil {
				m["pending_otp"] = map[string]any{"channel": p.Channel, "challenge_id": p.ChallengeID,
					"dest_masked": p.DestMasked, "ttl_seconds": int(time.Until(p.ExpiresAt).Seconds())}
				if b, err := json.Marshal(m); err == nil {
					payload = b
				}
			}
		}
	}
	_, _ = w.Write(payload)
}

// The one-time code is for phones only; an email address signs in as an email account (account_handlers.go).
type otpReqIn struct {
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
}

func (h *handler) authOTPRequest(w http.ResponseWriter, r *http.Request) {
	// Only the portal's own page may ask for a message: this route texts a number from the body, so a no-cors text/plain request from any origin could make the portal message a number of its choosing. A JSON content type forces a preflight portald never grants, and another origin is refused outright.
	if !jsonContentType(r) || h.crossSite(r) {
		jsonErr(w, http.StatusForbidden, "request refused")
		return
	}
	var in otpReqIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, 400, "bad body")
		return
	}
	switch in.Channel {
	case "sms", "whatsapp":
	default:
		jsonErr(w, 400, "channel must be sms or whatsapp")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		jsonErr(w, 400, "bad ip")
		return
	}
	// The code is bound to this device: the MAC comes from the kernel neighbour table, resolved rather than only consulted (arp_resolve.go), never from the client.
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		jsonErr(w, 400, "device not on this Wi-Fi network")
		return
	}
	body, _ := json.Marshal(map[string]string{
		"channel":     in.Channel,
		"destination": in.Destination,
		"ip":          ipString(ip),
		"mac":         mac.String(),
		"portal_base": portalLinkBase(r),
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/auth/otp/issue", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd otp issue", "err", err)
		jsonErr(w, 502, "service unavailable")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	// The code is in flight for this device, held server-side so a landing page opened after the captive browser was dismissed reopens on the code box.
	if resp.StatusCode == http.StatusOK && h.pendingOTP != nil {
		var out struct {
			ChallengeID string `json:"challenge_id"`
			TTLSeconds  int    `json:"ttl_seconds"`
		}
		if json.Unmarshal(payload, &out) == nil && out.ChallengeID != "" {
			ttl := time.Duration(out.TTLSeconds) * time.Second
			if ttl <= 0 || ttl > time.Hour {
				ttl = 10 * time.Minute
			}
			h.pendingOTP.put(pendingKeyFor(ip, mac), pendingOTP{Channel: in.Channel, ChallengeID: out.ChallengeID,
				DestMasked: maskDestination(in.Channel, in.Destination), dest: strings.TrimSpace(in.Destination),
				ExpiresAt: time.Now().Add(ttl)})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

type otpVerifyIn struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

func (h *handler) authOTPVerify(w http.ResponseWriter, r *http.Request) {
	var in otpVerifyIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, 400, "bad body")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		jsonErr(w, 400, "bad ip")
		return
	}
	// The MAC comes from the kernel neighbour table and the kernel is asked to resolve it (arp_resolve.go).
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		jsonErr(w, 400, "device not on this Wi-Fi network")
		return
	}
	body, _ := json.Marshal(map[string]string{
		"ip":           ipString(ip),
		"mac":          mac.String(),
		"challenge_id": in.ChallengeID,
		"code":         in.Code,
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/sessions/authorize-otp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd authorize-otp", "err", err)
		jsonErr(w, 502, "service unavailable")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	// A verified code is authentication, not access: the same commerce session the voucher and account forms get is issued, and the page is told where to go next.
	if resp.StatusCode == http.StatusOK {
		if h.pendingOTP != nil {
			h.pendingOTP.clear(pendingKeyFor(ip, mac))
		}
		if h.tryIAMv2AuthMode(w, r, payload, true) {
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
