package main

// Driven with a stand-in scd: nothing a guest can observe may distinguish one failure from another,
// however varied the reasons scd returns.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scdStub struct {
	resolve  any
	grant    any
	status   int
	failWith error
	calls    []string
	bodies   []map[string]any
}

func (s *scdStub) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.failWith != nil {
		return nil, s.failWith
	}
	var body map[string]any
	_ = json.NewDecoder(req.Body).Decode(&body)
	s.calls = append(s.calls, req.URL.Path)
	s.bodies = append(s.bodies, body)

	var payload any
	if strings.HasSuffix(req.URL.Path, "/resolve") {
		payload = s.resolve
	} else {
		payload = s.grant
	}
	raw, _ := json.Marshal(payload)
	code := s.status
	if code == 0 {
		code = http.StatusOK
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func stubHandler(t *testing.T, stub *scdStub) *handler {
	t.Helper()
	h, _ := stubHandlerWithClock(t, stub)
	return h
}

// stubHandlerWithClock installs the recording clock so failure cases do not spend the real budget, and so the
// budget is assertable (see pms_phase3_budget_test.go).
func stubHandlerWithClock(t *testing.T, stub *scdStub) (*handler, *recordingClock) {
	t.Helper()
	clk := newRecordingClock()
	h := &handler{scd: &http.Client{Transport: stub}, clock: clk}
	// Stands in for the ARP table: identity must be server-derived here as in production.
	h.arpCache = func(ip net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:aa:00:01")
		return mac, true
	}
	return h, clk
}

func phase3Post(t *testing.T, h *handler, body map[string]any) (*httptest.ResponseRecorder, phase3Out) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := httptest.NewRecorder()
	h.authPMSPhase3(rec, req)
	var out phase3Out
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable body %q: %v", rec.Body.String(), err)
	}
	return rec, out
}

func TestPhase3SingleOfferGrantsImmediately(t *testing.T) {
	stub := &scdStub{
		resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1",
			"offers": []map[string]any{{"package_revision_id": "pkg-1", "code": "STAY", "down_kbps": 9000}}},
		grant: map[string]any{"outcome": "VERIFIED", "session_id": "sess-1", "entitlement_id": "ent-1"},
	}
	h := stubHandler(t, stub)
	_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r1"})
	if !out.OK || out.SessionID != "sess-1" {
		t.Fatalf("a single-offer verification did not connect the guest: %+v", out)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("hops = %v, want resolve then grant", stub.calls)
	}
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev["ip"] != "10.77.0.25" || dev["mac"] != "02:00:00:aa:00:01" {
		t.Fatalf("the forwarded identity was not server-derived: %v", dev)
	}
}

func TestPhase3MultipleOffersAskTheGuest(t *testing.T) {
	stub := &scdStub{
		resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-2",
			"offers": []map[string]any{
				{"package_revision_id": "pkg-1", "code": "STANDARD", "down_kbps": 9000},
				{"package_revision_id": "pkg-2", "code": "PREMIUM", "down_kbps": 25000}}},
	}
	h := stubHandler(t, stub)
	_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r2"})
	if !out.OK || !out.NeedsChoice || len(out.Choices) != 2 {
		t.Fatalf("two offers were not presented as a choice: %+v", out)
	}
	if out.SessionID != "" {
		t.Fatal("a choice step handed out a session")
	}
	if len(stub.calls) != 1 {
		t.Fatalf("hops = %v, want resolve only", stub.calls)
	}
}

