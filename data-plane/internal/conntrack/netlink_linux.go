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

// netlinkFlush deletes every IPv4 conntrack entry naming one of ips. It acts in the calling thread's network
// namespace, which the netns test script relies on.
func netlinkFlush(ctx context.Context, ips []net.IP) (int, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_NETFILTER)
	if err != nil {
		return 0, fmt.Errorf("netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return 0, fmt.Errorf("netlink bind: %w", err)
	}
	// Bounded receive so an unresponsive kernel cannot hang a teardown pass.
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
	buf := make([]byte, 1<<16)
	// Only the entries naming one of ips are kept while reading, not the whole table (audit Phase 8 L10). A dump the
	// kernel marks interrupted (NLM_F_DUMP_INTR) may have missed entries, so it is repeated, keeping what each pass
	// found, up to dumpAttempts; a dump still interrupted after that is reported, after deleting what was found.
	const dumpAttempts = 3
	var matched []entry
	seen := map[string]bool{}
	interrupted := true
	for attempt := 0; attempt < dumpAttempts && interrupted; attempt++ {
		interrupted = false
		seq++
		if err := syscall.Sendto(fd, buildDumpRequest(seq), 0, kernel); err != nil {
			return 0, fmt.Errorf("conntrack dump request: %w", err)
		}
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
			interrupted = interrupted || r.interrupted
			for _, e := range selectEntries(r.entries, ips) {
				if k := string(e.rawOrig) + "|" + string(e.rawZone); !seen[k] {
					seen[k] = true
					matched = append(matched, e)
				}
			}
			if r.done {
				break
			}
		}
	}

	deleted := 0
	var firstErr error
	if interrupted {
		firstErr = fmt.Errorf("conntrack dump: still interrupted by concurrent changes after %d attempts; some flows may remain", dumpAttempts)
	}
	for _, e := range matched {
		if err := ctx.Err(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		seq++
		if err := syscall.Sendto(fd, buildDeleteRequest(e, seq), 0, kernel); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("conntrack delete: %w", err)
			}
			continue
		}
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
