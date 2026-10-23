package main

// The applier must not authorize an address on the producer's word alone: a device that left and never came
// back leaves no durable-state evidence, but DHCP (which assigned the address) does.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// fakeLeases stages DHCP evidence; nil error with no rows means "nobody holds anything", unlike a server that could not be asked.
type fakeLeases struct {
	rows []KeaLease
	err  error
}

func (f fakeLeases) Leases() ([]KeaLease, error) { return f.rows, f.err }

// switchableLeases lets a failure be turned on and off between passes.
type switchableLeases struct {
	rows []KeaLease
	err  error
}

func (f *switchableLeases) Leases() ([]KeaLease, error) { return f.rows, f.err }

// lease is a lease held now: stamped at this instant, since it must also not have lapsed.
func lease(ip, mac string) KeaLease {
	return KeaLease{IPAddress: ip, HWAddr: mac, ValidLft: 600, State: 0, CLTT: time.Now().Unix()}
}

func TestAddressOwner_LeasedToThisDeviceIsOwned(t *testing.T) {
	o := addressOwner{src: fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressOwned {
		t.Fatalf("ownership = %v, want owned", got)
	}
	// MAC case must not decide the answer.
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:F9:7A:9B:09"); got != addressOwned {
		t.Fatalf("case-different MAC = %v, want owned", got)
	}
}

// The address is live and belongs to somebody else.
func TestAddressOwner_ReassignedToAnotherDeviceIsForeign(t *testing.T) {
	o := addressOwner{src: fakeLeases{rows: []KeaLease{lease("192.168.77.139", "d6:5a:1c:d2:12:d6")}}}
	if got := o.Owns(context.Background(), net.ParseIP("192.168.77.139"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("ownership = %v, want foreign — this is the case that put a stranger on a guest's account", got)
	}
}

// No live lease at all: nobody holds the address, so this session does not either.
func TestAddressOwner_NoLeaseIsForeign(t *testing.T) {
	o := addressOwner{src: fakeLeases{rows: []KeaLease{lease("10.0.0.9", "aa:bb:cc:dd:ee:ff")}}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("ownership = %v, want foreign", got)
	}
}

// An expired or released lease is history.
func TestAddressOwner_AnExpiredLeaseIsNotOwnership(t *testing.T) {
	expired := lease("10.0.0.5", "96:48:f9:7a:9b:09")
	expired.State = keaLeaseStateExpired
	o := addressOwner{src: fakeLeases{rows: []KeaLease{expired}}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("an expired lease read as %v, want foreign", got)
	}
}

// An evidence outage is not evidence: Kea being unreachable must never disconnect every guest.
func TestAddressOwner_UnreadableEvidenceIsUnknown(t *testing.T) {
	o := addressOwner{src: fakeLeases{err: errors.New("control socket unavailable")}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressUnknown {
		t.Fatalf("ownership = %v, want unknown", got)
	}
	// A plan carrying no MAC is also unknown.
	o2 := addressOwner{src: fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}}
	if got := o2.Owns(context.Background(), net.ParseIP("10.0.0.5"), ""); got != addressUnknown {
		t.Fatalf("a session with no MAC read as %v, want unknown", got)
	}
}

func ownershipWriter(t *testing.T, tc *fakeTC, g *fakeGate, src leaseSource) *phase3Shaping {
	t.Helper()
	p := liveWriter(tc)
	p.gate = g
	p.owner = addressOwner{src: src}
	return p
}

func sessionOn(ip, mac string) shapeplan.Session {
	return shapeplan.Session{SessionID: "s-1", DeviceID: "dev-1", IP: ip, MAC: mac, Bridge: "br-guest",
		EntitlementID: "ent-1", DownKbps: 9000, UpKbps: 4000, Entitled: true}
}

// A foreign address is withdrawn immediately; the bounded nft lease is only the backstop for netd dying.
func TestApplier_ForeignAddressIsRevokedAndTornDown(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "d6:5a:1c:d2:12:d6")}})

	res, err := p.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 {
		t.Fatalf("a session on somebody else's address was shaped: %+v", res)
	}
	if g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("the address is still authorized after DHCP said it belongs to another device")
	}
	if fwd, _ := tc.SessionForwarding(context.Background(), "br-guest", net.ParseIP("10.0.0.5")); fwd {
		t.Fatal("the accountable class is still forwarding for an address this session does not own")
	}
}

