package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
)

// ---- /api/auth-methods (proxy to scd) ---------------------------------------

// branding serves the hotel's published portal design to the landing page.
//
// GUEST-FACING AND THEREFORE NARROW. It proxies scd, which reads the tenant's branding document; portald
// adds no interpretation. If the hotel has published nothing, or scd cannot answer, the portal falls back to
// its own defaults -- which is why a failure here is an empty object rather than an error page. A guest must
// never be unable to sign in because a logo could not be fetched.
func (h *handler) branding(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://unix/v1/tenant/branding", nil)
	resp, err := h.scd.Do(req)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		_, _ = w.Write([]byte(`{}`))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(w, resp.Body)
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
	_, _ = io.Copy(w, resp.Body)
}

// ---- /auth/otp/request (channel-agnostic: email | sms) ----------------------

type otpReqIn struct {
	Channel     string `json:"channel"`     // "email" | "sms"
	Destination string `json:"destination"` // email or E.164-ish phone
}

func (h *handler) authOTPRequest(w http.ResponseWriter, r *http.Request) {
	var in otpReqIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, 400, "bad body")
		return
	}
	switch in.Channel {
	case "email", "sms":
	default:
		jsonErr(w, 400, "channel must be email or sms")
		return
	}
	ip := clientIP(r)
	body, _ := json.Marshal(map[string]string{
		"channel":     in.Channel,
		"destination": in.Destination,
		"ip":          ipString(ip),
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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
	mac, ok := h.arpCache(ip)
	if !ok {
		jsonErr(w, 400, "device not on guest network")
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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
