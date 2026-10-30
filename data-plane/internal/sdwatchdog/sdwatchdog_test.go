package sdwatchdog

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestIntervalIsAThirdAndOffWithoutConfiguration(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if Interval(get) != 0 {
		t.Fatal("no WATCHDOG_USEC must mean no watchdog")
	}
	env["WATCHDOG_USEC"] = "60000000"
	if got := Interval(get); got != 20*time.Second {
		t.Fatalf("interval = %v, want 20s", got)
	}
	env["WATCHDOG_PID"] = strconv.Itoa(os.Getpid() + 1)
	if Interval(get) != 0 {
		t.Fatal("a watchdog addressed to another process must be ignored")
	}
}

func TestPingStopsWhenTheLoopIsNotAlive(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "n.sock")
	l, err := net.ListenPacket("unixgram", sock)
	if err != nil {
		t.Skip("unixgram unavailable:", err)
	}
	defer l.Close()
	var pings atomic.Int32
	go func() {
		buf := make([]byte, 64)
		for {
			n, _, err := l.ReadFrom(buf)
			if err != nil {
				return
			}
			if string(buf[:n]) == "WATCHDOG=1" {
				pings.Add(1)
			}
		}
	}()
	var alive atomic.Bool
	alive.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go run(ctx, 10*time.Millisecond, sock, alive.Load)
	time.Sleep(100 * time.Millisecond)
	if pings.Load() < 3 {
		t.Fatalf("expected pings while alive, got %d", pings.Load())
	}
	alive.Store(false)
	time.Sleep(30 * time.Millisecond)
	before := pings.Load()
	time.Sleep(100 * time.Millisecond)
	if pings.Load() != before {
		t.Fatalf("pings continued after the loop stopped being alive (%d -> %d)", before, pings.Load())
	}
}
