package pmsd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type failingApplier struct {
	err   error
	calls int
}

func (f *failingApplier) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	f.calls++
	return false, f.err
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func runOnce(t *testing.T, err error) string {
	t.Helper()
	var buf syncBuf
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	runApplierScope(ctx, &failingApplier{err: err}, applierScope{Interface: "i1"},
		applierConfig{ErrorPause: time.Hour, IdlePause: time.Hour}, log)
	return buf.String()
}

// Audit P7 L12: a database error's message and detail (which can carry a constraint's key values) never reach the
// applier's log; its SQLSTATE does. An engine sentinel is still logged as text.
func TestApplierLog_DatabaseErrorTextNotLogged(t *testing.T) {
	pg := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint",
		Detail: "Key (reservation)=(GUEST-SECRET-42) already exists.", ConstraintName: "stays_res_key"}
	out := runOnce(t, fmt.Errorf("apply: %w", pg))
	if strings.Contains(out, "GUEST-SECRET-42") || strings.Contains(out, "duplicate key value") {
		t.Fatalf("database error text reached the log: %s", out)
	}
	if !strings.Contains(out, `"sqlstate":"23505"`) {
		t.Fatalf("SQLSTATE missing from the log: %s", out)
	}

	sentinel := errors.New("stayengine: checkout requires a wired Checkout Converter")
	if out := runOnce(t, sentinel); !strings.Contains(out, sentinel.Error()) {
		t.Fatalf("an engine sentinel must still be logged: %s", out)
	}
}
