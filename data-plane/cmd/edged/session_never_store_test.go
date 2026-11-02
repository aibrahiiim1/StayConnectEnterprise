package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

// Never is honoured by the store, not only by the reported policy: nothing but the setting ends a session.
func TestNeverKeepsTheSessionInTheStore(t *testing.T) {
	st := &sessionStore{m: map[string]*session{}, idle: time.Hour, limit: time.Hour}
	tok := st.create(&session{OperatorID: "op"})
	never := sessionPolicy{}
	st.mu.Lock()
	st.m[tok].CreatedAt = time.Now().Add(-90 * 24 * time.Hour)
	st.m[tok].LastSeen = time.Now().Add(-30 * 24 * time.Hour)
	st.mu.Unlock()

	if st.active(tok, never.idle(), never.max(), true) == nil {
		t.Fatal("with both limits at Never a 90-day-old session idle for 30 days is still signed in")
	}
	st.sweep(time.Now().Add(365 * 24 * time.Hour))
	if st.peek(tok) == nil {
		t.Fatal("the sweep must apply the setting, not a fixed store lifetime")
	}

	limited := sessionPolicy{IdleTimeoutMinutes: 30, MaxSessionHours: 12}
	st.mu.Lock()
	st.m[tok].LastSeen = time.Now().Add(-time.Hour)
	st.mu.Unlock()
	if st.active(tok, limited.idle(), limited.max(), true) != nil {
		t.Fatal("once the limits are back, an idle hour ends the session")
	}
}

func TestTheSweepEndsSessionsPastTheSetting(t *testing.T) {
	st := &sessionStore{m: map[string]*session{}, idle: 30 * time.Minute, limit: 12 * time.Hour}
	tok := st.create(&session{OperatorID: "op"})
	st.sweep(time.Now().Add(31 * time.Minute))
	if st.peek(tok) != nil {
		t.Fatal("an idle session must be swept")
	}
}

func TestTheCookieOutlivesALongSession(t *testing.T) {
	w := httptest.NewRecorder()
	(&server{}).setSessionCookie(w, "tok")
	ck := w.Result().Cookies()
	if len(ck) != 1 || time.Until(ck[0].Expires) < 300*24*time.Hour {
		t.Fatalf("the browser must keep the cookie long enough for Never: %+v", ck)
	}
}
