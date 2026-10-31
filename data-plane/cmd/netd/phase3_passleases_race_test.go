package main

// passLeases is reachable from more than one goroutine (the continuity governor installs one on the shared address
// owner while a request handler reads that owner). Its lazily built index and its one-fetch state must tolerate that:
// before the lock, the first LeasesForIP could assign into a map another goroutine had just reset, which panicked netd
// ("assignment to entry in nil map") at minute 43 of a lab outage run. Run with -race.

import (
	"fmt"
	"sync"
	"testing"
)

func TestPassLeasesConcurrentUseIsSafe(t *testing.T) {
	rows := make([]KeaLease, 0, 64)
	for i := 0; i < 64; i++ {
		rows = append(rows, KeaLease{IPAddress: fmt.Sprintf("10.0.0.%d", i+10), HWAddr: fmt.Sprintf("02:00:00:00:00:%02x", i)})
	}
	for iter := 0; iter < 300; iter++ {
		l := &passLeases{src: fakeLeases{rows: rows}}
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					if _, err := l.Leases(); err != nil {
						t.Errorf("Leases: %v", err)
						return
					}
					got, ok := l.LeasesForIP(fmt.Sprintf("10.0.0.%d", (g+i)%64+10))
					if !ok || len(got) != 1 {
						t.Errorf("LeasesForIP: ok=%v rows=%d", ok, len(got))
						return
					}
				}
			}(g)
		}
		wg.Wait()
	}
}
