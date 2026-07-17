package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/phone"
	"github.com/stayconnect/enterprise/data-plane/internal/session"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/voucher"
)

// ---- /v1/tenant/auth-methods ------------------------------------------------
// Read-only mirror of tenants.auth_methods with which methods this appliance
// considers active. portald polls this on landing render to decide tabs.

func (s *server) tenantAuthMethods(w http.ResponseWriter, r *http.Request) {
	cfg, err := tenantcfg.Load(r.Context(), s.db, s.tenID)
	if err != nil {
		slog.Error("auth-methods load", "err", err)
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}
	// License gating: unlicensed methods never reach the portal.
	s.applyLicenseToMethods(cfg)
	writeJSON(w, http.StatusOK, cfg)
}

// ---- /v1/auth/otp/issue ----------------------------------------------------

type otpIssueReq struct {
	Channel     string `json:"channel"`     // "email" (sms in 4.2)
	Destination string `json:"destination"` // email address
	IP          string `json:"ip"`
}

type otpIssueResp struct {
	ChallengeID string `json:"challenge_id"`
	TTLSeconds  int    `json:"ttl_seconds"`
}

func (s *server) otpIssue(w http.ResponseWriter, r *http.Request) {
	var req otpIssueReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	// License gating per channel.
	switch req.Channel {
	case "email":
		if !s.licenseGate(w, licstate.FeatEmailOTP) {
			return
		}
	case "sms":
		if !s.licenseGate(w, licstate.FeatSMSOTP) {
			return
		}
	}

	cfg, err := tenantcfg.Load(r.Context(), s.db, s.tenID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}

	// Per-channel: validate destination, resolve method config, decide sender.
	var (
		dest, sentBody string
		method         *tenantcfg.AuthMethod
		channelLabel   = req.Channel
	)
	switch req.Channel {
	case "email":
		method = cfg.Email
		d := strings.TrimSpace(strings.ToLower(req.Destination))
		if !looksLikeEmail(d) {
			httpErr(w, http.StatusBadRequest, "invalid email")
			return
		}
		dest = d
	case "sms":
		method = cfg.SMS
		d, err := phone.Normalize(req.Destination)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "invalid phone: "+err.Error())
			return
		}
		dest = d
	default:
		httpErr(w, http.StatusBadRequest, "unsupported channel")
		return
	}
	if method == nil || !method.Enabled {
		httpErr(w, http.StatusForbidden, channelLabel+" auth disabled for this tenant")
		return
	}

	// Durable throttle on issuance (authoritative; no-op unless enabled). otp.Issue also enforces its
	// own per-destination cooldown + hourly caps; this adds the restart-surviving cross-method layer.
	if ok, retry := s.throttleGuard(r.Context(), "otp", net.ParseIP(req.IP), nil, dest); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	issued, err := otp.Issue(r.Context(), s.db, s.otpRing, otp.IssueParams{
		TenantID:    s.tenID,
		ApplianceID: s.applID,
		TemplateID:  method.TemplateID,
		Channel:     channelLabel,
		Destination: dest,
		IP:          req.IP,
		UserAgent:   r.UserAgent(),
	})
	if err != nil {
		switch {
		case errors.Is(err, otp.ErrCooldown):
			httpErr(w, http.StatusTooManyRequests, "wait before requesting another code")
		case errors.Is(err, otp.ErrHourlyCap):
			httpErr(w, http.StatusTooManyRequests, "too many requests this hour")
		case errors.Is(err, otp.ErrIPRateLimited):
			httpErr(w, http.StatusTooManyRequests, "too many requests from your network")
		default:
			slog.Error("otp issue", "err", err)
			httpErr(w, http.StatusInternalServerError, "issue failed")
		}
		return
	}

	sentBody = fmt.Sprintf("Your one-time code is: %s\n\nIt expires in %d minutes.",
		issued.Code, int(otp.DefaultTTL.Minutes()))

	switch channelLabel {
	case "email":
		if err := s.mail.Send(r.Context(), mail.Message{
			To:      dest,
			Subject: "Your Wi-Fi access code",
			Text:    sentBody,
		}); err != nil {
			slog.Warn("mail send", "err", err)
		}
	case "sms":
		shortText := fmt.Sprintf("Wi-Fi code: %s (expires in %dm)", issued.Code, int(otp.DefaultTTL.Minutes()))
		if err := s.sms.Send(r.Context(), sms.Message{
			To:   dest,
			Text: shortText,
		}); err != nil {
			slog.Warn("sms send", "err", err)
		}
	}

	s.met.OTPIssued.WithLabelValues(channelLabel).Inc()
	writeJSON(w, http.StatusOK, otpIssueResp{
		ChallengeID: issued.ChallengeID,
		TTLSeconds:  int(time.Until(issued.ExpiresAt).Seconds()),
	})
}

