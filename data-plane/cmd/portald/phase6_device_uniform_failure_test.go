package main

// A dark device capability must not report authentication failures: the answer is about devices, and it
// tells the guest nothing about why.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// darkScd models scd not mounting the endpoints.
type darkScd struct{}

func (darkScd) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("phase6 endpoints are not mounted on this appliance")
}

func darkFixture(t *testing.T) *handler {
	t.Helper()
	h := &handler{scd: &http.Client{Transport: darkScd{}}, clock: newRecordingClock()}
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:00:60:01")
		return mac, true
	}
	return h
}

// darkProbe returns what the guest is told and what the journal records.
func darkProbe(t *testing.T, route, body string) (int, deviceOut, string) {
	t.Helper()
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	req.RemoteAddr = "10.9.9.9:41000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	darkFixture(t).routes().ServeHTTP(rec, req)

	var out deviceOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, logged.String()
}

// An unserved device request must never claim the stay failed to verify, in the body or the log.
func TestDeviceFailure_DoesNotClaimTheStayCouldNotBeVerified(t *testing.T) {
	for _, c := range []struct{ route, body string }{
		{"/devices/list", "{}"},
		{"/devices/release", `{"device_id":"11111111-1111-1111-1111-111111111111"}`},
	} {
		code, out, logged := darkProbe(t, c.route, c.body)

		if code != http.StatusOK {
			t.Fatalf("%s: status %d — a distinct status is itself a signal; the uniform answer is 200",
				c.route, code)
		}
		if out.OK {
			t.Fatalf("%s: reported success with no capability behind it", c.route)
		}
		if out.Message == guestAuthMessage {
			t.Fatalf("%s: a device request answered with the AUTHENTICATION failure message (%q). The guest's "+
				"stay was never in question here.", c.route, out.Message)
		}
		if out.Message != deviceMessage {
			t.Fatalf("%s: message = %q, want the single device message %q", c.route, out.Message, deviceMessage)
		}
		if len(out.Devices) != 0 {
			t.Fatalf("%s: a refusal carried %d device(s)", c.route, len(out.Devices))
		}
		if strings.Contains(logged, "phase3 guest auth not verified") {
			t.Fatalf("%s: recorded a guest AUTHENTICATION failure for a device request:\n%s", c.route, logged)
		}
		if !strings.Contains(logged, "phase6 device request not served") {
			t.Fatalf("%s: nothing recorded what actually happened:\n%s", c.route, logged)
		}
	}
}

// One message for every cause keeps "no such feature here" indistinguishable from "request failed".
func TestDeviceFailure_TellsTheGuestNothingAboutTheCause(t *testing.T) {
	causes := []struct{ route, body string }{
		{"/devices/list", "{}"},
		{"/devices/list", `{"stay":"1104"}`}, // an identity-looking field
		{"/devices/list", `{`},               // malformed
		{"/devices/release", `{"device_id":""}`},
		{"/devices/release", `{"device_id":"22222222-2222-2222-2222-222222222222"}`},
		{"/devices/release", `{"device_id":"not-a-uuid"}`},
	}
	var first string
	for i, c := range causes {
		_, out, _ := darkProbe(t, c.route, c.body)
		if out.OK {
			t.Fatalf("case %d (%s %s) succeeded against a dark appliance", i, c.route, c.body)
		}
		if i == 0 {
			first = out.Message
			continue
		}
		if out.Message != first {
			t.Fatalf("case %d (%s %s) answered %q but case 0 answered %q — the differences between causes are "+
				"exactly what a probe wants to learn", i, c.route, c.body, out.Message, first)
		}
	}
	low := strings.ToLower(first)
	for _, term := range append([]string{"phase", "entitlement", "scd", "socket", "disabled", "dark", "quota"},
		forbiddenGuestTerms...) {
		if strings.Contains(low, term) {
			t.Fatalf("the device message discloses %q: %q", term, first)
		}
	}
}

// A real Phase-3 failure must still answer with the stay message.
func TestDeviceFailure_AuthenticationSurfaceStillSaysWhatItAlwaysSaid(t *testing.T) {
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "room_not_found", classCredential, "", "", 0)
	if body.Message != guestAuthMessage {
		t.Fatalf("the authentication failure message changed to %q", body.Message)
	}
	if leaksDetail(body) {
		t.Fatal("the authentication failure body leaks detail")
	}
}
