package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The idle warning's "Stay signed in" must count as activity, and the background whoami check must not (0132):
// otherwise the button would not keep the session, or an open tab would never be signed out.
func TestKeepaliveCountsAsActivityAndWhoamiDoesNot(t *testing.T) {
	s := &server{sessions: newSessionStore(sessionStoreCap)}
	tok := s.sessions.create(&session{OperatorID: "op-1", Email: "op@example.test"})
	idleSince := time.Now().Add(-20 * time.Minute)
	s.sessions.mu.Lock()
	s.sessions.m[tok].LastSeen = idleSince
	s.sessions.mu.Unlock()

	call := func(path string, h http.HandlerFunc) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		rec := httptest.NewRecorder()
		s.requireAuth(h).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return body
	}
	lastSeen := func() time.Time {
		s.sessions.mu.Lock()
		defer s.sessions.mu.Unlock()
		return s.sessions.m[tok].LastSeen
	}

	call("/edge/v1/auth/whoami", s.whoami)
	if !lastSeen().Equal(idleSince) {
		t.Fatal("the background whoami check counted as activity")
	}

	body := call("/edge/v1/auth/keepalive", s.keepalive)
	if !lastSeen().After(idleSince.Add(19 * time.Minute)) {
		t.Fatal("keepalive did not count as activity")
	}
	ends, err := time.Parse(time.RFC3339Nano, body["expires_at"].(string))
	if err != nil {
		t.Fatalf("expires_at: %v", err)
	}
	// Default idle limit is 30 minutes from the keepalive itself.
	if d := time.Until(ends); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("expires_at is %v away, want about 30 minutes", d)
	}
}

// Keepalive cannot outlive the absolute limit: the end it reports stops at the sign-in time plus the maximum.
func TestKeepaliveDoesNotMoveTheAbsoluteLimit(t *testing.T) {
	sess := &session{CreatedAt: time.Now().Add(-defaultSessionPolicy.max() + 5*time.Minute), LastSeen: time.Now()}
	ends := sessionEnds(sess, defaultSessionPolicy)
	if want := sess.CreatedAt.Add(defaultSessionPolicy.max()); !ends.Equal(want) {
		t.Fatalf("ends = %v, want the absolute limit %v", ends, want)
	}
}

// A console poll carries X-OneGate-Background and must not extend the idle limit; the same request without it
// is an operator's action and must.
func TestBackgroundRequestsAreNotActivity(t *testing.T) {
	s := &server{sessions: newSessionStore(sessionStoreCap)}
	tok := s.sessions.create(&session{OperatorID: "op-1", Email: "op@example.test"})
	idleSince := time.Now().Add(-20 * time.Minute)
	s.sessions.mu.Lock()
	s.sessions.m[tok].LastSeen = idleSince
	s.sessions.mu.Unlock()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	call := func(path string, background bool) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		if background {
			req.Header.Set(backgroundHeader, "1")
		}
		rec := httptest.NewRecorder()
		s.requireAuth(ok).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
	lastSeen := func() time.Time {
		s.sessions.mu.Lock()
		defer s.sessions.mu.Unlock()
		return s.sessions.m[tok].LastSeen
	}
	call("/edge/v1/dashboard", true)
	if !lastSeen().Equal(idleSince) {
		t.Fatal("a background poll counted as activity")
	}
	call("/edge/v1/auth/keepalive", true)
	if lastSeen().Equal(idleSince) {
		t.Fatal("keepalive did not count as activity")
	}
	s.sessions.mu.Lock()
	s.sessions.m[tok].LastSeen = idleSince
	s.sessions.mu.Unlock()
	call("/edge/v1/dashboard", false)
	if !lastSeen().After(idleSince.Add(19 * time.Minute)) {
		t.Fatal("an operator's request did not count as activity")
	}
}
