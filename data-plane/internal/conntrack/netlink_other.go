//go:build !linux

package conntrack

import (
	"context"
	"errors"
	"net"
)

// netlinkFlush always fails off Linux so the flusher reports an error rather than a silent success.
func netlinkFlush(ctx context.Context, ips []net.IP) (int, error) {
	return 0, errors.New("conntrack netlink is only available on Linux")
}
