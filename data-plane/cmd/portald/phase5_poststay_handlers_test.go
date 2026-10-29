package main

// The guest surface must refuse, not silently drop, an identity it did not derive: the field is refused, the
// refusal is the uniform non-success (no oracle), and the request never reaches scd.

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
	h.postStayAuth(rec, req)
	var out postStayOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable body %q: %v", rec.Body.String(), err)
	}
	return rec, out
}

var identityLookingFields = []struct {
	name string
	body string
}{
	{"stay", `{"room":"101","reservation":"R1","stay":"11111111-1111-1111-1111-111111111111"}`},
	{"stay_id", `{"room":"101","reservation":"R1","stay_id":"11111111-1111-1111-1111-111111111111"}`},
	{"room_number", `{"room":"101","reservation":"R1","room_number":"412"}`},
	{"profile", `{"room":"101","reservation":"R1","profile":"22222222-2222-2222-2222-222222222222"}`},
	{"profile_id", `{"room":"101","reservation":"R1","profile_id":"22222222-2222-2222-2222-222222222222"}`},
	{"pms_interface_id", `{"room":"101","reservation":"R1","pms_interface_id":"33333333-3333-3333-3333-333333333333"}`},
	{"reservation_number", `{"room":"101","reservation":"R1","reservation_number":"RES-77"}`},
	{"pin", `{"room":"101","reservation":"R1","pin":"K7M4RTQX"}`},
	{"device", `{"room":"101","reservation":"R1","device":{"ip":"10.0.0.9","mac":"02:00:00:00:00:09"}}`},
}

func TestPostStayRefusesAGuestSuppliedIdentity(t *testing.T) {
	for _, path := range []string{"/auth/post-stay"} {
		for _, tc := range identityLookingFields {
			stub := &scdStub{}
			h := stubHandler(t, stub)
			rec, out := postStayPost(t, h, path, tc.body)
			if out.OK {
				t.Fatalf("%s %s: a guest-supplied identity field was ACCEPTED", path, tc.name)
			}
			if rec.Code != http.StatusOK {
				// A different status would be distinguishable from a wrong answer.
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

// The refusal must be indistinguishable from a wrong answer, or it fingerprints the build.
func TestPostStayStrictRefusalIsTheSameAnswerAsAWrongPIN(t *testing.T) {
	wrongStub := &scdStub{grant: map[string]any{"outcome": "UNAVAILABLE"}}
	wrongH := stubHandler(t, wrongStub)
	wrongRec, _ := postStayPost(t, wrongH, "/auth/post-stay", `{"room":"999","reservation":"WRONG"}`)

	for _, tc := range identityLookingFields {
		stub := &scdStub{}
		h := stubHandler(t, stub)
		rec, _ := postStayPost(t, h, "/auth/post-stay", tc.body)
		if rec.Code != wrongRec.Code {
			t.Fatalf("%s: status %d, wrong answer gives %d", tc.name, rec.Code, wrongRec.Code)
		}
		if rec.Body.String() != wrongRec.Body.String() {
			t.Fatalf("%s: body %q differs from the wrong-answer body %q",
				tc.name, rec.Body.String(), wrongRec.Body.String())
		}
	}
}

func TestPostStayMalformedBodyIsTheSameAnswer(t *testing.T) {
	stub := &scdStub{grant: map[string]any{"outcome": "UNAVAILABLE"}}
	h := stubHandler(t, stub)
	ref, _ := postStayPost(t, h, "/auth/post-stay", `{"room":"999","reservation":"WRONG"}`)

	stub2 := &scdStub{}
	h2 := stubHandler(t, stub2)
	rec, _ := postStayPost(t, h2, "/auth/post-stay", `{"room":`)
	if rec.Code != ref.Code || rec.Body.String() != ref.Body.String() {
		t.Fatalf("a malformed body is distinguishable: %d %q vs %d %q",
			rec.Code, rec.Body.String(), ref.Code, ref.Body.String())
	}
	if len(stub2.calls) != 0 {
		t.Fatalf("a malformed body reached scd: %v", stub2.calls)
	}
}

func TestPostStayAcceptsTheFieldsItActuallyDefines(t *testing.T) {
	stub := &scdStub{grant: map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx-1",
		"offers": []map[string]any{{"package_revision_id": "pkg-1", "code": "A"}, {"package_revision_id": "pkg-2", "code": "B"}}}}
	h := stubHandler(t, stub)
	_, out := postStayPost(t, h, "/auth/post-stay", `{"room":"101","reservation":"R1"}`)
	if !out.OK || out.AuthContextID != "ctx-1" || !out.NeedsChoice {
		t.Fatalf("a well-formed submission was refused: %+v", out)
	}
	if len(stub.bodies) != 1 {
		t.Fatalf("expected exactly one scd hop, got %d", len(stub.bodies))
	}
	dev, _ := stub.bodies[0]["device"].(map[string]any)
	if dev["mac"] != "02:00:00:aa:00:01" {
		t.Fatalf("scd was given a device the portal did not derive: %v", dev)
	}

	stub2 := &scdStub{grant: map[string]any{"outcome": "GRANTED", "session_id": "sess-1", "entitlement_id": "ent-1"}}
	h2 := stubHandler(t, stub2)
	_, out2 := postStayPost(t, h2, "/auth/post-stay",
		`{"auth_context_id":"ctx-1","package_revision_id":"pkg-1"}`)
	if !out2.OK || out2.SessionID != "sess-1" {
		t.Fatalf("the conversion call was refused: %+v", out2)
	}
}
