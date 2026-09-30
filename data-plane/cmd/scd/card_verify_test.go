package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

func waitChecks(t *testing.T, n *int32, want int32) {
	t.Helper()
	for i := 0; i < 200 && atomic.LoadInt32(n) < want; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
}

// Card payment is never ready before the provider has accepted the credentials, and a refusal keeps it unready.
func TestCardVerifier_ReadyOnlyAfterTheProviderAcceptsTheCredentials(t *testing.T) {
	v := newCardVerifier()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.now = func() time.Time { return now }
	var calls int32
	var fail atomic.Bool
	check := func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		if fail.Load() {
			return errors.New("401 invalid api key")
		}
		return nil
	}
	fp := credentialFingerprint("acct-1", payment.ModeTest, payment.Credentials{"secret_key": "sk_test_a"})
	if v.ready(fp, check) {
		t.Fatal("ready before any provider check")
	}
	waitChecks(t, &calls, 1)
	if !v.ready(fp, check) {
		t.Fatal("not ready after the provider accepted the credentials")
	}
	// A rotated secret is a different fingerprint: verified again before it is trusted.
	fp2 := credentialFingerprint("acct-1", payment.ModeTest, payment.Credentials{"secret_key": "sk_test_b"})
	if fp2 == fp {
		t.Fatal("rotating a secret must change the fingerprint")
	}
	fail.Store(true)
	if v.ready(fp2, check) {
		t.Fatal("rotated credentials trusted before verification")
	}
	waitChecks(t, &calls, 2)
	if v.ready(fp2, check) {
		t.Fatal("refused credentials made card payment ready")
	}
	// A refusal is retried after 5 minutes, not on every request.
	before := atomic.LoadInt32(&calls)
	v.ready(fp2, check)
	now = now.Add(6 * time.Minute)
	fail.Store(false)
	v.ready(fp2, check)
	waitChecks(t, &calls, before+1)
	if atomic.LoadInt32(&calls) != before+1 || !v.ready(fp2, check) {
		t.Fatalf("expected exactly one retry after the backoff and readiness after success: calls %d->%d", before, atomic.LoadInt32(&calls))
	}
	// A success lapses if never refreshed.
	now = now.Add(31 * time.Minute)
	fail.Store(true)
	if v.ready(fp2, check) {
		t.Fatal("a success older than 30 minutes still counted")
	}
}

// A wildcard extra domain is refused at save: the garden opens the addresses a name resolves to, and a wildcard
// names no host (automated review finding on PR #202).
func TestPaymentDomainsSave_RefusesWildcards(t *testing.T) {
	s := &server{}
	for _, d := range []string{"*.provider.example", "a.*.provider.example"} {
		rec := httptest.NewRecorder()
		body := `{"domains":["checkout.provider.example","` + d + `"],"operator":"op","reason":"test"}`
		s.paymentDomainsSave(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Wildcard") {
			t.Fatalf("%s: %d %s", d, rec.Code, rec.Body.String())
		}
	}
}
