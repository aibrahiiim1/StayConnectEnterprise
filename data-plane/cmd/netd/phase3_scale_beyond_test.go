package main

// One guest bridge holds more than 2,047 shaped sessions: the classification no longer uses one u32 priority per session, and a bridge wider than /20
// names its classes in the extension band. These run the REAL reconcile and shape client against the simulated kernel (which refuses the 2,048th u32
// priority on a device exactly as the real one does; the real-kernel proof is TestScaleTable with NETD_SCALE_REAL=1).

import (
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

func admitAll(t *testing.T, e *scaleEnv, n int) {
	t.Helper()
	for i := 0; i < 40 && e.activeCount() < n; i++ {
		e.pass()
		e.tick(5 * time.Second)
	}
}

func TestMoreThan2047SessionsOnOneBridgeAreShaped(t *testing.T) {
	defer shape.SetBridgeNetsForTest(map[string][]string{scaleBridge: {"10.20.0.0/17"}})()
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	const n = 4200 // crosses 2,047 and also the 4,096 hosts of the original class band
	e.addSessions(0, n)
	admitAll(t, e, n)
	if got := e.activeCount(); got != n {
		t.Fatalf("%d of %d sessions active", got, n)
	}
	k.mu.Lock()
	down, up := k.devs[scaleBridge], k.devs[shape.IFBName(scaleBridge)]
	flowers, hi := 0, 0
	for key := range down.filters {
		if key >= flowerKeyBase {
			flowers++
		}
	}
	for m := range down.classes {
		if m >= shape.ExtMinorBase {
			hi++
		}
	}
	upFilters := len(up.filters)
	k.mu.Unlock()
	if flowers != n || upFilters != n {
		t.Fatalf("flower filters: download %d, upload %d, want %d each", flowers, upFilters, n)
	}
	if want := n + 2 - 4096; hi != want { // sessions are hosts 2..n+1; hosts 4096 and up are in the extension band
		t.Fatalf("extension-band classes = %d, want %d: a /17 bridge was shaped with the original 12-bit rule", hi, want)
	}
	// a steady pass over all of them changes nothing
	e.tick(5 * time.Second)
	st := e.pass()
	if st.res.Failed != 0 || st.cnt.TCClassChange != 0 {
		t.Fatalf("steady pass: failed=%d class changes=%d %v", st.res.Failed, st.cnt.TCClassChange, st.res.Problems)
	}
}

// A kernel without cls_flower keeps the original behaviour, limit included: the sessions beyond 2,047 fail closed and are reported, nothing is
// shaped wrongly.
func TestWithoutFlowerTheOriginalLimitStillFailsClosed(t *testing.T) {
	k := newKernelFake(time.Now())
	k.noFlower = true
	e := newScaleEnv(t, k)
	e.addSessions(0, 2060)
	for i := 0; i < 4; i++ {
		e.pass()
		e.tick(5 * time.Second)
	}
	if got := e.activeCount(); got != 2047 {
		t.Fatalf("active = %d, want exactly the 2,047 the u32 scheme can hold", got)
	}
}

// Sessions classified by the previous netd (one u32 filter per pref) are still recognised and are replaced cleanly when they end.
func TestSessionsFromThePreviousClassifierAreReleasedAndReplaced(t *testing.T) {
	k := newKernelFake(time.Now())
	e := newScaleEnv(t, k)
	e.addSessions(0, 5)
	admitAll(t, e, 5)
	// rewrite what the kernel holds into the old form: u32 filters keyed by pref
	k.mu.Lock()
	for _, d := range k.devs {
		old := map[int]string{}
		for key, flow := range d.filters {
			if key >= flowerKeyBase {
				old[key-flowerKeyBase] = flow
			} else {
				old[key] = flow
			}
		}
		d.filters = old
	}
	k.mu.Unlock()
	e.tick(5 * time.Second)
	if st := e.pass(); st.res.Failed != 0 || st.res.Shaped != 5 {
		t.Fatalf("legacy-classified sessions must be recognised as forwarding: shaped=%d failed=%d %v", st.res.Shaped, st.res.Failed, st.res.Problems)
	}
	// end them all: the legacy filters must be removed so the classes can go
	e.sessions = nil
	for i := 0; i < 3; i++ {
		e.tick(5 * time.Second)
		e.pass()
	}
	k.mu.Lock()
	left := len(k.devs[scaleBridge].classes) + len(k.devs[scaleBridge].filters)
	k.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d classes/filters left behind after the legacy sessions ended", left)
	}
}
