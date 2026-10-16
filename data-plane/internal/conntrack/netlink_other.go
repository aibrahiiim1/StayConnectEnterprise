//go:build !linux

package conntrack

import (
	"context"
	"errors"
	"net"
)

// netlinkFlush has no implementation off Linux: there is no conntrack table to flush. The System flusher then
// tries the tool, which is not there either, and reports both failures -- never a silent success.
func netlinkFlush(ctx context.Context, ips []net.IP) (int, error) {
	return 0, errors.New("conntrack netlink is only available on Linux")
}
