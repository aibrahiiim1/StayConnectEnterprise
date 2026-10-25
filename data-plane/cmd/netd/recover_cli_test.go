package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serveOnSocket(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "n.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets are not available here: %v", err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestTheRecoverCommandReportsAndFailsWhenTheApplianceIsNotBackOnItsConfirmedConfiguration(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"service":"netd"}`)) })
	converged := false
	mux.HandleFunc("/v1/reassert-confirmed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if converged {
			_, _ = w.Write([]byte(`{"converged":true,"changes_refused":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"converged":false,"changes_refused":true,"detail":"kea refused"}`))
	})
	t.Setenv("NETD_SOCKET", serveOnSocket(t, mux))

	var out, errOut bytes.Buffer
	if code := runRecoverCommand([]string{"status"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "netd") {
		t.Fatalf("status = %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runRecoverCommand([]string{"reassert-confirmed"}, &out, &errOut); code != 1 || !strings.Contains(out.String(), "kea refused") {
		t.Fatalf("an unconverged reassert exited %d: %s", code, out.String())
	}
	converged = true
	if code := runRecoverCommand([]string{"reassert-confirmed"}, &out, &errOut); code != 0 {
		t.Fatalf("a converged reassert exited %d", code)
	}
	if code := runRecoverCommand([]string{"restore-factory-defaults"}, &out, &errOut); code != 2 {
		t.Fatalf("an operation the local tool does not offer exited %d", code)
	}
}

func TestTheRecoverCommandSaysSoWhenTheServiceIsNotRunning(t *testing.T) {
	t.Setenv("NETD_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	var out, errOut bytes.Buffer
	if code := runRecoverCommand([]string{"status"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "systemctl start") {
		t.Fatalf("%d %q", code, errOut.String())
	}
}