// ---- /v1/sessions/authorize-otp --------------------------------------------

type authorizeOTPReq struct {
	IP          string `json:"ip"`
	MAC         string `json:"mac"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

func (s *server) authorizeOTP(w http.ResponseWriter, r *http.Request) {
	var req authorizeOTPReq
	if !s.licenseGate(w, "") { // channel feature was gated at issue time
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil || ip.To4() == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	mac, err := net.ParseMAC(req.MAC)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "bad mac")
		return
	}
	if ok, retry := s.throttleGuard(r.Context(), "otp", ip, mac, ""); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	v, err := otp.Verify(r.Context(), s.db, s.otpRing, req.ChallengeID, req.Code)
	if err != nil {
		result := "internal"
		switch {
		case errors.Is(err, otp.ErrNotFound):
			result = "not_found"
			httpErr(w, http.StatusNotFound, "challenge not found")
		case errors.Is(err, otp.ErrExpired):
			result = "expired"
			httpErr(w, http.StatusGone, "code expired")
		case errors.Is(err, otp.ErrAttemptsExceeded):
			result = "locked"
			httpErr(w, http.StatusForbidden, "too many wrong attempts")
		case errors.Is(err, otp.ErrAlreadyUsed):
			result = "used"
			httpErr(w, http.StatusConflict, "code already used")
		case errors.Is(err, otp.ErrCodeMismatch):
			result = "mismatch"
			httpErr(w, http.StatusBadRequest, "incorrect code")
		default:
			slog.Error("otp verify", "err", err)
			httpErr(w, http.StatusInternalServerError, "verify failed")
		}
		// Channel unknown on failure (no row matched); use "unknown".
		s.met.OTPVerify.WithLabelValues("unknown", result).Inc()
		return
	}
	s.met.OTPVerify.WithLabelValues(v.Channel, "ok").Inc()

	// Resolve template parameters (duration / data cap / shaping).
	red := &voucher.Redeemed{}
	if v.TemplateID != "" {
		r2, err := s.vou.LoadTemplate(r.Context(), v.TemplateID)
		if err == nil {
			red = r2
		}
	}

	// Atomic licensed-capacity gate + session creation (see main.go authorize).
	au, err := s.sess.StartOTP(r.Context(), mac, ip, v.Channel, v.Destination, red.DurationSeconds)
	if err != nil {
		if capErr := (*session.CapacityError)(nil); errors.As(err, &capErr) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "LICENSE_CAPACITY_REACHED", "limit": capErr.Limit, "current": capErr.Current,
			})
			return
		}
		slog.Error("session start (otp)", "err", err)
		httpErr(w, http.StatusInternalServerError, "session start failed")
		return
	}
	nc := s.resolveNetwork(r.Context(), ip)
	ttl := time.Duration(red.DurationSeconds) * time.Second
	if err := s.nft.Allow(r.Context(), nc.Bridge, ip, ttl); err != nil {
		slog.Error("nft allow", "err", err)
		_ = s.sess.End(context.Background(), ip, "policy")
		httpErr(w, http.StatusInternalServerError, "nft allow failed")
		return
	}
	if err := s.shp.AddSession(r.Context(), nc.Bridge, ip, red.DownKbps, red.UpKbps); err != nil {
		slog.Error("shape add", "err", err)
		_ = s.nft.Deny(context.Background(), nc.Bridge, ip)
		_ = s.sess.End(context.Background(), ip, "policy")
		httpErr(w, http.StatusInternalServerError, "shape add failed")
		return
	}
	s.recordSessionNetwork(r.Context(), au.SessionID, nc)
	s.met.SessionsStarted.WithLabelValues("otp").Inc()

	resp := authorizeResp{
		SessionID:       au.SessionID,
		GuestID:         au.GuestID,
		DurationSeconds: red.DurationSeconds,
	}
	if au.ExpiresAt != nil {
		resp.ExpiresAt = au.ExpiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- helpers ---------------------------------------------------------------

func looksLikeEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\n") {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}
