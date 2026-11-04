package ledger

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

// Each way reading a server's key can fail is named for what the operator must fix, never one catch-all "unreachable".
func TestProbeHostKeySaysWhyTheKeyCouldNotBeRead(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			_ = c.Close()
		}
	}()
	notSSHPort := ln.Addr().(*net.TCPAddr).Port

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	defer ln.Close()

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	silentPort := silent.Addr().(*net.TCPAddr).Port

	for _, c := range []struct {
		host string
		port int
		kind string
	}{
		{"127.0.0.1", notSSHPort, ProbeNotSSH},
		{"127.0.0.1", refusedPort, ProbeRefused},
		{"no-such-host.invalid", 22, ProbeNotFound},
		{"127.0.0.1", silentPort, ProbeTimeout},
	} {
		d := &SFTPDest{Host: c.host, Port: c.port, Timeout: 2 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := d.ProbeHostKey(ctx)
		cancel()
		var pe *ProbeError
		if !errors.As(err, &pe) || pe.Kind != c.kind {
			t.Fatalf("%s:%s: got %v, want kind %s", c.host, strconv.Itoa(c.port), err, c.kind)
		}
	}
}
