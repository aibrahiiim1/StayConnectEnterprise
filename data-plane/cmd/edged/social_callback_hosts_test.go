package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

var twoHostnames = portalTLSState{
	OneGateHostname: "abcd1234.gw.onegate.work", CustomDomain: "wifi.nilehotel.com", CustomDomainStatus: "VERIFIED",
	HTTPSActive: true, CanonicalHostname: "wifi.nilehotel.com",
}

func TestValidateCallbackHosts(t *testing.T) {
	got, msg := validateCallbackHosts([]string{" WIFI.nilehotel.com. ", "abcd1234.gw.onegate.work", "wifi.nilehotel.com", ""}, twoHostnames)
	if msg != "" || !reflect.DeepEqual(got, []string{"abcd1234.gw.onegate.work", "wifi.nilehotel.com"}) {
		t.Fatalf("got %v %q", got, msg)
	}
	if got, msg := validateCallbackHosts(nil, twoHostnames); msg != "" || len(got) != 0 || got == nil {
		t.Fatalf("none ticked: %v %q", got, msg)
	}
	if _, msg := validateCallbackHosts([]string{"portal.example.com"}, twoHostnames); !strings.Contains(msg, "not a portal hostname") {
		t.Fatalf("an unknown host was accepted: %q", msg)
	}
	if _, msg := validateCallbackHosts([]string{"abcd1234.gw.onegate.work"}, portalTLSState{}); !strings.Contains(msg, "no portal hostname yet") {
		t.Fatalf("no hostnames yet: %q", msg)
	}
	removed := twoHostnames
	removed.CustomDomainStatus = "REMOVED"
	if _, msg := validateCallbackHosts([]string{"wifi.nilehotel.com"}, removed); msg == "" {
		t.Fatal("a removed hotel domain was accepted as a callback host")
	}
}

func TestSocialOffered(t *testing.T) {
	onegateCanonical := portalTLSState{OneGateHostname: "abcd1234.gw.onegate.work", HTTPSActive: true,
		CanonicalHostname: "abcd1234.gw.onegate.work"}
	fallback := portalTLSState{OneGateHostname: "abcd1234.gw.onegate.work", FallbackReason: "NO_CERTIFICATE"}
	both := []string{"abcd1234.gw.onegate.work", "wifi.nilehotel.com"}
	for _, tc := range []struct {
		name, provider string
		enabled        bool
		hosts          []string
		st             portalTLSState
		offered        bool
		reason         string
	}{
		{"facebook on the OneGate hostname", "facebook", true, []string{"abcd1234.gw.onegate.work"}, onegateCanonical, true, ""},
		{"apple on the hotel domain", "apple", true, both, twoHostnames, true, ""},
		{"google on the hotel domain", "google", true, []string{"wifi.nilehotel.com"}, twoHostnames, true, ""},
		{"google needs the hotel domain", "google", true, both, onegateCanonical, false, "needs_hotel_domain"},
		{"disabled", "microsoft", false, both, twoHostnames, false, "disabled"},
		{"http fallback hides every provider", "facebook", true, both, fallback, false, "http_fallback"},
		{"canonical not registered", "microsoft", true, []string{"abcd1234.gw.onegate.work"}, twoHostnames, false, "callback_not_registered"},
		{"nothing registered", "facebook", true, nil, onegateCanonical, false, "callback_not_registered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offered, reason := socialOffered(tc.provider, tc.enabled, tc.hosts, tc.st)
			if offered != tc.offered || reason != tc.reason {
				t.Fatalf("got (%v, %q), want (%v, %q)", offered, reason, tc.offered, tc.reason)
			}
		})
	}
}

// A legacy redirect_uri is shape-checked and only its host becomes the callback host; the URL is never stored.
func TestLegacyRedirectURIBecomesACallbackHost(t *testing.T) {
	in := socialWriteReq{RedirectURI: sp("https://wifi.nilehotel.com/auth/social/callback")}
	hosts, msg := resolveCallbackHosts(&in, twoHostnames)
	if msg != "" || !reflect.DeepEqual(hosts, []string{"wifi.nilehotel.com"}) {
		t.Fatalf("got %v %q", hosts, msg)
	}
	bad := socialWriteReq{RedirectURI: sp("http://wifi.nilehotel.com/auth/social/callback")}
	if _, msg := resolveCallbackHosts(&bad, twoHostnames); msg == "" {
		t.Fatal("a plain-http redirect URI was accepted")
	}
	// callback_hosts wins over a redirect_uri sent alongside it.
	both := socialWriteReq{RedirectURI: sp("https://wifi.nilehotel.com/auth/social/callback"),
		CallbackHosts: &[]string{"abcd1234.gw.onegate.work"}}
	if hosts, _ := resolveCallbackHosts(&both, twoHostnames); !reflect.DeepEqual(hosts, []string{"abcd1234.gw.onegate.work"}) {
		t.Fatalf("got %v", hosts)
	}
	// Neither field: unchanged (nil), which the UPDATE reads as "keep what is stored".
	if hosts, msg := resolveCallbackHosts(&socialWriteReq{}, twoHostnames); hosts != nil || msg != "" {
		t.Fatalf("got %v %q", hosts, msg)
	}
}

// The secret goes to scd once; on refusal edged reports scd's reason, never the secret.
func TestSocialSecretIsForwardedToSCD(t *testing.T) {
	rec := &callRecorder{}
	refuse := false
	sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if refuse {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"bad_key","message":"the social key is not installed"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s := &server{scd: newSCDClient(sock)}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if ok, msg := s.storeSocialSecret(req, "8f6c-id", "s3cr3t"); !ok {
		t.Fatalf("refused: %s", msg)
	}
	calls := rec.all()
	if len(calls) != 1 || calls[0].Method != http.MethodPut || calls[0].Path != "/v1/social/providers/8f6c-id/secret" {
		t.Fatalf("scd calls = %+v", calls)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil || body["secret"] != "s3cr3t" {
		t.Fatalf("body = %s", calls[0].Body)
	}
	refuse = true
	ok, msg := s.storeSocialSecret(req, "8f6c-id", "s3cr3t")
	if ok || msg != "the social key is not installed" || strings.Contains(msg, "s3cr3t") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
	if ok, _ := (&server{}).storeSocialSecret(req, "x", "y"); ok {
		t.Fatal("a missing scd client stored a secret")
	}
}

func TestSocialReloadCallsSCD(t *testing.T) {
	rec := &callRecorder{}
	sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	(&server{scd: newSCDClient(sock)}).reloadSocial()
	calls := rec.all()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/social/reload" {
		t.Fatalf("scd calls = %+v", calls)
	}
	(&server{}).reloadSocial()
}

func TestSocialProviderResponseShape(t *testing.T) {
	p := edgeSocialProvider{Provider: "facebook", Enabled: true, CallbackHosts: []string{"abcd1234.gw.onegate.work"}, SecretSet: true}
	decorateSocial(&p, twoHostnames)
	b, _ := json.Marshal(p)
	for _, want := range []string{`"callback_hosts"`, `"callback_urls"`, `"secret_set":true`, `"offered":false`,
		`"offered_reason":"callback_not_registered"`, `https://wifi.nilehotel.com/auth/social/callback`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("response lacks %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "redirect_uri") || strings.Contains(string(b), "client_secret") {
		t.Errorf("response carries a retired or secret field: %s", b)
	}
}
