package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A long path has no client deadline that cuts it to the default; a path not on the list is still held to the default.
func TestLongSCDPathsOutliveTheDefaultDeadline(t *testing.T) {
	dir, err := os.MkdirTemp("", "scdt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	oldDefault := scdDefaultTimeout
	scdDefaultTimeout = 200 * time.Millisecond
	longSCDPaths["/v1/test/long"] = 5 * time.Second
	defer func() { scdDefaultTimeout = oldDefault; delete(longSCDPaths, "/v1/test/long") }()

	c := newSCDClient(sock)
	if code, _, err := c.call(context.Background(), http.MethodGet, "/v1/test/long", nil); err != nil || code != 200 {
		t.Fatalf("a long path was cut short: %d %v", code, err)
	}
	if _, _, err := c.call(context.Background(), http.MethodGet, "/v1/test/short", nil); err == nil {
		t.Fatal("a path that is not listed as long was not held to the default deadline")
	}
}
