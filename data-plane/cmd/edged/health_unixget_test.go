package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// D10-H1: every health call must release its connection; the throwaway transport used to keep one open per call.
func TestUnixGetLeavesNoConnectionOpen(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Int64
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }),
		ConnState: func(_ net.Conn, st http.ConnState) {
			switch st {
			case http.StateNew:
				open.Add(1)
			case http.StateClosed, http.StateHijacked:
				open.Add(-1)
			}
		},
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	for i := 0; i < 20; i++ {
		code, body, err := unixGet(context.Background(), sock, "/v1/health")
		if err != nil || code != 200 || string(body) != `{"status":"ok"}` {
			t.Fatalf("call %d: code=%d body=%q err=%v", i, code, body, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := open.Load(); n != 0 {
		t.Fatalf("%d server-side connections still open after 20 health calls", n)
	}
}
