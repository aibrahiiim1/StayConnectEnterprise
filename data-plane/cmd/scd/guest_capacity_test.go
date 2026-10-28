package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// D2-L5 / D3-L5: guest requests beyond the cap are refused at once with 503 and Retry-After; admin routes are not capped.
func TestGuestCapacityRefusesBeyondTheCap(t *testing.T) {
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
