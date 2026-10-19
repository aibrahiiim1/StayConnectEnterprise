package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// D-R3: new recovery codes are refused before anything else unless a 6-digit authenticator code comes with the
// password. A recovery code is not one. (The wrong, replayed and correct codes need the database and scd; they
// are in operator_mfa_integration_test.go and cmd/scd.)
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

// The pending sign-in is shared between requests: a handler gets a copy, and the one change made after creation
// (an authenticator found already set up) goes through the lock. Run with -race.
func TestPendingSigninsHandOutCopies(t *testing.T) {
	var p pendingSignins
	tok := p.create(&pendingSignin{OperatorID: "op-1", Enrol: true, Roles: []string{"site_admin"}})
	a := p.get(tok)
	a.Enrol = false
	a.Roles = nil
	if b := p.get(tok); !b.Enrol || len(b.Roles) != 1 {
		t.Fatalf("a handler's copy changed the stored sign-in: %+v", b)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.setEnrol(tok, i%2 == 0)
		}
	}()
	for i := 0; i < 200; i++ {
		if ps := p.get(tok); ps != nil {
			_ = ps.Enrol
		}
	}
	<-done
	p.setEnrol(tok, false)
	if p.get(tok).Enrol {
		t.Fatal("setEnrol did not change the stored sign-in")
	}
	p.dropOperator("op-1")
	if p.get(tok) != nil {
		t.Fatal("dropOperator left the pending sign-in")
	}
	if passwordFingerprint("a") == passwordFingerprint("b") || passwordFingerprint("a") != passwordFingerprint("a") {
		t.Fatal("passwordFingerprint does not tell hashes apart")
	}
}
