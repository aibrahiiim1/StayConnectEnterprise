package main

// The guest-facing Phase-3 handler, driven with a stand-in scd. What is being proven is not that the proxy
// works — it is that NOTHING a guest can observe distinguishes one failure from another. The reasons scd
// returns are deliberately varied and detailed; every one of them must come out the other side identical.

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

// scdStub answers the two Phase-3 hops with whatever the test wants.
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

// stubHandlerWithClock installs the recording clock so a failing case does not spend the real response-time
// budget. Every failure test in this file goes through the budget, and six of them at 1.2 real seconds each
// would be a suite nobody runs. The clock is also what makes the budget ASSERTABLE — see phase3_budget_test.go.
func stubHandlerWithClock(t *testing.T, stub *scdStub) (*handler, *recordingClock) {
	t.Helper()
	clk := newRecordingClock()
	h := &handler{scd: &http.Client{Transport: stub}, clock: clk}
	// A fixed neighbour lookup stands in for the appliance's ARP table: the identity is server-derived in
	// production and must be server-derived here too, or the test would prove the wrong thing.
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

// A single offer is granted without a second round trip: presenting one option is not a choice, it is a
// second request on the guest's worst network.
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
	// the identity scd received must be the SERVER's view, not anything the guest sent
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev["ip"] != "10.77.0.25" || dev["mac"] != "02:00:00:aa:00:01" {
		t.Fatalf("the forwarded identity was not server-derived: %v", dev)
	}
}

// More than one offer is a real choice, and it is presented WITHOUT disclosing anything about the stay.
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

// THE CONTRACT, as it now stands. Everything that could describe ONE ROOM is byte-identical; only the
// site-wide conditions are allowed their own sentence.
//
// It used to be every failure without exception, and that made the portal useless to a guest whose details
// were correct on a property whose mirror could not answer. The line moved, and this is where it moved TO:
// scd decides the class (internal/signinattempt.GuestClass, which is where the reasoning lives), portald
// forwards it, and every case below that names a room shares one class and therefore one body.
func TestPhase3EveryPerRoomFailureIsIndistinguishable(t *testing.T) {
	// scd answers CREDENTIAL for all of these: no such room, wrong name, two candidates, an ineligible stay.
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

// THE SITE-WIDE FAILURES may say so, and this pins WHICH ones portald decides for itself.
//
// portald only ever knows what IT could not do. It never sees a room, a stay or a name, so it can never
// legitimately answer "your details are wrong" on its own — every one of these is technical, and a
// credential answer here would be a guess about a guest it knows nothing about.
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

// scd's class is FORWARDED, never re-derived. Two mappings from a reason to a guest class would drift, and
// the one that lives in internal/signinattempt is the one that carries the reasoning.
func TestPhase3ForwardsScdsClassVerbatim(t *testing.T) {
	for class, want := range map[string]string{
		"CREDENTIAL": guestAuthMessage,
		"TECHNICAL":  guestAuthTechnicalMessage,
		// RATE_LIMITED carries the SERVER's remaining seconds. The number below is scd's, and the sentence
		// the guest reads has to be built from that same number rather than from anything portald decided.
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

// A DEVICE THE APPLIANCE CANNOT PLACE IS STILL RECORDED, AND STILL TOLD THE TECHNICAL ANSWER.
//
// This case used to assert that such a request never reached scd, on the reasoning that forwarding it "would
// ask scd to trust an address portald could not verify". That reasoning does not hold: the address is the TCP
// peer's, which portald can verify, and scd re-derives the guest network from it and refuses an address on no
// enabled network regardless. What portald could not establish was the HARDWARE address, not the address.
//
// Meanwhile the cost was real and was found on PRE-LIVE: because portald answered alone, the refusal was written
// NOWHERE. The guest was told to contact the front desk, and the front desk opened the sign-in attempt log --
// the screen that exists for this exact question -- and saw nothing at all. Two such requests were measured
// producing the technical sentence and zero rows.
//
// So it is forwarded, and scd refuses and records through the same classifier as every other refusal. The
// property that mattered is kept and is asserted below: the guest is told the TECHNICAL answer, never to
// re-check typing that was never the problem.
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

	// IT REACHES scd, which is the only component that records an attempt.
	if len(stub.calls) == 0 {
		t.Fatal("an unplaceable device was refused without being recorded anywhere")
	}
	// ...carrying the source address it could establish, and NO hardware address it could not. A MAC is never
	// taken from the client, so an empty one is the honest thing to send.
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev == nil || dev["ip"] != "10.77.0.25" {
		t.Fatalf("the source address was not forwarded for the record: %v", stub.bodies[0]["device"])
	}
	if mac, _ := dev["mac"].(string); mac != "" {
		t.Fatalf("a hardware address was invented for a device the kernel could not place: %q", mac)
	}
	var out phase3Out
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// TECHNICAL, not credential: a device the appliance cannot place on a guest network has a networking
	// problem, and nothing the guest types will ever fix it.
	if out.OK || out.Message != guestAuthTechnicalMessage {
		t.Fatalf("an unplaceable device was told to re-check what it typed: %s", rec.Body.String())
	}
}

// ...AND A COLD NEIGHBOUR CACHE IS NOT AN UNPLACEABLE DEVICE. A client that has just taken a DHCP lease and
// submitted immediately has no cache entry yet; the kernel is asked, the entry appears, and the sign-in goes
// through. Before this, that guest was refused and sent to Reception for a reason that was not about them.
func TestPhase3AColdNeighbourCacheIsResolvedNotRefused(t *testing.T) {
	stub := &scdStub{resolve: map[string]any{"outcome": "VERIFIED"}}
	h := stubHandler(t, stub)
	appeared := false
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		if !appeared {
			return nil, false // cold: nothing cached yet
		}
		mac, _ := net.ParseMAC("02:00:00:ab:cd:01")
		return mac, true
	}
	h.arpNudge = func(net.IP) { appeared = true } // the kernel resolves it once asked
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

// A transport failure is not an error page, and it is not the guest's fault either. They are told the
// property cannot check right now — which is true, is the same for every guest in the building at that
// moment, and identifies nobody.
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
