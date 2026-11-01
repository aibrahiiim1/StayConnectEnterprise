package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const tShared = "portal.onegate.getsemantics.com"

// The shared Client Portal name is canonical and the Site has its own verified portal domain with a certificate.
var sharedWithSiteDomain = portalTLSState{HTTPSActive: true, Canonical: tShared, OneGateHostname: tOG,
	CustomDomain: tCustom, CustomDomainStatus: "VERIFIED", Hostnames: []string{tShared, tOG, tCustom}, PortalIP: "10.255.255.1"}

// Review finding (PR #286): Google returns to the Site's domain and the journey goes on there; sending the Client to the
// canonical name would lose the host-only commerce cookie and ask them to sign in again.
func TestAJourneyOnTheSitesOwnDomainIsNotMovedToTheCanonicalName(t *testing.T) {
	h := httpsHandler(t, sharedWithSiteDomain, nil)
	reached := ""
	front := h.tlsFront(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = requestHost(r) + r.URL.Path }))
	for _, tc := range []struct {
		host, path, wantReached, wantRedirect string
	}{
		{tCustom, "/packages", tCustom + "/packages", ""},
		{tCustom, "/auth/social/callback", tCustom + "/auth/social/callback", ""},
		{tOG, "/packages", "", "https://" + tShared + "/packages"}, // OneGate's own technical name still goes to canonical
		{tShared, "/packages", tShared + "/packages", ""},
	} {
		reached = ""
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "https://"+tc.host+tc.path, nil)
		front.ServeHTTP(w, r)
		if tc.wantRedirect != "" {
			if w.Code != http.StatusFound || w.Header().Get("Location") != tc.wantRedirect {
				t.Errorf("%s%s: want a redirect to %s, got %d %q", tc.host, tc.path, tc.wantRedirect, w.Code, w.Header().Get("Location"))
			}
			continue
		}
		if reached != tc.wantReached || w.Code >= 300 {
			t.Errorf("%s%s: reached %q (%d)", tc.host, tc.path, reached, w.Code)
		}
	}
}

// A domain that is not verified, or whose certificate is not installed, is never treated as the Site's own origin.
func TestOnlyAVerifiedSiteDomainWithACertificateIsKeptInPlace(t *testing.T) {
	st := sharedWithSiteDomain
	st.Hostnames = []string{tShared, tOG}
	if st.isSiteDomain(tCustom) {
		t.Fatal("no certificate for the domain: it is not served at all, so never kept in place")
	}
	st = sharedWithSiteDomain
	st.CustomDomainStatus = "PENDING"
	if st.isSiteDomain(tCustom) {
		t.Fatal("a domain that is not verified is never kept in place")
	}
	st = sharedWithSiteDomain
	st.CustomDomain = ""
	if st.isSiteDomain(tCustom) || st.isSiteDomain("") {
		t.Fatal("no portal domain configured")
	}
	if !sharedWithSiteDomain.isSiteDomain(tCustom) || sharedWithSiteDomain.isSiteDomain(tShared) || sharedWithSiteDomain.isSiteDomain(tOG) {
		t.Fatal("only the Site's own domain qualifies, never a OneGate name")
	}
}

// A card payment started on the Site's domain returns to it (the cookie lives there); elsewhere the canonical name.
func TestReturnBaseKeepsTheSitesOwnDomain(t *testing.T) {
	h := httpsHandler(t, sharedWithSiteDomain, nil)
	onSite := httptest.NewRequest(http.MethodGet, "https://"+tCustom+"/packages", nil)
	if got := h.returnBase(onSite); got != "https://"+tCustom {
		t.Errorf("on the Site's domain: %q", got)
	}
	onShared := httptest.NewRequest(http.MethodGet, "https://"+tShared+"/packages", nil)
	if got := h.returnBase(onShared); got != "https://"+tShared {
		t.Errorf("on the shared name: %q", got)
	}
}
