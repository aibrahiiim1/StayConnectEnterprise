package pmsd

import (
	"context"
	"net"
	"testing"
	"time"
)

// Audit P7 L2: the TCP connect runs under the revision's dial timeout, not only the TLS handshake. A dialer that never
// connects (a black-holed host) must return within the bound, coded as a dial failure.
func TestFIASDial_TCPConnectHonoursDialTimeout(t *testing.T) {
	blackHole := func(ctx context.Context, network, address string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dial := NewFIASDial(blackHole, testKeys(), time.Now, nil)
	rev := testRev()
	rev.DialTimeout = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := dial(context.Background(), DialParams{Iface: iface("i1"), Rev: rev})
		done <- err
	}()
	select {
	case err := <-done:
		if Classify(err) != CodeDialFailed {
			t.Fatalf("code = %q, want DIAL_FAILED", Classify(err))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dial did not honour the revision's dial timeout")
	}
}
