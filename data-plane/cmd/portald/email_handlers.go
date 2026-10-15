package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// ---- /api/auth-methods (proxy to scd) ---------------------------------------

// branding serves the hotel's published portal design to the landing page.
//
// GUEST-FACING AND THEREFORE NARROW. It reads the tenant's design from scd and hands the guest only what
// portaldesign.ForGuests lets through: known fields, each re-checked, the custom stylesheet and fragment
// rebuilt from the same allowlist edged applied on save. It used to copy scd's answer through verbatim, so a
// design that reached the database any other way -- written before the allowlist existed, restored from a
// backup -- reached every guest device exactly as stored.
//
// If the hotel has published nothing, or scd cannot answer, the portal falls back to its own defaults --
// which is why a failure here is an empty object rather than an error page. A guest must never be unable to
// sign in because a logo could not be fetched.
func (h *handler) branding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	d, ok := h.guestDesign(r.Context())
	if !ok {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"design": d})
}

func (h *handler) authMethods(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://unix/v1/tenant/auth-methods", nil)
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
	// THE DEVICE'S PENDING CODE RIDES WITH THE METHODS: the page opens on the code box when one is in flight.
	if h.pendingOTP != nil && resp.StatusCode == http.StatusOK {
		if p, ok := h.pendingOTP.get(pendingKey(r)); ok {
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

// ---- /auth/otp/request (sms | whatsapp) --------------------------------------

// THE ONE-TIME CODE IS FOR PHONES ONLY. An email address signs in as an email account with a password
// (account_handlers.go); the email code, and the temporary Internet it opened to read it, are gone (self-service
// contract section 1).
type otpReqIn struct {
	Channel     string `json:"channel"`     // "sms" | "whatsapp"
	Destination string `json:"destination"` // E.164-ish phone
}

func (h *handler) authOTPRequest(w http.ResponseWriter, r *http.Request) {
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
	// THE CODE IS BOUND TO THIS DEVICE. The hardware address comes from the kernel's neighbour table, never from
	// the client, resolved rather than only consulted (arp_resolve.go) -- the same guarantee every sign-in path
	// gives. scd records it with the challenge, and the code (or the message's link) verifies from it alone.
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
		// The origin this device reached the portal by: the message's tap-to-connect link uses it.
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
	// THE CODE IS NOW IN FLIGHT FOR THIS DEVICE. Held server-side for the code's lifetime, so a landing page
	// opened after the captive browser was dismissed reopens on the code box instead of starting over.
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
			h.pendingOTP.put(pendingKey(r), pendingOTP{Channel: in.Channel, ChallengeID: out.ChallengeID,
				DestMasked: maskDestination(in.Channel, in.Destination), dest: strings.TrimSpace(in.Destination),
				ExpiresAt: time.Now().Add(ttl)})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

// ---- /auth/otp/verify -------------------------------------------------------

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
	// The hardware address comes from the kernel's neighbour table, never from the client, and the kernel is
	// ASKED to resolve it rather than only consulted (arp_resolve.go): a cold cache is not evidence that a
	// device is off the guest network. Same mechanism, same guarantee, on every sign-in path.
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
	// A VERIFIED CODE IS AUTHENTICATION, NOT ACCESS. The IAM-v2 reply carries an auth context, and the Client
	// still has to join their live access or choose a package. This used to copy the reply through, and the
	// page -- reading the legacy session_id shape -- sent the Client to /success?s=&t=0. Now the same commerce
	// session the voucher and account forms get is issued here, and the page is told where to go next.
	if resp.StatusCode == http.StatusOK {
		if h.pendingOTP != nil {
			h.pendingOTP.clear(pendingKey(r))
		}
		if h.tryIAMv2AuthMode(w, r, payload, true) {
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

// ---- helpers ----------------------------------------------------------------

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
