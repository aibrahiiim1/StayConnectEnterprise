//go:build linux

package conntrack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// netlinkFlush dumps the IPv4 conntrack table once and deletes every entry naming one of ips.
//
// It runs in the network namespace of the calling thread, which is what lets the netns test script drive it with
// `ip netns exec <router> netd flows-end ...` against a disposable router namespace.
func netlinkFlush(ctx context.Context, ips []net.IP) (int, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_NETFILTER)
	if err != nil {
		return 0, fmt.Errorf("netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return 0, fmt.Errorf("netlink bind: %w", err)
	}
	// A bounded receive, so a kernel that never answers cannot hang a teardown pass.
	deadline := 5 * time.Second
	if d, ok := ctx.Deadline(); ok {
		if rem := time.Until(d); rem > 0 && rem < deadline {
			deadline = rem
		}
	}
	tv := syscall.NsecToTimeval(deadline.Nanoseconds())
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1<<20)

	kernel := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	seq := uint32(time.Now().UnixNano())
	if err := syscall.Sendto(fd, buildDumpRequest(seq), 0, kernel); err != nil {
		return 0, fmt.Errorf("conntrack dump request: %w", err)
	}
	buf := make([]byte, 1<<16)
	var all []entry
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return 0, fmt.Errorf("conntrack dump: %w", err)
		}
		r := parseMessages(buf[:n])
		if r.err != nil {
			return 0, fmt.Errorf("conntrack dump: %w", r.err)
		}
		all = append(all, r.entries...)
		if r.done {
			break
		}
	}

	deleted := 0
	var firstErr error
	for _, e := range selectEntries(all, ips) {
		seq++
		if err := syscall.Sendto(fd, buildDeleteRequest(e, seq), 0, kernel); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("conntrack delete: %w", err)
			}
			continue
		}
		// One ACK (or error) per request.
		for {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				if firstErr == nil {
					firstErr = fmt.Errorf("conntrack delete ack: %w", err)
				}
				break
			}
			r := parseMessages(buf[:n])
			switch {
			case r.err == nil:
				deleted++
			case errors.Is(r.err, enoent):
				// already gone
			default:
				if firstErr == nil {
					firstErr = fmt.Errorf("conntrack delete: %w", r.err)
				}
			}
			break
		}
	}
	return deleted, firstErr
}
