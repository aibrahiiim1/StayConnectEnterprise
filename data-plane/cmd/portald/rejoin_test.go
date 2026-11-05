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
