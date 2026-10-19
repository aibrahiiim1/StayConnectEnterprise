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
