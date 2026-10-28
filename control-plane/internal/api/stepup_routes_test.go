package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// Audit Phase 1 M16: the offline request import needs a password step-up like every other appliance action.
func TestOfflineImportRequiresStepUp(t *testing.T) {
	h := (&Base{}).CloudRoutes(nil)
	req := httptest.NewRequest(http.MethodPost, "/offline-activation/requests", bytes.NewReader([]byte(`{}`)))
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "s"})
	s := &auth.Session{OperatorID: "op", IsSuperAdmin: true, Roles: []string{"platform_owner"}, CreatedAt: time.Now()}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(auth.ContextWithSession(req.Context(), s)))
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte("reauth_required")) {
		t.Fatalf("import without step-up = %d %s, want 403 reauth_required", rec.Code, rec.Body.String())
	}
}

// Audit Phase 1 L2: a step-up that cannot be recorded is an error, not a silent ok.
func TestMarkReauthReportsFailure(t *testing.T) {
	if err := MarkReauth(context.Background(), nil, "tok"); err == nil {
		t.Fatal("no store must be an error")
	}
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	defer dead.Close()
	if err := MarkReauth(context.Background(), dead, "tok"); err == nil {
		t.Fatal("an unreachable store must be an error")
	}
}
