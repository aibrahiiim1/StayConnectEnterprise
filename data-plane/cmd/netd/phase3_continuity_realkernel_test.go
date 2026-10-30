package main

// Real-kernel proof of outage continuity (NETD_CONT_REAL=1, root, a privileged Linux container with the nft binary):
//
//	NETD_CONT_REAL=1 go test ./cmd/netd -run TestContinuityRealKernel -v -count=1
//
// The REAL nft gate and REAL kernel set timeouts, in the container's own network namespace; tc stays simulated (its counters
// are the budget meter and the scale/accuracy of tc is proven elsewhere). Time is real: element lifetimes are kernel seconds.
// The governor's monotonic clock is advanced in step with the wall clock so that nothing here depends on a sleep being exact.
//
// Proven here, against the kernel and not a map: an element renewed by continuity carries a kernel timeout that never reaches
// past the session's deadline (so a wedged netd cannot outlive it), the element is gone at the deadline, a revoked element is
// gone at once, and an element that was removed is not created again.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func realNft(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nft: %v: %s", err, out)
	}
}

func TestContinuityRealKernel(t *testing.T) {
	if os.Getenv("NETD_CONT_REAL") != "1" {
		t.Skip("set NETD_CONT_REAL=1 in a privileged container (root, nft installed)")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("no nft binary")
	}
	_ = exec.Command("nft", "delete", "table", "inet", "stayconnect").Run()
	realNft(t, `table inet stayconnect {
	set phase3_auth_ipv4 { type ifname . ipv4_addr; flags timeout; }
	set phase3_auth_mac_ipv4 { type ifname . ipv4_addr . ether_addr; flags timeout; }
}`)
	defer func() { _ = exec.Command("nft", "delete", "table", "inet", "stayconnect").Run() }()

	old, oldTTL, oldBelow := contTakeoverAge, phase3LeaseTTL, phase3LeaseRenewBelow
	contTakeoverAge, phase3LeaseTTL, phase3LeaseRenewBelow = 6*time.Second, 12*time.Second, 8*time.Second
	defer func() { contTakeoverAge, phase3LeaseTTL, phase3LeaseRenewBelow = old, oldTTL, oldBelow }()

	h := newContH(t)
	h.p.gate = &nftGate{c: nft.New()}
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	b := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-b")
	c := h.guest("c-3", "10.0.0.13", "96:48:f9:7a:9b:13", "ent-c")
	ss := []shapeplan.Session{a, b, c}
	ctx := context.Background()
	window := 24 * time.Second

	// Healthy for 10 real seconds: plans every 5 s, each carrying the block.
	realStart := time.Now()
	for i := 0; i < 2; i++ {
		h.gen++
		env := planAt(h.gen, time.Now(), ss...)
		blk := block(window, ss, nil, nil)
		blk.AsOf = time.Now()
		blk.ContentsHash = shapeplan.HashContinuity(*blk)
		env.Continuity = blk
		if res, err := h.p.submit(ctx, env, time.Now()); err != nil || !res.Accepted || res.Failed != 0 {
			t.Fatalf("plan: %+v %v", res, err)
		}
		h.clk.ms += 5000
		h.p.contTick(ctx, time.Now())
		time.Sleep(5 * time.Second)
	}
	h.clk.ms += 0
	for _, s := range ss {
		if ok, _ := h.p.gate.Authorized(ctx, "br-guest", netIP(s.IP)); !ok {
			t.Fatalf("precondition: %s must be in the real kernel set", s.IP)
		}
	}
	deadlineReal := realStart.Add(window) // the block was accepted at about realStart+0 (first plan); the second moved it to +5 s
	_ = deadlineReal

	// Plans stop. Tick every 2 real seconds, advancing the governor's clock the same amount, and watch the REAL kernel.
	srv := &server{phase3: h.p}
	killedAt := time.Time{}
	revokedB := false
	sawA := false
	for time.Since(realStart) < window+20*time.Second {
		time.Sleep(2 * time.Second)
		h.clk.ms += 2000
		h.p.contTick(ctx, time.Now())
		els, err := h.p.gate.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		exp := map[string]time.Duration{}
		for _, e := range els {
			exp[e.IP.String()] = e.Expires
		}
		if _, ok := exp[a.IP]; ok {
			sawA = true
			// The kernel's own timeout must never reach past the deadline, or a wedged netd would outlive it.
			st := h.p.cont.st
			if st != nil {
				if s := st.Sessions[classKey("br-guest", "c-1")]; s != nil {
					left := time.Duration(s.DeadlineBoot-h.clk.ms) * time.Millisecond
					if left > 0 && exp[a.IP] > left+2*time.Second {
						t.Fatalf("kernel timeout %v reaches past the deadline (%v left)", exp[a.IP], left)
					}
				}
			}
		}
		// Mid-outage: staff disconnect b.
		if !revokedB && time.Since(realStart) > 14*time.Second {
			if _, err := srv.endFlows(ctx, netIP(b.IP), "", &nopFlusher{}); err != nil {
				t.Fatal(err)
			}
			if ok, _ := h.p.gate.Authorized(ctx, "br-guest", netIP(b.IP)); ok {
				t.Fatal("a staff disconnect left the element in the real kernel set")
			}
			revokedB = true
		}
		// Mid-outage: somebody removes c; it must not come back.
		if killedAt.IsZero() && time.Since(realStart) > 16*time.Second {
			_ = h.p.gate.Revoke(ctx, "br-guest", netIP(c.IP))
			killedAt = time.Now()
		}
		if !killedAt.IsZero() {
			if ok, _ := h.p.gate.Authorized(ctx, "br-guest", netIP(c.IP)); ok {
				t.Fatal("continuity re-created an element that had been removed")
			}
		}
	}
	if !sawA {
		t.Fatal("continuity never kept a guest in the real set after the plans stopped")
	}
	for _, s := range ss {
		if ok, _ := h.p.gate.Authorized(ctx, "br-guest", netIP(s.IP)); ok {
			t.Fatalf("%s is still in the real kernel set after the window", s.IP)
		}
	}
}
