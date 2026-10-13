package main

// THE portal_https HEALTH CHECK — the administrator alert the contract requires when the Client Portal is not
// served over HTTPS (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §1.6, §4).
//
// It is not a supervised SERVICE (there is no unit to restart and no process to probe), so it is not one of
// services(). It is a check over the state scd persists, reported beside the services on Diagnostics, on the
// Portal HTTPS page, and as an attention item on the Overview.
//
// Three levels:
//
//	ok        HTTPS is active.
//	warn      the HTTP fallback is in force and HTTPS has never worked here yet (typically NOT_CONFIGURED:
//	          Central has no DNS/ACME configuration; NO_HOSTNAME: no hostname allocated; or the first
//	          certificate is still on its way). Expected on a new appliance; nothing has been lost.
//	critical  the fallback is in force AFTER HTTPS had been working (a certificate was installed at some
//	          point), or a served certificate expires within 7 days and has not been renewed. Clients have lost,
//	          or are about to lose, the HTTPS portal and social sign-in.

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

// portalExpiryAlert is how close to expiry an unrenewed certificate becomes an alert. Renewal normally happens
// at two thirds of a 90-day lifetime (about 30 days left), so 7 days means renewal has failed for weeks.
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
		// "Having been active" is read from the certificates: a certificate that was ever installed means HTTPS
		// worked here. Before that, whatever the reason, the portal is still being set up.
		if !everInstalled {
			c.Level, c.Message = portalCheckWarn, base+" HTTPS has not been set up on this appliance yet."
			return c
		}
		c.Level, c.Message = portalCheckCritical, base+" HTTPS was working before and has stopped."
		return c
	}
	// HTTPS is active. A served certificate that is close to expiry and still not renewed is the warning that
	// comes before the outage.
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

// portalHTTPSHealth reads the persisted state and evaluates it. ok=false when the state is unreadable; the
// caller then reports nothing rather than a guess.
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