// Unknown ownership withholds the renewal (durable state alone must not renew indefinitely) but tears nothing
// down: the installed bounded lease runs out on its own, tolerating a brief Kea outage.
func TestApplier_UnknownOwnershipWithholdsRenewalWithoutEndingAnything(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	src := &switchableLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	p := ownershipWriter(t, tc, g, src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")

	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}
	before := g.leaseOf("br-guest", "10.0.0.5")
	if before <= 0 {
		t.Fatal("setup: no lease was installed")
	}

	// Kea goes away: the pass must not renew or revoke, and must say so.
	src.err = errors.New("kea down")
	g.advance(30 * time.Second)
	res, err := p.submit(context.Background(), envelope(2, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 {
		t.Fatalf("a session was counted as shaped while its ownership was unverifiable: %+v", res)
	}
	if !g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("the guest was cut off the moment DHCP could not be asked — the bounded lease is the tolerance")
	}
	if after := g.leaseOf("br-guest", "10.0.0.5"); after >= before {
		t.Fatalf("the lease was renewed anyway (%s -> %s): durable state alone must not renew an address",
			before, after)
	}
	if p.unverifiedCount() != 1 {
		t.Fatalf("unverified sessions = %d, want 1 — the state must be visible, not merely absent",
			p.unverifiedCount())
	}
	var reported bool
	for _, msg := range res.Problems {
		if contains(msg, "NOT renewed") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("the withheld renewal was not reported: %+v", res.Problems)
	}
}

// Evidence returning before expiry resumes normal renewal.
func TestApplier_OwnershipConfirmedBeforeExpiryResumesRenewal(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	src := &switchableLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	p := ownershipWriter(t, tc, g, src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")

	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}
	src.err = errors.New("kea down")
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if p.unverifiedCount() != 1 {
		t.Fatalf("setup: the session was not marked unverified")
	}

	src.err = nil
	g.advance(30 * time.Second)
	res, err := p.submit(context.Background(), envelope(3, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 {
		t.Fatalf("renewal did not resume once ownership was confirmed again: %+v", res)
	}
	if p.unverifiedCount() != 0 {
		t.Fatalf("the session is still marked unverified after confirmation")
	}
	if got := g.leaseOf("br-guest", "10.0.0.5"); got < 60*time.Second {
		t.Fatalf("lease = %s after a confirming pass, want a full-length renewal", got)
	}
}

func TestApplier_OwnedAddressIsEnforcedNormally(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}})

	res, err := p.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 || res.Failed != 0 {
		t.Fatalf("a verified guest was not enforced: %+v", res)
	}
	if !g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("a verified guest holds no authorization")
	}
}

// A fresh applier re-verifies ownership on its first pass; a stale address must not survive a restart.
func TestApplier_ForeignAddressIsStillRefusedAfterRestart(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	first := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}})
	if res, err := first.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}

	tc2, g2 := newFakeTC(), newFakeGate()
	second := ownershipWriter(t, tc2, g2, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "d6:5a:1c:d2:12:d6")}})
	res, err := second.submit(context.Background(), envelope(2, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || g2.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("a restarted applier authorized a reassigned address: %+v", res)
	}
}

// A lapsed lease Kea still carries (state 0 until reclaimed) is not ownership.
func TestOwnership_ALeaseThatHasLapsedIsNotCurrent(t *testing.T) {
	stale := KeaLease{IPAddress: "192.168.77.102", HWAddr: "96:48:f9:7a:9b:09", ValidLft: 600, State: 0,
		CLTT: time.Now().Add(-72 * time.Hour).Unix()}
	o := addressOwner{src: fakeLeases{rows: []KeaLease{stale}}}

	if got := o.Owns(context.Background(), net.ParseIP("192.168.77.102"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("a lease three days past its lifetime was read as %v, want foreign", got)
	}
	fresh := stale
	fresh.CLTT = time.Now().Unix()
	o2 := addressOwner{src: fakeLeases{rows: []KeaLease{fresh}}}
	if got := o2.Owns(context.Background(), net.ParseIP("192.168.77.102"), "96:48:f9:7a:9b:09"); got != addressOwned {
		t.Fatalf("a lease renewed a moment ago was read as %v, want owned", got)
	}
}

// The boundary is the lease's own expiry, against a controlled clock.
func TestOwnership_CurrencyIsDecidedAtTheLeaseExpiry(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	l := KeaLease{IPAddress: "10.0.0.5", HWAddr: "96:48:f9:7a:9b:09", ValidLft: 600, State: 0, CLTT: base.Unix()}

	for _, tc := range []struct {
		at   time.Time
		want addressOwnership
		note string
	}{
		{at: base.Add(599 * time.Second), want: addressOwned, note: "one second before expiry"},
		{at: base.Add(601 * time.Second), want: addressForeign, note: "one second after expiry"},
	} {
		at := tc.at
		o := addressOwner{src: fakeLeases{rows: []KeaLease{l}}, now: func() time.Time { return at }}
		if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.note, got, tc.want)
		}
	}
}