// Everything that could describe one room is byte-identical; only site-wide conditions get their own
// sentence. scd decides the class (internal/signinattempt.GuestClass) and portald forwards it.
func TestPhase3EveryPerRoomFailureIsIndistinguishable(t *testing.T) {
	cred := map[string]any{"outcome": "NOT_VERIFIED", "failure_class": "CREDENTIAL"}
	cases := []struct {
		name string
		stub *scdStub
		body map[string]any
	}{
		{"no match", &scdStub{resolve: cred},
			map[string]any{"room": "999", "last_name": "Nobody", "request_id": "r1"}},
		{"ambiguous", &scdStub{resolve: cred},
			map[string]any{"room": "412", "last_name": "Shared", "request_id": "r2"}},
		{"room absent from the mirror", &scdStub{resolve: cred},
			map[string]any{"room": "905", "last_name": "Okonkwo", "request_id": "r3"}},
		{"stay not eligible", &scdStub{resolve: cred},
			map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r4"}},
		{"grant refused on a credential ground", &scdStub{
			resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "c",
				"offers": []map[string]any{{"package_revision_id": "p", "code": "STAY"}}},
			grant: map[string]any{"outcome": "NOT_VERIFIED", "failure_class": "CREDENTIAL"}},
			map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r5"}},
	}

	var wantStatus int
	var wantBody string
	for i, c := range cases {
		h := stubHandler(t, c.stub)
		raw, _ := json.Marshal(c.body)
		req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
		req.RemoteAddr = "10.77.0.25:51000"
		rec := httptest.NewRecorder()
		h.authPMSPhase3(rec, req)

		if i == 0 {
			wantStatus, wantBody = rec.Code, rec.Body.String()
			var out guestPMSResponse
			if json.Unmarshal([]byte(wantBody), &out) != nil {
				t.Fatalf("undecodable canonical body %q", wantBody)
			}
			if out.OK || out.Message != guestAuthMessage {
				t.Fatalf("the canonical per-room failure is not the incorrect-details message: %q", wantBody)
			}
			if leaksDetail(out) {
				t.Fatalf("the per-room failure leaks detail: %q", wantBody)
			}
			if term := bodyMentionsForbiddenTerm(out); term != "" {
				t.Fatalf("the per-room message mentions %q", term)
			}
			continue
		}
		if rec.Code != wantStatus || rec.Body.String() != wantBody {
			t.Fatalf("%s is distinguishable from another per-room failure — submitting room numbers would "+
				"now reveal which rooms are occupied:\n  got  %d %s\n  want %d %s",
				c.name, rec.Code, rec.Body.String(), wantStatus, wantBody)
		}
	}
}

// portald never judges a room, stay or name, so every failure it decides on its own is technical.
func TestPhase3PortaldsOwnFailuresAreTechnicalAndDiscloseNothing(t *testing.T) {
	cases := []struct {
		name string
		stub *scdStub
	}{
		{"scd refused the hop", &scdStub{status: http.StatusForbidden, resolve: map[string]any{}}},
		{"scd is dark (route absent)", &scdStub{status: http.StatusNotFound, resolve: map[string]any{}}},
		{"scd socket unavailable", &scdStub{failWith: errors.New("scd socket unavailable")}},
		{"verified but no offers", &scdStub{resolve: map[string]any{
			"outcome": "VERIFIED", "auth_context_id": "c", "offers": []map[string]any{}}}},
	}
	for _, c := range cases {
		h := stubHandler(t, c.stub)
		_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r"})
		if out.OK {
			t.Fatalf("%s: reported success", c.name)
		}
		if out.Message != guestAuthTechnicalMessage {
			t.Fatalf("%s: message = %q, want the technical one — telling this guest to re-check details "+
				"they may have typed perfectly is advice that cannot help them", c.name, out.Message)
		}
		body := guestPMSResponse{OK: out.OK, Message: out.Message}
		if term := bodyMentionsForbiddenTerm(body); term != "" {
			t.Fatalf("%s: the technical message mentions %q", c.name, term)
		}
	}
}

// scd's class is forwarded, never re-derived (two mappings would drift).
func TestPhase3ForwardsScdsClassVerbatim(t *testing.T) {
	for class, want := range map[string]string{
		"CREDENTIAL": guestAuthMessage,
		"TECHNICAL":  guestAuthTechnicalMessage,
		// The sentence must be built from scd's remaining seconds.
		"RATE_LIMITED": guestAuthRateLimitedMessage(37),
	} {
		h := stubHandler(t, &scdStub{resolve: map[string]any{
			"outcome": "NOT_VERIFIED", "failure_class": class, "retry_after_seconds": 37}})
		_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "X", "request_id": "r"})
		if out.Message != want {
			t.Errorf("class %s produced %q, want %q", class, out.Message, want)
		}
	}
}

// An unplaceable device is forwarded so scd records the refusal in the sign-in attempt log; the address is
// the TCP peer's (verifiable) and scd re-derives the network. The guest still gets the TECHNICAL answer.
func TestPhase3UnplaceableDeviceIsForwardedSoItIsRecorded(t *testing.T) {
	stub := &scdStub{resolve: map[string]any{
		"outcome": "NOT_VERIFIED", "failure_class": "TECHNICAL", "reason_code": "ROUTING_OR_INTERFACE_FAILURE"}}
	h := stubHandler(t, stub)
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	h.arpNudge = func(net.IP) {}        // no network in a test
	h.arpSleep = func(time.Duration) {} // and no real waiting
	raw, _ := json.Marshal(map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r"})
	req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := httptest.NewRecorder()
	h.authPMSPhase3(rec, req)

	if len(stub.calls) == 0 {
		t.Fatal("an unplaceable device was refused without being recorded anywhere")
	}
	// A MAC is never taken from the client, so an empty one is sent.
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev == nil || dev["ip"] != "10.77.0.25" {
		t.Fatalf("the source address was not forwarded for the record: %v", stub.bodies[0]["device"])
	}
	if mac, _ := dev["mac"].(string); mac != "" {
		t.Fatalf("a hardware address was invented for a device the kernel could not place: %q", mac)
	}
	var out phase3Out
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.OK || out.Message != guestAuthTechnicalMessage {
		t.Fatalf("an unplaceable device was told to re-check what it typed: %s", rec.Body.String())
	}
}

// A cold neighbour cache (fresh DHCP lease) is not an unplaceable device: the kernel is asked to resolve.
func TestPhase3AColdNeighbourCacheIsResolvedNotRefused(t *testing.T) {
	stub := &scdStub{resolve: map[string]any{"outcome": "VERIFIED"}}
	h := stubHandler(t, stub)
	appeared := false
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		if !appeared {
			return nil, false
		}
		mac, _ := net.ParseMAC("02:00:00:ab:cd:01")
		return mac, true
	}
	h.arpNudge = func(net.IP) { appeared = true }
	h.arpSleep = func(time.Duration) {}
	raw, _ := json.Marshal(map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r"})
	req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := httptest.NewRecorder()
	h.authPMSPhase3(rec, req)

	if len(stub.calls) == 0 {
		t.Fatal("a resolvable device never reached scd")
	}
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev == nil || dev["mac"] != "02:00:00:ab:cd:01" {
		t.Fatalf("the kernel-resolved hardware address was not forwarded: %v", stub.bodies[0]["device"])
	}
}

func TestPhase3TransportFailureIsTechnical(t *testing.T) {
	h := stubHandler(t, &scdStub{failWith: errors.New("scd socket unavailable")})
	_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r"})
	if out.OK || out.Message != guestAuthTechnicalMessage {
		t.Fatalf("a transport failure produced %+v, want the technical message", out)
	}
	if bodyMentionsForbiddenTerm(guestPMSResponse{Message: out.Message}) != "" {
		t.Fatal("the technical message discloses what failed")
	}
}
