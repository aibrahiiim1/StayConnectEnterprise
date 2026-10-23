package main

// portal_https health check over the state scd persists (not a supervised service).
// Levels: ok = HTTPS active; warn = HTTP fallback and HTTPS never worked yet (expected on a new appliance);
// critical = fallback after HTTPS had worked, or a served certificate expires within 7 days unrenewed.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	portalCheckOK       = "ok"
	portalCheckWarn     = "warn"
	portalCheckCritical = "critical"
)

// portalExpiryAlert: renewal normally happens ~30 days before expiry, so 7 days left means renewal has failed.
const portalExpiryAlert = 7 * 24 * time.Hour

type portalHTTPSCheck struct {
	Name    string `json:"name"`
	Level   string `json:"level"` // ok | warn | critical
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

// portalFallbackWords says what each fallback reason means to an administrator.
var portalFallbackWords = map[string]string{
	"NOT_CONFIGURED":            "OneGate Central has not been set up to issue portal certificates yet",
	"NO_HOSTNAME":               "this appliance has not been given its portal hostname yet",
	"NO_CERTIFICATE":            "no valid certificate is installed for the portal hostname",
	"CERTIFICATE_EXPIRED":       "the portal certificate has expired",
	"PORTAL_ADDRESS_NOT_ACTIVE": "the portal address is not active on the appliance (apply the network configuration)",
	"CENTRAL_UNREACHABLE":       "OneGate Central cannot be reached to obtain a certificate",
}

func portalFallbackWord(reason string) string {
	if w, ok := portalFallbackWords[reason]; ok {
		return w
	}
	if reason == "" {
		return "the reason was not recorded"
	}
	return strings.ToLower(strings.ReplaceAll(reason, "_", " "))
}

// evaluatePortalHTTPS is pure: the state, the certificate rows and the time decide the answer.
func evaluatePortalHTTPS(st portalTLSState, certs []portalCertificate, now time.Time) portalHTTPSCheck {
	c := portalHTTPSCheck{Name: "portal_https"}
	everInstalled := false
	for _, ce := range certs {
		if ce.InstalledAt != nil {
			everInstalled = true
		}
	}
	if !st.HTTPSActive {
		c.Reason = st.FallbackReason
		since := ""
		if st.FallbackSince != nil {
			since = " since " + st.FallbackSince.UTC().Format("2006-01-02 15:04 UTC")
		}
		base := "The Client Portal is on the HTTP fallback" + since + ": " + portalFallbackWord(st.FallbackReason) +
			". Clients can still sign in with a code; emails carry the code only and social sign-in is not offered."
		// Any certificate ever installed means HTTPS worked here; before that the portal is still being set up.
		if !everInstalled {
			c.Level, c.Message = portalCheckWarn, base+" HTTPS has not been set up on this appliance yet."
			return c
		}
		c.Level, c.Message = portalCheckCritical, base+" HTTPS was working before and has stopped."
		return c
	}
	// HTTPS is active; a near-expiry unrenewed certificate warns before the outage.
	for _, ce := range certs {
		if ce.InstalledAt == nil || ce.NotAfter == nil {
			continue
		}
		left := ce.NotAfter.Sub(now)
		if left > portalExpiryAlert {
			continue
		}
		c.Level, c.Reason = portalCheckCritical, "CERTIFICATE_EXPIRING"
		c.Message = fmt.Sprintf("The certificate for %s expires %s and has not been renewed.",
			ce.Hostname, ce.NotAfter.UTC().Format("2006-01-02 15:04 UTC"))
		if ce.LastError != "" {
			c.Message += " Last renewal error: " + ce.LastError + "."
		}
		return c
	}
	c.Level = portalCheckOK
	c.Message = "The Client Portal is served over HTTPS as " + st.CanonicalHostname + "."
	return c
}

// portalHTTPSHealth returns ok=false when the state is unreadable; the caller then reports nothing.
func (s *server) portalHTTPSHealth(ctx context.Context) (portalHTTPSCheck, bool) {
	if s.db == nil {
		return portalHTTPSCheck{}, false
	}
	st, err := s.loadPortalTLSState(ctx)
	if err != nil {
		return portalHTTPSCheck{}, false
	}
	certs, err := s.loadPortalCertificates(ctx)
	if err != nil {
		return portalHTTPSCheck{}, false
	}
	return evaluatePortalHTTPS(st, certs, time.Now()), true
}

// portalHTTPSAttention turns a non-ok check into the Overview's attention item.
func portalHTTPSAttention(c *portalHTTPSCheck) (attentionItem, bool) {
	if c == nil || c.Level == portalCheckOK || c.Level == "" {
		return attentionItem{}, false
	}
	it := attentionItem{ID: "portal-https", Severity: "warn", Href: "/portal-https", Action: "Open Portal HTTPS",
		Detail: c.Message}
	if c.Level == portalCheckCritical {
		it.Severity = "err"
		it.Title = "The Client Portal is not served over HTTPS"
		if c.Reason == "CERTIFICATE_EXPIRING" {
			it.Title = "A portal certificate is about to expire"
		}
	} else {
		it.Title = "Portal HTTPS is not set up yet"
	}
	return it, true
}
