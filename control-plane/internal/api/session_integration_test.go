package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// memSessions holds sessions in memory in place of Redis; the policy under test is RequireAuth +
// DBSessionChecker against the real database.
type memSessions struct {
	mu        sync.Mutex
	m         map[string]*auth.Session
	destroyed []string
}

func (s *memSessions) Get(_ context.Context, tok string) (*auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[tok]; ok {
		c := *v
		return &c, nil
	}
	return nil, nil
}

func (s *memSessions) Destroy(_ context.Context, tok string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, tok)
	s.destroyed = append(s.destroyed, tok)
	return nil
}

// guarded runs one request through RequireAuth + a permission check and reports the status.
func guarded(store *memSessions, f *fixture, tok, perm string) int {
	h := auth.RequireAuth(store, auth.DBSessionChecker{DB: f.pool})(auth.RequirePermission(perm)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	req := httptest.NewRequest(http.MethodGet, "/cloud/v1/appliances", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// X-3: disabling, demoting, re-passwording or removing an operator ends their open sessions at the next
// request; the absolute lifetime setting ends old sessions however active they are.
func TestOperatorSessionsEndWhenAccessChanges(t *testing.T) {
	f := newFixture(t)
	mk := func(email, role string) (string, *auth.Session) {
		var id string
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (email, password_hash, status) VALUES ($1,'x','active') RETURNING id::text`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,$2)`, id, role); err != nil {
			t.Fatal(err)
		}
		return id, &auth.Session{OperatorID: id, Email: email, Roles: []string{role}, IsSuperAdmin: true,
			CreatedAt: time.Now(), LastSeenAt: time.Now(), ExpiresAt: time.Now().Add(12 * time.Hour)}
	}
	store := &memSessions{m: map[string]*auth.Session{}}
	disabled, sd := mk("disabled@central.test", "platform_admin")
	demoted, sm := mk("demoted@central.test", "platform_admin")
	repass, sp := mk("repass@central.test", "platform_admin")
	removed, sr := mk("removed@central.test", "platform_admin")
	store.m["t-dis"], store.m["t-dem"], store.m["t-rep"], store.m["t-rem"] = sd, sm, sp, sr
	for _, tok := range []string{"t-dis", "t-dem", "t-rep", "t-rem"} {
		if c := guarded(store, f, tok, auth.PermAppliancesManage); c != http.StatusNoContent {
			t.Fatalf("%s before any change: %d", tok, c)
		}
	}

	patch := func(id string, body map[string]any) {
		rec := f.operatorCall(f.base.patchTeam, http.MethodPatch, id, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	patch(disabled, map[string]any{"status": "disabled"})
	patch(demoted, map[string]any{"role": "platform_support"})
	patch(repass, map[string]any{"password": "a-new-password-1"})
	if rec := f.operatorCall(f.base.deleteTeam, http.MethodDelete, removed, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}

	for _, tok := range []string{"t-dis", "t-dem", "t-rep", "t-rem"} {
		if c := guarded(store, f, tok, auth.PermFleetView); c != http.StatusUnauthorized {
			t.Errorf("%s: the session must end at the next request, got %d", tok, c)
		}
		if _, ok := store.m[tok]; ok {
			t.Errorf("%s: the ended session must be destroyed", tok)
		}
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action IN ('user.updated','user.removed') AND (payload->>'sessions_ended')::bool`); n != 4 {
		t.Fatalf("each change must record that sessions were ended, got %d", n)
	}

	// A new sign-in of the demoted operator works, with the NEW role only.
	_, fresh := mk("fresh@central.test", "platform_admin")
	store.m["t-new"] = fresh
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM operator_roles WHERE operator_id=$1`, fresh.OperatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_support')`, fresh.OperatorID); err != nil {
		t.Fatal(err)
	}
	if c := guarded(store, f, "t-new", auth.PermAppliancesManage); c != http.StatusForbidden {
		t.Fatalf("roles must be re-read per request: a role removed in the database must stop authorising, got %d", c)
	}
	if c := guarded(store, f, "t-new", auth.PermFleetView); c != http.StatusNoContent {
		t.Fatalf("the remaining role still works, got %d", c)
	}

	// The absolute lifetime: a 3-hour-old, still active session ends once the cap is set to 2 hours.
	_, old := mk("old@central.test", "platform_admin")
	old.CreatedAt = time.Now().Add(-3 * time.Hour)
	store.m["t-old"] = old
	if c := guarded(store, f, "t-old", auth.PermFleetView); c != http.StatusNoContent {
		t.Fatalf("under the default 24 h cap the session continues, got %d", c)
	}
	rec := f.operatorCall(f.base.patchSecuritySettings, http.MethodPatch, "", map[string]any{"operator_session_max_hours": 2, "reason": "shared desk"})
	if rec.Code != http.StatusOK || decode(t, rec)["operator_session_max_hours"].(float64) != 2 {
		t.Fatalf("set cap: %d %s", rec.Code, rec.Body.String())
	}
	if c := guarded(store, f, "t-old", auth.PermFleetView); c != http.StatusUnauthorized {
		t.Fatalf("a session older than the absolute cap must end, got %d", c)
	}
	for _, bad := range []any{0, 169, -1} {
		if rec := f.operatorCall(f.base.patchSecuritySettings, http.MethodPatch, "", map[string]any{"operator_session_max_hours": bad, "reason": "x"}); rec.Code != http.StatusBadRequest {
			t.Errorf("cap %v must be refused, got %d", bad, rec.Code)
		}
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='settings.security_updated'
                      AND payload->'operator_session_max_hours'->>'to' = '2'`); n != 1 {
		t.Fatal("the setting change must be audited with from/to")
	}
}
