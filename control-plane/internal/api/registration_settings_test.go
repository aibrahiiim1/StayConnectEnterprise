package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// The registration settings are changed only with team.manage: a read-only role is refused.
func TestRegistrationSettingsPatchIsGuarded(t *testing.T) {
	h := (&Base{}).CloudRoutes(nil)
	req := httptest.NewRequest(http.MethodPatch, "/settings/registration", strings.NewReader(`{"max_waiting_appliances":50,"reason":"x"}`))
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "s"})
	s := &auth.Session{OperatorID: "op", Roles: []string{"platform_support"}, CreatedAt: time.Now()}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(auth.ContextWithSession(req.Context(), s)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PATCH as platform_support = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// Validation runs before any database access: a missing reason or an out-of-bounds limit is a 400.
func TestRegistrationSettingsValidation(t *testing.T) {
	b := &Base{}
	for name, body := range map[string]string{
		"no value":     `{"reason":"x"}`,
		"no reason":    `{"max_waiting_appliances":50}`,
		"blank reason": `{"max_waiting_appliances":50,"reason":"  "}`,
		"below min":    `{"max_waiting_appliances":9,"reason":"x"}`,
		"above max":    `{"max_waiting_appliances":5001,"reason":"x"}`,
	} {
		rec := httptest.NewRecorder()
		b.patchRegistrationSettings(rec, httptest.NewRequest(http.MethodPatch, "/settings/registration", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
}
