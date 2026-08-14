package main

// THE PUBLIC GUEST SURFACE REFUSES AN IDENTITY IT DID NOT DERIVE.
//
// scd's own decoder already rejects unknown fields, so a stray `stay` or `room` never reached the server's
// logic. That is not the same as the guest surface refusing it. A permissive decoder in portald SILENTLY
// DROPS such a field: the request succeeds, the guest is served from their real device-derived identity, and
// nothing says the parameter was ignored — a surface that looks exactly like one which honours it.
//
// These tests prove three separate things, because passing the first two while failing the third would be
// the dangerous outcome:
//
//  1. an identity-looking field is REFUSED, not ignored;
//  2. the refusal is the ordinary uniform non-success, so it is not itself an oracle;
//  3. the request never reaches scd, so nothing downstream ever sees a guest-supplied subject.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postStayPost(t *testing.T, h *handler, path string, raw string) (*httptest.ResponseRecorder, postStayOut) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(raw)))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := httptest.NewRecorder()
	switch path {
	case "/poststay/issue":
		h.postStayIssue(rec, req)
	default:
		h.postStayAuth(rec, req)
	}
	var out postStayOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable body %q: %v", rec.Body.String(), err)
	}
	return rec, out
}

// The fields a caller might reach for to name someone else. Each is refused on BOTH guest endpoints.
var identityLookingFields = []struct {
	name string
	body string
}{
	{"stay", `{"pin":"K7M4RTQX","stay":"11111111-1111-1111-1111-111111111111"}`},
	{"stay_id", `{"pin":"K7M4RTQX","stay_id":"11111111-1111-1111-1111-111111111111"}`},
	{"room", `{"pin":"K7M4RTQX","room":"412"}`},
	{"room_number", `{"pin":"K7M4RTQX","room_number":"412"}`},
	{"profile", `{"pin":"K7M4RTQX","profile":"22222222-2222-2222-2222-222222222222"}`},
	{"profile_id", `{"pin":"K7M4RTQX","profile_id":"22222222-2222-2222-2222-222222222222"}`},
	{"pms_interface_id", `{"pin":"K7M4RTQX","pms_interface_id":"33333333-3333-3333-3333-333333333333"}`},
	{"last_name", `{"pin":"K7M4RTQX","last_name":"Smith"}`},
	{"reservation_number", `{"pin":"K7M4RTQX","reservation_number":"RES-77"}`},
	{"device", `{"pin":"K7M4RTQX","device":{"ip":"10.0.0.9","mac":"02:00:00:00:00:09"}}`},
}

func TestPostStayRefusesAGuestSuppliedIdentity(t *testing.T) {
	for _, path := range []string{"/poststay/issue", "/auth/post-stay-pin"} {
		for _, tc := range identityLookingFields {
			stub := &scdStub{}
			h := stubHandler(t, stub)
			rec, out := postStayPost(t, h, path, tc.body)
			if out.OK {
				t.Fatalf("%s %s: a guest-supplied identity field was ACCEPTED", path, tc.name)
			}
			if rec.Code != http.StatusOK {
				// The uniform non-success is a 200 with an outcome, exactly like every other guest failure.
				// A different status here would make this refusal distinguishable from a wrong PIN.
				t.Fatalf("%s %s: status %d, want the uniform 200", path, tc.name, rec.Code)
			}
			// ...and it never reached scd. A field that is refused at the edge but forwarded first would
			// still have handed a downstream service a guest-supplied subject to consider.
			if len(stub.calls) != 0 {
				t.Fatalf("%s %s: reached scd anyway: %v", path, tc.name, stub.calls)
			}
		}
	}
}

// The refusal must be INDISTINGUISHABLE from an ordinary wrong PIN — otherwise probing with a junk field
// answers "does this build validate that parameter", which is a fingerprint of the version running.
func TestPostStayStrictRefusalIsTheSameAnswerAsAWrongPIN(t *testing.T) {
	wrongStub := &scdStub{grant: map[string]any{"outcome": "UNAVAILABLE"}}
	wrongH := stubHandler(t, wrongStub)
	wrongRec, _ := postStayPost(t, wrongH, "/auth/post-stay-pin", `{"pin":"WRONGPIN"}`)

	for _, tc := range identityLookingFields {
		stub := &scdStub{}
		h := stubHandler(t, stub)
		rec, _ := postStayPost(t, h, "/auth/post-stay-pin", tc.body)
		if rec.Code != wrongRec.Code {
			t.Fatalf("%s: status %d, wrong PIN gives %d", tc.name, rec.Code, wrongRec.Code)
		}
		if rec.Body.String() != wrongRec.Body.String() {
			t.Fatalf("%s: body %q differs from the wrong-PIN body %q",
				tc.name, rec.Body.String(), wrongRec.Body.String())
		}
	}
}

// Malformed JSON is the same answer too, so "is this even valid JSON" is not learnable either.
func TestPostStayMalformedBodyIsTheSameAnswer(t *testing.T) {
	stub := &scdStub{grant: map[string]any{"outcome": "UNAVAILABLE"}}
	h := stubHandler(t, stub)
	ref, _ := postStayPost(t, h, "/auth/post-stay-pin", `{"pin":"WRONGPIN"}`)

	stub2 := &scdStub{}
	h2 := stubHandler(t, stub2)
	rec, _ := postStayPost(t, h2, "/auth/post-stay-pin", `{"pin":`)
	if rec.Code != ref.Code || rec.Body.String() != ref.Body.String() {
		t.Fatalf("a malformed body is distinguishable: %d %q vs %d %q",
			rec.Code, rec.Body.String(), ref.Code, ref.Body.String())
	}
	if len(stub2.calls) != 0 {
		t.Fatalf("a malformed body reached scd: %v", stub2.calls)
	}
}

// ...and the legitimate shapes still work, or the strictness above would just be a broken endpoint.
func TestPostStayAcceptsTheFieldsItActuallyDefines(t *testing.T) {
	stub := &scdStub{grant: map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1"}}
	h := stubHandler(t, stub)
	_, out := postStayPost(t, h, "/auth/post-stay-pin", `{"pin":"K7M4RTQX"}`)
	if !out.OK || out.AuthContextID != "ctx-1" {
		t.Fatalf("a well-formed PIN submission was refused: %+v", out)
	}
	// The device scd receives is the SERVER-derived one, never anything from the body.
	if len(stub.bodies) != 1 {
		t.Fatalf("expected exactly one scd hop, got %d", len(stub.bodies))
	}
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev["mac"] != "02:00:00:aa:00:01" {
		t.Fatalf("scd was given a device the portal did not derive: %v", dev)
	}

	// The second call, carrying a context and a chosen package, is also accepted.
	stub2 := &scdStub{grant: map[string]any{"outcome": "GRANTED", "session_id": "sess-1"}}
	h2 := stubHandler(t, stub2)
	_, out2 := postStayPost(t, h2, "/auth/post-stay-pin",
		`{"auth_context_id":"ctx-1","package_revision_id":"pkg-1"}`)
	if !out2.OK || out2.SessionID != "sess-1" {
		t.Fatalf("the conversion call was refused: %+v", out2)
	}
}
