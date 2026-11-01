package main

import (
	"strings"
	"testing"
)

func stepByID(steps []socialSetupStep, id string) *socialSetupStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

func TestGoogleSetupChecklist(t *testing.T) {
	ready := portalTLSState{HTTPSActive: true, CanonicalHostname: "portal.onegate.getsemantics.com", SharedHostname: "portal.onegate.getsemantics.com",
		CustomDomain: "WiFi.Site.example", CustomDomainStatus: "VERIFIED", SiteDomainUsable: true}
	good := edgeSocialProvider{Provider: "google", ClientID: "123-abc.apps.googleusercontent.com", SecretSet: true,
		CallbackHosts: []string{"wifi.site.example"}}

	t.Run("everything this appliance can check is done", func(t *testing.T) {
		steps := googleSetupSteps(good, ready)
		for _, id := range []string{"site_domain", "https", "client_id", "client_secret", "callback_ticked"} {
			if s := stepByID(steps, id); s == nil || s.State != "ok" {
				t.Errorf("%s: %+v", id, s)
			}
		}
		// What only Google can confirm is said to be unchecked, never reported as done.
		for _, id := range []string{"google_redirect", "google_domain", "google_publish"} {
			if s := stepByID(steps, id); s == nil || s.State != "manual" || !strings.Contains(s.Detail, "OneGate cannot check") && id != "google_redirect" {
				t.Errorf("%s must be a manual step: %+v", id, s)
			}
		}
		if s := stepByID(steps, "google_redirect"); !strings.Contains(s.Detail, "https://wifi.site.example/auth/social/callback") {
			t.Errorf("the exact redirect URI is shown, lower-cased: %q", s.Detail)
		}
	})

	t.Run("no portal domain of its own", func(t *testing.T) {
		st := ready
		st.CustomDomain, st.CustomDomainStatus, st.SiteDomainUsable = "", "", false
		steps := googleSetupSteps(good, st)
		s := stepByID(steps, "site_domain")
		if s.State != "todo" || s.Href != "/portal-https" || !strings.Contains(s.Detail, "domain of yours") {
			t.Errorf("%+v", s)
		}
		if stepByID(steps, "callback_ticked") != nil || stepByID(steps, "google_redirect") != nil {
			t.Error("without a domain there is no callback or redirect URI to show")
		}
	})

	t.Run("domain not verified, with Central's reason", func(t *testing.T) {
		st := ready
		st.CustomDomainStatus, st.CustomDomainError, st.SiteDomainUsable = "PENDING", "CNAME wifi.site.example is not published yet", false
		s := stepByID(googleSetupSteps(good, st), "site_domain")
		if s.State != "todo" || !strings.Contains(s.Detail, "not published yet") {
			t.Errorf("%+v", s)
		}
	})

	t.Run("verified, certificate pending", func(t *testing.T) {
		st := ready
		st.SiteDomainUsable = false
		s := stepByID(googleSetupSteps(good, st), "site_domain")
		if s.State != "todo" || !strings.Contains(s.Title, "Waiting for the certificate") {
			t.Errorf("%+v", s)
		}
	})

	t.Run("the portal is on the HTTP fallback", func(t *testing.T) {
		st := ready
		st.HTTPSActive = false
		if s := stepByID(googleSetupSteps(good, st), "https"); s.State != "todo" {
			t.Errorf("%+v", s)
		}
	})

	t.Run("client id and secret", func(t *testing.T) {
		for name, tc := range map[string]struct {
			id     string
			secret bool
			cid    string
			sec    string
		}{
			"empty":       {"", false, "todo", "todo"},
			"wrong shape": {"my-app", true, "todo", "ok"},
			"good":        {"1-x.apps.googleusercontent.com", true, "ok", "ok"},
		} {
			p := good
			p.ClientID, p.SecretSet = tc.id, tc.secret
			steps := googleSetupSteps(p, ready)
			if stepByID(steps, "client_id").State != tc.cid || stepByID(steps, "client_secret").State != tc.sec {
				t.Errorf("%s: %+v / %+v", name, stepByID(steps, "client_id"), stepByID(steps, "client_secret"))
			}
		}
	})

	t.Run("the callback is not marked as registered", func(t *testing.T) {
		p := good
		p.CallbackHosts = []string{"portal.onegate.getsemantics.com"}
		s := stepByID(googleSetupSteps(p, ready), "callback_ticked")
		if s.State != "todo" || !strings.Contains(s.Detail, "https://wifi.site.example/auth/social/callback") {
			t.Errorf("%+v", s)
		}
	})

	t.Run("every step that needs action says what to do", func(t *testing.T) {
		st := ready
		st.CustomDomain, st.CustomDomainStatus, st.SiteDomainUsable, st.HTTPSActive = "", "", false, false
		for _, s := range googleSetupSteps(edgeSocialProvider{Provider: "google"}, st) {
			if s.State == "todo" && (strings.TrimSpace(s.Detail) == "" || strings.TrimSpace(s.Title) == "") {
				t.Errorf("a step that needs action must explain it: %+v", s)
			}
		}
	})
}

// Only Google gets the checklist.
func TestOnlyGoogleCarriesASetupChecklist(t *testing.T) {
	st := portalTLSState{HTTPSActive: true, CanonicalHostname: "wifi.site.example"}
	for _, provider := range []string{"apple", "microsoft", "facebook"} {
		p := edgeSocialProvider{Provider: provider}
		decorateSocial(&p, st)
		if len(p.Setup) != 0 {
			t.Errorf("%s: %v", provider, p.Setup)
		}
	}
	g := edgeSocialProvider{Provider: "google"}
	decorateSocial(&g, st)
	if len(g.Setup) == 0 {
		t.Error("google carries its checklist")
	}
}
