package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

// P4 L6 (social/start): scd itself refuses a start without the device's IP and MAC, rather than storing an unbound state.
func TestSocialStartRequiresTheDevice(t *testing.T) {
	for name, body := range map[string]map[string]string{
		"no ip":  {"provider": "google", "mac": "aa:bb:cc:01:02:03"},
		"no mac": {"provider": "google", "ip": "10.77.0.42"},
		"ipv6":   {"provider": "google", "ip": "fe80::1", "mac": "aa:bb:cc:01:02:03"},
	} {
		t.Run(name, func(t *testing.T) {
			s := socialServer(t, fakePortal{true, "wifi.hotel.example"}, "wifi.hotel.example")
			s.lic = licenceWithModules(t, lic.ModuleSocialLogin)
			s.met = metrics.New("test", prometheus.Labels{})
			s.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
				return &tenantcfg.AuthMethods{Social: map[string]*tenantcfg.AuthMethod{"google": {Enabled: true}}}, nil
			}
			b, _ := json.Marshal(body)
			w := httptest.NewRecorder()
			s.socialStart(w, httptest.NewRequest(http.MethodPost, "/v1/auth/social/start", bytes.NewReader(b)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", w.Code, w.Body.String())
			}
		})
	}
}
