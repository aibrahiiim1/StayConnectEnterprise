// Package sdwatchdog lets a daemon prove to systemd that its work loop is still turning.
//
// A crashed daemon is restarted by Restart=always within seconds; a daemon that is alive but stuck (a database call
// that never returns, a lock never released) is not, and for acctd every second stuck is a second the authorization
// plan is not renewed. With WatchdogSec set, systemd restarts the unit when no WATCHDOG=1 arrives in time. The ping is
// withheld the moment the liveness check fails, so a stall ends in a restart instead of lasting until someone notices.
//
// Without WATCHDOG_USEC (not run by systemd, or the unit sets no WatchdogSec) nothing is sent.
package sdwatchdog

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"
)

// Interval returns a third of the watchdog period from the environment, or 0 when no watchdog is configured.
func Interval(getenv func(string) string) time.Duration {
	us, err := strconv.ParseInt(getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || us <= 0 {
		return 0
	}
	if pid := getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	return time.Duration(us) * time.Microsecond / 3
}

// Run pings systemd every interval while alive() is true, until ctx ends. It returns at once when no watchdog is configured.
func Run(ctx context.Context, alive func() bool) {
	every := Interval(os.Getenv)
	sock := os.Getenv("NOTIFY_SOCKET")
	if every <= 0 || sock == "" {
		return
	}
	run(ctx, every, sock, alive)
}

func run(ctx context.Context, every time.Duration, sock string, alive func() bool) {
	if sock[0] == '@' { // abstract socket
		sock = "\x00" + sock[1:]
	}
	conn, err := net.Dial("unixgram", sock)
	if err != nil {
		return
	}
	defer conn.Close()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if alive() {
			_, _ = conn.Write([]byte("WATCHDOG=1"))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
