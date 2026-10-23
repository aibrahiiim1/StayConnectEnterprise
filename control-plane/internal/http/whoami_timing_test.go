package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

func TestWhoamiReportsSessionExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sess := &auth.Session{OperatorID: "op", Email: "a@example.com", Roles: []string{"platform_admin"},
		CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(12 * time.Hour)}
	st := auth.OperatorState{Found: true, Status: "active", Email: "a@example.com", Roles: []string{"platform_admin"},
		MaxHours: 8, IdleMinutes: 20}
	fresh, _, err := auth.Validate(sess, st, now)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
	req = req.WithContext(auth.ContextWithSession(req.Context(), fresh))
	rec := httptest.NewRecorder()
	authDeps{}.whoami(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		OperatorID        string     `json:"operator_id"`
		IdleExpiresAt     *time.Time `json:"idle_expires_at"`
		AbsoluteExpiresAt *time.Time `json:"absolute_expires_at"`
		ServerTime        *time.Time `json:"server_time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OperatorID != "op" {
		t.Fatalf("operator_id %q", got.OperatorID)
	}
	if want := sess.LastSeenAt.Add(20 * time.Minute); got.IdleExpiresAt == nil || !got.IdleExpiresAt.Equal(want) {
		t.Errorf("idle_expires_at = %v, want %v", got.IdleExpiresAt, want)
	}
	if want := sess.CreatedAt.Add(8 * time.Hour); got.AbsoluteExpiresAt == nil || !got.AbsoluteExpiresAt.Equal(want) {
		t.Errorf("absolute_expires_at = %v, want %v", got.AbsoluteExpiresAt, want)
	}
	if got.ServerTime == nil || got.ServerTime.Sub(now) > time.Minute || now.Sub(*got.ServerTime) > time.Minute {
		t.Errorf("server_time = %v", got.ServerTime)
	}

	// Without a computed policy the times are omitted, not invented.
	bare := *sess
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
	req = req.WithContext(auth.ContextWithSession(req.Context(), &bare))
	rec = httptest.NewRecorder()
	authDeps{}.whoami(rec, req)
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"idle_expires_at", "absolute_expires_at"} {
		if _, ok := raw[k]; ok {
			t.Errorf("%s must be omitted when unknown: %s", k, rec.Body.String())
		}
	}
}
