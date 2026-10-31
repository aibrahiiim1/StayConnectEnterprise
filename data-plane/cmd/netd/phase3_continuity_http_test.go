package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func postRevoke(t *testing.T, s *server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/phase3/continuity/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.continuityRevoke(rec, req)
	return rec
}

func TestContinuityRevokeRoute_ValidatesJournalsAndDropsTheSession(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	b := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-b")
	ss := []shapeplan.Session{a, b}
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, ss, nil, nil) }, ss...)
	h.run(90*time.Second, nil)
	s := &server{phase3: h.p}

	for name, body := range map[string]string{
		"malformed":   `{`,
		"no reason":   `{"session_ids":["c-1"]}`,
		"nothing":     `{"reason":"CHECKOUT"}`,
		"bad ip":      `{"ip":"not-an-ip","reason":"CHECKOUT"}`,
		"long reason": `{"session_ids":["c-1"],"reason":"` + strings.Repeat("x", 65) + `"}`,
	} {
		if rec := postRevoke(t, s, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", name, rec.Code)
		}
	}
	if !h.authorized(a.IP) {
		t.Fatal("a refused request must change nothing")
	}

	if rec := postRevoke(t, s, `{"session_ids":["c-1"],"reason":"CHECKOUT_BOUNDARY"}`); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if h.authorized(a.IP) {
		t.Fatal("a revoked session must be removed before the route answers")
	}
	if !h.authorized(b.IP) {
		t.Fatal("the other session must be untouched")
	}
	raw, err := os.ReadFile(h.p.cont.journalPath)
	if err != nil || !strings.Contains(string(raw), "CHECKOUT_BOUNDARY") {
		t.Fatalf("the revocation was not journalled before the answer: %v %s", err, raw)
	}

	// An entitlement-wide revocation ends every session of it.
	if rec := postRevoke(t, s, `{"entitlement_ids":["ent-b"],"reason":"ENTITLEMENT_TERMINATED"}`); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if h.authorized(b.IP) {
		t.Fatal("an entitlement revocation must end its sessions")
	}
	st := h.p.contStatus()
	if st.Revocations != 2 || st.Sessions != 0 {
		t.Fatalf("status %+v", st)
	}
}

// A journal that cannot be written is reported to the caller, who must retry; the in-memory effect has still happened (fail closed).
func TestContinuityRevokeRoute_AnUnwritableJournalIsAnErrorNotASilentSuccess(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	h.run(90*time.Second, nil)
	h.p.cont.journalPath = h.dir // a directory: the atomic write cannot replace it
	rec := postRevoke(t, &server{phase3: h.p}, `{"session_ids":["c-1"],"reason":"ADMIN_DISCONNECT"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d: an unwritten journal must not be acknowledged", rec.Code)
	}
	if h.authorized(a.IP) {
		t.Fatal("even so, the session must have been removed (fail closed)")
	}
	_ = context.Background()
}
