package api

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

func TestAnAdministratorResetsAColleaguesTwoStepSignIn(t *testing.T) {
	f := newFixture(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(f.pool, key)
	if err != nil {
		t.Fatal(err)
	}
	var colleague string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('colleague@central.test','x','active') RETURNING id::text`).Scan(&colleague); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_support')`, colleague); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mfa.Enrol(f.ctx, colleague, "colleague@central.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE operator_mfa SET confirmed_at = now() WHERE operator_id = $1`, colleague); err != nil {
		t.Fatal(err)
	}
	// The colleague is locked out from two addresses; someone else is locked too.
	f.base.MFA = mfa
	for _, addr := range []string{"203.0.113.1", "203.0.113.2"} {
		if _, err := auth.SigninFailed(f.ctx, f.pool, mfa.SigninKey("colleague@central.test", addr), mfa.SigninName("colleague@central.test")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := auth.SigninFailed(f.ctx, f.pool, mfa.SigninKey("other@central.test", "203.0.113.1"), mfa.SigninName("other@central.test")); err != nil {
		t.Fatal(err)
	}
	if rec := f.operatorCall(f.base.listTeam, http.MethodGet, "", nil); !containsEnrolled(t, rec.Body.Bytes(), colleague) {
		t.Fatalf("the Team list does not show the colleague's authenticator: %s", rec.Body.String())
	}

	if rec := f.operatorCall(f.base.teamMFAReset, http.MethodPost, f.session.OperatorID, map[string]string{"reason": "lost phone"}); rec.Code != http.StatusConflict {
		t.Fatalf("an administrator reset their own two-step sign-in: %d", rec.Code)
	}
	if rec := f.operatorCall(f.base.teamMFAReset, http.MethodPost, colleague, map[string]string{"reason": ""}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a reset without a reason: %d", rec.Code)
	}
	rec := f.operatorCall(f.base.teamMFAReset, http.MethodPost, colleague, map[string]string{"reason": "lost their phone"})
	if rec.Code != http.StatusOK || decode(t, rec)["removed"] != true {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body.String())
	}
	st, _ := mfa.Status(f.ctx, colleague)
	if st.Enrolled {
		t.Fatal("the authenticator survived the reset")
	}
	if n := f.count(`SELECT count(*) FROM operators WHERE id = $1 AND sessions_not_before IS NOT NULL`, colleague); n != 1 {
		t.Fatal("the colleague's sessions were not ended")
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='user.mfa_reset' AND target_id=$1 AND payload->>'reason'='lost their phone'
	                 AND payload->>'signin_locks_cleared' = '2'`, colleague); n != 1 {
		t.Fatal("the reset was not audited with its reason and the locks it cleared")
	}
	// The reset lifted the colleague's sign-in locks from every address, and nobody else's.
	if n := f.count(`SELECT count(*) FROM operator_signin_throttle WHERE name_hash = $1`, mfa.SigninName("colleague@central.test")); n != 0 {
		t.Fatalf("%d of the colleague's sign-in locks survived the reset", n)
	}
	if n := f.count(`SELECT count(*) FROM operator_signin_throttle WHERE name_hash = $1`, mfa.SigninName("other@central.test")); n != 1 {
		t.Fatal("the reset cleared someone else's sign-in lock")
	}
}

func containsEnrolled(t *testing.T, body []byte, id string) bool {
	t.Helper()
	var out struct {
		Items []UserRow `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	for _, u := range out.Items {
		if u.ID == id {
			return u.TwoStepEnrolled
		}
	}
	return false
}
