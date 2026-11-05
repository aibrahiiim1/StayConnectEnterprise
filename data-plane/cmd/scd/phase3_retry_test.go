package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A pass that met a deadlock is discarded and run once more; only the answering pass reaches the client and leaves a record.
func TestRunRetriedRerunsOnlyARetryableFailureAndOnlyOnce(t *testing.T) {
	deadlock := fmt.Errorf("grant: %w", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"})
	for _, c := range []struct {
		name     string
		errs     []error // the error each pass meets
		passes   int
		recorded int
		wantBody string
	}{
		{"deadlock then success", []error{deadlock, nil}, 2, 1, "pass-2"},
		{"serialization then success", []error{&pgconn.PgError{Code: "40001"}, nil}, 2, 1, "pass-2"},
		{"deadlock twice: the second answer stands", []error{deadlock, deadlock}, 2, 1, "pass-2"},
		{"an ordinary error is never retried", []error{errors.New("MAX_DEVICES_REACHED")}, 1, 1, "pass-1"},
		{"success", []error{nil}, 1, 1, "pass-1"},
	} {
		passes, recorded := 0, 0
		rec := httptest.NewRecorder()
		runRetried(rec, httptest.NewRequest(http.MethodPost, "/", nil), func(w http.ResponseWriter, r *http.Request) {
			passes++
			defer func() {
				if !retrying(r.Context()) {
					recorded++
				}
			}()
			if err := c.errs[passes-1]; err != nil {
				noteRetry(r.Context(), err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "pass-%d", passes)
		})
		if passes != c.passes || recorded != c.recorded || rec.Body.String() != c.wantBody {
			t.Errorf("%s: %d passes, %d records, body %q; want %d, %d, %q", c.name, passes, recorded, rec.Body.String(),
				c.passes, c.recorded, c.wantBody)
		}
	}
}
