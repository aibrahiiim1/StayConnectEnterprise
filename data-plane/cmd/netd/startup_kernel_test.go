//go:build netdkernel && linux

package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// The real binary, started with a database that refuses connections: it must stay up, rebuild the last-confirmed network from the snapshot, and refuse changes.
func TestKernel_StartupWithTheDatabaseDownRestoresFromTheSnapshot(t *testing.T) {
	requireDisposableKernel(t)
	sh(t, "ip", "link", "add", "dums0", "type", "dummy")
	sh(t, "ip", "link", "set", "dums0", "up")
	t.Cleanup(func() {
		for _, n := range []string{"br-g73", "dums0.73", "dums0"} {
			_ = exec.Command("ip", "link", "del", n).Run()
		}
	})
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	a := &applier{generatedDir: gen}
	n := kernelNet("33333333-3333-3333-3333-333333333373", "dums0", 73, "br-g73")
	if err := a.saveLastConfirmed("rev-snap", "", "", []netcfg.GuestNetwork{n}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "netd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	sock := filepath.Join(dir, "netd.sock")
	cmd := exec.Command(bin)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	cmd.Env = append(os.Environ(),
		"NETD_SOCKET="+sock, "NETD_DB_URL=postgres://x:x@127.0.0.1:1/x?sslmode=disable&connect_timeout=1",
		"NETD_GENERATED_DIR="+gen, "NETD_NETPLAN_FILE="+filepath.Join(dir, "np.yaml"),
		"NETD_UNBOUND_FRAG="+filepath.Join(dir, "u.conf"), "NETD_UNBOUND_PORTAL_FRAG="+filepath.Join(dir, "up.conf"),
		"NETD_KEA_SOCKET="+filepath.Join(dir, "kea.sock"), "NETD_KEA_CONF="+filepath.Join(dir, "kea.conf"),
		"NETD_KEA_LEASE_CSV="+filepath.Join(dir, "leases.csv"), "NETD_ADMIN_CALLERS=root",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	deadline := time.Now().Add(90 * time.Second)
	for !ifaceExists("br-g73") {
		select {
		case err := <-exited:
			t.Fatalf("netd exited with the database down (%v):\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the snapshot's network was never rebuilt:\n%s", logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}

	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	var body string
	for end := time.Now().Add(60 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if resp, err := hc.Get("http://netd/v1/health"); err == nil {
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(resp.Body)
			resp.Body.Close()
			body = buf.String()
			break
		}
	}
	if !strings.Contains(body, "rev-snap") || !strings.Contains(body, `"incomplete":true`) {
		t.Fatalf("health does not report the provisional, blocked state: %q\n%s", body, logs.String())
	}
	resp, err := hc.Post("http://netd/v1/apply", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	refused := new(bytes.Buffer)
	_, _ = refused.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 400 || !strings.Contains(refused.String(), "database is unreachable") {
		t.Fatalf("a change was not refused while running from the snapshot: %d %s", resp.StatusCode, refused.String())
	}
	select {
	case err := <-exited:
		t.Fatalf("netd died after restoring: %v\n%s", err, logs.String())
	default:
	}
}
