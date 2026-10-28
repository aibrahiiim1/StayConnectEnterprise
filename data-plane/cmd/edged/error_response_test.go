package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// P9 L1: driver and transport detail stays in the log; a refusal a database function wrote for operators is shown.
func TestInternalErrHidesDetailButKeepsOperatorRefusals(t *testing.T) {
	for _, tc := range []struct {
		err       error
		hidden    string
		wantShown string
	}{
		{errors.New(`dial unix /run/stayconnect/scd.sock: connect: no such file`), "/run/stayconnect", ""},
		{&pgconn.PgError{Code: "42883", Message: "function iam_v2.secret_fn(uuid) does not exist"}, "iam_v2.secret_fn", ""},
		{&pgconn.PgError{Code: "P0001", Message: "the threshold must be between 3 and 20"}, "", "the threshold must be between 3 and 20"},
		{&pgconn.PgError{Code: "22023", Message: "say who is asking (2-120 characters)"}, "", "say who is asking"},
	} {
		rec := httptest.NewRecorder()
		internalErr(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusInternalServerError, "x_failed", tc.err)
		body := rec.Body.String()
		if rec.Code != http.StatusInternalServerError || !strings.Contains(body, `"x_failed"`) {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, body)
		}
		if tc.hidden != "" && strings.Contains(body, tc.hidden) {
			t.Errorf("%v: detail %q reached the response: %s", tc.err, tc.hidden, body)
		}
		if tc.wantShown != "" && !strings.Contains(body, tc.wantShown) {
			t.Errorf("%v: the operator refusal was hidden: %s", tc.err, body)
		}
	}
}
