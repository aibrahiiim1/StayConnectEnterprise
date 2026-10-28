package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/phone"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
)

// Read-only mirror of tenants.auth_methods with the methods this appliance considers active; portald polls it to decide tabs.

func (s *server) tenantAuthMethods(w http.ResponseWriter, r *http.Request) {
	cfg, err := tenantcfg.Load(r.Context(), s.db, s.tenID)
	if err != nil {
		slog.Error("auth-methods load", "err", err)
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}
	// Unlicensed methods never reach the portal, nor do code or social methods nothing can deliver (provider readiness).
	s.applyLicenseToMethods(cfg)
	s.applyProviderReadiness(r.Context(), cfg)
	// A social provider is listed only while it can be started: HTTPS active and the canonical hostname registered in the hotel's app. Under the HTTP fallback the buttons go.
	s.applySocialOffering(cfg)
	// A client on a network that narrows the site's sign-in sees only what that network offers.
	if ip := net.ParseIP(r.URL.Query().Get("ip")); ip != nil {
		if nc := s.resolveNetwork(r.Context(), ip); nc.NetworkID != "" {
			ov, oerr := s.networkSigninFor(r.Context(), nc.NetworkID)
			if oerr != nil {
				slog.Error("auth-methods: network sign-in settings unreadable", "err", oerr)
				httpErr(w, http.StatusInternalServerError, "network sign-in settings unavailable")
				return
			}
			ov.apply(cfg)
		}
	}

	// Can this site give a guest anything at all? A site can authenticate guests while having no package to give, and the portal
	// would tell them to check their details. This is a site-level fact, computed independent of any identity, so it is safe to
	// publish unauthenticated and creates no oracle for probing rooms or names; per-guest eligibility still decides the offer set uniformly.
	packagesAvailable := s.sitePackagesAvailable(r.Context())

	// Whether the post-stay surface is served: reported from whether this daemon mounted the post-stay routes at startup
	// (Phase5Config.GuestOn()), not from PMS room sign-in, which is a separate capability.
	postStayAvailable := s.p5poststay != nil

	if s.p3auth != nil {
		// Whether the portal submits the room form to the Stay-resolution flow or the legacy one; derived from startup flags, so a dark appliance advertises nothing.
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

// sitePackagesAvailable reports whether the site has at least one internet package a guest could be given. System packages
// (is_system, the checkout-grace catalogue) do not count: they are never offered on the portal.
// Fails open on error, deliberately: this drives an advisory notice, and a database hiccup must not make a working portal announce that the hotel has no internet.
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

type otpIssueReq struct {
	// Channel is "sms" or "whatsapp"; the email channel is removed (Email accounts replace it) and is refused as unsupported.
	Channel     string `json:"channel"`
	Destination string `json:"destination"` // a phone number (normalised to E.164)
	IP          string `json:"ip"`
	// MAC is the requesting device's hardware address as portald resolved it from the kernel; the challenge is bound to it
	// (migration 0110), so the code and its SMS link verify from this device only.
	MAC string `json:"mac"`
	// An older portald also sends portal_base (a guest-supplied Host header); unknown fields are ignored, and the link origin is
	// always the canonical HTTPS hostname scd established (otpLinkBase).
}

// otpLinkBase is https://<canonical hostname> while the portal is on HTTPS and "" (code alone) on the HTTP fallback: a
// plain-HTTP link would teach clients to follow http:// links from a message, and social sign-in exists only on HTTPS.
func otpLinkBase(st PortalTLSState) string {
	if !st.HTTPSActive || st.Canonical == "" {
		return ""
	}
	return "https://" + st.Canonical
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
	switch req.Channel {
	case "sms":
		if !s.licenseGate(w, licstate.FeatSMSOTP) {
			return
		}
		if ip := net.ParseIP(req.IP); ip != nil && !s.networkOffersIP(r.Context(), ip, keySMS) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_NOT_OFFERED_ON_NETWORK"})
			return
		}
	case "whatsapp":
		if !s.licenseGate(w, licstate.FeatWhatsAppOTP) {
			return
		}
		if ip := net.ParseIP(req.IP); ip != nil && !s.networkOffersIP(r.Context(), ip, keyWhatsApp) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_NOT_OFFERED_ON_NETWORK"})
			return
		}
	}

	var cfg *tenantcfg.AuthMethods
	var err error
	if s.methodSwitches != nil {
		cfg, err = s.methodSwitches(r.Context())
	} else {
		cfg, err = tenantcfg.Load(r.Context(), s.db, s.tenID)
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}

	var (
		dest         string
		method       *tenantcfg.AuthMethod
		channelLabel = req.Channel
	)
	switch req.Channel {
	case "sms":
		method = cfg.SMS
		d, err := phone.Normalize(req.Destination)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "invalid phone: "+err.Error())
			return
		}
		dest = d
	case "whatsapp":
		// WhatsApp is a different channel from SMS: its own method switch, provider and approved template.
		method = cfg.WhatsApp
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
	// A code is issued to a device or not at all (migration 0110): without the kernel-resolved MAC an unbound code would verify from any device, so the issue is refused.
	devHW, merr := net.ParseMAC(strings.TrimSpace(req.MAC))
	if merr != nil || net.ParseIP(req.IP) == nil {
		httpErr(w, http.StatusBadRequest, "device identity required")
		return
	}
	devMAC := devHW.String()
	// A code is issued only when an enabled sender exists for the channel: it is never "sent" to nowhere.
	if s.providerReadiness != nil || s.db != nil {
		if channels, _, ok := s.readProviderReadiness(r.Context()); !ok || !channels[req.Channel] {
			httpErr(w, http.StatusServiceUnavailable, channelLabel+" codes cannot be sent: no sender is configured")
			return
		}
	}

	// Durable throttle on issuance (no-op unless enabled); otp.Issue also enforces its own per-destination cooldown and hourly caps.
	if ok, retry := s.throttleGuard(r.Context(), "otp", net.ParseIP(req.IP), nil, dest); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	issue := s.otpIssuer
	if issue == nil {
		issue = func(ctx context.Context, p otp.IssueParams) (*otp.Issued, error) {
			return otp.Issue(ctx, s.db, s.otpRing, p)
		}
	}
	issued, err := issue(r.Context(), otp.IssueParams{
		TenantID:    s.tenID,
		ApplianceID: s.applID,
		TemplateID:  method.TemplateID,
		Channel:     channelLabel,
		Destination: dest,
		IP:          req.IP,
		UserAgent:   r.UserAgent(),
		DeviceMAC:   devMAC,
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

	// The tap-to-connect link opens the portal in the phone's real browser on a page with one Connect button; opening it verifies
	// nothing. The code in it is the same single-use ten-minute secret. Only while the portal is on HTTPS; the HTTP fallback sends the code alone.
	link := ""
	if base := otpLinkBase(s.portalTLS.State()); base != "" {
		link = fmt.Sprintf("%s/connect?c=%s&k=%s", base, url.QueryEscape(issued.ChallengeID), url.QueryEscape(issued.Code))
	}

	// A failed send is a failure: the sender's error is recorded on the provider row (Delivery page), and the client is told
	// the code could not be sent with no detail that would reveal whether the number is known.
	_, smsSender, waSender := s.senders()
	var sendErr error
	switch channelLabel {
	case "sms":
		shortText := fmt.Sprintf("Wi-Fi code: %s (expires in %dm)", issued.Code, int(otp.DefaultTTL.Minutes()))
		if link != "" {
			shortText += " " + link
		}
		sendErr = smsSender.Send(r.Context(), sms.Message{
			To:   dest,
			Text: shortText,
		})
	case "whatsapp":
		// The code travels as a parameter of the provider-approved authentication template, never as text.
		if waSender == nil {
			sendErr = errors.New("no whatsapp sender configured")
		} else {
			sendErr = waSender.Send(r.Context(), whatsapp.Message{
				To:         dest,
				Code:       issued.Code,
				TTLMinutes: int(otp.DefaultTTL.Minutes()),
			})
		}
	}
	if sendErr != nil {
		slog.Warn("otp delivery failed", "channel", channelLabel, "err", sendErr)
		s.met.OTPIssued.WithLabelValues(channelLabel + "_undelivered").Inc()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "DELIVERY_FAILED"})
		return
	}

	s.met.OTPIssued.WithLabelValues(channelLabel).Inc()
	// A sealed copy of the sent code, readable only by an operator with the code-reveal permission, for a client whose message does not arrive. Gone when the code is used or expires.
	s.escrowOTPCode(r.Context(), issued.ChallengeID, issued.Code, issued.ExpiresAt)
	writeJSON(w, http.StatusOK, otpIssueResp{
		ChallengeID: issued.ChallengeID,
		TTLSeconds:  int(time.Until(issued.ExpiresAt).Seconds()),
	})
}

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

	// From this device only: the challenge was bound at issue to the MAC the kernel resolved (migration 0110); another device
	// presenting the code is refused before the compare and counts as no attempt.
	v, err := otp.VerifyFrom(r.Context(), s.db, s.otpRing, req.ChallengeID, req.Code, mac.String())
	if err != nil {
		result := "internal"
		switch {
		case errors.Is(err, otp.ErrOtherDevice):
			result = "other_device"
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "OTHER_DEVICE"})
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
	// An email code no longer signs anybody in: one issued before this software can still be pending, is consumed above and
	// then refused, since mapping it onto a PHONE factor would misstate what was proven.
	if otpFactorType(v.Channel) == "" {
		s.met.OTPVerify.WithLabelValues(v.Channel, "channel_removed").Inc()
		writeJSON(w, http.StatusGone, map[string]any{"error": "CHANNEL_REMOVED"})
		return
	}
	s.met.OTPVerify.WithLabelValues(v.Channel, "ok").Inc()
	s.dropOTPEscrow(r.Context(), req.ChallengeID)
	// Staff-assisted: the code was shown to the client by staff, so the sign-in may create a new Client marked staff_assisted
	// and never reaches an existing one. Fails closed: an unreadable record is treated as staff-assisted.
	assisted := true
	if s.db != nil {
		var revealed bool
		if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM iam_v2.otp_code_reveals WHERE challenge_id=$1::uuid)`,
			req.ChallengeID).Scan(&revealed); err == nil {
			assisted = revealed
		}
	}
	credential := iamv2.CredentialForOTP(v.Channel)
	if assisted {
		credential = iamv2.AssistedCredential(credential)
	}

	// The challenge is verified here; identity is IAM-v2's. public.auth_otps is a transient challenge store outside the identity
	// model: only the verified factor crosses into iam_v2 (authOTPIdentity). Duration, data cap and shaping come from the
	// package the guest selects after authentication, not from the credential.
	// The channel the code came by is the method that must be offered on this network.
	if v.Channel == "sms" {
		r = withMethodKey(r, keySMS)
	} else if v.Channel == "whatsapp" {
		r = withMethodKey(r, keyWhatsApp)
	}
	s.authorizeViaIAMv2(w, r, iamv2.MethodOTP, iamv2.Request{
		FactorType:    otpFactorType(v.Channel),
		FactorValue:   v.Destination,
		Credential:    credential,
		StaffAssisted: assisted,
		Device:        iamv2.DeviceContext{MAC: mac.String()},
	}, ip)
}

// otpFactorType maps a verified challenge's channel to the IAM-v2 factor it proves: SMS and WhatsApp codes both prove
// possession of a phone number (PHONE); any other channel, email above all, proves nothing and answers "".
func otpFactorType(channel string) string {
	switch channel {
	case "sms", "whatsapp":
		return "PHONE"
	}
	return ""
}

// tenantBranding serves the portal design to the captive portal: the published design only, never the draft or revision history. Which design is decided
// by the network the client is on (the portal passes the connection's source address; a client cannot name its own network), falling back to the site's default design.
// Failure is an empty object, not an error, because a guest must never be unable to sign in over branding.
func (s *server) tenantBranding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	empty := func() { _, _ = w.Write([]byte(`{}`)) }
	if s.tenID == "" {
		empty()
		return
	}
	raw, err := s.portalDesignDoc(r)
	if err != nil {
		slog.Warn("portal branding unavailable; serving defaults", "err", err)
		empty()
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		empty()
		return
	}
	// The document holds the published design, the operator's draft and the revision history; only the first is the guest's business.
	out := map[string]any{}
	if d, ok := doc["design"]; ok {
		out["design"] = d
	} else {
		// A document written before designs were versioned is the design.
		delete(doc, "draft")
		delete(doc, "revisions")
		out["design"] = doc
	}
	b, err := json.Marshal(out)
	if err != nil {
		empty()
		return
	}
	_, _ = w.Write(b)
}

var profileUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// portalDesignDoc returns the stored design document for the profile that applies: the one named for a preview (only the local portal can ask for it), else the
// profile assigned to the network that owns the client's address, else the site default.
func (s *server) portalDesignDoc(r *http.Request) ([]byte, error) {
	var raw []byte
	if id := r.URL.Query().Get("profile"); id != "" {
		if !profileUUID.MatchString(id) {
			return nil, errors.New("not a profile id")
		}
		err := s.db.QueryRow(r.Context(), `SELECT doc FROM iam_v2.portal_profiles WHERE id=$1::uuid AND tenant_id=$2::uuid`, id, s.tenID).Scan(&raw)
		return raw, err
	}
	if ip := net.ParseIP(r.URL.Query().Get("ip")); ip != nil {
		if nc := s.resolveNetwork(r.Context(), ip); nc.NetworkID != "" {
			err := s.db.QueryRow(r.Context(), `
				SELECT p.doc FROM iam_v2.portal_profile_assignments a JOIN iam_v2.portal_profiles p ON p.id = a.profile_id
				 WHERE a.network_id = $1::uuid AND p.tenant_id = $2::uuid`, nc.NetworkID, s.tenID).Scan(&raw)
			if err == nil {
				return raw, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}
	}
	err := s.db.QueryRow(r.Context(), `SELECT COALESCE(branding, '{}'::jsonb) FROM tenants WHERE id = $1`, s.tenID).Scan(&raw)
	return raw, err
}
