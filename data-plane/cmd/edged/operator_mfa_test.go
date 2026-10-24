package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// New recovery codes are refused unless a 6-digit authenticator code accompanies the password; a recovery code is not one.
func TestNewRecoveryCodesRefusesWithoutAnAuthenticatorCode(t *testing.T) {
	s := &server{}
	for _, body := range []string{
		`{"password":"pw"}`,
		`{"password":"pw","code":""}`,
		`{"password":"pw","code":"ABCDE-FGHJK"}`,
		`{"password":"pw","code":"12345"}`,
		`{"password":"pw","code":"12345a"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/edge/v1/auth/mfa/recovery-codes", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), sessKey, &session{OperatorID: "op-1"}))
		rec := httptest.NewRecorder()
		s.newOwnRecoveryCodes(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "code_required") {
			t.Fatalf("%s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	for code, want := range map[string]bool{"123456": true, "123 456": true, "12345": false, "ABCDEFGHJK": false} {
		if got := isTOTPCode(strings.ReplaceAll(code, " ", "")); got != want {
			t.Fatalf("isTOTPCode(%q) = %v", code, got)
		}
	}
}

// The pending sign-in is shared between requests: a handler gets a copy, so its changes never reach the stored one.
// Run with -race.
func TestPendingSigninsHandOutCopies(t *testing.T) {
	var p pendingSignins
	tok := p.create(&pendingSignin{OperatorID: "op-1", Roles: []string{"site_admin"}})
	a := p.get(tok)
	a.MustChangePassword = true
	a.Roles = nil
	if b := p.get(tok); b.MustChangePassword || len(b.Roles) != 1 {
		t.Fatalf("a handler's copy changed the stored sign-in: %+v", b)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if ps := p.get(tok); ps != nil {
				ps.Roles = append(ps.Roles, "x")
			}
		}
	}()
	for i := 0; i < 200; i++ {
		if ps := p.get(tok); ps != nil {
			_ = len(ps.Roles)
		}
	}
	<-done
	if other := p.create(&pendingSignin{OperatorID: "op-1"}); p.get(tok) != nil || p.get(other) == nil {
		t.Fatal("a new password step did not replace the operator's earlier pending sign-in")
	}
	p.dropOperator("op-1")
	if p.get(tok) != nil {
		t.Fatal("dropOperator left the pending sign-in")
	}
	if passwordFingerprint("a") == passwordFingerprint("b") || passwordFingerprint("a") != passwordFingerprint("a") {
		t.Fatal("passwordFingerprint does not tell hashes apart")
	}
}

// Turning two-step sign-in off refuses before the password is checked when no code accompanies it.
func TestDisableOwnMFARefusesWithoutACode(t *testing.T) {
	s := &server{}
	for _, body := range []string{`{"password":"pw"}`, `{"password":"pw","code":"  "}`, `{"password":"pw","code":"` + strings.Repeat("A", 33) + `"}`} {
		req := httptest.NewRequest(http.MethodPost, "/edge/v1/auth/mfa/disable", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), sessKey, &session{OperatorID: "op-1"}))
		rec := httptest.NewRecorder()
		s.disableOwnMFA(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "code_required") {
			t.Fatalf("%s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/edge/v1/auth/mfa/confirm", strings.NewReader(`{"code":"ABCDE-FGHJK"}`))
	req = req.WithContext(context.WithValue(req.Context(), sessKey, &session{OperatorID: "op-1"}))
	rec := httptest.NewRecorder()
	s.confirmOwnMFA(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "code_required") {
		t.Fatalf("confirm with a recovery code = %d %s", rec.Code, rec.Body.String())
	}
}
