package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// P4 L7: a 1 MB body on a guest route is refused by the decoder; an admin route keeps room for licences and ledger batches.
func TestBodyLimitCapsGuestBodies(t *testing.T) {
	var decodeErr error
	h := bodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		decodeErr = json.NewDecoder(r.Body).Decode(&v)
	}))
	big := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), 1<<20)...)
	big = append(big, []byte(`"}`)...)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/sessions/authorize", bytes.NewReader(big)))
	if decodeErr == nil {
		t.Fatal("a 1 MB guest body was decoded")
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/ledger/apply", bytes.NewReader(big)))
	if decodeErr != nil {
		t.Fatalf("a 1 MB admin body was refused: %v", decodeErr)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/sessions/authorize", bytes.NewReader([]byte(`{"voucher":"ABC"}`))))
	if decodeErr != nil {
		t.Fatalf("a small guest body was refused: %v", decodeErr)
	}
}
