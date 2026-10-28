package main

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

// D1-L6: financial requests reuse one engine instead of building one (and possibly a new pool) each time; a failed
// construction is not kept.
func TestPaymentEngineIsBuiltOnceAndFailuresAreRetried(t *testing.T) {
	old := newPaymentEngine
	t.Cleanup(func() {
		newPaymentEngine = old
		paymentEngineMu.Lock()
		paymentEngines = map[*pgxpool.Pool]*payment.Engine{}
		paymentEngineMu.Unlock()
	})
	builds, fail := 0, true
	newPaymentEngine = func(*pgxpool.Pool) (*payment.Engine, error) {
		builds++
		if fail {
			return nil, errors.New("not configured")
		}
		return &payment.Engine{}, nil
	}
	s := &server{}
	if _, err := s.paymentEngine(); err == nil {
		t.Fatal("a failed construction is an error")
	}
	fail = false
	a, err := s.paymentEngine()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.paymentEngine()
	c, _ := s.paymentEngine()
	if a != b || b != c {
		t.Fatal("every request must get the same engine")
	}
	if builds != 2 {
		t.Fatalf("built %d times, want 2 (one failure, one success)", builds)
	}
}
