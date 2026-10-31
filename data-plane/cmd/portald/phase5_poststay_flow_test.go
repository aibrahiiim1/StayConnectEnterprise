package main

// These tests read the request bodies out of the real page script (landingHTML), send exactly those to the
// handler and assert what portald forwards to scd. The scd half is cmd/scd/phase5_poststay_integration_test.go.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

type pathStub struct {
	answers map[string]any
	calls   []string
	bodies  []map[string]any
}

func (s *pathStub) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	_ = json.NewDecoder(req.Body).Decode(&body)
	s.calls = append(s.calls, req.URL.Path)
	s.bodies = append(s.bodies, body)
	raw, _ := json.Marshal(s.answers[req.URL.Path])
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)),
		Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
}

func pathStubHandler(t *testing.T, s *pathStub) *handler {
	h := stubHandler(t, &scdStub{})
	h.scd = &http.Client{Transport: s}
	return h
}

func pageBodies(t *testing.T) [][]string {
	t.Helper()
	ms := regexp.MustCompile(`postStayCall\(\{([^}]*)\}\)`).FindAllStringSubmatch(landingHTML, -1)
	var out [][]string
	for _, m := range ms {
		var keys []string
		for _, part := range strings.Split(m[1], ",") {
			if k := strings.TrimSpace(strings.SplitN(part, ":", 2)[0]); k != "" {
				keys = append(keys, k)
			}
		}
		out = append(out, keys)
	}
	return out
}

// After Check-out Access (2026-10-30): the page sends the room and the reservation and nothing else. No surname, no code and no
// package choice exist any more, and the page never carries a context of its own (the server verifies and reconnects in one
// request), so a guest cannot skip the verification by posting a context.
func TestPostStayPageSendsOnlyTheRoomAndReservation(t *testing.T) {
	bodies := pageBodies(t)
	if len(bodies) != 1 {
		t.Fatalf("the page makes %d post-stay calls, want exactly 1 (the room and reservation): %v", len(bodies), bodies)
	}
	if strings.Join(bodies[0], ",") != "room,reservation" {
		t.Fatalf("the call sends %v, want exactly {room, reservation}", bodies[0])
	}
	for _, frag := range []string{"j.session_id"} {
		if !strings.Contains(landingHTML, frag) {
			t.Fatalf("the page script no longer reads %q", frag)
		}
	}
	for _, gone := range []string{"last_name: document.getElementById('ps-last')", `id="ps-last"`, `id="ps-choices"`,
		"renderPostStayChoices", "needs_choice", "package_revision_id"} {
		// package_revision_id and needs_choice still exist for the Room (PMS) flow elsewhere in the page; only the
		// post-stay panel and its script must be free of them.
		if gone == "package_revision_id" || gone == "needs_choice" {
			continue
		}
		if strings.Contains(landingHTML, gone) {
			t.Fatalf("the page still carries %q: after-check-out sign-in asks for the room and reservation only", gone)
		}
	}
}

func build(keys []string, vals map[string]string) string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = vals[k]
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func TestPostStayVerifiesThenReconnectsInOneRequest(t *testing.T) {
	bodies := pageBodies(t)
	s := &pathStub{answers: map[string]any{
		"/v1/phase5/auth/post-stay":   map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1"},
		"/v1/phase5/poststay/convert": map[string]any{"outcome": "GRANTED", "session_id": "sess-1", "entitlement_id": "ent-1"},
	}}
	h := pathStubHandler(t, s)
	_, out := postStayPost(t, h, "/auth/post-stay", build(bodies[0], map[string]string{"room": "101", "reservation": "R1"}))
	if !out.OK || out.SessionID != "sess-1" || out.RedirectTo != "/success" {
		t.Fatalf("a verified check did not connect the guest: %+v", out)
	}
	if len(s.calls) != 2 || s.calls[0] != "/v1/phase5/auth/post-stay" || s.calls[1] != "/v1/phase5/poststay/convert" {
		t.Fatalf("hops = %v, want verify then convert", s.calls)
	}
	if v := s.bodies[0]; v["room"] != "101" || v["reservation"] != "R1" || len(v) != 3 { // room, reservation, device
		t.Fatalf("verify hop carried %v; scd's decoder refuses anything but room, reservation and the derived device", v)
	}
	conv := s.bodies[1]
	if conv["auth_context_id"] != "ctx-1" {
		t.Fatalf("convert hop carried %v", conv)
	}
	if dev, _ := conv["device"].(map[string]any); dev["mac"] != "02:00:00:aa:00:01" {
		t.Fatalf("convert hop device = %v, want the server-derived one", conv["device"])
	}
	// scd's decoder refuses unknown fields: no package, no surname.
	if len(conv) != 2 {
		t.Fatalf("convert hop carried extra keys: %v", conv)
	}
}

// GRANTED without a real session is not access (the entitlement id is not a session id).
func TestPostStayGrantWithoutASessionIsRefused(t *testing.T) {
	for _, answer := range []map[string]any{
		{"outcome": "GRANTED", "session_id": "", "entitlement_id": "ent-1"},
		{"outcome": "GRANTED", "session_id": "ent-1", "entitlement_id": "ent-1"},
	} {
		s := &pathStub{answers: map[string]any{"/v1/phase5/poststay/convert": answer}}
		h := pathStubHandler(t, s)
		_, out := postStayPost(t, h, "/auth/post-stay", `{"auth_context_id":"ctx-1","package_revision_id":"pkg-a"}`)
		if out.OK || out.SessionID != "" {
			t.Fatalf("scd answer %v reached the guest as success: %+v", answer, out)
		}
	}
}

// scd that says VERIFIED but issues no context cannot be converted: the guest gets the uniform answer.
func TestPostStayVerifiedWithoutAContextIsTheUniformAnswer(t *testing.T) {
	s := &pathStub{answers: map[string]any{
		"/v1/phase5/auth/post-stay": map[string]any{"outcome": "VERIFIED"}}}
	h := pathStubHandler(t, s)
	_, out := postStayPost(t, h, "/auth/post-stay", `{"room":"101","reservation":"R1"}`)
	if out.OK || out.SessionID != "" {
		t.Fatalf("a verification with no context reached the guest: %+v", out)
	}
	if len(s.calls) != 1 {
		t.Fatalf("hops = %v, want only the verify hop", s.calls)
	}
}
