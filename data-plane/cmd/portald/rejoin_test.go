package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type rejoinScd struct {
	outcome string
	calls   int
	bodies  []string
}

func (f *rejoinScd) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	b, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, r.URL.Path+" "+string(b))
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"outcome":"` + f.outcome + `"}`)), Request: r}, nil
}

func rejoinHandler(outcome string) (*handler, *rejoinScd) {
	f := &rejoinScd{outcome: outcome}
	mac, _ := net.ParseMAC("02:00:00:00:77:01")
	return &handler{
		scd:         &http.Client{Transport: f},
		rejoinQuiet: newQuietCache(),
		arpCache:    func(net.IP) (net.HardwareAddr, bool) { return mac, true },
		cfg:         cfg{PortalFQDN: "portal.example"},
	}, f
}

func probe(h *handler, fn func(http.ResponseWriter, *http.Request), path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "10.3.0.150:51000"
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

// A reconnected device's own probes get each OS's "online" answer, so it leaves the captive state without the guest doing anything.
func TestAReconnectedDeviceGetsTheOnlineProbeAnswers(t *testing.T) {
	h, f := rejoinHandler("REJOINED")
	if w := probe(h, h.googleProbe, "/generate_204"); w.Code != http.StatusNoContent {
		t.Errorf("Android probe answered %d, want 204", w.Code)
	}
	if w := probe(h, h.appleProbe, "/hotspot-detect.html"); w.Code != 200 || !strings.Contains(w.Body.String(), "<TITLE>Success</TITLE>") {
		t.Errorf("Apple probe answered %d %q", w.Code, w.Body.String())
	}
	if w := probe(h, h.windowsProbe, "/connecttest.txt"); w.Body.String() != "Microsoft Connect Test" {
		t.Errorf("Windows probe answered %q", w.Body.String())
	}
	if w := probe(h, h.windowsProbe, "/ncsi.txt"); w.Body.String() != "Microsoft NCSI" {
		t.Errorf("Windows NCSI probe answered %q", w.Body.String())
	}
	// The device is the connection's, never anything the client sent.
	if !strings.Contains(f.bodies[0], "/v1/phase3/auth/pms/rejoin") || !strings.Contains(f.bodies[0], `"ip":"10.3.0.150"`) ||
		!strings.Contains(f.bodies[0], `"mac":"02:00:00:00:77:01"`) {
		t.Errorf("scd was asked %q", f.bodies[0])
	}
}

// A device with nothing to reconnect gets the captive answer as before, and its retried probes do not each reach scd.
func TestADeviceWithNothingToReconnectIsSentToSignInAndNotAskedAgainAtOnce(t *testing.T) {
	h, f := rejoinHandler("NOT_ELIGIBLE")
	for i := 0; i < 3; i++ {
		if w := probe(h, h.googleProbe, "/generate_204"); w.Code != http.StatusFound {
			t.Fatalf("probe %d answered %d, want the redirect to sign-in", i, w.Code)
		}
	}
	if f.calls != 1 {
		t.Errorf("scd was asked %d times within the quiet window, want 1", f.calls)
	}
}

// Without a resolvable device nothing is asked: a reconnection is never attempted on a guessed identity.
func TestNoReconnectionWithoutTheConnectionsDevice(t *testing.T) {
	h, f := rejoinHandler("REJOINED")
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	if w := probe(h, h.googleProbe, "/generate_204"); w.Code != http.StatusFound || f.calls != 0 {
		t.Errorf("answered %d after %d scd calls, want the sign-in redirect and none", w.Code, f.calls)
	}
}

// While the HTTPS portal is active, a reconnected device's probe is answered "online" before the redirect to the HTTPS page, so a
// device that only notifies (Android) is back online without the guest opening anything; an ineligible one is redirected as before.
func TestBehindTheHTTPSPortalAReconnectedDeviceGetsTheOnlineAnswerBeforeAnyRedirect(t *testing.T) {
	front := func(outcome string, st portalTLSState) (http.Handler, *rejoinScd) {
		h, f := rejoinHandler(outcome)
		h.portalTLS = &tlsStateHolder{st: st}
		return h.httpFront(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })), f
	}
	https := portalTLSState{HTTPSActive: true, Canonical: "wifi.example.com"}
	portalIP := portalTLSState{PortalIPActive: true, PortalIP: "10.255.255.1"}
	want := map[string]int{"/generate_204": 204, "/gen_204": 204, "/hotspot-detect.html": 200, "/library/test/success.html": 200,
		"/connecttest.txt": 200, "/ncsi.txt": 200}
	for name, st := range map[string]portalTLSState{"https": https, "portal address": portalIP} {
		for path, code := range want {
			srv, f := front("REJOINED", st)
			r := httptest.NewRequest(http.MethodGet, "http://connectivitycheck.example"+path, nil)
			r.RemoteAddr = "10.3.0.150:51000"
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != code || w.Header().Get("Location") != "" || f.calls != 1 {
				t.Errorf("%s %s: %d location=%q scd calls=%d, want %d with no redirect", name, path, w.Code, w.Header().Get("Location"), f.calls, code)
			}
		}
		srv, f := front("NOT_ELIGIBLE", st)
		for i := 0; i < 3; i++ {
			r := httptest.NewRequest(http.MethodGet, "http://connectivitycheck.example/generate_204", nil)
			r.RemoteAddr = "10.3.0.150:51000"
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != http.StatusFound {
				t.Fatalf("%s: an ineligible device's probe answered %d, want the redirect", name, w.Code)
			}
		}
		if f.calls != 1 {
			t.Errorf("%s: scd asked %d times for retried probes within the quiet window, want 1", name, f.calls)
		}
	}
	srv, f := front("REJOINED", https)
	r := httptest.NewRequest(http.MethodGet, "http://captive.example/some/page", nil)
	r.RemoteAddr = "10.3.0.150:51000"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusFound || f.calls != 0 {
		t.Errorf("a page that is not a probe: %d, scd calls %d; want the HTTPS redirect without a reconnection attempt", w.Code, f.calls)
	}
}
