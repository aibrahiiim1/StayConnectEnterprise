package main

// A /19 client network (8,190 addresses). The sessions held at the HIGH end of the range, from the top address down, are all shaped with
// one flower filter per direction each, in the right class band, with no collision, and a steady pass changes nothing. The limit proven is
// ADDRESS CAPACITY and the shaper's naming of classes; it is not a claim that 8,190 clients are served at once.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

func TestSlash19HighEndSessionsAreShaped(t *testing.T) {
	defer shape.SetBridgeNetsForTest(map[string][]string{scaleBridge: {"10.20.0.0/19"}})()
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	const n = 6000
	for i := 0; i < n; i++ {
		s := scaleSession(i)
		host := 8190 - i // 10.20.31.254 down to 10.20.8.xx
		s.IP = fmt.Sprintf("10.20.%d.%d", host/256, host%256)
		e.sessions = append(e.sessions, s)
		e.leases.mu.Lock()
		e.leases.leases = append(e.leases.leases, KeaLease{IPAddress: s.IP, HWAddr: s.MAC, CLTT: e.k.clock().Unix(), ValidLft: 3600})
		e.leases.mu.Unlock()
	}
	admitAll(t, e, n)
	if got := e.activeCount(); got != n {
		t.Fatalf("%d of %d high-end sessions active", got, n)
	}
	k.mu.Lock()
	down, up := k.devs[scaleBridge], k.devs[shape.IFBName(scaleBridge)]
	flowers, ext, orig := 0, 0, 0
	for key := range down.filters {
		if key >= flowerKeyBase {
			flowers++
		}
	}
	for m := range down.classes {
		switch {
		case m >= shape.ExtMinorBase:
			ext++
		case m >= shape.GuestMinorBase && m <= shape.GuestMinorMax:
			orig++
		}
	}
	upFilters := len(up.filters)
	_, haveTop := down.classes[0x3000+8190-4096]
	k.mu.Unlock()
	if flowers != n || upFilters != n {
		t.Fatalf("flower filters: download %d, upload %d, want %d each", flowers, upFilters, n)
	}
	// host indexes 4096..8190 are the extension band (4095 hosts); the rest of the 6,000 are in the original band
	if wantExt := 8190 - 4096 + 1; ext != wantExt || orig != n-wantExt {
		t.Fatalf("classes: extension band %d (want %d), original band %d (want %d)", ext, wantExt, orig, n-wantExt)
	}
	if !haveTop {
		t.Fatal("the last host of the /19 (10.20.31.254) has no class")
	}
	e.tick(5 * time.Second)
	if st := e.pass(); st.res.Failed != 0 || st.cnt.TCClassChange != 0 {
		t.Fatalf("steady pass: failed=%d class changes=%d %v", st.res.Failed, st.cnt.TCClassChange, st.res.Problems)
	}
}
