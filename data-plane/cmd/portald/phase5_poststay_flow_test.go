package main

// THE POST-STAY PAGE'S OWN REQUESTS, THROUGH portald, TO THE BODIES scd DECODES.
//
// The post-stay PIN could never succeed because the page's second call carried no package_revision_id and
// portald took scd's answer at its word. These tests read the request bodies OUT OF THE REAL PAGE SCRIPT
// (landingHTML) -- the keys the script puts in each postStayCall({...}) -- send exactly those to the handler,
// and assert what portald forwards to scd. The scd half (that those forwarded bodies produce an enforced
// session against a real database) is cmd/scd/phase5_poststay_integration_test.go.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// pathStub answers by scd path, and records every hop.
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

// pageBodies returns the key lists of every postStayCall({...}) in the real page script, in order.
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

func TestPostStayPageSendsThePINThenTheContextAndTheChosenPackage(t *testing.T) {
	bodies := pageBodies(t)
	if len(bodies) != 2 {
		t.Fatalf("the page makes %d post-stay calls, want 2 (the PIN, then the choice): %v", len(bodies), bodies)
	}
	if strings.Join(bodies[0], ",") != "pin" {
		t.Fatalf("the PIN call sends %v, want exactly {pin}", bodies[0])
	}
	if strings.Join(bodies[1], ",") != "auth_context_id,package_revision_id" {
		t.Fatalf("the choice call sends %v, want {auth_context_id, package_revision_id}: without the package "+
			"the conversion can never succeed", bodies[1])
	}
	// The page acts on the fields portald returns.
	for _, frag := range []string{"j.needs_choice", "j.choices", "j.session_id", "res.j.session_id",
		"c.package_revision_id", `id="ps-choices"`} {
		if !strings.Contains(landingHTML, frag) {
			t.Fatalf("the page script no longer reads %q", frag)
		}
	}
}

// build turns a key list from the page into the JSON the page sends, with the values the guest would have.
func build(keys []string, vals map[string]string) string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = vals[k]
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func TestPostStayChoiceFlowThroughPortald(t *testing.T) {
	bodies := pageBodies(t)
	s := &pathStub{answers: map[string]any{
		"/v1/phase5/auth/post-stay-pin": map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1",
			"offers": []map[string]any{
				{"package_revision_id": "pkg-a", "code": "PS-A", "down_kbps": 5000},
				{"package_revision_id": "pkg-b", "code": "PS-B", "down_kbps": 10000}}},
		"/v1/phase5/poststay/convert": map[string]any{"outcome": "GRANTED", "session_id": "sess-1", "entitlement_id": "ent-1"},
	}}
	h := pathStubHandler(t, s)

	// FIRST CALL, the page's PIN body.
	_, out := postStayPost(t, h, "/auth/post-stay-pin", build(bodies[0], map[string]string{"pin": "K7M4RTQX"}))
	if !out.OK || !out.NeedsChoice || out.AuthContextID != "ctx-1" || len(out.Choices) != 2 {
		t.Fatalf("a verified PIN with two packages did not ask the guest to choose: %+v", out)
	}
	if s.calls[0] != "/v1/phase5/auth/post-stay-pin" || s.bodies[0]["pin"] != "K7M4RTQX" {
		t.Fatalf("verify hop = %v %v", s.calls, s.bodies)
	}

	// SECOND CALL, the page's choice body, with the context and the package the guest picked.
	_, out2 := postStayPost(t, h, "/auth/post-stay-pin", build(bodies[1], map[string]string{
		"auth_context_id": out.AuthContextID, "package_revision_id": out.Choices[1].PackageRevisionID}))
	if !out2.OK || out2.SessionID != "sess-1" || out2.RedirectTo != "/success" {
		t.Fatalf("the choice did not connect the guest: %+v", out2)
	}
	if len(s.calls) != 2 || s.calls[1] != "/v1/phase5/poststay/convert" {
		t.Fatalf("hops = %v, want verify then convert", s.calls)
	}
	conv := s.bodies[1]
	if conv["auth_context_id"] != "ctx-1" || conv["package_revision_id"] != "pkg-b" {
		t.Fatalf("convert hop carried %v; scd refuses a conversion without the chosen package", conv)
	}
	if dev, _ := conv["device"].(map[string]any); dev["mac"] != "02:00:00:aa:00:01" {
		t.Fatalf("convert hop device = %v, want the server-derived one", conv["device"])
	}
	// Exactly the keys scd's decoder accepts (it refuses unknown fields).
	if len(conv) != 3 {
		t.Fatalf("convert hop carried extra keys: %v", conv)
	}
}

// ONE OFFER IS NOT A CHOICE: portald converts it in the same request, and the page goes straight to success.
func TestPostStaySingleOfferConvertsAtOnce(t *testing.T) {
	s := &pathStub{answers: map[string]any{
		"/v1/phase5/auth/post-stay-pin": map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1",
			"offers": []map[string]any{{"package_revision_id": "pkg-a", "code": "PS-A"}}},
		"/v1/phase5/poststay/convert": map[string]any{"outcome": "GRANTED", "session_id": "sess-1", "entitlement_id": "ent-1"},
	}}
	h := pathStubHandler(t, s)
	_, out := postStayPost(t, h, "/auth/post-stay-pin", `{"pin":"K7M4RTQX"}`)
	if !out.OK || out.SessionID != "sess-1" {
		t.Fatalf("a single post-stay package was not converted at once: %+v", out)
	}
	if len(s.calls) != 2 || s.bodies[1]["package_revision_id"] != "pkg-a" {
		t.Fatalf("hops = %v bodies = %v", s.calls, s.bodies)
	}
}

// A "GRANTED" WITHOUT A REAL SESSION IS NOT ACCESS. The old scd returned the entitlement id as the session id;
// portald must never send a guest to the success page on that.
func TestPostStayGrantWithoutASessionIsRefused(t *testing.T) {
	for _, answer := range []map[string]any{
		{"outcome": "GRANTED", "session_id": "", "entitlement_id": "ent-1"},
		{"outcome": "GRANTED", "session_id": "ent-1", "entitlement_id": "ent-1"},
	} {
		s := &pathStub{answers: map[string]any{"/v1/phase5/poststay/convert": answer}}
		h := pathStubHandler(t, s)
		_, out := postStayPost(t, h, "/auth/post-stay-pin", `{"auth_context_id":"ctx-1","package_revision_id":"pkg-a"}`)
		if out.OK || out.SessionID != "" {
			t.Fatalf("scd answer %v reached the guest as success: %+v", answer, out)
		}
	}
}

// A verified PIN with no offers is a contract break; the guest gets the uniform answer, not a dead context.
func TestPostStayVerifiedWithoutOffersIsTheUniformAnswer(t *testing.T) {
	s := &pathStub{answers: map[string]any{
		"/v1/phase5/auth/post-stay-pin": map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1"}}}
	h := pathStubHandler(t, s)
	_, out := postStayPost(t, h, "/auth/post-stay-pin", `{"pin":"K7M4RTQX"}`)
	if out.OK || out.AuthContextID != "" {
		t.Fatalf("a verification with nothing to grant reached the guest: %+v", out)
	}
}
