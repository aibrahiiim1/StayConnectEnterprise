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
	h := &handler{scd: &http.Client{Transport: stub}}
	// A fixed neighbour lookup stands in for the appliance's ARP table: the identity is server-derived in
	// production and must be server-derived here too, or the test would prove the wrong thing.
	h.arpCache = func(ip net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:aa:00:01")
		return mac, true
	}
	return h
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

// THE CONTRACT. Every one of these is a different internal reality; the guest must not be able to tell them
// apart from the response — same status, same bytes.
func TestPhase3EveryFailureIsIndistinguishable(t *testing.T) {
	cases := []struct {
		name string
		stub *scdStub
		body map[string]any
	}{
		{"no match", &scdStub{resolve: map[string]any{"outcome": "NOT_VERIFIED"}},
			map[string]any{"room": "999", "last_name": "Nobody", "request_id": "r1"}},
		{"ambiguous", &scdStub{resolve: map[string]any{"outcome": "NOT_VERIFIED"}},
			map[string]any{"room": "412", "last_name": "Shared", "request_id": "r2"}},
		{"verified but no offers", &scdStub{resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "c", "offers": []map[string]any{}}},
			map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r3"}},
		{"grant refused", &scdStub{
			resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "c",
				"offers": []map[string]any{{"package_revision_id": "p", "code": "STAY"}}},
			grant: map[string]any{"outcome": "NOT_VERIFIED"}},
			map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r4"}},
		{"scd refused the hop", &scdStub{status: http.StatusForbidden, resolve: map[string]any{}},
			map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r5"}},
		{"scd is dark (route absent)", &scdStub{status: http.StatusNotFound, resolve: map[string]any{}},
			map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r6"}},
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
			// decoded into the LEGACY uniform type on purpose: the Phase-3 failure body must be exactly
			// what the existing, already-tested contract produces, not merely something that looks like it
			var out guestPMSResponse
			if json.Unmarshal([]byte(wantBody), &out) != nil {
				t.Fatalf("undecodable canonical body %q", wantBody)
			}
			if out.OK || out.Message != guestAuthMessage {
				t.Fatalf("the canonical failure is not the uniform message: %q", wantBody)
			}
			if leaksDetail(out) {
				t.Fatalf("the uniform failure leaks detail: %q", wantBody)
			}
			if term := bodyMentionsForbiddenTerm(out); term != "" {
				t.Fatalf("the uniform message mentions %q", term)
			}
			continue
		}
		if rec.Code != wantStatus || rec.Body.String() != wantBody {
			t.Fatalf("%s is distinguishable:\n  got  %d %s\n  want %d %s",
				c.name, rec.Code, rec.Body.String(), wantStatus, wantBody)
		}
	}
}

// A device the appliance cannot place on a guest network never reaches scd at all: there is no scope to
// resolve in, and forwarding it would ask scd to trust an address portald could not verify.
func TestPhase3UnknownDeviceNeverReachesScd(t *testing.T) {
	stub := &scdStub{resolve: map[string]any{"outcome": "VERIFIED"}}
	h := stubHandler(t, stub)
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }

	raw, _ := json.Marshal(map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r"})
	req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := httptest.NewRecorder()
	h.authPMSPhase3(rec, req)

	if len(stub.calls) != 0 {
		t.Fatalf("an unplaceable device was forwarded to scd: %v", stub.calls)
	}
	var out phase3Out
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.OK || out.Message != guestAuthMessage {
		t.Fatalf("an unplaceable device got a distinguishable answer: %s", rec.Body.String())
	}
}

// A transport failure is not an error page. The guest gets the same message as a wrong room, because the
// difference is not theirs to act on — and because "the hotel's system is down" is itself information.
func TestPhase3TransportFailureIsTheSameMessage(t *testing.T) {
	h := stubHandler(t, &scdStub{failWith: errors.New("scd socket unavailable")})
	_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": "r"})
	if out.OK || out.Message != guestAuthMessage {
		t.Fatalf("a transport failure produced a distinguishable answer: %+v", out)
	}
}
