package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/deployment"
)

// A connection test is a real provider call. Without the provider-call capability it is refused before anything is read
// or contacted, whatever card state exists.
func TestConnectionTestRefusedWithoutProviderCalls(t *testing.T) {
	s := &server{card: &cardState{}}
	if s.ceiling.Available(deployment.CapCardProviderCalls) {
		t.Fatal("a zero ceiling must not permit provider calls")
	}
	rec := httptest.NewRecorder()
	s.paymentAccountTest(rec, httptest.NewRequest(http.MethodPost, "/v1/payment/accounts/x/test", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "provider_calls_not_authorised") {
		t.Fatalf("got %d %s, want 409 provider_calls_not_authorised", rec.Code, rec.Body.String())
	}
}
