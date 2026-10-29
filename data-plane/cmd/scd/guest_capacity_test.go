package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// D2-L5 / D3-L5: guest requests beyond the cap are refused at once with 503 and Retry-After; admin routes are not capped.
func TestGuestCapacityRefusesBeyondTheCap(t *testing.T) {
	// The refusal comes after the queue wait; shorten it so the test does not idle.
	defer func(w time.Duration) { guestQueueWait = w }(guestQueueWait)
	guestQueueWait = 50 * time.Millisecond
	release := make(chan struct{})
	var started sync.WaitGroup
	h := guestCapacity(2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Done()
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	guest := "/v1/sessions/authorize"
	var wg sync.WaitGroup
	started.Add(2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, guest, nil))
		}()
	}
	started.Wait()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, guest, nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("third guest request: %d Retry-After=%q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	// An admin route is never refused by the guest cap.
	started.Add(1)
	adminDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/x", nil))
		adminDone <- rec.Code
	}()
	started.Wait()
	close(release)
	if code := <-adminDone; code != http.StatusOK {
		t.Fatalf("admin route: %d", code)
	}
	wg.Wait()
}

// AD-26: 500 clients connecting AT ONCE (a burst of hundreds of short requests, far more than the in-flight cap) are all
// served: the requests beyond the cap queue briefly instead of being turned away, and none waits near the two-second bound.
func TestFiveHundredSimultaneousSignInsAreAllServed(t *testing.T) {
	const clients = 500
	var served, refused atomic.Int32
	var slowest atomic.Int64
	h := guestCapacity(maxGuestInFlight)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond) // a sign-in's database work
		w.WriteHeader(http.StatusOK)
	}))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			t0 := time.Now()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/authorize", nil))
			if d := time.Since(t0).Milliseconds(); d > slowest.Load() {
				slowest.Store(d)
			}
			if rec.Code == http.StatusOK {
				served.Add(1)
			} else {
				refused.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if served.Load() != clients || refused.Load() != 0 {
		t.Fatalf("served %d, refused %d of %d simultaneous sign-ins", served.Load(), refused.Load(), clients)
	}
	if slowest.Load() > 1500 {
		t.Fatalf("the slowest sign-in waited %d ms in the queue; the bound is %v", slowest.Load(), guestQueueWait)
	}
}
