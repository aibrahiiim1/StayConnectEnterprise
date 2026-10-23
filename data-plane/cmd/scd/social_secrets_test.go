package main

// Sealed secrets, the offering rule and the attempt redirect URI (social_secrets.go); database-free.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

const (
	socTenant   = "22222222-2222-2222-2222-222222222222"
	socProvider = "0f8b9a0e-1111-4111-8111-000000000001"
)

func socialKeyForTest(t *testing.T) sealbox.Key {
	t.Helper()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	k, err := sealbox.NewKey(socialDomain, raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The AAD names the owner: a ciphertext opens only for the tenant, provider row and generation it was sealed for, under the social key (not the notification key).
func TestSocialSecretSealingIsBoundToTenantProviderAndGeneration(t *testing.T) {
	k := socialKeyForTest(t)
	sl, err := sealSocial(k, socTenant, socProvider, 3, "GOCSPX-hotel-secret")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sl.Ciphertext, []byte("GOCSPX")) {
		t.Fatal("the ciphertext carries the plaintext")
	}
	if got, err := openSocial(k, socTenant, socProvider, 3, sl); err != nil || got != "GOCSPX-hotel-secret" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	for name, open := range map[string]func() (string, error){
		"another tenant": func() (string, error) {
			return openSocial(k, "33333333-3333-3333-3333-333333333333", socProvider, 3, sl)
		},
		"another provider":   func() (string, error) { return openSocial(k, socTenant, "0f8b9a0e-1111-4111-8111-000000000002", 3, sl) },
		"another generation": func() (string, error) { return openSocial(k, socTenant, socProvider, 2, sl) },
		"another key":        func() (string, error) { return openSocial(socialKeyForTest(t), socTenant, socProvider, 3, sl) },
		"the notify domain": func() (string, error) {
			// Same key bytes, different domain: what the notification sealing would produce.
			nk, _ := sealbox.NewKey(notifyDomain, bytes.Repeat([]byte{1}, 32))
			sk, _ := sealbox.NewKey(socialDomain, bytes.Repeat([]byte{1}, 32))
			s2, _ := sealSocial(sk, socTenant, socProvider, 3, "x")
			return openSocial(nk, socTenant, socProvider, 3, s2)
		},
		"tampered": func() (string, error) {
			bad := sl
			bad.Ciphertext = append([]byte(nil), sl.Ciphertext...)
			bad.Ciphertext[0] ^= 1
			return openSocial(k, socTenant, socProvider, 3, bad)
		},
	} {
		if got, err := open(); err == nil {
			t.Errorf("%s: opened (%q)", name, got)
		}
	}
}

type fakePortal struct {
	active    bool
	canonical string
}

func (f fakePortal) PortalTLS() (bool, string) { return f.active, f.canonical }

func socialServer(t *testing.T, portal portalHosts, hosts ...string) *server {
	t.Helper()
	g, err := social.NewGoogle("cid.apps.googleusercontent.com", "sec", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := social.NewRegistry()
	reg.Register(g)
	s := &server{socialPortal: portal, tenID: socTenant}
	s.socialSet.Store(&socialSet{reg: reg, hosts: map[string][]string{"google": hosts}, real: map[string]bool{"google": true}})
	return s
}

func TestARealProviderIsOfferedOnlyOverHTTPSOnARegisteredCallbackHost(t *testing.T) {
	for _, tc := range []struct {
		name       string
		portal     portalHosts
		hosts      []string
		ok         bool
		reason     string
		redirectTo string
	}{
		{"http fallback (accessor not wired)", nil, []string{"wifi.hotel.example"}, false, reasonHTTPFallback, ""},
		{"http fallback", noPortalHTTPS{}, []string{"wifi.hotel.example"}, false, reasonHTTPFallback, ""},
		{"active but no canonical", fakePortal{active: true}, []string{"wifi.hotel.example"}, false, reasonHTTPFallback, ""},
		{"canonical not registered", fakePortal{true, "ab12cd34.gw.onegate.work"}, []string{"wifi.hotel.example"}, false, reasonCallbackNotRegd, ""},
		{"nothing registered", fakePortal{true, "wifi.hotel.example"}, nil, false, reasonCallbackNotRegd, ""},
		{"registered", fakePortal{true, "wifi.hotel.example"}, []string{"ab12cd34.gw.onegate.work", "wifi.hotel.example"}, true, "",
			"https://wifi.hotel.example/auth/social/callback"},
		{"canonical case and trailing dot", fakePortal{true, "WiFi.Hotel.example."}, []string{"wifi.hotel.example"}, true, "",
			"https://wifi.hotel.example/auth/social/callback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := socialServer(t, tc.portal, tc.hosts...)
			// The portal's own Host-derived URL is passed and must be IGNORED for a real provider.
			o := s.offerSocial("google", "http://192.0.2.1:8380/auth/social/callback")
			if o.ok != tc.ok || o.reason != tc.reason || o.redirectURI != tc.redirectTo {
				t.Fatalf("offer = %+v", o)
			}
		})
	}
	s := socialServer(t, fakePortal{true, "wifi.hotel.example"}, "wifi.hotel.example")
	if o := s.offerSocial("facebook", ""); o.ok || o.reason != reasonProviderNotReady {
		t.Errorf("a provider that was not built is not offered: %+v", o)
	}
}

// The development stub keeps working over plain HTTP through the portal's own callback; it is only in the registry of a development build.
func TestTheDevelopmentStubIsAllowedOverHTTP(t *testing.T) {
	s := &server{socialPortal: noPortalHTTPS{}}
	s.socialBase = []social.Provider{&social.Stub{ProviderName: "google", AuthorizeBase: "http://portal/api/oauth/stub/authorize"}}
	o := s.offerSocial("google", "http://portal.local:8380/auth/social/callback")
	if !o.ok || !o.stub || o.redirectURI != "http://portal.local:8380/auth/social/callback" {
		t.Fatalf("stub offer = %+v", o)
	}
	cfg := &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": {Enabled: true}}}
	s.applySocialOffering(cfg)
	if cfg.Social["google"] == nil {
		t.Fatal("the stub button disappeared in a development build")
	}
}

func TestThePortalListsOnlyStartableProviders(t *testing.T) {
	on := func() *tenantcfg.AuthMethod { return &tenantcfg.AuthMethod{Enabled: true} }
	cfg := &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": on(), "apple": on()}}
	socialServer(t, noPortalHTTPS{}, "wifi.hotel.example").applySocialOffering(cfg)
	if cfg.Social != nil {
		t.Fatalf("under the HTTP fallback no social button is listed: %v", cfg.Social)
	}
	cfg = &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": on(), "apple": on()}}
	socialServer(t, fakePortal{true, "wifi.hotel.example"}, "wifi.hotel.example").applySocialOffering(cfg)
	if len(cfg.Social) != 1 || cfg.Social["google"] == nil {
		t.Fatalf("want google only (apple has no built provider): %v", cfg.Social)
	}
}

// socialStart refuses with a reason code, before any throttle charge or state row, when the provider may not be started.
func TestSocialStartRefusesWithAReasonCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		portal portalHosts
		want   string
	}{
		{"http fallback", noPortalHTTPS{}, reasonHTTPFallback},
		{"callback not registered", fakePortal{true, "ab12cd34.gw.onegate.work"}, reasonCallbackNotRegd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := socialServer(t, tc.portal, "wifi.hotel.example")
			s.lic = licenceWithModules(t, lic.ModuleSocialLogin)
			s.met = metrics.New("test", prometheus.Labels{})
			s.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
				return &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": {Enabled: true}}}, nil
			}
			body, _ := json.Marshal(map[string]string{"provider": "google", "ip": "10.77.0.42", "mac": "aa:bb:cc:01:02:03",
				"redirect_uri": "http://192.0.2.1:8380/auth/social/callback"})
			w := httptest.NewRecorder()
			s.socialStart(w, httptest.NewRequest(http.MethodPost, "/v1/auth/social/start", bytes.NewReader(body)))
			var out map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if w.Code != http.StatusConflict || out["error"] != tc.want {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSocialSecretStoreWithoutTheKeyRefuses(t *testing.T) {
	s := &server{tenID: socTenant}
	body, _ := json.Marshal(map[string]string{"secret": "s3cr3t"})
	r := httptest.NewRequest(http.MethodPut, "/v1/social/providers/"+socProvider+"/secret", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", socProvider)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.socialSecretStore(w, r)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("social_key_missing")) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("s3cr3t")) {
		t.Fatal("the secret was echoed")
	}
}

func TestTheRegistryIsSwappedWhole(t *testing.T) {
	s := socialServer(t, noPortalHTTPS{}, "a.example")
	old := s.currentSocial()
	s.socialSet.Store(&socialSet{reg: social.NewRegistry(), hosts: map[string][]string{}, real: map[string]bool{}})
	if _, err := old.reg.Get("google"); err != nil {
		t.Fatal("the generation a reader already holds changed under it")
	}
	if _, err := s.currentSocial().reg.Get("google"); err == nil {
		t.Fatal("the new generation was not swapped in")
	}
	if (&server{}).currentSocial().reg == nil {
		t.Fatal("no registry")
	}
}

func TestSocialAdministrationIsAdminOnly(t *testing.T) {
	for _, p := range []string{"/v1/social/providers/" + socProvider + "/secret", "/v1/social/providers/x/secret-state",
		"/v1/social/reload", "/v1/social/status"} {
		if classifyRoute(p) != adminRoute {
			t.Errorf("%s must be edged-only", p)
		}
	}
	if classifyRoute("/v1/auth/social/start") != guestRoute {
		t.Error("portald's social start must stay a guest route")
	}
}
