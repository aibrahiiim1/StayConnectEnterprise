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

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/phone"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
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

	// CAN THIS SITE GIVE A GUEST ANYTHING AT ALL?
	//
	// Proving who a guest is and giving them internet are two different things, and a site can be perfectly
	// configured for the first while having nothing to offer for the second. When that happens the guest
	// signs in successfully and is then refused, and the portal — which had no way to tell that apart from a
	// wrong room number — told them to check their details and see reception. Reception cannot fix it; only
	// an operator publishing a package can.
	//
	// This is a SITE-LEVEL fact, computed before and independently of any identity, which is precisely what
	// makes it safe to publish to an unauthenticated portal. It reveals nothing about any guest, and it is
	// equally true for everyone on the network, so it creates no oracle for probing room numbers or names.
	// Per-guest ELIGIBILITY still decides the actual offer set, and that answer stays uniform.
	packagesAvailable := s.sitePackagesAvailable(r.Context())

	// IS THE POST-STAY SURFACE ACTUALLY SERVED? Reported from the one thing that decides it: whether this
	// daemon mounted the post-stay routes at startup, which happens only when Phase5Config.GuestOn() is true.
	//
	// The portal previously showed the Post-Stay tab whenever PMS room sign-in was on. Those are separate
	// capabilities — PMS proves an in-house guest by room and name; post-stay lets a DEPARTED guest back in
	// with a PIN — and tying one to the other put a sign-in method in front of guests that the appliance was
	// not serving. Every PIN typed into it reached a route that does not exist.
	postStayAvailable := s.p5poststay != nil

	if s.p3auth != nil {
		// The portal needs one extra fact about PMS: whether to submit the room form to the Stay-resolution
		// flow or the legacy one. It is derived from the flags this daemon started with, so a dark appliance
		// advertises nothing and the portal keeps using the path it always used.
		writeJSON(w, http.StatusOK, struct {
			*tenantcfg.AuthMethods
			Phase3PMS         bool `json:"phase3_pms"`
			PostStay          bool `json:"phase5_poststay"`
			PackagesAvailable bool `json:"internet_packages_available"`
		}{cfg, true, postStayAvailable, packagesAvailable})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*tenantcfg.AuthMethods
		PostStay          bool `json:"phase5_poststay"`
		PackagesAvailable bool `json:"internet_packages_available"`
	}{cfg, postStayAvailable, packagesAvailable})
}

// sitePackagesAvailable reports whether this site has at least one internet package a guest could be given.
//
// SYSTEM PACKAGES DO NOT COUNT. The emergency checkout-grace catalogue is a package row (is_system = true)
// that exists so a departing guest's conversion has something to reference; it is never offered on the
// portal. Counting it would report a site as ready to serve guests when the only thing it can serve is a
// grace window nobody can ask for.
//
// Fails OPEN on error, and deliberately so: this drives an advisory notice, not an authorisation decision.
// A database hiccup must not make a working portal announce that the hotel has no internet.
func (s *server) sitePackagesAvailable(ctx context.Context) bool {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM iam_v2.internet_packages p
		 WHERE p.tenant_id = $1 AND p.site_id = $2
		   AND p.active
		   AND COALESCE(p.is_system, false) = false
		   AND p.current_revision_id IS NOT NULL`, s.tenID, s.siteID).Scan(&n)
	if err != nil {
		slog.Warn("auth-methods: could not count internet packages; assuming some exist", "err", err)
		return true
	}
	return n > 0
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

	// THE CHALLENGE IS VERIFIED; THE IDENTITY IS IAM-v2's.
	//
	// public.auth_otps is a TRANSIENT challenge store and is deliberately not part of the IAM-v2 identity
	// model -- the accepted decision (D2) is that the OTP challenge is verified upstream and only the
	// verified factor crosses into iam_v2, where authOTPIdentity maps it to a principal identity. So this
	// handler verifies the code and then hands the factor to the single guest authority.
	//
	// What was REMOVED here is the superseded pipeline that followed: a legacy access-plan lookup to derive
	// duration and shaping, a public.sessions row, and direct nft/shaping calls. Duration, data cap and
	// shaping are not properties of the credential in the current model -- they come from the package the
	// guest selects in the commerce flow after authentication.
	factorType := "EMAIL"
	if v.Channel != "email" {
		factorType = "PHONE"
	}
	s.authorizeViaIAMv2(w, r, iamv2.MethodOTP, iamv2.Request{
		FactorType:  factorType,
		FactorValue: v.Destination,
		Device:      iamv2.DeviceContext{MAC: mac.String()},
	}, ip)
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
