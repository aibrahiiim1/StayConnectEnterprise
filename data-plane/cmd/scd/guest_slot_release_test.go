package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Capacity acceptance 2026-10-30: the sign-in waits up to eight seconds for the kernel to confirm the guest. Counted against the 64 in-flight guest slots,
// that wait starved every other guest request. A handler that is only waiting hands its slot back (releaseGuestSlot); the others are served meanwhile.
func TestAWaitingHandlerHandsBackItsGuestSlot(t *testing.T) {
	defer func(w time.Duration) { guestQueueWait = w }(guestQueueWait)
	guestQueueWait = 40 * time.Millisecond
	released := make(chan struct{})
	finish := make(chan struct{})
	h := guestCapacity(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions/activate" {
			releaseGuestSlot(r.Context())
			releaseGuestSlot(r.Context()) // idempotent
			close(released)
			<-finish // the long wait
		}
		w.WriteHeader(http.StatusOK)
	}))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/sessions/activate", nil))
	}()
	<-released
	// The single slot is free although the first request is still running: a second guest request is served, not refused.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/authorize", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("a guest request while another only waits: %d, want 200", rec.Code)
	}
	close(finish)
	wg.Wait()
	// The slot was given back once only: with the first request over, capacity one still holds exactly one request at a time (a double release
	// would have made room for two).
	hold := make(chan struct{})
	in := make(chan struct{}, 2)
	h2 := guestCapacity(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in <- struct{}{}
		<-hold
	}))
	var wg2 sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/sessions/authorize", nil))
		}()
	}
	<-in
	select {
	case <-in:
		t.Fatal("two guest requests ran at once with a capacity of one: a slot was released twice")
	case <-time.After(100 * time.Millisecond):
	}
	close(hold)
	wg2.Wait()
}

// A handler outside a guest route (no slot) may call it freely.
func TestReleasingASlotThatWasNeverTakenIsHarmless(t *testing.T) {
	h := guestCapacity(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releaseGuestSlot(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/v1/health", "/v1/admin/x", "/v1/sessions/authorize"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	releaseGuestSlot(httptest.NewRequest(http.MethodGet, "/", nil).Context()) // no slot at all
}
