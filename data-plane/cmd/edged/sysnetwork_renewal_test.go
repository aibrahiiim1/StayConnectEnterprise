package main

import (
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// P9 L6: confirming a system-network change starts at most one detached certificate renewal at a time.
func TestHotelAdminCertRenewalRunsOnceAtATime(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "scd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	var calls atomic.Int32
	release := make(chan struct{})
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hotel-admin-cert/renew" {
			calls.Add(1)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	s := &server{scd: newSCDClient(sock)}
	if !s.renewHotelAdminCertOnce() {
		t.Fatal("the first renewal must start")
	}
	if s.renewHotelAdminCertOnce() || s.renewHotelAdminCertOnce() {
		t.Fatal("a second renewal must not start while one runs")
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	for certRenewalRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("scd was asked to renew %d times, want 1", n)
	}
	if certRenewalRunning.Load() {
		t.Fatal("the running flag must clear when the renewal ends")
	}
}
