package main

// Site-owned Google sign-in: the redirect host is the Site's own portal domain, whatever the canonical name is.

import (
	"strings"
	"testing"
)

// sitePortal is a portal view that knows the Site's own domain (the real agent does, fakePortal predates it).
type sitePortal struct {
	active    bool
	canonical string
	site      string
}

func (f sitePortal) PortalTLS() (bool, string) { return f.active, f.canonical }
func (f sitePortal) SiteDomain() string        { return f.site }

const sharedName = "portal.onegate.getsemantics.com"

func TestGoogleUsesTheSitesOwnDomainWhileTheSharedNameStaysCanonical(t *testing.T) {
	for _, tc := range []struct {
		name     string
		portal   sitePortal
		hosts    []string
		ok       bool
		reason   string
		redirect string
	}{
		{"the Site's domain is registered: Google returns to it, not to the shared name",
			sitePortal{true, sharedName, "wifi.site.example"}, []string{"wifi.site.example"}, true, "",
			"https://wifi.site.example/auth/social/callback"},
		{"the shared name ticked instead of the Site's domain is not enough",
			sitePortal{true, sharedName, "wifi.site.example"}, []string{sharedName}, false, reasonCallbackNotRegd, ""},
		{"no portal domain of its own: Google needs one",
			sitePortal{true, sharedName, ""}, []string{sharedName}, false, reasonNeedsSiteDomain, ""},
		{"HTTPS inactive stays the HTTP fallback",
			sitePortal{false, "", "wifi.site.example"}, []string{"wifi.site.example"}, false, reasonHTTPFallback, ""},
		{"domain case and trailing dot",
			sitePortal{true, sharedName, "WiFi.Site.example."}, []string{"wifi.site.example"}, true, "",
			"https://wifi.site.example/auth/social/callback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := socialServer(t, tc.portal, tc.hosts...)
			o := s.offerSocial("google", "http://192.0.2.1:8380/auth/social/callback")
			if o.ok != tc.ok || o.reason != tc.reason || o.redirectURI != tc.redirect {
				t.Fatalf("offer = %+v", o)
			}
		})
	}
}

func TestOnlyGoogleNeedsTheSitesOwnDomain(t *testing.T) {
	if !requiresSiteDomain("google") {
		t.Fatal("google requires a domain the Site owns")
	}
	for _, p := range []string{"apple", "microsoft", "facebook", ""} {
		if requiresSiteDomain(p) {
			t.Errorf("%q works on the portal's canonical name", p)
		}
	}
}

// The embedded-browser rule that sends Google and Facebook to "open in your browser" is tested in cmd/portald.
func TestSiteDomainAnswersOnlyWhileVerifiedWithACertificate(t *testing.T) {
	a := newPortalTLSAgent(newMemRepo(), nil, nil, "")
	a.mu.Lock()
	a.snap.CustomDomain, a.snap.CustomDomainStatus, a.snap.Hostnames = "wifi.site.example", "VERIFIED", []string{sharedName}
	a.mu.Unlock()
	if got := a.SiteDomain(); got != "" {
		t.Fatalf("no certificate installed for the domain yet: %q", got)
	}
	a.mu.Lock()
	a.snap.Hostnames = []string{sharedName, "wifi.site.example"}
	a.mu.Unlock()
	if got := a.SiteDomain(); got != "wifi.site.example" {
		t.Fatalf("verified with an installed certificate: %q", got)
	}
	a.mu.Lock()
	a.snap.CustomDomainStatus = "PENDING"
	a.mu.Unlock()
	if got := a.SiteDomain(); got != "" {
		t.Fatalf("a domain that is not verified is never used: %q", got)
	}
	if strings.Contains(sharedName, "wifi.site.example") {
		t.Fatal("test fixture sanity")
	}
}
